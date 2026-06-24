package tcp

import (
	"bytes"
	"testing"
	"time"
)

// waitStateSleep は receiver goroutine の往復処理を短いスリープで待ちつつ
// c が want になることを確認する。ループバックは 2 つの受信ループが交互に
// 進むため、CPU を握る busy-wait より sleep で譲る方が確実に収束する。
func waitStateSleep(t *testing.T, c *Conn, want State) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if c.State() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("状態が %v にならない: got %v", want, c.State())
}

// クライアント端点とサーバ端点。互いに local/remote が反転する。
var (
	lbClient = Endpoint{IP: [4]byte{10, 0, 0, 1}, Port: 40000}
	lbServer = Endpoint{IP: [4]byte{10, 0, 0, 2}, Port: 9000}
)

// 2 つの Conn を NewPipeLink の両端に置き、それぞれに受信ループを回して、
// 実際にバイトを流して 3way ハンドシェイク〜close を成立させる。
// 自作スタックが自分の受信ループを通して実際にプロトコルを喋れることの証明。
func TestLoopbackHandshakeAndClose(t *testing.T) {
	clientLink, serverLink := NewPipeLink()
	fc := newFakeClock()

	client := NewConn(clientLink, fc.Now, lbClient, lbServer)
	server := NewConn(serverLink, fc.Now, lbServer, lbClient)

	cr := newReceiver(client, clientLink, 65535)
	sr := newReceiver(server, serverLink, 65535)
	cr.Start()
	sr.Start()
	t.Cleanup(cr.Stop)
	t.Cleanup(sr.Stop)

	// passive open を先に張ってから active open で SYN を送る。
	server.PassiveOpen()
	client.ActiveOpen(1000)

	// 3way ハンドシェイク成立: 両側 ESTABLISHED。
	waitStateSleep(t, client, Established)
	waitStateSleep(t, server, Established)
	t.Logf("handshake done: client=%v server=%v", client.State(), server.State())

	// クライアントから能動 close → FIN 交換。
	client.Close()
	// server は FIN を受けて CLOSE-WAIT、client は FIN-WAIT-2 まで進む。
	waitStateSleep(t, server, CloseWait)
	waitStateSleep(t, client, FinWait2)
	t.Logf("client closed: client=%v server=%v", client.State(), server.State())

	// server も close → FIN 送出 (LAST-ACK)。client は TIME-WAIT へ。
	server.Close()
	waitStateSleep(t, client, TimeWait)
	waitStateSleep(t, server, Closed)
	t.Logf("server closed: client=%v server=%v", client.State(), server.State())

	// TIME-WAIT は 2MSL 満了で CLOSED。fake clock を進めて Tick で駆動する。
	fc.advance(timeWaitDuration)
	client.Tick()
	if client.State() != Closed {
		t.Fatalf("2MSL 満了後は CLOSED のはず: got %v", client.State())
	}
}

// recvAll は want と同じ長さになるまで Recv をポーリングして読み切る。
// 受信ループは別 goroutine が回しているので sleep で譲りながら待つ。
func recvAll(t *testing.T, c *Conn, want int) []byte {
	t.Helper()
	var got []byte
	buf := make([]byte, 4096)
	for i := 0; i < 2000 && len(got) < want; i++ {
		n, _ := c.Recv(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			continue
		}
		time.Sleep(time.Millisecond)
	}
	return got
}

// 握手後に実データを流し、相手側で送信順のバイト列が読めることを確認する。
// 小さいメッセージ・複数回 Send・MSS 超の大きいデータ (複数セグメント分割) を通す。
func TestLoopbackDataTransfer(t *testing.T) {
	clientLink, serverLink := NewPipeLink()
	fc := newFakeClock()

	client := NewConn(clientLink, fc.Now, lbClient, lbServer)
	server := NewConn(serverLink, fc.Now, lbServer, lbClient)

	cr := newReceiver(client, clientLink, 65535)
	sr := newReceiver(server, serverLink, 65535)
	cr.Start()
	sr.Start()
	t.Cleanup(cr.Stop)
	t.Cleanup(sr.Stop)

	server.PassiveOpen()
	client.ActiveOpen(1000)
	waitStateSleep(t, client, Established)
	waitStateSleep(t, server, Established)

	// 1) 小さいメッセージ。
	msg := []byte("hello world")
	if _, err := client.Send(msg); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	got := recvAll(t, server, len(msg))
	if !bytes.Equal(got, msg) {
		t.Fatalf("受信不一致: got %q want %q", got, msg)
	}
	t.Logf("小メッセージ受信 OK: %q", got)

	// 2) 複数回 Send + MSS 超の大きいデータ。連結して順序通りに届くこと。
	big := bytes.Repeat([]byte("0123456789"), defaultMSS/5) // > 2*MSS
	if _, err := client.Send(big); err != nil {
		t.Fatalf("Send (big) 失敗: %v", err)
	}
	gotBig := recvAll(t, server, len(big))
	if !bytes.Equal(gotBig, big) {
		t.Fatalf("大データ受信不一致: got %d bytes want %d", len(gotBig), len(big))
	}
	t.Logf("大データ受信 OK: %d バイト (MSS=%d なので複数セグメント)", len(gotBig), defaultMSS)
}

// 初期ウィンドウ (IW) を遥かに超える大データが、cwnd 制御下で ACK 駆動の
// ウィンドウ成長を経て全部・順序通りに届くことを確認する。
func TestLoopbackLargeTransferUnderCwnd(t *testing.T) {
	clientLink, serverLink := NewPipeLink()
	fc := newFakeClock()

	client := NewConn(clientLink, fc.Now, lbClient, lbServer)
	server := NewConn(serverLink, fc.Now, lbServer, lbClient)

	cr := newReceiver(client, clientLink, 65535)
	sr := newReceiver(server, serverLink, 65535)
	cr.Start()
	sr.Start()
	t.Cleanup(cr.Stop)
	t.Cleanup(sr.Stop)

	server.PassiveOpen()
	client.ActiveOpen(1000)
	waitStateSleep(t, client, Established)
	waitStateSleep(t, server, Established)

	// IW (=3*MSS=4080) の約 30 倍。一度に送り切れず ACK で cwnd が伸びて初めて完走する。
	big := bytes.Repeat([]byte("ABCDEFGHIJ"), 12*defaultMSS) // 約 120 KB
	if _, err := client.Send(big); err != nil {
		t.Fatalf("Send (large) 失敗: %v", err)
	}
	got := recvAll(t, server, len(big))
	if !bytes.Equal(got, big) {
		t.Fatalf("大量データ受信不一致: got %d bytes want %d", len(got), len(big))
	}
	t.Logf("cwnd 制御下の大量転送 OK: %d バイト全着", len(got))
}

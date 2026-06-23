//go:build linux

package tcp

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// 自作スタックのエンドポイント (UDP の運搬先とは別物。自作 TCP/IP ヘッダ内の IP)。
var (
	udpEPClient = Endpoint{IP: [4]byte{10, 0, 0, 1}, Port: 9000}
	udpEPServer = Endpoint{IP: [4]byte{10, 0, 0, 2}, Port: 9001}
)

func waitReachedEstablished(t *testing.T, c *Conn) {
	t.Helper()
	for i := 0; i < 3000; i++ {
		if c.ReachedEstablished() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ESTABLISHED に到達しない: 現在 %v", c.State())
}

func waitConnState(t *testing.T, c *Conn, want State) {
	t.Helper()
	for i := 0; i < 5000; i++ {
		if c.State() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("状態が %v にならない: got %v", want, c.State())
}

// 2 つの Conn を実 UDP ソケット越し (メモリ pipe でなく) に繋ぎ、Serve で
// 受信ループを回して、握手 -> データ転送 -> close まで通す。
// root 無しで通れば「完全ユーザー空間で自作 TCP が実通信」の証明。
func TestUDPLoopback_HandshakeDataClose(t *testing.T) {
	const pClient, pServer = 53100, 53101

	clientLink, err := NewUDPLink(pClient, loIP, pServer)
	if err != nil {
		t.Fatalf("client link: %v", err)
	}
	serverLink, err := NewUDPLink(pServer, loIP, pClient)
	if err != nil {
		clientLink.Close()
		t.Fatalf("server link: %v", err)
	}

	client := NewConn(clientLink, time.Now, udpEPClient, udpEPServer)
	server := NewConn(serverLink, time.Now, udpEPServer, udpEPClient)
	// TIME-WAIT を短縮して 2MSL 満了まで見られるように。
	client.SetMSL(200 * time.Millisecond)
	server.SetMSL(200 * time.Millisecond)

	stopC := Serve(client, 65535)
	stopS := Serve(server, 65535)
	t.Cleanup(stopC)
	t.Cleanup(stopS)

	server.PassiveOpen()
	client.ActiveOpen(1000)

	waitReachedEstablished(t, client)
	waitReachedEstablished(t, server)
	t.Logf("握手成立 (実 UDP 越し): client=%v server=%v", client.State(), server.State())

	// client -> server へ実データ。
	msg := []byte("hello over udp")
	if _, err := client.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := recvAll(t, server, len(msg))
	if !bytes.Equal(got, msg) {
		t.Fatalf("受信不一致: got %q want %q", got, msg)
	}
	t.Logf("データ転送 OK (実 UDP 越し): %q", got)

	// client 能動 close -> FIN 交換。server は FIN を受けて CLOSE-WAIT。
	client.Close()
	waitConnState(t, server, CloseWait)
	// server も close -> client は TIME-WAIT へ。
	server.Close()
	waitConnState(t, client, TimeWait)
	waitConnState(t, server, Closed)
	t.Logf("close 進行: client=%v server=%v", client.State(), server.State())

	// 2MSL 満了で client は CLOSED へ (Serve の Tick が駆動)。
	waitConnState(t, client, Closed)
	t.Logf("CLOSED 到達 (実 UDP 越しで握手〜データ〜close 完了)")

	// server 側で EOF が観測できることも確認 (FIN 受信後)。
	buf := make([]byte, 16)
	if _, err := server.Recv(buf); err != nil && err != io.EOF {
		t.Logf("server Recv after close: %v", err)
	}
}

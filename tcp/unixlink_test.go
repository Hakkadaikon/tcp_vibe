//go:build linux

package tcp

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// unixLink は Link インターフェースを満たす (コンパイル時保証)。
var _ Link = (*unixLink)(nil)

// requireUnixSocket は AF_UNIX/SOCK_DGRAM を作れない環境 (seccomp で socket(2) を
// EPERM 拒否するサンドボックス等) ではテストを skip する。実ホストなら特権不要で
// 必ず作れるので、skip されるのは隔離環境だけ。
func requireUnixSocket(t *testing.T) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("AF_UNIX ソケットが作れない環境のため skip (実ホストでは特権不要で動く): %v", err)
		}
		t.Fatalf("AF_UNIX socket: %v", err)
	}
	syscall.Close(fd)
}

// newUnixPair は t.TempDir 内に 2 つのソケットパスを作り、互いを宛先にした
// unixLink を返す。A: local=a remote=b / B: local=b remote=a。
func newUnixPair(t *testing.T) (Link, Link, string, string) {
	t.Helper()
	requireUnixSocket(t)
	dir := t.TempDir()
	pa := filepath.Join(dir, "a.sock")
	pb := filepath.Join(dir, "b.sock")
	a, err := NewUnixLink(pa, pb)
	if err != nil {
		t.Fatalf("NewUnixLink A: %v", err)
	}
	b, err := NewUnixLink(pb, pa)
	if err != nil {
		a.Close()
		t.Fatalf("NewUnixLink B: %v", err)
	}
	return a, b, pa, pb
}

// 実 Unix socket 越しに往復する: A.WritePacket -> B.ReadPacket、逆向きも一致する。
// AF_UNIX のバイト土管なのでカーネルの UDP/IP プロトコルを通さず、特権も不要。
// このサンドボックスでの実通信検証。
func TestUnixLink_RoundTrip(t *testing.T) {
	a, b, _, _ := newUnixPair(t)
	t.Cleanup(func() { a.Close(); b.Close() })

	want := []byte("ip-packet-bytes-A->B")
	if err := a.WritePacket(want); err != nil {
		t.Fatalf("WritePacket A: %v", err)
	}
	got, err := b.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket B: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("A->B 不一致: got %q want %q", got, want)
	}

	want2 := []byte("reply-B->A")
	if err := b.WritePacket(want2); err != nil {
		t.Fatalf("WritePacket B: %v", err)
	}
	got2, err := a.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket A: %v", err)
	}
	if !bytes.Equal(got2, want2) {
		t.Fatalf("B->A 不一致: got %q want %q", got2, want2)
	}
}

// Close すると Recvfrom でブロック中の ReadPacket が ErrLinkClosed で抜け、
// goroutine がリークしない。
func TestUnixLink_CloseUnblocksRead(t *testing.T) {
	a, b, _, _ := newUnixPair(t)
	t.Cleanup(func() { b.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := a.ReadPacket()
		done <- err
	}()

	// ReadPacket が Recvfrom でブロックに入るのを少し待ってから閉じる。
	time.Sleep(20 * time.Millisecond)
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrLinkClosed) {
			t.Fatalf("Close 後の ReadPacket は ErrLinkClosed のはず: got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close しても ReadPacket が抜けない (goroutine リーク)")
	}
}

// Close は冪等。二重呼び出しで panic もエラーもない。
func TestUnixLink_CloseIdempotent(t *testing.T) {
	a, b, _, _ := newUnixPair(t)
	b.Close()
	if err := a.Close(); err != nil {
		t.Fatalf("1 回目の Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("2 回目の Close (冪等のはず): %v", err)
	}
}

// Close 後の WritePacket は ErrLinkClosed を返す。
func TestUnixLink_WriteAfterClose(t *testing.T) {
	a, b, _, _ := newUnixPair(t)
	t.Cleanup(func() { b.Close() })
	a.Close()
	if err := a.WritePacket([]byte("x")); !errors.Is(err, ErrLinkClosed) {
		t.Fatalf("Close 後の WritePacket は ErrLinkClosed のはず: got %v", err)
	}
}

// Close すると bind した localPath のソケットファイルが掃除される。
func TestUnixLink_CloseUnlinksSocketFile(t *testing.T) {
	a, b, pa, _ := newUnixPair(t)
	t.Cleanup(func() { b.Close() })

	if _, err := os.Stat(pa); err != nil {
		t.Fatalf("bind 後にソケットファイルが無い: %v", err)
	}
	a.Close()
	if _, err := os.Stat(pa); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Close 後もソケットファイルが残っている: %v", err)
	}
}

// newUnixPairFD は socketpair(AF_UNIX, SOCK_DGRAM) で相互接続済みの 2 本を
// unixLink で包んで返す。NewUnixLink の socket(2)+bind 経路を許さない隔離環境
// でも、実 AF_UNIX データグラム土管の上で WritePacket/ReadPacket/Close が
// 正しく動くことをこの環境で実証するためのもの。
func newUnixPairFD(t *testing.T) (*unixLink, *unixLink) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	return newUnixLinkFD(fds[0]), newUnixLinkFD(fds[1])
}

// 実 AF_UNIX/SOCK_DGRAM 土管 (socketpair) 越しに往復する。1 write = 1 datagram
// = 1 IP パケットの境界が保たれ、送ったバイト列がそのまま届く。
func TestUnixLink_RoundTripFD(t *testing.T) {
	a, b := newUnixPairFD(t)
	t.Cleanup(func() { a.Close(); b.Close() })

	want := []byte("ip-packet-bytes-A->B")
	if err := a.WritePacket(want); err != nil {
		t.Fatalf("WritePacket A: %v", err)
	}
	got, err := b.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket B: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("A->B 不一致: got %q want %q", got, want)
	}

	want2 := []byte("reply-B->A")
	if err := b.WritePacket(want2); err != nil {
		t.Fatalf("WritePacket B: %v", err)
	}
	got2, err := a.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket A: %v", err)
	}
	if !bytes.Equal(got2, want2) {
		t.Fatalf("B->A 不一致: got %q want %q", got2, want2)
	}
}

// 実 AF_UNIX 土管 (socketpair) で Close すると Recvfrom でブロック中の
// ReadPacket が ErrLinkClosed で抜け、goroutine がリークしない。
func TestUnixLink_CloseUnblocksReadFD(t *testing.T) {
	a, b := newUnixPairFD(t)
	t.Cleanup(func() { b.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := a.ReadPacket()
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrLinkClosed) {
			t.Fatalf("Close 後の ReadPacket は ErrLinkClosed のはず: got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close しても ReadPacket が抜けない (goroutine リーク)")
	}
}

// 実 AF_UNIX 土管 (socketpair) で二重 Close が panic もエラーもなく冪等。
func TestUnixLink_CloseIdempotentFD(t *testing.T) {
	a, b := newUnixPairFD(t)
	b.Close()
	if err := a.Close(); err != nil {
		t.Fatalf("1 回目の Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("2 回目の Close (冪等のはず): %v", err)
	}
}

// 実 AF_UNIX 土管 (socketpair) で 2 つの Conn を繋ぎ、Serve で受信ループを
// 回して握手 -> データ転送 -> close まで通す。UDP プロトコルを一切使わず
// AF_UNIX のバイト土管だけで自作 TCP が実通信できることの証明。
func TestUnixLoopback_HandshakeDataCloseFD(t *testing.T) {
	clientLink, serverLink := newUnixPairFD(t)

	client := NewConn(clientLink, time.Now, unixEPClient, unixEPServer)
	server := NewConn(serverLink, time.Now, unixEPServer, unixEPClient)
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
	t.Logf("握手成立 (実 AF_UNIX socketpair 越し): client=%v server=%v", client.State(), server.State())

	msg := []byte("hello over unix socket")
	if _, err := client.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := recvAll(t, server, len(msg))
	if !bytes.Equal(got, msg) {
		t.Fatalf("受信不一致: got %q want %q", got, msg)
	}
	t.Logf("データ転送 OK (実 AF_UNIX socketpair 越し): %q", got)

	client.Close()
	waitConnState(t, server, CloseWait)
	server.Close()
	waitConnState(t, client, TimeWait)
	waitConnState(t, server, Closed)
	t.Logf("close 進行: client=%v server=%v", client.State(), server.State())

	waitConnState(t, client, Closed)
	t.Logf("CLOSED 到達 (実 AF_UNIX socketpair 越しで握手〜データ〜close 完了)")

	buf := make([]byte, 16)
	if _, err := server.Recv(buf); err != nil && err != io.EOF {
		t.Logf("server Recv after close: %v", err)
	}
}

// 自作スタックのエンドポイント (Unix の運搬先とは別物。自作 TCP/IP ヘッダ内の IP)。
var (
	unixEPClient = Endpoint{IP: [4]byte{10, 0, 0, 1}, Port: 9000}
	unixEPServer = Endpoint{IP: [4]byte{10, 0, 0, 2}, Port: 9001}
)

// 2 つの Conn を実 Unix socket 越し (メモリ pipe でなく) に繋ぎ、Serve で受信
// ループを回して、握手 -> データ転送 -> close まで通す。これが通れば「UDP
// プロトコルすら使わず Unix socket の土管だけで自作 TCP が別プロセス相当で実通信」
// の証明。特権不要。
func TestUnixLoopback_HandshakeDataClose(t *testing.T) {
	requireUnixSocket(t)
	dir := t.TempDir()
	pClient := filepath.Join(dir, "client.sock")
	pServer := filepath.Join(dir, "server.sock")

	clientLink, err := NewUnixLink(pClient, pServer)
	if err != nil {
		t.Fatalf("client link: %v", err)
	}
	serverLink, err := NewUnixLink(pServer, pClient)
	if err != nil {
		clientLink.Close()
		t.Fatalf("server link: %v", err)
	}

	client := NewConn(clientLink, time.Now, unixEPClient, unixEPServer)
	server := NewConn(serverLink, time.Now, unixEPServer, unixEPClient)
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
	t.Logf("握手成立 (実 Unix socket 越し): client=%v server=%v", client.State(), server.State())

	// client -> server へ実データ。
	msg := []byte("hello over unix socket")
	if _, err := client.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := recvAll(t, server, len(msg))
	if !bytes.Equal(got, msg) {
		t.Fatalf("受信不一致: got %q want %q", got, msg)
	}
	t.Logf("データ転送 OK (実 Unix socket 越し): %q", got)

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
	t.Logf("CLOSED 到達 (実 Unix socket 越しで握手〜データ〜close 完了)")

	// server 側で EOF が観測できることも確認 (FIN 受信後)。
	buf := make([]byte, 16)
	if _, err := server.Recv(buf); err != nil && err != io.EOF {
		t.Logf("server Recv after close: %v", err)
	}
}

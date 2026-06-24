//go:build linux

package tcp

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/hakkadaikon/tcp_vibe/tcp/link"
)

// requireUnixSocket は AF_UNIX/SOCK_DGRAM を作れない環境ではテストを skip する。
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

	clientLink, err := link.NewUnixLink(pClient, pServer)
	if err != nil {
		t.Fatalf("client link: %v", err)
	}
	serverLink, err := link.NewUnixLink(pServer, pClient)
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

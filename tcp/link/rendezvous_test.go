//go:build linux

package link

import (
	"fmt"
	"syscall"
	"testing"
	"time"
)

// regAndRecv は 1 つの UDP ソケットを開いてサーバへ登録し、PEER 応答を 1 つ受け取る。
// 返り値はソケットの自動割当ポートと、サーバが返した相手アドレス文字列。
func regAndRecv(t *testing.T, srvPort uint16, sid string) (myPort uint16, peer string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socket: %v", err)
	}
	t.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: 0, Addr: loIP}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	sa, _ := syscall.Getsockname(fd)
	myPort = uint16(sa.(*syscall.SockaddrInet4).Port)

	srv := &syscall.SockaddrInet4{Port: int(srvPort), Addr: loIP}
	if err := syscall.Sendto(fd, []byte(regPrefix+sid), 0, srv); err != nil {
		t.Fatalf("sendto reg: %v", err)
	}

	// 応答待ちにタイムアウトを設ける (相手が揃わないと来ない)。
	tv := syscall.Timeval{Sec: 3}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	buf := make([]byte, regBufSize)
	n, _, err := syscall.Recvfrom(fd, buf, 0)
	if err != nil {
		t.Fatalf("recvfrom peer (sid=%s): %v", sid, err)
	}
	return myPort, string(buf[:n])
}

// 2 端が同じ session ID で登録すると、サーバは互いの (localhost 上の) アドレスを
// 相手へ返す。これが「グローバルアドレスの中継」= STUN の本質の localhost 検証。
func TestRendezvous_PairsTwoClients(t *testing.T) {
	r, err := NewRendezvous(0)
	if err != nil {
		t.Fatalf("NewRendezvous: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	srvPort, err := r.LocalPort()
	if err != nil {
		t.Fatalf("LocalPort: %v", err)
	}
	go r.Serve()

	const sid = "sess-1"
	type res struct {
		myPort uint16
		peer   string
	}
	ch := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			p, peer := regAndRecv(t, srvPort, sid)
			ch <- res{p, peer}
		}()
	}
	a := <-ch
	b := <-ch

	// a が受け取った相手アドレスは b のポート、b が受け取ったのは a のポートのはず。
	if a.peer != fmt.Sprintf("%s127.0.0.1:%d", peerPrefix, b.myPort) {
		t.Fatalf("a が受け取った peer 不一致: got %q want port %d", a.peer, b.myPort)
	}
	if b.peer != fmt.Sprintf("%s127.0.0.1:%d", peerPrefix, a.myPort) {
		t.Fatalf("b が受け取った peer 不一致: got %q want port %d", b.peer, a.myPort)
	}
}

// 揃わない (相手不在) 場合は応答が来ず、client 側はタイムアウトする。
func TestRendezvous_NoPeerTimesOut(t *testing.T) {
	r, err := NewRendezvous(0)
	if err != nil {
		t.Fatalf("NewRendezvous: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	srvPort, _ := r.LocalPort()
	go r.Serve()

	fd, _ := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	t.Cleanup(func() { syscall.Close(fd) })
	_ = syscall.Bind(fd, &syscall.SockaddrInet4{Port: 0, Addr: loIP})
	srv := &syscall.SockaddrInet4{Port: int(srvPort), Addr: loIP}
	_ = syscall.Sendto(fd, []byte(regPrefix+"lonely"), 0, srv)

	tv := syscall.Timeval{Sec: 0, Usec: 300000}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	buf := make([]byte, regBufSize)
	start := time.Now()
	_, _, err = syscall.Recvfrom(fd, buf, 0)
	if err == nil {
		t.Fatalf("相手不在なのに応答が来た: %q", string(buf))
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("タイムアウトが早すぎる (相手不在で待つはず): %v", time.Since(start))
	}
}

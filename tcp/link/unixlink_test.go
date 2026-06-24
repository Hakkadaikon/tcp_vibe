//go:build linux

package link

import (
	"bytes"
	"errors"
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

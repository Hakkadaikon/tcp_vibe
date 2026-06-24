//go:build linux

package link

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

// udpLink は Link インターフェースを満たす (コンパイル時保証)。
var _ Link = (*udpLink)(nil)

var loIP = [4]byte{127, 0, 0, 1}

// newUDPPair は localhost の別ポートに 2 つの udpLink を開き、互いを宛先にして返す。
// A: local=pa remote=127.0.0.1:pb / B: local=pb remote=127.0.0.1:pa。
func newUDPPair(t *testing.T, pa, pb uint16) (Link, Link) {
	t.Helper()
	a, err := NewUDPLink(pa, loIP, pb)
	if err != nil {
		t.Fatalf("NewUDPLink A: %v", err)
	}
	b, err := NewUDPLink(pb, loIP, pa)
	if err != nil {
		a.Close()
		t.Fatalf("NewUDPLink B: %v", err)
	}
	return a, b
}

// 実 UDP ソケット越しに往復する: A.WritePacket -> B.ReadPacket、逆向きも一致する。
// このサンドボックス (root 無し) でも localhost UDP は特権不要なので実通信検証になる。
func TestUDPLink_RoundTrip(t *testing.T) {
	a, b := newUDPPair(t, 53001, 53002)
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
func TestUDPLink_CloseUnblocksRead(t *testing.T) {
	a, b := newUDPPair(t, 53003, 53004)
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

// Close は冪等。二重呼び出しでエラーにならない。
func TestUDPLink_CloseIdempotent(t *testing.T) {
	a, b := newUDPPair(t, 53005, 53006)
	b.Close()
	if err := a.Close(); err != nil {
		t.Fatalf("1 回目の Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("2 回目の Close (冪等のはず): %v", err)
	}
}

// Close 後の WritePacket は ErrLinkClosed を返す。
func TestUDPLink_WriteAfterClose(t *testing.T) {
	a, b := newUDPPair(t, 53007, 53008)
	t.Cleanup(func() { b.Close() })
	a.Close()
	if err := a.WritePacket([]byte("x")); !errors.Is(err, ErrLinkClosed) {
		t.Fatalf("Close 後の WritePacket は ErrLinkClosed のはず: got %v", err)
	}
}

// 対称化: punch リンクは remote を指定せずに開き、受信した送信元を学習して
// 以後そこへ送り返せる。A は固定 remote、B は学習で、B が A の送信元を覚えて往復する。
func TestUDPLink_PunchLearnsSource(t *testing.T) {
	const pa, pb = 53020, 53021
	a, err := NewUDPLink(pa, loIP, pb)
	if err != nil {
		t.Fatalf("NewUDPLink A: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := NewUDPLinkPunch(pb)
	if err != nil {
		t.Fatalf("NewUDPLinkPunch B: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	// remote 未確定の B は書けない。
	if err := b.WritePacket([]byte("x")); !errors.Is(err, ErrPunchPeerUnknown) {
		t.Fatalf("確定前 WritePacket は ErrPunchPeerUnknown のはず: got %v", err)
	}

	// A -> B。B は送信元 (A のアドレス) を学習する。
	want := []byte("from-A")
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

	// 学習後、B は remote を知らずとも A へ返せる。
	reply := []byte("reply-from-B")
	if err := b.WritePacket(reply); err != nil {
		t.Fatalf("学習後 WritePacket B: %v", err)
	}
	got2, err := a.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket A: %v", err)
	}
	if !bytes.Equal(got2, reply) {
		t.Fatalf("B->A 不一致: got %q want %q", got2, reply)
	}
}

// setRemote で先に固定すると学習を待たずに書ける。
func TestUDPLink_PunchSetRemote(t *testing.T) {
	const pa, pb = 53022, 53023
	a, err := NewUDPLink(pa, loIP, pb)
	if err != nil {
		t.Fatalf("NewUDPLink A: %v", err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := NewUDPLinkPunch(pb)
	if err != nil {
		t.Fatalf("NewUDPLinkPunch B: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	b.setRemote(loIP, pa)
	want := []byte("from-B-after-setRemote")
	if err := b.WritePacket(want); err != nil {
		t.Fatalf("setRemote 後 WritePacket: %v", err)
	}
	got, err := a.ReadPacket()
	if err != nil {
		t.Fatalf("ReadPacket A: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("不一致: got %q want %q", got, want)
	}
}

// localPort=0 を渡すと OS が空きポートを自動割当し、LocalPort で取得できる。
func TestUDPLink_AutoAssignPort(t *testing.T) {
	l, err := NewUDPLink(0, loIP, 53009)
	if err != nil {
		t.Fatalf("NewUDPLink: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	port, err := l.(*udpLink).LocalPort()
	if err != nil {
		t.Fatalf("LocalPort: %v", err)
	}
	if port == 0 {
		t.Fatal("自動割当ポートが 0 のまま")
	}
	t.Logf("自動割当ポート: %d", port)
}

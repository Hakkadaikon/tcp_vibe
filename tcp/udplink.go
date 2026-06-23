//go:build linux

package tcp

import (
	"errors"
	"sync"
	"syscall"
)

// udpLink は UDP ソケットを「IP パケットを運ぶ土管」として使う Link 実装。
// 自作スタックが組み立てた IP パケット (IPv4+TCP) を UDP のペイロードとして
// 相手へ送り、受信側は UDP データグラムの中身をそのまま IP パケットとして
// 状態機械に渡す。カーネルの TCP/IP ロジックは一切経由しない (UDP は単なる
// トランスポート = ケーブル代わり)。
//
// raw socket でないので CAP_NET_RAW も TUN (CAP_NET_ADMIN) も要らず、root 無しの
// どこでも動く。localhost (127.0.0.1) で 2 つの udpLink を別ポートで開けば、
// 同一ホスト・同一/別プロセスで自作スタック同士が実通信できる。
//
// 1 recvfrom = 1 データグラム = 1 IP パケットなので、1 read = 1 IP パケットを
// 期待する受信ループ (recvloop.go) と境界がそのまま合う。
type udpLink struct {
	fd     int
	remote syscall.SockaddrInet4

	mu     sync.Mutex
	closed bool
}

// NewUDPLink は 0.0.0.0:localPort に束ねた UDP ソケットを開き、remote を宛先と
// して保持する Link を返す。localPort=0 なら OS が空きポートを自動割当する
// (LocalPort で取得可能)。特権不要。
func NewUDPLink(localPort uint16, remoteIP [4]byte, remotePort uint16) (Link, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	// 連続テストや再起動で直前のポートが残っていても再バインドできるように。
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: int(localPort)}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	l := &udpLink{
		fd:     fd,
		remote: syscall.SockaddrInet4{Port: int(remotePort), Addr: remoteIP},
	}
	debugf("udp: open local=:%d remote=%s:%d", localPort, ipStr(remoteIP), remotePort)
	return l, nil
}

// WritePacket は IP パケット全体を UDP ペイロードとして remote へ送る。
func (l *udpLink) WritePacket(pkt []byte) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return ErrLinkClosed
	}
	err := syscall.Sendto(l.fd, pkt, 0, &l.remote)
	debugf("udp: write n=%d err=%v", len(pkt), err)
	return err
}

// ReadPacket は 1 データグラムを受け取り、その中身 (= IP パケット) を返す。
// Close 済み・Close 中のソケットクローズで Recvfrom が返すエラーは ErrLinkClosed に
// 正規化し、受信ループを正常終了させる。
func (l *udpLink) ReadPacket() ([]byte, error) {
	buf := make([]byte, 65535)
	for {
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()
		if closed {
			return nil, ErrLinkClosed
		}
		n, _, err := syscall.Recvfrom(l.fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			// Close により fd が閉じられた/シャットダウンされた場合は正常終了扱い。
			if errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EINVAL) {
				return nil, ErrLinkClosed
			}
			debugf("udp: read err=%v", err)
			return nil, err
		}
		// Recvfrom 中に Close されると shutdown が 0 バイトで起こすことがある。
		// 閉じていれば ErrLinkClosed で抜ける (空データグラムを返さない)。
		l.mu.Lock()
		closed = l.closed
		l.mu.Unlock()
		if closed {
			return nil, ErrLinkClosed
		}
		debugf("udp: read n=%d", n)
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		return pkt, nil
	}
}

// Close はソケットを閉じる。冪等。先に shutdown で受信をたたき起こしてから
// close することで、Recvfrom でブロック中の goroutine がエラーで抜けられる。
func (l *udpLink) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	// ブロック中の Recvfrom を起こす。SOCK_DGRAM では shutdown が効かない環境も
	// あるため、続く Close で fd を無効化して確実に抜けさせる。
	_ = syscall.Shutdown(l.fd, syscall.SHUT_RDWR)
	return syscall.Close(l.fd)
}

// LocalPort は実際に束ねられたローカルポートを返す (localPort=0 の自動割当の確認用)。
func (l *udpLink) LocalPort() (uint16, error) {
	sa, err := syscall.Getsockname(l.fd)
	if err != nil {
		return 0, err
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	if !ok {
		return 0, errors.New("udp: getsockname が IPv4 でない")
	}
	return uint16(in4.Port), nil
}

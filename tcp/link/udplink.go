//go:build linux

package link

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

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

	mu sync.Mutex
	// learn が真のとき remote は未確定で、最初に受信した送信元を remote に採用する
	// (UDP hole punching 用の対称動作)。確定後は learn=false になり固定 remote と同じ。
	learn  bool
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
	network.Debugf("udp: open local=:%d remote=%s:%d", localPort, network.IPStr(remoteIP), remotePort)
	return l, nil
}

// NewUDPLinkPunch は :localPort に束ねた UDP ソケットを remote 未確定で開く。
// hole punching 用。最初に受信したデータグラムの送信元を remote に採用し
// (ReadPacket 内)、以後そこへ送る対称動作になる。確定前に WritePacket すると
// ErrPunchPeerUnknown を返す。確立済みの相手が分かっているなら setRemote で
// 先に固定してもよい。
func NewUDPLinkPunch(localPort uint16) (*udpLink, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: int(localPort)}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	network.Debugf("udp: open(punch) local=:%d remote=<未確定>", localPort)
	return &udpLink{fd: fd, learn: true}, nil
}

// ErrPunchPeerUnknown は remote 未確定の punch リンクへ書こうとしたときに返る。
var ErrPunchPeerUnknown = errors.New("tcp: udp punch link の remote 未確定")

// setRemote は remote を固定し、学習モードを解除する。punch で相手アドレスが
// 分かったときに呼ぶ。
func (l *udpLink) setRemote(ip [4]byte, port uint16) {
	l.mu.Lock()
	l.remote = syscall.SockaddrInet4{Port: int(port), Addr: ip}
	l.learn = false
	l.mu.Unlock()
}

// WritePacket は IP パケット全体を UDP ペイロードとして remote へ送る。
func (l *udpLink) WritePacket(pkt []byte) error {
	l.mu.Lock()
	closed := l.closed
	unknown := l.learn
	remote := l.remote
	l.mu.Unlock()
	if closed {
		return ErrLinkClosed
	}
	if unknown {
		return ErrPunchPeerUnknown
	}
	err := syscall.Sendto(l.fd, pkt, 0, &remote)
	network.Debugf("udp: write n=%d err=%v", len(pkt), err)
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
		n, from, err := syscall.Recvfrom(l.fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			// Close により fd が閉じられた/シャットダウンされた場合は正常終了扱い。
			if errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EINVAL) {
				return nil, ErrLinkClosed
			}
			network.Debugf("udp: read err=%v", err)
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
		// 学習モードなら最初に受けた送信元を remote に採用し、以後そこへ送る。
		l.mu.Lock()
		if l.learn {
			if in4, ok := from.(*syscall.SockaddrInet4); ok {
				l.remote = *in4
				l.learn = false
				network.Debugf("udp: learn remote=%s:%d", network.IPStr(in4.Addr), in4.Port)
			}
		}
		l.mu.Unlock()
		// punch パケット (hole punching の打診) は IP パケットではないので上位に
		// 渡さず捨てる。確立後に相手の punch 連射が遅れて届いても無視するため。
		// IPv4 ヘッダは先頭が 0x4_ なので punch マーカ 'P' (0x50) と衝突しない。
		if isPunchPacket(buf[:n]) {
			network.Debugf("udp: drop late punch packet n=%d", n)
			continue
		}
		network.Debugf("udp: read n=%d", n)
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

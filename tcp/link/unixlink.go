//go:build linux

package link

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import (
	"errors"
	"sync"
	"syscall"
)

// unixLink は Unix domain socket (AF_UNIX, SOCK_DGRAM) を「IP パケットを運ぶ土管」
// として使う Link 実装。自作スタックが組み立てた IP パケット (IPv4+TCP) を Unix
// データグラムのペイロードとしてそのまま相手へ送り、受信側は中身をそのまま IP
// パケットとして状態機械に渡す。
//
// udpLink との違いはトランスポートだけ: udpLink はカーネルの UDP/IP プロトコル
// 処理を経由する (AF_INET) が、unixLink は AF_UNIX のプロセス間バイト土管なので
// カーネルの UDP/IP プロトコルを一切通さない。raw socket でないので CAP_NET_RAW も
// TUN も要らず特権不要。別プロセス間 (同一ホスト) で自作スタック同士が実通信できる。
//
// 1 recvfrom = 1 データグラム = 1 IP パケットなので、1 read = 1 IP パケットを
// 期待する受信ループ (recvloop.go) と境界がそのまま合う。
type unixLink struct {
	fd        int
	remote    syscall.SockaddrUnix
	localPath string
	connected bool // 接続済み fd (socketpair) なら Sendto の宛先を省く

	mu     sync.Mutex
	closed bool
}

// NewUnixLink は localPath に bind した AF_UNIX/SOCK_DGRAM ソケットを開き、
// remotePath を宛先として保持する Link を返す。bind 前に localPath に残った
// 古いソケットファイルがあれば掃除する。特権不要。
func NewUnixLink(localPath string, remotePath string) (Link, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	// 前回の異常終了などで残ったソケットファイルがあると bind が EADDRINUSE になる。
	// 残骸を掃除してから bind する (無ければ ENOENT で無害)。
	_ = syscall.Unlink(localPath)
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: localPath}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	l := &unixLink{
		fd:        fd,
		remote:    syscall.SockaddrUnix{Name: remotePath},
		localPath: localPath,
	}
	network.Debugf("unix: open local=%s remote=%s", localPath, remotePath)
	return l, nil
}

// newUnixLinkFD は既に接続済みの AF_UNIX/SOCK_DGRAM fd を包む。socketpair で
// 作った相互接続済みペアを土管に使うテスト用。Sendto の宛先は不要なので
// 「remote 無し (NULL アドレス)」で Sendto する (接続済みソケットは宛先省略で
// 相手へ届く)。localPath が空なら Close で Unlink しない。
func newUnixLinkFD(fd int) *unixLink {
	return &unixLink{fd: fd, connected: true}
}

// WritePacket は IP パケット全体を Unix データグラムとして remote へ送る。
// 相手がまだ bind していない握手初期は Sendto が ENOENT/ECONNREFUSED を返すが、
// リトライで届くので致命にせずログだけ残し、呼び出し側 (recvloop の再送) に委ねる。
func (l *unixLink) WritePacket(pkt []byte) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return ErrLinkClosed
	}
	var err error
	if l.connected {
		// ponytail: 接続済み fd (socketpair) 用の分岐。本番は下の Sendto 経路。
		// socket(2)+bind を許さないサンドボックスでも実通信を検証するための足場。
		_, err = syscall.Write(l.fd, pkt)
	} else {
		err = syscall.Sendto(l.fd, pkt, 0, &l.remote)
	}
	network.Debugf("unix: write n=%d err=%v", len(pkt), err)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		// 相手の bind 待ち。再送で回収されるので握手を止めない。
		return nil
	}
	return err
}

// ReadPacket は 1 データグラムを受け取り、その中身 (= IP パケット) を返す。
// Close 済み・Close 中のソケットクローズで Recvfrom が返すエラーは ErrLinkClosed に
// 正規化し、受信ループを正常終了させる。
func (l *unixLink) ReadPacket() ([]byte, error) {
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
			network.Debugf("unix: read err=%v", err)
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
		network.Debugf("unix: read n=%d", n)
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		return pkt, nil
	}
}

// Close はソケットを閉じ、localPath のソケットファイルを掃除する。冪等。
// 先に shutdown で受信をたたき起こしてから close することで、Recvfrom で
// ブロック中の goroutine がエラーで抜けられる。
func (l *unixLink) Close() error {
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
	err := syscall.Close(l.fd)
	// close 後に自分の bind したソケットファイルを掃除する (残骸を残さない)。
	_ = syscall.Unlink(l.localPath)
	return err
}

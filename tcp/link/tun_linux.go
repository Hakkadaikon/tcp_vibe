//go:build linux

package link

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// tunLink は L3 TUN デバイスを使う Link 実装。/dev/net/tun を開き、
// TUNSETIFF で指定インターフェースに attach する。
//
// IFF_TUN | IFF_NO_PI を指定するため、read/write は 4 バイトのパケット情報
// ヘッダ無しの生の IP パケットそのものになる。1 read = 1 IP パケットなので
// AF_PACKET (Ethernet) と違い Ethernet ヘッダの着脱は不要。
//
// 実行には CAP_NET_ADMIN (通常 root) と /dev/net/tun が要る。権限/デバイスが
// 無い環境ではこのコンストラクタが失敗する (tun_linux_test.go は失敗を skip)。
type tunLink struct {
	f      *os.File
	closed bool
}

const (
	// linux/if_tun.h より。
	iffTUN   = 0x0001
	iffNoPI  = 0x1000
	tunsetif = 0x400454ca // TUNSETIFF (_IOW('T', 202, int))
	ifnamsiz = 16
)

// NewTUNLink は ifName の TUN デバイスに attach した Link を開く。
// 要 CAP_NET_ADMIN。デバイスや権限が無ければエラーを返す。
func NewTUNLink(ifName string) (Link, error) {
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	// ifreq: char ifr_name[IFNAMSIZ]; short ifr_flags; ... を手で詰める
	// (encoding/binary 不使用方針)。先頭 16 バイトに名前、続く 2 バイトにフラグ。
	var req [ifnamsiz + 24]byte
	if len(ifName) >= ifnamsiz {
		f.Close()
		return nil, errors.New("tun: interface name too long")
	}
	copy(req[:ifnamsiz], ifName)
	flags := uint16(iffTUN | iffNoPI)
	req[ifnamsiz] = byte(flags)
	req[ifnamsiz+1] = byte(flags >> 8)

	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		uintptr(tunsetif),
		uintptr(unsafe.Pointer(&req[0])),
	)
	if errno != 0 {
		f.Close()
		return nil, errno
	}
	return &tunLink{f: f}, nil
}

// WritePacket は IP パケットをそのまま書く (IFF_NO_PI なので前置ヘッダ無し)。
func (l *tunLink) WritePacket(pkt []byte) error {
	if l.closed {
		return ErrLinkClosed
	}
	n, err := l.f.Write(pkt)
	network.Debugf("tun: write n=%d err=%v", n, err)
	return err
}

// ReadPacket は 1 回の read で 1 つの IP パケットを返す (TUN は境界を保つ)。
func (l *tunLink) ReadPacket() ([]byte, error) {
	if l.closed {
		return nil, ErrLinkClosed
	}
	buf := make([]byte, 65535)
	n, err := l.f.Read(buf)
	network.Debugf("tun: read n=%d err=%v", n, err)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// Close はファイルディスクリプタを閉じる。冪等。
func (l *tunLink) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return l.f.Close()
}

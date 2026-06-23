//go:build linux

package tcp

import (
	"errors"
	"syscall"
)

// afPacketLink は AF_PACKET raw socket を使う Link 実装。x86/64 Linux VPS 上で
// カーネルの TCP/IP を迂回して Ethernet フレームを直接読み書きする。
//
// 実行には CAP_NET_RAW (通常 root) が要る。メモリ仮想リンクと同じ Link を実装
// するので、権限の取れる実機ではこれを差し替えるだけで状態機械がそのまま載る。
//
// ponytail: 宛先 MAC 解決 (ARP) と Ethernet フレームの完全な組み立ては未実装。
// 単一サブネットの固定 peer MAC を渡す前提の最小形。ARP が要る運用になったら
// 解決テーブルをここに足す。このサンドボックスは CAP_NET_RAW を持たないため
// 実通信の自動テストはできない (afpacket_linux_test.go は権限が無ければ skip)。
type afPacketLink struct {
	fd      int
	ifIndex int
	peerMAC [6]byte
	selfMAC [6]byte
	closed  bool
}

const (
	ethTypeIPv4   = 0x0800
	ethHeaderSize = 14
)

// htons はホストバイト順を network バイト順 (big-endian) に変換する (16bit)。
func htons(v uint16) uint16 { return v<<8 | v>>8 }

// NewAFPacketLink は ifIndex の NIC に束ねた AF_PACKET raw socket を開く。
// selfMAC/peerMAC は Ethernet ヘッダの送信元/宛先に使う固定値。
// 要 CAP_NET_RAW。権限が無ければ syscall がエラーを返す。
func NewAFPacketLink(ifIndex int, selfMAC, peerMAC [6]byte) (Link, error) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(ethTypeIPv4)))
	if err != nil {
		return nil, err
	}
	ll := syscall.SockaddrLinklayer{
		Protocol: htons(ethTypeIPv4),
		Ifindex:  ifIndex,
	}
	if err := syscall.Bind(fd, &ll); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &afPacketLink{fd: fd, ifIndex: ifIndex, selfMAC: selfMAC, peerMAC: peerMAC}, nil
}

// WritePacket は IP パケットを Ethernet フレームに包んで送る。
func (l *afPacketLink) WritePacket(pkt []byte) error {
	if l.closed {
		return ErrLinkClosed
	}
	frame := make([]byte, ethHeaderSize+len(pkt))
	copy(frame[0:6], l.peerMAC[:])
	copy(frame[6:12], l.selfMAC[:])
	frame[12] = byte(ethTypeIPv4 >> 8)
	frame[13] = byte(ethTypeIPv4 & 0xFF)
	copy(frame[ethHeaderSize:], pkt)
	to := &syscall.SockaddrLinklayer{Ifindex: l.ifIndex, Halen: 6}
	copy(to.Addr[:6], l.peerMAC[:])
	return syscall.Sendto(l.fd, frame, 0, to)
}

// ReadPacket は 1 フレームを読み、Ethernet ヘッダを剥がして IP パケットを返す。
// IPv4 以外のフレームは読み飛ばす。
func (l *afPacketLink) ReadPacket() ([]byte, error) {
	buf := make([]byte, 65535+ethHeaderSize)
	for {
		if l.closed {
			return nil, ErrLinkClosed
		}
		n, _, err := syscall.Recvfrom(l.fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			return nil, err
		}
		if n < ethHeaderSize {
			continue // フレーム未満は捨てる
		}
		ethType := uint16(buf[12])<<8 | uint16(buf[13])
		if ethType != ethTypeIPv4 {
			continue
		}
		pkt := make([]byte, n-ethHeaderSize)
		copy(pkt, buf[ethHeaderSize:n])
		return pkt, nil
	}
}

// Close はソケットを閉じる。冪等。
func (l *afPacketLink) Close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return syscall.Close(l.fd)
}

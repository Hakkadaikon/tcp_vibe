//go:build linux

package tcp

import (
	"errors"
	"syscall"
)

// afPacketLink は AF_PACKET raw socket を使う Link 実装。x86/64 Linux VPS 上で
// カーネルの TCP/IP を迂回して Ethernet フレームを直接読み書きする。宛先 MAC は
// ARP (RFC 826) を自作スタックで解決するため、運搬層はカーネルのプロトコル
// (UDP/IP/ARP) を一切使わず NIC への raw バイト I/O だけになる。
//
// 実行には CAP_NET_RAW (通常 root) が要る。メモリ仮想リンクと同じ Link を実装
// するので、権限の取れる実機ではこれを差し替えるだけで状態機械がそのまま載る。
// このサンドボックスは CAP_NET_RAW を持たないため実通信の自動テストはできない
// (afpacket_linux_test.go は権限が無ければ skip)。L2 ロジック (フレーム組立・
// ARP 透過処理) はソケット非依存に切り出して root 無しで単体テストする。
//
// ponytail: 同一 L2 セグメント前提で gateway/サブネット越えは扱わない。未解決 IP
// への送信は ARP request を撒いて当該パケットを drop し、上位の再送に任せる
// (保留キュー無し)。完全な保留キューが要る運用になったら足す。
type afPacketLink struct {
	fd      int
	ifIndex int
	selfMAC [6]byte
	selfIP  [4]byte
	arp     *arpTable
	closed  bool
}

const (
	ethTypeIPv4   = 0x0800
	ethHeaderSize = 14
)

var broadcastMAC = [6]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// htons はホストバイト順を network バイト順 (big-endian) に変換する (16bit)。
func htons(v uint16) uint16 { return v<<8 | v>>8 }

// NewAFPacketLink は ifIndex の NIC に束ねた AF_PACKET raw socket を開く。
// selfMAC/selfIP は自分の L2/L3 アドレス。宛先 MAC は ARP で動的解決する。
// IPv4 と ARP の両方を受けるため ETH_P_ALL で束ねる。
// 要 CAP_NET_RAW。権限が無ければ syscall がエラーを返す。
func NewAFPacketLink(ifIndex int, selfMAC [6]byte, selfIP [4]byte) (Link, error) {
	const ethPAll = 0x0003 // ETH_P_ALL: IPv4 も ARP も受ける
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(ethPAll)))
	if err != nil {
		return nil, err
	}
	ll := syscall.SockaddrLinklayer{
		Protocol: htons(ethPAll),
		Ifindex:  ifIndex,
	}
	if err := syscall.Bind(fd, &ll); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &afPacketLink{
		fd:      fd,
		ifIndex: ifIndex,
		selfMAC: selfMAC,
		selfIP:  selfIP,
		arp:     newARPTable(),
	}, nil
}

// buildEthFrame は Ethernet ヘッダを付けたフレームを組む。
func buildEthFrame(dst, src [6]byte, ethType uint16, payload []byte) []byte {
	frame := make([]byte, ethHeaderSize+len(payload))
	copy(frame[0:6], dst[:])
	copy(frame[6:12], src[:])
	putBe16(frame, 12, ethType)
	copy(frame[ethHeaderSize:], payload)
	return frame
}

// handleInboundFrame は受信した 1 フレームを分類する。ソケット非依存にして
// L2 ロジックを単体テストできるようにしてある。
//   - IPv4: ip に IP パケットを返す (上位へ渡す)。
//   - ARP request で target が selfIP: reply フレームを reply に返す (送り返す)。
//   - ARP reply: arpTable を更新する (ip/reply はともに nil)。
//   - それ以外 (短すぎ・他 ethType・自分宛でない ARP): すべて nil で読み飛ばす。
func (l *afPacketLink) handleInboundFrame(frame []byte) (ip []byte, reply []byte) {
	if len(frame) < ethHeaderSize {
		return nil, nil
	}
	ethType := be16(frame, 12)
	payload := frame[ethHeaderSize:]
	switch ethType {
	case ethTypeIPv4:
		pkt := make([]byte, len(payload))
		copy(pkt, payload)
		return pkt, nil
	case ethTypeARP:
		a, err := ParseARP(payload)
		if err != nil {
			return nil, nil
		}
		switch a.Op {
		case arpOpReply:
			l.arp.update(a.SenderIP, a.SenderMAC)
		case arpOpRequest:
			if a.TargetIP == l.selfIP {
				rep := newARPReply(l.selfMAC, l.selfIP, a)
				return nil, buildEthFrame(a.SenderMAC, l.selfMAC, ethTypeARP, rep.Marshal())
			}
		}
	}
	return nil, nil
}

// WritePacket は IP パケットを Ethernet フレームに包んで送る。宛先 MAC は
// IP ヘッダの宛先 IP から ARP で解決する。未解決なら ARP request を撒いて
// このパケットを drop し、上位の再送に任せる (ponytail: 保留キュー無し)。
func (l *afPacketLink) WritePacket(pkt []byte) error {
	if l.closed {
		return ErrLinkClosed
	}
	h, err := ParseIPv4Header(pkt)
	if err != nil {
		return err
	}
	mac, ok := l.arp.lookup(h.DstAddr)
	if !ok {
		req := newARPRequest(l.selfMAC, l.selfIP, h.DstAddr)
		frame := buildEthFrame(broadcastMAC, l.selfMAC, ethTypeARP, req.Marshal())
		if err := l.sendFrame(frame, broadcastMAC); err != nil {
			return err
		}
		return errARPUnresolved
	}
	return l.sendFrame(buildEthFrame(mac, l.selfMAC, ethTypeIPv4, pkt), mac)
}

func (l *afPacketLink) sendFrame(frame []byte, dst [6]byte) error {
	to := &syscall.SockaddrLinklayer{Ifindex: l.ifIndex, Halen: 6}
	copy(to.Addr[:6], dst[:])
	return syscall.Sendto(l.fd, frame, 0, to)
}

// errARPUnresolved は宛先 MAC が未解決でパケットを drop したことを示す。
var errARPUnresolved = errors.New("tcp: arp unresolved, packet dropped")

// ReadPacket は IPv4 パケットが届くまでフレームを読み続ける。ARP は内部で
// 透過処理し (request に reply、reply で table 更新)、上位には IPv4 だけ届ける。
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
		ip, reply := l.handleInboundFrame(buf[:n])
		if reply != nil {
			// ARP request への unicast 応答。宛先 MAC はフレーム先頭にある。
			// 失敗は致命でないので無視して読み続ける。
			var dst [6]byte
			copy(dst[:], reply[0:6])
			_ = l.sendFrame(reply, dst)
		}
		if ip != nil {
			return ip, nil
		}
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

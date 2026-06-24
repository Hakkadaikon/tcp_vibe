package tcp

import (
	"errors"
	"sync"
)

// ARP (RFC 826) を Ethernet/IPv4 用に最小実装する。IP→MAC 解決をカーネルに
// 頼らず自作スタックで行うためのもの。同一 L2 セグメント前提で、gateway 越え
// (サブネット外への解決) は扱わない。
//
// ponytail: 対応するのは hwtype=Ethernet, ptype=IPv4 の 28 バイト固定形のみ。
// proxy ARP・gratuitous ARP・RARP は対象外。要るようになったら opcode/フラグを足す。

const (
	ethTypeARP = 0x0806

	arpHWTypeEthernet = 1
	arpPacketSize     = 28

	arpOpRequest = 1
	arpOpReply   = 2
)

// ARPPacket は Ethernet/IPv4 用 ARP パケットの可変部。固定フィールド
// (hwtype/ptype/hlen/plen) は Marshal で埋め、Parse で検証する。
type ARPPacket struct {
	Op        uint16
	SenderMAC [6]byte
	SenderIP  [4]byte
	TargetMAC [6]byte
	TargetIP  [4]byte
}

var (
	errARPShort   = errors.New("arp: buffer shorter than 28 bytes")
	errARPHWType  = errors.New("arp: unsupported hardware type")
	errARPPType   = errors.New("arp: unsupported protocol type")
	errARPAddrLen = errors.New("arp: unexpected address lengths")
)

// Marshal は ARP パケットを 28 バイトへ書き出す。
func (a ARPPacket) Marshal() []byte {
	b := make([]byte, arpPacketSize)
	putBe16(b, 0, arpHWTypeEthernet) // hardware type
	putBe16(b, 2, ethTypeIPv4)       // protocol type
	b[4] = 6                         // hlen
	b[5] = 4                         // plen
	putBe16(b, 6, a.Op)
	copy(b[8:14], a.SenderMAC[:])
	copy(b[14:18], a.SenderIP[:])
	copy(b[18:24], a.TargetMAC[:])
	copy(b[24:28], a.TargetIP[:])
	return b
}

// ParseARP はバイト列を ARP パケットへ復号する。28 バイト未満・想定外の
// hwtype/ptype・アドレス長不一致は拒否する (trust boundary)。
func ParseARP(b []byte) (ARPPacket, error) {
	if len(b) < arpPacketSize {
		return ARPPacket{}, errARPShort
	}
	if be16(b, 0) != arpHWTypeEthernet {
		return ARPPacket{}, errARPHWType
	}
	if be16(b, 2) != ethTypeIPv4 {
		return ARPPacket{}, errARPPType
	}
	if b[4] != 6 || b[5] != 4 {
		return ARPPacket{}, errARPAddrLen
	}
	a := ARPPacket{Op: be16(b, 6)}
	copy(a.SenderMAC[:], b[8:14])
	copy(a.SenderIP[:], b[14:18])
	copy(a.TargetMAC[:], b[18:24])
	copy(a.TargetIP[:], b[24:28])
	return a, nil
}

// newARPRequest は targetIP の MAC を問い合わせる ARP request を組む。
// target MAC は未知なのでゼロ。
func newARPRequest(senderMAC [6]byte, senderIP, targetIP [4]byte) ARPPacket {
	return ARPPacket{
		Op:        arpOpRequest,
		SenderMAC: senderMAC,
		SenderIP:  senderIP,
		TargetIP:  targetIP,
	}
}

// newARPReply は受け取った request に対する reply を組む。sender/target を
// 入れ替え、自分の MAC を sender に入れる。
func newARPReply(selfMAC [6]byte, selfIP [4]byte, req ARPPacket) ARPPacket {
	return ARPPacket{
		Op:        arpOpReply,
		SenderMAC: selfMAC,
		SenderIP:  selfIP,
		TargetMAC: req.SenderMAC,
		TargetIP:  req.SenderIP,
	}
}

// arpTable は IP→MAC の解決キャッシュ。reply 受信で埋める。
// ponytail: TTL 無しの単純キャッシュ。エントリの陳腐化が問題になったら
// clock seam で寿命を付ける。
type arpTable struct {
	mu      sync.Mutex
	entries map[[4]byte][6]byte
}

func newARPTable() *arpTable {
	return &arpTable{entries: make(map[[4]byte][6]byte)}
}

func (t *arpTable) lookup(ip [4]byte) ([6]byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	mac, ok := t.entries[ip]
	return mac, ok
}

func (t *arpTable) update(ip [4]byte, mac [6]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries[ip] = mac
}

//go:build linux

package link

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import (
	"bytes"
	"errors"
	"syscall"
	"testing"
)

// afPacketLink は Link インターフェースを満たす (コンパイル時保証)。
var _ Link = (*afPacketLink)(nil)

// AF_PACKET raw socket は CAP_NET_RAW が要る。権限が無い環境 (このサンドボックス等)
// では NewAFPacketLink が permission denied を返すことを確認する。権限があれば
// socket が開けるので skip する (実通信テストは実機の責務)。
func TestAFPacketLink_RequiresCapability(t *testing.T) {
	mac := [6]byte{0x02, 0, 0, 0, 0, 1}
	ip := [4]byte{10, 0, 0, 1}
	link, err := NewAFPacketLink(1, mac, ip) // ifIndex=1 (lo)
	if err == nil {
		link.Close()
		t.Skip("CAP_NET_RAW があり socket を開けた (実機環境)。実通信テストは別途")
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		t.Logf("socket open failed with %v (permission 以外の理由かもしれない)", err)
	}
}

// newTestLink は socket を開かずに L2 ロジックだけ叩くための link を作る。
func newTestLink() *afPacketLink {
	return &afPacketLink{
		selfMAC: [6]byte{0xbb, 0, 0, 0, 0, 2},
		selfIP:  [4]byte{10, 0, 0, 1},
		arp:     newARPTable(),
	}
}

// IPv4 フレームは Ethernet ヘッダを剥がして上位へ返す。
func TestHandleInboundFrame_IPv4(t *testing.T) {
	l := newTestLink()
	payload := []byte{0x45, 0, 0, 20, 1, 2, 3, 4}
	frame := buildEthFrame(l.selfMAC, [6]byte{1, 1, 1, 1, 1, 1}, ethTypeIPv4, payload)
	ip, reply := l.handleInboundFrame(frame)
	if reply != nil {
		t.Errorf("IPv4 should not produce a reply, got %v", reply)
	}
	if !bytes.Equal(ip, payload) {
		t.Errorf("ip = %v, want %v", ip, payload)
	}
}

// 自分宛 ARP request には reply を返す (opcode=2, sender=self)。
func TestHandleInboundFrame_ARPRequestForSelf(t *testing.T) {
	l := newTestLink()
	reqMAC := [6]byte{0xaa, 0, 0, 0, 0, 9}
	req := newARPRequest(reqMAC, [4]byte{10, 0, 0, 9}, l.selfIP)
	frame := buildEthFrame(broadcastMAC, reqMAC, ethTypeARP, req.Marshal())

	ip, reply := l.handleInboundFrame(frame)
	if ip != nil {
		t.Errorf("ARP should not surface to upper layer, got %v", ip)
	}
	if reply == nil {
		t.Fatal("expected an ARP reply frame")
	}
	// reply フレームは reqMAC 宛 unicast。
	if !bytes.Equal(reply[0:6], reqMAC[:]) {
		t.Errorf("reply dst MAC = %v, want %v", reply[0:6], reqMAC)
	}
	if network.Be16(reply, 12) != ethTypeARP {
		t.Errorf("reply ethType = %#x, want ARP", network.Be16(reply, 12))
	}
	a, err := ParseARP(reply[ethHeaderSize:])
	if err != nil {
		t.Fatalf("reply not parseable: %v", err)
	}
	if a.Op != arpOpReply {
		t.Errorf("op = %d, want reply", a.Op)
	}
	if a.SenderMAC != l.selfMAC || a.SenderIP != l.selfIP {
		t.Errorf("reply sender = %v/%v, want self %v/%v", a.SenderMAC, a.SenderIP, l.selfMAC, l.selfIP)
	}
	if a.TargetMAC != reqMAC || a.TargetIP != ([4]byte{10, 0, 0, 9}) {
		t.Errorf("reply target not echoed: %v/%v", a.TargetMAC, a.TargetIP)
	}
}

// 自分宛でない ARP request は無視する。
func TestHandleInboundFrame_ARPRequestForOther(t *testing.T) {
	l := newTestLink()
	req := newARPRequest([6]byte{0xaa, 0, 0, 0, 0, 9}, [4]byte{10, 0, 0, 9}, [4]byte{10, 0, 0, 99})
	frame := buildEthFrame(broadcastMAC, [6]byte{0xaa, 0, 0, 0, 0, 9}, ethTypeARP, req.Marshal())
	ip, reply := l.handleInboundFrame(frame)
	if ip != nil || reply != nil {
		t.Errorf("foreign ARP request should be ignored, got ip=%v reply=%v", ip, reply)
	}
}

// ARP reply を受けると table が更新される。
func TestHandleInboundFrame_ARPReplyUpdatesTable(t *testing.T) {
	l := newTestLink()
	peerMAC := [6]byte{0xcc, 0, 0, 0, 0, 3}
	peerIP := [4]byte{10, 0, 0, 50}
	reply := ARPPacket{Op: arpOpReply, SenderMAC: peerMAC, SenderIP: peerIP, TargetMAC: l.selfMAC, TargetIP: l.selfIP}
	frame := buildEthFrame(l.selfMAC, peerMAC, ethTypeARP, reply.Marshal())

	ip, out := l.handleInboundFrame(frame)
	if ip != nil || out != nil {
		t.Errorf("reply should be consumed internally, got ip=%v out=%v", ip, out)
	}
	if mac, ok := l.arp.lookup(peerIP); !ok || mac != peerMAC {
		t.Errorf("table not updated: got %v %v, want %v", mac, ok, peerMAC)
	}
}

// 短すぎるフレームや他 ethType は読み飛ばす。
func TestHandleInboundFrame_Ignored(t *testing.T) {
	l := newTestLink()
	if ip, reply := l.handleInboundFrame([]byte{1, 2, 3}); ip != nil || reply != nil {
		t.Error("short frame should be ignored")
	}
	other := buildEthFrame(l.selfMAC, l.selfMAC, 0x86DD, []byte{1, 2, 3}) // IPv6
	if ip, reply := l.handleInboundFrame(other); ip != nil || reply != nil {
		t.Error("non-IPv4/ARP frame should be ignored")
	}
}

package link

import (
	"bytes"
	"testing"
	"testing/quick"
)

// Marshal → ParseARP の往復で可変部が保たれることを確認する (property)。
func TestARPRoundTrip(t *testing.T) {
	f := func(op uint16, sMAC [6]byte, sIP [4]byte, tMAC [6]byte, tIP [4]byte) bool {
		in := ARPPacket{Op: op, SenderMAC: sMAC, SenderIP: sIP, TargetMAC: tMAC, TargetIP: tIP}
		out, err := ParseARP(in.Marshal())
		return err == nil && out == in
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}

// request のバイトレイアウトを固定値で検証する (GOLD)。
func TestARPRequestBytes(t *testing.T) {
	req := newARPRequest([6]byte{1, 2, 3, 4, 5, 6}, [4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2})
	want := []byte{
		0x00, 0x01, // hwtype=1
		0x08, 0x00, // ptype=IPv4
		0x06, 0x04, // hlen=6, plen=4
		0x00, 0x01, // op=request
		1, 2, 3, 4, 5, 6, // sender MAC
		10, 0, 0, 1, // sender IP
		0, 0, 0, 0, 0, 0, // target MAC (unknown)
		10, 0, 0, 2, // target IP
	}
	if got := req.Marshal(); !bytes.Equal(got, want) {
		t.Errorf("request bytes\n got %v\nwant %v", got, want)
	}
}

// reply は sender/target を入れ替え、自分の MAC を sender に入れる。
func TestARPReply(t *testing.T) {
	req := ARPPacket{
		Op:        arpOpRequest,
		SenderMAC: [6]byte{0xaa, 0, 0, 0, 0, 1},
		SenderIP:  [4]byte{10, 0, 0, 9},
		TargetIP:  [4]byte{10, 0, 0, 1},
	}
	self := [6]byte{0xbb, 0, 0, 0, 0, 2}
	reply := newARPReply(self, [4]byte{10, 0, 0, 1}, req)
	if reply.Op != arpOpReply {
		t.Errorf("op = %d, want %d", reply.Op, arpOpReply)
	}
	if reply.SenderMAC != self {
		t.Errorf("sender MAC = %v, want self %v", reply.SenderMAC, self)
	}
	if reply.SenderIP != ([4]byte{10, 0, 0, 1}) {
		t.Errorf("sender IP = %v", reply.SenderIP)
	}
	if reply.TargetMAC != req.SenderMAC || reply.TargetIP != req.SenderIP {
		t.Errorf("target not echoed back: %v %v", reply.TargetMAC, reply.TargetIP)
	}
}

func TestParseARPRejects(t *testing.T) {
	valid := ARPPacket{Op: arpOpRequest}.Marshal()

	if _, err := ParseARP(valid[:27]); err != errARPShort {
		t.Errorf("short: got %v, want errARPShort", err)
	}

	badHW := append([]byte(nil), valid...)
	badHW[1] = 0x09 // hwtype != Ethernet
	if _, err := ParseARP(badHW); err != errARPHWType {
		t.Errorf("hwtype: got %v, want errARPHWType", err)
	}

	badPT := append([]byte(nil), valid...)
	badPT[3] = 0x06 // ptype != IPv4
	if _, err := ParseARP(badPT); err != errARPPType {
		t.Errorf("ptype: got %v, want errARPPType", err)
	}

	badLen := append([]byte(nil), valid...)
	badLen[4] = 8 // hlen != 6
	if _, err := ParseARP(badLen); err != errARPAddrLen {
		t.Errorf("addrlen: got %v, want errARPAddrLen", err)
	}
}

func TestARPTable(t *testing.T) {
	tbl := newARPTable()
	ip1 := [4]byte{10, 0, 0, 1}
	ip2 := [4]byte{10, 0, 0, 2}
	mac1 := [6]byte{1, 1, 1, 1, 1, 1}
	mac2 := [6]byte{2, 2, 2, 2, 2, 2}

	if _, ok := tbl.lookup(ip1); ok {
		t.Error("empty table should miss")
	}
	tbl.update(ip1, mac1)
	tbl.update(ip2, mac2)
	if mac, ok := tbl.lookup(ip1); !ok || mac != mac1 {
		t.Errorf("ip1 = %v %v, want %v hit", mac, ok, mac1)
	}
	if mac, ok := tbl.lookup(ip2); !ok || mac != mac2 {
		t.Errorf("ip2 = %v %v, want %v hit", mac, ok, mac2)
	}
}

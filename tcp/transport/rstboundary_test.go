package transport

import "testing"

// RFC 5961 §3.2 の RST 厳格化の窓境界 off-by-one を突くテスト群。
// 窓内かつ SEG.SEQ==RCV.NXT のときだけ reset。窓内だが !=RCV.NXT は challenge ACK のみ。
// 窓外は silently drop。estab() の窓は [8000, 9000) (RCV.NXT=8000, RCV.WND=1000)。

// 窓内だが RCV.NXT+1 (= RCV.NXT 直後) の RST → challenge のみ、reset しない。
func TestRstAtRcvNxtPlus1Challenges(t *testing.T) {
	c, peer := scaledEstab(t, 0, 0) // estab + shift=0
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: c.tcb.rcv.nxt + 1}, nil)
	if c.State() != Established {
		t.Fatalf("RCV.NXT+1 の RST で reset してはいけない: got %v", c.State())
	}
	if _, ok := drainPeerNonblockKeep(peer); !ok {
		t.Fatal("窓内 !=NXT の RST には challenge ACK が必要")
	}
}

// 窓内上端最後 (RCV.NXT+RCV.WND-1) の RST → 窓内 !=NXT なので challenge、reset しない。
func TestRstAtWindowUpperEdgeChallenges(t *testing.T) {
	c, peer := scaledEstab(t, 0, 0)
	upper := c.tcb.rcv.nxt + c.tcb.rcv.wnd - 1 // 窓内最後
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: upper}, nil)
	if c.State() != Established {
		t.Fatalf("窓内上端の RST で reset してはいけない: got %v", c.State())
	}
	if _, ok := drainPeerNonblockKeep(peer); !ok {
		t.Fatal("窓内上端 RST には challenge ACK が必要")
	}
}

// 窓外最初 (RCV.NXT+RCV.WND) の RST → silently drop (challenge も reset もしない)。
func TestRstAtWindowOuterEdgeDropped(t *testing.T) {
	c, peer := scaledEstab(t, 0, 0)
	outer := c.tcb.rcv.nxt + c.tcb.rcv.wnd // 窓外最初
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: outer}, nil)
	if c.State() != Established {
		t.Fatalf("窓外 RST で状態が変わってはいけない: got %v", c.State())
	}
	if _, ok := drainPeerNonblockKeep(peer); ok {
		t.Fatal("窓外 RST には何も返してはいけない (silently drop)")
	}
}

package tcp

import "testing"

// synAckOpts は SYN-ACK 受信を模す。指定オプションを載せて active open 中の
// Conn へ渡し、折衝結果 (TCB) を観測できるようにする。
func establishActiveWith(t *testing.T, peerOpts TCPOptions) (*Conn, Link) {
	t.Helper()
	c, peer, _ := newTestConn(t)
	c.ActiveOpen(1000)
	drainPeer(t, peer) // 自分の SYN を捨てる
	h := TCPHeader{
		Flags: Flags(FlagSYN | FlagACK), SeqNum: 5000, AckNum: 1001,
		Window: 65535, Options: peerOpts.Marshal(),
	}
	c.onSegment(h, nil)
	return c, peer
}

// 両側が WScale を送ったとき有効になり、shift が保存される。>14 は 14 に clamp。
func TestWScaleNegotiatedBothSides(t *testing.T) {
	// 自分は ActiveOpen で WScale を送る前提。相手も WScale=7 を送る。
	c, _ := establishActiveWith(t, TCPOptions{HasWScale: true, WindowScale: 7})
	if c.tcb.sndWindShift != 7 {
		t.Errorf("sndWindShift=%d want 7 (相手の shift)", c.tcb.sndWindShift)
	}
	if c.tcb.rcvWindShift == 0 {
		t.Errorf("rcvWindShift=0; 自分も WScale を送ったので有効のはず")
	}
}

func TestWScaleClampedAbove14(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{HasWScale: true, WindowScale: 200})
	if c.tcb.sndWindShift != 14 {
		t.Errorf("sndWindShift=%d want 14 (clamp)", c.tcb.sndWindShift)
	}
}

// 相手が WScale を送らなければ両側無効 (shift=0)。
func TestWScaleDisabledWhenPeerOmits(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{}) // 相手オプション無し
	if c.tcb.sndWindShift != 0 || c.tcb.rcvWindShift != 0 {
		t.Errorf("shift should be 0 when peer omits WScale: snd=%d rcv=%d",
			c.tcb.sndWindShift, c.tcb.rcvWindShift)
	}
}

// MSS 交換で SendMSS が相手の MSS になる。
func TestMSSNegotiated(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{HasMSS: true, MSS: 1400})
	if c.tcb.sendMSS != 1400 {
		t.Errorf("sendMSS=%d want 1400", c.tcb.sendMSS)
	}
}

// MSS 未受信なら既定 536 (IPv4)。
func TestMSSDefaultWhenAbsent(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{})
	if c.tcb.sendMSS != 536 {
		t.Errorf("sendMSS=%d want 536 (default)", c.tcb.sendMSS)
	}
}

// timestamps は両側 OK なら有効。
func TestTimestampsNegotiatedBothSides(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{HasTimestamp: true, TSVal: 9999})
	if !c.tcb.tsOK {
		t.Error("tsOK should be true when both sides send timestamps")
	}
}

// 相手が timestamps を送らなければ無効。
func TestTimestampsDisabledWhenPeerOmits(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{})
	if c.tcb.tsOK {
		t.Error("tsOK should be false when peer omits timestamps")
	}
}

// SACK-Permitted は両側 OK なら有効。
func TestSackPermittedNegotiated(t *testing.T) {
	c, _ := establishActiveWith(t, TCPOptions{SACKPermitted: true})
	if !c.tcb.sackOK {
		t.Error("sackOK should be true when both sides permit SACK")
	}
}

// 自分が ActiveOpen で送る SYN には MSS/WScale/TS/SACK-Permitted が載る。
func TestActiveSynCarriesOptions(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.ActiveOpen(1000)
	h, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("SYN not sent")
	}
	o, err := ParseTCPOptions(h.Options)
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasMSS || !o.HasWScale || !o.HasTimestamp || !o.SACKPermitted {
		t.Errorf("SYN missing options: %+v", o)
	}
}

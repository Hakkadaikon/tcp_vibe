package tcp

import "testing"

// Window Scale のスケール演算が実際に送受信へ効くことを突くテスト群 (RFC 7323 §2.3)。
// 折衝後の shift 保存値だけでなく、入力 SEG.WND の左シフトと出力 SEG.WND の右シフトが
// 内部窓・広告窓・送出量へ反映されることを確認する。

// scaledEstab は shift を折衝済みにした ESTABLISHED Conn を返す。
// 入力 shift (Snd.Wind.Shift) と出力 shift (Rcv.Wind.Shift) を別々に指定できる。
func scaledEstab(t *testing.T, sndShift, rcvShift uint8) (*Conn, Link) {
	t.Helper()
	c, peer, _ := estab(t)
	c.tcb.sndWindShift = sndShift
	c.tcb.rcvWindShift = rcvShift
	// updateSendWindow の WL1/WL2 順序判定を通せるよう、過去の窓更新点を手前に置く。
	c.tcb.snd.wl1 = c.tcb.rcv.nxt - 1
	c.tcb.snd.wl2 = c.tcb.snd.una
	return c, peer
}

// 入力: 非 SYN セグメントの SEG.WND は Snd.Wind.Shift で左シフトされ SND.WND になる。
// shift=7, SEG.WND=10 → SND.WND==10<<7==1280。さらにその窓ぶんだけ送れる。
func TestInputWindowLeftShiftedIntoSndWnd(t *testing.T) {
	c, peer := scaledEstab(t, 7, 7)
	// cwnd は窓検証の邪魔をしないよう十分大きく。
	c.tcb.cong.cwnd = 1 << 20

	// SEG.WND=10 の ACK を入れる (seq=RCV.NXT で受理性 OK)。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt, AckNum: c.tcb.snd.una, Window: 10}, nil)

	if c.tcb.snd.wnd != 10<<7 {
		t.Fatalf("SND.WND がスケールされていない: got %d want %d", c.tcb.snd.wnd, 10<<7)
	}

	// 送出量が SND.WND(=1280) に従うこと。MSS 1360 より小さいので 1 セグメント=1280 になる。
	if _, err := c.Send(make([]byte, 4000)); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	first, ok := drainPeer(t, peer)
	_ = first
	if !ok {
		t.Fatal("データセグメントが送られていない")
	}
	// 送出済みバイト = SND.NXT-SND.UNA は SND.WND(1280) を超えない。
	inflight := c.SndNxt() - c.SndUna()
	if inflight > 1280 {
		t.Fatalf("送出量が SND.WND を超えた: inflight=%d want<=1280", inflight)
	}
	if inflight != 1280 {
		t.Fatalf("窓いっぱい(1280)まで送るはず: inflight=%d", inflight)
	}
}

// 出力: 非 SYN 送出セグメントの広告 Window は RCV.WND を Rcv.Wind.Shift で右シフトした値。
// RCV.WND=0x8000(32768), shift=7 → 広告 Window==32768>>7==256。
func TestOutputWindowRightShiftedFromRcvWnd(t *testing.T) {
	c, peer := scaledEstab(t, 7, 7)
	c.tcb.rcv.wnd = 0x8000 // 32768

	// 何か ACK を返させる: 受理不可セグメントへの空 ACK で広告窓を観測する。
	// seq を窓外にして acceptable=false → sendAck が走る。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt + c.tcb.rcv.wnd + 100, AckNum: c.tcb.snd.una}, nil)
	h, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("ACK が送られていない")
	}
	if h.Window != 0x8000>>7 {
		t.Fatalf("広告 Window が右シフトされていない: got %d want %d", h.Window, 0x8000>>7)
	}
}

// 境界: shift=14 でも左シフトが効く。SEG.WND=3, shift=14 → SND.WND==3<<14==49152。
func TestInputWindowLeftShiftMax(t *testing.T) {
	c, _ := scaledEstab(t, 14, 14)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt, AckNum: c.tcb.snd.una, Window: 3}, nil)
	if c.tcb.snd.wnd != 3<<14 {
		t.Fatalf("shift=14 で左シフトされていない: got %d want %d", c.tcb.snd.wnd, 3<<14)
	}
}

// 境界: 右シフトで小さい RCV.WND が 0 に落ちる。RCV.WND=100, shift=7 → 100>>7==0。
// 広告窓が 0 になる (相手は zero-window と見なす) ことを確認する。
func TestOutputWindowRightShiftUnderflowsToZero(t *testing.T) {
	c, peer := scaledEstab(t, 7, 7)
	c.tcb.rcv.wnd = 100
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt + c.tcb.rcv.wnd + 100, AckNum: c.tcb.snd.una}, nil)
	h, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("ACK が送られていない")
	}
	if h.Window != 0 {
		t.Fatalf("右シフトで 0 に落ちるはず: got %d", h.Window)
	}
}

// SYN/SYN-ACK の Window は折衝前の生値 (スケール無し) で送る (RFC 7323 §2.3)。
// 内部 RCV.WND は 65535 (defaultRcvWindow) なので、SYN の Window も 65535 (生値)。
// もしここで rcvWindShift が適用されてしまうと 65535>>7 になり落ちる。
func TestSynWindowIsRawNotScaled(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.ActiveOpen(1000)
	syn, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("SYN が送られていない")
	}
	if !syn.Flags.Has(FlagSYN) {
		t.Fatalf("SYN のはず: flags=%v", syn.Flags)
	}
	if syn.Window != maxWindow {
		t.Fatalf("SYN の Window は生値 (65535) のはず: got %d", syn.Window)
	}
}

// SYN-ACK の Window も生値で送る。LISTEN→SYN 受信で SYN-ACK を観測する。
func TestSynAckWindowIsRawNotScaled(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.tcb.snd.iss = 7000
	c.PassiveOpen()
	// 相手 SYN は WScale を提示し、自分の rcvWindShift を非 0 にさせる。
	syn := TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000,
		Options: TCPOptions{HasWScale: true, WindowScale: 7}.Marshal()}
	c.onSegment(syn, nil)
	synAck, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("SYN-ACK が送られていない")
	}
	if !synAck.Flags.Has(FlagSYN) || !synAck.Flags.Has(FlagACK) {
		t.Fatalf("SYN-ACK のはず: flags=%v", synAck.Flags)
	}
	// rcvWindShift は折衝で 7 になっているが、SYN-ACK は生値で広告する。
	if c.tcb.rcvWindShift != myWindowScale {
		t.Fatalf("rcvWindShift が折衝されていない: got %d", c.tcb.rcvWindShift)
	}
	if synAck.Window != maxWindow {
		t.Fatalf("SYN-ACK の Window は生値 (65535) のはず: got %d (shift 適用なら %d)",
			synAck.Window, uint32(maxWindow)>>myWindowScale)
	}
}

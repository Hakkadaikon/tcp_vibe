package transport

import "testing"

// MSS / SACK-Permitted は SYN・SYN-ACK でのみ交換する option (RFC 9293 §3.1, RFC 2018)。
// 通常 (非 SYN) のデータ/ACK セグメントには載らないことを否定テストで突く。
func TestNonSynSegmentOmitsSynOnlyOptions(t *testing.T) {
	// timestamps を折衝した ESTABLISHED を組む (TS option は非 SYN にも載る = ノイズ)。
	c, peer := establishActiveWith(t, TCPOptions{
		HasMSS: true, MSS: 1400, HasWScale: true, WindowScale: 7,
		HasTimestamp: true, TSVal: 1, SACKPermitted: true,
	})
	drainPeer(t, peer) // 握手の ACK を読み捨て

	// データを送る → 通常の PSH|ACK セグメントを観測する。
	c.tcb.snd.wnd = 65535
	c.tcb.cong.cwnd = 65535
	if _, err := c.Send([]byte("payload")); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	h, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("データセグメントが送られていない")
	}
	if h.Flags.Has(FlagSYN) {
		t.Fatalf("通常セグメントが SYN を持っている: %v", h.Flags)
	}
	o, err := ParseTCPOptions(h.Options)
	if err != nil {
		t.Fatalf("option parse 失敗: %v", err)
	}
	if o.HasMSS {
		t.Error("非 SYN セグメントに MSS option が載っている (SYN 限定のはず)")
	}
	if o.SACKPermitted {
		t.Error("非 SYN セグメントに SACK-Permitted option が載っている (SYN 限定のはず)")
	}
	if o.HasWScale {
		t.Error("非 SYN セグメントに Window Scale option が載っている (SYN 限定のはず)")
	}
}

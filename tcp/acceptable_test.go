package tcp

import "testing"

// 受理性テスト (RFC 9293 §3.10.7.4) の 4 ケース × 境界を直接突くデシジョンテーブル。
// acceptable() を TCB 直叩きで検証する (onSegment を通さず純粋判定を見る)。
//
//	SEG.LEN  RCV.WND   受理条件
//	  0        0       SEG.SEQ == RCV.NXT のみ
//	  0       >0       RCV.NXT =< SEG.SEQ < RCV.NXT+RCV.WND
//	 >0        0       常に不受理
//	 >0       >0       始端 or 終端のいずれかが窓内
func TestAcceptableDecisionTable(t *testing.T) {
	const nxt = 1000
	hdr := func(seq uint32) TCPHeader { return TCPHeader{SeqNum: seq, Flags: Flags(FlagACK)} }
	data := func(n int) []byte { return make([]byte, n) }

	cases := []struct {
		name    string
		wnd     uint32
		seq     uint32
		payload []byte
		want    bool
	}{
		// SEG.LEN=0, RCV.WND=0: SEG.SEQ==RCV.NXT のみ受理。
		{"len0/wnd0/seq==nxt", 0, nxt, nil, true},
		{"len0/wnd0/seq!=nxt", 0, nxt + 1, nil, false},
		{"len0/wnd0/seq<nxt", 0, nxt - 1, nil, false},

		// SEG.LEN=0, RCV.WND>0: [RCV.NXT, RCV.NXT+RCV.WND) に入るか。
		{"len0/wnd100/at-nxt", 100, nxt, nil, true},
		{"len0/wnd100/at-upper-in", 100, nxt + 99, nil, true},    // 上端窓内最後 (RCV.NXT+N-1)
		{"len0/wnd100/at-upper-out", 100, nxt + 100, nil, false}, // 窓外最初 (RCV.NXT+N)
		{"len0/wnd100/below", 100, nxt - 1, nil, false},

		// SEG.LEN>0, RCV.WND=0: 常に不受理 (probe であっても本実装は弾く)。
		{"len1/wnd0/at-nxt", 0, nxt, data(1), false},
		{"len5/wnd0/at-nxt", 0, nxt, data(5), false},

		// SEG.LEN>0, RCV.WND>0: 始端 or 終端が窓内。
		{"len10/wnd100/fully-in", 100, nxt, data(10), true},
		{"len1/wnd100/last-in-window", 100, nxt + 99, data(1), true},    // 始端=終端=上端窓内最後
		{"len1/wnd100/first-out", 100, nxt + 100, data(1), false},       // 始端窓外最初
		{"len10/wnd5/straddle-start-in", 5, nxt + 3, data(10), true},    // 始端窓内・終端窓外
		{"len10/wnd5/straddle-end-in", 5, nxt - 5, data(10), true},      // 始端窓外・終端窓内
		{"len5/wnd100/all-below-window", 100, nxt - 10, data(5), false}, // 始端終端とも窓手前
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tcb := &TCB{}
			tcb.rcv.nxt = nxt
			tcb.rcv.wnd = tc.wnd
			got := tcb.acceptable(hdr(tc.seq), tc.payload)
			if got != tc.want {
				t.Fatalf("acceptable(seq=%d len=%d wnd=%d)=%v want %v",
					tc.seq, len(tc.payload), tc.wnd, got, tc.want)
			}
		})
	}
}

// 受信窓を跨ぐデータ (始端窓内・終端窓外) は onSegment 経由でも受理され、窓内ぶんだけ
// 取り込まれて RCV.NXT が窓端まで前進する (trim される)。
func TestStraddlingSegmentTrimmedToWindow(t *testing.T) {
	c, peer := scaledEstab(t, 0, 0)
	c.tcb.rcv.wnd = 5
	c.tcb.rcvBuffTotal = 5 // 窓再計算で広げないよう小さく固定
	nxt := c.tcb.rcv.nxt
	// 窓 [nxt, nxt+5) に 10 バイト: 始端 nxt は窓内、終端 nxt+9 は窓外。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK | FlagPSH), SeqNum: nxt, AckNum: c.tcb.snd.una}, make([]byte, 10))
	if c.RcvNxt() != nxt+5 {
		t.Fatalf("窓ぶん(5)だけ取り込むはず: RcvNxt %d -> %d want %d", nxt, c.RcvNxt(), nxt+5)
	}
	drainPeerNonblockKeep(peer) // ACK が出る
}

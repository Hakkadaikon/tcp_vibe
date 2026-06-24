package transport

import (
	"testing"
	"time"
)

// PAWS の古さ判定 (RFC 7323 §5)。timestamp も 32bit wrap で SeqLT 比較する。
// 境界値は形式検証で確定したもの:
//
//	等値 → 棄却しない / 新しい → 棄却しない / 古い → 棄却 /
//	wrap: drop(0, 0xFFFFFFFF)=false (0 は max より新しい) /
//	wrap: drop(0xFFFFFFFF, 0)=true (max は 0 より古い)。
func TestPawsStale(t *testing.T) {
	cases := []struct {
		tsval, recent uint32
		wantDrop      bool
	}{
		{1000, 1000, false}, // 等値は古くない
		{1001, 1000, false}, // 新しい
		{999, 1000, true},   // 古い
		{0, 0xFFFFFFFF, false},
		{0xFFFFFFFF, 0, true},
	}
	for _, tc := range cases {
		if got := pawsStale(tc.tsval, tc.recent); got != tc.wantDrop {
			t.Errorf("pawsStale(%d,%d)=%v want %v", tc.tsval, tc.recent, got, tc.wantDrop)
		}
	}
}

// TS.Recent 更新規則 (RFC 7323 §4.3):
// SEG.TSval >= TS.Recent (wrap) かつ SEG.SEQ <= Last.ACK.sent のときだけ更新。
func TestTsRecentUpdate(t *testing.T) {
	cases := []struct {
		recent, tsval, seq, lastAck, want uint32
	}{
		{1000, 1005, 50, 100, 1005},  // fresh かつ seq gate 通過 → 更新
		{1000, 1005, 200, 100, 1000}, // seq > lastAck → 据え置き
		{1000, 999, 50, 100, 1000},   // stale → 据え置き
	}
	for _, tc := range cases {
		if got := tsRecentUpdate(tc.recent, tc.tsval, tc.seq, tc.lastAck); got != tc.want {
			t.Errorf("tsRecentUpdate(%d,%d,%d,%d)=%d want %d",
				tc.recent, tc.tsval, tc.seq, tc.lastAck, got, tc.want)
		}
	}
}

// TS.Recent は wrap 順序で後退しない (形式検証の単調性をテストに橋渡し)。
func TestTsRecentMonotone(t *testing.T) {
	f := func(recent, tsval, seq, lastAck uint32) bool {
		next := tsRecentUpdate(recent, tsval, seq, lastAck)
		// next >= recent (wrap)。SeqLT(next, recent) が成り立ってはならない。
		return !SeqLT(next, recent)
	}
	// 代表値 + wrap 境界を網羅。
	vals := []uint32{0, 1, 100, 1000, 0x7FFFFFFF, 0x80000000, 0xFFFFFFFF}
	for _, r := range vals {
		for _, ts := range vals {
			for _, s := range []uint32{50, 200} {
				if !f(r, ts, s, 100) {
					t.Errorf("monotone violated: recent=%d tsval=%d seq=%d", r, ts, s)
				}
			}
		}
	}
}

// timestamp clock は単調増加 (時刻が進めば値も進む / 後退しない)。
func TestTimestampClockMonotone(t *testing.T) {
	fc := newFakeClock()
	c := NewConn(nil, fc.Now, testLocal, testRemote)
	t0 := c.tcb.tsNow()
	fc.advance(5 * time.Millisecond)
	t1 := c.tcb.tsNow()
	if SeqLT(t1, t0) {
		t.Errorf("timestamp clock went backward: %d -> %d", t0, t1)
	}
	if t1 == t0 {
		t.Errorf("timestamp clock did not advance: %d", t1)
	}
}

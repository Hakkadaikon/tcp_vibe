package tcp

import (
	"math"
	"testing"
	"testing/quick"
)

// T-010: SEG_LT 境界 (mod 2^32 環状比較)
func TestSeqLT_Boundaries(t *testing.T) {
	const max = math.MaxUint32 // 2^32-1
	cases := []struct {
		a, b uint32
		want bool
		name string
	}{
		{0, 1, true, "0<1"},
		{max, 0, true, "wrap: (2^32-1)<0"},    // max の次は 0
		{0, max, false, "0<(2^32-1) は false"}, // 0 から見て max は手前(直前)
		{5, 5, false, "x<x は false"},
		{1, 0, false, "1<0 は false"},
	}
	for _, c := range cases {
		if got := SeqLT(c.a, c.b); got != c.want {
			t.Errorf("%s: SeqLT(%d,%d)=%v want %v", c.name, c.a, c.b, got, c.want)
		}
	}
}

// T-011: SEG_LEQ / GT 境界
func TestSeqLEQ_GT(t *testing.T) {
	if !SeqLEQ(5, 5) {
		t.Error("SeqLEQ(x,x) should be true")
	}
	if !SeqLEQ(0, 1) {
		t.Error("SeqLEQ(0,1) should be true")
	}
	if !SeqGT(1, 0) {
		t.Error("SeqGT(1,0) should be true")
	}
	if SeqGT(5, 5) {
		t.Error("SeqGT(x,x) should be false")
	}
}

// T-012: 三分律・反対称 (半周内) — property-based
func TestSeqLT_Trichotomy(t *testing.T) {
	// 半周 (2^31) 未満の距離なら厳密な全順序。
	f := func(a uint32, d uint16) bool {
		b := a + uint32(d) // 距離 d < 2^16 < 2^31, 確実に半周内
		lt := SeqLT(a, b)
		gt := SeqLT(b, a)
		eq := a == b
		// 三分律: ちょうど1つだけ真
		count := 0
		if lt {
			count++
		}
		if gt {
			count++
		}
		if eq {
			count++
		}
		return count == 1
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

func TestSeqLT_Antisymmetry(t *testing.T) {
	f := func(a uint32, d uint16) bool {
		if d == 0 {
			return true // a==b は対象外
		}
		b := a + uint32(d)
		// 半周内で a<b なら ¬(b<a)
		return !(SeqLT(a, b) && SeqLT(b, a))
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

// 環状比較の subtlety: 対蹠点 (距離ちょうど 2^31) では順序が定まらない
// (RFC 9293:876 "subtleties to computer modulo arithmetic")。
// 本実装の "距離 < 2^31" 定義では対蹠点は両方向 false (三分律が破れる: a≠b なのに
// a<b でも b<a でもない)。TCP は窓を 2^31 未満に保つことでこの領域を踏まない。
// 回帰防止に「対蹠点では信頼できる順序が出ない」ことを固定する。
func TestSeqLT_AntipodeIsAmbiguous(t *testing.T) {
	const half uint32 = 1 << 31
	a, b := uint32(0), half // a-b = 2^31 (対蹠点)
	if SeqLT(a, b) || SeqLT(b, a) {
		t.Errorf("対蹠点では順序が定まらない (両方向 false) のはず: SeqLT(%d,%d)=%v SeqLT(%d,%d)=%v",
			a, b, SeqLT(a, b), b, a, SeqLT(b, a))
	}
}

// 窓を 2^31 未満に保てば反対称律は成立する (Lean で証明した境界)。
func TestSeqLT_AntisymmetryWithinHalfWindow(t *testing.T) {
	f := func(a uint32, d uint32) bool {
		d = d % (1 << 31) // 半周未満に制限
		if d == 0 {
			return true
		}
		b := a + d
		return !(SeqLT(a, b) && SeqLT(b, a)) // 半周内なら一方向のみ
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

// T-013: acceptable ACK 判定 SND.UNA < SEG.ACK =< SND.NXT
func TestAcceptableAck_Boundaries(t *testing.T) {
	const una, nxt uint32 = 100, 200
	cases := []struct {
		ack  uint32
		want bool
		name string
	}{
		{una, false, "ack==UNA は不可 (重複)"},
		{una + 1, true, "ack==UNA+1 は可"},
		{nxt, true, "ack==NXT は可"},
		{nxt + 1, false, "ack==NXT+1 は不可 (未送信)"},
		{150, true, "中間は可"},
	}
	for _, c := range cases {
		if got := AcceptableAck(una, c.ack, nxt); got != c.want {
			t.Errorf("%s: AcceptableAck(%d,%d,%d)=%v want %v", c.name, una, c.ack, nxt, got, c.want)
		}
	}
}

// T-013 (wrap): SND.UNA/SND.NXT がラップを跨ぐケース
func TestAcceptableAck_Wrap(t *testing.T) {
	const max uint32 = math.MaxUint32
	una := max - 5 // 2^32-6
	nxt := uint32(5)
	if !AcceptableAck(una, 0, nxt) {
		t.Error("wrap: ack=0 should be acceptable in (max-5, 5]")
	}
	if !AcceptableAck(una, max, nxt) {
		t.Error("wrap: ack=max should be acceptable")
	}
	if AcceptableAck(una, una, nxt) {
		t.Error("wrap: ack==una not acceptable")
	}
	if AcceptableAck(una, 6, nxt) {
		t.Error("wrap: ack=6 (>nxt) not acceptable")
	}
}

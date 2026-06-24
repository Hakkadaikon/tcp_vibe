package transport

import "testing"

// InitEst の具体値 (RFC 6298 §2.2): SRTT=R, RTTVAR=R/2。
func TestInitEst(t *testing.T) {
	e := initEst(300, 1)
	if e.srtt != 300 || e.rttvar != 150 || e.g != 1 {
		t.Fatalf("initEst(300,1) = %+v, want {300 150 1}", e)
	}
}

// Rto の具体値 (RFC 6298 §2.2 + §2.4 下限)。
//
//	R=300: RTO=300+max(1,4*150)=900 → 下限 1000 へ。
//	R=2000: RTO=2000+max(1,4*1000)=6000 (下限超え)。
func TestRtoConcrete(t *testing.T) {
	if got := initEst(300, 1).Rto(); got != 1000 {
		t.Fatalf("Rto(initEst(300,1)) = %d, want 1000", got)
	}
	if got := initEst(2000, 1).Rto(); got != 6000 {
		t.Fatalf("Rto(initEst(2000,1)) = %d, want 6000", got)
	}
}

// RTO は常に下限 1000ms 以上 (RFC 6298 §2.4)。任意の e で破れない。
func TestRtoFloorAlways(t *testing.T) {
	cases := []rttEstimator{
		{0, 0, 0}, {10, 5, 1}, {1, 0, 0}, {999, 0, 1}, {500, 0, 0},
	}
	for _, e := range cases {
		if got := e.Rto(); got < 1000 {
			t.Fatalf("Rto(%+v) = %d < 1000", e, got)
		}
	}
}

// RTTVAR=0 で Rto の生値は SRTT+G (RFC 6298 §2.3 の granularity branch)。
func TestRtoRttvarZeroIsSrttPlusG(t *testing.T) {
	// SRTT+G が下限を超えるケースで生値の式を確認する。
	e := rttEstimator{srtt: 1500, rttvar: 0, g: 1}
	if got := e.Rto(); got != 1501 {
		t.Fatalf("Rto({1500,0,1}) = %d, want 1501", got)
	}
}

// UpdateEst は順序厳守 (RTTVAR が旧 SRTT を使う)。R=300,G=1 に R'=340:
//
//	RTTVAR' = (3*150 + |300-340|)/4 = (450+40)/4 = 122
//	SRTT'   = (7*300 + 340)/8 = 2440/8 = 305
func TestUpdateEstOrder(t *testing.T) {
	e := initEst(300, 1).UpdateEst(340)
	if e.srtt != 305 || e.rttvar != 122 {
		t.Fatalf("UpdateEst = %+v, want srtt=305 rttvar=122", e)
	}
}

// 有界サンプルで SRTT/RTTVAR が発散しない: 全サンプル <= B なら更新後も <= B。
func TestUpdateEstBounded(t *testing.T) {
	const B = 500
	e := initEst(B, 1) // srtt=500, rttvar=250 <= B
	for i := 0; i < 1000; i++ {
		r := uint32((i*37 + 13) % (B + 1)) // 0..B の擬似ランダム標本
		e = e.UpdateEst(r)
		if e.srtt > B || e.rttvar > B {
			t.Fatalf("発散: i=%d e=%+v B=%d", i, e, B)
		}
	}
}

// Backoff は単調 (cur<=Backoff<=cap)。具体境界 (RFC 6298 §5.5)。
func TestBackoff(t *testing.T) {
	if got := backoff(60000, 1000); got != 2000 {
		t.Fatalf("backoff(60000,1000) = %d, want 2000", got)
	}
	if got := backoff(60000, 40000); got != 60000 {
		t.Fatalf("backoff(60000,40000) = %d, want 60000 (cap)", got)
	}
	// 単調性: cur<=cap なら cur<=backoff<=cap。
	for _, cur := range []uint32{1, 1000, 30000, 59999, 60000} {
		b := backoff(60000, cur)
		if b < cur || b > 60000 {
			t.Fatalf("backoff(60000,%d)=%d 単調性破れ", cur, b)
		}
	}
}

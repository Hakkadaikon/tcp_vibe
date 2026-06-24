package transport

import "testing"

// テストはすべて SMSS = defaultMSS (1360) を前提にする。
const tSMSS = defaultMSS

func newCong() *congestion { return newCongestion(tSMSS) }

// IW (初期ウィンドウ) は SMSS で 3 段階 (RFC 5681 §3.1)。
func TestInitialWindow(t *testing.T) {
	cases := []struct {
		smss uint32
		want uint32
	}{
		{1000, 4 * 1000}, // <=1095 → 4*SMSS
		{1095, 4 * 1095},
		{1096, 3 * 1096}, // <=2190 → 3*SMSS
		{2190, 3 * 2190},
		{2191, 2 * 2191}, // それ以外 → 2*SMSS
		{4000, 2 * 4000},
	}
	for _, tc := range cases {
		if got := initialWindow(tc.smss); got != tc.want {
			t.Fatalf("initialWindow(%d) = %d, want %d", tc.smss, got, tc.want)
		}
	}
}

// 初期状態: cwnd=IW, ssthresh は高く, state=SlowStart。
func TestCongestionInit(t *testing.T) {
	c := newCong()
	if c.cwnd != initialWindow(tSMSS) {
		t.Fatalf("初期 cwnd = %d, want IW=%d", c.cwnd, initialWindow(tSMSS))
	}
	if c.ssthresh < 2*tSMSS {
		t.Fatalf("初期 ssthresh = %d, want 大きい値 (>=2SMSS)", c.ssthresh)
	}
	if c.state != ccSlowStart {
		t.Fatalf("初期 state = %v, want SlowStart", c.state)
	}
}

// Slow Start: 1 ACK あたり cwnd += min(N, SMSS)。1 回で SMSS を超えない。
func TestSlowStartIncrement(t *testing.T) {
	c := newCong()
	c.ssthresh = 1 << 30 // 十分高く → 必ず SS
	start := c.cwnd

	// N=SMSS の確認 → ちょうど SMSS 増える。
	c.onNewAck(tSMSS, 10*tSMSS)
	if c.cwnd != start+tSMSS {
		t.Fatalf("SS +SMSS: cwnd=%d, want %d", c.cwnd, start+tSMSS)
	}

	// N > SMSS の確認 → min で SMSS に丸まる (1 回で SMSS 超えない)。
	c2 := newCong()
	c2.ssthresh = 1 << 30
	s2 := c2.cwnd
	c2.onNewAck(5*tSMSS, 10*tSMSS)
	if c2.cwnd != s2+tSMSS {
		t.Fatalf("SS は 1 回で最大 SMSS: cwnd=%d, want %d", c2.cwnd, s2+tSMSS)
	}

	// N < SMSS の確認 → N だけ増える。
	c3 := newCong()
	c3.ssthresh = 1 << 30
	s3 := c3.cwnd
	c3.onNewAck(100, 10*tSMSS)
	if c3.cwnd != s3+100 {
		t.Fatalf("SS +N (N<SMSS): cwnd=%d, want %d", c3.cwnd, s3+100)
	}
}

// cwnd が ssthresh に到達したら SS → CA。
func TestSlowStartToCongestionAvoidance(t *testing.T) {
	c := newCong()
	c.ssthresh = c.cwnd + tSMSS // 次の +SMSS でちょうど到達
	c.onNewAck(tSMSS, 10*tSMSS)
	if c.cwnd < c.ssthresh {
		t.Fatalf("ssthresh 到達: cwnd=%d ssthresh=%d", c.cwnd, c.ssthresh)
	}
	if c.state != ccCongestionAvoidance {
		t.Fatalf("ssthresh 到達で CA のはず: state=%v", c.state)
	}
}

// Congestion Avoidance: 1 RTT (= cwnd バイト確認) ごとに +SMSS。1 回で SMSS 超えない。
func TestCongestionAvoidanceIncrement(t *testing.T) {
	c := newCong()
	c.state = ccCongestionAvoidance
	c.cwnd = 10 * tSMSS
	c.ssthresh = tSMSS // cwnd > ssthresh
	start := c.cwnd

	// cwnd 未満の累積では増えない。
	c.onNewAck(tSMSS, 100*tSMSS)
	if c.cwnd != start {
		t.Fatalf("CA: cwnd 未満の確認では増えない: cwnd=%d, want %d", c.cwnd, start)
	}
	// 累積が cwnd に達したら +SMSS (合計 cwnd バイトで 1 回だけ)。
	c.onNewAck(c.cwnd-tSMSS, 100*tSMSS)
	if c.cwnd != start+tSMSS {
		t.Fatalf("CA: 1 RTT で +SMSS: cwnd=%d, want %d", c.cwnd, start+tSMSS)
	}
}

// 3 dup ACK で Fast Recovery 入り: ssthresh 半減を先に, cwnd=ssthresh+3SMSS。
// 1, 2 番目の dup では cwnd 不変 (Limited Transmit)。
func TestThreeDupAckEntersFastRecovery(t *testing.T) {
	c := newCong()
	c.cwnd = 10 * tSMSS
	flight := uint32(10 * tSMSS)
	cwndBefore := c.cwnd

	// 1, 2 番目: cwnd 不変。
	c.onDupAck(flight)
	c.onDupAck(flight)
	if c.cwnd != cwndBefore {
		t.Fatalf("1,2 番目の dup で cwnd 不変のはず: cwnd=%d, want %d", c.cwnd, cwndBefore)
	}
	if c.state == ccFastRecovery {
		t.Fatalf("3 つ目の前に FR に入ってはいけない")
	}

	// 3 番目: FR 入り。
	c.onDupAck(flight)
	wantSsthresh := maxU32(flight/2, 2*tSMSS)
	if c.ssthresh != wantSsthresh {
		t.Fatalf("FR 入り ssthresh=%d, want max(FlightSize/2,2SMSS)=%d", c.ssthresh, wantSsthresh)
	}
	if c.cwnd != c.ssthresh+3*tSMSS {
		t.Fatalf("FR 入り cwnd=%d, want ssthresh+3SMSS=%d", c.cwnd, c.ssthresh+3*tSMSS)
	}
	if c.state != ccFastRecovery {
		t.Fatalf("3 dup で FR のはず: state=%v", c.state)
	}
}

// FR 中の追加 dup ACK で cwnd += SMSS。
func TestFastRecoveryInflate(t *testing.T) {
	c := newCong()
	c.cwnd = 10 * tSMSS
	flight := uint32(10 * tSMSS)
	c.onDupAck(flight)
	c.onDupAck(flight)
	c.onDupAck(flight) // FR 入り
	cwndAtEntry := c.cwnd
	c.onDupAck(flight) // 追加 dup
	if c.cwnd != cwndAtEntry+tSMSS {
		t.Fatalf("FR 中の追加 dup で +SMSS: cwnd=%d, want %d", c.cwnd, cwndAtEntry+tSMSS)
	}
}

// FR を抜ける新規 ACK で deflate: cwnd = ssthresh。
func TestFastRecoveryDeflate(t *testing.T) {
	c := newCong()
	c.cwnd = 10 * tSMSS
	flight := uint32(10 * tSMSS)
	c.onDupAck(flight)
	c.onDupAck(flight)
	c.onDupAck(flight) // FR 入り
	wantSsthresh := c.ssthresh

	c.onNewAck(tSMSS, flight) // 回復完了の新規 ACK
	if c.cwnd != wantSsthresh {
		t.Fatalf("deflate: cwnd=%d, want ssthresh=%d", c.cwnd, wantSsthresh)
	}
	if c.state == ccFastRecovery {
		t.Fatalf("新規 ACK で FR を抜けるはず: state=%v", c.state)
	}
}

// RTO 満了: cwnd=1SMSS (LW), ssthresh=max(FlightSize/2,2SMSS) (初回再送のみ)。
func TestRtoTimeoutResetsWindow(t *testing.T) {
	c := newCong()
	c.cwnd = 20 * tSMSS
	flight := uint32(20 * tSMSS)
	c.onRtoTimeout(flight)
	if c.cwnd != tSMSS {
		t.Fatalf("RTO 後 cwnd=%d, want 1SMSS=%d", c.cwnd, tSMSS)
	}
	if c.ssthresh != maxU32(flight/2, 2*tSMSS) {
		t.Fatalf("RTO 後 ssthresh=%d, want max(FlightSize/2,2SMSS)=%d", c.ssthresh, maxU32(flight/2, 2*tSMSS))
	}
	if c.state != ccSlowStart {
		t.Fatalf("RTO 後は SlowStart のはず: state=%v", c.state)
	}
}

// 初回半減 vs 再送済み保持: 同一損失エピソードでは 2 回目の RTO で ssthresh を変えない。
// 新規 ACK でフラグがクリアされたら、再び半減しうる。
func TestRtoSsthreshFirstHalveThenHold(t *testing.T) {
	c := newCong()
	c.cwnd = 20 * tSMSS
	flight1 := uint32(20 * tSMSS)

	// 初回 RTO: 半減する。
	c.onRtoTimeout(flight1)
	ssthreshAfterFirst := c.ssthresh

	// 同一損失エピソードでの 2 回目 RTO: ssthresh は保持 (flight は小さくとも変えない)。
	c.cwnd = 4 * tSMSS // バックオフ後に少し送れて再びタイムアウトした想定
	c.onRtoTimeout(uint32(4 * tSMSS))
	if c.ssthresh != ssthreshAfterFirst {
		t.Fatalf("2 回目 RTO で ssthresh を保持すべき: got %d, want %d", c.ssthresh, ssthreshAfterFirst)
	}
	if c.cwnd != tSMSS {
		t.Fatalf("2 回目 RTO でも cwnd=1SMSS: got %d", c.cwnd)
	}

	// 新規 ACK でフラグがクリアされる → 次の RTO は再び半減しうる。
	c.onNewAck(tSMSS, uint32(2*tSMSS))
	c.cwnd = 16 * tSMSS
	flight3 := uint32(16 * tSMSS)
	c.onRtoTimeout(flight3)
	if c.ssthresh != maxU32(flight3/2, 2*tSMSS) {
		t.Fatalf("フラグクリア後の RTO は再び半減すべき: got %d, want %d", c.ssthresh, maxU32(flight3/2, 2*tSMSS))
	}
}

// 不変条件 (property): どの操作列の後でも cwnd>=1SMSS かつ ssthresh>=2SMSS。
func TestCongestionInvariants(t *testing.T) {
	c := newCong()
	flights := []uint32{0, 1, tSMSS, 3 * tSMSS, 100 * tSMSS}
	// 操作を擬似ランダムに混ぜて不変条件を突く。
	for i := 0; i < 2000; i++ {
		f := flights[i%len(flights)]
		switch i % 4 {
		case 0:
			c.onNewAck(uint32((i*53)%(5*tSMSS)+1), f)
		case 1:
			c.onDupAck(f)
		case 2:
			c.onRtoTimeout(f)
		case 3:
			c.onNewAck(tSMSS, f)
		}
		if c.cwnd < tSMSS {
			t.Fatalf("i=%d cwnd=%d < 1SMSS", i, c.cwnd)
		}
		if c.ssthresh < 2*tSMSS {
			t.Fatalf("i=%d ssthresh=%d < 2SMSS", i, c.ssthresh)
		}
	}
}

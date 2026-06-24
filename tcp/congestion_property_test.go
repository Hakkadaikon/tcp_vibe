package tcp

import (
	"testing"
	"testing/quick"
)

// 輻輳ウィンドウ増加則の性質を testing/quick で橋渡しする。
// ackedBytes 列を quick が生成し、SlowStart / CongestionAvoidance 双方の
// 増分上限と byte-counting の健全性を検査する。

// SS 増分 <= SMSS: cwnd<ssthresh のとき、任意 ackedBytes の onNewAck 1 回で
// cwnd の増分は SMSS を超えない (RFC 5681 §3.1: cwnd += min(N, SMSS))。
func TestSlowStartIncrementBoundedProperty(t *testing.T) {
	f := func(acked uint32) bool {
		c := newCong()
		c.ssthresh = 1 << 30 // 十分高く → 必ず SS のまま
		before := c.cwnd
		c.onNewAck(acked, 100*tSMSS)
		return c.cwnd-before <= tSMSS
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
		t.Error(err)
	}
}

// CA 1RTT で <= SMSS: cwnd>=ssthresh のとき、合計が cwnd 未満の ACK 列を流すと
// cwnd は不変 (1 RTT = cwnd バイト確認あたり高々 1 SMSS, RFC 5681 §3.1 式2)。
func TestCongestionAvoidanceNoIncreaseBelowRttProperty(t *testing.T) {
	f := func(chunks []uint16) bool {
		c := newCong()
		c.state = ccCongestionAvoidance
		c.cwnd = 20 * tSMSS
		c.ssthresh = tSMSS // cwnd > ssthresh で必ず CA
		start := c.cwnd
		var sum uint32
		for _, ch := range chunks {
			n := uint32(ch) % tSMSS // 各 ACK は sub-MSS に抑える
			if n == 0 {
				continue
			}
			if sum+n >= c.cwnd { // 合計が cwnd 未満に収まる範囲だけ流す
				break
			}
			sum += n
			c.onNewAck(n, 100*tSMSS)
		}
		// 累積が cwnd 未満なら cwnd は 1 回も増えない。
		return c.cwnd == start
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// byte-counting アンダーフロー無し: CA で ACK 列を流し続けても bytesAckedThisRtt は
// cwnd 到達でリセットされ、常に cwnd 未満に保たれる (負や wrap を起こさない)。
func TestByteCountingNoUnderflowProperty(t *testing.T) {
	f := func(chunks []uint16) bool {
		c := newCong()
		c.state = ccCongestionAvoidance
		c.cwnd = 5 * tSMSS
		c.ssthresh = tSMSS
		for _, ch := range chunks {
			n := uint32(ch)%(3*tSMSS) + 1
			c.onNewAck(n, 100*tSMSS)
			// 累積は常に cwnd 未満 (到達したら即 -=cwnd でリセットされる)。
			if c.bytesAckedThisRtt >= c.cwnd {
				t.Logf("byte-counting が cwnd 以上に滞留: acc=%d cwnd=%d", c.bytesAckedThisRtt, c.cwnd)
				return false
			}
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// cwnd 単調 + 下限: 新規 ACK だけを任意の列で流すと cwnd は単調非減少で、
// 常に >= SMSS。新規 ACK は loss エピソードを終わらせ縮小要因にならない。
func TestCwndMonotonicUnderNewAcksProperty(t *testing.T) {
	f := func(acked []uint16) bool {
		c := newCong()
		prev := c.cwnd
		for _, a := range acked {
			n := uint32(a) + 1
			c.onNewAck(n, 100*tSMSS)
			if c.cwnd < prev {
				t.Logf("新規 ACK のみで cwnd が減った: %d -> %d", prev, c.cwnd)
				return false
			}
			if c.cwnd < tSMSS {
				t.Logf("cwnd が下限 SMSS を割った: %d", c.cwnd)
				return false
			}
			prev = c.cwnd
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

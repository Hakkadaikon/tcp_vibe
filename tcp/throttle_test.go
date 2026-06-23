package tcp

import (
	"testing"
	"time"
)

// injectSyn は同期状態へ SYN を注入し challenge ACK を誘発する (RFC 5961 §4.2)。
func injectSyn(c *Conn) {
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 8000}, nil)
}

// countSent は peer に溜まった全セグメント数を読み切って返す。
// drainPeerNonblock は peer を閉じるが、閉じた後も inbox 非空なら読めるので
// 「送られた総数」を一括カウントできる。抑制されたぶんは inbox に積まれない。
func countSent(peer Link) int {
	n := 0
	for {
		if _, ok := drainPeerNonblock(peer); !ok {
			return n
		}
		n++
	}
}

// 5 秒窓で上限個まで送出、上限超は抑制。
func TestChallengeAckThrottleWithinWindow(t *testing.T) {
	c, peer, _ := estab(t)

	// 上限 + 余分に注入しても、送出は上限個ちょうどで頭打ち。
	for i := 0; i < challengeAckLimit+5; i++ {
		injectSyn(c)
	}
	if got := countSent(peer); got != challengeAckLimit {
		t.Fatalf("窓内の challenge ACK は上限 %d 個で頭打ちのはず: got %d 個", challengeAckLimit, got)
	}
}

// 窓経過 +1ns でカウンタがリセットされ再び送出される。
func TestChallengeAckThrottleResetsAfterWindow(t *testing.T) {
	// 窓を上限まで使い切ったあと、窓ちょうどでは未リセット、窓+1ns でリセット。
	c, peer, fc := estab(t)
	for i := 0; i < challengeAckLimit; i++ {
		injectSyn(c)
	}
	// 窓ちょうど: まだ同じ窓 (リセットされない) → 追加分は抑制。
	fc.advance(challengeAckWindow)
	injectSyn(c)
	// ここまでの送出は上限個ちょうど。
	if got := countSent(peer); got != challengeAckLimit {
		t.Fatalf("窓ちょうどではリセットされず上限止まりのはず: got %d 個", got)
	}

	// 窓 +1ns: 新しい窓 → 再び送出可能。
	c, peer, fc = estab(t)
	for i := 0; i < challengeAckLimit; i++ {
		injectSyn(c)
	}
	fc.advance(challengeAckWindow + time.Nanosecond)
	injectSyn(c)
	if got := countSent(peer); got != challengeAckLimit+1 {
		t.Fatalf("窓経過後はリセットされ追加 1 個送出されるはず: got %d 個", got)
	}
}

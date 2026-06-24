package tcp

import "testing"

// ACK 受理範囲 (RFC 5961 §5.2 data injection 緩和) の境界・wrap を突くテスト群。
// 受理範囲は [SND.UNA-MAX.SND.WND, SND.NXT]。範囲外は challenge ACK のみで
// SND.UNA を前進させない。範囲内でも acceptable ACK (SND.UNA<ACK=<SND.NXT) で
// なければ前進しない (古い重複 ACK は challenge せず黙って受ける)。

// ackRangeConn は una/nxt/maxSndWnd を指定した ESTABLISHED Conn を返す。
func ackRangeConn(t *testing.T, una, nxt, maxSndWnd uint32) (*Conn, Link) {
	t.Helper()
	c, peer, _ := estab(t)
	c.tcb.snd.una = una
	c.tcb.snd.nxt = nxt
	c.tcb.snd.iss = una - 1
	c.tcb.maxSndWnd = maxSndWnd
	// 窓更新の WL 判定が advance を邪魔しないように手前へ。
	c.tcb.snd.wl1 = c.tcb.rcv.nxt - 1
	c.tcb.snd.wl2 = una - 1
	return c, peer
}

// 下端ちょうど (ACK==SND.UNA-MAX.SND.WND): 5961 範囲内なので challenge しない。
// acceptable ではない (ACK<UNA) ので SND.UNA は前進しない。
func TestAckAtLowerBoundAcceptedNoChallenge(t *testing.T) {
	const una, nxt, mw = 10000, 10100, 4000
	c, peer := ackRangeConn(t, una, nxt, mw)
	lo := uint32(una - mw)

	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt, AckNum: lo, Window: 1}, nil)
	if c.SndUna() != una {
		t.Fatalf("下端 ACK で SND.UNA が前進してはいけない: got %d want %d", c.SndUna(), una)
	}
	if _, ok := drainPeerNonblockKeep(peer); ok {
		t.Fatal("下端ちょうどの ACK に challenge してはいけない (範囲内)")
	}
}

// 下端の 1 つ下 (ACK==SND.UNA-MAX.SND.WND-1): 範囲外 → challenge ACK のみ。
func TestAckBelowLowerBoundChallenges(t *testing.T) {
	const una, nxt, mw = 10000, 10100, 4000
	c, peer := ackRangeConn(t, una, nxt, mw)
	lo := uint32(una - mw)

	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt, AckNum: lo - 1, Window: 1}, nil)
	if c.SndUna() != una {
		t.Fatalf("範囲外 ACK で SND.UNA が前進した: got %d", c.SndUna())
	}
	ch, ok := drainPeerNonblockKeep(peer)
	if !ok {
		t.Fatal("下端未満の ACK には challenge ACK が必要")
	}
	if ch.SeqNum != c.SndNxt() || ch.AckNum != c.RcvNxt() {
		t.Fatalf("challenge ACK の形式違反: seq=%d ack=%d", ch.SeqNum, ch.AckNum)
	}
}

// 上端ちょうど (ACK==SND.NXT): acceptable → SND.UNA を SND.NXT まで前進。
func TestAckAtSndNxtAdvancesUna(t *testing.T) {
	const una, nxt, mw = 10000, 10100, 4000
	c, _ := ackRangeConn(t, una, nxt, mw)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt, AckNum: nxt, Window: 1}, nil)
	if c.SndUna() != nxt {
		t.Fatalf("ACK==SND.NXT で SND.UNA が前進していない: got %d want %d", c.SndUna(), nxt)
	}
}

// 上端の 1 つ上 (ACK==SND.NXT+1): 範囲外 → challenge ACK のみ、前進しない。
func TestAckAboveSndNxtChallenges(t *testing.T) {
	const una, nxt, mw = 10000, 10100, 4000
	c, peer := ackRangeConn(t, una, nxt, mw)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.tcb.rcv.nxt, AckNum: nxt + 1, Window: 1}, nil)
	if c.SndUna() != una {
		t.Fatalf("SND.NXT より上の ACK で前進した: got %d", c.SndUna())
	}
	if _, ok := drainPeerNonblockKeep(peer); !ok {
		t.Fatal("SND.NXT より上の ACK には challenge ACK が必要")
	}
}

// wrap 領域: SND.UNA が小さく maxSndWnd で 0 を跨ぐ下端。
// una=100, maxSndWnd=500 → lo=100-500 が 2^32 付近へ wrap する。
// lo ちょうどは範囲内 (challenge しない)、lo-1 は範囲外 (challenge)。
func TestAckLowerBoundWrapsAroundZero(t *testing.T) {
	var una, nxt, mw uint32 = 100, 200, 500
	lo := una - mw // = 2^32 - 400, wrap

	// 下端ちょうど: 範囲内、challenge しない。
	c1, p1 := ackRangeConn(t, una, nxt, mw)
	c1.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c1.tcb.rcv.nxt, AckNum: lo, Window: 1}, nil)
	if _, ok := drainPeerNonblockKeep(p1); ok {
		t.Fatal("wrap した下端ちょうどに challenge してはいけない")
	}
	if c1.SndUna() != una {
		t.Fatalf("wrap 下端 ACK で前進した: got %d", c1.SndUna())
	}

	// 下端未満: wrap 空間でも範囲外と判定し challenge。
	c2, p2 := ackRangeConn(t, una, nxt, mw)
	c2.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c2.tcb.rcv.nxt, AckNum: lo - 1, Window: 1}, nil)
	if _, ok := drainPeerNonblockKeep(p2); !ok {
		t.Fatal("wrap した下端未満には challenge ACK が必要")
	}
}

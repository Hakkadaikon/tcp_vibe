package tcp

import (
	"testing"
	"time"
)

// armedConn は SYN を送って再送キューに 1 件積んだ SYN-SENT の Conn を返す。
// SYN は 1 seq を消費するため再送キューに乗る (RTO 駆動の最小土台)。
// 初回 SYN は peer に残したまま返す (観測は呼び出し側で行う)。
func armedConn(t *testing.T) (*Conn, Link, *fakeClock) {
	t.Helper()
	c, peer, fc := newTestConn(t)
	c.ActiveOpen(1000)
	return c, peer, fc
}

// lastSent は peer に溜まった全セグメントを読み切り、最後の 1 つを返す。
// drainPeerNonblock は peer を閉じるので、複数セグメントが溜まっていても
// 閉じた後に残りを読み切れる (ReadPacket は closed でも inbox 非空なら返す)。
func lastSent(t *testing.T, peer Link) (TCPHeader, int) {
	t.Helper()
	var last TCPHeader
	n := 0
	for {
		h, ok := drainPeerNonblock(peer)
		if !ok {
			return last, n
		}
		last = h
		n++
	}
}

// RTO ちょうどで再送 / RTO-1ns では再送しない。
func TestRetransmitFiresAtRTO(t *testing.T) {
	// RTO 直前: 再送しない (初回 SYN の 1 通だけ)。
	c, peer, fc := armedConn(t)
	fc.advance(initialRTO - time.Nanosecond)
	c.Tick()
	if _, n := lastSent(t, peer); n != 1 {
		t.Fatalf("RTO 直前は初回 SYN のみのはず: got %d 通", n)
	}

	// RTO ちょうど: 先頭 (SYN) を再送する (初回 + 再送 = 2 通)。
	c, peer, fc = armedConn(t)
	fc.advance(initialRTO)
	c.Tick()
	h, n := lastSent(t, peer)
	if n != 2 {
		t.Fatalf("RTO 到達で再送され 2 通のはず: got %d 通", n)
	}
	if !h.Flags.Has(FlagSYN) || h.SeqNum != 1000 {
		t.Fatalf("再送は元 SYN(seq=1000) のはず: got flags=%v seq=%d", h.Flags, h.SeqNum)
	}
}

// RTO 前に acceptable ACK が来たらキューから除去され、再送されない。
func TestAckBeforeRTOClearsQueue(t *testing.T) {
	c, peer, fc := armedConn(t)

	// SYN,ACK で自 SYN が確認される (ack=ISS+1=1001) → SYN がキューから消える。
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN | FlagACK), SeqNum: 5000, AckNum: 1001}, nil)

	fc.advance(initialRTO * 4)
	c.Tick()
	// SYN は初回送信の 1 個だけのはず。再送されると 2 個以上になる。
	syns := 0
	for {
		h, ok := drainPeerNonblock(peer)
		if !ok {
			break
		}
		if h.Flags.Has(FlagSYN) {
			syns++
		}
	}
	if syns != 1 {
		t.Fatalf("ACK 済セグメントは再送されず SYN は初回 1 個のみのはず: got %d 個", syns)
	}
}

// 連続再送で RTO が倍化する (2 回目は 2·RTO の境界で発火)。
func TestRetransmitExponentialBackoff(t *testing.T) {
	// 1 回目を発火させたあと、2·RTO 直前では 2 回目が発火しない。
	c, peer, fc := armedConn(t)
	fc.advance(initialRTO)
	c.Tick() // 1 回目再送
	fc.advance(2*initialRTO - time.Nanosecond)
	c.Tick()
	_, n := lastSent(t, peer)
	// 初回 SYN + 1 回目再送 = 2 通。2 回目はまだ。
	if n != 2 {
		t.Fatalf("2·RTO 直前は再送 1 回まで (計 2 通) のはず: got %d 通", n)
	}

	// 2·RTO ちょうどで 2 回目が発火する (計 3 通)。
	c, peer, fc = armedConn(t)
	fc.advance(initialRTO)
	c.Tick() // 1 回目再送
	fc.advance(2 * initialRTO)
	c.Tick() // 2 回目再送
	_, n = lastSent(t, peer)
	if n != 3 {
		t.Fatalf("2·RTO で 2 回目が発火し計 3 通のはず: got %d 通", n)
	}
}

// 再送上限到達で接続が CLOSED (宙吊りしない)。
func TestRetransmitLimitClosesConnection(t *testing.T) {
	c, _, fc := armedConn(t)

	// maxRetransmits 回まで再送。各回ごとに現在の RTO 以上進める。
	rto := initialRTO
	for i := 0; i < maxRetransmits; i++ {
		fc.advance(rto)
		c.Tick()
		if c.State() == Closed {
			t.Fatalf("上限前 (%d 回目) に CLOSED になってはいけない", i+1)
		}
		if rto < maxRTO {
			rto *= 2
			if rto > maxRTO {
				rto = maxRTO
			}
		}
	}
	// 上限超の発火で CLOSED へ。
	fc.advance(rto)
	c.Tick()
	if c.State() != Closed {
		t.Fatalf("再送上限到達で CLOSED のはず: got %v", c.State())
	}
}

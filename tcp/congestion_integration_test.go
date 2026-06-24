package tcp

import (
	"testing"
	"time"
)

// established は握手を済ませた ESTABLISHED の Conn を返す (能動オープン側)。
// SYN(seq=1000) → SYN,ACK(seq=5000,ack=1001) を注入して ESTABLISHED にする。
// peer の inbox に溜まる送出セグメントは drain しない (pipeLink は drain で閉じ、
// 以後の送信が失われるため)。各テストは観測ヘルパか末尾の 1 回 drain で確認する。
func established(t *testing.T) (*Conn, Link, *fakeClock) {
	t.Helper()
	c, peer, fc := newTestConn(t)
	c.ActiveOpen(1000) // SYN(seq=1000), SND.NXT=1001
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN | FlagACK), SeqNum: 5000, AckNum: 1001, Window: maxWindow}, nil)
	if c.State() != Established {
		t.Fatalf("ESTABLISHED にならない: %v", c.State())
	}
	return c, peer, fc
}

// drainAll は peer に溜まった全セグメントを読み切って返す (末尾で 1 回だけ呼ぶ)。
func drainAll(t *testing.T, peer Link) []TCPHeader {
	t.Helper()
	var hs []TCPHeader
	for {
		h, ok := drainPeerNonblock(peer)
		if !ok {
			return hs
		}
		hs = append(hs, h)
	}
}

// 新規 ACK で RTT サンプルを取得し、推定器が更新され RTO が動的計算 (RFC 6298) になる。
func TestDynamicRtoFromRttSample(t *testing.T) {
	c, _, fc := established(t)
	c.Send([]byte("hello")) // データ seq=1001..1005, SND.NXT=1006

	// RTT = 800ms 経過後に新規 ACK 受信 → updateEst で SRTT が 800 寄りへ動く。
	// (握手の SYN サンプルは RTT=0→1ms 扱い。新規 ACK で更新されることを確認。)
	fc.advance(800 * time.Millisecond)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1006, Window: maxWindow}, nil)

	if !c.RttSampled() {
		t.Fatalf("新規 ACK で RTT サンプルが取れているはず")
	}
	if c.SrttMS() <= 1 {
		t.Fatalf("新規 ACK で SRTT が更新されるはず: got %d", c.SrttMS())
	}
}

// Karn: 再送したセグメントの ACK からは RTT を測らない (SRTT が変わらない)。
func TestKarnNoSampleFromRetransmit(t *testing.T) {
	c, _, fc := established(t)
	c.Send([]byte("data!")) // seq=1001..1005, SND.NXT=1006
	srttBefore := c.SrttMS()

	// RTO 満了で再送させる → このセグメントは Karn の対象 (RTT に使わない)。
	fc.advance(initialRTO)
	c.Tick()

	// 再送後に来た ACK では RTT を測らない: SRTT は変わらないはず。
	fc.advance(50 * time.Millisecond)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1006, Window: maxWindow}, nil)
	if c.SrttMS() != srttBefore {
		t.Fatalf("再送セグメントの ACK で SRTT を更新してはいけない (Karn): before=%d after=%d", srttBefore, c.SrttMS())
	}
}

// 送信量は min(cwnd, rwnd) を超えない。cwnd を小さくすると送出が絞られる。
func TestSendLimitedByCwnd(t *testing.T) {
	c, _, _ := established(t)
	c.SetCwnd(defaultMSS) // cwnd を 1 SMSS に絞る (rwnd は maxWindow)

	c.Send(make([]byte, 3*defaultMSS))
	inflight := c.SndNxt() - c.SndUna()
	if inflight > defaultMSS {
		t.Fatalf("送信中バイトが cwnd を超えた: inflight=%d cwnd=%d", inflight, defaultMSS)
	}
}

// 3 つ目の重複 ACK で損失セグメントを即再送する (fast retransmit)。
func TestFastRetransmitOnThreeDupAcks(t *testing.T) {
	c, peer, _ := established(t)
	c.SetCwnd(100 * defaultMSS) // 複数セグメント送れるように
	c.Send(make([]byte, 3*defaultMSS))
	const firstSeq = 1001 // 握手後の最初のデータ seq

	// ack=firstSeq の重複 ACK を 3 回 (重複条件: データ無し/窓同一/最大 ACK)。
	dup := TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: firstSeq, Window: maxWindow}
	c.onSegment(dup, nil)
	c.onSegment(dup, nil)
	c.onSegment(dup, nil)

	// 3 つ目で firstSeq のセグメントが再送されているはず (出力に 2 回以上現れる)。
	count := 0
	for _, h := range drainAll(t, peer) {
		if h.SeqNum == firstSeq && h.Flags.Has(FlagPSH) {
			count++
		}
	}
	if count < 2 {
		t.Fatalf("3 dup ACK で損失セグメント(seq=%d)が即再送されるはず: 出現 %d 回", firstSeq, count)
	}
}

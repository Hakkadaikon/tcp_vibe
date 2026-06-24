package tcp

import (
	"testing"
	"time"
)

// estabTS は timestamps 折衝済みの ESTABLISHED 接続を組む。
// TS.Recent=1000, Last.ACK.sent を広めに置き SEQ ゲートが通る状態にする。
func estabTS(t *testing.T) (*Conn, Link, *fakeClock) {
	t.Helper()
	c, peer, fc := estab(t)
	c.tcb.tsOK = true
	c.tcb.tsRecent = 1000
	c.tcb.lastAckSent = 9000 // SEG.SEQ(8000) <= Last.ACK.sent を満たす
	return c, peer, fc
}

func segTS(seq, ack uint32, tsval, tsecr uint32, flags Flags, payload []byte) (TCPHeader, []byte) {
	o := TCPOptions{HasTimestamp: true, TSVal: tsval, TSecr: tsecr}
	return TCPHeader{
		Flags: flags, SeqNum: seq, AckNum: ack, Window: 1000, Options: o.Marshal(),
	}, payload
}

// 古い TSval (< TS.Recent) のデータセグメントは PAWS で drop され、RCV.NXT は進まない。
// drop 時も ACK は返す (RFC 7323 §5.3)。
func TestPawsDropsOldSegment(t *testing.T) {
	c, peer, _ := estabTS(t)
	before := c.RcvNxt()
	h, pl := segTS(8000, 4000, 999, 0, Flags(FlagACK|FlagPSH), []byte("hi"))
	c.onSegment(h, pl)
	if c.RcvNxt() != before {
		t.Errorf("RCV.NXT advanced on stale segment: %d -> %d", before, c.RcvNxt())
	}
	if _, ok := drainPeer(t, peer); !ok {
		t.Error("PAWS drop should still send an ACK")
	}
}

// 新しい TSval のデータは受理され、TS.Recent が更新される。
func TestPawsAcceptsAndUpdatesTsRecent(t *testing.T) {
	c, _, _ := estabTS(t)
	h, pl := segTS(8000, 4000, 1005, 0, Flags(FlagACK|FlagPSH), []byte("hi"))
	c.onSegment(h, pl)
	if c.RcvNxt() != 8002 {
		t.Errorf("RCV.NXT should advance by 2: got %d", c.RcvNxt())
	}
	c.mu.Lock()
	got := c.tcb.tsRecent
	c.mu.Unlock()
	if got != 1005 {
		t.Errorf("TS.Recent should update to 1005, got %d", got)
	}
}

// RST は PAWS 対象外: 古い TSval でも PAWS で drop せず RST 処理に進む。
// 窓内 SEG.SEQ=RCV.NXT の RST は接続を reset する。
func TestPawsExemptsRst(t *testing.T) {
	c, _, _ := estabTS(t)
	h, _ := segTS(8000, 0, 1, 0, Flags(FlagRST), nil) // 非常に古い TSval
	c.onSegment(h, nil)
	if c.State() != Closed {
		t.Errorf("RST with stale TS should still reset; state=%v", c.State())
	}
}

// TSecr から RTT サンプルを取り推定器に渡す (Karn の例外: timestamp があれば測れる)。
func TestTimestampRTTSample(t *testing.T) {
	c, peer, fc := estabTS(t)
	// timestamp clock を非ゼロ起点に進める (TSecr=0 は「ACK 無し」の規約値で
	// echo として弾かれるため、送信時刻が 0 だと RTT を測れない)。
	fc.advance(1000 * time.Millisecond)
	// データを送って未確認にし、相手 ACK が TSecr を echo して返る状況を作る。
	c.tcb.state = Established
	c.mu.Lock()
	c.tcb.sndBuf = append(c.tcb.sndBuf, []byte("data")...)
	c.tcb.snd.wnd = 1000
	c.tcb.cong.cwnd = 1000
	sentTS := c.tcb.tsNow()
	c.flushSend()
	c.mu.Unlock()
	drainPeer(t, peer) // 送ったデータを捨てる
	// 50ms 経過後、相手が ACK (TSecr=送信時の TSval) を返す。
	fc.advance(50 * time.Millisecond)
	h, _ := segTS(8000, c.SndNxt(), 1005, sentTS, Flags(FlagACK), nil)
	c.onSegment(h, nil)
	if !c.RttSampled() {
		t.Error("RTT should be sampled from TSecr")
	}
	if c.SrttMS() == 0 {
		t.Error("SRTT should be set from timestamp RTT")
	}
}

// 折衝済みなら通常 ACK に TS option が載り、TSecr に TS.Recent を echo する。
func TestOutgoingCarriesTimestamp(t *testing.T) {
	c, peer, _ := estabTS(t)
	h, pl := segTS(8000, 4000, 1005, 0, Flags(FlagACK|FlagPSH), []byte("x"))
	c.onSegment(h, pl)
	out, ok := drainPeer(t, peer)
	if !ok {
		t.Fatal("no ACK sent")
	}
	o, err := ParseTCPOptions(out.Options)
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasTimestamp {
		t.Fatal("outgoing segment missing TS option")
	}
	if o.TSecr != 1005 {
		t.Errorf("TSecr should echo TS.Recent(1005), got %d", o.TSecr)
	}
}

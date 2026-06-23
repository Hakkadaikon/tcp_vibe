package tcp

import (
	"testing"
	"time"
)

// fakeClock は決定論的な時刻 seam。advance で時間を進める。
type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time          { return f.now }
func (f *fakeClock) advance(d time.Duration) { f.now = f.now.Add(d) }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(0, 0)}
}

// drainPeer は対向リンクに溜まった送信セグメントを 1 つ読み、ヘッダを返す。
// 送信が無ければ ok=false。テストでは pipeLink を非ブロッキングに読むため
// 事前に閉じず、ReadPacket がブロックしないよう「来ているはず」の前提で呼ぶ。
func drainPeer(t *testing.T, peer Link) (TCPHeader, bool) {
	t.Helper()
	pkt, err := peer.ReadPacket()
	if err != nil {
		return TCPHeader{}, false
	}
	h, err := ParseTCPHeader(pkt)
	if err != nil {
		t.Fatalf("送信セグメントの解析に失敗: %v", err)
	}
	return h, true
}

// newTestConn は Conn と、Conn の送信を観測する対向リンクを返す。
func newTestConn(t *testing.T) (*Conn, Link, *fakeClock) {
	t.Helper()
	a, b := NewPipeLink()
	fc := newFakeClock()
	c := NewConn(a, fc.Now)
	return c, b, fc
}

// expectFlags は対向に届いた次セグメントが期待フラグを持つか検証する。
func expectFlags(t *testing.T, peer Link, want Flags) TCPHeader {
	t.Helper()
	h, ok := drainPeer(t, peer)
	if !ok {
		t.Fatalf("セグメントが送られていない (期待フラグ %v)", want)
	}
	if h.Flags != want {
		t.Fatalf("フラグ不一致: got %v want %v", h.Flags, want)
	}
	return h
}

// 能動 3way ハンドシェイクで ESTABLISHED に到達する。
func TestActiveHandshakeReachesEstablished(t *testing.T) {
	c, peer, _ := newTestConn(t)

	c.ActiveOpen(1000)
	if c.State() != SynSent {
		t.Fatalf("active OPEN 後は SYN-SENT のはず: got %v", c.State())
	}
	syn := expectFlags(t, peer, Flags(FlagSYN))
	if syn.SeqNum != 1000 {
		t.Fatalf("SYN の seq は ISS のはず: got %d", syn.SeqNum)
	}

	// 相手が SYN,ACK (seq=5000, ack=ISS+1=1001) を返す。
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN | FlagACK), SeqNum: 5000, AckNum: 1001}, nil)
	if c.State() != Established {
		t.Fatalf("SYN,ACK 受信後は ESTABLISHED のはず: got %v", c.State())
	}
	expectFlags(t, peer, Flags(FlagACK))
}

// 受動オープン + SYN で SYN-RECEIVED (passive origin)、その後 RST で LISTEN へ戻る。
func TestPassiveOpenSynRcvdReturnsToListenOnRst(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.tcb.snd.iss = 7000

	c.PassiveOpen()
	if c.State() != Listen {
		t.Fatalf("passive OPEN 後は LISTEN のはず: got %v", c.State())
	}

	c.onSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000}, nil)
	if c.State() != SynReceived {
		t.Fatalf("SYN 受信後は SYN-RECEIVED のはず: got %v", c.State())
	}
	if c.Origin() != OriginPassive {
		t.Fatalf("由来は passive のはず: got %v", c.Origin())
	}
	expectFlags(t, peer, Flags(FlagSYN|FlagACK))

	// RST in window (SEG.SEQ=RCV.NXT=3001) → passive 由来なので LISTEN へ戻る (CLOSED でない)。
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: c.RcvNxt()}, nil)
	if c.State() != Listen {
		t.Fatalf("passive 由来 SYN-RCVD の RST は LISTEN へ戻すはず: got %v", c.State())
	}
}

// 同時オープン: SYN-SENT で bare SYN → SYN-RECEIVED (active origin)、RST で CLOSED へ。
func TestSimultaneousOpenRecordsActiveOriginAndRstGoesToClosed(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.ActiveOpen(2000)
	expectFlags(t, peer, Flags(FlagSYN)) // 自 SYN を捨てる

	// bare SYN (自 SYN 未 ACK) が届く = 同時オープン。
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 9000}, nil)
	if c.State() != SynReceived {
		t.Fatalf("同時オープンは SYN-RECEIVED のはず: got %v", c.State())
	}
	if c.Origin() != OriginActive {
		t.Fatalf("由来は active のはず: got %v", c.Origin())
	}
	expectFlags(t, peer, Flags(FlagSYN|FlagACK))

	// active 由来 SYN-RCVD の RST は CLOSED へ (LISTEN でない)。
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: c.RcvNxt()}, nil)
	if c.State() != Closed {
		t.Fatalf("active 由来 SYN-RCVD の RST は CLOSED へ落とすはず: got %v", c.State())
	}
}

// estab は ESTABLISHED 状態の Conn を組む (同期状態テストの共通土台)。
// SND.UNA=4000, SND.NXT=4000, RCV.NXT=8000, RCV.WND=1000 とする。
func estab(t *testing.T) (*Conn, Link, *fakeClock) {
	t.Helper()
	c, peer, fc := newTestConn(t)
	c.tcb.state = Established
	c.tcb.snd.una = 4000
	c.tcb.snd.nxt = 4000
	c.tcb.snd.iss = 3999
	c.tcb.rcv.nxt = 8000
	c.tcb.rcv.wnd = 1000
	c.tcb.rcv.irs = 7999
	c.tcb.maxSndWnd = maxWindow
	return c, peer, fc
}

// 窓外 RST → silently drop (reset しない、状態維持)。
func TestSyncOutOfWindowRstIsDropped(t *testing.T) {
	c, peer, _ := estab(t)
	// 窓 [8000,9000) の外。
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: 9500}, nil)
	if c.State() != Established {
		t.Fatalf("窓外 RST で状態が変わってはいけない: got %v", c.State())
	}
	if _, ok := drainPeerNonblock(peer); ok {
		t.Fatal("窓外 RST に対し何も送ってはいけない (silently drop)")
	}
}

// 窓内だが SEG.SEQ != RCV.NXT の RST → challenge ACK のみ、reset 禁止。
func TestSyncInWindowNonNxtRstOnlyChallenges(t *testing.T) {
	c, peer, _ := estab(t)
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: 8500}, nil)
	if c.State() != Established {
		t.Fatalf("窓内 !=NXT の RST で reset してはいけない: got %v", c.State())
	}
	ch := expectFlags(t, peer, Flags(FlagACK))
	if ch.SeqNum != c.SndNxt() || ch.AckNum != c.RcvNxt() {
		t.Fatalf("challenge ACK の形式違反: seq=%d ack=%d", ch.SeqNum, ch.AckNum)
	}
}

// SEG.SEQ = RCV.NXT の RST → reset (CLOSED)。
func TestSyncRstAtRcvNxtResets(t *testing.T) {
	c, _, _ := estab(t)
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: 8000}, nil)
	if c.State() != Closed {
		t.Fatalf("SEG.SEQ=RCV.NXT の RST は reset するはず: got %v", c.State())
	}
}

// 同期状態の SYN → challenge ACK のみ、reset しない。
func TestSyncSynNeverResetsOnlyChallenges(t *testing.T) {
	c, peer, _ := estab(t)
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 8000}, nil)
	if c.State() != Established {
		t.Fatalf("同期状態の SYN で reset してはいけない: got %v", c.State())
	}
	ch := expectFlags(t, peer, Flags(FlagACK))
	if ch.SeqNum != c.SndNxt() || ch.AckNum != c.RcvNxt() {
		t.Fatalf("challenge ACK の形式違反: seq=%d ack=%d", ch.SeqNum, ch.AckNum)
	}
}

// ACK 範囲外 → SND.UNA 前進せず・データ適用せず。
func TestAckOutOfRangeDoesNotAdvanceUna(t *testing.T) {
	c, peer, _ := estab(t)
	// SND.NXT を進めて未確認データを作る: una=4000, nxt=4100。
	c.tcb.snd.nxt = 4100
	before := c.SndUna()
	// 受理範囲 (una-maxSndWnd .. nxt) = (4000-65535 .. 4100)。
	// nxt より大きい 5000 は範囲外。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 8000, AckNum: 5000}, nil)
	if c.SndUna() != before {
		t.Fatalf("範囲外 ACK で SND.UNA が前進した: %d -> %d", before, c.SndUna())
	}
	// challenge ACK が返る。
	expectFlags(t, peer, Flags(FlagACK))
}

// 範囲外 ACK の付いた FIN では閉じない (spoofed FIN robustness)。
func TestOutOfRangeAckFinDoesNotClose(t *testing.T) {
	c, _, _ := estab(t)
	c.tcb.snd.nxt = 4100
	c.onSegment(TCPHeader{Flags: Flags(FlagFIN | FlagACK), SeqNum: 8000, AckNum: 5000}, nil)
	if c.State() != Established {
		t.Fatalf("範囲外 ACK の FIN で状態が変わってはいけない: got %v", c.State())
	}
}

// TIME-WAIT: 2MSL 満了で CLOSED (ちょうど/直前)。
func TestTimeWaitExpiresAfter2MSL(t *testing.T) {
	c, _, fc := newTestConn(t)
	c.tcb.state = TimeWait
	c.tcb.timeWaitDeadline = fc.Now().Add(timeWaitDuration)

	// 満了直前: まだ CLOSED にしない。
	fc.advance(timeWaitDuration - time.Nanosecond)
	c.Tick()
	if c.State() != TimeWait {
		t.Fatalf("2MSL 満了前は TIME-WAIT 維持のはず: got %v", c.State())
	}

	// ちょうど満了: CLOSED へ。
	fc.advance(time.Nanosecond)
	c.Tick()
	if c.State() != Closed {
		t.Fatalf("2MSL 満了で CLOSED のはず: got %v", c.State())
	}
}

// TIME-WAIT で RST → 2MSL を待たず即 CLOSED。
func TestTimeWaitRstAbortsImmediately(t *testing.T) {
	c, _, fc := newTestConn(t)
	c.tcb.state = TimeWait
	c.tcb.rcv.nxt = 8000
	c.tcb.rcv.wnd = 1000
	c.tcb.timeWaitDeadline = fc.Now().Add(timeWaitDuration)

	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: 8000}, nil)
	if c.State() != Closed {
		t.Fatalf("TIME-WAIT の RST は即 CLOSED のはず: got %v", c.State())
	}
}

// TIME-WAIT で FIN 再送 → ACK 再送 + 2MSL 再起動 + TIME-WAIT 維持。
func TestTimeWaitFinRetransmitRestartsTimer(t *testing.T) {
	c, peer, fc := newTestConn(t)
	c.tcb.state = TimeWait
	c.tcb.snd.una = 4000
	c.tcb.snd.nxt = 4000
	c.tcb.rcv.nxt = 8000
	c.tcb.rcv.wnd = 1000
	c.tcb.timeWaitDeadline = fc.Now().Add(timeWaitDuration)

	// 時間を半分進めてから FIN 再送。再送時に 2MSL タイマが再起動される。
	fc.advance(msl / 2)
	wantDeadline := fc.Now().Add(timeWaitDuration)
	c.onSegment(TCPHeader{Flags: Flags(FlagFIN | FlagACK), SeqNum: 8000, AckNum: 4000}, nil)

	if c.State() != TimeWait {
		t.Fatalf("FIN 再送後も TIME-WAIT 維持のはず: got %v", c.State())
	}
	expectFlags(t, peer, Flags(FlagACK)) // ACK 再送
	if !c.tcb.timeWaitDeadline.Equal(wantDeadline) {
		t.Fatalf("2MSL タイマが再起動されていない: got %v want %v", c.tcb.timeWaitDeadline, wantDeadline)
	}
}

// graceful close: ESTAB→CLOSE→FIN-WAIT-1→(ACK)→FIN-WAIT-2→(FIN)→TIME-WAIT。
func TestGracefulActiveClose(t *testing.T) {
	c, peer, _ := estab(t)

	c.Close()
	if c.State() != FinWait1 {
		t.Fatalf("CLOSE 後は FIN-WAIT-1 のはず: got %v", c.State())
	}
	fin := expectFlags(t, peer, Flags(FlagFIN|FlagACK))
	finSeq := fin.SeqNum // この FIN の seq。ack = finSeq+1 で確認される。

	// 自 FIN への ACK (ack=SND.NXT)。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 8000, AckNum: c.SndNxt()}, nil)
	if c.State() != FinWait2 {
		t.Fatalf("自 FIN の ACK で FIN-WAIT-2 のはず: got %v", c.State())
	}

	// 相手の FIN。
	c.onSegment(TCPHeader{Flags: Flags(FlagFIN | FlagACK), SeqNum: 8000, AckNum: c.SndNxt()}, nil)
	if c.State() != TimeWait {
		t.Fatalf("相手 FIN 受信で TIME-WAIT のはず: got %v", c.State())
	}
	expectFlags(t, peer, Flags(FlagACK))
	_ = finSeq
}

// --- 代表ケース: 許可されない遷移の拒否 ---

// LISTEN で bare ACK → RST 応答、遷移しない。
func TestListenBareAckRepliesRst(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.PassiveOpen()
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 100, AckNum: 555}, nil)
	if c.State() != Listen {
		t.Fatalf("LISTEN は bare ACK で遷移しないはず: got %v", c.State())
	}
	rst := expectFlags(t, peer, Flags(FlagRST))
	if rst.SeqNum != 555 {
		t.Fatalf("RST の seq は SEG.ACK のはず: got %d", rst.SeqNum)
	}
}

// CLOSED で非 OPEN セグメント (ACK) → RST 応答。
func TestClosedSegmentRepliesRst(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 100, AckNum: 777}, nil)
	if c.State() != Closed {
		t.Fatalf("CLOSED は遷移しないはず: got %v", c.State())
	}
	rst := expectFlags(t, peer, Flags(FlagRST))
	if rst.SeqNum != 777 {
		t.Fatalf("RST の seq は SEG.ACK のはず: got %d", rst.SeqNum)
	}
}

// CLOSED で RST 受信 → 無反応 (RST に RST を返さない)。
func TestClosedRstIsIgnored(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.onSegment(TCPHeader{Flags: Flags(FlagRST), SeqNum: 100}, nil)
	if _, ok := drainPeerNonblock(peer); ok {
		t.Fatal("CLOSED で RST に応答してはいけない")
	}
}

// drainPeerNonblock は対向にセグメントが届いていれば読む。無ければ ok=false で
// ブロックしない。pipeLink を一旦閉じて溜まった分だけ読み切る方式。
func drainPeerNonblock(peer Link) (TCPHeader, bool) {
	peer.Close()
	pkt, err := peer.ReadPacket()
	if err != nil {
		return TCPHeader{}, false
	}
	h, _ := ParseTCPHeader(pkt)
	return h, true
}

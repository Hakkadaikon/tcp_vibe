package transport

import (
	"github.com/hakkadaikon/tcp_vibe/tcp/link"
	"github.com/hakkadaikon/tcp_vibe/tcp/network"
	"testing"
	"time"
)

// drainSegs は peer に溜まった全セグメントを読み (ヘッダ, payload) の列で返す。
// pipeLink を閉じて溜まったぶんだけ読み切る (以後この peer は読めない)。
type sentSeg struct {
	h       TCPHeader
	payload []byte
}

func drainSegs(peer link.Link) []sentSeg {
	peer.Close()
	var out []sentSeg
	for {
		pkt, err := peer.ReadPacket()
		if err != nil {
			return out
		}
		ip, err := network.ParseIPv4Header(pkt)
		if err != nil {
			continue
		}
		seg := pkt[int(ip.IHL)*4:]
		h, err := ParseTCPHeader(seg)
		if err != nil {
			continue
		}
		out = append(out, sentSeg{h: h, payload: seg[int(h.DataOffset)*4:]})
	}
}

// swapPeer は Conn の送信リンクを新しい pipe に差し替え、新しい観測側を返す。
// 既存 peer を閉じた drainSegs の後、以降の送出だけを観測したいときに使う。
func (c *Conn) swapPeer(t *testing.T) (*Conn, link.Link, *fakeClock) {
	t.Helper()
	a, b := link.NewPipeLink()
	c.mu.Lock()
	c.link = a
	c.mu.Unlock()
	return c, b, nil
}

// 受信窓を縮めない: データ受信で RCV.NXT が前進しても右窓端 (RCV.NXT+RCV.WND) は
// 不変。アプリが Recv で読むと窓が開き右窓端は前進する (単調非減少)。
func TestRcvWindowRightEdgeMonotonic(t *testing.T) {
	c, _, _ := establishedConn(t, maxWindow)
	c.mu.Lock()
	c.tcb.rcvBuffTotal = 4000 // 小さめの受信バッファで挙動を見る
	c.tcb.rcv.wnd = 4000
	c.mu.Unlock()

	rightEdge := func() uint32 {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.tcb.rcv.nxt + uint32(c.tcb.rcv.wnd)
	}
	edge0 := rightEdge()

	// in-order データを 2 回受信。右窓端は不変のはず。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1001, Window: maxWindow}, make([]byte, 1000))
	if got := rightEdge(); got != edge0 {
		t.Fatalf("データ受信で右窓端が動いた: %d -> %d", edge0, got)
	}
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 6001, AckNum: 1001, Window: maxWindow}, make([]byte, 1000))
	if got := rightEdge(); got != edge0 {
		t.Fatalf("データ受信で右窓端が動いた (2回目): %d -> %d", edge0, got)
	}

	// アプリが全部読む → 窓が開いて右窓端が前進 (単調非減少)。
	buf := make([]byte, 4000)
	n, _ := c.Recv(buf)
	if n != 2000 {
		t.Fatalf("読めたバイト数: got %d want 2000", n)
	}
	if got := rightEdge(); got < edge0 {
		t.Fatalf("Recv 後に右窓端が縮んだ: %d < %d", got, edge0)
	}
}

// 受信窓が 64KB を超えても (window scale 折衝済み) 壊れない。
// 内部窓は実バイトを保持し、広告窓は >> rcvWindShift で 16bit に収めて送る。
func TestLargeRcvBufferWithWindowScale(t *testing.T) {
	c, peer, _ := newTestConn(t)
	c.SetRcvBuffer(200000) // 65535 超: uint16 だと wrap して窓が壊れる
	c.ActiveOpen(1000)
	drainPeer(t, peer) // SYN を読み捨て
	// SYN-ACK に WScale=7 を載せて折衝を成立させる。
	o := TCPOptions{HasWScale: true, WindowScale: 7}
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN | FlagACK), SeqNum: 5000, AckNum: 1001, Window: 65535, Options: o.Marshal()}, nil)
	if c.State() != Established {
		t.Fatalf("握手が成立していない: %v", c.State())
	}

	// 内部受信窓は実バイト (200000) を保持する。
	c.mu.Lock()
	internal := c.tcb.rcv.wnd
	shift := c.tcb.rcvWindShift
	c.mu.Unlock()
	if internal != 200000 {
		t.Fatalf("内部受信窓が壊れている: got %d want 200000", internal)
	}
	if shift != 7 {
		t.Fatalf("window scale が折衝されていない: shift=%d", shift)
	}

	// 握手 ACK の広告窓は 200000>>7 = 1562 で 16bit に収まる。
	ack := expectFlags(t, peer, Flags(FlagACK))
	if ack.Window != uint16(200000>>7) {
		t.Fatalf("広告窓が壊れている: got %d want %d", ack.Window, uint16(200000>>7))
	}

	// 65535 を超える seq 範囲のデータを受信できる (窓が実バイトを許す)。
	big := make([]byte, 70000)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1001, Window: 65535}, big)
	buf := make([]byte, 70000)
	n, _ := c.Recv(buf)
	if n != 70000 {
		t.Fatalf("65535 超のデータが受信できない: got %d want 70000", n)
	}
}

// 送信側 Nagle: idle 開始の sub-MSS は即送る。
func TestSendIdleSubMSSSentImmediately(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	c.Send([]byte("hi")) // idle で 2 バイト
	segs := drainSegs(peer)
	if len(segs) == 0 || len(segs[0].payload) != 2 {
		t.Fatalf("idle の sub-MSS が即送されていない: %+v", segs)
	}
}

// 送信側 Nagle: 未確認データ中の sub-MSS は溜める (ACK が来るまで送らない)。
func TestSendNagleHoldsSubMSS(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	// まずフルセグメントを送り未確認状態にする。
	c.Send(make([]byte, defaultMSS))
	// 続けて sub-MSS。未確認中なので Nagle が溜める。
	c.Send([]byte("tail"))
	segs := drainSegs(peer)
	total := 0
	for _, s := range segs {
		total += len(s.payload)
	}
	if total != defaultMSS {
		t.Fatalf("未確認中の sub-MSS が溜められていない: 送出 %d バイト (フルのみ %d を期待)", total, defaultMSS)
	}
}

// Nagle 無効化 (TCP_NODELAY): 未確認データ中でも sub-MSS を即送る。
func TestSendNoDelaySendsImmediately(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	c.SetNoDelay(true)
	c.Send(make([]byte, defaultMSS)) // フル
	c.Send([]byte("tail"))           // 未確認中だが NoDelay で即送る
	segs := drainSegs(peer)
	total := 0
	for _, s := range segs {
		total += len(s.payload)
	}
	if total != defaultMSS+4 {
		t.Fatalf("NoDelay で sub-MSS が即送されていない: 送出 %d バイト want %d", total, defaultMSS+4)
	}
}

// override タイマ: 未確認中フル未満で詰まったら、override 満了で sub-MSS を送出する。
func TestOverrideTimerFlushesStuckData(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	c.Send(make([]byte, defaultMSS)) // フル送出 → 未確認状態
	c.Send([]byte("tail"))           // sub-MSS は Nagle で保留 + override arm
	drainSegs(peer)                  // ここまでの送出を読み捨て (peer 閉鎖)

	_, peer2, _ := c.swapPeer(t) // 新しい観測リンクに差し替え
	fc.advance(overrideTimeout)
	c.Tick() // override 満了 → 保留 sub-MSS を強制送出
	segs := drainSegs(peer2)
	got := 0
	for _, s := range segs {
		got += len(s.payload)
	}
	if got != 4 {
		t.Fatalf("override 満了で保留 sub-MSS が送出されない: got %d バイト want 4", got)
	}
}

// zero-window probe / persist: 相手窓 0 で送信データありなら persist 起動。満了で
// 1 octet probe を送り指数バックオフ。窓>0 を受けたら解除 (デッドロックしない)。
func TestZeroWindowPersistProbe(t *testing.T) {
	c, peer, fc := establishedConn(t, 0) // 相手窓 0
	c.Send([]byte("hello"))              // 窓 0 なので送れない → persist arm
	if segs := drainSegs(peer); len(segs) != 0 {
		t.Fatalf("窓 0 でデータが送られた: %+v", segs)
	}

	_, peer2, _ := c.swapPeer(t)
	fc.advance(persistInitial)
	c.Tick() // persist 満了 → 1 octet probe
	segs := drainSegs(peer2)
	if len(segs) != 1 || len(segs[0].payload) != 1 {
		t.Fatalf("persist 満了で 1 octet probe が出ない: %+v", segs)
	}
	c.mu.Lock()
	b1, d1 := c.tcb.persistBackoff, c.tcb.persistDeadline
	c.mu.Unlock()
	if b1 != 1 {
		t.Fatalf("probe 1 回でバックオフ段階が進んでいない: got %d want 1", b1)
	}

	// 指数バックオフ: 次の persist 満了は倍の間隔 (persistInitial<<1) 後。
	c.mu.Lock()
	gap := d1.Sub(fc.now)
	c.mu.Unlock()
	if gap != persistInitial<<1 {
		t.Fatalf("バックオフ後の persist 間隔が倍化していない: got %v want %v", gap, persistInitial<<1)
	}

	// 窓 0 継続中は persist が止まらない (再 arm され続ける)。
	c.mu.Lock()
	stillArmed := c.tcb.persistArmed
	c.mu.Unlock()
	if !stillArmed {
		t.Fatal("窓 0 継続中に persist が停止した")
	}

	// 窓>0 を受けたら persist 解除。以降は通常送出が動く (デッドロック回避)。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: c.SndUna(), Window: maxWindow}, nil)
	c.mu.Lock()
	armed := c.tcb.persistArmed
	c.mu.Unlock()
	if armed {
		t.Fatal("窓>0 受信後も persist が解除されていない")
	}
}

// 受信側は窓 0 でも probe (1 octet) に ACK を返す (窓再開の信頼経路)。
func TestReceiverAcksProbeUnderZeroWindow(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	c.mu.Lock()
	c.tcb.rcv.wnd = 0 // 受信窓を 0 にする
	c.mu.Unlock()
	drainSegs(peer)
	_, peer2, _ := c.swapPeer(t)

	// 窓 0 への probe: RCV.NXT の 1 octet。受理性テストで弾かれるが ACK は返る。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.RcvNxt(), AckNum: 1001, Window: maxWindow}, []byte{0x42})
	segs := drainSegs(peer2)
	if len(segs) == 0 || !segs[0].h.Flags.Has(FlagACK) {
		t.Fatalf("窓 0 での probe に ACK を返していない: %+v", segs)
	}
}

// delayed ACK: フルセグ 1 個なら ACK を遅延し、delAckTimeout (<0.5s) で送る。
func TestDelayedAckSingleSegment(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	drainSegs(peer)
	_, peer2, _ := c.swapPeer(t)

	// in-order フルセグメント 1 個 → ACK 遅延。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.RcvNxt(), AckNum: 1001, Window: maxWindow}, make([]byte, defaultMSS))
	if segs := drainSegs(peer2); len(segs) != 0 {
		t.Fatalf("フル 1 個目で即 ACK してはいけない (遅延のはず): %+v", segs)
	}
	if delAckTimeout >= 500*time.Millisecond {
		t.Fatalf("delayed ACK の遅延は 0.5s 未満必須: %v", delAckTimeout)
	}
	_, peer3, _ := c.swapPeer(t)
	fc.advance(delAckTimeout)
	c.Tick() // 遅延満了 → ACK
	if segs := drainSegs(peer3); len(segs) != 1 || !segs[0].h.Flags.Has(FlagACK) {
		t.Fatalf("delayed ACK が満了で送られない: %+v", segs)
	}
}

// delayed ACK: フルセグ 2 個目で即 ACK (溜めず即返す)。
func TestDelayedAckSecondSegmentImmediate(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	drainSegs(peer)
	_, peer2, _ := c.swapPeer(t)

	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.RcvNxt(), AckNum: 1001, Window: maxWindow}, make([]byte, defaultMSS))
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.RcvNxt(), AckNum: 1001, Window: maxWindow}, make([]byte, defaultMSS))
	segs := drainSegs(peer2)
	if len(segs) != 1 || !segs[0].h.Flags.Has(FlagACK) {
		t.Fatalf("フル 2 個目で即 ACK が返らない: %+v", segs)
	}
}

// delayed ACK: out-of-order は即 ACK (損失検出を急がせる)。
func TestOutOfOrderImmediateAck(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	drainSegs(peer)
	_, peer2, _ := c.swapPeer(t)

	// gap を空けた先行セグメント → 即 ACK。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: c.RcvNxt() + 100, AckNum: 1001, Window: maxWindow}, make([]byte, 50))
	segs := drainSegs(peer2)
	if len(segs) != 1 || !segs[0].h.Flags.Has(FlagACK) {
		t.Fatalf("out-of-order で即 ACK が返らない: %+v", segs)
	}
}

// 窓更新採用: 古い ACK (WL1/WL2 が後退) で SND.WND を上書きしない。
func TestStaleAckDoesNotUpdateWindow(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	drainSegs(peer)
	// 新しい ACK で窓を 1000 に。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1001, Window: 1000}, nil)
	c.mu.Lock()
	w1 := c.tcb.snd.wnd
	c.mu.Unlock()
	// 古い ACK (同じ AckNum, より小さい SeqNum) で窓 50 を広告 → 採用しない。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 4000, AckNum: 1001, Window: 50}, nil)
	c.mu.Lock()
	w2 := c.tcb.snd.wnd
	c.mu.Unlock()
	if w2 != w1 {
		t.Fatalf("古い ACK で SND.WND が上書きされた: %d -> %d", w1, w2)
	}
}

// 受信側 SWS 回避: 開ける量が閾値未満なら現窓を維持し、小窓を広告しない。
// 閾値以上なら buffTotal-used まで開く (RFC 1122 §4.2.3.3)。
func TestAdvertiseWindowSWS(t *testing.T) {
	// buffTotal=1000, effMSS=100 → 閾値 = min(500, 100) = 100。
	const buff, mss = 1000, 100
	tests := []struct {
		name   string
		used   uint32
		curWnd uint32
		want   uint32
	}{
		// 現窓 950, 使用 50 → 開ける余地 = (1000-50)-950 = 0 < 100 → 維持。
		{"小さく開く状況は維持", 50, 950, 950},
		// 現窓 0 (zero window), 使用 0 → 開ける余地 1000 >= 100 → 全開。
		{"閾値以上で全開", 0, 0, 1000},
		// 現窓 940, 使用 0 → 余地 = 1000-940 = 60 < 100 → 維持。
		{"60 octet は閾値未満で維持", 0, 940, 940},
		// 現窓 900, 使用 0 → 余地 = 100 >= 100 → 全開 1000。
		{"ちょうど閾値で全開", 0, 900, 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := advertiseWindow(buff, tt.used, tt.curWnd, mss)
			if got != tt.want {
				t.Fatalf("advertiseWindow(%d,%d,%d,%d)=%d want %d",
					buff, tt.used, tt.curWnd, mss, got, tt.want)
			}
		})
	}
}

// 送信側 SWS/Nagle 送出判定 (RFC 9293 §3.7.4)。
func TestCanSend(t *testing.T) {
	const mss, maxWnd = 100, 1000 // Fs*Max = 500
	tests := []struct {
		name          string
		d, usable     uint32
		idle          bool
		nagleDisabled bool
		want          bool
	}{
		// (1) フル MSS 組める → 送る。
		{"フル送れる", 200, 1000, false, false, true},
		// (2) idle (未確認なし) なら sub-MSS も即送る。
		{"idle の sub-MSS は送る", 50, 1000, true, false, true},
		// 未確認中の sub-MSS は Nagle で溜める。
		{"未確認中 sub-MSS は溜める", 50, 1000, false, false, false},
		// (3) min(D,U) >= Fs*Max(SND.WND) なら送る。
		{"半窓以上は送る", 600, 1000, false, false, true},
		// Nagle 無効なら未確認中でも即送る。
		{"Nagle 無効で即送る", 50, 1000, false, true, true},
		// 送れるデータが無い。
		{"データなし", 0, 1000, true, false, false},
		// 相手窓 0 (usable=0) は送れない。
		{"窓 0 は送らない", 50, 0, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := canSend(tt.d, tt.usable, mss, maxWnd, tt.idle, tt.nagleDisabled)
			if got != tt.want {
				t.Fatalf("canSend(d=%d,u=%d,idle=%v,nodelay=%v)=%v want %v",
					tt.d, tt.usable, tt.idle, tt.nagleDisabled, got, tt.want)
			}
		})
	}
}

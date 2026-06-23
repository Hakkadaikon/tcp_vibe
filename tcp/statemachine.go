package tcp

import "sync"

// maxWindow は window scale 無しの送信窓上限 (RFC 5961 MAX.SND.WND 既定値)。
const maxWindow uint16 = 65535

// Conn は 1 つの TCP 接続。状態 (TCB) を 1 つの mutex で守り、送信も同じ
// クリティカルセクションで直列化する。受信は onSegment を呼ぶ側 (将来の受信
// ループ goroutine) が 1 本に絞る前提。状態アクセスは必ず mutex 越し。
type Conn struct {
	link  Link
	mu    sync.Mutex
	tcb   TCB
	ports struct {
		src, dst uint16
	}
}

// NewConn は CLOSED 状態の接続を作る。clock は時刻 seam (再送・TIME-WAIT の決定論検証用)。
func NewConn(link Link, clock Clock) *Conn {
	c := &Conn{link: link}
	c.tcb.state = Closed
	c.tcb.clock = clock
	c.tcb.maxSndWnd = maxWindow
	return c
}

// --- 観測ヘルパ (mutex 越し。並行テスト用) ---

// State は現在の状態を返す。
func (c *Conn) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.state
}

// Origin は SYN-RECEIVED の由来を返す。
func (c *Conn) Origin() Origin {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.origin
}

// SndUna は SND.UNA を返す。
func (c *Conn) SndUna() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.snd.una
}

// SndNxt は SND.NXT を返す。
func (c *Conn) SndNxt() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.snd.nxt
}

// RcvNxt は RCV.NXT を返す。
func (c *Conn) RcvNxt() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.rcv.nxt
}

// --- ユーザコール ---

// ActiveOpen は能動オープン。SYN を送り SYN-SENT へ遷移する (RFC 9293 §3.10.1)。
func (c *Conn) ActiveOpen(iss uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.snd.iss = iss
	c.tcb.snd.una = iss
	c.tcb.snd.nxt = iss + 1
	c.tcb.state = SynSent
	c.send(Flags(FlagSYN), iss, 0)
}

// PassiveOpen は受動オープン。LISTEN へ遷移し相手の SYN を待つ (RFC 9293 §3.10.1)。
func (c *Conn) PassiveOpen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.state = Listen
}

// Close はユーザの CLOSE 要求。状態に応じて FIN を送る (RFC 9293 §3.10.4)。
func (c *Conn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.tcb.state {
	case Established, SynReceived:
		c.send(Flags(FlagFIN|FlagACK), c.tcb.snd.nxt, c.tcb.rcv.nxt)
		c.tcb.snd.nxt++ // FIN が 1 seq 消費
		c.tcb.state = FinWait1
	case CloseWait:
		c.send(Flags(FlagFIN|FlagACK), c.tcb.snd.nxt, c.tcb.rcv.nxt)
		c.tcb.snd.nxt++
		c.tcb.state = LastAck
	}
}

// --- タイマ ---

// Tick は時刻経過を駆動する。TIME-WAIT の 2MSL 満了で CLOSED へ落とし、
// 再送キューの RTO 満了で先頭を再送する。
// 満了判定は clock seam の現在時刻と deadline の比較で決定論的に行う。
func (c *Conn) Tick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tcb.state == TimeWait && !c.tcb.clock().Before(c.tcb.timeWaitDeadline) {
		c.tcb.state = Closed
	}
	c.checkRetransmit()
}

// checkRetransmit は再送キュー先頭の RTO 満了を判定し、満了なら再送する。
// 送信回数が上限 (R2) を超えたら接続を閉じる (RFC 9293 §3.8.1, R-091/093)。
func (c *Conn) checkRetransmit() {
	if len(c.tcb.retxQueue) == 0 {
		return
	}
	front := &c.tcb.retxQueue[0]
	deadline := front.sentAt.Add(c.tcb.curRTO)
	if c.tcb.clock().Before(deadline) {
		return // RTO 未到達
	}
	if front.retries >= maxRetransmits {
		// R2 到達: 再送し尽くした → 接続を閉じる (LIVE-3 の失敗決着、宙吊り回避)。
		c.tcb.state = Closed
		c.tcb.retxQueue = nil
		c.tcb.curRTO = 0
		return
	}
	// 先頭を再送し、回数・送信時刻を更新、RTO を倍化 (上限 maxRTO)。
	c.writeSeg(front.flags, front.seq, c.tcb.rcv.nxt)
	front.retries++
	front.sentAt = c.tcb.clock()
	if c.tcb.curRTO < maxRTO {
		c.tcb.curRTO *= 2
		if c.tcb.curRTO > maxRTO {
			c.tcb.curRTO = maxRTO
		}
	}
}

// ackRetxQueue は acceptable ACK で完全確認された先頭エントリ群を除去する。
// SEG.SEQ+SEG.LEN =< SEG.ACK を満たすぶんが確認済み (RFC 9293 §3.8.1, R-012)。
// 除去後、残りがあれば RTO を初期値に戻して再起動、空ならタイマ停止。
func (c *Conn) ackRetxQueue(ack uint32) {
	removed := false
	for len(c.tcb.retxQueue) > 0 {
		s := c.tcb.retxQueue[0]
		if !SeqLEQ(s.seq+s.seqLen(), ack) {
			break
		}
		c.tcb.retxQueue = c.tcb.retxQueue[1:]
		removed = true
	}
	if !removed {
		return
	}
	if len(c.tcb.retxQueue) == 0 {
		c.tcb.curRTO = 0 // 全確認 → タイマ停止
		return
	}
	// 新しい先頭から測り直す (RTO 初期化 + 送信時刻起点を現在へ)。
	c.tcb.curRTO = initialRTO
	c.tcb.retxQueue[0].sentAt = c.tcb.clock()
}

// --- 送信ヘルパ ---

// send は 1 セグメントを送る。ヘッダのみ (本タスクではペイロード送信は扱わない)。
// 送信は mutex 保持中に呼ぶこと。seq を消費するセグメント (SYN/FIN/データ) は
// 再送キューに積み、RTO タイマを起動する (RFC 9293 §3.8.1)。
func (c *Conn) send(flags Flags, seq, ack uint32) {
	c.writeSeg(flags, seq, ack)
	// seq を占めるセグメントだけ再送対象。ACK/RST 単独や challenge ACK は積まない。
	seg := retxSeg{seq: seq, flags: flags, sentAt: c.tcb.clock()}
	if seg.seqLen() == 0 {
		return
	}
	if len(c.tcb.retxQueue) == 0 {
		c.tcb.curRTO = initialRTO // キューが空からの追加でタイマ起動
	}
	c.tcb.retxQueue = append(c.tcb.retxQueue, seg)
}

// writeSeg はヘッダを組んで 1 セグメントをリンクへ書く (キュー操作なし)。
func (c *Conn) writeSeg(flags Flags, seq, ack uint32) {
	h := TCPHeader{
		SrcPort:    c.ports.src,
		DstPort:    c.ports.dst,
		SeqNum:     seq,
		AckNum:     ack,
		DataOffset: 5,
		Flags:      flags,
		Window:     c.tcb.rcv.wnd,
	}
	_ = c.link.WritePacket(h.Marshal())
}

// sendChallengeAck は RFC 5961 の challenge ACK を送る。3 攻撃 (blind RST/SYN/
// data injection) いずれも同一形式 <SEQ=SND.NXT><ACK=RCV.NXT><CTL=ACK>。
// RFC 5961 §7 のレート制限: 任意の challengeAckWindow 窓で challengeAckLimit 個まで。
// timestamp+counter で実装しタイマは持たない。上限超は送出しない (抑制)。
func (c *Conn) sendChallengeAck() {
	if !c.allowChallengeAck() {
		return
	}
	c.send(Flags(FlagACK), c.tcb.snd.nxt, c.tcb.rcv.nxt)
}

// allowChallengeAck はレート制限のトークン判定。窓が経過していればカウンタを
// リセットし、窓内なら上限未満のときだけ true を返してカウントを進める。
func (c *Conn) allowChallengeAck() bool {
	now := c.tcb.clock()
	if now.Sub(c.tcb.challengeWindowStart) > challengeAckWindow {
		c.tcb.challengeWindowStart = now
		c.tcb.challengeCount = 0
	}
	if c.tcb.challengeCount >= challengeAckLimit {
		return false
	}
	c.tcb.challengeCount++
	return true
}

// sendAck は素の ACK を返す (受理不可セグメントへの応答・FIN への ACK 等)。
func (c *Conn) sendAck() {
	c.send(Flags(FlagACK), c.tcb.snd.nxt, c.tcb.rcv.nxt)
}

// sendRst は RST を送る。CLOSED や LISTEN での不正セグメント応答に使う。
func (c *Conn) sendRst(seq uint32) {
	c.send(Flags(FlagRST), seq, 0)
}

// segLen は SEG.LEN を返す。SYN と FIN はそれぞれ 1 seq を占める (RFC 9293 §3.4)。
func segLen(h TCPHeader, payload []byte) uint32 {
	n := uint32(len(payload))
	if h.Flags.Has(FlagSYN) {
		n++
	}
	if h.Flags.Has(FlagFIN) {
		n++
	}
	return n
}

// inWindow は seq が受信窓 [RCV.NXT, RCV.NXT+RCV.WND) に入るか。
func (t *TCB) inWindow(seq uint32) bool {
	if t.rcv.wnd == 0 {
		return seq == t.rcv.nxt
	}
	return SeqLEQ(t.rcv.nxt, seq) && SeqLT(seq, t.rcv.nxt+uint32(t.rcv.wnd))
}

// acceptable は受理性テスト (RFC 9293 §3.10.7.4, R-001〜004) を判定する。
func (t *TCB) acceptable(h TCPHeader, payload []byte) bool {
	sl := segLen(h, payload)
	switch {
	case sl == 0 && t.rcv.wnd == 0:
		return h.SeqNum == t.rcv.nxt
	case sl == 0 && t.rcv.wnd > 0:
		return t.inWindow(h.SeqNum)
	case sl > 0 && t.rcv.wnd == 0:
		return false
	default: // sl > 0 && wnd > 0: 始端 or 終端が窓内
		return t.inWindow(h.SeqNum) || t.inWindow(h.SeqNum+sl-1)
	}
}

// onSegment は受信セグメントを処理する。RFC 9293 §3.10.7 の固定処理順序で実装する。
// 非同期状態 (CLOSED/LISTEN/SYN-SENT) は §3.10.7.1-3 の個別経路、同期状態は
// §3.10.7.4 の順序: 1 受理性 → 2 RST → 3 SYN → 4 ACK field → 5 text → 6 FIN。
func (c *Conn) onSegment(h TCPHeader, payload []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.tcb.state {
	case Closed:
		c.onSegmentClosed(h, payload)
	case Listen:
		c.onSegmentListen(h)
	case SynSent:
		c.onSegmentSynSent(h)
	default:
		c.onSegmentSynchronized(h, payload)
	}
}

// onSegmentClosed: CLOSED は TCB が無い。RST 以外には RST を返す (RFC 9293 §3.10.7.1)。
func (c *Conn) onSegmentClosed(h TCPHeader, payload []byte) {
	if h.Flags.Has(FlagRST) {
		return
	}
	if h.Flags.Has(FlagACK) {
		c.sendRst(h.AckNum)
	} else {
		c.sendRst(0)
		c.send(Flags(FlagRST|FlagACK), 0, h.SeqNum+segLen(h, payload))
	}
}

// onSegmentListen: LISTEN の処理 (RFC 9293 §3.10.7.2)。
//   - RST → 無視
//   - ACK → RST 応答 (LISTEN 維持)
//   - SYN → SYN,ACK を送り SYN-RECEIVED (passive origin)
func (c *Conn) onSegmentListen(h TCPHeader) {
	if h.Flags.Has(FlagRST) {
		return
	}
	if h.Flags.Has(FlagACK) {
		c.sendRst(h.AckNum)
		return
	}
	if h.Flags.Has(FlagSYN) {
		c.tcb.rcv.irs = h.SeqNum
		c.tcb.rcv.nxt = h.SeqNum + 1
		c.tcb.snd.una = c.tcb.snd.iss
		c.tcb.snd.nxt = c.tcb.snd.iss + 1
		c.tcb.origin = OriginPassive
		c.tcb.state = SynReceived
		c.send(Flags(FlagSYN|FlagACK), c.tcb.snd.iss, c.tcb.rcv.nxt)
	}
}

// onSegmentSynSent: SYN-SENT の処理 (RFC 9293 §3.10.7.3 + RFC 5961 §4.2)。
func (c *Conn) onSegmentSynSent(h TCPHeader) {
	// ACK field チェック: 自 SYN を確認する ACK か (R-035/036)。
	ackOK := false
	if h.Flags.Has(FlagACK) {
		// SEG.ACK =< ISS or SEG.ACK > SND.NXT は受理不可。
		if SeqLEQ(h.AckNum, c.tcb.snd.iss) || SeqGT(h.AckNum, c.tcb.snd.nxt) {
			if !h.Flags.Has(FlagRST) {
				c.sendRst(h.AckNum)
			}
			return
		}
		ackOK = true
	}

	// RST: ACK が自 SYN を確認しているときのみ受理 (R-113)。
	if h.Flags.Has(FlagRST) {
		if ackOK {
			c.tcb.state = Closed
		}
		return
	}

	if !h.Flags.Has(FlagSYN) {
		return // SYN/RST 共に無ければ破棄
	}

	// SYN を受信。
	c.tcb.rcv.irs = h.SeqNum
	c.tcb.rcv.nxt = h.SeqNum + 1
	if ackOK {
		// SYN,ACK で自 SYN が確認された → ESTABLISHED (R-037)。
		c.tcb.snd.una = h.AckNum
		c.ackRetxQueue(h.AckNum) // 確認済み SYN を再送キューから除去 (R-012)
		c.tcb.state = Established
		c.sendAck()
	} else {
		// bare SYN (同時オープン) → SYN-RECEIVED (active origin) (R-038)。
		c.tcb.origin = OriginActive
		c.tcb.state = SynReceived
		c.send(Flags(FlagSYN|FlagACK), c.tcb.snd.iss, c.tcb.rcv.nxt)
	}
}

// onSegmentSynchronized は同期状態 (および SYN-RECEIVED) の固定処理順序。
// RFC 9293 §3.10.7.4 + RFC 5961 の三チェックを順序通りに適用する。
func (c *Conn) onSegmentSynchronized(h TCPHeader, payload []byte) {
	// 1. 受理性テスト。受理不可かつ RST 無なら空 ACK を返し破棄 (R-006/072)。
	//    RST は受理不可でも 5961 の窓判定へ進めるため別扱い。
	if !c.tcb.acceptable(h, payload) && !h.Flags.Has(FlagRST) {
		c.sendAck()
		return
	}

	// 2. RST 処理 (RFC 5961 §3.2, 三チェックの (a))。
	if h.Flags.Has(FlagRST) {
		c.handleRst(h)
		return
	}

	// 3. SYN 処理 (RFC 5961 §4.2, 三チェックの (b))。同期状態で SYN は
	//    seq によらず challenge ACK のみ。reset しない (R-114, INV-006)。
	if h.Flags.Has(FlagSYN) {
		c.sendChallengeAck()
		return
	}

	// 4. ACK field 処理 (RFC 5961 §5.2 data injection を含む)。
	if !h.Flags.Has(FlagACK) {
		return // 同期状態で ACK off は破棄 (R-084)。
	}
	if !c.handleAck(h) {
		return // ACK 受理範囲外: challenge ACK 済み、データ適用せず破棄。
	}

	// 5. text 処理。
	// ponytail: 本タスクは状態遷移が主眼。受信データの user buffer 蓄積は別タスク。
	//           ここでは窓内データ分だけ RCV.NXT を前進させ ACK を返す最小処理に留める。
	if len(payload) > 0 && h.SeqNum == c.tcb.rcv.nxt {
		c.tcb.rcv.nxt += uint32(len(payload))
		c.sendAck()
	}

	// 6. FIN 処理。
	if h.Flags.Has(FlagFIN) {
		c.handleFin(h)
	}
}

// handleRst は同期状態での RST を RFC 5961 §3.2 の三チェックで処理する。
// reset の "根拠" を厳格化する (INV-005): SEG.SEQ=RCV.NXT のときだけ reset。
func (c *Conn) handleRst(h TCPHeader) {
	if !c.tcb.inWindow(h.SeqNum) {
		return // 窓外 RST → silently drop (reset しない) (R-110)。
	}
	if h.SeqNum != c.tcb.rcv.nxt {
		c.sendChallengeAck() // 窓内だが !=RCV.NXT → challenge のみ、reset 禁止 (R-112)。
		return
	}
	// SEG.SEQ=RCV.NXT → reset (R-111)。
	c.resetConnection()
}

// resetConnection は RST 受理時の状態遷移。SYN-RECEIVED は由来で遷移先が変わる
// (passive→LISTEN, active→CLOSED)。それ以外の同期状態は CLOSED へ abort。
func (c *Conn) resetConnection() {
	if c.tcb.state == SynReceived && c.tcb.origin == OriginPassive {
		c.tcb.state = Listen
		return
	}
	c.tcb.state = Closed
}

// handleAck は ACK field を処理する。RFC 5961 §5.2 の受理範囲チェック
// (SND.UNA-MAX.SND.WND) =< SEG.ACK =< SND.NXT を先に行い (R-115, INV-007)、
// 範囲外なら challenge ACK を返して false を返す (SND.UNA を前進させない)。
// 範囲内なら acceptable ACK (R-011) のときだけ SND.UNA を前進させ、状態遷移する。
func (c *Conn) handleAck(h TCPHeader) bool {
	lo := c.tcb.snd.una - uint32(c.tcb.maxSndWnd)
	if SeqLT(h.AckNum, lo) || SeqGT(h.AckNum, c.tcb.snd.nxt) {
		c.sendChallengeAck()
		return false
	}

	// acceptable ack (SND.UNA < SEG.ACK =< SND.NXT) でのみ UNA 前進 (INV-001/002)。
	if AcceptableAck(c.tcb.snd.una, h.AckNum, c.tcb.snd.nxt) {
		c.tcb.snd.una = h.AckNum
		c.ackRetxQueue(h.AckNum) // 確認済みセグメントを再送キューから除去 (R-012)
	}
	c.advanceStateOnAck(h)
	return true
}

// finAcked は自分が送った FIN がこの ACK で確認されたか。FIN は SND.NXT-1 を占める。
func (c *Conn) finAcked() bool {
	return c.tcb.snd.una == c.tcb.snd.nxt
}

// advanceStateOnAck は ACK 受理に伴う状態遷移 (FIN の確認による close 進行)。
func (c *Conn) advanceStateOnAck(h TCPHeader) {
	switch c.tcb.state {
	case SynReceived:
		c.tcb.state = Established
	case FinWait1:
		if c.finAcked() {
			c.tcb.state = FinWait2
		}
	case Closing:
		if c.finAcked() {
			c.enterTimeWait()
		}
	case LastAck:
		if c.finAcked() {
			c.tcb.state = Closed
		}
	}
}

// handleFin は FIN 受信を処理する。RCV.NXT を FIN 分前進させ ACK を返し、
// 状態を進める (RFC 9293 §3.10.7.4)。
func (c *Conn) handleFin(h TCPHeader) {
	// FIN は受信窓左端で消費される。text 段で payload 分は前進済みなので
	// FIN 1 seq 分だけ RCV.NXT を進める。
	c.tcb.rcv.nxt++
	c.sendAck()
	switch c.tcb.state {
	case Established, SynReceived:
		c.tcb.state = CloseWait
	case FinWait1:
		if c.finAcked() {
			c.enterTimeWait() // 自 FIN が ACK 済 → TIME-WAIT 直行 (Note2)
		} else {
			c.tcb.state = Closing
		}
	case FinWait2:
		c.enterTimeWait()
	case TimeWait:
		c.restartTimeWait() // FIN 再送 → ACK 再送 (sendAck 済) + 2MSL 再起動
	}
}

// enterTimeWait は TIME-WAIT へ入り 2MSL タイマを起動する。
func (c *Conn) enterTimeWait() {
	c.tcb.state = TimeWait
	c.restartTimeWait()
}

// restartTimeWait は 2MSL タイマを再起動する。
func (c *Conn) restartTimeWait() {
	c.tcb.timeWaitDeadline = c.tcb.clock().Add(timeWaitDuration)
}

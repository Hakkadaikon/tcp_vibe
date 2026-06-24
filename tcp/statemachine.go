package tcp

import (
	"sync"
	"time"
)

// maxWindow は window scale 無しの送信窓上限 (RFC 5961 MAX.SND.WND 既定値)。
const maxWindow uint16 = 65535

// defaultRcvWindow は接続開始時に広告する既定の受信窓。0 だとデータ/FIN
// (SEG.LEN>0) が受理性テスト (RFC 9293 §3.10.7.4) で弾かれて通信できないため、
// window scale 無しの最大値を広告する。
const defaultRcvWindow uint16 = maxWindow

// Endpoint は接続端点 (IPv4 アドレスとポート)。送出する IP/TCP ヘッダの
// 送信元・宛先と、TCP チェックサムの擬似ヘッダに使う。
type Endpoint struct {
	IP   [4]byte
	Port uint16
}

// Conn は 1 つの TCP 接続。状態 (TCB) を 1 つの mutex で守り、送信も同じ
// クリティカルセクションで直列化する。受信は onSegment を呼ぶ側 (将来の受信
// ループ goroutine) が 1 本に絞る前提。状態アクセスは必ず mutex 越し。
type Conn struct {
	link   Link
	local  Endpoint
	remote Endpoint
	mu     sync.Mutex
	tcb    TCB
	ports  struct {
		src, dst uint16
	}
}

// NewConn は CLOSED 状態の接続を作る。clock は時刻 seam (再送・TIME-WAIT の決定論検証用)。
// local/remote は送出パケットの IP/TCP ヘッダと TCP チェックサム擬似ヘッダに使う。
func NewConn(link Link, clock Clock, local, remote Endpoint) *Conn {
	c := &Conn{link: link, local: local, remote: remote}
	c.ports.src = local.Port
	c.ports.dst = remote.Port
	c.tcb.state = Closed
	c.tcb.clock = clock
	c.tcb.maxSndWnd = uint32(maxWindow)
	c.tcb.timeWaitDuration = timeWaitDuration // 既定 2*MSL (RFC 通り)
	c.tcb.cong = newCongestion(defaultMSS)
	return c
}

// --- 観測ヘルパ (mutex 越し。並行テスト用) ---

// State は現在の状態を返す。
func (c *Conn) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.state
}

// ReachedEstablished は一度でも ESTABLISHED に達したかを返す。
// 現在状態が ESTABLISHED を過ぎていても (即 CLOSE-WAIT 等) 握手成立を取りこぼさない。
func (c *Conn) ReachedEstablished() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.reachedEstablished
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

// RttSampled は RTT を一度でも測定したかを返す (テスト観測用)。
func (c *Conn) RttSampled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.rttValid
}

// CurRTOms は現在の RTO をミリ秒で返す (テスト観測用)。
func (c *Conn) CurRTOms() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.curRTO.Milliseconds()
}

// SrttMS は RTT 推定器の現在の SRTT (ミリ秒) を返す (テスト観測用)。
func (c *Conn) SrttMS() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tcb.rtt.srtt
}

// SetCwnd は輻輳ウィンドウを設定する (テストで送信量を絞る/広げるための seam)。
func (c *Conn) SetCwnd(cwnd uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.cong.cwnd = cwnd
}

// SetMSL は MSL を設定し、TIME-WAIT の linger を 2*MSL に更新する。
// 握手前 (CLOSED) に呼ぶ前提。デモで短い MSL を注入し TIME-WAIT を早く抜けるのに使う。
func (c *Conn) SetMSL(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.timeWaitDuration = 2 * d
}

// --- ユーザコール ---

// ActiveOpen は能動オープン。SYN を送り SYN-SENT へ遷移する (RFC 9293 §3.10.1)。
func (c *Conn) ActiveOpen(iss uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.snd.iss = iss
	c.tcb.snd.una = iss
	c.tcb.snd.nxt = iss + 1
	c.tcb.rcv.wnd = defaultRcvWindow
	c.tcb.state = SynSent
	c.sendSyn(Flags(FlagSYN), iss, 0)
}

// sendSyn は SYN / SYN-ACK をオプション (MSS/WScale/TS/SACK-Permitted) 込みで送り、
// 再送キューに積む。再送は writeSeg 経由でオプションなしになるが SYN は折衝済みなら
// 1 度通れば足りる。ponytail: SYN 再送時のオプション再送は割愛 (握手は早期に確立する)。
func (c *Conn) sendSyn(flags Flags, seq, ack uint32) {
	c.writeSegOpts(flags, seq, ack, nil, c.synOptionBytes(ack))
	seg := retxSeg{seq: seq, flags: flags, sentAt: c.tcb.clock()}
	if len(c.tcb.retxQueue) == 0 {
		c.tcb.curRTO = c.currentRTO()
	}
	c.tcb.retxQueue = append(c.tcb.retxQueue, seg)
}

// PassiveOpen は受動オープン。LISTEN へ遷移し相手の SYN を待つ (RFC 9293 §3.10.1)。
func (c *Conn) PassiveOpen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.rcv.wnd = defaultRcvWindow
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
// 送信回数が上限 (R2) を超えたら接続を閉じる (RFC 9293 §3.8.1)。
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
	// RTO 満了で輻輳制御を更新 (cwnd=1SMSS, ssthresh 半減は初回再送のみ)。
	flightSize := c.tcb.snd.nxt - c.tcb.snd.una
	c.tcb.cong.onRtoTimeout(flightSize)
	// 先頭を再送し、回数・送信時刻を更新、RTO を倍化 (上限 maxRTO)。payload 込みで再送。
	c.writeSeg(front.flags, front.seq, c.tcb.rcv.nxt, front.payload)
	front.retries++
	front.retransmitted = true // Karn: この ACK からは RTT を測らない
	front.sentAt = c.tcb.clock()
	c.tcb.curRTO = backoffRTO(c.tcb.curRTO)
}

// backoffRTO は満了時の指数バックオフ。time.Duration を ms 整数に直して backoff
// (= min(cap, cur*2), RFC 6298 §5.5) を適用し、上限 maxRTO で飽和させる。
func backoffRTO(cur time.Duration) time.Duration {
	curMS := uint32(cur.Milliseconds())
	capMS := uint32(maxRTO.Milliseconds())
	return time.Duration(backoff(capMS, curMS)) * time.Millisecond
}

// ackRetxQueue は acceptable ACK で完全確認された先頭エントリ群を除去する。
// SEG.SEQ+SEG.LEN =< SEG.ACK を満たすぶんが確認済み (RFC 9293 §3.8.1)。
// 確認したセグメントのうち再送していないものから RTT サンプルを 1 つ採り
// (Karn, RFC 6298 §3, §4)、推定器を更新して RTO を測り直す。
func (c *Conn) ackRetxQueue(ack uint32) {
	removed := false
	for len(c.tcb.retxQueue) > 0 {
		s := c.tcb.retxQueue[0]
		if !SeqLEQ(s.seq+s.seqLen(), ack) {
			break
		}
		// Karn: 再送していないセグメントだけ RTT サンプルに使う。
		// timestamps 有効時は TSecr で測るのでここでは測らない (二重計上を避ける)。
		if !s.retransmitted && !c.tcb.tsOK {
			c.sampleRTT(s.sentAt)
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
	// 新しい先頭から測り直す (RTO 再起動 + 送信時刻起点を現在へ)。
	c.tcb.curRTO = c.currentRTO()
	c.tcb.retxQueue[0].sentAt = c.tcb.clock()
}

// sampleRTT は 1 つの RTT サンプル (now - sentAt) で推定器を更新する (RFC 6298)。
// 初回は initEst、以降は updateEst (順序: RTTVAR→SRTT)。負・0 は 1ms に丸める。
func (c *Conn) sampleRTT(sentAt time.Time) {
	r := c.tcb.clock().Sub(sentAt).Milliseconds()
	if r < 1 {
		r = 1 // 粒度未満は 1ms (RFC 6298 §4 step 2.4 の趣旨)
	}
	rms := uint32(r)
	if !c.tcb.rttValid {
		c.tcb.rtt = initEst(rms, clockGranMS)
		c.tcb.rttValid = true
	} else {
		c.tcb.rtt = c.tcb.rtt.UpdateEst(rms)
	}
}

// sampleRTTFromTimestamp は echo された TSecr から RTT を測り推定器を更新する
// (RFC 7323 §4.1)。RTT = 現在の timestamp clock - TSecr (ms)。timestamp clock と
// 同じ wrap 空間なので減算は modulo 2^32 で正しい。負・0 は 1ms に丸める。
func (c *Conn) sampleRTTFromTimestamp(tsecr uint32) {
	r := c.tcb.tsNow() - tsecr // wrap 減算 = 経過 ms
	if r == 0 {
		r = 1
	}
	if !c.tcb.rttValid {
		c.tcb.rtt = initEst(r, clockGranMS)
		c.tcb.rttValid = true
	} else {
		c.tcb.rtt = c.tcb.rtt.UpdateEst(r)
	}
}

// currentRTO は今の RTO (time.Duration) を返す。RTT 未測定なら initialRTO、
// 測定済みなら推定器の Rto() (下限 1000ms) を使う (RFC 6298 §2.1/§2.4)。
func (c *Conn) currentRTO() time.Duration {
	if !c.tcb.rttValid {
		return initialRTO
	}
	return time.Duration(c.tcb.rtt.Rto()) * time.Millisecond
}

// --- 送信ヘルパ ---

// send は 1 セグメント (ヘッダのみ) を送る。データを載せる送信は sendData を使う。
// 送信は mutex 保持中に呼ぶこと。seq を消費するセグメント (SYN/FIN) は再送キューに
// 積み、RTO タイマを起動する (RFC 9293 §3.8.1)。
func (c *Conn) send(flags Flags, seq, ack uint32) {
	c.sendData(flags, seq, ack, nil)
}

// sendData は payload を載せて 1 セグメントを送る。seq を占めるセグメント
// (SYN/FIN/データ) は payload ごと再送キューに積み RTO タイマを起動する。
func (c *Conn) sendData(flags Flags, seq, ack uint32, payload []byte) {
	c.writeSeg(flags, seq, ack, payload)
	// seq を占めるセグメントだけ再送対象。ACK/RST 単独や challenge ACK は積まない。
	seg := retxSeg{seq: seq, flags: flags, payload: payload, sentAt: c.tcb.clock()}
	if seg.seqLen() == 0 {
		return
	}
	if len(c.tcb.retxQueue) == 0 {
		c.tcb.curRTO = c.currentRTO() // キューが空からの追加でタイマ起動
	}
	c.tcb.retxQueue = append(c.tcb.retxQueue, seg)
}

// writeSeg はヘッダを組んで 1 セグメントを完全な IPv4 パケットとしてリンクへ書く
// (キュー操作なし)。TCP チェックサムを擬似ヘッダ込みで埋めてから IPv4 ヘッダで包む。
// これにより送出が受信ループ (IPv4 を剥がし TCP チェックサムを検証する) の前提と一致する。
func (c *Conn) writeSeg(flags Flags, seq, ack uint32, payload []byte) {
	c.writeSegOpts(flags, seq, ack, payload, nil)
}

// writeSegOpts は writeSeg にオプション領域 (生バイト) を加えて送る。
// 出力 SEG.WND は折衝した Rcv.Wind.Shift で右シフトする (SYN/SYN-ACK は生値)。
// ACK を立てたセグメントを送った時点を Last.ACK.sent として記録する (TS.Recent ゲート用)。
func (c *Conn) writeSegOpts(flags Flags, seq, ack uint32, payload, opts []byte) {
	// timestamps 折衝済みの非 SYN・非 RST セグメントには TS option を載せる
	// (RFC 7323 §3)。TSval=現在のクロック、ACK セット時 TSecr=TS.Recent、無しは 0。
	if opts == nil && c.tcb.tsOK && !flags.Has(FlagSYN) && !flags.Has(FlagRST) {
		o := TCPOptions{HasTimestamp: true, TSVal: c.tcb.tsNow()}
		if flags.Has(FlagACK) {
			o.TSecr = c.tcb.tsRecent
		}
		opts = o.Marshal()
	}
	win := c.tcb.rcv.wnd
	// SYN を含まないセグメントの window だけスケールする (RFC 7323 §2.3)。
	if !flags.Has(FlagSYN) && c.tcb.rcvWindShift > 0 {
		win = uint16(uint32(c.tcb.rcv.wnd) >> c.tcb.rcvWindShift)
	}
	if flags.Has(FlagACK) {
		c.tcb.lastAckSent = ack
	}
	h := TCPHeader{
		SrcPort:    c.ports.src,
		DstPort:    c.ports.dst,
		SeqNum:     seq,
		AckNum:     ack,
		DataOffset: 5,
		Flags:      flags,
		Window:     win,
		Options:    opts,
	}
	seg := append(h.Marshal(), payload...)
	putBe16(seg, 16, TCPChecksum(c.local.IP, c.remote.IP, seg))
	ip := IPv4Header{
		Protocol:    6, // TCP
		TotalLength: uint16(ipv4MinHeader + len(seg)),
		TTL:         64,
		SrcAddr:     c.local.IP,
		DstAddr:     c.remote.IP,
	}
	debugf("send: flags=%s seq=%d ack=%d dst=%s:%d", flagsStr(flags), seq, ack, ipStr(c.remote.IP), c.ports.dst)
	_ = c.link.WritePacket(append(ip.Marshal(), seg...))
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

// acceptable は受理性テスト (RFC 9293 §3.10.7.4) を判定する。
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
		if o, err := ParseTCPOptions(h.Options); err == nil {
			c.negotiateOptions(o) // 相手 SYN の option で折衝結果を確定
		}
		c.initSendWindow(h) // 相手の広告窓で SND.WND を初期化 (SYN は生値)
		c.sendSyn(Flags(FlagSYN|FlagACK), c.tcb.snd.iss, c.tcb.rcv.nxt)
	}
}

// onSegmentSynSent: SYN-SENT の処理 (RFC 9293 §3.10.7.3 + RFC 5961 §4.2)。
func (c *Conn) onSegmentSynSent(h TCPHeader) {
	// ACK field チェック: 自 SYN を確認する ACK か。
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

	// RST: ACK が自 SYN を確認しているときのみ受理。
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
	if o, err := ParseTCPOptions(h.Options); err == nil {
		c.negotiateOptions(o) // 相手 SYN/SYN-ACK の option で折衝結果を確定
	}
	if ackOK {
		// SYN,ACK で自 SYN が確認された → ESTABLISHED。
		c.tcb.snd.una = h.AckNum
		c.ackRetxQueue(h.AckNum) // 確認済み SYN を再送キューから除去
		c.initSendWindow(h)      // 相手の広告窓で SND.WND を初期化 (SYN は生値)
		c.tcb.state = Established
		c.tcb.reachedEstablished = true
		c.sendAck()
	} else {
		// bare SYN (同時オープン) → SYN-RECEIVED (active origin)。
		c.tcb.origin = OriginActive
		c.tcb.state = SynReceived
		c.sendSyn(Flags(FlagSYN|FlagACK), c.tcb.snd.iss, c.tcb.rcv.nxt)
	}
}

// onSegmentSynchronized は同期状態 (および SYN-RECEIVED) の固定処理順序。
// RFC 9293 §3.10.7.4 + RFC 5961 の三チェックを順序通りに適用する。
func (c *Conn) onSegmentSynchronized(h TCPHeader, payload []byte) {
	o, _ := ParseTCPOptions(h.Options) // 不正 option は無視 (ゼロ値で続行)

	// 0. PAWS: timestamps 折衝済みで TSopt を持つ非 RST セグメントが TS.Recent より
	//    厳密に古ければ受理しない (RFC 7323 §5.3)。acceptability より前に行い、
	//    drop しても ACK を返す。RST は PAWS 対象外。
	if c.tcb.tsOK && o.HasTimestamp && !h.Flags.Has(FlagRST) &&
		pawsStale(o.TSVal, c.tcb.tsRecent) {
		c.sendAck()
		return
	}

	// 1. 受理性テスト。受理不可かつ RST 無なら空 ACK を返し破棄。
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
	//    seq によらず challenge ACK のみ。reset しない。
	if h.Flags.Has(FlagSYN) {
		c.sendChallengeAck()
		return
	}

	// TS.Recent 更新 (RFC 7323 §4.3): TSopt が新しく (>= TS.Recent) かつ
	//    SEG.SEQ =< Last.ACK.sent のときだけ TS.Recent を進める (環状順序で単調)。
	if c.tcb.tsOK && o.HasTimestamp {
		c.tcb.tsRecent = tsRecentUpdate(c.tcb.tsRecent, o.TSVal, h.SeqNum, c.tcb.lastAckSent)
	}

	// 4. ACK field 処理 (RFC 5961 §5.2 data injection を含む)。
	if !h.Flags.Has(FlagACK) {
		return // 同期状態で ACK off は破棄。
	}
	// TSecr による RTT 測定 (RFC 7323 §4.1, Karn の例外)。echo された TSval から
	//    RTT サンプルを取り推定器へ渡す。再送セグメントでも測れる。
	if c.tcb.tsOK && o.HasTimestamp && o.TSecr != 0 {
		c.sampleRTTFromTimestamp(o.TSecr)
	}
	if !c.handleAck(h, payload) {
		return // ACK 受理範囲外: challenge ACK 済み、データ適用せず破棄。
	}

	// 5. text 処理。窓内データを再組立てバッファへ取り込み RCV.NXT を前進させる。
	if len(payload) > 0 {
		c.acceptText(h, payload)
	}

	// 6. FIN 処理。FIN の seq まで in-order に届いた (RCV.NXT が FIN seq に追いついた)
	//    ときだけ FIN を消費する。先行 FIN (手前にデータ欠けがある) はまだ消費しない。
	if h.Flags.Has(FlagFIN) && h.SeqNum+segLen(h, payload)-1 == c.tcb.rcv.nxt {
		c.handleFin(h)
	}
}

// handleRst は同期状態での RST を RFC 5961 §3.2 の三チェックで処理する。
// reset の "根拠" を厳格化する: SEG.SEQ=RCV.NXT のときだけ reset。
func (c *Conn) handleRst(h TCPHeader) {
	if !c.tcb.inWindow(h.SeqNum) {
		return // 窓外 RST → silently drop (reset しない)。
	}
	if h.SeqNum != c.tcb.rcv.nxt {
		c.sendChallengeAck() // 窓内だが !=RCV.NXT → challenge のみ、reset 禁止。
		return
	}
	// SEG.SEQ=RCV.NXT → reset。
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
// (SND.UNA-MAX.SND.WND) =< SEG.ACK =< SND.NXT を先に行い、
// 範囲外なら challenge ACK を返して false を返す (SND.UNA を前進させない)。
// 範囲内なら acceptable ACK のときだけ SND.UNA を前進させ、状態遷移する。
func (c *Conn) handleAck(h TCPHeader, payload []byte) bool {
	lo := c.tcb.snd.una - c.tcb.maxSndWnd
	if SeqLT(h.AckNum, lo) || SeqGT(h.AckNum, c.tcb.snd.nxt) {
		c.sendChallengeAck()
		return false
	}

	// acceptable ack (SND.UNA < SEG.ACK =< SND.NXT) でのみ UNA 前進。
	if AcceptableAck(c.tcb.snd.una, h.AckNum, c.tcb.snd.nxt) {
		oldUna := c.tcb.snd.una
		flightSize := c.tcb.snd.nxt - oldUna
		ackedBytes := h.AckNum - oldUna
		c.tcb.snd.una = h.AckNum
		c.ackRetxQueue(h.AckNum) // 確認済みセグメントを再送キューから除去 (RTT 採取込み)
		c.releaseAckedSend(oldUna)
		c.tcb.cong.onNewAck(ackedBytes, flightSize) // 新規 ACK で cwnd 更新 (RFC 5681)
	} else if c.isDupAck(h, payload) {
		c.onDuplicateAck(h)
	}
	// 送信窓を更新する。相手の広告窓を受け取り、空いたぶんを送り出す。
	c.updateSendWindow(h)
	c.advanceStateOnAck(h)
	c.flushSend() // 窓が空いた / 確認が進んだぶん未送信データを送る。
	return true
}

// isDupAck は重複 ACK か判定する (RFC 5681)。条件すべて満たすとき真:
//   - 未確認データがある (再送キューが空でない)
//   - データを運んでいない (SEG.LEN=0)
//   - SYN/FIN フラグが立っていない
//   - ACK 番号が既知の最大 ACK (= 現在の SND.UNA) と同じ
//   - 広告窓が直前と変わらない (窓更新でないこと)
func (c *Conn) isDupAck(h TCPHeader, payload []byte) bool {
	if len(c.tcb.retxQueue) == 0 {
		return false // 未確認データなし
	}
	if len(payload) > 0 || h.Flags.Has(FlagSYN) || h.Flags.Has(FlagFIN) {
		return false
	}
	if h.AckNum != c.tcb.snd.una {
		return false
	}
	return uint32(h.Window)<<c.tcb.sndWindShift == c.tcb.snd.wnd // 窓が同一 (窓更新でない)
}

// onDuplicateAck は重複 ACK を輻輳制御へ渡し、3 つ目で損失セグメントを即再送する
// (fast retransmit, RFC 5681 §3.2)。flightSize は ACK 前の送信中バイト数。
func (c *Conn) onDuplicateAck(h TCPHeader) {
	flightSize := c.tcb.snd.nxt - c.tcb.snd.una
	if flightSize == 0 {
		return // 判定は flightSize>0 のときだけ
	}
	wasFR := c.tcb.cong.state == ccFastRecovery
	c.tcb.cong.onDupAck(flightSize)
	// 3 つ目 (= FR に入った瞬間) で再送キュー先頭 (損失セグメント) を即再送する。
	if !wasFR && c.tcb.cong.state == ccFastRecovery {
		c.fastRetransmit()
	}
}

// fastRetransmit は再送キュー先頭のセグメントを RTO を待たずに再送する。
// Karn のため retransmitted を立て、RTT サンプルの対象外にする。
func (c *Conn) fastRetransmit() {
	if len(c.tcb.retxQueue) == 0 {
		return
	}
	front := &c.tcb.retxQueue[0]
	c.writeSeg(front.flags, front.seq, c.tcb.rcv.nxt, front.payload)
	front.retransmitted = true
}

// synOptionBytes は SYN / SYN-ACK に載せる自分のオプションを生バイトで返す。
// 自分の受信 MSS・希望 Window Scale・Timestamps・SACK-Permitted を常に提示する。
// TS option のみ ACK 番号に応じて TSecr を載せる (SYN 単独は TSecr=0)。
func (c *Conn) synOptionBytes(ack uint32) []byte {
	o := TCPOptions{
		HasMSS: true, MSS: defaultMSS,
		HasWScale: true, WindowScale: myWindowScale,
		HasTimestamp: true, TSVal: c.tcb.tsNow(),
		SACKPermitted: true,
	}
	if ack != 0 {
		o.TSecr = c.tcb.tsRecent
	}
	return o.Marshal()
}

// negotiateOptions は握手で受け取った相手の SYN/SYN-ACK オプションを TCB へ反映する
// (RFC 7323 / RFC 2018 / RFC 9293)。自分は SYN で全 option を提示しているので、
// 各機能は相手も送った場合のみ有効 (両側折衝)。Window Scale は >14 を 14 に clamp する
// (ParseTCPOptions が clamp 済み)。相手が送らなければ shift=0 で scaling 無効。
func (c *Conn) negotiateOptions(o TCPOptions) {
	if o.HasWScale {
		c.tcb.sndWindShift = o.WindowScale // Snd.Wind.Shift (相手の shift)
		c.tcb.rcvWindShift = myWindowScale // Rcv.Wind.Shift (自分の shift)
	} // 相手が欠けば両 shift=0 (初期値) のまま = scaling 無効
	if o.HasTimestamp {
		c.tcb.tsOK = true
		c.tcb.tsRecent = o.TSVal
	}
	if o.SACKPermitted {
		c.tcb.sackOK = true
	}
	if o.HasMSS {
		c.tcb.sendMSS = o.MSS
	} else {
		c.tcb.sendMSS = defaultSendMSS // 未受信は既定 536 (RFC 9293)
	}
}

// initSendWindow は握手で受け取った SYN/SYN,ACK の広告窓で SND.WND を初期化する
// (RFC 9293 §3.10.7.3/4)。以後の更新は updateSendWindow が WL1/WL2 で順序判定する。
func (c *Conn) initSendWindow(h TCPHeader) {
	// SYN/SYN-ACK の window はスケールしない生値で初期化する (RFC 7323 §2.3)。
	w := uint32(h.Window)
	c.tcb.snd.wnd = w
	c.tcb.snd.wl1 = h.SeqNum
	c.tcb.snd.wl2 = h.AckNum
	if w > c.tcb.maxSndWnd {
		c.tcb.maxSndWnd = w
	}
}

// updateSendWindow は相手の広告窓で SND.WND を更新する (RFC 9293 §3.10.7.4)。
// 古い窓更新を弾くため SND.WL1/WL2 で順序を見る。maxSndWnd も最大値を追う。
func (c *Conn) updateSendWindow(h TCPHeader) {
	// 通常セグメントの window は Snd.Wind.Shift で左シフトして復元する
	// (RFC 7323 §2.3, SND.WND = SEG.WND << Snd.Wind.Shift)。未折衝なら shift=0。
	w := uint32(h.Window) << c.tcb.sndWindShift
	if SeqLT(c.tcb.snd.wl1, h.SeqNum) ||
		(c.tcb.snd.wl1 == h.SeqNum && SeqLEQ(c.tcb.snd.wl2, h.AckNum)) {
		c.tcb.snd.wnd = w
		c.tcb.snd.wl1 = h.SeqNum
		c.tcb.snd.wl2 = h.AckNum
	}
	if w > c.tcb.maxSndWnd {
		c.tcb.maxSndWnd = w
	}
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
		c.tcb.reachedEstablished = true
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
	// FIN 1 seq 分だけ RCV.NXT を進める。Recv の EOF 判定用に FIN seq を記録する。
	c.tcb.peerFin = true
	c.tcb.peerFinSeq = c.tcb.rcv.nxt
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
	c.tcb.timeWaitDeadline = c.tcb.clock().Add(c.tcb.timeWaitDuration)
}

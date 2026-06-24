package tcp

import "time"

// フロー制御 (RFC 9293 §3.7 + RFC 1122 §4.2.3)。受信窓の動的更新と SWS 回避、
// 送信側 SWS/Nagle の送出判定、persist/override/delayed-ACK のタイマ駆動を扱う。
//
// 純粋ロジック (advertiseWindow / canSend) はここに置き、TCB の状態を読み書きする
// Conn メソッド側 (persist/override/delAck の arm・満了処理) と分離してテストしやすくする。

// advertiseWindow は受信側 SWS 回避を適用した広告窓を返す (RFC 1122 §4.2.3.3)。
// buffTotal は受信バッファ総容量 (RCV.BUFF)、used は未読データ量 (RCV.USER)、
// curWnd は現在広告中の窓、effMSS は有効送信 MSS。
//
// 開ける余地 = (buffTotal-used) - curWnd が閾値 min(buffTotal/2, effMSS) 以上の
// ときだけ buffTotal-used まで開く。閾値未満の小さな開きは広告せず現窓を維持する
// (右窓端を縮めないため curWnd は常に維持できる量)。
func advertiseWindow(buffTotal, used, curWnd, effMSS uint32) uint32 {
	var avail uint32
	if buffTotal > used {
		avail = buffTotal - used
	}
	if avail <= curWnd {
		return curWnd // これ以上開けない (右窓端を縮めない)
	}
	opening := avail - curWnd
	threshold := buffTotal / 2
	if effMSS < threshold {
		threshold = effMSS
	}
	if opening < threshold {
		return curWnd // 小窓は広告しない (SWS 回避)
	}
	return avail
}

// canSend は送信側 SWS 回避 + Nagle の送出判定 (RFC 9293 §3.7.4)。
// d は送れる未送信データ量、usable U = SND.UNA+SND.WND-SND.NXT、mss は有効 MSS、
// maxSndWnd は観測した過去最大の SND.WND、idle は未確認データが無いか (SND.NXT=SND.UNA)。
//
// 送る条件:
//
//	(1) min(D,U) >= MSS         フルセグメントを組める
//	(2) idle なら sub-MSS も即送る (Nagle は未確認データ中のみ抑制)
//	(3) min(D,U) >= Fs*Max(SND.WND) (Fs=1/2) 半窓以上溜まった
//	nagleDisabled (TCP_NODELAY) なら未確認中でも即送る。
//
// いずれも満たさなければ false (溜める。呼び出し側が override timer を arm する)。
func canSend(d, usable, mss, maxSndWnd uint32, idle, nagleDisabled bool) bool {
	send := d
	if usable < send {
		send = usable
	}
	if send == 0 {
		return false
	}
	if send >= mss { // (1) フル
		return true
	}
	if idle || nagleDisabled { // (2) idle / TCP_NODELAY は sub-MSS 即送
		return true
	}
	if send >= maxSndWnd/2 { // (3) 半窓以上
		return true
	}
	return false
}

// effSndMSS は送信に使う有効 MSS (相手広告 MSS と自分の defaultMSS の min)。
func (t *TCB) effSndMSS() uint32 {
	m := uint32(defaultMSS)
	if t.sendMSS != 0 && uint32(t.sendMSS) < m {
		m = uint32(t.sendMSS)
	}
	return m
}

// rcvBuffCap は受信バッファ総容量 (RCV.BUFF)。未設定なら defaultRcvWindow。
func (t *TCB) rcvBuffCap() uint32 {
	if t.rcvBuffTotal == 0 {
		return defaultRcvWindow
	}
	return t.rcvBuffTotal
}

// initialRcvWindow は Open 時の内部受信窓 (実バイト)。rcvBuffTotal が設定されていれば
// それを、無ければ defaultRcvWindow。出力時に rcvWindShift で 16bit へ収める。
func (t *TCB) initialRcvWindow() uint32 {
	return t.rcvBuffCap()
}

// --- 送信側タイマ (persist / override) の arm・満了 ---

// armPersist は zero-window persist タイマを起動する (RFC 9293 §3.8.6.1)。
// 既に arm 済みなら deadline を維持する (満了ごとに probePersist が再 arm する)。
func (c *Conn) armPersist() {
	c.disarmOverride() // 窓 0 では override でなく persist が活性を担う
	if c.tcb.persistArmed {
		return
	}
	c.tcb.persistArmed = true
	c.tcb.persistDeadline = c.tcb.clock().Add(c.persistInterval())
}

// disarmPersist は persist タイマを止め backoff 段階をリセットする (窓>0 受信時)。
func (c *Conn) disarmPersist() {
	c.tcb.persistArmed = false
	c.tcb.persistBackoff = 0
}

// persistInterval は現在の backoff 段階の persist 間隔 = persistInitial<<backoff
// を persistMax で飽和させた値 (指数バックオフ)。
func (c *Conn) persistInterval() time.Duration {
	d := persistInitial << c.tcb.persistBackoff
	if d > persistMax || d <= 0 {
		d = persistMax
	}
	return d
}

// probePersist は persist 満了処理。1 octet probe を送り backoff を進め再 arm する
// (窓 0 継続中は停止しない)。送るデータが無い / 窓が開いたら何もしない。
func (c *Conn) probePersist() {
	off := int(c.tcb.snd.nxt - c.tcb.snd.una)
	if off >= len(c.tcb.sndBuf) || c.tcb.snd.wnd != 0 {
		return // 送るものが無い or 窓が開いた (flushSend 側で disarm 済み)
	}
	// 未送信先頭の 1 octet を SND.NXT で送る (窓 0 でも受理される probe)。
	payload := []byte{c.tcb.sndBuf[off]}
	c.sendData(Flags(FlagPSH|FlagACK), c.tcb.snd.nxt, c.tcb.rcv.nxt, payload)
	c.tcb.snd.nxt++
	c.tcb.persistBackoff++ // 指数バックオフ
	c.tcb.persistDeadline = c.tcb.clock().Add(c.persistInterval())
}

// armOverride は送信側 SWS/Nagle の override タイマを起動する (RFC 1122 §4.2.3.4)。
// 既に arm 済みなら deadline を維持する。
func (c *Conn) armOverride() {
	if c.tcb.overrideArmed {
		return
	}
	c.tcb.overrideArmed = true
	c.tcb.overrideDeadline = c.tcb.clock().Add(overrideTimeout)
}

// disarmOverride は override タイマを止める。
func (c *Conn) disarmOverride() { c.tcb.overrideArmed = false }

// fireOverride は override 満了処理。詰まった sub-MSS を 1 セグメント強制送出する。
// 相手窓 (usable) があるぶんだけ送る (窓 0 ならここには来ない=persist 管轄)。
func (c *Conn) fireOverride() {
	off := int(c.tcb.snd.nxt - c.tcb.snd.una)
	if off >= len(c.tcb.sndBuf) {
		c.disarmOverride()
		return
	}
	usable := c.sendUsable()
	if usable == 0 {
		c.disarmOverride()
		return
	}
	c.emitSegment(off, uint32(len(c.tcb.sndBuf)-off), usable)
}

// --- delayed ACK (RFC 1122 §4.2.3.2) ---

// onDelayableSegment は遅延 ACK の対象になる in-order フルセグメント受信を処理する。
// 未 ACK フルセグメントは高々 2: 2 個目で即 ACK しカウンタを戻す。1 個目は ACK を
// 遅延し delAckTimeout (<0.5s) のタイマを arm する (満了 or 2 個目で必ず ACK)。
func (c *Conn) onDelayableSegment() {
	c.tcb.delAckCount++
	if c.tcb.delAckCount >= 2 {
		c.sendDelayedAck()
		return
	}
	if !c.tcb.delAckArmed {
		c.tcb.delAckArmed = true
		c.tcb.delAckDeadline = c.tcb.clock().Add(delAckTimeout)
	}
}

// sendDelayedAck は溜めた ACK を送る (sendAck が delayed ACK 状態をクリアする)。
func (c *Conn) sendDelayedAck() { c.sendAck() }

// fireDelayedAck は delayed ACK タイマ満了処理。溜めた ACK を送る。
func (c *Conn) fireDelayedAck() { c.sendDelayedAck() }

// recomputeRcvWindow は受信側 SWS 回避を適用して RCV.WND を更新する。
// 未読データ (rcvBuf) のぶん窓を消費し、アプリが読んで空けば SWS 閾値を超えた
// ときだけ窓を開く。右窓端 RCV.NXT+RCV.WND は単調非減少に保たれる:
//   - データ受信で RCV.NXT が n 前進する直前に RCV.WND を n 減らすため右窓端は不変
//   - Recv で読むと rcvBuf が減り avail が増え、ここで RCV.WND が増えて右窓端が前進
func (c *Conn) recomputeRcvWindow() {
	used := uint32(len(c.tcb.rcvBuf))
	w := advertiseWindow(c.tcb.rcvBuffCap(), used, c.tcb.rcv.wnd, c.tcb.effSndMSS())
	c.tcb.rcv.wnd = w
}

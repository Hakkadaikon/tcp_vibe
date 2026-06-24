package transport

import "time"

// keepalive (RFC 1122 §4.2.3.6 / RFC 9293 §3.8.4)。既定 OFF。idle が一定時間
// 続いたら窓外 probe を送り、相手の生存を確かめる。単一無応答では切らない。

// SetKeepAlive は接続ごとに keepalive を ON/OFF する (既定 OFF が MUST)。
// idle に keepalive 間隔を指定する。0 以下なら既定 (2 時間) を使う。
// idle 起点を呼び出し時点にリセットする。
func (c *Conn) SetKeepAlive(enabled bool, idle time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.keepaliveEnabled = enabled
	if idle <= 0 {
		idle = defaultKeepaliveIdle
	}
	c.tcb.keepaliveIdle = idle
	c.tcb.lastRecvTime = c.tcb.clock()
	c.tcb.keepaliveProbes = 0
}

// checkKeepAlive は idle 超過を判定し、超えていれば probe を送る (Tick から呼ぶ)。
// 未確認データがある間は idle でない (送信中) ので何もしない。probe 無応答が
// 上限を超えたら接続を閉じる。
func (c *Conn) checkKeepAlive() {
	if !c.tcb.keepaliveEnabled || !c.tcb.state.synchronized() {
		return
	}
	// 未確認データがあるなら送信中で idle でない (再送タイマが面倒を見る)。
	if c.tcb.snd.una != c.tcb.snd.nxt {
		return
	}
	if c.tcb.clock().Sub(c.tcb.lastRecvTime) < c.tcb.keepaliveIdle {
		return
	}
	if c.tcb.keepaliveProbes >= keepaliveMaxProbes {
		// 無応答 probe が上限超過: 相手は到達不能とみなし閉じる (実装定義)。
		c.tcb.state = Closed
		c.tcb.retxQueue = nil
		c.tcb.curRTO = 0
		return
	}
	c.sendKeepAliveProbe()
}

// sendKeepAliveProbe は keepalive probe を送る。SEG.SEQ = SND.NXT-1 (窓外)、
// データ無しの ACK。窓外なので相手は重複と判断し ACK だけ返す (RFC 1122)。
// 再送キューには積まない (probe 自体は信頼配送の対象でない)。
func (c *Conn) sendKeepAliveProbe() {
	c.writeSeg(Flags(FlagACK), c.tcb.snd.nxt-1, c.tcb.rcv.nxt, nil)
	c.tcb.keepaliveProbes++
	// 次 probe まで idle を空けるため idle 起点を進める。
	c.tcb.lastRecvTime = c.tcb.clock()
}

package tcp

// 輻輳制御 (RFC 5681)。cwnd/ssthresh/状態を管理し、ACK・重複 ACK・RTO 満了で
// ウィンドウを更新する。単位はバイト。SMSS は接続の最大セグメントサイズ。
//
// 状態は SlowStart / CongestionAvoidance / FastRecovery の 3 つ。
//   cwnd < ssthresh  → SlowStart        (RFC 5681 §3.1, 指数増加)
//   cwnd >= ssthresh → CongestionAvoidance (線形増加)
//   3 dup ACK 検出   → FastRecovery     (§3.2)

type ccState int

const (
	ccSlowStart ccState = iota
	ccCongestionAvoidance
	ccFastRecovery
)

func (s ccState) String() string {
	switch s {
	case ccSlowStart:
		return "SlowStart"
	case ccCongestionAvoidance:
		return "CongestionAvoidance"
	case ccFastRecovery:
		return "FastRecovery"
	default:
		return "UNKNOWN"
	}
}

// congestion は 1 接続の輻輳制御状態。
type congestion struct {
	smss      uint32
	cwnd      uint32
	ssthresh  uint32
	state     ccState
	dupAckCnt int // 連続した重複 ACK 数 (3 で fast retransmit)

	// bytesAckedThisRtt は CongestionAvoidance の byte counting 累積 (RFC 5681 §3.1 式2)。
	// cwnd に達したら cwnd += SMSS してリセットする。
	bytesAckedThisRtt uint32

	// retransmittedThisLoss は今の損失エピソードで既に再送 (ssthresh 半減) したか。
	// 初回 RTO でだけ ssthresh を半減し、同一エピソードの 2 回目以降は保持する
	// (RFC 6298 §5)。新規 ACK でクリアする。
	retransmittedThisLoss bool
}

// initialWindow は初期ウィンドウ IW を SMSS から決める (RFC 5681 §3.1)。
func initialWindow(smss uint32) uint32 {
	switch {
	case smss <= 1095:
		return 4 * smss
	case smss <= 2190:
		return 3 * smss
	default:
		return 2 * smss
	}
}

// newCongestion は初期状態を作る。cwnd=IW, ssthresh は十分高く (制限なし相当)。
func newCongestion(smss uint32) *congestion {
	return &congestion{
		smss:     smss,
		cwnd:     initialWindow(smss),
		ssthresh: uint32(maxWindow), // 初期は高く (RFC 5681 §3.1)
		state:    ccSlowStart,
	}
}

// halveSsthresh は ssthresh = max(FlightSize/2, 2*SMSS) を返す (RFC 5681 §3.1 式4)。
func (c *congestion) halveSsthresh(flightSize uint32) uint32 {
	return maxU32(flightSize/2, 2*c.smss)
}

// onNewAck は新規 (確認を進める) ACK での更新。ackedBytes は新たに確認された
// バイト数、flightSize は ACK 前の送信中バイト数。
func (c *congestion) onNewAck(ackedBytes, flightSize uint32) {
	// 損失エピソードは新規 ACK で終わる。再送フラグと dup カウンタをクリアする。
	c.retransmittedThisLoss = false
	c.dupAckCnt = 0

	if c.state == ccFastRecovery {
		// 回復完了: cwnd を ssthresh に戻す (deflate, RFC 5681 §3.2 step 6)。
		c.cwnd = c.ssthresh
		c.exitRecovery()
		return
	}

	if c.cwnd < c.ssthresh {
		// Slow Start: cwnd += min(N, SMSS) (RFC 5681 §3.1)。
		c.cwnd += minU32(ackedBytes, c.smss)
		if c.cwnd >= c.ssthresh {
			c.state = ccCongestionAvoidance
		}
		return
	}

	// Congestion Avoidance: byte counting。累積が cwnd に達したら +SMSS (式2)。
	c.state = ccCongestionAvoidance
	c.bytesAckedThisRtt += ackedBytes
	if c.bytesAckedThisRtt >= c.cwnd {
		c.bytesAckedThisRtt -= c.cwnd
		c.cwnd += c.smss
	}
}

// onDupAck は重複 ACK での更新。3 つ目で Fast Recovery に入り、以降の dup で
// cwnd を膨らませる (RFC 5681 §3.2)。1, 2 番目は cwnd 不変 (Limited Transmit)。
func (c *congestion) onDupAck(flightSize uint32) {
	if c.state == ccFastRecovery {
		c.cwnd += c.smss // 追加 dup ごと cwnd += SMSS (step 4)
		return
	}
	c.dupAckCnt++
	if c.dupAckCnt < 3 {
		return // 1, 2 番目は cwnd 不変
	}
	// 3 つ目: Fast Recovery 入り (step 2, 3)。ssthresh を先に半減してから cwnd 設定。
	c.ssthresh = c.halveSsthresh(flightSize)
	c.retransmittedThisLoss = true
	c.cwnd = c.ssthresh + 3*c.smss
	c.state = ccFastRecovery
}

// onRtoTimeout は RTO 満了での更新 (RFC 5681 §3.1 + RFC 6298 §5)。
// cwnd を 1*SMSS (LW) に落とし SlowStart へ。ssthresh は今の損失エピソードで
// まだ半減していないときだけ半減し、再送済みなら保持する。
func (c *congestion) onRtoTimeout(flightSize uint32) {
	if !c.retransmittedThisLoss {
		c.ssthresh = c.halveSsthresh(flightSize)
		c.retransmittedThisLoss = true
	}
	c.cwnd = c.smss // LW = 1*SMSS
	c.state = ccSlowStart
	c.dupAckCnt = 0
	c.bytesAckedThisRtt = 0
}

// exitRecovery は Fast Recovery を抜けたあとの状態を cwnd と ssthresh から決める。
func (c *congestion) exitRecovery() {
	c.bytesAckedThisRtt = 0
	if c.cwnd < c.ssthresh {
		c.state = ccSlowStart
	} else {
		c.state = ccCongestionAvoidance
	}
}

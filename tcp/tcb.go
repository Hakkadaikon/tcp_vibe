package tcp

import "time"

// State は RFC 9293 §3.3.2 の接続状態 (11 種)。CLOSED は TCB を持たない架空状態。
type State int

const (
	Closed State = iota
	Listen
	SynSent
	SynReceived
	Established
	FinWait1
	FinWait2
	CloseWait
	Closing
	LastAck
	TimeWait
)

func (s State) String() string {
	switch s {
	case Closed:
		return "CLOSED"
	case Listen:
		return "LISTEN"
	case SynSent:
		return "SYN-SENT"
	case SynReceived:
		return "SYN-RECEIVED"
	case Established:
		return "ESTABLISHED"
	case FinWait1:
		return "FIN-WAIT-1"
	case FinWait2:
		return "FIN-WAIT-2"
	case CloseWait:
		return "CLOSE-WAIT"
	case Closing:
		return "CLOSING"
	case LastAck:
		return "LAST-ACK"
	case TimeWait:
		return "TIME-WAIT"
	default:
		return "UNKNOWN"
	}
}

// synchronized は同期状態か (ESTABLISHED 以降)。RFC 5961 三チェックの適用範囲。
func (s State) synchronized() bool {
	switch s {
	case Established, FinWait1, FinWait2, CloseWait, Closing, LastAck, TimeWait:
		return true
	default:
		return false
	}
}

// Origin は SYN-RECEIVED の由来。RST 受信時の遷移先を決める (RFC 9293 §3.10.7.3, MUST-11)。
// 入口で確定し値そのものを保持する: passive は LISTEN から、active は同時オープンから。
type Origin int

const (
	OriginPassive Origin = iota // LISTEN 経由。RST で LISTEN へ戻る
	OriginActive                // SYN-SENT 経由 (同時オープン)。RST で CLOSED へ
)

// sndVars は送信側状態変数 (RFC 9293 §3.3.1)。
type sndVars struct {
	una uint32 // 未確認の最古 seq
	nxt uint32 // 次に送る seq
	wnd uint16 // 送信窓
	wl1 uint32 // 最後に窓更新した seg の seq
	wl2 uint32 // 最後に窓更新した seg の ack
	iss uint32 // 自分の初期送信 seq
}

// rcvVars は受信側状態変数 (RFC 9293 §3.3.1)。
type rcvVars struct {
	nxt uint32 // 次に受け取りたい seq
	wnd uint16 // 受信窓
	irs uint32 // 相手の初期受信 seq
}

// TCB は Transmission Control Block。1 接続分の状態を保持する。
// 観測・更新は Conn の mutex 越しに行う (並行アクセスで -race を通すため)。
type TCB struct {
	state  State
	origin Origin
	snd    sndVars
	rcv    rcvVars

	// maxSndWnd は peer から受信した過去最大の窓。RFC 5961 ACK 受理範囲の下限に使う。
	// window scale 無しなので初期値は 65535 上限まで取りうる。
	maxSndWnd uint16

	clock Clock

	// timeWaitDeadline は 2MSL タイマの満了時刻。TIME-WAIT 滞在中のみ有効。
	timeWaitDeadline time.Time

	// retxQueue は未 ACK セグメントの再送キュー。送信順 (seq 昇順) を保つ。
	// 先頭が最古の未確認セグメントで、RTO はこの先頭基準で駆動する (RFC 9293 §3.8.1)。
	retxQueue []retxSeg
	// curRTO は次の発火に使う現在の RTO。再送ごとに倍化する (指数バックオフ)。
	// 0 はキューが空 (タイマ停止) を表す。
	curRTO time.Duration

	// challenge ACK throttling のトークン状態 (RFC 5961 §7, timestamp+counter)。
	// challengeWindowStart は現在の計数窓の開始時刻、challengeCount は窓内送出数。
	challengeWindowStart time.Time
	challengeCount       int
}

// retxSeg は再送キューの 1 エントリ。再送に必要な最小情報だけ持つ。
// ponytail: payload は持たない。send はヘッダのみで seq を占めるのは SYN/FIN だけ。データ送信時に payload を足す。
type retxSeg struct {
	seq     uint32
	flags   Flags
	sentAt  time.Time // 先頭エントリの sentAt が RTO 起点
	retries int       // 再送回数 (R2 上限判定用)
}

// seqLen はこのエントリが占める seq 数 (SYN/FIN は各 1)。ACK 除去判定に使う。
func (s retxSeg) seqLen() uint32 {
	var n uint32
	if s.flags.Has(FlagSYN) {
		n++
	}
	if s.flags.Has(FlagFIN) {
		n++
	}
	return n
}

// msl は Maximum Segment Lifetime (RFC 9293 §3.4.2 の推奨値 2 分)。
const msl = 2 * time.Minute

// timeWaitDuration は TIME-WAIT の linger 時間 = 2*MSL (RFC 9293 R-059, MUST-13)。
const timeWaitDuration = 2 * msl

// 再送タイマの定数 seam (テストで境界を突けるよう変数でなく定数だが調整可)。
// ponytail: SRTT/RTTVAR の RFC 6298 動的計算は未実装。固定 initialRTO + 指数バックオフで足りる。RTT 測定が要るなら Karn 込みでここに足す。
const (
	initialRTO     = 1 * time.Second  // RTO 初期値 (RFC 6298 推奨は 1 秒)
	maxRTO         = 60 * time.Second // バックオフ上限 (RFC 6298 §2.5 の下限 60 秒)
	maxRetransmits = 5                // R2 相当。これを超える再送で接続を閉じる (R-093)
)

// challenge ACK throttling の定数 seam (RFC 5961 §7 の SHOULD、調整可)。
const (
	challengeAckLimit  = 10              // 1 窓あたりの送出上限
	challengeAckWindow = 5 * time.Second // 計数窓
)

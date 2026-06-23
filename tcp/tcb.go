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

	// reachedEstablished は一度でも ESTABLISHED に入ったかの記録。
	// ESTABLISHED→CLOSE-WAIT が速く現在値ポーリングでは取りこぼす握手成立を、
	// 到達の事実として残すための観測専用フラグ (遷移ロジックには影響しない)。
	reachedEstablished bool

	// maxSndWnd は peer から受信した過去最大の窓。RFC 5961 ACK 受理範囲の下限に使う。
	// window scale 無しなので初期値は 65535 上限まで取りうる。
	maxSndWnd uint16

	clock Clock

	// timeWaitDeadline は 2MSL タイマの満了時刻。TIME-WAIT 滞在中のみ有効。
	timeWaitDeadline time.Time
	// timeWaitDuration は TIME-WAIT の linger 時間 (= 2*MSL)。接続ごとに設定可能
	// にし、デモで短い MSL を注入できるようにする。既定は 2*msl で RFC 通り。
	timeWaitDuration time.Duration

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

	// sndBuf は未送信のユーザデータ。Send で追記し flushSend が窓と MSS の範囲で
	// セグメント化して送る。送出済みは ACK で SND.UNA が進んだぶん解放する。
	sndBuf []byte
	// rcvBuf は再組立て済みで Recv 待ちのデータ。RCV.NXT まで連続したぶんがここに入る。
	rcvBuf []byte
	// oooSegs は窓内だが順番待ちの保持セグメント (seq 昇順)。RCV.NXT が追いつくと
	// rcvBuf へ取り込んで RCV.NXT を前進させる (out-of-order 再組立て)。
	oooSegs []segFragment

	// peerFinSeq は相手 FIN が占める seq。peerFin が true のとき有効。
	// この seq まで読み切ったら Recv は EOF を返す。
	peerFin    bool
	peerFinSeq uint32
}

// segFragment は受信した連続バイト片 (seq とデータ)。out-of-order 再組立て用。
type segFragment struct {
	seq  uint32
	data []byte
}

// retxSeg は再送キューの 1 エントリ。再送に必要な最小情報を持つ。
type retxSeg struct {
	seq     uint32
	flags   Flags
	payload []byte    // データセグメントの本体 (SYN/FIN 単独なら nil)
	sentAt  time.Time // 先頭エントリの sentAt が RTO 起点
	retries int       // 再送回数 (R2 上限判定用)
}

// seqLen はこのエントリが占める seq 数 (payload 長 + SYN/FIN 各 1)。ACK 除去判定に使う。
func (s retxSeg) seqLen() uint32 {
	n := uint32(len(s.payload))
	if s.flags.Has(FlagSYN) {
		n++
	}
	if s.flags.Has(FlagFIN) {
		n++
	}
	return n
}

// defaultMSS は 1 セグメントに載せるデータの上限。TUN の MTU 1500 から
// IPv4(20)+TCP(20) ヘッダを引いた 1460 より控えめにし、断片化しない値にする。
// ponytail: MSS option による相手との折衝は未実装。固定値で足りる。折衝が要るならここを seam に。
const defaultMSS = 1360

// msl は Maximum Segment Lifetime (RFC 9293 §3.4.2 の推奨値 2 分)。
const msl = 2 * time.Minute

// timeWaitDuration は TIME-WAIT の linger 時間 = 2*MSL (RFC 9293 MUST-13)。
const timeWaitDuration = 2 * msl

// 再送タイマの定数 seam (テストで境界を突けるよう変数でなく定数だが調整可)。
// ponytail: SRTT/RTTVAR の RFC 6298 動的計算は未実装。固定 initialRTO + 指数バックオフで足りる。RTT 測定が要るなら Karn 込みでここに足す。
const (
	initialRTO     = 1 * time.Second  // RTO 初期値 (RFC 6298 推奨は 1 秒)
	maxRTO         = 60 * time.Second // バックオフ上限 (RFC 6298 §2.5 の下限 60 秒)
	maxRetransmits = 5                // R2 相当。これを超える再送で接続を閉じる
)

// challenge ACK throttling の定数 seam (RFC 5961 §7 の SHOULD、調整可)。
const (
	challengeAckLimit  = 10              // 1 窓あたりの送出上限
	challengeAckWindow = 5 * time.Second // 計数窓
)

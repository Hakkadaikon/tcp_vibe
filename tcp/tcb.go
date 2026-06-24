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
	wnd uint32 // 送信窓。Window Scale 後は 65535 を超え最大 2^30 まで取りうる
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

	// maxSndWnd は peer から受信した過去最大の窓 (スケール適用後)。RFC 5961 ACK 受理
	// 範囲の下限に使う。Window Scale 有効時は 65535 を超え最大 2^30 まで取りうる。
	maxSndWnd uint32

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
	// 0 はキューが空 (タイマ停止) を表す。RTT サンプル取得後は rtt 由来の値になる。
	curRTO time.Duration

	// rtt は RTT 推定器 (RFC 6298)。rttValid が true のとき有効。RTT を一度も
	// 測っていない間は curRTO=initialRTO 固定 (RFC 6298 §2.1)。
	rtt      rttEstimator
	rttValid bool

	// cong は輻輳制御 (RFC 5681)。送信窓を min(cwnd, rwnd) に絞り、ACK/重複 ACK/
	// RTO 満了で cwnd・ssthresh を更新する。
	cong *congestion

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

	// --- オプション折衝結果 (握手で確定。RFC 7323 / RFC 2018) ---

	// sndWindShift は相手から受けた Window Scale (Snd.Wind.Shift)。入力 SEG.WND を
	// 左シフトして SND.WND にする。rcvWindShift は自分が広告する shift (Rcv.Wind.Shift)
	// で、出力 SEG.WND を右シフトする。両側が SYN で WScale を送ったときのみ非 0。
	sndWindShift uint8
	rcvWindShift uint8

	// tsOK は両側が timestamps を折衝したか (Snd.TS.OK)。true なら以降の全セグメントに
	// TS option を載せる。tsRecent は echo する相手の最新 TSval、lastAckSent は
	// 直近に送った ACK 番号 (TS.Recent 更新の SEQ ゲート Last.ACK.sent)。
	tsOK        bool
	tsRecent    uint32
	lastAckSent uint32

	// sackOK は両側が SACK-Permitted を折衝したか (Sack.OK)。
	sackOK bool

	// sendMSS は相手が広告した受信 MSS (送信時の 1 セグメント上限)。未受信なら既定 536。
	sendMSS uint16

	// --- フロー制御 (RFC 9293 §3.7 + RFC 1122 §4.2.3) ---

	// rcvBuffTotal は受信バッファの総容量 (RCV.BUFF)。広告窓はこの上限から未読の
	// rcvBuf 使用量を引いて決める。0 のときは defaultRcvWindow を総容量とみなす。
	rcvBuffTotal uint32

	// nagleDisabled は Nagle アルゴリズムの無効化フラグ (TCP_NODELAY 相当)。
	// true なら未確認データ中でも sub-MSS を溜めず即送る。
	nagleDisabled bool

	// persistDeadline は zero-window persist タイマの満了時刻。persistArmed が
	// true のときだけ有効。満了ごとに 1 octet probe を送り backoff 段階を進める。
	persistDeadline time.Time
	persistArmed    bool
	persistBackoff  int // 指数バックオフ段階 (0 起点)。窓>0 受信でリセット。

	// overrideDeadline は送信側 SWS/Nagle の override タイマ満了時刻。overrideArmed が
	// true のときだけ有効。「未確認中・相手窓ありだがフル未満で詰まった」全ケースで
	// arm し、満了で sub-MSS を強制送出する (Nagle デッドロックの唯一の活性保証)。
	overrideDeadline time.Time
	overrideArmed    bool

	// delAckDeadline は delayed ACK タイマの満了時刻。delAckArmed が true のときだけ
	// 有効。delAckCount は ACK 未送のフルセグメント数 (2 個目で即 ACK)。
	delAckDeadline time.Time
	delAckArmed    bool
	delAckCount    int
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

	// retransmitted は一度でも再送されたか。Karn のアルゴリズム (RFC 6298 §3) で
	// 再送セグメントの ACK から RTT を測らないために使う。
	retransmitted bool
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
// 自分が SYN で広告する受信 MSS でもある。実際の送信上限は相手 MSS との min を使う。
const defaultMSS = 1360

// defaultSendMSS は相手 MSS 未受信時の送信 MSS (RFC 9293 の既定値 536, IPv4)。
const defaultSendMSS uint16 = 536

// myWindowScale は自分が SYN/SYN-ACK で広告する Window Scale shift (Rcv.Wind.Shift)。
// 14 以下。65535 を超える受信窓を扱えるようにする (RFC 7323 §2)。
const myWindowScale uint8 = 7

// msl は Maximum Segment Lifetime (RFC 9293 §3.4.2 の推奨値 2 分)。
const msl = 2 * time.Minute

// timeWaitDuration は TIME-WAIT の linger 時間 = 2*MSL (RFC 9293 MUST-13)。
const timeWaitDuration = 2 * msl

// 再送タイマの定数 seam (テストで境界を突けるよう変数でなく定数だが調整可)。
const (
	initialRTO     = 1 * time.Second  // RTO 初期値 (RFC 6298 §2.1, RTT 測定前)
	maxRTO         = 60 * time.Second // バックオフ上限 (RFC 6298 §2.5 の下限 60 秒)
	maxRetransmits = 5                // R2 相当。これを超える再送で接続を閉じる
	clockGranMS    = 1                // RTT 推定のクロック粒度 G (ミリ秒, RFC 6298 §2)
)

// challenge ACK throttling の定数 seam (RFC 5961 §7 の SHOULD、調整可)。
const (
	challengeAckLimit  = 10              // 1 窓あたりの送出上限
	challengeAckWindow = 5 * time.Second // 計数窓
)

// フロー制御タイマの定数 seam (RFC 9293 §3.7 / RFC 1122 §4.2.3、調整可)。
const (
	// persistInitial は zero-window persist タイマの初回値。満了ごとに倍化し
	// persistMax で飽和させる (RFC 9293 §3.8.6.1, RTO と同様の指数バックオフ)。
	persistInitial = 1 * time.Second
	persistMax     = 60 * time.Second
	// overrideTimeout は送信側 SWS/Nagle の override 値。RFC 1122 §4.2.3.4 の
	// 0.1〜1.0 秒の範囲。フル未満で詰まったとき満了で sub-MSS を強制送出する。
	overrideTimeout = 200 * time.Millisecond
	// delAckTimeout は delayed ACK の遅延上限。RFC 1122 §4.2.3.2 の 0.5 秒未満必須。
	delAckTimeout = 200 * time.Millisecond
)

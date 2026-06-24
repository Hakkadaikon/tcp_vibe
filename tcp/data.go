package tcp

import (
	"errors"
	"io"
)

// ErrNotEstablished は ESTABLISHED 前の Send/Recv で返る。
var ErrNotEstablished = errors.New("tcp: connection not established")

// ErrConnClosed は閉じ方向 (FIN 送出後や CLOSED) への Send で返る。
var ErrConnClosed = errors.New("tcp: connection closing or closed")

// SetRcvBuffer は受信バッファ総容量 (RCV.BUFF) を設定し受信窓もそこへ合わせる。
// 握手前 (現窓が既定値) に呼ぶ前提。小さくすると受信側 SWS / 窓開閉が観測しやすい。
func (c *Conn) SetRcvBuffer(total uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.rcvBuffTotal = total
	if c.tcb.rcv.wnd > total {
		c.tcb.rcv.wnd = total
	}
}

// SetNoDelay は Nagle アルゴリズムの有効/無効を切り替える (TCP_NODELAY 相当)。
// true で無効化すると未確認データ中でも sub-MSS を溜めず即送る (RFC 1122 §4.2.3.4)。
func (c *Conn) SetNoDelay(disable bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tcb.nagleDisabled = disable
}

// Send はユーザデータを送信バッファに積み、送信窓の余地ぶんを送り出す。
// 積めたバイト数 (= len(data)) を返す。ESTABLISHED でなければ何もせずエラー。
// FIN 送出後や CLOSED など送信不能な状態では ErrConnClosed。
func (c *Conn) Send(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.tcb.state {
	case Established, CloseWait:
		// CLOSE-WAIT は相手が閉じただけで自分の送信方向は生きている。
	case Closed:
		return 0, ErrConnClosed
	case FinWait1, FinWait2, Closing, LastAck, TimeWait:
		return 0, ErrConnClosed // 自分が FIN 済み: もう送れない。
	default:
		return 0, ErrNotEstablished
	}
	c.tcb.sndBuf = append(c.tcb.sndBuf, data...)
	c.flushSend()
	return len(data), nil
}

// flushSend は送信側 SWS 回避 + Nagle (RFC 9293 §3.7.4) の判定で、未送信バイトを
// PSH|ACK セグメントに切り出して送る。送ったぶん SND.NXT を進める。
// 送信は mutex 保持中に呼ぶこと。
//
// 相手窓 0 で未送信ありなら persist タイマを arm し (zero-window probe の起点)、
// 窓はあるがフル未満で Nagle/SWS により送れないときは override タイマを arm する
// (詰まりの唯一の活性保証)。送れた / 未送信が尽きたらタイマは解除する。
func (c *Conn) flushSend() {
	// 呼び出し開始時に未確認データが無ければ idle。idle 開始の一括送出はこの回で
	// 出せるデータを端数まで送り切る (RFC 9293 §3.7.4 古典 Nagle: 未確認の小データが
	// 1 つ出るまで小セグメントを送ってよい)。Nagle が抑えるのは「未確認データが
	// 既にある状態」から積まれた新規 sub-MSS だけ。
	idle := c.tcb.snd.nxt == c.tcb.snd.una
	for {
		off := int(c.tcb.snd.nxt - c.tcb.snd.una)
		if off >= len(c.tcb.sndBuf) {
			c.disarmOverride() // 未送信なし: 詰まりタイマ不要
			c.disarmPersist()
			return
		}
		d := uint32(len(c.tcb.sndBuf) - off)
		// 相手窓 0 で未送信あり → persist で probe を出し続ける (override は使わない)。
		if c.tcb.snd.wnd == 0 {
			c.armPersist()
			return
		}
		usable := c.sendUsable()
		if !canSend(d, usable, c.tcb.effSndMSS(), c.tcb.maxSndWnd, idle, c.tcb.nagleDisabled) {
			c.armOverride() // フル未満で詰まった: 満了で sub-MSS を強制送出
			return
		}
		c.emitSegment(off, d, usable)
	}
}

// sendUsable は今送れるバイト数 = min(rwnd 残余, cwnd 残余)。
// rwnd 残余 = SND.UNA+SND.WND-SND.NXT、cwnd 残余 = cwnd - 送信中バイト。
func (c *Conn) sendUsable() uint32 {
	usable := c.tcb.snd.una + uint32(c.tcb.snd.wnd) - c.tcb.snd.nxt
	if SeqGT(c.tcb.snd.nxt, c.tcb.snd.una+uint32(c.tcb.snd.wnd)) {
		usable = 0
	}
	inflight := c.tcb.snd.nxt - c.tcb.snd.una
	var cwndRoom uint32
	if c.tcb.cong.cwnd > inflight {
		cwndRoom = c.tcb.cong.cwnd - inflight
	}
	if cwndRoom < usable {
		usable = cwndRoom
	}
	return usable
}

// emitSegment は off から 1 セグメント (min(D, usable, MSS) バイト) を切り出し送出する。
// 送出できたので詰まりタイマ (override) は解除する。
func (c *Conn) emitSegment(off int, d, usable uint32) {
	n := d
	if n > usable {
		n = usable
	}
	if n > defaultMSS {
		n = defaultMSS
	}
	payload := make([]byte, n)
	copy(payload, c.tcb.sndBuf[off:off+int(n)])
	c.sendData(Flags(FlagPSH|FlagACK), c.tcb.snd.nxt, c.tcb.rcv.nxt, payload)
	c.tcb.snd.nxt += n
	c.disarmOverride()
}

// releaseAckedSend は SND.UNA が oldUna から前進したぶんを送信バッファから解放する。
// sndBuf[0] は常に SND.UNA のバイトを指す不変条件を保つ (flushSend が
// SND.NXT-SND.UNA を offset に使うため)。SYN/FIN は seq を占めるが sndBuf には
// 入らないので、解放量はデータぶんだけに丸める (バッファ長で上限)。
func (c *Conn) releaseAckedSend(oldUna uint32) {
	acked := int(c.tcb.snd.una - oldUna)
	if acked <= 0 {
		return
	}
	if acked > len(c.tcb.sndBuf) {
		acked = len(c.tcb.sndBuf)
	}
	c.tcb.sndBuf = c.tcb.sndBuf[acked:]
}

// Recv は再組立て済みデータを buf へコピーし、読んだバイト数を返す。
// データが無ければ 0 (ブロックしない)。相手が FIN 済みで残データも無ければ io.EOF。
func (c *Conn) Recv(buf []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tcb.rcvBuf) == 0 {
		if c.finReached() {
			return 0, io.EOF
		}
		return 0, nil
	}
	n := copy(buf, c.tcb.rcvBuf)
	c.tcb.rcvBuf = c.tcb.rcvBuf[n:]
	// 読んだぶん受信バッファが空いた。SWS 閾値を超えていれば窓を開き、開いたら
	// window update ACK で送信側へ知らせる (RFC 1122 §4.2.3.3)。これが無いと
	// 窓が縮んで止まった送信側は (persist が起きない限り) 再開できない。
	old := c.tcb.rcv.wnd
	c.recomputeRcvWindow()
	if c.tcb.rcv.wnd > old && c.tcb.state.synchronized() {
		c.sendAck()
	}
	return n, nil
}

// finReached は相手 FIN を受信し、その手前まで全部読み切ったか。Recv の EOF 判定。
func (c *Conn) finReached() bool {
	return c.tcb.peerFin && SeqLEQ(c.tcb.peerFinSeq, c.tcb.rcv.nxt)
}

// acceptText は text 段の受信データを再組立てする (RFC 9293 §3.10.7.4 step 5)。
// 順番通りなら rcvBuf へ取り込み RCV.NXT を前進、欠け埋めで oooSegs も取り込む。
// 先行セグメントは oooSegs に保持する。重複・部分重複・窓外は drain/trim で正す。
// データを取り込んだ (または受理した) ら ACK を返す。
func (c *Conn) acceptText(h TCPHeader, payload []byte) {
	if len(payload) == 0 {
		return
	}
	seq := h.SeqNum
	// 窓外 (左: 既受信済み, 右: 窓を超える) を切り落とす。
	data := c.trimToWindow(seq, payload)
	if len(data.data) == 0 {
		c.sendAck() // 全部既受信/窓外でも ACK で RCV.NXT を広告 (重複応答)。
		return
	}
	before := c.tcb.rcv.nxt
	inOrder := data.seq == c.tcb.rcv.nxt
	if inOrder {
		c.tcb.rcvBuf = append(c.tcb.rcvBuf, data.data...)
		c.tcb.rcv.nxt += uint32(len(data.data))
		c.drainOoo()
	} else {
		c.insertOoo(data)
	}
	// RCV.NXT 前進ぶん受信窓を消費し右窓端 RCV.NXT+RCV.WND を固定する (窓を縮めない)。
	c.consumeRcvWindow(c.tcb.rcv.nxt - before)
	// out-of-order / gap 残りは即 ACK (損失検出を急がせる, RFC 1122 §4.2.3.2)。
	// in-order で gap が無いフルセグメントだけ delayed ACK の対象にする。
	full := uint32(len(data.data)) >= c.tcb.effSndMSS()
	if inOrder && len(c.tcb.oooSegs) == 0 && full {
		c.onDelayableSegment()
	} else {
		c.sendAck()
	}
}

// consumeRcvWindow は RCV.NXT が n 前進したぶん RCV.WND を減らし右窓端を固定する。
// 窓未満まで減ったら 0 で飽和させる (右窓端は RCV.NXT を下回らない)。
func (c *Conn) consumeRcvWindow(n uint32) {
	if c.tcb.rcv.wnd > n {
		c.tcb.rcv.wnd -= n
	} else {
		c.tcb.rcv.wnd = 0
	}
}

// trimToWindow は seq から始まる payload のうち、受信窓内かつ RCV.NXT 以降の部分を返す。
// 左端 (RCV.NXT より前の既受信ぶん) と右端 (窓を超えるぶん) を捨てる。
func (c *Conn) trimToWindow(seq uint32, payload []byte) segFragment {
	end := seq + uint32(len(payload))
	// 左トリム: seq < RCV.NXT なら RCV.NXT まで捨てる。
	if SeqLT(seq, c.tcb.rcv.nxt) {
		skip := c.tcb.rcv.nxt - seq
		if skip >= uint32(len(payload)) {
			return segFragment{seq: c.tcb.rcv.nxt} // 全部既受信
		}
		payload = payload[skip:]
		seq = c.tcb.rcv.nxt
	}
	// 右トリム: 窓の右端 RCV.NXT+RCV.WND を超えるぶんを捨てる。
	winEnd := c.tcb.rcv.nxt + c.tcb.rcv.wnd
	if SeqGT(end, winEnd) {
		over := end - winEnd
		if over >= uint32(len(payload)) {
			return segFragment{seq: seq}
		}
		payload = payload[:uint32(len(payload))-over]
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	return segFragment{seq: seq, data: cp}
}

// insertOoo は先行セグメントを oooSegs に seq 昇順で挿入する。
// 既存と重複する部分は drainOoo 側の trim で解消するため、ここは単純挿入でよい。
func (c *Conn) insertOoo(frag segFragment) {
	i := 0
	for i < len(c.tcb.oooSegs) && SeqLT(c.tcb.oooSegs[i].seq, frag.seq) {
		i++
	}
	if i < len(c.tcb.oooSegs) && c.tcb.oooSegs[i].seq == frag.seq &&
		len(c.tcb.oooSegs[i].data) >= len(frag.data) {
		return // 同じ seq で既存が同等以上: 重複なので捨てる。
	}
	c.tcb.oooSegs = append(c.tcb.oooSegs, segFragment{})
	copy(c.tcb.oooSegs[i+1:], c.tcb.oooSegs[i:])
	c.tcb.oooSegs[i] = frag
}

// drainOoo は RCV.NXT に連続する保持セグメントを順に rcvBuf へ取り込む。
// 部分重複 (一部既受信) は RCV.NXT で切り詰めて新規ぶんだけ取り込む。
func (c *Conn) drainOoo() {
	for {
		idx := -1
		for i := range c.tcb.oooSegs {
			s := c.tcb.oooSegs[i]
			segEnd := s.seq + uint32(len(s.data))
			if SeqLEQ(segEnd, c.tcb.rcv.nxt) {
				continue // 全部既受信: あとで掃除。
			}
			if SeqLEQ(s.seq, c.tcb.rcv.nxt) {
				idx = i // RCV.NXT を覆う (連続 or 部分重複)。
				break
			}
		}
		if idx == -1 {
			break
		}
		s := c.tcb.oooSegs[idx]
		// RCV.NXT より前の既受信ぶんを飛ばして新規ぶんだけ取り込む。
		skip := c.tcb.rcv.nxt - s.seq
		newData := s.data[skip:]
		c.tcb.rcvBuf = append(c.tcb.rcvBuf, newData...)
		c.tcb.rcv.nxt += uint32(len(newData))
		c.removeFullyConsumedOoo()
	}
	c.removeFullyConsumedOoo()
}

// removeFullyConsumedOoo は RCV.NXT 以下に収まりきった (もう新規ぶんが無い)
// 保持セグメントを oooSegs から取り除く。
func (c *Conn) removeFullyConsumedOoo() {
	kept := c.tcb.oooSegs[:0]
	for _, s := range c.tcb.oooSegs {
		segEnd := s.seq + uint32(len(s.data))
		if SeqLEQ(segEnd, c.tcb.rcv.nxt) {
			continue
		}
		kept = append(kept, s)
	}
	c.tcb.oooSegs = kept
}

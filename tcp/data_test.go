package tcp

import "github.com/hakkadaikon/tcp_vibe/tcp/link"

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
)

// establishedConn は握手を済ませた ESTABLISHED の Conn を返す。
// 相手の広告窓は wnd。送出は peer で観測する。ISS=1000, 相手 ISS=5000。
func establishedConn(t *testing.T, wnd uint16) (*Conn, link.Link, *fakeClock) {
	t.Helper()
	c, peer, fc := newTestConn(t)
	c.ActiveOpen(1000)
	drainPeer(t, peer) // SYN を読み捨て
	c.onSegment(TCPHeader{Flags: Flags(FlagSYN | FlagACK), SeqNum: 5000, AckNum: 1001, Window: wnd}, nil)
	if c.State() != Established {
		t.Fatalf("握手が成立していない: %v", c.State())
	}
	drainPeer(t, peer) // 握手の ACK を読み捨て
	return c, peer, fc
}

// drainPayloads は peer に溜まった全セグメントを読み、各 payload を連結して返す。
func drainPayloads(peer link.Link) []byte {
	peer.Close()
	var out []byte
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
		out = append(out, seg[int(h.DataOffset)*4:]...)
	}
}

// 送信: Send したデータが payload 付きで送出され、SND.NXT が前進する。
func TestSendEmitsPayload(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	msg := []byte("hello world")
	n, err := c.Send(msg)
	if err != nil || n != len(msg) {
		t.Fatalf("Send 失敗: n=%d err=%v", n, err)
	}
	if got := c.SndNxt(); got != 1001+uint32(len(msg)) {
		t.Fatalf("SND.NXT が payload 分前進していない: got %d", got)
	}
	if got := drainPayloads(peer); !bytes.Equal(got, msg) {
		t.Fatalf("送出 payload 不一致: got %q want %q", got, msg)
	}
}

// 送信: ESTABLISHED 前の Send はエラー。
func TestSendBeforeEstablished(t *testing.T) {
	c, _, _ := newTestConn(t)
	if _, err := c.Send([]byte("x")); err == nil {
		t.Fatal("ESTABLISHED 前の Send はエラーのはず")
	}
}

// 送信: ゼロ長 Send は何も送らず成功 (n=0)。
func TestSendZeroLength(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	n, err := c.Send(nil)
	if err != nil || n != 0 {
		t.Fatalf("ゼロ長 Send: n=%d err=%v", n, err)
	}
	if got := drainPayloads(peer); len(got) != 0 {
		t.Fatalf("ゼロ長 Send で payload が出てはいけない: got %q", got)
	}
}

// 送信: MSS を超えるデータは複数セグメントに分割され、全バイトが順序通り出る。
func TestSendSplitsByMSS(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	msg := bytes.Repeat([]byte("A"), defaultMSS*2+100)
	if _, err := c.Send(msg); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	if got := drainPayloads(peer); !bytes.Equal(got, msg) {
		t.Fatalf("分割送出の連結が元データと不一致: got %d bytes want %d", len(got), len(msg))
	}
}

// 送信窓: 窓を超える Send は窓ぶんだけ送られ、残りは ACK で窓が空いたら送られる。
func TestSendBlocksOnWindow(t *testing.T) {
	c, peer, _ := establishedConn(t, 5) // 窓 5 バイト
	if _, err := c.Send([]byte("0123456789")); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	// 相手が 5 バイト ACK + 窓を 10 に広げる → 残りが送られる。
	// (drainPayloads は peer を閉じるので、ACK 投入後にまとめて読む。)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1006, Window: 10}, nil)
	if got := drainPayloads(peer); !bytes.Equal(got, []byte("0123456789")) {
		t.Fatalf("窓 5→10 で最初 5 + 残り 5 が送られるはず: got %q", got)
	}
}

// 送信バッファ・再送キュー解放: ACK で確認されたぶんは再送されない。
func TestAckReleasesSendBuffer(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	if _, err := c.Send([]byte("hello")); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	drainPayloads(peer)
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1006, Window: maxWindow}, nil)
	c.mu.Lock()
	bufLen := len(c.tcb.sndBuf)
	qLen := len(c.tcb.retxQueue)
	c.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("ACK 後は送信バッファが空のはず: got %d", bufLen)
	}
	if qLen != 0 {
		t.Fatalf("ACK 後は再送キューが空のはず: got %d", qLen)
	}
}

// 受信: 順番通りのデータを受け取り Recv で読める。
func TestRecvInOrder(t *testing.T) {
	c, _, _ := establishedConn(t, maxWindow)
	c.onSegment(seg(5001, 1001, []byte("hello")), []byte("hello"))
	buf := make([]byte, 32)
	n, err := c.Recv(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte("hello")) {
		t.Fatalf("Recv: n=%d err=%v got=%q", n, err, buf[:n])
	}
	if got := c.RcvNxt(); got != 5006 {
		t.Fatalf("RCV.NXT が前進していない: got %d", got)
	}
}

// 受信: データ無しの Recv は 0 を返しブロックしない。
func TestRecvEmptyNonblock(t *testing.T) {
	c, _, _ := establishedConn(t, maxWindow)
	n, err := c.Recv(make([]byte, 8))
	if n != 0 || err != nil {
		t.Fatalf("空 Recv は (0,nil) のはず: n=%d err=%v", n, err)
	}
}

// 受信: 小さいバッファでの分割読み。
func TestRecvPartialRead(t *testing.T) {
	c, _, _ := establishedConn(t, maxWindow)
	c.onSegment(seg(5001, 1001, []byte("hello")), []byte("hello"))
	buf := make([]byte, 2)
	var got []byte
	for {
		n, err := c.Recv(buf)
		if n == 0 {
			if err == io.EOF || err == nil {
				break
			}
		}
		got = append(got, buf[:n]...)
		if len(got) >= 5 {
			break
		}
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("分割読みの連結不一致: got %q", got)
	}
}

// 受信: FIN 後に残データを読み切ると EOF。
func TestRecvEOFAfterFin(t *testing.T) {
	c, _, _ := establishedConn(t, maxWindow)
	c.onSegment(seg(5001, 1001, []byte("hi")), []byte("hi"))
	c.onSegment(TCPHeader{Flags: Flags(FlagFIN | FlagACK), SeqNum: 5003, AckNum: 1001, Window: maxWindow}, nil)
	buf := make([]byte, 8)
	n, _ := c.Recv(buf)
	if !bytes.Equal(buf[:n], []byte("hi")) {
		t.Fatalf("残データ読み出し不一致: got %q", buf[:n])
	}
	if _, err := c.Recv(buf); err != io.EOF {
		t.Fatalf("残データ読み切り後は EOF のはず: got %v", err)
	}
}

// 受信: 手前にデータ欠けがある間は先行 FIN を消費しない (欠け埋め後に EOF)。
func TestFinNotConsumedBeforeGap(t *testing.T) {
	c, _, _ := establishedConn(t, maxWindow)
	// "ab" は届くが "cd" が欠けたまま、その後ろの FIN(seq=5005) が先に届く想定。
	c.onSegment(seg(5001, 1001, []byte("ab")), []byte("ab"))
	// 欠けの先 (seq=5003 の "cd") を飛ばして FIN を投入: seq=5005, len 込み末尾は RCV.NXT と不一致。
	c.onSegment(TCPHeader{Flags: Flags(FlagFIN | FlagACK), SeqNum: 5005, AckNum: 1001, Window: maxWindow}, nil)
	if c.State() != Established {
		t.Fatalf("欠けがある間は FIN を消費せず ESTABLISHED のまま: got %v", c.State())
	}
	// 欠けの "cd" が届くと RCV.NXT が FIN seq に追いつく。FIN を再送すれば消費される。
	c.onSegment(seg(5003, 1001, []byte("cd")), []byte("cd"))
	c.onSegment(TCPHeader{Flags: Flags(FlagFIN | FlagACK), SeqNum: 5005, AckNum: 1001, Window: maxWindow}, nil)
	if c.State() != CloseWait {
		t.Fatalf("欠け埋め後の FIN で CLOSE-WAIT のはず: got %v", c.State())
	}
}

// seg はデータセグメントのヘッダを組む。payload は呼び出し側が別途 onSegment に渡す。
func seg(seqNum, ackNum uint32, payload []byte) TCPHeader {
	return TCPHeader{
		Flags:  Flags(FlagPSH | FlagACK),
		SeqNum: seqNum,
		AckNum: ackNum,
		Window: maxWindow,
	}
}

// 再組立て: 順不同・重複・部分重複で投入しても元のバイト列に戻る (ランダム多数回)。
func TestReassemblyRandomOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	const irs = 5000
	for trial := 0; trial < 200; trial++ {
		c, _, _ := establishedConn(t, maxWindow)
		// 元ストリームを作る。
		streamLen := 1 + rng.Intn(60)
		stream := make([]byte, streamLen)
		for i := range stream {
			stream[i] = byte('a' + rng.Intn(26))
		}
		// 連続する断片に分割 (1〜7 バイト)、各断片をセグメント化。
		type frag struct {
			off, n int
		}
		var frags []frag
		for off := 0; off < streamLen; {
			n := 1 + rng.Intn(7)
			if off+n > streamLen {
				n = streamLen - off
			}
			frags = append(frags, frag{off, n})
			off += n
		}
		// 重複・部分重複を混ぜる: 一部の断片を複製し、たまに前にずらして重ねる。
		var send []frag
		for _, f := range frags {
			send = append(send, f)
			if rng.Intn(3) == 0 {
				send = append(send, f) // 完全重複
			}
			if rng.Intn(3) == 0 && f.off > 0 {
				// 部分重複: 1 バイト手前から始める。
				send = append(send, frag{f.off - 1, f.n + 1})
			}
		}
		// 順不同にシャッフルして投入。
		rng.Shuffle(len(send), func(i, j int) { send[i], send[j] = send[j], send[i] })
		for _, f := range send {
			payload := stream[f.off : f.off+f.n]
			c.onSegment(seg(irs+1+uint32(f.off), 1001, payload), payload)
		}
		// 全部埋まれば元ストリームに戻る。
		var got []byte
		buf := make([]byte, 128)
		for {
			n, _ := c.Recv(buf)
			if n == 0 {
				break
			}
			got = append(got, buf[:n]...)
		}
		if !bytes.Equal(got, stream) {
			t.Fatalf("trial %d: 再組立て不一致\n got=%q\nwant=%q", trial, got, stream)
		}
		if c.RcvNxt() != irs+1+uint32(streamLen) {
			t.Fatalf("trial %d: RCV.NXT が全部前進していない: got %d want %d", trial, c.RcvNxt(), irs+1+uint32(streamLen))
		}
	}
}

// 再送: payload を載せたセグメントが RTO で payload 込みで再送される。
func TestRetransmitCarriesPayload(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	if _, err := c.Send([]byte("data!")); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	fc.advance(initialRTO)
	c.Tick()
	got := drainPayloads(peer)
	// 初回 + 再送で "data!" が 2 回出るはず。
	if !bytes.Equal(got, []byte("data!data!")) {
		t.Fatalf("再送で payload が再送されていない: got %q", got)
	}
}

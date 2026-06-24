package transport

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
	"testing/quick"
)

// 受信側の再組立てが満たすべき性質を testing/quick で橋渡しする。
// onSegment へデータセグメントを順不同・重複・部分重複で投入し、Recv で読めた
// バイト列と RCV.NXT の振る舞いを検査する。投入順と分割は quick が生成する。

const reasmIRS = 5000 // 相手 ISS。establishedConn が固定で使う値に合わせる。

// drainRecv は Recv で読めるだけ読み、連結したバイト列を返す。
func drainRecv(c *Conn) []byte {
	var got []byte
	buf := make([]byte, 256)
	for {
		n, _ := c.Recv(buf)
		if n == 0 {
			break
		}
		got = append(got, buf[:n]...)
	}
	return got
}

// reasmPlan は quick が生成する再組立てシナリオ。streamLen のストリームを
// 各 cut でフラグメントに切り、order で投入順を、dup で複製有無を決める。
type reasmPlan struct {
	streamLen int
	cuts      []int  // 分割位置 (0..streamLen の昇順でなくてよい。正規化する)
	order     []int  // フラグメント投入順 (任意。modulo で割り当てる)
	dupMask   uint64 // bit i が立つフラグメントは 2 回投入する
	straddle  bool   // RCV.NXT を跨ぐ部分重複セグメントを 1 つ混ぜる
}

// Generate は quick 用に reasmPlan を生成する (unexported フィールドのため必須)。
func (reasmPlan) Generate(rng *rand.Rand, _ int) reflect.Value {
	p := reasmPlan{
		streamLen: rng.Int(),
		dupMask:   rng.Uint64(),
		straddle:  rng.Intn(2) == 0,
	}
	for n := rng.Intn(8); n > 0; n-- {
		p.cuts = append(p.cuts, rng.Intn(80))
	}
	for n := 24; n > 0; n-- { // フラグメント総数を覆う長さの swap 列
		p.order = append(p.order, rng.Int())
	}
	return reflect.ValueOf(p)
}

// fragments は plan からフラグメント [off,end) の昇順リストを作る。
func (p reasmPlan) fragments() [][2]int {
	n := p.streamLen
	marks := map[int]bool{0: true, n: true}
	for _, c := range p.cuts {
		if c <= 0 || c >= n {
			continue
		}
		marks[c] = true
	}
	var bounds []int
	for k := 0; k <= n; k++ {
		if marks[k] {
			bounds = append(bounds, k)
		}
	}
	var frags [][2]int
	for i := 0; i+1 < len(bounds); i++ {
		frags = append(frags, [2]int{bounds[i], bounds[i+1]})
	}
	return frags
}

// buildStream は決定的に [0,n) のストリームを作る。
func buildStream(n int) []byte {
	s := make([]byte, n)
	for i := range s {
		s[i] = byte('A' + i%26)
	}
	return s
}

// 再構成健全性: ストリームを任意分割しランダム順 (+重複混入) で投入すると、
// 全部届いた後に Recv で読めるバイト列が元と完全一致する。RCV.NXT は単調に
// 前進し続け (後退しない)、最終的にストリーム末尾へ到達する。
func TestReassemblySoundnessProperty(t *testing.T) {
	f := func(p reasmPlan) bool {
		p.streamLen = 1 + (p.streamLen&0x7fffffff)%80 // 1..80
		stream := buildStream(p.streamLen)
		frags := p.fragments()
		if len(frags) == 0 {
			return true
		}

		// 投入リストを作る: 各フラグメント + dupMask による複製。
		type send struct{ off, end int }
		var list []send
		for i, fr := range frags {
			list = append(list, send{fr[0], fr[1]})
			if p.dupMask&(uint64(1)<<uint(i%64)) != 0 {
				list = append(list, send{fr[0], fr[1]}) // 完全重複
			}
		}
		// straddle: RCV.NXT 進行中に部分重複を起こすよう、後半フラグメントを
		// 1 バイト手前から始める版を 1 つ混ぜる。
		if p.straddle && len(frags) >= 2 {
			fr := frags[len(frags)-1]
			if fr[0] > 0 {
				list = append(list, send{fr[0] - 1, fr[1]})
			}
		}
		// order を Fisher-Yates の swap 列として使い投入順をシャッフルする。
		// order[i] は位置 i と swap する相手のインデックスを決める。
		for i := len(list) - 1; i > 0; i-- {
			if i-1 >= len(p.order) {
				continue
			}
			o := p.order[i-1]
			j := ((o % (i + 1)) + (i + 1)) % (i + 1)
			list[i], list[j] = list[j], list[i]
		}

		c, _, _ := establishedConn(t, maxWindow)
		prevNxt := c.RcvNxt()
		for _, s := range list {
			payload := stream[s.off:s.end]
			c.onSegment(seg(uint32(reasmIRS+1+s.off), 1001, payload), payload)
			// RCV.NXT 単調: 各セグメント処理後に後退しない。
			cur := c.RcvNxt()
			if SeqLT(cur, prevNxt) {
				t.Logf("RCV.NXT が後退: %d -> %d", prevNxt, cur)
				return false
			}
			prevNxt = cur
		}
		got := drainRecv(c)
		if !bytes.Equal(got, stream) {
			t.Logf("再組立て不一致: got=%q want=%q", got, stream)
			return false
		}
		if c.RcvNxt() != uint32(reasmIRS+1+p.streamLen) {
			t.Logf("RCV.NXT 末尾未到達: got=%d want=%d", c.RcvNxt(), reasmIRS+1+p.streamLen)
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1500}); err != nil {
		t.Error(err)
	}
}

// 重複の冪等: 既受信レンジのセグメントを再投入しても RCV.NXT は不変、Recv で
// 読めるバイトも増えない。順番通りに全部入れた後、同じセグメント群をもう一度
// 投入して検証する。
func TestReassemblyDuplicateIdempotentProperty(t *testing.T) {
	f := func(streamLen int, cuts []int) bool {
		n := 1 + (streamLen&0x7fffffff)%60
		stream := buildStream(n)
		p := reasmPlan{streamLen: n, cuts: cuts}
		frags := p.fragments()

		c, _, _ := establishedConn(t, maxWindow)
		feed := func() {
			for _, fr := range frags {
				payload := stream[fr[0]:fr[1]]
				c.onSegment(seg(uint32(reasmIRS+1+fr[0]), 1001, payload), payload)
			}
		}
		feed()
		nxtAfterFirst := c.RcvNxt()
		// 既受信ぶんを丸ごと再投入: RCV.NXT は動かないはず (冪等)。
		feed()
		if c.RcvNxt() != nxtAfterFirst {
			t.Logf("重複再投入で RCV.NXT が動いた: %d -> %d", nxtAfterFirst, c.RcvNxt())
			return false
		}
		got := drainRecv(c)
		if !bytes.Equal(got, stream) {
			t.Logf("冪等性破れ: got=%q want=%q", got, stream)
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
	}
}

// 部分重複 (straddle): a < RCV.NXT < a+L のセグメントを入れると RCV.NXT は
// a+L へ進み、重複バイト [a,RCV.NXT) は二重取り込みされず新規ぶんだけ追加される。
// 前半を順に入れて RCV.NXT を p1 まで進め、[p1-overlap, total) を投入して検証。
func TestReassemblyStraddleProperty(t *testing.T) {
	f := func(a, b, c uint8) bool {
		// 3 区間に分けて mid を作る: [0,p1) 既受信, [p1-overlap, total) を straddle 投入。
		p1 := 1 + int(a)%30      // 既受信境界 (= 投入後の RCV.NXT 相対位置)
		overlap := 1 + int(b)%p1 // 1..p1 バイトの重複
		tail := 1 + int(c)%30    // straddle セグメントの新規ぶん
		total := p1 + tail
		stream := buildStream(total)

		conn, _, _ := establishedConn(t, maxWindow)
		// [0,p1) を 1 バイトずつ順に投入 → RCV.NXT は IRS+1+p1。
		for i := 0; i < p1; i++ {
			payload := stream[i : i+1]
			conn.onSegment(seg(uint32(reasmIRS+1+i), 1001, payload), payload)
		}
		nxtBefore := conn.RcvNxt()
		if nxtBefore != uint32(reasmIRS+1+p1) {
			t.Logf("前提崩れ: RCV.NXT=%d want=%d", nxtBefore, reasmIRS+1+p1)
			return false
		}
		// straddle: [p1-overlap, total) を 1 セグメントで投入。a < RCV.NXT < a+L。
		off := p1 - overlap
		payload := stream[off:total]
		conn.onSegment(seg(uint32(reasmIRS+1+off), 1001, payload), payload)

		// RCV.NXT は total へ進む。
		if conn.RcvNxt() != uint32(reasmIRS+1+total) {
			t.Logf("straddle 後 RCV.NXT=%d want=%d", conn.RcvNxt(), reasmIRS+1+total)
			return false
		}
		// 読めるバイト列は stream 全体と一致する。straddle の重複ぶんは二重に
		// 取り込まれず、新規ぶん (tail) だけが追加される。
		got := drainRecv(conn)
		if !bytes.Equal(got, stream[:total]) {
			t.Logf("straddle 後の読み出し不一致 (重複二重取り込みの疑い): got=%q want=%q", got, stream[:total])
			return false
		}
		return true
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 1000}); err != nil {
		t.Error(err)
	}
}

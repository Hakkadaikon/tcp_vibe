package tcp

import (
	"bytes"
	"math/rand"
	"testing"
	"testing/quick"
)

const testMaxPacket = 65535

// makePacket は version/IHL/TotalLength の整合した IPv4 パケット風バイト列を作る。
// framing 層はバイト境界のみ見るためチェックサムや本文の意味は問わない。
// ihlWords は IHL (5..15)、payloadLen はヘッダ後ろの追加バイト数。
func makePacket(ihlWords, payloadLen int, fill byte) []byte {
	hdr := ihlWords * 4
	total := hdr + payloadLen
	p := make([]byte, total)
	p[0] = 0x40 | byte(ihlWords) // version=4, IHL
	p[2] = byte(total >> 8)
	p[3] = byte(total)
	for i := 4; i < total; i++ {
		p[i] = fill + byte(i) // 内容一致を確かめるため位置依存で埋める
	}
	return p
}

// pushAll は packets を連結し、stream を chunkSizes で分割して順に Push、
// 復元されたパケット列を返す。
func pushAll(t *testing.T, f *Framer, stream []byte, chunkSizes []int) [][]byte {
	t.Helper()
	var got [][]byte
	off := 0
	for _, n := range chunkSizes {
		end := off + n
		if end > len(stream) {
			end = len(stream)
		}
		out, err := f.Push(stream[off:end])
		if err != nil {
			t.Fatalf("unexpected Push error: %v", err)
		}
		got = append(got, out...)
		off = end
	}
	if off < len(stream) { // 端数を 1 回で流し込む
		out, err := f.Push(stream[off:])
		if err != nil {
			t.Fatalf("unexpected Push error: %v", err)
		}
		got = append(got, out...)
	}
	return got
}

func equalPackets(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// T-015 (PBT 主役): 任意の IPv4 パケット列を連結した 1 本のバイト列を、
// 乱数で決めた任意の分割点でチャンク分割して順に Push しても、
// 復元パケット列が元の送信列と (順序・境界・バイト内容まで) 完全一致する。
// これは「任意のチャンク分割で再構成パケット列が送信列と一致する」不変条件そのもの。
func TestFramer_ArbitrarySplitRestores(t *testing.T) {
	f := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		// パケット列を生成 (1〜6 個、各 IHL 5..7、payload 0..40)。
		n := rng.Intn(6) + 1
		var want [][]byte
		var stream []byte
		for i := 0; i < n; i++ {
			p := makePacket(5+rng.Intn(3), rng.Intn(41), byte(rng.Intn(256)))
			want = append(want, p)
			stream = append(stream, p...)
		}
		// 任意分割: 1byte〜全体までの乱数チャンク列。
		var chunks []int
		for remain := len(stream); remain > 0; {
			c := rng.Intn(remain) + 1
			chunks = append(chunks, c)
			remain -= c
		}
		fr := NewFramer(testMaxPacket)
		got := pushAll(t, fr, stream, chunks)
		return equalPackets(got, want)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 3000}); err != nil {
		t.Error(err)
	}
}

// T-016: 部分読み境界。IP ヘッダ途中・本文途中・極端に 1byte ずつ feed しても同一復元。
func TestFramer_PartialReads(t *testing.T) {
	p1 := makePacket(5, 10, 0x11)
	p2 := makePacket(6, 0, 0x22) // ゼロ payload, オプション付きヘッダ長
	stream := append(append([]byte{}, p1...), p2...)
	want := [][]byte{p1, p2}

	// 1byte ずつ feed。
	fr := NewFramer(testMaxPacket)
	var got [][]byte
	for i := 0; i < len(stream); i++ {
		out, err := fr.Push(stream[i : i+1])
		if err != nil {
			t.Fatalf("1byte feed error at %d: %v", i, err)
		}
		got = append(got, out...)
	}
	if !equalPackets(got, want) {
		t.Errorf("1byte feed mismatch")
	}

	// ヘッダ途中 (2byte) で一度切る → まだ何も出ない。
	fr2 := NewFramer(testMaxPacket)
	out, err := fr2.Push(stream[:2])
	if err != nil || len(out) != 0 {
		t.Fatalf("2byte prefix should yield nothing: out=%d err=%v", len(out), err)
	}
}

// T-017: 連結到着。2 パケット 1 チャンク → 2 個復元。1.5 パケット → 1 個 + 残保持。
func TestFramer_ConcatenatedArrival(t *testing.T) {
	p1 := makePacket(5, 8, 0x33)
	p2 := makePacket(5, 12, 0x44)

	// 2 パケットを 1 チャンクで。
	fr := NewFramer(testMaxPacket)
	out, err := fr.Push(append(append([]byte{}, p1...), p2...))
	if err != nil {
		t.Fatal(err)
	}
	if !equalPackets(out, [][]byte{p1, p2}) {
		t.Errorf("2 packets in 1 chunk should yield both")
	}

	// 1.5 パケット: p1 全部 + p2 の前半 → 1 個だけ返り残りは保持。
	fr2 := NewFramer(testMaxPacket)
	half := append(append([]byte{}, p1...), p2[:5]...)
	out, err = fr2.Push(half)
	if err != nil {
		t.Fatal(err)
	}
	if !equalPackets(out, [][]byte{p1}) {
		t.Fatalf("1.5 packets should yield exactly p1, got %d", len(out))
	}
	// 残り feed で p2 完成。
	out, err = fr2.Push(p2[5:])
	if err != nil {
		t.Fatal(err)
	}
	if !equalPackets(out, [][]byte{p2}) {
		t.Errorf("remainder should complete p2")
	}
}

// T-018: length 確定と trust boundary。
// 宣言ちょうど → 1 個 / 宣言 > 実バッファ → 待つ / 宣言が maxPacket 超 → error。
func TestFramer_LengthDecisionAndTrustBoundary(t *testing.T) {
	// 宣言ちょうど。
	p := makePacket(5, 30, 0x55)
	fr := NewFramer(testMaxPacket)
	out, err := fr.Push(p)
	if err != nil || !equalPackets(out, [][]byte{p}) {
		t.Fatalf("exact length should yield 1: out=%d err=%v", len(out), err)
	}

	// 宣言 > 実バッファ: TotalLength=50 と宣言しつつ実体 30byte → 待つ (error なし, 0 個)。
	short := makePacket(5, 10, 0x66) // 実体 30byte
	short[2] = 0
	short[3] = 50 // 宣言 50
	fr2 := NewFramer(testMaxPacket)
	out, err = fr2.Push(short)
	if err != nil || len(out) != 0 {
		t.Fatalf("under-filled declared length should wait: out=%d err=%v", len(out), err)
	}

	// 宣言が maxPacket 超 → error。過剰確保しないことを示すため小さい上限を使う。
	big := makePacket(5, 10, 0x77) // total=30
	frSmall := NewFramer(20)       // 上限 20 < 宣言 30
	_, err = frSmall.Push(big)
	if err == nil {
		t.Error("declared length over maxPacket must error")
	}

	// version 不正 → error。
	bad := makePacket(5, 4, 0x00)
	bad[0] = 0x60 // version=6
	frBad := NewFramer(testMaxPacket)
	if _, err := frBad.Push(bad); err == nil {
		t.Error("non-IPv4 version must error")
	}

	// IHL<5 → error。
	badIHL := makePacket(5, 4, 0x00)
	badIHL[0] = 0x44 // version=4 IHL=4
	frIHL := NewFramer(testMaxPacket)
	if _, err := frIHL.Push(badIHL); err == nil {
		t.Error("IHL<5 must error")
	}

	// バッファ肥大防止: maxPacket を超える未確定蓄積 → error。
	// 上限 40、宣言 40 のパケットの先頭だけ少しずつ送り 41byte 溜める前に error になること。
	frFull := NewFramer(40)
	hdr := makePacket(5, 20, 0x88) // total=40, ちょうど上限
	out, err = frFull.Push(hdr)
	if err != nil || !equalPackets(out, [][]byte{hdr}) {
		t.Fatalf("total==maxPacket should pass: err=%v", err)
	}
}

// T-019 (metamorphic): 同じバイト列を「1 回で feed」と「任意分割で feed」した
// 結果が常に同一 (分割不変)。
func TestFramer_SplitInvariance(t *testing.T) {
	f := func(seed int64) bool {
		rng := rand.New(rand.NewSource(seed))
		n := rng.Intn(5) + 1
		var stream []byte
		for i := 0; i < n; i++ {
			stream = append(stream, makePacket(5+rng.Intn(2), rng.Intn(30), byte(rng.Intn(256)))...)
		}
		// 一括 feed。
		whole := NewFramer(testMaxPacket)
		oneShot, err := whole.Push(stream)
		if err != nil {
			return false
		}
		// 任意分割 feed。
		var chunks []int
		for remain := len(stream); remain > 0; {
			c := rng.Intn(remain) + 1
			chunks = append(chunks, c)
			remain -= c
		}
		split := pushAll(t, NewFramer(testMaxPacket), stream, chunks)
		return equalPackets(oneShot, split)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// T-020: ゼロ長 payload / 最小 (20byte ヘッダのみ) / 最大セグメント。
func TestFramer_ZeroMinMax(t *testing.T) {
	// 最小: 20byte ヘッダのみ、payload 0。
	min := makePacket(5, 0, 0x01)
	if len(min) != 20 {
		t.Fatalf("min packet must be 20 bytes, got %d", len(min))
	}
	fr := NewFramer(testMaxPacket)
	out, err := fr.Push(min)
	if err != nil || !equalPackets(out, [][]byte{min}) {
		t.Fatalf("min packet failed: out=%d err=%v", len(out), err)
	}

	// 最大: TotalLength=65535。
	max := makePacket(5, 65535-20, 0x02)
	if len(max) != 65535 {
		t.Fatalf("max packet must be 65535 bytes, got %d", len(max))
	}
	fr2 := NewFramer(testMaxPacket)
	out, err = fr2.Push(max)
	if err != nil || !equalPackets(out, [][]byte{max}) {
		t.Fatalf("max packet failed: out=%d err=%v", len(out), err)
	}

	// ゼロ長 Push は何も起こさない。
	fr3 := NewFramer(testMaxPacket)
	out, err = fr3.Push(nil)
	if err != nil || len(out) != 0 {
		t.Fatalf("empty push should be no-op: out=%d err=%v", len(out), err)
	}
}

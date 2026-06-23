package tcp

import (
	"testing"
	"testing/quick"
)

// T-004: checksum 計算正当性 (GOLD 既知バイト列の手計算値一致)。
// 教科書的な IPv4 ヘッダ例 (RFC 1071 系)。checksum フィールドは 0 にしてある。
//
//	4500 0073 0000 4000 4011 0000 c0a8 0001 c0a8 00c7
//
// ワード和 = 0x0001 0BBE → 畳み込み 0x0BBF → 補数 0xB861。
func TestChecksum_Gold(t *testing.T) {
	data := []byte{
		0x45, 0x00, 0x00, 0x73, 0x00, 0x00, 0x40, 0x00,
		0x40, 0x11, 0x00, 0x00, 0xc0, 0xa8, 0x00, 0x01,
		0xc0, 0xa8, 0x00, 0xc7,
	}
	const want uint16 = 0xb861
	if got := Checksum(data); got != want {
		t.Errorf("Checksum = %#04x want %#04x", got, want)
	}
}

// T-007: end-around carry とゼロ表現。合計が 0xFFFF なら補数 0x0000。
func TestChecksum_EndAroundCarryAndZero(t *testing.T) {
	// 0xFFFF + 0x0000 = 0xFFFF → 補数 0x0000。
	if got := Checksum([]byte{0xFF, 0xFF, 0x00, 0x00}); got != 0x0000 {
		t.Errorf("sum 0xFFFF should give checksum 0x0000, got %#04x", got)
	}
	// carry が複数段でも畳み込みで収束すること。
	// 0xFFFF * 3 = 0x2FFFD → 畳み込み 0xFFFF+0x0002 = ... = 0xFFFF → 補数 0x0000。
	if got := Checksum([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}); got != 0x0000 {
		t.Errorf("three 0xFFFF words should give 0x0000, got %#04x", got)
	}
}

// T-006: 16bit アラインメント (奇数長は末尾を 0 パディング)。
// 末尾 1 バイト 0xAB は 0xAB00 として加算される。明示ゼロを足した偶数長と一致。
func TestChecksum_OddLengthPadding(t *testing.T) {
	odd := []byte{0x12, 0x34, 0xAB}
	even := []byte{0x12, 0x34, 0xAB, 0x00}
	if Checksum(odd) != Checksum(even) {
		t.Errorf("odd-length padding mismatch: odd=%#04x even=%#04x", Checksum(odd), Checksum(even))
	}
}

// T-005: 検証往復 + 1bit 反転検出 (property / metamorphic)。
// 正しい checksum を埋めたバッファ全体の Checksum は 0 になる。
// さらに 1bit でも反転すれば 0 でなくなる (誤り検出)。
func TestChecksum_VerifyRoundTripAndBitFlip(t *testing.T) {
	verify := func(payload []byte, flipByte uint8, flipBit uint8) bool {
		if len(payload) == 0 {
			return true
		}
		// checksum を末尾 2 バイトに埋める領域を確保し、全体を偶数長に揃える
		// (奇数長 padding と checksum 埋め込みが干渉しないようにする)。
		size := len(payload) + 2
		if size%2 == 1 {
			size++
		}
		buf := make([]byte, size)
		copy(buf, payload)
		cs := Checksum(buf) // 末尾 2 バイトは 0 の状態で計算
		putBe16(buf, len(buf)-2, cs)
		// 検証往復: 正しい checksum 込みなら 0。
		if Checksum(buf) != 0 {
			return false
		}
		// 1bit 反転: payload 部分の 1bit を反転すると 0 でなくなるはず。
		idx := int(flipByte) % len(payload)
		buf[idx] ^= 1 << (flipBit % 8)
		return Checksum(buf) != 0
	}
	if err := quick.Check(verify, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// T-004/T-005: 擬似ヘッダ込みの検証往復。
// セグメント内の checksum フィールド (offset 16) に計算値を埋めると、
// 同じ擬似ヘッダで再計算した結果が 0 になる。
func TestTCPChecksum_VerifyRoundTrip(t *testing.T) {
	src := [4]byte{192, 168, 0, 1}
	dst := [4]byte{192, 168, 0, 199}
	seg := make([]byte, 20)     // 最小 TCP ヘッダ (checksum=0)
	seg[0], seg[1] = 0x30, 0x39 // src port 12345
	seg[2], seg[3] = 0x00, 0x50 // dst port 80
	cs := TCPChecksum(src, dst, seg)
	putBe16(seg, 16, cs) // checksum フィールド
	if got := TCPChecksum(src, dst, seg); got != 0 {
		t.Errorf("TCP checksum verify round-trip should be 0, got %#04x", got)
	}
}

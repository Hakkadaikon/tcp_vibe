package tcp

import "testing"

// ワイヤ形式パーサ (trust boundary) の fuzz target。任意バイト列で panic や
// 境界外読みが起きないことを確認する。エラーを返すのは正常 (不正入力の拒否)。
// 通常の `go test` では seed corpus を実行するだけで十分なカバレッジになり、
// `go test -fuzz=FuzzXxx` で探索を続けられる。

func FuzzParseTCPHeader(f *testing.F) {
	// seed: 最小ヘッダ・option 付き・短すぎ・data offset 過大。
	f.Add(TCPHeader{SrcPort: 1, DstPort: 2, DataOffset: 5, Flags: Flags(FlagSYN)}.Marshal())
	f.Add(TCPHeader{SrcPort: 1, DstPort: 2, DataOffset: 5, Flags: Flags(FlagACK),
		Options: TCPOptions{HasMSS: true, MSS: 1460, HasTimestamp: true}.Marshal()}.Marshal())
	f.Add([]byte{0, 1, 2})                                                       // 20 バイト未満
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xF0, 0, 0, 0, 0, 0, 0, 0}) // dataOffset=15 だが option 無し

	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := ParseTCPHeader(b)
		if err != nil {
			return
		}
		// 受理したヘッダは option 領域まで読めるはず (境界外参照しない)。
		_, _ = ParseTCPOptions(h.Options)
	})
}

func FuzzParseTCPOptions(f *testing.F) {
	f.Add(TCPOptions{HasMSS: true, MSS: 1460}.Marshal())
	f.Add(TCPOptions{HasWScale: true, WindowScale: 7, HasTimestamp: true, TSVal: 1, TSecr: 2,
		SACKPermitted: true}.Marshal())
	f.Add([]byte{optMSS, 4, 0x05}) // length=4 と宣言するが value 欠け
	f.Add([]byte{optSACK, 0})      // length=0 (不正)
	f.Add([]byte{3, 200})          // length が領域を超える
	f.Add([]byte{optNOP, optNOP, optEOL})

	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = ParseTCPOptions(b) // panic しなければ良い (エラー返しは正常)
	})
}

func FuzzParseIPv4Header(f *testing.F) {
	f.Add(IPv4Header{Protocol: 6, TotalLength: 20, TTL: 64,
		SrcAddr: [4]byte{10, 0, 0, 1}, DstAddr: [4]byte{10, 0, 0, 2}}.Marshal())
	f.Add([]byte{0x45, 0, 0, 20})                                                // 20 バイト未満
	f.Add([]byte{0x4F, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) // IHL=15 だが 20 バイトしか無い

	f.Fuzz(func(t *testing.T, b []byte) {
		h, err := ParseIPv4Header(b)
		if err != nil {
			return
		}
		// 受理した IP ヘッダから TCP セグメント切り出しまで境界外を読まない。
		_, _ = tcpSegment(h, b)
	})
}

package network

// ネットワークバイトオーダ (big-endian) の読み書きヘルパ。
// encoding/binary を使わず自前で持つ (標準ライブラリ最小化の方針)。

// Be16 は b[off:off+2] を uint16 (big-endian) として読む。
func Be16(b []byte, off int) uint16 {
	return uint16(b[off])<<8 | uint16(b[off+1])
}

// Be32 は b[off:off+4] を uint32 (big-endian) として読む。
func Be32(b []byte, off int) uint32 {
	return uint32(b[off])<<24 | uint32(b[off+1])<<16 | uint32(b[off+2])<<8 | uint32(b[off+3])
}

// PutBe16 は v を big-endian で b[off:off+2] に書く。
func PutBe16(b []byte, off int, v uint16) {
	b[off] = byte(v >> 8)
	b[off+1] = byte(v)
}

// PutBe32 は v を big-endian で b[off:off+4] に書く。
func PutBe32(b []byte, off int, v uint32) {
	b[off] = byte(v >> 24)
	b[off+1] = byte(v >> 16)
	b[off+2] = byte(v >> 8)
	b[off+3] = byte(v)
}

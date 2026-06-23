package tcp

import "errors"

// TCP の制御ビット (RFC 9293 §3.1)。下位 6 ビットを使う。
type Flags uint8

const (
	FlagFIN Flags = 1 << 0
	FlagSYN Flags = 1 << 1
	FlagRST Flags = 1 << 2
	FlagPSH Flags = 1 << 3
	FlagACK Flags = 1 << 4
	FlagURG Flags = 1 << 5
)

// Has は f に指定フラグが立っているか返す。
func (f Flags) Has(x Flags) bool { return f&x != 0 }

// TCP ヘッダ (RFC 9293 §3.1)。本スタックに必要な最小限のフィールド。
type TCPHeader struct {
	SrcPort    uint16
	DstPort    uint16
	SeqNum     uint32
	AckNum     uint32
	DataOffset uint8 // 32bit ワード数。最小 5 (=20 バイト)
	Flags      Flags
	Window     uint16
	Checksum   uint16 // 擬似ヘッダ依存。Marshal では 0、TCPChecksum で別途埋める
	UrgentPtr  uint16
}

var (
	errTCPShort      = errors.New("tcp: buffer shorter than declared header")
	errTCPDataOffset = errors.New("tcp: data offset out of range")
)

// Marshal は TCP ヘッダを 20 バイトへ書き出す。
// オプションは扱わないため DataOffset は 5 固定で出力する。
// チェックサムは擬似ヘッダが必要なため 0 のままにし、呼び出し側が TCPChecksum で埋める。
func (h TCPHeader) Marshal() []byte {
	b := make([]byte, 20)
	putBe16(b, 0, h.SrcPort)
	putBe16(b, 2, h.DstPort)
	putBe32(b, 4, h.SeqNum)
	putBe32(b, 8, h.AckNum)
	b[12] = 5 << 4 // data offset=5, reserved=0
	b[13] = byte(h.Flags & 0x3F)
	putBe16(b, 14, h.Window)
	putBe16(b, 16, h.Checksum)
	putBe16(b, 18, h.UrgentPtr)
	return b
}

// ParseTCPHeader はバイト列を TCP ヘッダへ復号する。
// 宣言長より短いバッファ・不正な data offset は拒否する (trust boundary)。
func ParseTCPHeader(b []byte) (TCPHeader, error) {
	if len(b) < 20 {
		return TCPHeader{}, errTCPShort
	}
	dataOffset := b[12] >> 4
	if dataOffset < 5 {
		return TCPHeader{}, errTCPDataOffset
	}
	if len(b) < int(dataOffset)*4 { // 宣言長 > 実バッファ: 過剰確保せず拒否
		return TCPHeader{}, errTCPShort
	}
	return TCPHeader{
		SrcPort:    be16(b, 0),
		DstPort:    be16(b, 2),
		SeqNum:     be32(b, 4),
		AckNum:     be32(b, 8),
		DataOffset: dataOffset,
		Flags:      Flags(b[13] & 0x3F),
		Window:     be16(b, 14),
		Checksum:   be16(b, 16),
		UrgentPtr:  be16(b, 18),
	}, nil
}

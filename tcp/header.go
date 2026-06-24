package tcp

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

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
	Options    []byte // オプション領域の生バイト (4 バイト境界済み)。nil ならオプション無し
}

var (
	errTCPShort      = errors.New("tcp: buffer shorter than declared header")
	errTCPDataOffset = errors.New("tcp: data offset out of range")
)

// Marshal は TCP ヘッダ (20 バイト + オプション領域) を書き出す。
// Options は 4 バイト境界済みの生バイト列を想定し、その長さから DataOffset を決める。
// チェックサムは擬似ヘッダが必要なため 0 のままにし、呼び出し側が TCPChecksum で埋める。
func (h TCPHeader) Marshal() []byte {
	b := make([]byte, 20+len(h.Options))
	network.PutBe16(b, 0, h.SrcPort)
	network.PutBe16(b, 2, h.DstPort)
	network.PutBe32(b, 4, h.SeqNum)
	network.PutBe32(b, 8, h.AckNum)
	b[12] = DataOffsetForOptions(len(h.Options)) << 4 // data offset, reserved=0
	b[13] = byte(h.Flags & 0x3F)
	network.PutBe16(b, 14, h.Window)
	network.PutBe16(b, 16, h.Checksum)
	network.PutBe16(b, 18, h.UrgentPtr)
	copy(b[20:], h.Options)
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
	var opts []byte
	if hdrLen := int(dataOffset) * 4; hdrLen > 20 {
		opts = b[20:hdrLen] // オプション領域 (固定 20 バイト超)
	}
	return TCPHeader{
		SrcPort:    network.Be16(b, 0),
		DstPort:    network.Be16(b, 2),
		SeqNum:     network.Be32(b, 4),
		AckNum:     network.Be32(b, 8),
		DataOffset: dataOffset,
		Flags:      Flags(b[13] & 0x3F),
		Window:     network.Be16(b, 14),
		Checksum:   network.Be16(b, 16),
		UrgentPtr:  network.Be16(b, 18),
		Options:    opts,
	}, nil
}

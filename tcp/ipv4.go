package tcp

import "errors"

// IPv4 ヘッダ (RFC 791)。本スタックに必要な最小限のフィールドのみ持つ。
type IPv4Header struct {
	Version     uint8 // 常に 4
	IHL         uint8 // ヘッダ長 (32bit ワード数)。最小 5 (=20 バイト)
	TotalLength uint16
	ID          uint16
	TTL         uint8
	Protocol    uint8 // 6=TCP, 17=UDP
	SrcAddr     [4]byte
	DstAddr     [4]byte
}

var (
	errIPv4Short    = errors.New("ipv4: buffer shorter than declared header")
	errIPv4IHL      = errors.New("ipv4: IHL out of range")
	errIPv4Checksum = errors.New("ipv4: header checksum mismatch")
)

// Marshal は IPv4 ヘッダをバイト列へ書き出す。チェックサムも計算して埋める。
// オプションは扱わないため IHL は 5 (20 バイト) として出力する。
func (h IPv4Header) Marshal() []byte {
	b := make([]byte, 20)
	b[0] = 4<<4 | 5 // version=4, IHL=5
	// b[1] (DSCP/ECN) は 0。
	putBe16(b, 2, h.TotalLength)
	putBe16(b, 4, h.ID)
	// b[6:8] (flags/fragment offset) は 0。
	b[8] = h.TTL
	b[9] = h.Protocol
	// b[10:12] チェックサムは一旦 0 のまま。
	copy(b[12:16], h.SrcAddr[:])
	copy(b[16:20], h.DstAddr[:])
	putBe16(b, 10, Checksum(b))
	return b
}

// ParseIPv4Header はバイト列を IPv4 ヘッダへ復号する。
// 宣言長より短いバッファ・不正な IHL・チェックサム不一致は拒否する (trust boundary)。
func ParseIPv4Header(b []byte) (IPv4Header, error) {
	if len(b) < 20 {
		return IPv4Header{}, errIPv4Short
	}
	ihl := b[0] & 0x0F
	if ihl < 5 {
		return IPv4Header{}, errIPv4IHL
	}
	hdrLen := int(ihl) * 4
	if len(b) < hdrLen { // 宣言長 > 実バッファ: 過剰確保せず拒否
		return IPv4Header{}, errIPv4Short
	}
	if Checksum(b[:hdrLen]) != 0 {
		return IPv4Header{}, errIPv4Checksum
	}
	h := IPv4Header{
		Version:     b[0] >> 4,
		IHL:         ihl,
		TotalLength: be16(b, 2),
		ID:          be16(b, 4),
		TTL:         b[8],
		Protocol:    b[9],
	}
	copy(h.SrcAddr[:], b[12:16])
	copy(h.DstAddr[:], b[16:20])
	return h, nil
}

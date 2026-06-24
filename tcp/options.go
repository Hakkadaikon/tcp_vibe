package tcp

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import "errors"

// TCP オプションの parse / marshal (RFC 9293 §3.1, RFC 7323, RFC 2018)。
// 純粋なワイヤ形式処理。状態機械・輻輳制御のロジックは含まない。

// オプション kind 番号。
const (
	optEOL       = 0  // End of Option List
	optNOP       = 1  // No-Operation (パディング)
	optMSS       = 2  // Maximum Segment Size       len=4
	optWScale    = 3  // Window Scale               len=3
	optSACKPerm  = 4  // SACK Permitted             len=2
	optSACK      = 5  // SACK blocks                len=8n+2
	optTimestamp = 8  // Timestamps                 len=10
	maxWScale    = 14 // shift の上限 (RFC 7323 §2.3)。超過は clamp。
)

// TCPOptions は TCP ヘッダのオプション領域を解釈した値。
// 各 HasXxx / 真偽フラグで「その option が存在したか」を表す。
type TCPOptions struct {
	MSS           uint16
	HasMSS        bool
	WindowScale   uint8 // shift 量 (0..14)
	HasWScale     bool
	TSVal         uint32
	TSecr         uint32
	HasTimestamp  bool
	SACKPermitted bool
	SACKBlocks    [][2]uint32 // 各要素 [Left, Right)
}

var errBadOption = errors.New("tcp: malformed option")

// ParseTCPOptions は TCP ヘッダのオプション領域 (data offset の 20 バイト超の部分)
// をパースする。
//
// trust boundary: length=0 や残りバイトを超える length は不正として弾き、
// 境界外は読まない。未知 kind は length 分読み飛ばして無視する。
// EOL 以降はゼロパディング扱いで終端する。
func ParseTCPOptions(b []byte) (TCPOptions, error) {
	var o TCPOptions
	i := 0
	for i < len(b) {
		kind := b[i]
		if kind == optEOL {
			break // 以降はパディング
		}
		if kind == optNOP {
			i++
			continue
		}
		// kind+length+value 形式。length バイトが領域内にあるか確認。
		if i+1 >= len(b) {
			return TCPOptions{}, errBadOption
		}
		length := int(b[i+1])
		// length は kind+length 自身を含む全体バイト数。最低 2、かつ領域内。
		if length < 2 || i+length > len(b) {
			return TCPOptions{}, errBadOption
		}
		val := b[i+2 : i+length] // value 部 (length-2 バイト)

		switch kind {
		case optMSS:
			if length != 4 {
				return TCPOptions{}, errBadOption
			}
			o.HasMSS = true
			o.MSS = network.Be16(val, 0)
		case optWScale:
			if length != 3 {
				return TCPOptions{}, errBadOption
			}
			o.HasWScale = true
			o.WindowScale = clampWScale(val[0])
		case optSACKPerm:
			if length != 2 {
				return TCPOptions{}, errBadOption
			}
			o.SACKPermitted = true
		case optSACK:
			if length < 2 || (length-2)%8 != 0 {
				return TCPOptions{}, errBadOption
			}
			n := (length - 2) / 8
			for k := 0; k < n; k++ {
				off := k * 8
				o.SACKBlocks = append(o.SACKBlocks,
					[2]uint32{network.Be32(val, off), network.Be32(val, off+4)})
			}
		case optTimestamp:
			if length != 10 {
				return TCPOptions{}, errBadOption
			}
			o.HasTimestamp = true
			o.TSVal = network.Be32(val, 0)
			o.TSecr = network.Be32(val, 4)
		default:
			// 未知 option: length 分読み飛ばして無視 (エラーにしない)。
		}
		i += length
	}
	return o, nil
}

// clampWScale は shift を RFC 7323 の上限 14 にクランプする。
func clampWScale(s uint8) uint8 {
	if s > maxWScale {
		return maxWScale
	}
	return s
}

// Marshal はセットされたフィールドをワイヤ形式へ並べ、
// TCP ヘッダの 4 バイト境界に合わせて NOP/EOL でパディングする。
// どの option を出すか (SYN 用 / 通常用) の出し分けは呼び出し側に委ねる。
func (o TCPOptions) Marshal() []byte {
	var b []byte
	if o.HasMSS {
		b = append(b, optMSS, 4, byte(o.MSS>>8), byte(o.MSS))
	}
	if o.HasWScale {
		b = append(b, optWScale, 3, clampWScale(o.WindowScale))
	}
	if o.SACKPermitted {
		b = append(b, optSACKPerm, 2)
	}
	if o.HasTimestamp {
		b = append(b, optTimestamp, 10)
		b = appendBe32(b, o.TSVal)
		b = appendBe32(b, o.TSecr)
	}
	if n := len(o.SACKBlocks); n > 0 {
		b = append(b, optSACK, byte(2+n*8))
		for _, blk := range o.SACKBlocks {
			b = appendBe32(b, blk[0])
			b = appendBe32(b, blk[1])
		}
	}
	// 4 バイト境界へパディング。末尾は NOP、最後の 1 つだけ EOL でも良いが、
	// NOP 埋め + 末尾 EOL が一般的。ここでは NOP で埋め、領域を 4 の倍数に揃える。
	for len(b)%4 != 0 {
		b = append(b, optNOP)
	}
	return b
}

func appendBe32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// DataOffsetForOptions はオプション領域長 (バイト) から TCP ヘッダの
// DataOffset 値 (32bit ワード数) を返す。固定ヘッダ 20 バイト = 5 ワードに
// オプションを 4 バイト単位で切り上げて加算する。
func DataOffsetForOptions(optLen int) uint8 {
	return uint8(5 + (optLen+3)/4)
}

package tcp

import "errors"

// Framer はリンク層から来るバイト列を IPv4 パケット境界で再分割する。
//
// TCP/IP はストリーム的に届くため 1 read = 1 パケットの保証はない。
// 部分読み (1 パケットが複数 Push に跨る)・連結到着 (複数パケットが 1 Push)・
// その混在を、IPv4 TotalLength を境界として正しく分割する。
type Framer struct {
	buf       []byte // 未確定 (パケット境界未満) の蓄積バイト
	maxPacket int    // 1 パケットの許容上限。これを超える宣言長/蓄積は接続エラー
}

const (
	ipv4MinHeader = 20 // オプション無し IPv4 ヘッダ長。TotalLength の下限でもある
)

var (
	errFrameVersion = errors.New("framing: not IPv4")
	errFrameIHL     = errors.New("framing: IHL out of range")
	errFrameTooLong = errors.New("framing: declared length exceeds maxPacket")
	errFrameBufFull = errors.New("framing: buffered bytes exceed maxPacket")
)

// NewFramer は 1 パケット上限 maxPacket バイトの再分割器を作る。
// maxPacket は IPv4 の理論上限 65535 など、運用 MTU に応じた値を渡す。
func NewFramer(maxPacket int) *Framer {
	return &Framer{maxPacket: maxPacket}
}

// Push は受信チャンクをバッファに追加し、完成した IPv4 パケット
// (ヘッダ+ペイロードの生バイト列) を到着順に返す。
//
// 1 パケットに満たない端数は次の Push まで保持する (部分読み)。
// 連結到着していれば 1 回の Push で複数パケットを返す。
//
// trust boundary: TotalLength を信じて事前確保しない。version/IHL 不正、
// maxPacket 超の宣言長、上限超のバッファ肥大はいずれも接続エラーとして返す。
// エラー時、それまでに切り出せたパケットは packets として返す。
func (f *Framer) Push(chunk []byte) (packets [][]byte, err error) {
	f.buf = append(f.buf, chunk...)

	for {
		// ヘッダ先頭 4 バイト (version/IHL + TotalLength) が揃うまで待つ。
		if len(f.buf) < 4 {
			break
		}
		if f.buf[0]>>4 != 4 {
			return packets, errFrameVersion
		}
		if f.buf[0]&0x0F < 5 {
			return packets, errFrameIHL
		}
		total := int(uint16(f.buf[2])<<8 | uint16(f.buf[3]))
		if total < ipv4MinHeader {
			return packets, errFrameIHL // ヘッダ未満の宣言長は不正
		}
		if total > f.maxPacket {
			return packets, errFrameTooLong // 過剰確保せず即拒否
		}
		if len(f.buf) < total {
			// パケット 1 つ分に満たない: 残りを保持して次の Push を待つ。
			// ここで蓄積が上限を超えていたら、来ない続きを待ち続けないよう拒否。
			if len(f.buf) > f.maxPacket {
				return packets, errFrameBufFull
			}
			break
		}
		pkt := make([]byte, total)
		copy(pkt, f.buf[:total])
		packets = append(packets, pkt)
		f.buf = f.buf[total:]
	}

	// 切り詰めた残りを詰め直し、参照を解放してバッファ肥大を防ぐ。
	if len(f.buf) == 0 {
		f.buf = nil
	} else {
		f.buf = append([]byte(nil), f.buf...)
	}
	return packets, nil
}

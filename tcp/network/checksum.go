package network

// インターネットチェックサム (RFC 1071 / RFC 9293 §3.1)。
// 16bit ワードの ones' complement sum の ones' complement。

// Checksum は data の 16bit ワード和 (end-around carry 畳み込み) の補数を返す。
// 奇数長は末尾を 0 パディングして 16bit に整える (計算のみ。パッドは送信しない)。
func Checksum(data []byte) uint16 {
	var sum uint32
	n := len(data)
	for i := 0; i+1 < n; i += 2 {
		sum += uint32(data[i])<<8 | uint32(data[i+1])
	}
	if n%2 == 1 { // 末尾 1 バイトは下位を 0 パディング
		sum += uint32(data[n-1]) << 8
	}
	// end-around carry: 上位 16bit を下位へ畳み込む (2 回で必ず収束)。
	for sum>>16 != 0 {
		sum = (sum & 0xFFFF) + (sum >> 16)
	}
	return ^uint16(sum)
}

// TCPChecksum は IPv4 擬似ヘッダ (src/dst/zero/proto=6/tcpLen) 込みで
// TCP セグメントのチェックサムを計算する。
// tcpSegment はチェックサムフィールドを 0 にした状態で渡す。
func TCPChecksum(srcIP, dstIP [4]byte, tcpSegment []byte) uint16 {
	const protoTCP = 6
	// 擬似ヘッダ 12 バイト + セグメントを連結して計算する。
	buf := make([]byte, 12+len(tcpSegment))
	copy(buf[0:4], srcIP[:])
	copy(buf[4:8], dstIP[:])
	buf[8] = 0
	buf[9] = protoTCP
	PutBe16(buf, 10, uint16(len(tcpSegment)))
	copy(buf[12:], tcpSegment)
	return Checksum(buf)
}

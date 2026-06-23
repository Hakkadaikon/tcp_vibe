package tcp

// TCP シーケンス番号は 32bit 環状空間 (modulo 2^32) で比較する (RFC 9293 §3.4)。
// 比較は RFC 1982 風: a < b は「b-a の wrap 差が 0 でなく半周 (2^31) 未満」。
// uint32 の自然なオーバーフロー減算がそのまま modulo 2^32 になる。

const halfSeqSpace uint32 = 1 << 31 // 2^31

// SeqLT は a < b を環状空間で判定する。
func SeqLT(a, b uint32) bool {
	d := b - a // wrap 減算 = (b-a) mod 2^32
	return d != 0 && d < halfSeqSpace
}

// SeqLEQ は a <= b。
func SeqLEQ(a, b uint32) bool {
	return a == b || SeqLT(a, b)
}

// SeqGT は a > b。
func SeqGT(a, b uint32) bool {
	return SeqLT(b, a)
}

// SeqGEQ は a >= b。
func SeqGEQ(a, b uint32) bool {
	return a == b || SeqLT(b, a)
}

// SeqAdd は (a + b) mod 2^32。uint32 の自然なラップ。
func SeqAdd(a, b uint32) uint32 {
	return a + b
}

// AcceptableAck は SND.UNA < SEG.ACK =< SND.NXT を環状空間で判定する (RFC 9293 R-011)。
func AcceptableAck(una, ack, nxt uint32) bool {
	return SeqLT(una, ack) && SeqLEQ(ack, nxt)
}

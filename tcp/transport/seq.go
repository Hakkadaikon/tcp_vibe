package transport

// TCP シーケンス番号は 32bit 環状空間 (modulo 2^32) で比較する (RFC 9293 §3.4)。
// 比較は RFC 1982 風: a < b は「b-a の wrap 差が 0 でなく半周 (2^31) 未満」。
// uint32 の自然なオーバーフロー減算がそのまま modulo 2^32 になる。
//
// 注意: 環状ゆえ順序は無条件には全順序にならない。対蹠点 (a-b が ちょうど 2^31)
// では本実装は両方向 false となり順序が定まらない (RFC 9293:876 の
// "subtleties to computer modulo arithmetic")。TCP はこれを「窓幅を 2^31 未満に
// 保つ」不変条件で回避する。本実装の受信窓は window scale 後でも最大 2^30 で
// 2^31 を下回るため常に安全 (TCB.inWindow がこの前提を満たす)。

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

// AcceptableAck は SND.UNA < SEG.ACK =< SND.NXT を環状空間で判定する (RFC 9293)。
func AcceptableAck(una, ack, nxt uint32) bool {
	return SeqLT(una, ack) && SeqLEQ(ack, nxt)
}

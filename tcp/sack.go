package tcp

// SACK ブロック生成 (受信側、RFC 2018)。oooSegs (順番待ちの先行セグメント) を
// 連続領域ごとに [left, right) のブロックへまとめ、ACK に広告する。
//
// ponytail: SACK ブロックの生成と広告まで。送信側の選択再送 (受信した SACK で
// SACKed 範囲をスキップする再送) は未実装、必要なら再送ロジック (checkRetransmit)
// に足す。受信側が正しく広告できれば相手 (本物の TCP 等) は活用できる。

// maxSackBlocks は SACK option に載せる最大ブロック数。TS 併用時は option 領域
// (40 バイト) が TS(10) で削られるため 3、無しなら 4 (RFC 2018 §3)。
const maxSackBlocks = 4

// sackBlocks は oooSegs の連続領域を [left, right) のブロック列にして返す。
// 先頭は最新受信 (lastOooSeq) を含むブロック (RFC 2018 §4)。最大 max 個まで。
func (c *Conn) sackBlocks(max int) [][2]uint32 {
	if len(c.tcb.oooSegs) == 0 || max <= 0 {
		return nil
	}
	// oooSegs は seq 昇順。隣接 (left == 直前の right) をマージして領域化する。
	var blocks [][2]uint32
	for _, s := range c.tcb.oooSegs {
		left := s.seq
		right := s.seq + uint32(len(s.data))
		if n := len(blocks); n > 0 && blocks[n-1][1] == left {
			blocks[n-1][1] = right
			continue
		}
		blocks = append(blocks, [2]uint32{left, right})
	}
	// 最新受信を含むブロックを先頭へ。lastOooSeq が [left,right) に入るものを探す。
	for i, b := range blocks {
		if SeqLEQ(b[0], c.tcb.lastOooSeq) && SeqLT(c.tcb.lastOooSeq, b[1]) {
			if i != 0 {
				blocks[0], blocks[i] = blocks[i], blocks[0]
			}
			break
		}
	}
	if len(blocks) > max {
		blocks = blocks[:max]
	}
	return blocks
}

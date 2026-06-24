package transport

// PAWS (Protect Against Wrapped Sequences) と TS.Recent 更新 (RFC 7323 §4-5)。
// timestamp は seq と同じ 32bit 環状空間で比較する (RFC 7323 §5.2 が RFC 1982 の
// serial number arithmetic を再利用すると規定)。よって SeqLT/SeqLEQ を流用する。

// pawsStale は SEG.TSval が TS.Recent より厳密に古いか (= 受理不可) を返す。
// 等値・新しいものは古くない。RST はこのチェックの対象外 (呼び出し側で除外)。
func pawsStale(tsval, recent uint32) bool {
	return SeqLT(tsval, recent)
}

// tsRecentUpdate は TS.Recent の更新後の値を返す (RFC 7323 §4.3)。
// SEG.TSval >= TS.Recent (環状) かつ SEG.SEQ <= Last.ACK.sent のときだけ tsval へ
// 更新し、そうでなければ recent を据え置く。この規則により TS.Recent は環状順序で
// 後退しない (単調)。
func tsRecentUpdate(recent, tsval, seq, lastAckSent uint32) uint32 {
	if !SeqLT(tsval, recent) && SeqLEQ(seq, lastAckSent) {
		return tsval
	}
	return recent
}

// tsNow は送信に載せる timestamp clock 値を返す。clock seam の現在時刻を
// ミリ秒で取り uint32 に丸めた単調増加の擬似クロック (RFC 7323 §5.4 は単調性のみ要求)。
func (t *TCB) tsNow() uint32 {
	return uint32(t.clock().UnixMilli())
}

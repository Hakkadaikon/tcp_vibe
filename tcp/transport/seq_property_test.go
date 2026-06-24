package transport

import (
	"testing"
	"testing/quick"
)

// SeqLT の半窓内健全性を testing/quick で橋渡しする。窓を 2^31 未満に保つ限り
// SeqLT は環状空間で正しい順序判定になり、PAWS / acceptable-ack の保証が効く。

// 半窓内一致: 距離が半窓 (2^31) 未満の a,b について、SeqLT(a,b) は
// 「b-a が 0 でなく 2^31 未満」と一致する。すなわち a から見て b が前方
// 半窓内にあるかどうかの判定として正しい。窓をこの範囲に保つ限り順序は確定する。
func TestSeqLTHalfWindowSoundProperty(t *testing.T) {
	const half uint32 = 1 << 31
	f := func(a, gap uint32) bool {
		d := gap % half // 0..2^31-1 の前方距離
		b := a + d
		want := d != 0 && d < half // 半窓内で a より真に前方
		return SeqLT(a, b) == want
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

// 半窓内では SeqLT が全順序の片側を一意に与える: 距離 0<d<2^31 なら
// SeqLT(a,a+d) が真かつ SeqLT(a+d,a) が偽 (逆向きは半窓を超えるため false)。
// 対蹠点 (d==2^31) を踏まない限り曖昧さは生じない。
func TestSeqLTDirectionalWithinHalfWindowProperty(t *testing.T) {
	const half uint32 = 1 << 31
	f := func(a, gap uint32) bool {
		d := gap%(half-1) + 1 // 1..2^31-1
		b := a + d
		return SeqLT(a, b) && !SeqLT(b, a)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 5000}); err != nil {
		t.Error(err)
	}
}

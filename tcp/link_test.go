package tcp

import (
	"bytes"
	"errors"
	"testing"
)

// メモリ仮想リンク: 一方に書いたパケットが他方で読める。
func TestPipeLink_Basic(t *testing.T) {
	a, b := NewPipeLink()
	pkt := []byte{0x45, 0x00, 0x00, 0x14, 1, 2, 3, 4}
	if err := a.WritePacket(pkt); err != nil {
		t.Fatal(err)
	}
	got, err := b.ReadPacket()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pkt) {
		t.Errorf("got %x want %x", got, pkt)
	}
}

// パケット境界が保たれる(複数書き込みが混ざらない)。
func TestPipeLink_PreservesBoundaries(t *testing.T) {
	a, b := NewPipeLink()
	p1 := []byte{1, 1, 1}
	p2 := []byte{2, 2}
	a.WritePacket(p1)
	a.WritePacket(p2)
	g1, _ := b.ReadPacket()
	g2, _ := b.ReadPacket()
	if !bytes.Equal(g1, p1) || !bytes.Equal(g2, p2) {
		t.Errorf("boundary lost: %x %x", g1, g2)
	}
}

// Close 後の Read は EOF 相当のエラー。
func TestPipeLink_CloseEOF(t *testing.T) {
	a, b := NewPipeLink()
	a.Close()
	if _, err := b.ReadPacket(); !errors.Is(err, ErrLinkClosed) {
		t.Errorf("expected ErrLinkClosed, got %v", err)
	}
}

// Close 後の Write もエラー。
func TestPipeLink_WriteAfterClose(t *testing.T) {
	a, _ := NewPipeLink()
	a.Close()
	if err := a.WritePacket([]byte{1}); !errors.Is(err, ErrLinkClosed) {
		t.Errorf("expected ErrLinkClosed, got %v", err)
	}
}

// 書いたパケットのコピーが渡る(呼び出し側のバッファ再利用で壊れない)。
func TestPipeLink_CopiesPayload(t *testing.T) {
	a, b := NewPipeLink()
	buf := []byte{9, 9, 9}
	a.WritePacket(buf)
	buf[0] = 0 // 書き込み後に元バッファを書き換え
	got, _ := b.ReadPacket()
	if got[0] != 9 {
		t.Errorf("payload not copied: got %x", got)
	}
}

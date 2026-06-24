package tcp

import (
	"bytes"
	"testing"
	"testing/quick"
)

// 既知の TCP ヘッダバイト列を Parse して各フィールドが一致する (GOLD)。
func TestParseTCPHeader_Gold(t *testing.T) {
	// src=0x3039(12345) dst=0x0050(80) seq=0x01020304 ack=0x05060708
	// dataOffset=5, flags=SYN|ACK (0x12), window=0x7210, checksum=0, urg=0
	raw := []byte{
		0x30, 0x39, 0x00, 0x50,
		0x01, 0x02, 0x03, 0x04,
		0x05, 0x06, 0x07, 0x08,
		0x50, 0x12, 0x72, 0x10,
		0x00, 0x00, 0x00, 0x00,
	}
	h, err := ParseTCPHeader(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h.SrcPort != 12345 || h.DstPort != 80 {
		t.Errorf("ports: %+v", h)
	}
	if h.SeqNum != 0x01020304 || h.AckNum != 0x05060708 {
		t.Errorf("seq/ack: %+v", h)
	}
	if h.DataOffset != 5 || h.Window != 0x7210 {
		t.Errorf("dataoffset/window: %+v", h)
	}
	if !h.Flags.Has(FlagSYN) || !h.Flags.Has(FlagACK) {
		t.Errorf("flags should be SYN|ACK: %#02x", uint8(h.Flags))
	}
	if h.Flags.Has(FlagFIN) || h.Flags.Has(FlagRST) {
		t.Errorf("unexpected extra flags: %#02x", uint8(h.Flags))
	}
}

// Marshal してから Parse すると元に戻る。
func TestTCPHeader_RoundTrip(t *testing.T) {
	f := func(sp, dp uint16, seq, ack uint32, flags uint8, win, urg uint16) bool {
		h := TCPHeader{
			SrcPort: sp, DstPort: dp, SeqNum: seq, AckNum: ack,
			DataOffset: 5, Flags: Flags(flags & 0x3F), Window: win, UrgentPtr: urg,
		}
		got, err := ParseTCPHeader(h.Marshal())
		if err != nil {
			return false
		}
		return headerEqual(got, h)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// headerEqual はオプション生バイト列込みでヘッダ等価を判定する
// (Options は slice なので == では比べられないため)。
func headerEqual(a, b TCPHeader) bool {
	return a.SrcPort == b.SrcPort && a.DstPort == b.DstPort &&
		a.SeqNum == b.SeqNum && a.AckNum == b.AckNum &&
		a.DataOffset == b.DataOffset && a.Flags == b.Flags &&
		a.Window == b.Window && a.Checksum == b.Checksum &&
		a.UrgentPtr == b.UrgentPtr && bytes.Equal(a.Options, b.Options)
}

// Marshal はオプション生バイトを載せ、Parse で同じ列が復元される。
func TestTCPHeader_OptionsRoundTrip(t *testing.T) {
	opts := TCPOptions{HasMSS: true, MSS: 1460, HasWScale: true, WindowScale: 7}.Marshal()
	h := TCPHeader{
		SrcPort: 1, DstPort: 2, SeqNum: 3, AckNum: 4,
		Flags: Flags(FlagSYN), Window: 65535, Options: opts,
	}
	got, err := ParseTCPHeader(h.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Options, opts) {
		t.Fatalf("options not preserved: got %v want %v", got.Options, opts)
	}
	o, err := ParseTCPOptions(got.Options)
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasMSS || o.MSS != 1460 || !o.HasWScale || o.WindowScale != 7 {
		t.Fatalf("parsed options wrong: %+v", o)
	}
}

// control bit 個別保存 (各フラグ単独 / 全部 / 組合せ)。
func TestTCPHeader_ControlBits(t *testing.T) {
	all := []Flags{FlagFIN, FlagSYN, FlagRST, FlagPSH, FlagACK, FlagURG}
	// 各フラグ単独。
	for _, fl := range all {
		h := TCPHeader{DataOffset: 5, Flags: fl}
		got, _ := ParseTCPHeader(h.Marshal())
		if got.Flags != fl {
			t.Errorf("single flag %#02x not preserved, got %#02x", uint8(fl), uint8(got.Flags))
		}
	}
	// 全部 ON。
	var every Flags
	for _, fl := range all {
		every |= fl
	}
	h := TCPHeader{DataOffset: 5, Flags: every}
	if got, _ := ParseTCPHeader(h.Marshal()); got.Flags != every {
		t.Errorf("all flags not preserved: %#02x", uint8(got.Flags))
	}
	// 任意の 6bit 組合せが保存される。
	f := func(bits uint8) bool {
		fl := Flags(bits & 0x3F)
		hh := TCPHeader{DataOffset: 5, Flags: fl}
		got, err := ParseTCPHeader(hh.Marshal())
		return err == nil && got.Flags == fl
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Error(err)
	}
}

// data offset 境界 (5=20byte / 6=オプション付き / <5 はエラー)。
func TestParseTCPHeader_DataOffsetBoundary(t *testing.T) {
	// DataOffset=6 → 24 バイトヘッダ (オプション 4 バイト)。
	raw := make([]byte, 24)
	raw[12] = 6 << 4
	h, err := ParseTCPHeader(raw)
	if err != nil {
		t.Fatalf("dataOffset=6 should parse: %v", err)
	}
	if h.DataOffset != 6 {
		t.Errorf("dataOffset=6 expected, got %d", h.DataOffset)
	}
	// DataOffset<5 は不正。
	bad := make([]byte, 20)
	bad[12] = 4 << 4
	if _, err := ParseTCPHeader(bad); err == nil {
		t.Error("dataOffset<5 must be rejected")
	}
}

// short read 拒否 (20byte OK / 19byte エラー)。宣言長 > 実バッファもエラー。
func TestParseTCPHeader_ShortRead(t *testing.T) {
	ok := make([]byte, 20)
	ok[12] = 5 << 4
	if _, err := ParseTCPHeader(ok); err != nil {
		t.Fatalf("20 bytes should parse: %v", err)
	}
	if _, err := ParseTCPHeader(ok[:19]); err == nil {
		t.Error("19 bytes must be rejected")
	}
	// dataOffset=6 (24byte 宣言) なのに 20byte → 拒否 (過剰確保しない)。
	short := make([]byte, 20)
	short[12] = 6 << 4
	if _, err := ParseTCPHeader(short); err == nil {
		t.Error("declared dataOffset longer than buffer must be rejected")
	}
}

// Flags.String は立っているビットを記号で連結し、無印は "-" を返す。
func TestFlagsString(t *testing.T) {
	cases := []struct {
		in   Flags
		want string
	}{
		{0, "-"},
		{FlagSYN, "SYN"},
		{FlagSYN | FlagACK, "SYN|ACK"},
		{FlagFIN, "FIN"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("Flags(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

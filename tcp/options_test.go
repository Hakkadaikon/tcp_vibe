package tcp

import (
	"reflect"
	"testing"
	"testing/quick"
)

// --- 個別 option の parse ---

func TestParseMSSOnly(t *testing.T) {
	// MSS=1460 → [2][4][0x05][0xB4] (GOLD)
	o, err := ParseTCPOptions([]byte{2, 4, 0x05, 0xB4})
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasMSS || o.MSS != 1460 {
		t.Fatalf("MSS got HasMSS=%v MSS=%d, want true 1460", o.HasMSS, o.MSS)
	}
}

func TestParseWScaleOnly(t *testing.T) {
	o, err := ParseTCPOptions([]byte{3, 3, 7})
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasWScale || o.WindowScale != 7 {
		t.Fatalf("got HasWScale=%v shift=%d, want true 7", o.HasWScale, o.WindowScale)
	}
}

func TestParseWScaleClamp(t *testing.T) {
	for _, tc := range []struct{ in, want uint8 }{{0, 0}, {14, 14}, {15, 14}, {255, 14}} {
		o, err := ParseTCPOptions([]byte{3, 3, tc.in})
		if err != nil {
			t.Fatal(err)
		}
		if o.WindowScale != tc.want {
			t.Fatalf("shift=%d clamped to %d, want %d", tc.in, o.WindowScale, tc.want)
		}
	}
}

func TestParseSACKPermitted(t *testing.T) {
	o, err := ParseTCPOptions([]byte{4, 2})
	if err != nil {
		t.Fatal(err)
	}
	if !o.SACKPermitted {
		t.Fatal("want SACKPermitted")
	}
}

func TestParseTimestamps(t *testing.T) {
	// TSval=0x01020304, TSecr=0x05060708
	o, err := ParseTCPOptions([]byte{8, 10, 1, 2, 3, 4, 5, 6, 7, 8})
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasTimestamp || o.TSVal != 0x01020304 || o.TSecr != 0x05060708 {
		t.Fatalf("ts got %v %#x %#x", o.HasTimestamp, o.TSVal, o.TSecr)
	}
}

func TestParseSACKBlocks(t *testing.T) {
	// 2 ブロック: len=2+8*2=18
	b := []byte{5, 18,
		0, 0, 0, 1, 0, 0, 0, 2, // [1,2)
		0, 0, 0, 5, 0, 0, 0, 9, // [5,9)
	}
	o, err := ParseTCPOptions(b)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]uint32{{1, 2}, {5, 9}}
	if !reflect.DeepEqual(o.SACKBlocks, want) {
		t.Fatalf("SACK got %v want %v", o.SACKBlocks, want)
	}
}

// --- 構造系: EOL / NOP / 未知 kind / 空 ---

func TestParseEOLTerminates(t *testing.T) {
	// EOL の後ろはゼロパディング扱いで無視。NOP, MSS, EOL, ゴミ
	o, err := ParseTCPOptions([]byte{1, 2, 4, 0x05, 0xB4, 0, 99, 99, 99})
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasMSS || o.MSS != 1460 {
		t.Fatalf("got %+v", o)
	}
}

func TestParseNOPPadding(t *testing.T) {
	o, err := ParseTCPOptions([]byte{1, 1, 3, 3, 7})
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasWScale || o.WindowScale != 7 {
		t.Fatalf("got %+v", o)
	}
}

func TestParseUnknownKindSkipped(t *testing.T) {
	// 未知 kind 99, len 4, value 2 バイト → 読み飛ばし、後続 MSS を読む
	o, err := ParseTCPOptions([]byte{99, 4, 0xAA, 0xBB, 2, 4, 0x05, 0xB4})
	if err != nil {
		t.Fatal(err)
	}
	if !o.HasMSS || o.MSS != 1460 {
		t.Fatalf("got %+v", o)
	}
}

func TestParseEmpty(t *testing.T) {
	o, err := ParseTCPOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o, TCPOptions{}) {
		t.Fatalf("want zero, got %+v", o)
	}
}

func TestParseAllZeroPadding(t *testing.T) {
	o, err := ParseTCPOptions([]byte{0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o, TCPOptions{}) {
		t.Fatalf("want zero, got %+v", o)
	}
}

// --- trust boundary: 不正 length ---

func TestParseLengthZero(t *testing.T) {
	if _, err := ParseTCPOptions([]byte{2, 0, 0, 0}); err == nil {
		t.Fatal("len=0 must error")
	}
}

func TestParseLengthOverflow(t *testing.T) {
	// MSS と称して len=4 だが value が足りない
	if _, err := ParseTCPOptions([]byte{2, 4, 0x05}); err == nil {
		t.Fatal("truncated value must error")
	}
}

func TestParseLengthBelowMin(t *testing.T) {
	// length < 2 (kind+len 自身に満たない) は不正
	if _, err := ParseTCPOptions([]byte{2, 1, 0, 0}); err == nil {
		t.Fatal("len<2 must error")
	}
}

func TestParseKindWithoutLength(t *testing.T) {
	// kind=2 (要 length) だが length バイトが無い
	if _, err := ParseTCPOptions([]byte{2}); err == nil {
		t.Fatal("missing length byte must error")
	}
}

func TestParseWrongFixedLength(t *testing.T) {
	// MSS の length が 4 でない
	if _, err := ParseTCPOptions([]byte{2, 5, 0, 0, 0}); err == nil {
		t.Fatal("MSS len!=4 must error")
	}
	// SACK len が 8n+2 でない
	if _, err := ParseTCPOptions([]byte{5, 9, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Fatal("SACK len!=8n+2 must error")
	}
}

// --- marshal GOLD ---

func TestMarshalMSSGold(t *testing.T) {
	o := TCPOptions{HasMSS: true, MSS: 1460}
	got := o.Marshal()
	want := []byte{2, 4, 0x05, 0xB4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMarshalTimestampsGold(t *testing.T) {
	o := TCPOptions{HasTimestamp: true, TSVal: 0x01020304, TSecr: 0x05060708}
	got := o.Marshal()
	// TS(10) を 4 境界へ末尾 NOP 2 つで 12 バイト
	want := []byte{8, 10, 1, 2, 3, 4, 5, 6, 7, 8, 1, 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestMarshalPaddedTo4(t *testing.T) {
	o := TCPOptions{HasWScale: true, WindowScale: 7}
	got := o.Marshal()
	if len(got)%4 != 0 {
		t.Fatalf("len %d not multiple of 4: %v", len(got), got)
	}
}

func TestMarshalEmpty(t *testing.T) {
	if got := (TCPOptions{}).Marshal(); len(got) != 0 {
		t.Fatalf("empty marshal want 0 len, got %v", got)
	}
}

// --- DataOffset ヘルパ ---

func TestDataOffsetForOptions(t *testing.T) {
	for _, tc := range []struct {
		optlen int
		want   uint8
	}{{0, 5}, {1, 6}, {4, 6}, {5, 7}, {40, 15}} {
		if got := DataOffsetForOptions(tc.optlen); got != tc.want {
			t.Fatalf("optlen=%d got %d want %d", tc.optlen, got, tc.want)
		}
	}
}

// --- 往復 property ---

func TestRoundTripProperty(t *testing.T) {
	f := func(seed uint32, hasMSS, hasWS, hasTS, sackPerm bool, nblocks uint8) bool {
		o := TCPOptions{}
		if hasMSS {
			o.HasMSS = true
			o.MSS = uint16(seed)
		}
		if hasWS {
			o.HasWScale = true
			o.WindowScale = uint8(seed % 15) // 0..14 妥当域
		}
		if hasTS {
			o.HasTimestamp = true
			o.TSVal = seed
			o.TSecr = seed * 2
		}
		o.SACKPermitted = sackPerm
		n := int(nblocks % 5) // 0..4 ブロック
		for i := 0; i < n; i++ {
			l := seed + uint32(i)*10
			o.SACKBlocks = append(o.SACKBlocks, [2]uint32{l, l + 5})
		}
		got, err := ParseTCPOptions(o.Marshal())
		if err != nil {
			t.Logf("parse error: %v for %+v", err, o)
			return false
		}
		return reflect.DeepEqual(got, o)
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Fatal(err)
	}
}

package tcp

import (
	"testing"
	"testing/quick"
)

// 既知の IPv4 ヘッダバイト列を Parse して各フィールドが一致する (GOLD)。
func TestParseIPv4Header_Gold(t *testing.T) {
	// version=4 IHL=5, TotalLength=0x0073, proto=17(UDP), src=192.168.0.1 dst=192.168.0.199
	raw := []byte{
		0x45, 0x00, 0x00, 0x73, 0x00, 0x00, 0x40, 0x00,
		0x40, 0x11, 0xb8, 0x61, 0xc0, 0xa8, 0x00, 0x01,
		0xc0, 0xa8, 0x00, 0xc7,
	}
	h, err := ParseIPv4Header(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 4 || h.IHL != 5 || h.TotalLength != 0x0073 || h.Protocol != 17 {
		t.Errorf("unexpected fields: %+v", h)
	}
	if h.SrcAddr != [4]byte{192, 168, 0, 1} || h.DstAddr != [4]byte{192, 168, 0, 199} {
		t.Errorf("addr mismatch: %+v", h)
	}
}

// Marshal してから Parse すると元に戻る。
func TestIPv4Header_RoundTrip(t *testing.T) {
	f := func(totalLen uint16, proto uint8, id uint16, ttl uint8, sa, da uint32) bool {
		h := IPv4Header{
			Version: 4, IHL: 5,
			TotalLength: totalLen, ID: id, TTL: ttl, Protocol: proto,
		}
		putBe32(h.SrcAddr[:], 0, sa)
		putBe32(h.DstAddr[:], 0, da)
		got, err := ParseIPv4Header(h.Marshal())
		if err != nil {
			return false
		}
		return got.Version == h.Version && got.IHL == h.IHL &&
			got.TotalLength == h.TotalLength && got.ID == h.ID &&
			got.TTL == h.TTL && got.Protocol == h.Protocol &&
			got.SrcAddr == h.SrcAddr && got.DstAddr == h.DstAddr
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 2000}); err != nil {
		t.Error(err)
	}
}

// Marshal がヘッダチェックサムを正しく埋め、Parse が検証できる。
func TestIPv4Header_ChecksumVerified(t *testing.T) {
	h := IPv4Header{Version: 4, IHL: 5, TotalLength: 40, TTL: 64, Protocol: 6,
		SrcAddr: [4]byte{10, 0, 0, 1}, DstAddr: [4]byte{10, 0, 0, 2}}
	b := h.Marshal()
	if Checksum(b[:20]) != 0 {
		t.Errorf("marshaled header checksum must verify to 0, got %#04x", Checksum(b[:20]))
	}
	// 1bit 壊すと Parse がチェックサムエラーで弾く。
	b[0] ^= 0x01
	if _, err := ParseIPv4Header(b); err == nil {
		t.Error("corrupted header should be rejected by checksum")
	}
}

// IHL 境界 (5=20byte / 6=オプション付き / <5 はエラー)。
func TestParseIPv4Header_IHLBoundary(t *testing.T) {
	// IHL=6: 24 バイトヘッダ。
	raw := make([]byte, 24)
	raw[0] = 0x46 // version=4 IHL=6
	putBe16(raw, 2, 24)
	raw[9] = 6
	putBe16(raw, 10, Checksum(raw[:24]))
	h, err := ParseIPv4Header(raw)
	if err != nil {
		t.Fatalf("IHL=6 should parse: %v", err)
	}
	if h.IHL != 6 {
		t.Errorf("IHL=6 expected, got %d", h.IHL)
	}
	// IHL<5 は不正。
	bad := make([]byte, 20)
	bad[0] = 0x44 // IHL=4
	if _, err := ParseIPv4Header(bad); err == nil {
		t.Error("IHL<5 must be rejected")
	}
}

// tcpSegment は TotalLength で切り詰め、末尾パディングを除く。範囲外は弾く。
func TestTCPSegmentTruncatesAndValidates(t *testing.T) {
	// IP(20) + TCP(20) = 40 バイトの正当パケットに 6 バイトのパディングを付ける。
	ip := IPv4Header{Version: 4, IHL: 5, Protocol: 6, TotalLength: 40, TTL: 64,
		SrcAddr: [4]byte{10, 0, 0, 1}, DstAddr: [4]byte{10, 0, 0, 2}}
	pkt := append(ip.Marshal(), make([]byte, 20)...) // TCP 部
	pkt = append(pkt, 1, 2, 3, 4, 5, 6)              // パディング

	seg, ok := tcpSegment(ip, pkt)
	if !ok {
		t.Fatal("正当な TotalLength が弾かれた")
	}
	if len(seg) != 20 {
		t.Fatalf("パディングが切り詰められていない: len=%d want 20", len(seg))
	}

	// TotalLength > 実バッファ → 弾く。
	bad := ip
	bad.TotalLength = uint16(len(pkt) + 10)
	if _, ok := tcpSegment(bad, pkt); ok {
		t.Fatal("TotalLength > バッファを弾かなかった")
	}

	// TotalLength < ヘッダ長 → 弾く。
	bad2 := ip
	bad2.TotalLength = 10
	if _, ok := tcpSegment(bad2, pkt); ok {
		t.Fatal("TotalLength < ヘッダ長を弾かなかった")
	}
}

// short read 拒否 (20byte OK / 19byte エラー)。宣言長 > 実バッファもエラー。
func TestParseIPv4Header_ShortRead(t *testing.T) {
	ok := make([]byte, 20)
	ok[0] = 0x45
	putBe16(ok, 10, Checksum(ok[:20]))
	if _, err := ParseIPv4Header(ok); err != nil {
		t.Fatalf("20 bytes should parse: %v", err)
	}
	if _, err := ParseIPv4Header(ok[:19]); err == nil {
		t.Error("19 bytes must be rejected")
	}
	// IHL=6 (24byte 宣言) なのに 20byte しか無い → 拒否 (過剰確保しない)。
	short := make([]byte, 20)
	short[0] = 0x46
	if _, err := ParseIPv4Header(short); err == nil {
		t.Error("declared IHL longer than buffer must be rejected")
	}
}

//go:build linux

package link

import "testing"

func TestParsePeer(t *testing.T) {
	ip, port, ok := parsePeer("PEER 127.0.0.1:54321")
	if !ok || ip != [4]byte{127, 0, 0, 1} || port != 54321 {
		t.Fatalf("parsePeer 失敗: ip=%v port=%d ok=%v", ip, port, ok)
	}
	if _, _, ok := parsePeer("REG foo"); ok {
		t.Fatal("PEER 以外を受理してはいけない")
	}
	if _, _, ok := parsePeer("PEER bad"); ok {
		t.Fatal("不正アドレスを受理してはいけない")
	}
}

func TestIsPunchPacket(t *testing.T) {
	if !isPunchPacket([]byte("PUNCH")) {
		t.Fatal("PUNCH を punch と認識すべき")
	}
	// IPv4 ヘッダ先頭 (Version=4, IHL=5 => 0x45) は punch と誤認しない。
	if isPunchPacket([]byte{0x45, 0x00, 0x00, 0x28}) {
		t.Fatal("IPv4 パケットを punch と誤認した")
	}
}

package tcp

import "github.com/hakkadaikon/tcp_vibe/tcp/link"

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import "testing"

// sackOpts は peer に届いた次セグメントの SACK ブロックを返す。
func sackOpts(t *testing.T, peer link.Link) (TCPOptions, bool) {
	t.Helper()
	pkt, err := peer.ReadPacket()
	if err != nil {
		return TCPOptions{}, false
	}
	ip, err := network.ParseIPv4Header(pkt)
	if err != nil {
		t.Fatalf("IP 解析失敗: %v", err)
	}
	seg := pkt[int(ip.IHL)*4:]
	h, err := ParseTCPHeader(seg)
	if err != nil {
		t.Fatalf("TCP 解析失敗: %v", err)
	}
	o, err := ParseTCPOptions(h.Options)
	if err != nil {
		t.Fatalf("option 解析失敗: %v", err)
	}
	return o, true
}

// 穴のある受信: seq が飛んだセグメントを先に受信すると、その ACK に SACK option が載り
// ブロックが連続領域 [left,right) を示す。最初のブロックは最新受信を含む。
func TestSackBlockOnHole(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	c.tcb.sackOK = true
	// RCV.NXT = 5001。seq 5001 を飛ばして 5101..5151 を受信 → 穴ができる。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5101, AckNum: 1001,
		Window: maxWindow}, make([]byte, 50))
	o, ok := sackOpts(t, peer)
	if !ok {
		t.Fatal("ACK が送られていない")
	}
	if len(o.SACKBlocks) != 1 {
		t.Fatalf("SACK ブロック数: got %d want 1", len(o.SACKBlocks))
	}
	if o.SACKBlocks[0] != [2]uint32{5101, 5151} {
		t.Fatalf("SACK ブロック不一致: got %v want [5101 5151)", o.SACKBlocks[0])
	}
}

// sackOK 未折衝なら SACK option を載せない。
func TestNoSackWhenNotNegotiated(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	// sackOK は false のまま。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5101, AckNum: 1001,
		Window: maxWindow}, make([]byte, 50))
	o, ok := sackOpts(t, peer)
	if !ok {
		t.Fatal("ACK が送られていない")
	}
	if len(o.SACKBlocks) != 0 {
		t.Fatalf("折衝していないのに SACK が載った: %v", o.SACKBlocks)
	}
}

// 複数の穴 → 複数ブロック。最初のブロックが最新受信を含む。
func TestSackMultipleBlocksNewestFirst(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	c.tcb.sackOK = true
	// RCV.NXT=5001。穴を 2 つ作る: [5101,5151) を先に, 次に [5201,5251)。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5101, AckNum: 1001,
		Window: maxWindow}, make([]byte, 50))
	sackOpts(t, peer) // 1 個目の ACK を読み捨て
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5201, AckNum: 1001,
		Window: maxWindow}, make([]byte, 50))
	o, ok := sackOpts(t, peer)
	if !ok {
		t.Fatal("ACK が送られていない")
	}
	if len(o.SACKBlocks) != 2 {
		t.Fatalf("SACK ブロック数: got %d want 2", len(o.SACKBlocks))
	}
	// 最初のブロックは最新受信 (5201..5251) を含む (RFC 2018)。
	if o.SACKBlocks[0] != [2]uint32{5201, 5251} {
		t.Fatalf("先頭ブロックが最新でない: got %v", o.SACKBlocks[0])
	}
	if o.SACKBlocks[1] != [2]uint32{5101, 5151} {
		t.Fatalf("2 番目ブロック不一致: got %v", o.SACKBlocks[1])
	}
}

// TS と SACK の併載: timestamp 折衝済みなら両方載り、option 長が正しい。
func TestSackWithTimestamp(t *testing.T) {
	c, peer, _ := establishedConn(t, maxWindow)
	c.tcb.sackOK = true
	c.tcb.tsOK = true
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5101, AckNum: 1001,
		Window: maxWindow, Options: (TCPOptions{HasTimestamp: true, TSVal: 7}).Marshal()},
		make([]byte, 50))
	o, ok := sackOpts(t, peer)
	if !ok {
		t.Fatal("ACK が送られていない")
	}
	if !o.HasTimestamp {
		t.Fatal("TS option が載っていない")
	}
	if len(o.SACKBlocks) != 1 || o.SACKBlocks[0] != [2]uint32{5101, 5151} {
		t.Fatalf("SACK ブロック不一致: got %v", o.SACKBlocks)
	}
}

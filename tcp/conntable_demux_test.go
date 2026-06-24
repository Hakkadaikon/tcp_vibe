package tcp

import (
	"testing"
	"time"
)

var (
	dxLocal  = [4]byte{10, 0, 0, 2} // Stack 側 (宛先)
	dxRemote = [4]byte{10, 0, 0, 1} // 相手 (送信元)
)

// buildSeg は src→dst の IPv4+TCP パケットを組む (チェックサム整合)。
func buildSeg(src, dst [4]byte, h TCPHeader, payload []byte) []byte {
	tcp := append(h.Marshal(), payload...)
	putBe16(tcp, 16, TCPChecksum(src, dst, tcp))
	ip := IPv4Header{Protocol: 6, TotalLength: uint16(20 + len(tcp)), SrcAddr: src, DstAddr: dst, TTL: 64}
	return append(ip.Marshal(), tcp...)
}

// readSegWithin は peer link から 1 パケット読み TCP ヘッダを返す。timeout で nil。
func readSegWithin(t *testing.T, peer Link, d time.Duration) *TCPHeader {
	t.Helper()
	type res struct {
		h  TCPHeader
		ok bool
	}
	ch := make(chan res, 1)
	go func() {
		pkt, err := peer.ReadPacket()
		if err != nil {
			ch <- res{}
			return
		}
		ip, _ := ParseIPv4Header(pkt)
		h, _ := ParseTCPHeader(pkt[int(ip.IHL)*4:])
		ch <- res{h, true}
	}()
	select {
	case r := <-ch:
		if !r.ok {
			return nil
		}
		return &r.h
	case <-time.After(d):
		return nil
	}
}

// 一致無し・非 RST → ちょうど 1 つの RST が返る (RFC 9293 §3.10.7.1)。
func TestDemuxNoMatchNonRstGeneratesOneRst(t *testing.T) {
	stackLink, peer := NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)

	// どの接続にも一致しない ACK を送る → RST 応答。
	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{Flags: Flags(FlagACK), SeqNum: 100, AckNum: 500, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	h := readSegWithin(t, peer, time.Second)
	if h == nil {
		t.Fatal("RST が返らない")
	}
	if !h.Flags.Has(FlagRST) {
		t.Fatalf("RST のはず: flags=%v", h.Flags)
	}
	// 2 つ目は来ない (ちょうど 1 つ)。
	if extra := readSegWithin(t, peer, 100*time.Millisecond); extra != nil {
		t.Fatalf("RST は 1 つのはず、2 つ目が来た: flags=%v", extra.Flags)
	}
}

// RST 入力にはRST を返さない (RST に RST を返さない)。
func TestDemuxRstInputNoRstBack(t *testing.T) {
	stackLink, peer := NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)

	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{Flags: Flags(FlagRST), SeqNum: 100, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	if h := readSegWithin(t, peer, 200*time.Millisecond); h != nil {
		t.Fatalf("RST 入力に応答してはいけない: flags=%v", h.Flags)
	}
}

// LISTEN のある port に SYN → 派生して SYN-ACK が返る。
func TestDemuxSynToListenDerives(t *testing.T) {
	stackLink, peer := NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagSYN), SeqNum: 5000, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	h := readSegWithin(t, peer, time.Second)
	if h == nil {
		t.Fatal("SYN-ACK が返らない")
	}
	if !h.Flags.Has(FlagSYN) || !h.Flags.Has(FlagACK) {
		t.Fatalf("SYN-ACK のはず: flags=%v", h.Flags)
	}
}

// TIME-WAIT の 4-tuple へ新 SYN → 置換され二重にならない (LISTEN 派生で新 incarnation)。
func TestDemuxTimeWaitReplacedBySyn(t *testing.T) {
	stackLink, peer := NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	remote := Endpoint{IP: dxRemote, Port: 1234}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	old, _ := s.table.insertIfAbsent(tp, func() *Conn {
		c := NewConn(stackLink, fc.Now, local, remote)
		c.tcb.state = TimeWait
		return c
	})

	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagSYN), SeqNum: 8000, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	// SYN-ACK が返る (新 incarnation が握手を始めた)。
	if h := readSegWithin(t, peer, time.Second); h == nil || !h.Flags.Has(FlagSYN) || !h.Flags.Has(FlagACK) {
		t.Fatalf("新 incarnation の SYN-ACK が返らない: %v", h)
	}
	// テーブルは新 Conn を指し、TIME-WAIT の古い Conn ではない (二重にならない)。
	if got := s.table.lookup(tp); got == old {
		t.Fatal("TIME-WAIT が置換されず古い Conn のまま")
	}
}

// broadcast/multicast/不正 src の SYN は破棄 (派生も RST もしない)。
func TestDemuxInvalidSrcSynDropped(t *testing.T) {
	stackLink, peer := NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	mcast := [4]byte{224, 0, 0, 5}
	pkt := buildSeg(mcast, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagSYN), SeqNum: 5000, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	if h := readSegWithin(t, peer, 200*time.Millisecond); h != nil {
		t.Fatalf("不正 src の SYN は破棄するはず、応答が来た: flags=%v", h.Flags)
	}
}

// 完全一致 TCB が LISTEN より優先される。LISTEN と同じ local port で確立済み
// 接続があるとき、その 4-tuple 宛のセグメントは派生ではなく既存接続へ届く。
func TestDemuxExactMatchBeatsListen(t *testing.T) {
	stackLink, peer := NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	// 既存接続を直接テーブルに置く (LISTEN と同 local port, 特定 remote)。
	remote := Endpoint{IP: dxRemote, Port: 1234}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	existing, _ := s.table.insertIfAbsent(tp, func() *Conn {
		c := NewConn(stackLink, fc.Now, local, remote)
		c.tcb.state = Established
		c.tcb.rcv.nxt = 5000
		c.tcb.snd.nxt = 7000
		c.tcb.snd.una = 7000
		c.tcb.rcv.wnd = defaultRcvWindow
		return c
	})

	// この 4-tuple 宛にデータ ACK を送る → 派生 (SYN-ACK) ではなく既存へ届く。
	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagACK), SeqNum: 5000, AckNum: 7000, DataOffset: 5, Window: maxWindow}, []byte("hi"))
	_ = peer.WritePacket(pkt)

	// 既存接続が RCV.NXT を進めれば届いた証拠。派生なら新 Conn ができ既存は不変。
	deadline := time.Now().Add(time.Second)
	for existing.RcvNxt() == 5000 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if existing.RcvNxt() != 5002 {
		t.Fatalf("完全一致 TCB にデータが届いていない: RcvNxt=%d want 5002", existing.RcvNxt())
	}
}

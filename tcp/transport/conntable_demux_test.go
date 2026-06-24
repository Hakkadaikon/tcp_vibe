package transport

import (
	"github.com/hakkadaikon/tcp_vibe/tcp/link"
	"github.com/hakkadaikon/tcp_vibe/tcp/network"
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
	network.PutBe16(tcp, 16, network.TCPChecksum(src, dst, tcp))
	ip := network.IPv4Header{Protocol: 6, TotalLength: uint16(20 + len(tcp)), SrcAddr: src, DstAddr: dst, TTL: 64}
	return append(ip.Marshal(), tcp...)
}

// readSegWithin は peer link から 1 パケット読み TCP ヘッダを返す。timeout で nil。
func readSegWithin(t *testing.T, peer link.Link, d time.Duration) *TCPHeader {
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
		ip, _ := network.ParseIPv4Header(pkt)
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
	stackLink, peer := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)

	// どの接続にも一致しない ACK を送る → RST 応答。demux を同期で叩けば、戻った時点で
	// 応答はすべて peer inbox に積まれており、個数を確定的に数えられる (実時間に依存しない)。
	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{Flags: Flags(FlagACK), SeqNum: 100, AckNum: 500, DataOffset: 5}, nil)
	s.demux(pkt)

	h, ok := drainPeerNonblockKeep(peer)
	if !ok {
		t.Fatal("RST が返らない")
	}
	if !h.Flags.Has(FlagRST) {
		t.Fatalf("RST のはず: flags=%v", h.Flags)
	}
	// 2 つ目は来ない (ちょうど 1 つ)。
	if extra, ok := drainPeerNonblockKeep(peer); ok {
		t.Fatalf("RST は 1 つのはず、2 つ目が来た: flags=%v", extra.Flags)
	}
}

// RST 入力にはRST を返さない (RST に RST を返さない)。
// 「応答が来ないこと」は実時間タイムアウトでなく demux を同期で叩いて確定的に判定する。
// demux が返った時点で同期応答はすべて peer inbox に積まれているので、空なら確実に無応答。
func TestDemuxRstInputNoRstBack(t *testing.T) {
	stackLink, peer := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)

	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{Flags: Flags(FlagRST), SeqNum: 100, DataOffset: 5}, nil)
	s.demux(pkt)

	if h, ok := drainPeerNonblockKeep(peer); ok {
		t.Fatalf("RST 入力に応答してはいけない: flags=%v", h.Flags)
	}
}

// LISTEN のある port に SYN → 派生して SYN-ACK が返る。
func TestDemuxSynToListenDerives(t *testing.T) {
	stackLink, peer := link.NewPipeLink()
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
	stackLink, peer := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	remote := Endpoint{IP: dxRemote, Port: 1234}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	old, _ := s.table.insertIfAbsent(tp, func(_ *Conn) *Conn {
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

// CLOSED まで進めた接続は connTable と Tick 集合から回収され、同じ 4-tuple で
// 新規 SYN が LISTEN 派生して握手できる (4-tuple 再利用可能)。
func TestClosedConnReapedAndTupleReusable(t *testing.T) {
	stackLink, peer := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	remote := Endpoint{IP: dxRemote, Port: 1234}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}

	// CLOSED な残骸をテーブルと Tick 集合に置く (LAST-ACK 完了後などの状態)。
	dead, _ := s.table.insertIfAbsent(tp, func(_ *Conn) *Conn {
		c := NewConn(stackLink, fc.Now, local, remote)
		c.tcb.state = Closed
		return c
	})
	s.track(dead)

	// 同じ 4-tuple へ新規 SYN → CLOSED 残骸は回収され LISTEN 派生で握手が始まる。
	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagSYN), SeqNum: 8000, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	if h := readSegWithin(t, peer, time.Second); h == nil || !h.Flags.Has(FlagSYN) || !h.Flags.Has(FlagACK) {
		t.Fatalf("再利用した 4-tuple で SYN-ACK が返らない: %v", h)
	}
	// テーブルは CLOSED 残骸でなく新 incarnation を指す。
	if got := s.table.lookup(tp); got == dead {
		t.Fatal("CLOSED 残骸が回収されていない (古い Conn のまま)")
	}
}

// CLOSED へ達した接続は Tick 集合から消える (tickLoop が死接続を叩き続けない)。
func TestClosedConnRemovedFromTickSet(t *testing.T) {
	stackLink, _ := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)

	remote := Endpoint{IP: dxRemote, Port: 5555}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	dead, _ := s.table.insertIfAbsent(tp, func(_ *Conn) *Conn {
		c := NewConn(stackLink, fc.Now, local, remote)
		c.tcb.state = Closed
		return c
	})
	s.track(dead)

	// reap 経路 (tickLoop の CLOSED 検出) を直接叩く。
	s.reapClosed()

	s.mu.Lock()
	_, present := s.conns[dead]
	s.mu.Unlock()
	if present {
		t.Fatal("CLOSED 接続が Tick 集合に残っている")
	}
	if s.table.lookup(tp) != nil {
		t.Fatal("CLOSED 接続が connTable に残っている")
	}
}

// TIME-WAIT の 4-tuple へ新 SYN → 派生接続の ISS が旧接続の max seq より大きい。
func TestTimeWaitReplacementIssExceedsOldMaxSeq(t *testing.T) {
	stackLink, peer := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	remote := Endpoint{IP: dxRemote, Port: 1234}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	const oldMax = 50000
	s.table.insertIfAbsent(tp, func(_ *Conn) *Conn {
		c := NewConn(stackLink, fc.Now, local, remote)
		c.tcb.state = TimeWait
		c.tcb.snd.nxt = oldMax // 旧 incarnation が送った最大 seq
		c.tcb.snd.una = oldMax
		return c
	})

	pkt := buildSeg(dxRemote, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagSYN), SeqNum: 8000, DataOffset: 5}, nil)
	_ = peer.WritePacket(pkt)

	h := readSegWithin(t, peer, time.Second)
	if h == nil || !h.Flags.Has(FlagSYN) || !h.Flags.Has(FlagACK) {
		t.Fatalf("新 incarnation の SYN-ACK が返らない: %v", h)
	}
	// SYN-ACK の seq = 新 ISS。旧 max seq より大きいこと。
	if !SeqGT(h.SeqNum, oldMax) {
		t.Fatalf("新 ISS が旧 max seq を上回らない: iss=%d oldMax=%d", h.SeqNum, oldMax)
	}
}

// broadcast/multicast/不正 src の SYN は破棄 (派生も RST もしない)。
func TestDemuxInvalidSrcSynDropped(t *testing.T) {
	stackLink, peer := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	mcast := [4]byte{224, 0, 0, 5}
	pkt := buildSeg(mcast, dxLocal, TCPHeader{SrcPort: 1234, DstPort: 9000, Flags: Flags(FlagSYN), SeqNum: 5000, DataOffset: 5}, nil)
	// demux を同期で叩き、戻った時点の peer inbox を確定的に検査する (実時間に依存しない)。
	s.demux(pkt)

	if h, ok := drainPeerNonblockKeep(peer); ok {
		t.Fatalf("不正 src の SYN は破棄するはず、応答が来た: flags=%v", h.Flags)
	}
}

// 完全一致 TCB が LISTEN より優先される。LISTEN と同じ local port で確立済み
// 接続があるとき、その 4-tuple 宛のセグメントは派生ではなく既存接続へ届く。
func TestDemuxExactMatchBeatsListen(t *testing.T) {
	stackLink, _ := link.NewPipeLink()
	fc := newFakeClock()
	s := NewStack(stackLink, fc.Now)
	t.Cleanup(s.Close)
	ln := s.Listen(Endpoint{IP: dxLocal, Port: 9000})
	t.Cleanup(ln.Close)

	// 既存接続を直接テーブルに置く (LISTEN と同 local port, 特定 remote)。
	remote := Endpoint{IP: dxRemote, Port: 1234}
	local := Endpoint{IP: dxLocal, Port: 9000}
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	existing, _ := s.table.insertIfAbsent(tp, func(_ *Conn) *Conn {
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
	// demux を同期で叩く。戻った時点で onSegment は完了しているので sleep ポーリング不要。
	s.demux(pkt)

	// 既存接続が RCV.NXT を進めれば届いた証拠。派生なら新 Conn ができ既存は不変。
	if existing.RcvNxt() != 5002 {
		t.Fatalf("完全一致 TCB にデータが届いていない: RcvNxt=%d want 5002", existing.RcvNxt())
	}
}

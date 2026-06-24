package tcp

import (
	"sync"
	"time"
)

// Stack は 1 つの link を複数接続で共有し、受信を 1 本の goroutine で demux する。
// 4-tuple 完全一致 → LISTEN 派生 → RST 生成、の順で受信セグメントを振り分ける。
type Stack struct {
	link  Link
	clock Clock
	table *connTable

	wg   sync.WaitGroup
	done chan struct{}
	once sync.Once

	// delivered は派生接続を accept チャネルへ既に渡したかの記録。
	// demux goroutine だけが触るので mutex 不要 (受信は単一 goroutine)。
	delivered map[*Conn]bool

	// active/derived な接続を Tick で駆動するための集合。demux goroutine が
	// 追加し、Tick goroutine が読む。
	mu    sync.Mutex
	conns map[*Conn]struct{}
}

// NewStack は link を共有する接続多重化スタックを作り、受信 demux と Tick を起動する。
// demux は受信パケットの宛先で接続を引くので local IP はパケット側から決まる。
func NewStack(link Link, clock Clock) *Stack {
	s := &Stack{
		link:      link,
		clock:     clock,
		table:     newConnTable(),
		done:      make(chan struct{}),
		delivered: make(map[*Conn]bool),
		conns:     make(map[*Conn]struct{}),
	}
	s.wg.Add(2)
	go s.recvLoop()
	go s.tickLoop()
	return s
}

// Close は demux と Tick を止め link を閉じて goroutine を回収する。冪等。
func (s *Stack) Close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.link.Close()
		s.wg.Wait()
	})
}

// track は接続を Tick 対象に登録する。
func (s *Stack) track(c *Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

// Dial は能動オープン。Conn を connTable に test-and-set で登録し ActiveOpen する。
// 既存 4-tuple が非 TIME-WAIT で占有していればそれを返す (二重 OPEN を作らない)。
func (s *Stack) Dial(local, remote Endpoint, iss uint32) *Conn {
	tp := fourTuple{local.IP, local.Port, remote.IP, remote.Port}
	c, created := s.table.insertIfAbsent(tp, func() *Conn {
		return NewConn(s.link, s.clock, local, remote)
	})
	if created {
		s.track(c)
		c.ActiveOpen(iss)
	}
	return c
}

// Listen は LISTEN エントリを作り Listener を返す。SYN 受信で派生した確立済み
// 接続が Accept で取れる。
func (s *Stack) Listen(local Endpoint) *Listener {
	accept := make(chan *Conn, 16)
	s.table.addListener(local, accept)
	return &Listener{stack: s, local: local, accept: accept}
}

// recvLoop は link から 1 read = 1 IP パケットを読み demux する。
func (s *Stack) recvLoop() {
	defer s.wg.Done()
	for {
		pkt, err := s.link.ReadPacket()
		if err != nil {
			return // link クローズで終了。
		}
		s.demux(pkt)
	}
}

// tickLoop は登録済み接続の再送・TIME-WAIT 満了を定期駆動する。
func (s *Stack) tickLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			cs := make([]*Conn, 0, len(s.conns))
			for c := range s.conns {
				cs = append(cs, c)
			}
			s.mu.Unlock()
			for _, c := range cs {
				c.Tick()
			}
		}
	}
}

// demux は 1 IPv4 パケットを解析し、4-tuple で接続を引いて振り分ける。
// 照合順序: 完全一致 TCB → LISTEN 派生 → RST 生成 (RFC 9293 §3.10.7)。
func (s *Stack) demux(pkt []byte) {
	ip, err := ParseIPv4Header(pkt)
	if err != nil || ip.Protocol != 6 { // 6 = TCP
		return
	}
	segment := pkt[int(ip.IHL)*4:]
	if TCPChecksum(ip.SrcAddr, ip.DstAddr, segment) != 0 {
		return // チェックサム不一致は破棄。
	}
	h, err := ParseTCPHeader(segment)
	if err != nil {
		return
	}
	payload := segment[int(h.DataOffset)*4:]

	// 受信視点: 宛先 = local, 送信元 = remote。
	tp := fourTuple{ip.DstAddr, h.DstPort, ip.SrcAddr, h.SrcPort}

	// 1. 完全一致 TCB → そこへ dispatch。
	//    例外: TIME-WAIT への新 SYN は新 incarnation を許す (RFC 9293 §3.10.7.4,
	//    MAY-2)。LISTEN 派生へ落とし、insertIfAbsent が TIME-WAIT を置換する。
	if c := s.table.lookup(tp); c != nil {
		if !(h.Flags.Has(FlagSYN) && c.State() == TimeWait) {
			c.onSegment(h, payload)
			s.maybeDeliver(c)
			return
		}
	}

	// 2. LISTEN を探す (local 一致, remote ワイルドカード)。
	if le := s.table.lookupListener(ip.DstAddr, h.DstPort); le != nil {
		s.demuxListen(le, ip, h, payload, tp)
		return
	}

	// 3. どちらも無し (CLOSED 相当): RST 生成 (RST には RST を返さない)。
	s.sendClosedRst(ip, h, payload)
}

// demuxListen は LISTEN への受信を処理する (RFC 9293 §3.10.7.2)。
//   - RST → 無視 / ACK → RST 返す / SYN → 新 TCB 派生 (LISTEN は残す) / else drop
func (s *Stack) demuxListen(le *listenEntry, ip IPv4Header, h TCPHeader, payload []byte, tp fourTuple) {
	if h.Flags.Has(FlagRST) {
		return
	}
	if h.Flags.Has(FlagACK) {
		s.sendClosedRst(ip, h, payload)
		return
	}
	if !h.Flags.Has(FlagSYN) {
		return
	}
	// broadcast/multicast/不正 src の SYN は破棄 (RFC 9293)。
	if !validUnicastSrc(ip.SrcAddr) {
		return
	}

	remote := Endpoint{IP: ip.SrcAddr, Port: h.SrcPort}
	// SYN → 新 TCB 派生。test-and-set で登録し (二重派生を防ぐ)、LISTEN 自身は残す。
	c, created := s.table.insertIfAbsent(tp, func() *Conn {
		nc := NewConn(s.link, s.clock, le.local, remote)
		nc.PassiveOpen() // LISTEN へ。続く onSegment(SYN) で SYN-RECEIVED へ派生。
		return nc
	})
	if created {
		c.deriveTo = le.accept
		s.track(c)
	}
	c.onSegment(h, payload)
	s.maybeDeliver(c)
}

// maybeDeliver は派生接続が ESTABLISHED に達したら 1 度だけ accept チャネルへ渡す。
func (s *Stack) maybeDeliver(c *Conn) {
	if c.deriveTo == nil || s.delivered[c] {
		return
	}
	if c.ReachedEstablished() {
		// accept バッファが満杯なら demux 全体を止めないよう諦め、次の受信で再試行する
		// (delivered を立てない)。ESTABLISHED 後はデータ ACK 等で必ず再来する。
		select {
		case c.deriveTo <- c:
			s.delivered[c] = true
		default:
		}
	}
}

// sendClosedRst は TCB の無い受信への RST 応答を出す (RFC 9293 §3.10.7.1)。
// 使い捨ての CLOSED Conn に処理を委ね、RST 生成・RST への無応答を再利用する。
func (s *Stack) sendClosedRst(ip IPv4Header, h TCPHeader, payload []byte) {
	remote := Endpoint{IP: ip.SrcAddr, Port: h.SrcPort}
	local := Endpoint{IP: ip.DstAddr, Port: h.DstPort}
	tmp := NewConn(s.link, s.clock, local, remote) // CLOSED 状態
	tmp.onSegment(h, payload)
}

// validUnicastSrc は SYN の送信元として妥当か (broadcast/multicast/0.0.0.0 を弾く)。
func validUnicastSrc(ip [4]byte) bool {
	if ip == [4]byte{0, 0, 0, 0} || ip == [4]byte{255, 255, 255, 255} {
		return false
	}
	if ip[0] >= 224 { // 224.0.0.0/4 = multicast 以上
		return false
	}
	return true
}

// Listener は LISTEN している端点。Accept で確立済みの派生接続を受け取る。
type Listener struct {
	stack  *Stack
	local  Endpoint
	accept chan *Conn
}

// Accept は確立した派生接続を 1 つ返す (ブロッキング)。Listener が閉じたら nil。
func (l *Listener) Accept() *Conn {
	c, ok := <-l.accept
	if !ok {
		return nil
	}
	return c
}

// Close は LISTEN エントリを外す。既に確立した派生接続は閉じない。
func (l *Listener) Close() {
	l.stack.table.removeListener(l.local)
}

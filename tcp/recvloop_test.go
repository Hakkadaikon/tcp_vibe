package tcp

import (
	"runtime"
	"sync"
	"testing"
)

// testAddr は受信ループテストで使う固定の src/dst IP。
var (
	rlSrc = [4]byte{10, 0, 0, 1}
	rlDst = [4]byte{10, 0, 0, 2}
)

// buildSegment は h と payload から、チェックサム整合の取れた IPv4+TCP パケットの
// 生バイト列 (リンク層へ流すバイト列) を組み立てる。
func buildSegment(h TCPHeader, payload []byte) []byte {
	tcp := append(h.Marshal(), payload...)
	// TCP チェックサムは擬似ヘッダ込みで計算し直して埋める。
	putBe16(tcp, 16, TCPChecksum(rlSrc, rlDst, tcp))

	total := 20 + len(tcp)
	ip := IPv4Header{
		Protocol:    6,
		TotalLength: uint16(total),
		SrcAddr:     rlSrc,
		DstAddr:     rlDst,
		TTL:         64,
	}
	return append(ip.Marshal(), tcp...)
}

// newTestReceiver は LISTEN 中の Conn と、Conn へパケットを流し込む書き込み側 Link、
// 起動済みの receiver を返す。teardown で Stop する。
func newTestReceiver(t *testing.T) (*Conn, Link, *receiver) {
	t.Helper()
	a, b := NewPipeLink() // a が Conn 側、b がテストから書き込む側
	fc := newFakeClock()
	c := NewConn(a, fc.Now, Endpoint{IP: rlDst}, Endpoint{IP: rlSrc})
	c.tcb.snd.iss = 7000
	c.PassiveOpen()

	r := newReceiver(c, a, 65535)
	r.Start()
	t.Cleanup(r.Stop)
	return c, b, r
}

// 正常系: LISTEN の Conn に SYN を送ると受信ループ経由で SYN-RECEIVED へ遷移する。
func TestReceiveLoopDeliversSynToStateMachine(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	pkt := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	if err := peer.WritePacket(pkt); err != nil {
		t.Fatalf("WritePacket 失敗: %v", err)
	}

	waitState(t, c, SynReceived)
}

// フレーミング連携: 1 セグメントを複数チャンクに分割して書いても 1 つとして届く。
func TestReceiveLoopReassemblesSplitSegment(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	pkt := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	// パケットを途中で割って 2 回に分けて送る (部分到着)。
	mid := len(pkt) / 2
	if err := peer.WritePacket(pkt[:mid]); err != nil {
		t.Fatalf("WritePacket(前半) 失敗: %v", err)
	}
	if c.State() != Listen {
		t.Fatalf("部分到着の段階ではまだ LISTEN のはず: got %v", c.State())
	}
	if err := peer.WritePacket(pkt[mid:]); err != nil {
		t.Fatalf("WritePacket(後半) 失敗: %v", err)
	}

	waitState(t, c, SynReceived)
}

// フレーミング連携: 複数セグメントを 1 回の書き込みに連結しても全部届く。
// SYN → (SYN-RECEIVED) → ACK → ESTABLISHED を 1 チャンクで送る。
func TestReceiveLoopHandlesConcatenatedSegments(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	syn := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	// SYN-RECEIVED で送る SYN,ACK の seq=ISS(7000)。相手の ACK は ack=7001。
	ack := buildSegment(TCPHeader{
		Flags: Flags(FlagACK), SeqNum: 3001, AckNum: 7001, DataOffset: 5,
	}, nil)

	if err := peer.WritePacket(append(syn, ack...)); err != nil {
		t.Fatalf("WritePacket 失敗: %v", err)
	}

	waitState(t, c, Established)
}

// 不正パケット: IPv4 チェックサム不一致 / 短すぎ / TCP でない を混ぜても接続は壊れず、
// 後続の正常パケットは届く (不正パケットは状態機械に届かない)。
func TestReceiveLoopDropsInvalidPackets(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	// (1) IPv4 チェックサムを壊したパケット。ParseIPv4Header が拒否し破棄される。
	//     TotalLength は整合させ Framer は 1 パケットとして切り出せるようにする。
	badIP := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 1111, DataOffset: 5}, nil)
	badIP[10] ^= 0xFF // IPv4 ヘッダチェックサム域を破壊
	if err := peer.WritePacket(badIP); err != nil {
		t.Fatalf("WritePacket(badIP) 失敗: %v", err)
	}

	// (2) TCP でないパケット (Protocol=17 UDP)。Framer は通すが dispatch で破棄。
	udp := IPv4Header{Protocol: 17, TotalLength: 40, SrcAddr: rlSrc, DstAddr: rlDst, TTL: 64}
	udpPkt := append(udp.Marshal(), make([]byte, 20)...) // 中身は何でもよい (届かない)
	if err := peer.WritePacket(udpPkt); err != nil {
		t.Fatalf("WritePacket(udp) 失敗: %v", err)
	}

	// (2b) IPv4 は正しいが TCP チェックサムが不正なパケット。dispatch で破棄される。
	badTCP := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 2222, DataOffset: 5}, nil)
	badTCP[int(ipv4MinHeader)+16] ^= 0xFF // TCP チェックサム域を破壊 (IP ヘッダ後の seg 内 offset 16)
	if err := peer.WritePacket(badTCP); err != nil {
		t.Fatalf("WritePacket(badTCP) 失敗: %v", err)
	}

	// (3) IPv4 としては正しいが TCP ヘッダが短すぎるパケット。ParseTCPHeader が拒否。
	shortIP := IPv4Header{Protocol: 6, TotalLength: 24, SrcAddr: rlSrc, DstAddr: rlDst, TTL: 64}
	shortPkt := append(shortIP.Marshal(), 0, 0, 0, 0) // TCP 部 4 バイトだけ (20 未満)
	if err := peer.WritePacket(shortPkt); err != nil {
		t.Fatalf("WritePacket(short) 失敗: %v", err)
	}

	// (4) 正常な SYN。これが届けば前段の不正パケットで接続が壊れていない証拠。
	good := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	if err := peer.WritePacket(good); err != nil {
		t.Fatalf("WritePacket(good) 失敗: %v", err)
	}

	waitState(t, c, SynReceived)
}

// 終了: link を閉じると受信ループ goroutine が終了し Stop が返る (リークしない)。
func TestReceiveLoopStopsWhenLinkClosed(t *testing.T) {
	a, _ := NewPipeLink()
	fc := newFakeClock()
	c := NewConn(a, fc.Now, Endpoint{IP: rlDst}, Endpoint{IP: rlSrc})
	c.PassiveOpen()

	r := newReceiver(c, a, 65535)
	r.Start()

	r.Stop() // link を閉じて回収。返れば goroutine は終了している。
	if err := r.Err(); err != nil {
		t.Fatalf("正常終了のはずがエラー: %v", err)
	}
	r.Stop() // 二重 Stop で panic しないこと (冪等)。
}

// 並行性 (最重要): 受信ループが回っている最中に別 goroutine から State() を読んでも
// -race がクリーン。高速にセグメントを送りつけながら観測する。
func TestReceiveLoopConcurrentObservationRaceFree(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	const n = 200
	var wg sync.WaitGroup

	// 送信側: 大量のセグメントを送りつける。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			pkt := buildSegment(TCPHeader{
				Flags: Flags(FlagSYN), SeqNum: uint32(3000 + i), DataOffset: 5,
			}, nil)
			_ = peer.WritePacket(pkt)
		}
	}()

	// 観測側: 同時に状態を読み続ける (mutex 越しのアクセスが受信ループと競合しないこと)。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n*5; i++ {
			_ = c.State()
			_ = c.RcvNxt()
			_ = c.SndNxt()
		}
	}()

	wg.Wait()
	waitState(t, c, SynReceived)
}

// waitState は受信ループが非同期に処理するのを待ち、c が want になることを確認する。
// pipeLink は cond で起こされるため、State をポーリングしつつ最終状態を待つ。
func waitState(t *testing.T, c *Conn, want State) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if c.State() == want {
			return
		}
		// 受信 goroutine に処理を譲る。busy-wait だが決定論的に収束する。
		runtime.Gosched()
	}
	t.Fatalf("状態が %v にならない: got %v", want, c.State())
}

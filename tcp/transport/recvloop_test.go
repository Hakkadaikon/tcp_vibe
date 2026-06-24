package transport

import (
	"github.com/hakkadaikon/tcp_vibe/tcp/link"
	"github.com/hakkadaikon/tcp_vibe/tcp/network"
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
	network.PutBe16(tcp, 16, network.TCPChecksum(rlSrc, rlDst, tcp))

	total := 20 + len(tcp)
	ip := network.IPv4Header{
		Protocol:    6,
		TotalLength: uint16(total),
		SrcAddr:     rlSrc,
		DstAddr:     rlDst,
		TTL:         64,
	}
	return append(ip.Marshal(), tcp...)
}

// newTestReceiver は LISTEN 中の Conn と、Conn へパケットを流し込む書き込み側 link.Link、
// 起動済みの receiver を返す。teardown で Stop する。
func newTestReceiver(t *testing.T) (*Conn, link.Link, *receiver) {
	t.Helper()
	a, b := link.NewPipeLink() // a が Conn 側、b がテストから書き込む側
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

// パケット境界: 現状の link.Link は 1 WritePacket = 1 IP パケットの境界を保つ。
// 2 回 WritePacket すれば 2 パケットがそれぞれ処理される。
// SYN → (SYN-RECEIVED) → ACK → ESTABLISHED を 2 回に分けて送る。
func TestReceiveLoopProcessesEachPacketSeparately(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	syn := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	if err := peer.WritePacket(syn); err != nil {
		t.Fatalf("WritePacket(syn) 失敗: %v", err)
	}
	waitState(t, c, SynReceived)

	// SYN-RECEIVED で送る SYN,ACK の seq=ISS(7000)。相手の ACK は ack=7001。
	ack := buildSegment(TCPHeader{
		Flags: Flags(FlagACK), SeqNum: 3001, AckNum: 7001, DataOffset: 5,
	}, nil)
	if err := peer.WritePacket(ack); err != nil {
		t.Fatalf("WritePacket(ack) 失敗: %v", err)
	}
	waitState(t, c, Established)
}

// 回帰 (今回のバグ): 非 IPv4 パケット (IPv6 等) を受け取っても受信ループは死なず、
// 後続の正常な SYN が届いて SYN-RECEIVED に到達する。
// 旧実装は ReadPacket の結果を Framer.Push に通し、非 IPv4 で致命的エラーが返ると
// loop() が return して受信ループごと停止していた。
func TestReceiveLoopSurvivesNonIPv4Packet(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	// version=6 (IPv6) の先頭バイトを持つパケット。network.ParseIPv4Header は非 IPv4 として拒否。
	ipv6ish := make([]byte, 48)
	ipv6ish[0] = 0x60 // 上位 4bit = version = 6
	if err := peer.WritePacket(ipv6ish); err != nil {
		t.Fatalf("WritePacket(ipv6) 失敗: %v", err)
	}

	// 受信ループが死んでいなければ、この正常 SYN が届いて遷移する。
	good := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	if err := peer.WritePacket(good); err != nil {
		t.Fatalf("WritePacket(good) 失敗: %v", err)
	}

	waitState(t, c, SynReceived)
}

// 不正パケット: IPv4 チェックサム不一致 / 短すぎ / TCP でない を混ぜても接続は壊れず、
// 後続の正常パケットは届く (不正パケットは状態機械に届かない)。
func TestReceiveLoopDropsInvalidPackets(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	// (1) IPv4 チェックサムを壊したパケット。network.ParseIPv4Header が拒否し破棄される。
	badIP := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 1111, DataOffset: 5}, nil)
	badIP[10] ^= 0xFF // IPv4 ヘッダチェックサム域を破壊
	if err := peer.WritePacket(badIP); err != nil {
		t.Fatalf("WritePacket(badIP) 失敗: %v", err)
	}

	// (2) TCP でないパケット (Protocol=17 UDP)。dispatch が proto!=6 で破棄。
	udp := network.IPv4Header{Protocol: 17, TotalLength: 40, SrcAddr: rlSrc, DstAddr: rlDst, TTL: 64}
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
	shortIP := network.IPv4Header{Protocol: 6, TotalLength: 24, SrcAddr: rlSrc, DstAddr: rlDst, TTL: 64}
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

// 末尾パディング付きの正当パケットが正しく処理される (パディングが TCP セグメントへ
// 混入しない)。dispatch は TotalLength で切り詰めてから checksum/parse する。
func TestReceiveLoopHandlesTrailingPadding(t *testing.T) {
	c, peer, _ := newTestReceiver(t)

	// 正当な SYN パケットを組み、末尾にゴミパディングを連結する。
	// TotalLength はパディングを含まない (パディングは TCP セグメント外)。
	pkt := buildSegment(TCPHeader{Flags: Flags(FlagSYN), SeqNum: 3000, DataOffset: 5}, nil)
	padded := append(pkt, 0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x11)
	if err := peer.WritePacket(padded); err != nil {
		t.Fatalf("WritePacket(padded) 失敗: %v", err)
	}

	// パディングが TCP セグメントへ混入していなければ checksum が通り遷移する。
	waitState(t, c, SynReceived)
}

// 終了: link を閉じると受信ループ goroutine が終了し Stop が返る (リークしない)。
func TestReceiveLoopStopsWhenLinkClosed(t *testing.T) {
	a, _ := link.NewPipeLink()
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

package tcp

import (
	"sync"
	"time"

	"github.com/hakkadaikon/tcp_vibe/tcp/link"
	"github.com/hakkadaikon/tcp_vibe/tcp/network"
)

// Serve は conn の受信ループを起動し、再送・TIME-WAIT 満了を駆動する Tick を
// 定期的に回す。返す stop を呼ぶと両方を停止し link を閉じて goroutine を回収する。
// receiver は非公開なので、アプリ層 (cmd) はこの薄い公開配線で接続を駆動する。
func Serve(conn *Conn, maxPacket int) (stop func()) {
	r := newReceiver(conn, conn.link, maxPacket)
	r.Start()

	ticker := time.NewTicker(100 * time.Millisecond)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ticker.C:
				conn.Tick()
			case <-done:
				return
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			ticker.Stop()
			close(done)
			wg.Wait()
			r.Stop()
		})
	}
}

// receiver は Conn の受信ループを 1 本の goroutine で駆動する。
//
// handoff の並行設計を実体化する:
//   - 受信は単一 goroutine。link.ReadPacket を直列に呼び、1 read = 1 IP パケット
//     (現状の Link はすべて境界を保つ) を直接 dispatch する。
//   - 状態機械への入口 onSegment は Conn の mutex で 1 クリティカルセクションに直列化
//     される。ユーザコール (State 等の観測・送信) と受信ループは mutex 越しに競合しない。
//
// receiver 自身は Conn を改変せず、外から onSegment を呼んで駆動する薄い層。
type receiver struct {
	conn *Conn
	link link.Link

	wg   sync.WaitGroup
	once sync.Once // Stop の冪等化

	mu  sync.Mutex // err を守る (受信 goroutine が書き、観測側が読む)
	err error      // ループ終了理由 (正常終了は nil)
}

// newReceiver は link から TCP セグメントを読んで conn.onSegment へ流す受信器を作る。
// maxPacket は将来のストリーム型 Link 用に予約 (現状の境界保持 Link では未使用)。
func newReceiver(conn *Conn, lnk link.Link, maxPacket int) *receiver {
	_ = maxPacket
	return &receiver{
		conn: conn,
		link: lnk,
	}
}

// Start は受信ループ goroutine を起動する。1 度だけ呼ぶこと。
func (r *receiver) Start() {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.loop()
	}()
}

// Stop は link を閉じて受信 goroutine を回収する。冪等 (二重呼び出しで panic しない)。
func (r *receiver) Stop() {
	r.once.Do(func() {
		_ = r.link.Close()
	})
	r.wg.Wait()
}

// Err はループ終了理由を返す。正常終了 (link クローズ) は nil。Stop 後に観測すること。
func (r *receiver) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// loop は link から 1 read = 1 IP パケットを読み、そのまま dispatch へ渡す。
// link クローズで正常終了する。非 IPv4 や不正パケットは dispatch が捨てて継続するので、
// 受信ループ自体はパケット単位で死なない (TUN は IPv6 等の非 IPv4 も普通に届ける)。
func (r *receiver) loop() {
	for {
		pkt, err := r.link.ReadPacket()
		if err != nil {
			network.Debugf("recv: ReadPacket エラー: %v", err)
			return // ErrLinkClosed 等。link が閉じたらループ終了。
		}
		r.dispatch(pkt)
	}
}

// dispatch は 1 つの IPv4 パケットを解析し、TCP セグメントなら状態機械へ渡す。
// 解析失敗 (チェックサム不一致・短すぎ・TCP でない) は接続を壊さずそのパケットを
// 捨てて継続する (不正パケットは状態機械に届かない)。
func (r *receiver) dispatch(pkt []byte) {
	ip, err := network.ParseIPv4Header(pkt)
	if err != nil {
		network.Debugf("recv: 破棄 (IPv4 パース失敗): len=%d err=%v", len(pkt), err)
		return // 不正な IPv4 ヘッダ (チェックサム不一致等) は破棄。
	}
	network.Debugf("recv: パケット len=%d %s -> %s proto=%d", len(pkt), network.IPStr(ip.SrcAddr), network.IPStr(ip.DstAddr), ip.Protocol)
	if ip.Protocol != 6 { // 6 = TCP
		network.Debugf("recv: 破棄 (非TCP proto=%d)", ip.Protocol)
		return
	}
	segment, ok := network.TCPSegment(ip, pkt)
	if !ok {
		network.Debugf("recv: 破棄 (IPv4 TotalLength 不正): len=%d total=%d", len(pkt), ip.TotalLength)
		return // TotalLength がバッファと矛盾するパケットは破棄。
	}
	// TCP チェックサム検証 (擬似ヘッダ込み)。不一致なら状態機械に届けない。
	// 正しいセグメントは checksum 欄込みの ones'-comp sum が 0 になる。
	if sum := network.TCPChecksum(ip.SrcAddr, ip.DstAddr, segment); sum != 0 {
		network.Debugf("recv: 破棄 (TCP チェックサム不一致 計算値=0x%04x)", sum)
		return
	}
	h, err := ParseTCPHeader(segment)
	if err != nil {
		network.Debugf("recv: 破棄 (TCP ヘッダパース失敗): err=%v", err)
		return // 不正な TCP ヘッダは破棄。
	}
	network.Debugf("recv: onSegment flags=%s seq=%d ack=%d", h.Flags, h.SeqNum, h.AckNum)
	r.conn.onSegment(h, segment[int(h.DataOffset)*4:])
}

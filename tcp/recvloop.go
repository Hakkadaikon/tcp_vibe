package tcp

import (
	"sync"
	"time"
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
//   - 受信は単一 goroutine。link.ReadPacket を直列に呼ぶので Framer の状態 (buf)
//     は 1 goroutine しか触らず、ロック不要。
//   - 状態機械への入口 onSegment は Conn の mutex で 1 クリティカルセクションに直列化
//     される。ユーザコール (State 等の観測・送信) と受信ループは mutex 越しに競合しない。
//
// receiver 自身は Conn を改変せず、外から onSegment を呼んで駆動する薄い層。
type receiver struct {
	conn   *Conn
	link   Link
	framer *Framer

	wg   sync.WaitGroup
	once sync.Once // Stop の冪等化

	mu  sync.Mutex // err を守る (受信 goroutine が書き、観測側が読む)
	err error      // ループ終了理由 (正常終了は nil。Framer エラー等で停止したら非 nil)
}

// newReceiver は link から TCP セグメントを読んで conn.onSegment へ流す受信器を作る。
// maxPacket は Framer の 1 パケット上限。
func newReceiver(conn *Conn, link Link, maxPacket int) *receiver {
	return &receiver{
		conn:   conn,
		link:   link,
		framer: NewFramer(maxPacket),
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

// Err はループ終了理由を返す。正常終了 (link クローズ) は nil、Framer のエラー
// (maxPacket 超等) で停止した場合は非 nil。Stop 後に観測すること。
func (r *receiver) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// loop は link からバイト列を読み、IPv4 パケット境界で再分割し、TCP セグメントを
// 状態機械へ届ける。link クローズで正常終了、Framer エラーで停止する。
func (r *receiver) loop() {
	for {
		chunk, err := r.link.ReadPacket()
		if err != nil {
			return // ErrLinkClosed 等。link が閉じたらループ終了。
		}
		packets, ferr := r.framer.Push(chunk)
		// Framer はエラー時も切り出せたパケットを返す。先に届ける。
		for _, pkt := range packets {
			r.dispatch(pkt)
		}
		if ferr != nil {
			// maxPacket 超など、続きを待っても回復しない接続エラー。理由を残して停止。
			r.mu.Lock()
			r.err = ferr
			r.mu.Unlock()
			return
		}
	}
}

// dispatch は 1 つの IPv4 パケットを解析し、TCP セグメントなら状態機械へ渡す。
// 解析失敗 (チェックサム不一致・短すぎ・TCP でない) は接続を壊さずそのパケットを
// 捨てて継続する (不正パケットは状態機械に届かない)。
func (r *receiver) dispatch(pkt []byte) {
	ip, err := ParseIPv4Header(pkt)
	if err != nil {
		return // 不正な IPv4 ヘッダ (チェックサム不一致等) は破棄。
	}
	if ip.Protocol != 6 { // 6 = TCP
		return
	}
	segment := pkt[int(ip.IHL)*4:]
	// TCP チェックサム検証 (擬似ヘッダ込み)。不一致なら状態機械に届けない。
	// 正しいセグメントは checksum 欄込みの ones'-comp sum が 0 になる。
	if TCPChecksum(ip.SrcAddr, ip.DstAddr, segment) != 0 {
		return
	}
	h, err := ParseTCPHeader(segment)
	if err != nil {
		return // 不正な TCP ヘッダは破棄。
	}
	r.conn.onSegment(h, segment[int(h.DataOffset)*4:])
}

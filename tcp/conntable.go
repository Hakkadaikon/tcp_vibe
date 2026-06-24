package tcp

import "sync"

// fourTuple は接続を一意に識別する 4-tuple (RFC 9293 §3.3.2)。
// local 視点で持つ (受信時は宛先が local, 送信元が remote)。
type fourTuple struct {
	localIP    [4]byte
	localPort  uint16
	remoteIP   [4]byte
	remotePort uint16
}

// listenEntry は LISTEN しているローカル端点。SYN 受信で派生した確立済み接続を
// accept チャネルへ流す。LISTEN は派生しても LISTEN のまま残る (非破壊)。
type listenEntry struct {
	local  Endpoint
	accept chan *Conn
}

// connTable は 4-tuple → Conn の対応表と LISTEN 集合を 1 つの mutex で守る。
// demux (受信 goroutine) と OPEN/CLOSE (ユーザ goroutine) が並行アクセスする。
//
// 核心は insertIfAbsent の test-and-set: 占有判定と挿入を分離すると並行 demux/OPEN で
// 同一 4-tuple に二重 TCB ができる。単一 mutex の下で不可分に行うことで防ぐ。
type connTable struct {
	mu        sync.Mutex
	conns     map[fourTuple]*Conn
	listeners map[localKey]*listenEntry // remote ワイルドカードなので local (IP,port) で引く
}

// localKey は LISTEN の引き当てキー (remote はワイルドカード)。
type localKey struct {
	ip   [4]byte
	port uint16
}

func newConnTable() *connTable {
	return &connTable{
		conns:     make(map[fourTuple]*Conn),
		listeners: make(map[localKey]*listenEntry),
	}
}

// insertIfAbsent は test-and-set。すべて mutex 下で不可分に行う:
//   - 非 TIME-WAIT な占有があれば既存を返し created=false (二重 TCB を作らない)
//   - 空、または TIME-WAIT 占有 (新 incarnation 可) なら mkConn で作って入れ created=true。
//     置換した旧 TIME-WAIT 接続を mkConn に渡し、新 ISS を旧 max seq より大きく採れる
//     ようにする (RFC 9293 §3.10.7.4)。
//
// ロック順は ct.mu → c.mu (existing.State() が c.mu を取る)。逆順 (Conn から ct.mu)
// にするとデッドロックするため、reap は必ず Conn ロック外で行う。CLOSED 残骸は demux/
// tickLoop が reap で先に削除してからここを通すので、ここで特別扱いはしない。
func (ct *connTable) insertIfAbsent(tp fourTuple, mkConn func(replaced *Conn) *Conn) (*Conn, bool) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	var replaced *Conn
	if existing, ok := ct.conns[tp]; ok {
		if existing.State() != TimeWait {
			return existing, false // 非 TIME-WAIT 占有: そのまま返す
		}
		replaced = existing // TIME-WAIT: 新 incarnation で置換する
	}
	c := mkConn(replaced)
	ct.conns[tp] = c
	return c, true
}

// lookup は完全一致 4-tuple の Conn を返す。無ければ nil。
func (ct *connTable) lookup(tp fourTuple) *Conn {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.conns[tp]
}

// remove は 4-tuple のエントリを消す。
func (ct *connTable) remove(tp fourTuple) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.conns, tp)
}

// removeIf は 4-tuple のエントリが want と同一のときだけ消す。CLOSED 残骸の回収中に
// 既に新 incarnation へ置換されていたら消さない (取り違え防止)。
func (ct *connTable) removeIf(tp fourTuple, want *Conn) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	if ct.conns[tp] == want {
		delete(ct.conns, tp)
	}
}

// addListener は LISTEN エントリを登録する。
func (ct *connTable) addListener(local Endpoint, accept chan *Conn) *listenEntry {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	le := &listenEntry{local: local, accept: accept}
	ct.listeners[localKey{local.IP, local.Port}] = le
	return le
}

// removeListener は LISTEN エントリを消す。
func (ct *connTable) removeListener(local Endpoint) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.listeners, localKey{local.IP, local.Port})
}

// lookupListener は local (IP, port) 一致の LISTEN を remote ワイルドカードで引く。
func (ct *connTable) lookupListener(ip [4]byte, port uint16) *listenEntry {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	return ct.listeners[localKey{ip, port}]
}

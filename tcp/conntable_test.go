package tcp

import (
	"sync"
	"sync/atomic"
	"testing"
)

func tupleFor(local, remote Endpoint) fourTuple {
	return fourTuple{
		localIP:    local.IP,
		localPort:  local.Port,
		remoteIP:   remote.IP,
		remotePort: remote.Port,
	}
}

// insertIfAbsent は test-and-set。同一 4-tuple へ 2 回入れても 2 回目は既存を返し、
// mkConn は 1 度しか呼ばれない (二重 TCB を作らない)。
func TestInsertIfAbsentReturnsExisting(t *testing.T) {
	ct := newConnTable()
	tp := tupleFor(lbServer, lbClient)
	calls := 0
	c1, created1 := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { calls++; return &Conn{} })
	if !created1 {
		t.Fatal("初回 insert は created=true のはず")
	}
	c2, created2 := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { calls++; return &Conn{} })
	if created2 {
		t.Fatal("2 回目 insert は created=false のはず")
	}
	if c1 != c2 {
		t.Fatal("2 回目は既存 Conn を返すはず")
	}
	if calls != 1 {
		t.Fatalf("mkConn は 1 度だけ呼ばれるはず: %d 回", calls)
	}
}

// 並行に同一 4-tuple へ insert しても作られる Conn は 1 つだけ (test-and-set が atomic)。
func TestInsertIfAbsentConcurrentSingleConn(t *testing.T) {
	ct := newConnTable()
	tp := tupleFor(lbServer, lbClient)
	var created int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { return &Conn{} }); ok {
				atomic.AddInt64(&created, 1)
			}
		}()
	}
	wg.Wait()
	if created != 1 {
		t.Fatalf("並行 insert で作られた Conn は 1 つのはず: %d", created)
	}
}

// lookup は完全一致 4-tuple の Conn を返し、無ければ nil。
func TestLookupExactMatch(t *testing.T) {
	ct := newConnTable()
	tp := tupleFor(lbServer, lbClient)
	want, _ := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { return &Conn{} })
	if got := ct.lookup(tp); got != want {
		t.Fatal("lookup が一致 Conn を返さない")
	}
	other := tupleFor(lbServer, Endpoint{IP: [4]byte{10, 0, 0, 9}, Port: 1})
	if got := ct.lookup(other); got != nil {
		t.Fatal("不一致 4-tuple は nil のはず")
	}
}

// remove で消えた 4-tuple は再び insert で作れる。
func TestRemoveAllowsReinsert(t *testing.T) {
	ct := newConnTable()
	tp := tupleFor(lbServer, lbClient)
	ct.insertIfAbsent(tp, func(_ *Conn) *Conn { return &Conn{} })
	ct.remove(tp)
	if _, ok := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { return &Conn{} }); !ok {
		t.Fatal("remove 後は再 insert できるはず")
	}
}

// TIME-WAIT の 4-tuple は新 incarnation を許す: insertIfAbsent が置換して created=true。
func TestInsertIfAbsentReplacesTimeWait(t *testing.T) {
	ct := newConnTable()
	tp := tupleFor(lbServer, lbClient)
	old, _ := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { c := &Conn{}; c.tcb.state = TimeWait; return c })
	fresh, created := ct.insertIfAbsent(tp, func(_ *Conn) *Conn { return &Conn{} })
	if !created {
		t.Fatal("TIME-WAIT は新 incarnation を許す (created=true)")
	}
	if fresh == old {
		t.Fatal("TIME-WAIT は新しい Conn に置換されるはず")
	}
	if ct.lookup(tp) != fresh {
		t.Fatal("テーブルは新 Conn を指すはず (二重にならない)")
	}
}

// lookupListener は local (IP,port) 一致の LISTEN を remote ワイルドカードで引く。
func TestLookupListenerWildcardRemote(t *testing.T) {
	ct := newConnTable()
	ct.addListener(lbServer, make(chan *Conn, 4))
	if le := ct.lookupListener(lbServer.IP, lbServer.Port); le == nil {
		t.Fatal("LISTEN が引けない")
	}
	if le := ct.lookupListener(lbServer.IP, 1); le != nil {
		t.Fatal("ポート違いは引けてはいけない")
	}
}

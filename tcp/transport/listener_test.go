package transport

import (
	"bytes"
	"github.com/hakkadaikon/tcp_vibe/tcp/link"
	"sync"
	"testing"
	"time"
)

// 2 つの Stack を pipe で繋ぎ、一方が Listen、他方が複数 Dial して
// 複数接続が同時に握手〜データ転送〜close できることを確認する (loopback)。
func TestStackLoopbackMultipleConns(t *testing.T) {
	aLink, bLink := link.NewPipeLink()
	fc := newFakeClock()

	ipA := [4]byte{10, 0, 0, 1}
	ipB := [4]byte{10, 0, 0, 2}
	server := NewStack(bLink, fc.Now)
	client := NewStack(aLink, fc.Now)
	t.Cleanup(server.Close)
	t.Cleanup(client.Close)

	ln := server.Listen(Endpoint{IP: ipB, Port: 9000})
	t.Cleanup(ln.Close)

	const n = 3
	var conns []*Conn
	for i := 0; i < n; i++ {
		local := Endpoint{IP: ipA, Port: uint16(40000 + i)}
		remote := Endpoint{IP: ipB, Port: 9000}
		c := client.Dial(local, remote, uint32(1000+i*1000))
		conns = append(conns, c)
	}

	// サーバ側で n 本 Accept できる。LISTEN は残る (n 本とも受けられる)。
	var accepted []*Conn
	for i := 0; i < n; i++ {
		ac := ln.Accept()
		if ac == nil {
			t.Fatalf("%d 本目の Accept が nil", i)
		}
		accepted = append(accepted, ac)
	}

	for i, c := range conns {
		waitStateSleep(t, c, Established)
		waitStateSleep(t, accepted[i], Established)
	}

	// 各接続で独立にデータが流れる (相乗りした接続が干渉しない)。
	for i, c := range conns {
		msg := bytes.Repeat([]byte{byte('A' + i)}, 50)
		if _, err := c.Send(msg); err != nil {
			t.Fatalf("conn %d Send 失敗: %v", i, err)
		}
		got := recvAll(t, accepted[i], len(msg))
		if !bytes.Equal(got, msg) {
			t.Fatalf("conn %d 受信不一致: got %q want %q", i, got, msg)
		}
	}

	// 全接続を能動 close (client) → server も応じて close。client は TIME-WAIT へ。
	for _, c := range conns {
		c.Close()
	}
	for i, c := range conns {
		waitStateSleep(t, accepted[i], CloseWait)
		accepted[i].Close()
		waitStateSleep(t, c, TimeWait)
	}
}

// Accept で待機中に Close すると Accept が nil を返してブロックを抜ける。
// 二重 Close でも panic しない。
func TestListenerCloseUnblocksAccept(t *testing.T) {
	_, bLink := link.NewPipeLink()
	fc := newFakeClock()
	server := NewStack(bLink, fc.Now)
	t.Cleanup(server.Close)

	ln := server.Listen(Endpoint{IP: [4]byte{10, 0, 0, 2}, Port: 9000})

	got := make(chan *Conn, 1)
	go func() { got <- ln.Accept() }()

	// Accept が待機に入るのを少し待ってから Close する。
	time.Sleep(20 * time.Millisecond)
	ln.Close()

	select {
	case c := <-got:
		if c != nil {
			t.Fatalf("Close 後の Accept は nil のはず: got %v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("Close しても Accept がブロックから戻らない")
	}

	ln.Close() // 二重 Close で panic しないこと。
}

// LISTEN が派生後も LISTEN のまま残ることを、別 remote から順次 SYN を送って確認する。
func TestListenerStaysListening(t *testing.T) {
	aLink, bLink := link.NewPipeLink()
	fc := newFakeClock()
	ipA := [4]byte{10, 0, 0, 1}
	ipB := [4]byte{10, 0, 0, 2}
	server := NewStack(bLink, fc.Now)
	client := NewStack(aLink, fc.Now)
	t.Cleanup(server.Close)
	t.Cleanup(client.Close)

	ln := server.Listen(Endpoint{IP: ipB, Port: 9000})
	t.Cleanup(ln.Close)

	for i := 0; i < 3; i++ {
		local := Endpoint{IP: ipA, Port: uint16(50000 + i)}
		client.Dial(local, Endpoint{IP: ipB, Port: 9000}, uint32(2000+i*1000))
		ac := ln.Accept()
		waitStateSleep(t, ac, Established)
	}
}

// 1 つの link 上で複数接続が同時に握手・データ・close しても race が無い (-race)。
func TestStackConcurrentConnsRaceClean(t *testing.T) {
	aLink, bLink := link.NewPipeLink()
	fc := newFakeClock()
	ipA := [4]byte{10, 0, 0, 1}
	ipB := [4]byte{10, 0, 0, 2}
	server := NewStack(bLink, fc.Now)
	client := NewStack(aLink, fc.Now)
	t.Cleanup(server.Close)
	t.Cleanup(client.Close)

	ln := server.Listen(Endpoint{IP: ipB, Port: 9000})
	t.Cleanup(ln.Close)

	const n = 8
	var wg sync.WaitGroup
	// accept 側を回し続ける goroutine。
	acceptDone := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			ac := ln.Accept()
			if ac == nil {
				return
			}
		}
		close(acceptDone)
	}()

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			local := Endpoint{IP: ipA, Port: uint16(40000 + i)}
			c := client.Dial(local, Endpoint{IP: ipB, Port: 9000}, uint32(1000+i*7919))
			deadline := time.Now().Add(2 * time.Second)
			for c.State() != Established && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			_, _ = c.Send([]byte("ping"))
			c.Close()
		}(i)
	}
	wg.Wait()
	select {
	case <-acceptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("全 accept が揃わなかった")
	}
}

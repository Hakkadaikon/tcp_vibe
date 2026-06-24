//go:build linux

package tcp

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/hakkadaikon/tcp_vibe/tcp/link"
)

// 主目的の統合テスト: localhost でランデブーサーバを立て、2 つの client が
// DialHolePunch で互いのアドレスを交換して直接 UDP を確立し、その Link 上で
// 2 つの自作 TCP スタックが握手 -> データ転送 (バイト一致) -> close まで通す。
// NAT は無いが手順 (ランデブー学習 -> 同時 punch -> 直接通信確立 -> 自作 TCP) は同一。
func TestHolePunch_RendezvousToTCPRoundTrip(t *testing.T) {
	r, err := link.NewRendezvous(0)
	if err != nil {
		t.Fatalf("NewRendezvous: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	srvPort, err := r.LocalPort()
	if err != nil {
		t.Fatalf("LocalPort: %v", err)
	}
	go r.Serve()

	const sid = "punch-it"
	const timeout = 5 * time.Second

	type dialRes struct {
		l   link.Link
		err error
	}
	ch := make(chan dialRes, 2)
	// 2 端をほぼ同時に起動する (両者が punch を送り合うのが hole punching の要)。
	for i := 0; i < 2; i++ {
		go func() {
			l, err := link.DialHolePunch(loIP, srvPort, sid, 0, timeout)
			ch <- dialRes{l, err}
		}()
	}
	r1 := <-ch
	r2 := <-ch
	if r1.err != nil || r2.err != nil {
		t.Fatalf("DialHolePunch 失敗: %v / %v", r1.err, r2.err)
	}
	t.Cleanup(func() { r1.l.Close(); r2.l.Close() })
	t.Logf("hole punch 確立: 2 つの直接 UDP Link が成立")

	// 確立した 2 つの Link を自作 TCP の土管にして握手〜データ〜close。
	client := NewConn(r1.l, time.Now, udpEPClient, udpEPServer)
	server := NewConn(r2.l, time.Now, udpEPServer, udpEPClient)
	client.SetMSL(200 * time.Millisecond)
	server.SetMSL(200 * time.Millisecond)

	stopC := Serve(client, 65535)
	stopS := Serve(server, 65535)
	t.Cleanup(stopC)
	t.Cleanup(stopS)

	server.PassiveOpen()
	client.ActiveOpen(2000)

	waitReachedEstablished(t, client)
	waitReachedEstablished(t, server)
	t.Logf("自作 TCP 握手成立 (hole punch した UDP 越し): client=%v server=%v", client.State(), server.State())

	msg := []byte("hello through a punched hole")
	if _, err := client.Send(msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got := recvAll(t, server, len(msg))
	if !bytes.Equal(got, msg) {
		t.Fatalf("受信不一致: got %q want %q", got, msg)
	}
	t.Logf("データ転送 OK: %q", got)

	client.Close()
	waitConnState(t, server, CloseWait)
	server.Close()
	waitConnState(t, client, TimeWait)
	waitConnState(t, server, Closed)
	waitConnState(t, client, Closed)
	t.Logf("close 完了: client=%v server=%v", client.State(), server.State())

	buf := make([]byte, 16)
	if _, err := server.Recv(buf); err != nil && err != io.EOF {
		t.Logf("server Recv after close: %v", err)
	}
}

// 相手不在ならランデブーで PEER を受け取れず、timeout で ErrPunchTimeout を返す。
func TestHolePunch_NoPeerTimesOut(t *testing.T) {
	r, err := link.NewRendezvous(0)
	if err != nil {
		t.Fatalf("NewRendezvous: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	srvPort, _ := r.LocalPort()
	go r.Serve()

	_, err = link.DialHolePunch(loIP, srvPort, "lonely-session", 0, 500*time.Millisecond)
	if !errors.Is(err, link.ErrPunchTimeout) {
		t.Fatalf("相手不在は ErrPunchTimeout のはず: got %v", err)
	}
}

// tcpdemo は自作 TCP スタックを TUN デバイス越しに動かすデモ。
// server (passive open) / client (active open) の 2 モードで握手し、
// ESTABLISHED に達したら close するまでを実演する。
//
// 実行には root と TUN デバイスが要る。手順は README を参照。
package main

import (
	"flag"
	"io"
	"log"
	"math/rand"
	"net"
	"time"

	"github.com/hakkadaikon/tcp_vibe/tcp"
)

func main() {
	mode := flag.String("mode", "client", "client (active open) または server (passive open)")
	ifName := flag.String("tun", "tun0", "TUN デバイス名")
	localIP := flag.String("local-ip", "10.0.0.1", "自分の IPv4 アドレス")
	localPort := flag.Uint("local-port", 9000, "自分のポート")
	remoteIP := flag.String("remote-ip", "10.0.0.2", "相手の IPv4 アドレス")
	remotePort := flag.Uint("remote-port", 9001, "相手のポート")
	debug := flag.Bool("debug", false, "TCP スタックの診断ログを出す (受信/送信/TUN I/O)")
	msl := flag.Duration("msl", 0, "MSL を上書き (例 2s)。TIME-WAIT は 2*MSL で抜ける。0 なら既定の 2 分 (TIME-WAIT 4 分)")
	flag.Parse()

	if *debug {
		tcp.Debug = func(f string, a ...any) { log.Printf("[tcp] "+f, a...) }
	}

	local := tcp.Endpoint{IP: parseIP(*localIP), Port: uint16(*localPort)}
	remote := tcp.Endpoint{IP: parseIP(*remoteIP), Port: uint16(*remotePort)}

	link, err := tcp.NewTUNLink(*ifName)
	if err != nil {
		log.Fatalf("TUN デバイス %q を開けない (root か? デバイスはあるか?): %v", *ifName, err)
	}

	conn := tcp.NewConn(link, time.Now, local, remote)
	if *msl > 0 {
		conn.SetMSL(*msl) // 握手前に注入。TIME-WAIT linger = 2*MSL
		log.Printf("MSL=%v に設定 (TIME-WAIT は %v で抜ける)", *msl, 2*(*msl))
	}
	stop := tcp.Serve(conn, 65535)
	defer stop()

	switch *mode {
	case "server":
		conn.PassiveOpen()
		log.Printf("passive open: %s:%d で SYN を待つ", *localIP, *localPort)
		runServer(conn)
	case "client":
		conn.ActiveOpen(rand.Uint32())
		log.Printf("active open: %s:%d -> %s:%d へ SYN 送出", *localIP, *localPort, *remoteIP, *remotePort)
		runClient(conn)
	default:
		log.Fatalf("不明な mode: %q (client か server)", *mode)
	}
}

// runClient は能動側のフロー。握手成立後にメッセージを送り、graceful close を主導する。
func runClient(conn *tcp.Conn) {
	if !waitEstablished(conn, 30*time.Second) {
		log.Fatalf("握手が成立しなかった: 現在 %v", conn.State())
	}
	log.Printf("ESTABLISHED 到達 (握手成立)")

	msg := []byte("hello from client\n")
	n, err := conn.Send(msg)
	if err != nil {
		log.Printf("Send 失敗: %v", err)
	} else {
		log.Printf("送信: %q (%d バイト)", msg, n)
	}
	// FIN より先に相手へデータが届くよう少し待つ。
	time.Sleep(500 * time.Millisecond)

	log.Printf("close 開始: client から FIN を送る")
	conn.Close()
	waitClientClosed(conn)
}

// waitClientClosed は能動 close 側の終端を待つ。能動側は FIN 交換後に TIME-WAIT へ
// 入り、2MSL 経過で CLOSED になる (RFC 9293 §3.10.4)。TIME-WAIT 到達は close 成功。
// CLOSED まで待つが、2MSL 未経過で抜けられなくてもそれは正常な挙動。
func waitClientClosed(conn *tcp.Conn) {
	if !waitState(conn, tcp.TimeWait, 30*time.Second) {
		log.Printf("TIME-WAIT に入らず終了: 現在 %v (握手到達=%v)", conn.State(), conn.ReachedEstablished())
		return
	}
	log.Printf("close 成功 (TIME-WAIT)。2MSL 経過で CLOSED へ")

	// 2MSL 経過を待つ。既定 (4 分) では長いので余裕を持たせる。--msl で短縮可。
	if !waitState(conn, tcp.Closed, 5*time.Minute) {
		log.Printf("TIME-WAIT のまま終了 (2MSL 未経過。これは正常。--msl を短くすると最後まで見られる)。握手到達=%v", conn.ReachedEstablished())
		return
	}
	log.Printf("CLOSED 到達。正常終了 (握手到達=%v)", conn.ReachedEstablished())
}

// runServer は受動側のフロー。自分からは閉じず、相手の FIN で CLOSE-WAIT に
// なったら自分も Close して LAST-ACK → CLOSED まで進める。
func runServer(conn *tcp.Conn) {
	if !waitEstablished(conn, 30*time.Second) {
		log.Fatalf("握手が成立しなかった: 現在 %v", conn.State())
	}
	log.Printf("ESTABLISHED 到達 (握手成立)")

	// 相手が FIN を送って CLOSE-WAIT になるまで Recv をポーリングし、受信データをログに出す。
	var received []byte
	buf := make([]byte, 4096)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && conn.State() != tcp.CloseWait {
		n, err := conn.Recv(buf)
		if n > 0 {
			received = append(received, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if n == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if len(received) > 0 {
		log.Printf("受信: %q (%d バイト)", received, len(received))
	}
	if conn.State() != tcp.CloseWait {
		log.Printf("相手の close を待たずタイムアウト: 現在 %v", conn.State())
		return
	}
	log.Printf("close 開始: 相手の FIN を受けて server からも FIN を送る")
	conn.Close()
	waitClosed(conn)
}

// waitClosed は CLOSED 到達まで待ち、節目をログに出す。
func waitClosed(conn *tcp.Conn) {
	if !waitState(conn, tcp.Closed, 30*time.Second) {
		log.Printf("CLOSED まで到達せず終了: 現在 %v", conn.State())
		return
	}
	log.Printf("CLOSED 到達。正常終了")
}

// waitEstablished は握手成立を待つ。現在値ではなく「ESTABLISHED に到達したか」で
// 判定するため、ESTABLISHED→CLOSE-WAIT が速くても取りこぼさない。
func waitEstablished(conn *tcp.Conn, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	nextReport := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if conn.ReachedEstablished() {
			return true
		}
		if time.Now().After(nextReport) {
			log.Printf("握手待機中: 現在 %v", conn.State())
			nextReport = nextReport.Add(1 * time.Second)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return conn.ReachedEstablished()
}

// waitState は conn が want になるまで timeout まで待つ。到達したら true。
// 終端状態 (CLOSE-WAIT/CLOSED) の検知に使う。1 秒ごとに現在状態をログに出す。
func waitState(conn *tcp.Conn, want tcp.State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	nextReport := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if conn.State() == want {
			return true
		}
		if time.Now().After(nextReport) {
			log.Printf("待機中: 現在 %v (目標 %v)", conn.State(), want)
			nextReport = nextReport.Add(1 * time.Second)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return conn.State() == want
}

// parseIP は "a.b.c.d" を [4]byte に変換する。不正なら fatal。
func parseIP(s string) [4]byte {
	v4 := net.ParseIP(s).To4()
	if v4 == nil {
		log.Fatalf("不正な IPv4 アドレス: %q", s)
	}
	return [4]byte{v4[0], v4[1], v4[2], v4[3]}
}

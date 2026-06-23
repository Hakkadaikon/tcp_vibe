// tcpdemo は自作 TCP スタックを TUN デバイス越しに動かすデモ。
// server (passive open) / client (active open) の 2 モードで握手し、
// ESTABLISHED に達したら close するまでを実演する。
//
// 実行には root と TUN デバイスが要る。手順は README を参照。
package main

import (
	"flag"
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

// runClient は能動側のフロー。握手成立を確認したら graceful close を主導する。
func runClient(conn *tcp.Conn) {
	if !waitEstablished(conn, 30*time.Second) {
		log.Fatalf("握手が成立しなかった: 現在 %v", conn.State())
	}
	log.Printf("ESTABLISHED 到達 (握手成立)")

	log.Printf("close 開始: client から FIN を送る")
	conn.Close()
	waitClosed(conn)
}

// runServer は受動側のフロー。自分からは閉じず、相手の FIN で CLOSE-WAIT に
// なったら自分も Close して LAST-ACK → CLOSED まで進める。
func runServer(conn *tcp.Conn) {
	if !waitEstablished(conn, 30*time.Second) {
		log.Fatalf("握手が成立しなかった: 現在 %v", conn.State())
	}
	log.Printf("ESTABLISHED 到達 (握手成立)")

	// 相手が FIN を送って CLOSE-WAIT になるまで待ち、そこで自分も閉じる。
	if !waitState(conn, tcp.CloseWait, 30*time.Second) {
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

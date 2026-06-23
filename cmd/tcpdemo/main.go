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
	case "client":
		conn.ActiveOpen(rand.Uint32())
		log.Printf("active open: %s:%d -> %s:%d へ SYN 送出", *localIP, *localPort, *remoteIP, *remotePort)
	default:
		log.Fatalf("不明な mode: %q (client か server)", *mode)
	}

	// ESTABLISHED まで待つ。
	if !waitFor(conn, tcp.Established, 30*time.Second) {
		log.Fatalf("握手が成立しなかった: 現在 %v", conn.State())
	}
	log.Printf("ESTABLISHED 到達")

	// 接続を確認したら close する。
	conn.Close()
	log.Printf("close 要求送出: 現在 %v", conn.State())

	// close 完了 (CLOSED) まで待って終了する。
	if !waitFor(conn, tcp.Closed, 30*time.Second) {
		log.Printf("CLOSED まで到達せず終了: 現在 %v", conn.State())
		return
	}
	log.Printf("CLOSED 到達。終了")
}

// waitFor は conn が want になるまで timeout まで待つ。到達したら true。
// 1 秒ごとに現在の状態をログに出し、どこで詰まっているか分かるようにする。
func waitFor(conn *tcp.Conn, want tcp.State, timeout time.Duration) bool {
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

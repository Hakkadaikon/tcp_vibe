//go:build linux

// rendezvous は UDP hole punching の足場となる小さな STUN ライクのサーバ。
// 同じ session ID で登録してきた 2 端を待ち、揃ったら互いの (サーバから見えた)
// グローバル IP:ポートを相手へ返す。tcpdemo --link=holepunch がこのサーバへ繋ぐ。
package main

import (
	"flag"
	"log"

	"github.com/hakkadaikon/tcp_vibe/tcp"
	"github.com/hakkadaikon/tcp_vibe/tcp/network"
)

func main() {
	port := flag.Uint("port", 7000, "待ち受け UDP ポート")
	debug := flag.Bool("debug", false, "診断ログを出す")
	flag.Parse()

	if *debug {
		network.Debug = func(f string, a ...any) { log.Printf("[rdv] "+f, a...) }
	}

	r, err := tcp.NewRendezvous(uint16(*port))
	if err != nil {
		log.Fatalf("ランデブーサーバを開けない (:%d): %v", *port, err)
	}
	log.Printf("ランデブーサーバ起動: UDP :%d で登録待ち", *port)
	if err := r.Serve(); err != nil {
		log.Fatalf("Serve: %v", err)
	}
}

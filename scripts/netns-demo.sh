#!/usr/bin/env bash
# 自作 TCP スタック同士を 1 台で通信させるための network namespace セットアップ。
#
# 仕組み:
#   netns "tcpa" と "tcpb" を veth ペアで繋ぐ。
#   各 netns に TUN を 1 本置き、相手の「サービス IP」宛を TUN に向ける。
#   自作スタックは TUN を read/write してパケットをやり取りする。
#
#   サービス IP (自作スタックが名乗る IP) は veth でも TUN デバイスでもなく、
#   「相手の netns の TUN に向けて転送される宛先」として使う。カーネルにその IP を
#   持たせない (= TUN/veth に振らない) ことで、カーネルが自作スタック宛に RST を
#   返すのを防ぐ。
#
# 使い方:
#   sudo ./scripts/netns-demo.sh up      # セットアップ
#   sudo ./scripts/netns-demo.sh down    # 後片付け
#   sudo ./scripts/netns-demo.sh run     # demo を両 netns で起動 (要 bin/tcpdemo)
set -euo pipefail

NS_A=tcpa
NS_B=tcpb
# veth: a 側 10.1.0.1, b 側 10.1.0.2 (実際にパケットを運ぶトランスポート)
VETH_A_IP=10.1.0.1
VETH_B_IP=10.1.0.2
# サービス IP: 自作スタックが名乗る TCP エンドポイント。カーネルには振らない。
SVC_A=10.0.0.1
SVC_B=10.0.0.2

up() {
	ip netns add "$NS_A"
	ip netns add "$NS_B"

	# veth で 2 つの netns を直結。
	ip link add veth-a netns "$NS_A" type veth peer name veth-b netns "$NS_B"
	ip -n "$NS_A" addr add "$VETH_A_IP/24" dev veth-a
	ip -n "$NS_B" addr add "$VETH_B_IP/24" dev veth-b
	ip -n "$NS_A" link set veth-a up
	ip -n "$NS_B" link set veth-b up
	ip -n "$NS_A" link set lo up
	ip -n "$NS_B" link set lo up

	# 各 netns に TUN を作る (persistent。アプリが attach する)。
	# IP は振らない (カーネルに自作スタックの IP を持たせない)。
	ip -n "$NS_A" tuntap add dev tun0 mode tun
	ip -n "$NS_B" tuntap add dev tun0 mode tun
	ip -n "$NS_A" link set tun0 up
	ip -n "$NS_B" link set tun0 up

	# ルーティング:
	# A から見て、自分のサービス IP (SVC_A) 宛は自分の TUN に落とす
	# (= 自作スタックが読む)。相手のサービス IP (SVC_B) 宛は veth で B へ送る。
	ip -n "$NS_A" route add "$SVC_A" dev tun0
	ip -n "$NS_A" route add "$SVC_B" via "$VETH_B_IP" dev veth-a
	ip -n "$NS_B" route add "$SVC_B" dev tun0
	ip -n "$NS_B" route add "$SVC_A" via "$VETH_A_IP" dev veth-b

	# B に届いた SVC_B 宛を TUN へ流すため、転送を有効化。
	ip netns exec "$NS_A" sysctl -q -w net.ipv4.ip_forward=1
	ip netns exec "$NS_B" sysctl -q -w net.ipv4.ip_forward=1

	# reverse path filter を無効化 (TUN から入る非対称経路のパケットが
	# rp_filter で落とされるのを防ぐ。よくある落とし穴)。
	ip netns exec "$NS_A" sysctl -q -w net.ipv4.conf.all.rp_filter=0
	ip netns exec "$NS_B" sysctl -q -w net.ipv4.conf.all.rp_filter=0
	ip netns exec "$NS_A" sysctl -q -w net.ipv4.conf.tun0.rp_filter=0 || true
	ip netns exec "$NS_B" sysctl -q -w net.ipv4.conf.tun0.rp_filter=0 || true

	# カーネルが自作スタック宛 (SVC_*) に勝手に RST/ICMP を返さないよう、
	# 念のため両 netns で OUTPUT の RST を抑止する。
	ip netns exec "$NS_A" iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP || true
	ip netns exec "$NS_B" iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP || true

	echo "up: netns $NS_A (svc $SVC_A) <-> $NS_B (svc $SVC_B)"
	echo "run server: sudo ip netns exec $NS_A ./bin/tcpdemo --mode=server --tun=tun0 --local-ip=$SVC_A --local-port=9000 --remote-ip=$SVC_B --remote-port=9001 --debug"
	echo "run client: sudo ip netns exec $NS_B ./bin/tcpdemo --mode=client --tun=tun0 --local-ip=$SVC_B --local-port=9001 --remote-ip=$SVC_A --remote-port=9000 --debug"
}

down() {
	ip netns del "$NS_A" 2>/dev/null || true
	ip netns del "$NS_B" 2>/dev/null || true
	echo "down: removed $NS_A and $NS_B"
}

case "${1:-}" in
up) up ;;
down) down ;;
*)
	echo "usage: sudo $0 {up|down}" >&2
	exit 1
	;;
esac

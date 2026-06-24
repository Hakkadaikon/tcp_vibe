# 動かし方

ビルドとテスト、リンク別の起動手順、e2e テスト、TIME-WAIT の待ち時間を説明する。
リンク層の違いと選び方は [networking.md](networking.md) を参照。

ビルドとテストは justfile のレシピを `just <レシピ名>` で実行する。
リンク層によって特権の要否と動かせる環境が変わる。
特権の要らない UDP トンネル、Unix domain socket、hole punching は root のないサンドボックスでも別プロセス間の実通信を確かめられる。
TUN は実機の root が要る。

## ビルドとテスト

| レシピ | 何をするか |
|---|---|
| `just setup` | aqua で Go と just を取得 |
| `just build` | ビルド |
| `just demo-build` | 実機デモのバイナリを `bin/tcpdemo` に出力 |
| `just test` | race 検出付きで全テストを実行 |
| `just check` | 静的解析、整形チェック、race 検出付きテストを順に通す (提出前) |
| `just fmt` | 整形 |
| `just vet` | 静的解析 |
| `just cover` | カバレッジ計測 |
| `just test-flaky` | race 検出付きで複数回実行 |
| `just e2e` | 2 プロセス間の実通信を検証 |

## リンク別の起動手順

`--local-ip` などは自作スタックのエンドポイントで、`--udp-*` や `--unix-*` の運搬先とは別物である。
特権の要らないリンクから順に示す。

### UDP トンネル

localhost の 2 ポートを使い、server (受動オープン) と client (能動オープン) を別プロセスとして起動する。

1. デモのバイナリをビルドする。

```sh
just demo-build
```

2. server と client を別ポートで起動する。

```sh
./bin/tcpdemo --mode=server --link=udp --udp-local-port=40000 --udp-remote-port=40001 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001 --msl=2s
./bin/tcpdemo --mode=client --link=udp --udp-local-port=40001 --udp-remote-port=40000 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

別ホスト間で動かすときは `--udp-remote-host` に相手の IP を渡す。

### Unix domain socket

運搬層が違うだけで扱いは UDP トンネルと同じである。
`--link=unix` を選び、運搬先をパスで指定する。

```sh
./bin/tcpdemo --mode=server --link=unix --unix-local=/tmp/tcpvibe-a.sock --unix-remote=/tmp/tcpvibe-b.sock --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001 --msl=2s
./bin/tcpdemo --mode=client --link=unix --unix-local=/tmp/tcpvibe-b.sock --unix-remote=/tmp/tcpvibe-a.sock --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

### UDP hole punching (NAT 越え)

ランデブーサーバ、server、client の 3 プロセス構成である。
ローカルポートは自動割当なので指定しない。
仕組みは [networking.md](networking.md) を参照。

1. ランデブーサーバを起動する。

```sh
go run ./cmd/rendezvous --port=7000
```

2. server と client を同じ `--session` で起動する。両端で同じセッション ID にする。

```sh
./bin/tcpdemo --mode=server --link=holepunch --rendezvous=127.0.0.1:7000 --session=demo --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001 --msl=2s
./bin/tcpdemo --mode=client --link=holepunch --rendezvous=127.0.0.1:7000 --session=demo --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

### TUN (実機)

TUN は L3 デバイスで、TCP は自作スタックが処理する一方、IP パケットの配送はカーネルに任せる。
カーネルの IP と TCP が同じホストに同居するため、自作スタック宛のセグメントにカーネルが RST を返すことがある。
手順 2 で iptables による RST 抑止を挟むのはこのためである。

root のある Linux 実機で、TUN デバイス経由で自作スタック同士の握手を実演できる。

1. TUN デバイスを作り、アドレスを割り当てて起動する。

```sh
sudo ip tuntap add dev tun0 mode tun
sudo ip addr add 10.0.0.1/24 dev tun0
sudo ip link set tun0 up
```

2. カーネルの RST を抑止する。対象サブネット発の RST を止める。

```sh
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s 10.0.0.0/24 -j DROP
```

3. 別々の TUN デバイスを用意し、一方を server、もう一方を client として起動すると、握手から close までが進む。

```sh
sudo ./bin/tcpdemo --mode=server --link=tun --tun=tun0 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001
sudo ./bin/tcpdemo --mode=client --link=tun --tun=tun1 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000
```

## e2e テスト

手動の 2 プロセス起動は `just e2e` で自動化している。
`e2e/e2e_test.go` が tcpdemo をビルドし、server と client の 2 プロセスを UDP トンネル越しに起動して、握手からデータ転送、close までが成立することを検証する。
両プロセスが exit 0 で終わり、受信バイトが送信バイトと一致することを確かめる。

```sh
just e2e
```

build tag `e2e` で分離してあるため、通常の `just test` や `just check` では走らない。

## TIME-WAIT の待ち時間

能動 close 側 (client) は FIN 交換のあと TIME-WAIT に入り、2MSL 待ってから CLOSED になる (RFC 9293 通り)。
既定では MSL が 2 分なので TIME-WAIT は 4 分続き、デモで CLOSED まで待つと時間がかかる。
最後まで見たいときは `--msl=2s` のように短い MSL を渡すと、TIME-WAIT が 2*MSL (この例で 4 秒) で抜けて CLOSED に達する。

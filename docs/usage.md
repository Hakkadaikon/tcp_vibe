# 動かし方

ビルドとテストは justfile のレシピを `just <レシピ名>` で実行する。
リンク層によって、特権の要否と動かせる環境が変わる。
特権の要らない Unix domain socket と UDP トンネルは、root のないサンドボックスでも別プロセス間の実通信を確かめられる。
TUN と AF_PACKET は実機の root が要る。

## セットアップとビルド

```sh
just setup          # aqua で Go と just を取得
just build          # ビルド
just test           # race 検出付きで全テストを実行
```

提出前の検証は `check` にまとめてある。
静的解析、整形差分のチェック、race 検出を有効にした複数回のテストを順に通す。

```sh
just check
```

このほかに `just fmt` (整形)、`just vet` (静的解析)、`just cover` (カバレッジ計測)、`just test-flaky` (race 検出付きで複数回実行) がある。

## root も TUN も要らないユーザー空間デモ

UDP ソケットを使うリンク (`--link=udp`) と Unix domain socket を使うリンク (`--link=unix`) は、raw socket でないため特権を要さない。
自作スタックが組み立てた IP パケットを土管のペイロードとしてそのまま運び、受信側はその中身を IP パケットとして自作スタックに渡す。
TCP の状態遷移とセグメント処理は自作スタックがすべて行い、土管はパケットを運ぶだけである。
ソケットは標準の `net` パッケージを使わず `syscall` で直接開く。

まずバイナリをビルドする。
sudo で実行する場合に備え、独立したファイル `bin/tcpdemo` に出力する。

```sh
just demo-build
```

localhost の 2 ポートを使い、server (受動オープン) と client (能動オープン) を別プロセスとして起動する。
`--local-ip` などは自作スタックのエンドポイントで、`--udp-*` の運搬先とは別物である。

```sh
./bin/tcpdemo --mode=server --link=udp --udp-local-port=40000 --udp-remote-port=40001 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001 --msl=2s
./bin/tcpdemo --mode=client --link=udp --udp-local-port=40001 --udp-remote-port=40000 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

別ホスト間で動かすときは `--udp-remote-host` に相手の IP を渡す。
Unix domain socket リンクは運搬層が違うだけで扱いは同じで、`--link=unix` を選び、運搬先をパスで指定する (`--unix-local <path>`、`--unix-remote <path>`)。

## e2e テスト

手動の 2 プロセス起動は `just e2e` で自動化している。
`e2e/e2e_test.go` が tcpdemo をビルドし、server と client の 2 プロセスを UDP トンネル越しに起動して、握手からデータ転送、close までが成立することを検証する。
両プロセスが exit 0 で終わり、受信バイトが送信バイトと一致することを確かめる。

```sh
just e2e
```

build tag `e2e` で分離してあるため、通常の `just test` や `just check` では走らない。
リンク単体の往復と、土管越しの握手からデータ転送、close までは `tcp/udplink_test.go`、`tcp/udploopback_test.go`、`tcp/unixlink_test.go` で検証している。
これらは特権を要さないため、リポジトリのテストとして常時実行される。

## 実機での実通信 (TUN)

root のある Linux 実機では、TUN デバイス経由で自作スタック同士の握手を実演できる。
まず TUN デバイスを作り、アドレスを割り当てて起動する。

```sh
sudo ip tuntap add dev tun0 mode tun
sudo ip addr add 10.0.0.1/24 dev tun0
sudo ip link set tun0 up
```

カーネルの TCP/IP と同じサブネットを共有すると、自作スタック宛のセグメントにカーネルが RST を返すことがある。
自作スタック同士だけで通信するなら不要だが、カーネルの TCP と混在させるときは、対象サブネット発の RST を抑止する。

```sh
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s 10.0.0.0/24 -j DROP
```

別々の TUN デバイスを用意し、一方を server、もう一方を client として起動すると、握手から close までが進む。

```sh
sudo ./bin/tcpdemo --mode=server --tun=tun0 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001
sudo ./bin/tcpdemo --mode=client --tun=tun1 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000
```

## TIME-WAIT の待ち時間

能動 close 側 (client) は FIN 交換のあと TIME-WAIT に入り、2MSL 待ってから CLOSED になる (RFC 9293 通り)。
既定では MSL が 2 分なので TIME-WAIT は 4 分続き、デモで CLOSED まで待つと時間がかかる。
最後まで見たいときは `--msl=2s` のように短い MSL を渡すと、TIME-WAIT が 2*MSL (この例で 4 秒) で抜けて CLOSED に達する。

```sh
./bin/tcpdemo --mode=client --link=udp --udp-local-port=40001 --udp-remote-port=40000 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

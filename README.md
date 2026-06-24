# tcp_vibe

Go の標準 `net` パッケージを使わずに、TCP をゼロから実装したプロトコルスタックです。
RFC 9293 (Transmission Control Protocol) の状態機械と、RFC 5961 (TCP の盲目的な in-window 攻撃への堅牢化) の対策を実装しています。
握手と close だけでなく、双方向のデータ転送、動的な再送タイムアウト、輻輳制御、フロー制御、主要な TCP オプションの折衝、複数接続の多重化までを備えています。
ヘッダの組み立てやバイト列の変換といった部分も、標準ライブラリに頼らず自前で書いています。

対象環境は x86/64 の Linux VPS です。

## 何を自作しているか

このスタックはカーネルの TCP/IP を迂回し、IPv4 と TCP のセグメントを自分で組み立てて解釈します。
TCP の状態遷移とセグメント処理のロジックそのものを書くことを主眼に置いているため、ソケット API も標準の `encoding/binary` も使わず、必要な部品を下から積み上げています。

実装済みの機能は次のとおりです。

- TCP/IP チェックサム：擬似ヘッダを含めた ones' complement sum を計算する。
- IPv4 ヘッダと TCP ヘッダの marshal/parse：標準ライブラリに依存せず、バイト列との相互変換を行う。
- シーケンス番号の比較：mod 2^32 の環状空間で大小を判定する。
- IPv4 パケットの再分割：連続したバイトストリームから個々のパケットを切り出す。パケットが分割して届く場合と、複数が連結して届く場合の両方に対応する。
- TCP 状態機械：11 状態を持ち、3-way handshake (能動オープン、受動オープン、同時オープン)、graceful close、TIME-WAIT を扱う。
- RFC 5961 の challenge ACK：blind RST、blind SYN、blind data injection への対策として challenge ACK を返し、その送出にレート制限をかける。
- データ転送：Send と Recv でユーザバッファを介して双方向にデータをやり取りする。送信は MSS 単位にセグメント化し、受信は順不同で届いたセグメントを再組立てして連続したストリームに戻す。
- 動的 RTO (RFC 6298)：RTT を測り、SRTT と RTTVAR から再送タイムアウトを動的に算出する。再送したセグメントの ACK は RTT 標本に使わない (Karn のアルゴリズム)。
- 輻輳制御 (RFC 5681)：slow start、congestion avoidance、fast retransmit、fast recovery を実装し、cwnd と ssthresh で送出量を調整する。
- TCP オプションの折衝 (RFC 7323、RFC 2018)：MSS、window scale、timestamps、SACK を握手時に折衝する。window scale により受信窓を 64KB 超に広げられる。
- PAWS (RFC 7323)：timestamp を見て、巻き戻った古い重複セグメントを棄却する。
- フロー制御 (RFC 9293、RFC 1122)：受信窓を動的に更新し (窓は縮めない)、相手が窓 0 を広告したときは persist timer で zero-window probe を送る。silly window syndrome を送受信の両側で回避し、Nagle アルゴリズムと delayed ACK で小さなセグメントを抑える。
- keepalive (RFC 1122)：既定では無効で、設定で有効にできる。
- 複数接続の多重化：接続を 4-tuple (送信元 IP とポート、宛先 IP とポート) で識別し、Listener と Accept で複数の接続を同時に扱う。
- 再送タイマ：RTO を指数バックオフで延ばし、再送回数が上限に達した接続を終了する。
- 受信ループ：受信を単一の goroutine で回し、送信は mutex で直列化する。
- リンク層の抽象化：テスト用のメモリ仮想リンクと、実機用の AF_PACKET ドライバを同じインタフェースの背後に置く。AF_PACKET ドライバは CAP_NET_RAW を要する。

これらの挙動はテストで検証しています。

## 構成

実装は `tcp/` 配下にあります。

- `seq.go`：シーケンス番号の mod 2^32 環状算術。
- `checksum.go`：擬似ヘッダ込みのチェックサム計算。
- `ipv4.go`：IPv4 ヘッダの marshal/parse。
- `header.go`：TCP ヘッダの marshal/parse。
- `bytes.go`：ビッグエンディアン変換。
- `framing.go`：バイトストリームからの IPv4 パケット再分割。
- `link.go`：リンク層の抽象 (Link) と、テスト用のメモリ仮想リンク。
- `statemachine.go`：TCP 状態機械。
- `tcb.go`：状態定義と接続ごとの制御ブロック。
- `data.go`：Send と Recv によるデータの送受信と、ユーザバッファの管理。
- `rto.go`：RTT 計測にもとづく動的 RTO の算出。
- `congestion.go`：cwnd と ssthresh による輻輳制御。
- `options.go`：TCP オプションの marshal/parse と折衝。
- `paws.go`：timestamp による古い重複セグメントの棄却。
- `flowcontrol.go`：受信窓の更新、zero-window probe、silly window syndrome の回避、Nagle と delayed ACK。
- `sack.go`：受信側の SACK ブロックの生成。
- `keepalive.go`：keepalive プローブ。
- `conntable.go`：4-tuple で接続を引く接続テーブル。
- `listener.go`：Listener と、接続を多重化する Stack。
- `recvloop.go`：受信ループと、接続を駆動する Serve ヘルパ。
- `afpacket_linux.go`：実機用の AF_PACKET ドライバ。
- `tun_linux.go`：実機用の TUN ドライバ (L3、IP パケットを直接やり取り)。
- `udplink.go`：UDP ソケットを IP パケットの土管として使うドライバ。特権を要さず、自作スタック同士の実通信を root 無しで実演できる。
- `cmd/tcpdemo`：TUN または UDP トンネル越しに握手と close を実演するデモ。

## 必要なもの

ツールは aqua で固定しています。
Go は `.aqua` 配下に取得され、システムの Go には依存しません。
コマンドは justfile のレシピを `just <レシピ名>` で実行します。
justfile が aqua 配下の Go を PATH に通すため、別途の設定は要りません。

aqua 本体だけは事前に PATH に通しておいてください。

## 使い方

まず初回のセットアップで Go と just を取得します。

```sh
just setup
```

ビルドとテストは次のとおりです。

```sh
just build          # ビルド
just test           # race 検出付きで全テストを実行
```

提出前の検証ゲートは `check` にまとめてあります。
これは静的解析 (`vet`)、整形差分のチェック、race 検出を有効にした複数回のテストを順に通します。

```sh
just check
```

このほかに次のレシピがあります。

```sh
just fmt            # 整形
just vet            # 静的解析
just cover          # カバレッジ計測
just test-flaky     # 全テストを race 検出付きで複数回実行
```

## 制約と前提

実通信を行う構成には、いくつかの未実装と限定があります。

- このスタックはカーネルの TCP/IP を迂回するため、実際にパケットを送受信するには AF_PACKET 用に CAP_NET_RAW (通常は root) が必要です。
- 動作確認はメモリ仮想リンク上で行っています。AF_PACKET ドライバは ARP と Ethernet フレームの完全な処理を実装しておらず、peer の MAC アドレスを固定した最小構成です。
- SACK は受信側でブロックを広告するところまでです。送信側で SACK 済みの範囲を飛ばして再送する選択的再送は実装していません。
- window scale により受信窓を 64KB 超に広げられますが、状態遷移とプロトコルの正しさを主眼としており、転送性能の最適化やバッファ管理の作り込みは限定的です。

## 実機での実通信手順

root のある Linux 実機では、TUN デバイス経由で自作スタック同士の握手を実演できます。

まず TUN デバイスを作り、アドレスを割り当てて起動します。

```
sudo ip tuntap add dev tun0 mode tun
sudo ip addr add 10.0.0.1/24 dev tun0
sudo ip link set tun0 up
```

カーネルの TCP/IP と同じサブネットを共有すると、自作スタック宛のセグメントにカーネルが RST を返すことがあります。
自作スタック同士だけで通信するなら不要ですが、カーネル TCP と混在させるときは、対象サブネット発の RST を抑止します。

```
sudo iptables -A OUTPUT -p tcp --tcp-flags RST RST -s 10.0.0.0/24 -j DROP
```

デモのバイナリを先にビルドします。
sudo で実行するため、独立したファイル `bin/tcpdemo` に出力します。

```
just demo-build
```

デモは server (受動オープン) と client (能動オープン) の 2 モードを持ちます。
別々の TUN デバイスを用意し、一方を server、もう一方を client として起動すると、握手から close までが進みます。

```
sudo ./bin/tcpdemo --mode=server --tun=tun0 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001
sudo ./bin/tcpdemo --mode=client --tun=tun1 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000
```

能動 close 側 (client) は FIN 交換後に TIME-WAIT へ入り、2MSL 待ってから CLOSED になります (RFC 9293 通り)。
既定では MSL=2 分なので TIME-WAIT は 4 分続き、デモでは CLOSED まで待つと時間がかかります。
最後まで見たいときは `--msl=2s` のように短い MSL を渡すと、TIME-WAIT が 2*MSL (この例で 4 秒) で抜けて CLOSED に到達します。

```
sudo ./bin/tcpdemo --mode=client --tun=tun1 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

## root も TUN も要らないユーザー空間デモ

TUN も AF_PACKET もカーネルに依存し特権を要します。
これを避けるため、UDP ソケットを「IP パケットを運ぶ土管」として使うリンク (`--link=udp`) を用意しています。

自作スタックが組み立てた IP パケット (IPv4+TCP) を UDP のペイロードとして相手へ送り、受信側は UDP データグラムの中身をそのまま IP パケットとして自作スタックに渡します。
カーネルの TCP/IP ロジックは一切経由しません。
UDP は単なるトランスポート (ケーブル代わり) で、TCP の状態遷移とセグメント処理は自作スタックがすべて行います。
raw socket ではないので特権が要らず、root のないサンドボックスでも動きます。

UDP ソケットは標準の `net` パッケージを使わず `syscall` で直接開きます (`Socket`、`Bind`、`Sendto`、`Recvfrom`、`Close`)。

localhost の 2 ポートを使い、server と client を別プロセスとして起動します。
IP アドレスとポート (`--local-ip` 等) は自作スタックのエンドポイントで、UDP の運搬先 (`--udp-*`) とは別物です。

```
just demo-build
./bin/tcpdemo --mode=server --link=udp --udp-local-port=40000 --udp-remote-port=40001 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001 --msl=2s
./bin/tcpdemo --mode=client --link=udp --udp-local-port=40001 --udp-remote-port=40000 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000 --msl=2s
```

別ホスト間で動かすときは `--udp-remote-host` に相手の IP を渡します。

上記の手動 2 プロセス起動は `just e2e` で自動化しています。
`e2e/e2e_test.go` が tcpdemo をビルドし、server / client の 2 プロセスを UDP トンネル越しに起動して、握手 → データ転送 → close が成立すること (両プロセスが exit 0、受信バイトが一致) を検証します。
bash で手起動して echo を目視する必要はありません。

```
just e2e
```

build tag `e2e` で分離してあるため通常の `just test` / `just check` では走りません。

このリンクの往復と、UDP トンネル越しの握手からデータ転送、close までは `tcp/udplink_test.go` と `tcp/udploopback_test.go` で検証しています。
どちらも特権を要さないため、このリポジトリのテストとして常時実行されます。

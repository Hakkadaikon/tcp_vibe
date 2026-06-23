# tcp_vibe

Go の標準 `net` パッケージを使わずに、TCP をゼロから実装したプロトコルスタックです。
RFC 9293 (Transmission Control Protocol) の状態機械と、RFC 5961 (TCP の盲目的な in-window 攻撃への堅牢化) の対策を実装しています。
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
- `recvloop.go`：受信ループ。
- `afpacket_linux.go`：実機用の AF_PACKET ドライバ。

## 必要なもの

ツールは aqua で固定しています。
Go と just は `.aqua` 配下に取得され、システムの Go には依存しません。
コマンドは justfile のレシピを薄いラッパ `./j` 経由で実行します。

aqua 本体だけは事前に PATH に通しておいてください。

## 使い方

まず初回のセットアップで Go と just を取得します。

```sh
./j setup
```

ビルドとテストは次のとおりです。

```sh
./j build          # ビルド
./j test           # race 検出付きで全テストを実行
```

提出前の検証ゲートは `check` にまとめてあります。
これは静的解析 (`vet`)、整形差分のチェック、race 検出を有効にした複数回のテストを順に通します。

```sh
./j check
```

このほかに次のレシピがあります。

```sh
./j fmt            # 整形
./j vet            # 静的解析
./j cover          # カバレッジ計測
./j test-flaky     # 全テストを race 検出付きで複数回実行
```

## 制約と前提

実通信を行う構成には、いくつかの未実装と限定があります。

- このスタックはカーネルの TCP/IP を迂回するため、実際にパケットを送受信するには AF_PACKET 用に CAP_NET_RAW (通常は root) が必要です。
- 動作確認はメモリ仮想リンク上で行っています。AF_PACKET ドライバは ARP と Ethernet フレームの完全な処理を実装しておらず、peer の MAC アドレスを固定した最小構成です。
- データ転送はユーザバッファへの蓄積が最小限の実装です。状態遷移とプロトコルの正しさを主眼としており、転送性能やバッファ管理は作り込んでいません。

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

デモは server (受動オープン) と client (能動オープン) の 2 モードを持ちます。
別々の TUN デバイスを用意し、一方を server、もう一方を client として起動すると、握手から close までが進みます。

```
sudo ./cmd/tcpdemo --mode=server --tun=tun0 --local-ip=10.0.0.1 --local-port=9000 --remote-ip=10.0.0.2 --remote-port=9001
sudo ./cmd/tcpdemo --mode=client --tun=tun1 --local-ip=10.0.0.2 --local-port=9001 --remote-ip=10.0.0.1 --remote-port=9000
```

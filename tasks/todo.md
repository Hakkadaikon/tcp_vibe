# TODO: 自作 TCP プロトコルスタック (RFC 9293 + RFC 5961)

## スコープ確定事項

- 生 TCP/IP スタックを Go で自作。標準 `net` 不使用、syscall とリンク層 I/O のみ。
- 土台: リンク層は `io.ReadWriteCloser` 相当の抽象 (`Link`)。
  - **メモリ仮想リンク**で全検証(このサンドボックスは CAP_NET_RAW/ADMIN 無し → raw socket/TUN 不可)。
  - AF_PACKET / TUN ドライバは Link 実装として用意し、権限が取れたら差すだけ。
- パッケージ管理: aqua。コマンド実行: justfile。x86/64 Linux VPS 想定。
- 検証フル: TLA+ (状態機械) + Lean (seq/再分割の往復) + PBT + Gherkin 配線。modeler/prover に並列委譲。
- 標準ライブラリ最小化: encoding/binary 等も自作。OSS は不使用(自作)。テストの testing は使う。

## 検証ゲート3問 (TCP スコープで答え直し)

### Q1. 状態遷移・並行・プロトコル → YES。TLA+ 確定
固める対象:
- 接続ライフサイクル: 11 状態 (CLOSED/LISTEN/SYN-SENT/SYN-RECEIVED/ESTABLISHED/
  FIN-WAIT-1/FIN-WAIT-2/CLOSE-WAIT/CLOSING/LAST-ACK/TIME-WAIT)
- 3way handshake / 同時オープン / graceful & simultaneous close
- seq/ack 空間、受信ウィンドウ、再送、RFC 5961 のチャレンジ ACK

安全性 INV 候補(過剰抽出は安全):
- INV-A: 状態遷移は RFC 9293 図の許可された辺のみ
- INV-B: ESTABLISHED に達するのは正しい 3way 完了時のみ
- INV-C: 受理する seg は受信ウィンドウ内 (acceptability test, RFC 9293 3.4)
- INV-D: バイト列のフレーミング(IP/TCP ヘッダ境界)が任意チャンク分割で正しく再構成
- INV-E: in-flight (未 ACK) セグメント集合は送信で増え ACK でのみ減る
- INV-F (5961): in-window だが SEG.SEQ != RCV.NXT の RST は challenge ACK、即リセットしない
- INV-G (5961): SYN は in-window でも challenge ACK
- INV-H (5961): Data injection — ACK が許容窓外なら challenge ACK / ドロップ

活性 LIVE 候補:
- LIVE-1: 送った SYN はいつか ESTABLISHED か失敗で決着
- LIVE-2: FIN を送ったらいつか CLOSED へ
- LIVE-3: 再送はいつか ACK されるか接続が落ちる

### Q2. Lean → 限定 YES (優先度低)
候補(カタログに載せる):
- TCP/IP チェックサム: `verify(data ++ checksum(data)) = true`、ones-complement の結合則
- seq 比較の mod-2^32 順序の健全性 (SEG_LT/SEG_LEQ の全順序性, RFC 1323 風)
- ヘッダ encode/decode 往復 `decode(encode(h)) = h`
判断: TLA+ INV-D + PBT で二重に縛れる部分は Lean スキップ既定。
seq の mod 2^32 比較は無限/環状で PBT が弱い → Lean 候補本命。
着手は TLA+/PBT 後、損益分岐を再評価。

### Q3. 橋渡し
- 各 INV ↔ T-ID 1対1
- INV-D は PBT 主役 (testing/quick): 任意チャンク分割で同じパケット列に戻る
- LIVE は fake clock 注入の決定論テスト(再送/TIME-WAIT 境界ちょうど/直前)
- 反例トレース → `tasks/loopeng/*.feature` → テスト配線

## 段取り(並列最大化)

- [x] P0: 環境土台 (aqua + justfile + go mod + ./j ラッパ) — commit ffbac73
- [x] P1: RFC 抽出 → `tasks/loopeng/requirements.md` (R-001〜R-117, INV-001〜016, 状態遷移表)
- [ ] P2: TLA+ spec → modeler に TLC + mutation 委譲 (background)
- [x] P3: test-design 振る舞い網羅抽出 → `tasks/test-extract.md` (T-001〜T-065, 0段クローズ)
- [ ] P4: Lean (checksum/seq 比較) → prover 委譲 (P2 と並行可)
- [ ] P5: TDD 実装 (T-ID 順 Red→Green→Refactor)
  - [x] seq 算術 (mod 2^32 比較) — commit 9052903, T-010〜014
  - [x] ヘッダ encode/decode (IP, TCP) + checksum — commit 82acb8c〜ac1a795, T-001〜009
  - [x] Link 抽象 + メモリ仮想リンク + Clock seam — commit 34d37cb
  - [x] フレーミング再分割 — commit 84bc2c6, T-015〜020
  - [x] 状態機械 (11 状態遷移) — commit 1be5443, TLA+ 由来境界を配線
  - [x] 3way handshake (能動/受動/同時オープン) — 状態機械に含む
  - [x] graceful close + TIME-WAIT (2MSL 修正済) — 状態機械に含む
  - [x] RFC 5961 challenge ACK (RST/SYN/Data) — 状態機械に含む, T-040〜047
  - [x] FV 反映 (対蹠点 subtlety, RFC1071 ゴールデン) — commit f86df83
  - [x] 再送タイマ (RTO, fake clock) — commit 5a18ac0, T-051〜054
  - [x] challenge ACK throttling (RFC 5961 §7) — commit 5a18ac0, T-049
  - [x] 受信ループ goroutine + 並行性テスト (T-065) — commit 8f74009
  - [x] TCP checksum 受信検証 (INV-010 TCP 側) — commit 8f74009
  - [ ] データ転送 user buffer 蓄積 — 現在最小 (RCV.NXT 前進のみ, 別途)
- [ ] P6: AF_PACKET Link 実装 (権限が取れたら実通信。サンドボックスは CAP 無しで実行不可)
- [x] P7: 自己再検証 `./j check` (vet + fmt-check + test -race -count=5) 全 PASS / flaky 無し
- [x] P8: micro-commit (管理番号・手法用語を本体に漏らさず継続中)
- [x] P9: `tasks/retrospective.md` (KPT + トレーサビリティ)

## レビュー欄

完了。ブランチ feat/tcp-stack、16 コミット、65 テスト、`./j check` 全 PASS flaky 無し。
- 検証3層フル実施 (TLA+ で状態機械 520 states + mutation、Lean で seq/checksum 証明、TDD+PBT)。
- FV が真の設計穴を3つ検出 (M2 reset 根拠 / M11 origin 値 / 反対称律の対蹠点)、全て実装テストに配線。
- 自己再検証で 2 バグ修正 (2MSL→正しくは 2*MSL、TCP checksum 受信検証漏れ)。
- 過剰設計レビューで未使用 API 除去。
- 残課題: AF_PACKET 実通信 (要 root)、ARP/Ethernet 完全対応、データ転送バッファ、輻輳制御、Dial/Listen API。
- 管理番号・手法用語は tasks/ 内に閉じ、本体・コミット・README に漏らさず。

## P10: 完全ユーザー空間実装 (root/TUN 不要)

目標: AF_PACKET/TUN (カーネル依存・要 root・netns/iptables 必要) を避け、
ユーザー空間だけで 2 つの自作 TCP スタックが通信できるようにする。

設計判断:
- 「完全ユーザー空間」= カーネルの TCP/IP スタック・ルーティング・特権を介さず、
  ユーザー空間プロセス間でIPパケットを直接運ぶ。root 不要・netns 不要・どこでも動く。
- 実現: Link を「UDP ソケット越しに IP パケットをトンネルする」実装にする (udpLink)。
  - UDP は「パケットを運ぶ土管」であって TCP ロジックは 100% 自作スタック。
  - カーネルの TCP は一切経由しない。raw socket でないので CAP も要らない。
  - 2 プロセス (or 同一プロセス2 goroutine) がそれぞれ自作スタックを持ち UDP で繋がる。

タスク:
- [x] udpLink: net パッケージ不使用方針との折り合い。UDP ソケットは syscall で直接開く
      (socket/bind/sendto/recvfrom)。net.UDPConn を使うと「標準 net 不使用」に反するため
      syscall で自作する。Link インターフェースを満たす。
- [x] udpLink のテスト: 2 つの udpLink を localhost UDP で繋ぎ、パケット往復。
- [x] loopback 相当を udpLink で: 2 スタックを UDP 越しに握手〜データ転送〜close。
      これが root 無しで通れば「ユーザー空間で実通信」の証明。
- [x] cmd/tcpdemo に --link=udp モードを足す (TUN か UDP かを選べる)。
- [x] README に「root 不要のユーザー空間デモ」手順を追記。
- [x] 自己検証 ./j check 全 PASS、udpLink デモが実際に root 無しで通る。

## P11: フルスタック化 (4領域、聞かず自走で実装)

現状は接続ライフサイクル+基本データ転送までで、TCP の中核機能が欠けている。
コード量が少ないのはこのため。以下4領域を埋めてフルスタックに近づける。

実装順 (依存順):
1. [ ] TCP オプション交渉 — MSS 折衝 / window scale / timestamps(PAWS) / SACK の
       parse+marshal+処理。他機能の土台 (wscale→受信窓, timestamps→RTO/PAWS)。
2. [ ] 動的 RTO + 輻輳制御 — SRTT/RTTVAR/Karn (RFC6298) で RTO 動的計算。
       slow start / congestion avoidance / cwnd / ssthresh / fast retransmit (3 dup ACK)。
3. [ ] フロー制御の作り込み — 受信窓の動的更新(バッファ消費連動)、zero-window probe/
       persist timer、SWS 回避、Nagle、delayed ACK、keepalive。
4. [ ] 複数接続多重化 — 1 リンク上で 4-tuple (src/dst IP+port) demux、Listener/Accept、
       複数同時接続。受信ループの上位構造。並行性は -race で固める。

各領域: TDD、境界/property テスト、loopback/e2e で実通信検証。管理番号・手法用語は
本体に漏らさない。状態機械の骨格 (握手/close/RFC5961) は壊さない。

## P11 進捗 (2巡目)
- [x] 4領域 RFC 取得 (5681/6298/7323/2018/1122)
- [x] 4領域 0段抽出 → tasks/loopeng/req-{congestion,options,flowcontrol,mux}.md
- [x] オプション parse/marshal 実装 (commit 17764ac) — 検証独立の純関数層
- [x] TLA+ 検証 background 並列: 多重化(並行性), 輻輳制御(cwnd状態機械), フロー制御(活性)
- [x] Lean 検証 background: RTO健全性/wscale clamp/PAWS単調/4-tuple
- [x] 検証完了後、境界を持って statemachine 系を順に実装:
      オプション折衝(握手) → 輻輳制御 → フロー制御 → 多重化(受信ループ上位)
- [x] 各領域 Gherkin → テスト配線

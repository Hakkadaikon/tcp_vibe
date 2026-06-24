# レトロスペクティブ: 自作 TCP プロトコルスタック (RFC 9293 + RFC 5961)

前作 (net.Conn フレーミング) の handoff を引き継ぎつつ、goal で範囲が「生 TCP/IP スタック自作」に
拡大したセッションの振り返り。次セッションのための自己完結メモ。

## 成果サマリ

- ブランチ `feat/tcp-stack`、16 コミット (micro-commit)。本体 ~3000 LOC、65 テスト関数。
- `./j check` (vet + fmt-check + test -race -count=5) 全 PASS、flaky 無し。
- 検証3層をフルで回した: TLA+ (状態機械), Lean (seq/checksum), TDD+PBT (実装)。

## Keep (うまくいった)

- **検証ゲート3問を着手前に答えた**。実装ファーストに流れず、TLA+/Lean/TDD の層を plan で先に決めた。
  結果、状態機械の「reset の根拠」「origin の値」という外しやすい穴を mutation で先回り検出できた。
- **並列化を最大化**。RFC 抽出2本 → modeler+prover を background → coder を逐次/並列で。
  土台非依存の純関数層 (seq/checksum/header/framing/link) を FV と独立に先行実装し、待ち時間を消した。
- **modeler の mutation oracle が真の穴を2つ発見** (M2: 窓外RSTがreset → INV-005 強化, M11: 同時オープンの
  origin取り違え → INV-014 強化)。安全性 INV が「遷移先」しか見ておらず「根拠/値」を縛れていなかった。
  これを Go テストに1対1配線し、実際に mutation 注入してテストが落ちることを確認した。
- **prover が素朴な反対称律の偽を発見** (対蹠点 a-b=2^31 で両方向の順序が壊れる)。
  実装は RFC 通り正しい (窓<2^31 で回避) が、この境界をコメント+テストで固定し回帰防止にした。
- **自分で再検証した** (agent 報告を鵜呑みにせず)。2MSL バグ (restartTimeWait が 1MSL だった) を
  自分のコードレビューで発見・修正。テストも 2MSL の絶対値を固定するよう強化した。
- **TCP checksum の受信検証漏れ (INV-010 の TCP 側) を自分で発見・修正**。recvloop が IPv4 checksum しか
  見ていなかった。dispatch に TCPChecksum 検証を足し、不一致破棄のテストを追加。
- **過剰設計レビューを入れた**。未使用 API (SeqAdd/SeqGEQ/SystemClock) を削除。
  ただし RFC 構造として正当なフィールド (wl1/wl2, UrgentPtr, 全制御ビット) は残す判断をした。

## Problem (詰まった/課題)

- **サンドボックスに CAP_NET_RAW が無い**。AF_PACKET/TUN/RAW_INET 全て operation not permitted。
  当初ユーザが AF_PACKET を選んだが実行不可と判明 → メモリ仮想リンクで全検証する方針に切替。
  AF_PACKET ドライバはコンパイル可能なスケルトンとして残し、権限のある実機で差し替え可能にした。
- **aqua-proxy の鶏卵問題**。proxy 経由の初回 just 起動が AQUA_GLOBAL_CONFIG 未設定でレジストリ再取得 →
  read-only に当たり失敗。`./j` ラッパで環境を固めてから実体 just を直接起動して回避。
- **GOCACHE が read-only**。`~/.cache/go-build` 不可 → repo 内 `.gocache` に向けた (justfile/`./j` に集約)。
- **bash の cwd が subshell で tcp/ に残る罠**。`cd tcp` 後にラッパ `./j` が見つからない。フルパス/明示 cd で対処。

## Try (次に活かす)

- AF_PACKET の実通信検証は実機 (root or CAP_NET_RAW) で。ARP + Ethernet フレーム完全対応が残課題。
- データ転送のユーザバッファ蓄積、out-of-order 再組立、輻輳制御 (slow start) は未実装。次の塊。
- 受信ループと Conn を繋ぐ高レベル API (Dial/Listen 相当) はまだ。recvloop は内部 receiver 止まり。

## トレーサビリティ (INV/T-ID → 実装/テスト/コミット)

| 領域 | 要件/INV | テスト (T-ID) | 実装 | commit |
|---|---|---|---|---|
| seq 比較 | R-010,011 / INV-001,002 | T-010〜013 | seq.go | 9052903, f86df83 |
| checksum | R-100〜103 / INV-010 | T-004〜007 | checksum.go | dd352f0, f86df83 |
| IPv4/TCP header | R-014 | T-001〜003,008,009 | ipv4.go, header.go | 355931d, ac1a795 |
| framing | R-007 / INV-C,D | T-015〜020 | framing.go | 84bc2c6 |
| link/clock seam | — | (link_test) | link.go | 34d37cb |
| 状態機械 | 状態遷移表 / INV-A,B,011,014 | T-021〜028,056〜062 | statemachine.go, tcb.go | 6bd4f00, 1be5443 |
| RFC 5961 | R-110〜117 / INV-005,006,007,013 | T-040〜049 | statemachine.go | 1be5443, 5a18ac0 |
| 再送 | R-090〜093 / LIVE-3 | T-051〜054 | statemachine.go | 5a18ac0 |
| 受信ループ | 並行設計 / INV-010(TCP) | T-065, recvloop tests | recvloop.go | 8f74009 |
| AF_PACKET | — | RequiresCapability | afpacket_linux.go | cdbe18c |

## FV 成果物の所在 (git 管理外)

- TLA+: `tasks/loopeng/TCP.tla`, `.cfg`, `TCP.feature` (Gherkin 12 シナリオ), `TCP.extract.md` (S-001〜051)
  - 520 distinct states 安全性 No error、mutation 13/13 KILLED survivor 0、LIVE-2 は条件付き活性に分類
- Lean: `tasks/fv/TcpFv/` (Seq.lean, Checksum.lean, Audit.lean)
  - lake build 成功、sorry ゼロ、native_decide/SAT 公理なし (全カーネル検証)
- 要件台帳: `tasks/loopeng/requirements.md` (R-001〜117, INV-001〜016, 状態遷移表, 5961 三チェック擬似コード)
- テスト観点: `tasks/test-extract.md` (T-001〜065, 0段クローズ)

管理番号・手法用語は tasks/ 内に閉じ、本体ソース・コミット・README には漏らしていない (規範どおり)。

# 総合振り返りレポート: 自作 TCP プロトコルスタック

tcp_vibe プロジェクト全体の振り返り。`tasks/handoff.md` の「net.Conn 上の最小フレーミング」
から始まり、生 TCP/IP スタック → 実通信 → 完全ユーザー空間 → フルスタック化 → 甘さレビューと
不足実装、まで到達した全工程を総括する。git 管理外。

## 1. 最終到達点

- 標準 `net` 不使用・`encoding/binary` も自作の TCP/IP スタック (RFC 9293 + 5961 + 5681 + 6298 + 7323 + 2018 + 1122)。
- 本体 41 コミット、~3854 LOC。テスト ~5873 LOC、235 テスト/fuzz 関数。
- `just check` (vet + fmt-check + `go test -race -count=5`) 全 PASS、flaky 無し。クリーンビルド可。
- `just e2e` で別プロセス2つが UDP トンネル越しに実通信 (root 不要、握手〜データ〜close)。
- パッケージ管理 aqua、タスクランナー justfile (`just <recipe>`)。

### 実装した機能の全体像
- 基盤: seq mod 2^32 比較 / checksum (擬似ヘッダ) / IPv4・TCP ヘッダ marshal・parse / IPv4 再分割 (Framer)
- 状態機械: 11 状態、3way handshake (能動/受動/同時)、graceful & simultaneous close、TIME-WAIT
- RFC 5961: blind RST/SYN/data injection への challenge ACK、ACK レート制限
- データ転送: Send/Recv、out-of-order 再組立て、MSS セグメント化
- 動的 RTO (6298): SRTT/RTTVAR、Karn、指数バックオフ
- 輻輳制御 (5681): slow start / congestion avoidance / fast retransmit / fast recovery
- オプション (7323/2018): MSS 折衝、window scale (受信窓 64KB 超)、timestamps、SACK 受信広告
- PAWS (7323): 古い重複セグメントの破棄
- フロー制御 (9293/1122): 受信窓動的更新 (窓を縮めない)、zero-window probe/persist、SWS 回避、Nagle、delayed ACK
- keepalive (1122): 既定 OFF
- 多重化: 4-tuple demux、Listener/Accept、複数同時接続、接続テーブル (test-and-set)
- リンク層: メモリ仮想 / TUN (L3) / UDP トンネル (ユーザー空間) / AF_PACKET (L2)

## 2. 検証三層の実績

### TLA+ (設計の網羅検査 + mutation)
| spec | 状態数 | mutation | 主な発見 |
|---|---|---|---|
| TCP.tla (状態機械) | 520 | 13 kill survivor 0 | M2 reset の根拠 / M11 origin の値 / LIVE-2 は条件付き活性 |
| cc.tla (輻輳制御) | 949 | 14 kill survivor 0 | 初回半減 vs 再送済み保持 / 積極性上限 / FR inflate |
| Mux.tla (多重化) | 27211 | 11 kill + 1 equiv | test-and-set 必須を反例で実証 / 全単射違反 3 件 |
| fc.tla (フロー制御) | 1,021,992 | 11 kill + 2 equiv | Nagle+delayed ACK デッドロック検出、override が唯一の脱出路 |

### Lean (実装の数学的証明、sorry/native_decide 0)
- Seq (mod 2^32 比較、対蹠点の subtlety)、Checksum (ones'-comp 往復・結合則)
- Rto (整数 EWMA 非発散・下限・バックオフ単調)、WScale (clamp・オーバーフロー無し)
- Paws (timestamp wrap 単調)、Mux (demux キー単射)
- Reasm (out-of-order 再組立て健全性・冪等・単調)、Cwnd (増加則・byte-counting 非負)
- SeqGo (Go 実装 SeqLT と msb 定義の窓<2^31 仮説下一致 = 橋渡し)
- 全 96+ 定理、Audit.lean で標準 3 公理以内を監査。

### TDD + PBT + Gherkin 配線
- 各 INV/証明済み性質を property-based (testing/quick) と境界テストに 1 対 1 配線。
- フレーミング再分割・out-of-order・cwnd 増加則・seq 比較は PBT 主役。
- 反例由来 Gherkin を回帰テストに (管理番号・手法用語は本体に漏らさず)。
- parse 系に Go fuzz target。

## 3. 実際に踏んで潰したバグ (机上検証だけでは出なかったもの)

実通信・レビューで検出し修正:
1. 非 IPv4 パケットで受信ループが停止 (TUN に IPv6 が流れて Framer が致命扱い)
2. 受信ウィンドウ未広告で FIN が受理性テストに弾かれる
3. 握手成立の観測取りこぼし (ESTABLISHED→CLOSE-WAIT が速すぎた)
4. TIME-WAIT が 2MSL でなく 1MSL (定数の取り違え)
5. TCP checksum の受信検証漏れ (IPv4 のみ検証していた)
6. SYN-RECEIVED が acceptable でない ACK でも ESTABLISHED 昇格
7. 受信窓 uint16 + window scale で 64KB 超バッファ破壊
8. dispatch が IPv4 TotalLength 無視 (パディング混入)
9. CLOSED 接続がテーブル未回収で 4-tuple 再利用不能 + goroutine リーク
10. TIME-WAIT 置換の ISS ゲート欠如 / FIN 再送無応答 / fast retransmit 二重送出 / Accept 永久ブロック / 非同期 CLOSE 未実装

## 4. Keep (うまくいった)

- **検証ゲート3問を着手前に通した**。実装ファーストに流れず TLA+/Lean/TDD の層を先に決めた。
  輻輳制御の「初回半減 vs 保持」、多重化の「test-and-set 必須」など、実装で外しやすい穴を mutation で先回り検出できた。
- **並列化を最大化**。RFC 抽出 4 領域同時、modeler/prover を background で並走、coder を逐次/並列。
  土台非依存の純関数層を検証と独立に先行実装し待ち時間を消した。
- **検証が真の設計穴を多数検出**。各 mutation oracle が survivor を出し、それを INV 追加で潰す過程が
  そのまま実装の正しさ要件になった。Lean は「対蹠点で反対称律が偽」「整数 RTO でないと証明と乖離」を発見。
- **agent 報告を鵜呑みにせず自分で再検証**。2MSL バグ・TCP checksum 漏れ・RTT 配線・fake clock 起点問題を
  自分のレビュー/実テストで発見・修正した。
- **3観点の独立並列レビュー (実装/テスト/形式検証) が同じ critical を裏付けた**。複数観点一致で信頼度が上がり、
  優先度づけが明確になった。
- **micro-commit を徹底**。41 コミットすべて論理単位。管理番号・手法用語を本体・コミット・README に漏らさず、
  痕跡を tasks/ 内に閉じた。
- **実通信まで到達**。机上 (メモリ仮想リンク) だけでなく TUN・UDP・netns で本物のパケットを流し、
  そこでしか出ないバグ (上記 1〜5) を踏んで潰せた。
- **ユーザー空間化で実用性が跳ねた**。UDP トンネルで root 不要・どこでも動く実通信を実現し、e2e で自動化。

## 5. Problem (詰まった/環境の罠)

- **サンドボックスに特権が無い**。CAP_NET_RAW/ADMIN 無し、/dev/net/tun 無し、no_new_privs。
  raw socket/TUN が一切開けず、当初の AF_PACKET 案が実行不能と判明 → メモリ仮想リンク + UDP トンネルへ転換。
- **aqua-proxy の鶏卵問題**。当初 ./j ラッパで回避したが、justfile が env を export していれば
  `just` 直接で足りると後で判明し ./j を削除した (過剰だった)。
- **GOCACHE が read-only**。`~/.cache/go-build` 不可 → repo 内 .gocache に向け justfile に集約。
- **fake clock の起点 0**。timestamp RTT テストで TSecr=0 が「ACK 無し」規約値と衝突。テスト側で時刻を進めて回避。
- **coder のツールコール parse エラーで中断**。最終報告生成時に1回。実装は完了していたが RTT 配線の
  最終確認とテストの起点問題が残り、自分で補完した。報告を鵜呑みにしない姿勢が効いた。
- **同一ホストの TUN 2 本は互いに繋がらない**。netns + veth で分離する必要があった。

## 6. Try (次に活かす / 残課題)

- SACK 送信側の選択再送 (SACKed をスキップする再送)。受信側広告までは実装済み。
- カーネル TCP (nc/curl) との相互運用テスト (RFC 準拠の本当の証明)。iptables で RST 抑止が要る。
- 輻輳制御の ECN、PMTU discovery、輻輳制御 spec の SMSS>1 でのモデル検査 (現在 SMSS=1 抽象)。
- フロー制御 spec の活性をより大きい状態空間で (apalache の記号検査)。
- AF_PACKET ドライバの ARP/Ethernet 完全対応 (現在は固定 peer MAC の最小形)。
- ブランチ feat/tcp-stack は未 push。

## 7. トレーサビリティ (主要 INV/性質 → 検証層 → 実装/テスト → commit)

| 領域 | 検証 (TLA+/Lean) | 実装 | commit |
|---|---|---|---|
| seq 比較 | Lean Seq/SeqGo | seq.go | 9052903, d56e18e |
| checksum | Lean Checksum | checksum.go | dd352f0, f86df83 |
| 状態機械 | TLA+ TCP.tla (520) | statemachine.go, tcb.go | 6bd4f00, 1be5443 |
| RFC 5961 | TLA+ TCP.tla | statemachine.go | 1be5443, 5a18ac0 |
| データ転送 | Lean Reasm | data.go | 43f6cfb, d56e18e |
| 動的 RTO | Lean Rto | rto.go | 0c28a49, d56e18e |
| 輻輳制御 | TLA+ cc.tla (949) + Lean Cwnd | congestion.go | 0c28a49, d56e18e |
| オプション/PAWS | Lean WScale/Paws | options.go, paws.go | 17764ac, 292457a |
| フロー制御 | TLA+ fc.tla (102万) | flowcontrol.go | cf4a3a5 |
| 多重化 | TLA+ Mux.tla (27211) + Lean Mux | conntable.go, listener.go | d4b71a2 |
| SACK/keepalive | (折衝のみ検証) | sack.go, keepalive.go | 89babdb |
| 実害バグ修正 | レビュー 3 観点 | 横断 | 549902c |
| テスト補強 | レビュー | _test.go 群 | 6cd48c1 |

検証成果物: TLA+ 4 spec (`tasks/loopeng/*.tla` + *.feature)、Lean 10 module (`tasks/fv/TcpFv/TcpFv/*.lean`)、
要件台帳 (`tasks/loopeng/req-*.md`, requirements.md)、テスト観点 (`tasks/test-extract.md`)、
レビュー (`tasks/review.md`)。すべて git 管理外で、本体は手法を知らない人が読んで完結する状態を保った。

## 8. 当初 handoff との対比

handoff は「net.Conn 上の最小フレーミングプロトコル (多重化・heartbeat・graceful close)」を想定していた。
実際の goal はそれを大きく超え、生 TCP/IP スタックの自作 → 実通信 → 完全ユーザー空間 → フルスタック化 →
甘さレビューと不足実装、に発展した。handoff が確立した「検証ゲートを先に通す」「mutation で穴を先回り」
「agent 報告を自分で再検証」「micro-commit + 痕跡を tasks/ に閉じる」という成功フローは、
スコープが桁違いに大きくなっても有効だった。

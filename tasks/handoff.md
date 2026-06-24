# ハンドオフ: net.Conn の上の最小フレーミングプロトコルを TLA+ + test-design で実装する

別セッションでゼロから実装するための自己完結メモ。
このファイルだけ読めば、検証ゲートから再開できる状態にしてある。
前作 `../token_bucket_late_limiter`(lazy 補充トークンバケット)の成功フローを踏襲する。

## 何を作るか(スコープは確定済み)

標準 `net.Conn`(TCP)の上に載せる、最小の双方向アプリプロトコル。生 TCP スタックは書かない。

- フレーム = `[4byte length(BE)][1byte type][payload]`
  - type ∈ {REQ, RESP, PING, PONG, CLOSE}
- 1 コネクション上で複数 REQ を ID 付きで同時 in-flight(多重化)。RESP は ID で対応付け
- 一定間隔で PING、相手の PONG/任意フレームが idle timeout 内に来なければ接続を落とす
- graceful close(CLOSE 交換)
- サーバ/クライアント両方が動く実用形。標準ライブラリのみ。Go module 名は前作踏襲で `github.com/hakkadaikon/tcp_vibe`

設計の肝(前作のモデル検査が「先に」確定させた類の勘所。実装で気づくのではなく先に決める):
- フレーム境界の再分割が load-bearing。TCP は「1 read = 1 フレーム」を保証しない。部分読み・複数フレーム同時到着を必ず正しく分割する
- 受信ループは単一 goroutine、送信は mutex で直列化。アプリ状態(in-flight 集合・接続状態)は1つのクリティカルセクションで守る
- `length` を信じて事前確保しない(trust boundary)。上限を設けて超過フレームは接続エラー

## 検証ゲート3問(コードを1行書く前に通すこと。前作 Keep の核心 = 実装ファーストに流れない)

### Q1. 状態遷移・並行・プロトコルがあるか? → YES。TLA+ 確定

固める対象:
- 接続ライフサイクル: `Connecting → Open → Closing → Closed`(+ heartbeat 副状態 Healthy/Idle)
- 多重化: 同時 in-flight な REQ-ID 集合、RESP の ID 対応、CLOSE 後に新規 REQ を出さない相互排他
- 順序/結合: バイト列をフレーム境界で正しく再分割(部分読み・連結到着)

安全性 INV 候補(過剰抽出は安全・漏れは危険。迷ったら載せる):
- INV-A: CLOSE 送信後は REQ を送らない
- INV-B: 未知 ID / 既決着 ID の RESP を受理しない(二重決着なし)
- INV-C: length 健全性(0 と上限の境界、上限超で接続エラー、過剰確保なし)
- INV-D: 任意のチャンク分割で読んでも、再構成されるフレーム列は送信フレーム列と一致(再分割の正しさ)
- INV-E: in-flight 集合は REQ 発行で増え RESP/timeout でのみ減る(取りこぼし・幽霊エントリなし)

活性 LIVE 候補(★前作 YAGNI で見送った活性を、今回は本気でやる):
- LIVE-1: 送った REQ はいつか RESP かタイムアウトで必ず決着する(宙吊りなし)
- LIVE-2: idle timeout に達したら接続はいつか Closed へ進む
- 公平性条件の置き方を明示する(weak/strong fairness をどの遷移に張るか)。重ければ modeler に委譲して到達性+可能なら temporal まで

### Q2. Lean が要る数学的性質があるか? → 限定的に YES。ただし優先度低、まず TLA+ + PBT で二重に縛れるか見る

候補(カタログには載せる。"挙げ過ぎは安全"):
- フレーミング往復 `decode(encode(frame)) = frame`(エンコード/デコードの一致)
- length 境界(0 / 最大 / 部分読みの結合)の健全性
判断: 前作と同じく、TLA+ の INV-D と PBT で二重に縛れるなら Lean はスキップが既定線。
着手は TLA+/PBT を回した後。損益分岐(テスト・網羅検査で届かない無限領域か?)を再評価してから入れる。
前作の学び: lazy 補充は線形で自明だったので Lean スキップが正解だった。今回の往復も同程度なら同じ判断になる見込み。

### Q3. 上2層の性質を TDD のテストリストにどう橋渡しするか

- 各 INV ↔ T-ID を1対1(前作の INV↔T-ID 表を踏襲)
- INV-D(再分割)は PBT が主役: 任意のチャンク分割列で読んでも同じフレーム列に戻る。標準 `testing/quick` だけで足りる
- LIVE は決定論テストへ: fake clock を注入し「REQ 後、RESP 来ず timeout 時刻到達 → REQ がエラー決着する」を境界ちょうど/直前で
- 反例トレース → Gherkin 受け入れ仕様(`tasks/loopeng/*.feature`)

3問すべて答えた。検証層が plan に立っている(実装タスクだけを並べない)。

## 実装の段取り(前作の成功フローそのまま)

1. `tasks/loopeng/` で NL → EARS 抽出台帳(採番 + トレーサビリティ表が必須ゲート)→ `.tla` + `.cfg`
2. modeler に TLC + mutation testing を委譲(重い検査をメイン文脈から切り離す)
   - mutation で safety では kill できない survivor が出たら → 正常系/活性の対象として分類し、対応する正常系テストを必ず1本立てる(前作 M6→T-060 の後追いを、今回は先回りで)
   - INV の範囲縛りは TypeOK ではなく専用 INV の管轄に置く(前作で clamp 除去 M1 を Inv1 で kill できた設計判断。再現する)
3. `tasks/test-extract.md` で test-design の振る舞い網羅抽出(0段クローズ = 未チェック0件・欠番チェック)
4. 1 と 3 は独立なので並行起動(modeler に TLC、メインで振る舞い抽出)
5. coder で TDD: T-ID を1つずつ Red → Green → Refactor。PBT は testing/quick のみ
6. 自分で再検証: agent 報告を鵜呑みにせず `go vet ./...` と `go test -race -count=5 ./...` を実行し、全 PASS かつ flaky でないことを目で確認
7. micro-commit で論理単位に分割(本体ソース・コミットに管理番号 S-/R-/T-/INV- やループ手法用語を漏らさない。痕跡は tasks/ 内だけ)
8. 終わったら `tasks/retrospective.md` を書く(前作と同形式の KPT + トレーサビリティ)

## 前作から引き継ぐ具体テクニック

- clock seam を最小に: 既定 `time.Now`、テスト時だけ `func() time.Time` を注入。heartbeat の timeout 境界(ちょうど/直前)を決定論検証
- 環境の罠: サンドボックスは `~/.cache/go-build` が read-only。`GOCACHE=$TMPDIR/gocache`(または repo 内)に向ける。`-race` を必ず通す
- 観測用ヘルパは mutex 経由で内部状態を読む(並行テストでも安全に)

## 着手時の最初の一手

新セッションで plan mode に入り、この handoff を読んで `tasks/todo.md` にチェック可能項目で落とす。
リポは `tcp_vibe` を使う(git 初期化済み・LICENSE のみ)。`tasks/` は `.gitignore` 済みで git 管理外。
`go mod init github.com/hakkadaikon/tcp_vibe` から。

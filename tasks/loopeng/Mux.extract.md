# 接続多重化 (4-tuple demux + 接続テーブル並行管理) — 抽出台帳 (0段)

出典: `tasks/loopeng/req-mux.md`(RFC 9293 から多重化スコープに抽出済み)を一次台帳とし、
本ファイルは TLA+ モデル化スコープに絞った採番チェックリスト + トレーサビリティ表。
S-NNN は本モデルの抽出単位 ID(req-mux.md の R-MUX-xxx / INV-MUX-xxx へ逆引き可能)。

原則: 過剰抽出は安全・漏れは危険。スコープ外は §「対象外」に明記。

スコープの核心は **並行性**: demux(受信)と OPEN/CLOSE(アプリ)が同一 connTable を
並行に read/write する競合。insert を test-and-set(無ければ作る)にしないと
4-tuple 一意性(INV-MUX-001)が破れることを、モデル検査と mutation で示す。

## 台帳(採番チェックリスト)

### 接続識別 / テーブル構造
- [x] S-001 接続 = 4-tuple (lip,lport,rip,rport) で一意 (R-MUX-001, req-mux:4)
      → The system SHALL identify each connection by its 4-tuple. (ubiquitous)
- [x] S-002 同一 local port に remote 違いで複数接続が相乗りできる (R-MUX-004, req-mux:5)
      → WHERE two connections differ in remote endpoint the system SHALL keep them as distinct TCBs on the same local port. (optional)
- [x] S-003 接続 = ちょうど 1 TCB、TCB 不在 = CLOSED (R-MUX-018, req-mux:21)
      → The system SHALL maintain exactly one TCB per live connection and treat absence of a TCB as CLOSED. (ubiquitous)

### OPEN(アプリ → テーブル書込み)
- [x] S-004 passive OPEN は LISTEN TCB を生成し既存に影響しない (R-MUX-010, req-mux:16)
      → WHEN the application issues passive OPEN the system SHALL create a LISTEN TCB without disturbing existing TCBs. (event)
- [x] S-005 active OPEN は 4-tuple を確保し SYN-SENT TCB を作る (R-MUX-001/018, req-mux:4)
      → WHEN the application issues active OPEN for a free 4-tuple the system SHALL insert a SYN-SENT TCB for it. (event)
- [x] S-006 既存 4-tuple への OPEN は "already exists"(拒否、上書きしない) (R-MUX-021, req-mux:18)
      → IF the application opens an already-occupied 4-tuple THEN the system SHALL reject with already-exists and SHALL NOT overwrite. (unwanted)

### demux(受信 → テーブル read + 派生)
- [x] S-007 完全一致 TCB があれば dispatch(その TCB にのみ届く) (R-MUX-002, req-mux:9)
      → WHEN a segment arrives and an exact-match TCB exists the system SHALL dispatch it to that TCB only. (event)
- [x] S-008 完全一致なし + LISTEN(local 一致, remote ワイルド)に SYN → 新 TCB を SYN-RECEIVED で派生 (R-MUX-012, req-mux:11)
      → WHEN a SYN arrives with no exact match but a matching LISTEN exists the system SHALL derive a new SYN-RECEIVED TCB. (event)
- [x] S-009 派生時 LISTEN TCB は LISTEN のまま残る(非破壊) (R-MUX-003, req-mux:17)
      → WHILE deriving from a LISTEN the system SHALL keep the LISTEN TCB in LISTEN. (state)
- [x] S-010 完全一致なし + LISTEN に ACK → RST を返す (req-mux:11)
      → IF an ACK arrives with no exact match but a matching LISTEN exists THEN the system SHALL send a RST. (unwanted)
- [x] S-011 完全一致なし + LISTEN に RST → 無視(drop) (req-mux:11)
      → IF a RST arrives with no exact match but a matching LISTEN exists THEN the system SHALL drop it silently. (unwanted)
- [x] S-012 完全一致も LISTEN も無し(CLOSED) + 非RST → RST を 1 つ生成 (R-MUX-004 demux step3 / INV-MUX-004, req-mux:12)
      → IF a non-RST segment arrives with no matching TCB THEN the system SHALL generate exactly one RST. (unwanted)
- [x] S-013 完全一致も LISTEN も無し + RST 含み → 破棄(無応答) (req-mux:12)
      → IF a RST segment arrives with no matching TCB THEN the system SHALL discard it without responding. (unwanted)
- [x] S-014 broadcast/multicast/不正 src の SYN は破棄 (R-MUX-009, req-mux:13)
      → IF a SYN arrives from a broadcast/multicast/invalid source THEN the system SHALL discard it. (unwanted)

### establishment / teardown(テーブル状態遷移)
- [x] S-015 SYN-RECEIVED で SYN を ack され ESTABLISHED へ (派生 TCB の前進)
      → WHEN a derived SYN-RECEIVED TCB gets its SYN acked the system SHALL move it to ESTABLISHED. (event)
- [x] S-016 SYN-SENT TCB が SYN,ACK を受け ESTABLISHED へ (active の前進)
      → WHEN an active SYN-SENT TCB receives SYN,ACK the system SHALL move it to ESTABLISHED. (event)
- [x] S-017 接続 CLOSE → TIME-WAIT へ(2MSL 待ち)、4-tuple は予約され続ける (R-MUX-024, req-mux:22)
      → WHEN the application closes an ESTABLISHED connection the system SHALL move it to TIME-WAIT. (event)
- [x] S-018 TIME-WAIT が 2MSL 満了 → TCB 削除(CLOSED) (R-MUX-024, req-mux:22)
      → WHEN the TIME-WAIT timer expires the system SHALL delete the TCB. (event)

### incarnation / 競合
- [x] S-019 TIME-WAIT 中の 4-tuple に新 SYN: 新 ISS が条件を満たせば新 incarnation を受理 (R-MUX-024, req-mux:22)
      → WHERE a SYN for a TIME-WAIT 4-tuple has a sufficiently new ISS the system SHALL reopen a new incarnation. (optional)
- [x] S-020 SYN-RECEIVED(passive 由来)に RST → LISTEN へ復帰(派生元へ戻る) (R-MUX-027, req-mux:23)
      → IF a RST arrives in a passive-origin SYN-RECEIVED TCB THEN the system SHALL return it to LISTEN. (unwanted)

### 並行性(本モデルの主眼。req-mux:31-35)
- [x] S-021 demux と OPEN/CLOSE は同一 connTable を並行に read/write する (req-mux:32)
      → The system SHALL allow demux and application OPEN/CLOSE to operate on the connection table concurrently. (ubiquitous)
- [x] S-022 insert は test-and-set(無ければ作る)で行い、占有済み 4-tuple を上書きしない (req-mux:32)
      → WHEN inserting a TCB the system SHALL atomically test-and-set so that an occupied 4-tuple is never overwritten. (event)
- [x] S-023 LISTEN への同時 SYN(別 remote)は別 4-tuple へ複数派生する (req-mux:33)
      → WHEN multiple SYNs arrive concurrently at a LISTEN the system SHALL derive distinct TCBs for distinct 4-tuples. (event)
- [x] S-024 TIME-WAIT 削除と新 TCB 生成の競合があってもテーブル不変条件を保つ (req-mux:33)
      → WHILE a TIME-WAIT deletion races a new insert the system SHALL keep table invariants. (state)

### 不変条件(安全性)
- [x] S-025 INV-MUX-001(核心) 各 4-tuple に非 TIME-WAIT TCB は高々 1 つ (INV-MUX-001, req-mux:26)
      → The system SHALL never hold two non-TIME-WAIT TCBs for the same 4-tuple. (ubiquitous / INV)
- [x] S-026 INV-MUX-002 dispatch される TCB は 4-tuple 完全一致のもののみ (INV-MUX-002, req-mux:27)
      → The system SHALL dispatch only to an exact-4-tuple-match TCB. (ubiquitous / INV)
- [x] S-027 INV-MUX-003 LISTEN は SYN 派生後も LISTEN のまま (INV-MUX-003, req-mux:28)
      → The system SHALL keep a LISTEN TCB in LISTEN across derivations. (ubiquitous / INV)
- [x] S-028 INV-MUX-004 一致無し非RST セグメントには RST が 1 つ生成(RST 含みは無応答) (INV-MUX-004, req-mux:29)
      → The system SHALL emit exactly one RST for an unmatched non-RST and none for an unmatched RST. (ubiquitous / INV)
- [x] S-029 INV-MUX-008 TCB ⇔ 接続 全単射(TCB 不在 ⇔ CLOSED) (INV-MUX-008, req-mux:29)
      → The system SHALL keep a bijection between TCBs and live connections. (ubiquitous / INV)

### 活性(公平性)
- [x] S-030 LIVE-MUX OPEN した接続はいつか ESTABLISHED か CLOSED に決着する(test-and-set 競合で永久に作れない状態に陥らない) (req-mux:34)
      → WHEN a connection is opened the system SHALL eventually reach ESTABLISHED or CLOSED. (event / liveness)

## トレーサビリティ・マトリクス

| 仕様条項 (req-mux)      | 要件(EARS S) | 形式手法 (TLA+)                       | テスト (Gherkin)              |
|------------------------|--------------|--------------------------------------|-------------------------------|
| R-MUX-001 4-tuple 一意 | S-001        | TypeOK / DOMAIN connTable            | -                             |
| R-MUX-004 相乗り       | S-002        | Tuples 集合 (複数 key)               | scn: distinct remotes         |
| R-MUX-018 1TCB/接続    | S-003        | INV-MUX-008 (InvBijection)           | -                             |
| R-MUX-010 passive OPEN | S-004        | PassiveOpen (disjunct)               | -                             |
| active OPEN            | S-005        | ActiveOpen (disjunct, test-and-set)  | -                             |
| R-MUX-021 already-exist| S-006        | ActiveOpen ガード (占有なら no-op)   | scn: open occupied            |
| R-MUX-002 exact dispatch| S-007       | EstablishActive / InvDispatchExact   | scn: exact dispatch           |
| R-MUX-012 SYN 派生     | S-008        | SynToListener (disjunct)             | scn: derive synrcvd           |
| R-MUX-003 LISTEN 非破壊| S-009        | SynToListener / InvListenStable      | scn: listen survives          |
| ACK→RST                | S-010        | SegArriveListenAck / InvDemuxOrder   | -                             |
| RST→drop               | S-011        | SegArriveListenRst / InvDemuxNoRstToRst | -                          |
| demux step3 RST 生成   | S-012        | SegArriveNoMatch / InvDemuxOrderRespected | scn: unmatched→rst       |
| RST 含み無応答         | S-013        | SegArriveNoMatchRst / InvDemuxNoRstToRst | scn: unmatched rst no resp |
| R-MUX-009 bad src drop | S-014        | SegArriveBadSrc (disjunct)           | -                             |
| SYN-RCVD→ESTAB         | S-015        | Establish (disjunct)                 | -                             |
| SYN-SENT→ESTAB         | S-016        | EstablishActive (disjunct)           | -                             |
| R-MUX-024 close→TW     | S-017        | Close (disjunct)                     | -                             |
| R-MUX-024 TW expire    | S-018        | TimeWaitTimeout (disjunct, delete)   | -                             |
| R-MUX-024 reopen       | S-019        | ReopenFromTimeWait (disjunct)        | scn: reopen incarnation       |
| R-MUX-027 passive RST  | S-020        | RstSynRcvdPassive (disjunct)         | -                             |
| 並行 read/write        | S-021        | Next の interleaving (全 disjunct)   | -                             |
| test-and-set           | S-022        | ActiveOpen/SynToListener ガード      | scn: tas prevents overwrite   |
| 同時 SYN 複数派生      | S-023        | SynToListener (∀ tuple)              | scn: concurrent syn           |
| TW削除×新規 競合       | S-024        | INV-MUX-001 across interleavings     | scn: race tw delete           |
| INV-MUX-001 核心       | S-025        | InvUnique                            | scn: tas uniqueness           |
| INV-MUX-002            | S-026        | InvDispatchExact / InvDemuxBranchExclusive | -                       |
| INV-MUX-003            | S-027        | InvListenStable                      | scn: listen survives          |
| INV-MUX-004            | S-028        | InvRstExactlyOne / InvDemuxRstOnlyUnmatched / InvDemuxNoRstToRst | scn: unmatched rst |
| INV-MUX-008            | S-029        | InvBijection                         | scn: replace not duplicate    |
| LIVE-MUX               | S-030        | PendingCanProgress (デッドロックフリー安全性) | scn: pending progresses |

## 対象外(スコープ外、明記)
- seq/ack 番号の窓検査・データ転送(前回 TCP.tla の領分。ここは demux と一意性に絞る)
- 個別状態機械の全遷移(FIN_WAIT_2/CLOSING/LAST_ACK 等)。多重化に効くのは
  生成・派生・削除・ESTAB・TIME-WAIT のみなので状態集合を縮約する。
- 実 IP の broadcast/multicast 判定ロジック(S-014 は「badSrc フラグ付き SYN」で抽象化)
- 2MSL の実時間(twTimer を 0/1 の有限カウンタに抽象化)

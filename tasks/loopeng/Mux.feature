# 接続多重化 (4-tuple demux + 接続テーブル並行管理) 受け入れ仕様
# 源泉: tasks/loopeng/Mux.tla + Mux.extract.md。手編集禁止(設計を直して再生成)。
# 各 Scenario はモデル検査と mutation で確定した境界。本体テストへ移植する際は
# 管理番号・手法用語をコメントに残さず、振る舞いそのものを書くこと。

Feature: TCP connection multiplexing and concurrent connection table

  # --- 反例由来(設計の穴。回帰防止。これらは「起きてはならない」)---

  # 由来: test-and-set ガードを外した接続テーブルの反例(中ループ核心)。
  # LISTEN から SYN_RCVD を派生した直後、同じ 4-tuple へ並行 active OPEN が
  # ガード無しで挿入すると、同一 4-tuple に非 TIME-WAIT な TCB が 2 つできる。
  # insert は必ず atomic な test-and-set でなければならない。
  Scenario: Concurrent insert without test-and-set breaks 4-tuple uniqueness
    Given a LISTEN on local port p1
    And a derived SYN-RECEIVED TCB for 4-tuple (p1,r1)
    When an active OPEN concurrently inserts a TCB for the same 4-tuple (p1,r1) without test-and-set
    Then two non-TIME-WAIT TCBs exist for 4-tuple (p1,r1)
    And this violates 4-tuple uniqueness and must never happen

  # 由来: active OPEN が占有 TCB を消さず純追加する反例。
  # TIME-WAIT の 4-tuple へ新しい incarnation を作るとき、古い TIME-WAIT TCB を
  # 置換せず追加すると、同一 4-tuple に TCB が 2 つ並ぶ(全単射が壊れる)。
  Scenario: Reusing a TIME-WAIT 4-tuple must replace, not duplicate
    Given a TIME-WAIT TCB for 4-tuple (p1,r1)
    When an active OPEN reuses 4-tuple (p1,r1) as a new incarnation
    Then the old TIME-WAIT TCB is removed before the new TCB is inserted
    And exactly one TCB exists for 4-tuple (p1,r1)

  # 由来: demux が照合順序(完全一致→LISTEN→RST)を飛ばす反例。
  # LISTEN が存在する 4-tuple へ非RST が来たとき、LISTEN を見ずに RST を返してはならない。
  Scenario: A segment for a port with a LISTEN must not get an unmatched RST
    Given a LISTEN on local port p1 and no exact-match TCB for (p1,r1)
    When a non-RST segment arrives for (p1,r1)
    Then the segment is handled via the LISTEN, not answered with an unmatched RST

  # 由来: RST 入力に RST で応答する反例(INV-MUX-004)。
  Scenario: An unmatched RST segment is never answered with a RST
    Given no matching TCB and no LISTEN for 4-tuple (p2,r2)
    When a segment that includes RST arrives for (p2,r2)
    Then the segment is discarded
    And no RST is generated

  # 由来: SYN→LISTEN 派生が LISTEN を消す反例(INV-MUX-003)。
  Scenario: Deriving from a LISTEN must not remove the LISTEN
    Given a LISTEN on local port p1
    When a SYN arrives and derives a SYN-RECEIVED TCB for (p1,r1)
    Then the LISTEN on p1 still exists for future connections

  # --- 正シナリオ(EARS 正常系。実装の受け入れテスト)---

  # demux 完全一致 dispatch
  Scenario: Exact 4-tuple match dispatches to the owning TCB
    Given a SYN-SENT TCB for 4-tuple (p1,r1)
    When a SYN,ACK acking our SYN arrives for (p1,r1)
    Then it is dispatched to that TCB only
    And the TCB becomes ESTABLISHED

  # 相乗り: 同一 local port に remote 違いで複数接続
  Scenario: Same local port carries distinct connections for distinct remotes
    Given a LISTEN on local port p1
    When a SYN arrives from r1 and another SYN arrives from r2
    Then two distinct SYN-RECEIVED TCBs exist for (p1,r1) and (p1,r2)
    And the LISTEN on p1 still exists

  # 一致無し非RST → RST 1 つ
  Scenario: An unmatched non-RST segment gets exactly one RST
    Given no matching TCB and no LISTEN for 4-tuple (p2,r1)
    When a non-RST segment arrives for (p2,r1)
    Then exactly one RST is generated

  # already-exists: 占有 4-tuple への OPEN は拒否(上書きしない)
  Scenario: Opening an already-occupied 4-tuple is rejected
    Given a non-TIME-WAIT TCB for 4-tuple (p1,r1)
    When the application issues OPEN for (p1,r1)
    Then the OPEN is rejected as already-exists
    And the existing TCB is not overwritten

  # TIME-WAIT からの reopen(新 incarnation)
  Scenario: A new incarnation reopens a TIME-WAIT 4-tuple via its LISTEN
    Given a TIME-WAIT TCB for 4-tuple (p1,r1)
    And a LISTEN on local port p1
    When a fresh SYN arrives for (p1,r1)
    Then the TIME-WAIT TCB is replaced by a new SYN-RECEIVED TCB
    And exactly one TCB exists for (p1,r1)

  # passive 由来 SYN_RCVD に RST → LISTEN 復帰
  Scenario: RST on a passively-derived SYN-RECEIVED returns to LISTEN
    Given a passively-derived SYN-RECEIVED TCB for (p1,r1)
    And a LISTEN on local port p1
    When a RST arrives for (p1,r1)
    Then the SYN-RECEIVED TCB is removed
    And the LISTEN on p1 remains to accept future connections

  # 活性: OPEN した接続はデッドロックせず決着できる
  Scenario: A pending connection always has a way to progress
    Given a SYN-SENT or SYN-RECEIVED TCB under concurrent table activity
    Then an action to establish or tear it down is always enabled
    And the connection never gets stuck unable to progress

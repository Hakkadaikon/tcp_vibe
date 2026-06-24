# TCP 接続状態機械 受け入れ仕様
# 源泉: tasks/loopeng/TCP.tla + TCP.extract.md。手編集禁止(設計を直して再生成)。
# 各 Scenario はモデル検査で確定した境界。本体テストへ移植する際は
# 管理番号・手法用語をコメントに残さず、振る舞いそのものを書くこと。

Feature: TCP connection state machine (RFC 9293 + RFC 5961)

  # --- 反例由来(設計の穴を塞いだ境界。回帰防止)---

  # 由来: InvTwLinger action property の反例 (中ループ第1反復)
  # TIME-WAIT で RST を受けたら 2MSL を待たず即 CLOSED へ落ちてよい。
  # しかしタイマ満了 "でない" 通常経路では 2MSL 経過が必須(下の正シナリオ)。
  Scenario: RST in TIME-WAIT aborts immediately without waiting 2MSL
    Given a connection in TIME-WAIT with the 2MSL timer still running
    When a RST segment arrives
    Then the connection moves to CLOSED
    And the TCB is deleted

  # 由来: LIVE-2 (LiveTimeWait) のフル Next 反例 (中ループ第2反復)
  # 敵対的に FIN を再送し続けられると TIME-WAIT が 2MSL を再起動し続け、
  # 永久に滞留しうる(RFC 既知挙動)。無条件の活性は成立しない。
  Scenario: Repeated FIN in TIME-WAIT keeps restarting the 2MSL timer
    Given a connection in TIME-WAIT
    When a retransmitted FIN arrives before the 2MSL timer expires
    Then the connection re-sends the ACK
    And the 2MSL timer is restarted
    And the connection stays in TIME-WAIT

  # 由来: mutation M2 survivor を塞いだ INV-005 強化 (中ループ第3反復)
  Scenario: Out-of-window RST while synchronized is silently dropped
    Given a synchronized connection (e.g. ESTABLISHED)
    When a RST arrives with a sequence number outside the receive window
    Then the connection does not reset
    And the connection stays in its current state

  Scenario: In-window but non-RCV.NXT RST while synchronized only challenges
    Given a synchronized connection
    When a RST arrives in window but with SEG.SEQ different from RCV.NXT
    Then the connection sends a challenge ACK
    And the connection does not reset

  # 由来: mutation M11 survivor を塞いだ INV-014 由来追跡 (中ループ第4反復)
  Scenario: Simultaneous open records active origin
    Given a connection in SYN-SENT after an active open
    When a bare SYN arrives (simultaneous open)
    Then the connection moves to SYN-RCVD with active origin
    And a later RST in SYN-RCVD drives it to CLOSED (not back to LISTEN)

  # --- 正常系(EARS 正常系。設計確定後の受け入れシナリオ)---

  Scenario: Active three-way handshake reaches ESTABLISHED
    Given a connection in CLOSED
    When the application issues an active OPEN
    Then a SYN is sent and the connection moves to SYN-SENT
    When a SYN,ACK acking our SYN arrives
    Then an ACK is sent and the connection moves to ESTABLISHED

  Scenario: Passive open and incoming SYN reaches SYN-RCVD with passive origin
    Given a connection in CLOSED
    When the application issues a passive OPEN
    Then the connection moves to LISTEN
    When a SYN arrives
    Then a SYN,ACK is sent and the connection moves to SYN-RCVD with passive origin
    And a later RST in SYN-RCVD returns it to LISTEN (not CLOSED)

  Scenario: RST at RCV.NXT while synchronized resets the connection
    Given a synchronized connection
    When a RST arrives with SEG.SEQ equal to RCV.NXT
    Then the connection aborts to CLOSED

  Scenario: SYN while synchronized never resets, only challenges
    Given a synchronized connection
    When a SYN arrives
    Then the connection sends a challenge ACK
    And the connection does not reset

  Scenario: ACK outside the acceptable range does not advance SND.UNA
    Given a synchronized connection with SND.UNA and SND.NXT
    When an ACK arrives outside (SND.UNA - MAX.SND.WND)..SND.NXT
    Then the segment data is not applied
    And SND.UNA does not advance

  Scenario: TIME-WAIT leaves to CLOSED only after 2MSL expires
    Given a connection in TIME-WAIT with the 2MSL timer running
    When the 2MSL timer counts down to zero
    Then the connection moves to CLOSED
    But it does not move to CLOSED while the timer is still running

  Scenario: Graceful active close walks FIN-WAIT-1 to TIME-WAIT
    Given a connection in ESTABLISHED
    When the application issues CLOSE
    Then a FIN is sent and the connection moves to FIN-WAIT-1
    When our FIN is acked
    Then the connection moves to FIN-WAIT-2
    When a FIN arrives
    Then an ACK is sent and the connection moves to TIME-WAIT

# TCP フロー制御 受け入れ仕様
# 源泉: tasks/loopeng/fc.tla + fc.extract.md。手編集禁止 (設計を直して再生成)。
# 各 Scenario はモデル検査で確定した境界。本体テストへ移植する際は
# 管理番号・手法用語をコメントに残さず、振る舞いそのものを書くこと。

Feature: TCP flow control (receive window / zero-window persist / SWS avoidance / Nagle / delayed ACK)

  # ======================================================================
  # 反例由来 (設計の穴を塞いだ境界。回帰防止)
  # ======================================================================

  # 由来: 進展活性の反例 (中ループ第1反復)
  # idle (未確認データなし) のとき sub-MSS のデータも遅延なく送れねばならない。
  # 「フルセグメント (>=MSS) でないと送らない」だけにすると、相手窓があり未確認も
  # 無いのに 1 バイトが永久に送れず詰まる。Nagle は未確認データ中だけ抑制する。
  Scenario: Idle sub-MSS data is sent immediately when no data is in flight
    Given the send queue holds 1 byte of data
    And there is no unacknowledged data in flight
    And the peer advertised window has room
    When the sender evaluates whether to send
    Then the sender transmits the 1 byte without waiting

  # 由来: 進展活性の反例 (中ループ第2反復)
  # 相手窓を使い切った (usable window = 0) 状態で送り残しがあるとき、これは
  # zero-window と同じく ACK / window-update 待ちであり、override では救えない。
  # この「待ち」を活性違反と誤判定しないこと (正当な待ち)。
  Scenario: With usable window exhausted the sender waits for an ACK
    Given the sender has unacknowledged data filling the peer window
    And the send queue still holds data
    When no advancing ACK arrives
    Then the sender legitimately waits until an ACK frees window space

  # 由来: NagleDelAckLive の反例 (中ループ第3反復)
  # 未確認データ中に sub-MSS の送り残しがあり相手窓もある (Nagle 抑制下) とき、
  # override timer を起動しないと永久に送れない。override の arm を必ず行うこと。
  Scenario: Nagle-withheld sub-MSS data arms the override timer
    Given there is unacknowledged data in flight
    And the send queue holds less than one full segment
    And the peer window has room but not a full segment
    When the sender cannot form a full segment
    Then the sender arms the override timer
    And on override timeout it sends the withheld data

  # 由来: NagleDelAckLive の反例 (ACK 停滞ケース, 中ループ第4反復)
  # 送信側が Nagle で溜め、受信側が delayed ACK で ACK を遅延する二重待ちでも、
  # override timer が溜めたデータを送り出してデッドロックを解く。
  Scenario: Nagle plus delayed-ACK double-wait is broken by the override timer
    Given the sender withholds sub-MSS data under Nagle
    And the receiver delays its ACK
    When neither side acts and the override timer fires
    Then the sender transmits the withheld data
    And the deadlock is resolved

  # 由来: ZeroWindowProgress の反例 (中ループ第5反復)
  # 相手窓が 0 で送るデータがあるなら persist timer を起動し probe を送り続ける。
  # 自発的な window-update はロストしうるので、それだけに頼ると永久に窓0で詰まる。
  Scenario: Zero window arms a persist timer that keeps probing
    Given the peer advertised a zero window
    And the send queue holds data
    When the persist timer is not yet armed
    Then the sender arms the persist timer
    And it keeps sending zero-window probes until the window reopens

  # 由来: ZeroWindowProgress の反例 (probe 応答, 中ループ第6反復)
  # 受信側は zero-window probe に必ず ACK (現在の RCV.NXT と窓) を返す。
  # この probe 応答が窓再開を伝える信頼経路で、自発 window-update のロストを補償する。
  Scenario: A zero-window probe always elicits an ACK carrying the current window
    Given the receiver previously advertised a zero window
    And the receiving application has since freed buffer space
    When a zero-window probe arrives
    Then the receiver replies with an ACK carrying the reopened window
    And the sender resumes transmission

  # ======================================================================
  # 正常系 (固まった設計の受け入れシナリオ)
  # ======================================================================

  # 受信窓を縮めない (右窓端 RCV.NXT+RCV.WND は単調非減少)。
  Scenario: The receiver never shrinks the right window edge
    Given an advertised right window edge at some position
    When the receiver updates its advertised window
    Then the new right window edge is greater than or equal to the old one

  # データ受信で RCV.NXT が前進するとき、右窓端は一定 (窓を消費する)。
  Scenario: Receiving data advances RCV.NXT and consumes the window
    Given RCV.NXT and RCV.WND define a right window edge
    When in-window data arrives advancing RCV.NXT by k
    Then RCV.WND decreases by k
    And the right window edge stays unchanged

  # 窓更新は最大 ACK のみ採用 (古い ACK の窓で上書きしない)。
  Scenario: Only the largest ACK updates the send window
    Given the largest ACK seen so far
    When an older, smaller ACK arrives carrying a smaller window
    Then the send window is not overwritten with the stale window
    And SND.UNA does not move backward

  # 受信側 SWS 回避: 閾値未満の小窓は広告しない。
  Scenario: The receiver does not advertise a sub-threshold window
    Given the receive window is currently advertised as zero
    And the application frees space smaller than min(RCV.BUFF/2, MSS)
    When the receiver considers advertising the increase
    Then it keeps advertising zero until the increase reaches the threshold

  # 送信側 SWS 回避: usable window が小さくフルが組めないとき override を待つ。
  Scenario: The sender withholds a sub-MSS segment until full or override
    Given there is unacknowledged data in flight
    And the usable window is smaller than a full segment
    When sub-MSS data is queued
    Then the sender does not send it immediately
    And it waits for a full segment to form or the override timer to fire

  # delayed ACK: 未 ACK のフルセグメントは高々 2 (2 個目までに ACK)。
  Scenario: At most two full segments go unacknowledged
    Given one full segment has arrived and its ACK is delayed
    When a second full segment arrives
    Then the receiver sends an ACK immediately
    And the count of unacknowledged full segments never exceeds two

  # delayed ACK: 遅延 ACK は必ず発火する (永久に未送信のままにならない)。
  Scenario: A delayed ACK is always eventually sent
    Given an ACK is being delayed
    When the delayed-ACK timer fires
    Then the receiver sends the ACK
    And the delay timer is cleared

# 輻輳制御 cwnd 状態機械 + RTO バックオフ の受け入れ仕様。
# TLA+ モデル (cc.tla) で安全性・liveness を緑、mutation 14/14 kill (survivor 0) まで固めた設計から派生。
# SMSS=1 単位の抽象。実装では SMSS バイト単位に展開する。
# 正の Scenario = 守るべき正常系。負の Scenario = mutation が示した「壊れ方」(回帰防止)。

Feature: Congestion control cwnd state machine and RTO backoff

  Background:
    Given SMSS is the sender maximum segment size
    And the loss window LW equals 1 SMSS
    And ssthresh is initialised high and never below 2 SMSS
    And cwnd is initialised to the initial window and starts in slow start

  # --- Slow start (S-004, S-005b) ---
  Scenario: Slow start increases cwnd by at most one SMSS per new ACK
    Given state is "SlowStart"
    And cwnd is below ssthresh
    When a new ACK arrives
    Then cwnd increases by exactly 1 SMSS
    And cwnd increases by no more than 1 SMSS for that ACK

  Scenario: Slow start transitions to congestion avoidance when cwnd reaches ssthresh
    Given state is "SlowStart"
    And cwnd plus 1 SMSS is greater than or equal to ssthresh
    When a new ACK arrives
    Then cwnd increases by 1 SMSS
    And state becomes "CongestionAvoidance"

  # --- Congestion avoidance (S-005) ---
  Scenario: Congestion avoidance increases cwnd by at most one SMSS per RTT
    Given state is "CongestionAvoidance"
    When a full window of data is acknowledged
    Then cwnd increases by no more than 1 SMSS

  # --- Limited transmit on 1st/2nd duplicate ACK (S-007) ---
  Scenario: First and second duplicate ACKs do not change cwnd
    Given state is "SlowStart" or "CongestionAvoidance"
    And there is unacknowledged data in flight
    And the duplicate ACK count is less than 2
    When a duplicate ACK arrives
    Then the duplicate ACK count increases by 1
    And cwnd stays unchanged

  # --- Fast retransmit / enter fast recovery on 3rd duplicate ACK (S-008, S-009) ---
  Scenario: Third duplicate ACK enters fast recovery with ssthresh halved and cwnd inflated
    Given state is "SlowStart" or "CongestionAvoidance"
    And the duplicate ACK count is 2
    And there is unacknowledged data in flight
    When the third duplicate ACK arrives
    Then the lost segment is retransmitted
    And ssthresh becomes max(FlightSize/2, 2 SMSS)
    And cwnd becomes ssthresh plus 3 SMSS
    And state becomes "FastRecovery"

  # --- Inflation during fast recovery (S-010) ---
  Scenario: Each additional duplicate ACK during fast recovery inflates cwnd by one SMSS
    Given state is "FastRecovery"
    When an additional duplicate ACK arrives
    Then cwnd increases by 1 SMSS

  # --- Deflate on recovery completion (S-011, INV-CC-016) ---
  Scenario: New ACK completing recovery deflates cwnd to ssthresh
    Given state is "FastRecovery"
    When a new ACK acknowledges the recovered data
    Then cwnd becomes equal to ssthresh
    And state becomes "CongestionAvoidance"

  # --- RTO expiry: first retransmit halves ssthresh (S-012, R-CC-053) ---
  Scenario: First RTO expiry in a loss episode halves ssthresh
    Given no segment has yet been retransmitted in this loss episode
    When the RTO expires
    Then ssthresh becomes max(FlightSize/2, 2 SMSS)
    And cwnd becomes 1 SMSS
    And state becomes "SlowStart"
    And the RTO is doubled

  # --- RTO expiry: subsequent retransmit holds ssthresh (S-013, R-CC-054) ---
  Scenario: Subsequent RTO expiry in the same loss episode holds ssthresh
    Given a segment was already retransmitted in this loss episode
    When the RTO expires again
    Then ssthresh stays unchanged
    And cwnd becomes 1 SMSS
    And the RTO is doubled

  # --- RTO backoff is monotone up to the cap (S-016, S-017, INV-CC-008) ---
  Scenario: Consecutive RTO expiries double the RTO monotonically up to the maximum
    Given the RTO backoff stage is below the maximum
    When the RTO expires
    Then the backoff stage increases by exactly 1
    And the backoff stage never decreases on an RTO expiry

  Scenario: RTO backoff saturates at the maximum
    Given the RTO backoff stage is at the maximum
    When the RTO expires again
    Then the backoff stage stays at the maximum

  # --- Recovery on new ACK resets backoff (S-018) ---
  Scenario: A new ACK resets the loss-episode retransmit flag and the RTO backoff stage
    Given the connection is backed off after a loss
    When a new ACK arrives
    Then the RTO backoff stage resets to base
    And the loss-episode retransmit flag clears

  # --- Liveness (S-019, LIVE-CC) ---
  Scenario: cwnd does not stay pinned at the loss window forever
    Given cwnd has been reduced to 1 SMSS after a loss
    When new ACKs keep arriving
    Then cwnd eventually grows above 1 SMSS

  # === Negative scenarios: regressions the mutation oracle proved must fail ===

  Scenario: cwnd must never drop below 1 SMSS
    Given any reachable state
    Then cwnd is always greater than or equal to 1 SMSS

  Scenario: ssthresh must never drop below 2 SMSS
    Given any reachable state
    Then ssthresh is always greater than or equal to 2 SMSS

  Scenario: state selection must stay consistent outside fast recovery
    Given state is not "FastRecovery"
    Then cwnd below ssthresh implies state is "SlowStart"
    And cwnd above ssthresh implies state is "CongestionAvoidance"

  Scenario: during fast recovery cwnd stays at or above ssthresh
    Given state is "FastRecovery"
    Then cwnd is greater than or equal to ssthresh

  Scenario: RTO loss handling must not skip halving on the first retransmit
    Given no segment has yet been retransmitted in this loss episode
    When the RTO expires
    Then ssthresh must not be held constant

  Scenario: RTO loss handling must not re-halve an already reduced ssthresh
    Given a segment was already retransmitted in this loss episode
    When the RTO expires again
    Then ssthresh must not be halved a second time

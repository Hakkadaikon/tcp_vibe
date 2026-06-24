# TCP 接続状態機械 — 抽出台帳 (0段)

出典: `tasks/loopeng/requirements.md`(RFC 9293 + RFC 5961 から抽出済み)を一次台帳とし、
本ファイルは TLA+ モデル化スコープに絞った採番チェックリスト + トレーサビリティ表。
S-NNN は本モデルの抽出単位 ID(requirements.md の R-xxx / INV-xxx / 状態遷移表へ逆引き可能)。

原則: 過剰抽出は安全・漏れは危険。スコープ外は §「対象外」に明記。

## 台帳(採番チェックリスト)

### 状態遷移(§2 状態遷移表を1辺=1 ID へ)
- [x] S-001 CLOSED + active OPEN → snd SYN, SYN-SENT (R-030, 9293:2967)
      → WHEN active OPEN in CLOSED the system SHALL send SYN and go to SYN-SENT. (event)
- [x] S-002 CLOSED + passive OPEN → LISTEN (R-031, 9293:2966)
      → WHEN passive OPEN in CLOSED the system SHALL go to LISTEN. (event)
- [x] S-003 LISTEN + rcv SYN → snd SYN,ACK, SYN-RCVD(passive 由来) (R-032, 9293:3318)
      → WHEN a SYN arrives in LISTEN the system SHALL send SYN,ACK and go to SYN-RCVD recording passive origin. (event)
- [x] S-004 LISTEN + rcv RST → 無視 (R-034, 9293:3303)
      → IF a RST arrives in LISTEN THEN the system SHALL ignore it and stay in LISTEN. (unwanted)
- [x] S-005 SYN-SENT + rcv SYN,ACK(自SYN ACK済) → snd ACK, ESTABLISHED (R-037, 9293:3414)
      → WHEN a SYN,ACK acceptably acking our SYN arrives in SYN-SENT the system SHALL send ACK and go to ESTABLISHED. (event)
- [x] S-006 SYN-SENT + rcv SYN(同時オープン) → snd SYN,ACK, SYN-RCVD(active 由来) (R-038, 9293:3429)
      → WHEN a bare SYN arrives in SYN-SENT the system SHALL send SYN,ACK and go to SYN-RCVD recording active origin. (event)
- [x] S-007 SYN-SENT + rcv RST(ACK が自SYN確認) → CLOSED (R-113/R-035, 9293:3370)
      → WHEN a RST whose ACK confirms our SYN arrives in SYN-SENT the system SHALL go to CLOSED. (event)
- [x] S-008 SYN-RCVD + rcv ACK of SYN(acceptable) → ESTABLISHED (R-040, 9293:3729)
      → WHEN an acceptable ACK of our SYN arrives in SYN-RCVD the system SHALL go to ESTABLISHED. (event)
- [x] S-009 SYN-RCVD + rcv RST(passive 由来) → LISTEN (R-074, 9293:3580)
      → IF a RST arrives in SYN-RCVD of passive origin THEN the system SHALL go to LISTEN. (unwanted)
- [x] S-010 SYN-RCVD + rcv RST(active 由来) → CLOSED (R-074, 9293:3587)
      → IF a RST arrives in SYN-RCVD of active origin THEN the system SHALL go to CLOSED. (unwanted)
- [x] S-011 ESTABLISHED + CLOSE → snd FIN, FIN-WAIT-1 (R-050, 9293:3149)
      → WHEN CLOSE in ESTABLISHED the system SHALL send FIN and go to FIN-WAIT-1. (event)
- [x] S-012 ESTABLISHED + rcv FIN → snd ACK, CLOSE-WAIT (R-054, 9293:3896)
      → WHEN a FIN arrives in ESTABLISHED the system SHALL ack and go to CLOSE-WAIT. (event)
- [x] S-013 FIN-WAIT-1 + rcv ACK of FIN → FIN-WAIT-2 (R-053, 9293:3772)
      → WHEN our FIN is acked in FIN-WAIT-1 the system SHALL go to FIN-WAIT-2. (event)
- [x] S-014 FIN-WAIT-1 + rcv FIN(自FIN 未ACK) → snd ACK, CLOSING (R-055, 9293:3900)
      → WHEN a FIN arrives in FIN-WAIT-1 with our FIN not yet acked the system SHALL ack and go to CLOSING. (event)
- [x] S-015 FIN-WAIT-1 + rcv FIN+ACK(自FIN ACK済) → snd ACK, TIME-WAIT(2MSL) (R-055, 9293:848)
      → WHEN a FIN with ACK of our FIN arrives in FIN-WAIT-1 the system SHALL ack and go to TIME-WAIT starting 2MSL. (event)
- [x] S-016 FIN-WAIT-2 + rcv FIN → snd ACK, TIME-WAIT(2MSL) (R-056, 9293:3906)
      → WHEN a FIN arrives in FIN-WAIT-2 the system SHALL ack and go to TIME-WAIT starting 2MSL. (event)
- [x] S-017 CLOSE-WAIT + CLOSE → snd FIN, LAST-ACK (R-052, 9293:3164)
      → WHEN CLOSE in CLOSE-WAIT the system SHALL send FIN and go to LAST-ACK. (event)
- [x] S-018 CLOSING + rcv ACK of FIN → TIME-WAIT(2MSL) (R-057, 9293:3788)
      → WHEN our FIN is acked in CLOSING the system SHALL go to TIME-WAIT starting 2MSL. (event)
- [x] S-019 LAST-ACK + rcv ACK of FIN → CLOSED (R-058, 9293:3794)
      → WHEN our FIN is acked in LAST-ACK the system SHALL delete TCB and go to CLOSED. (event)
- [x] S-020 TIME-WAIT + 2MSL timeout → CLOSED (R-059, 9293:3946)
      → WHEN the 2MSL timer expires in TIME-WAIT the system SHALL go to CLOSED. (event)
- [x] S-021 TIME-WAIT + rcv FIN → snd ACK, 2MSL 再起動 (R-060, 9293:3923)
      → WHEN a FIN arrives in TIME-WAIT the system SHALL re-ack and restart 2MSL. (event)
- [x] S-022 TIME-WAIT + rcv RST → CLOSED (R-073, 9293:3612)
      → IF a RST arrives in TIME-WAIT THEN the system SHALL go to CLOSED. (unwanted)
- [x] S-023 SYN-RCVD + rcv FIN → snd ACK, CLOSE-WAIT (R-054, 9293:3894)
      → WHEN a FIN arrives in SYN-RCVD the system SHALL ack and go to CLOSE-WAIT. (event)
- [x] S-024 SYN-RCVD + CLOSE → snd FIN, FIN-WAIT-1 (R-051, 9293:3143)
      → WHEN CLOSE in SYN-RCVD the system SHALL send FIN and go to FIN-WAIT-1. (event)
- [x] S-025 ESTABLISHED + rcv RST(SEG.SEQ=RCV.NXT) → abort, CLOSED (R-073/R-111, 9293:3602)
      → WHEN a RST with SEG.SEQ=RCV.NXT arrives in ESTABLISHED the system SHALL abort to CLOSED. (event)

### RFC 5961 三チェック(同期状態のガード)
- [x] S-030 同期 + RST + 窓外 → silently drop, 状態不変 (R-110/INV-005, 5961:380)
      → IF a RST out of window arrives while synchronized THEN the system SHALL silently drop it and not reset. (unwanted)
- [x] S-031 同期 + RST + SEG.SEQ=RCV.NXT → reset (R-111/INV-005, 5961:383)
      → WHEN a RST with SEG.SEQ=RCV.NXT arrives while synchronized the system SHALL reset the connection. (event)
- [x] S-032 同期 + RST + 窓内 ≠ RCV.NXT → challenge ACK, drop, 状態不変 (R-112/INV-005, 5961:399)
      → IF a RST in window but not at RCV.NXT arrives while synchronized THEN the system SHALL send a challenge ACK and not reset. (unwanted)
- [x] S-033 同期 + SYN → challenge ACK, drop, 状態不変 (R-114/INV-006, 5961:479)
      → IF a SYN arrives while synchronized THEN the system SHALL send a challenge ACK and not reset. (unwanted)
- [x] S-034 ACK 受理範囲 (SND.UNA-MAX.SND.WND)<=SEG.ACK<=SND.NXT 外 → discard, ACK 返す (R-115/INV-007, 5961:593)
      → IF SEG.ACK is outside (SND.UNA-MAX.SND.WND)..SND.NXT THEN the system SHALL discard the data and not apply it. (unwanted)

### 送受信窓・データ受理(seq/ack 抽象化)
- [x] S-040 ESTAB で acceptable ACK(SND.UNA<SEG.ACK<=SND.NXT) → SND.UNA 前進 (R-080/INV-001/002, 9293:3748)
      → WHEN an acceptable ACK arrives in a sending state the system SHALL advance SND.UNA up to SEG.ACK. (event)
- [x] S-041 窓内データ → RCV.NXT 前進, snd ACK (R-081/INV-003/004, 9293:3833)
      → WHEN in-window data arrives the system SHALL advance RCV.NXT and ack. (event)
- [x] S-042 窓外/未送信 ACK → 破棄, 状態・変数不変 (R-080/R-084, 9293:3711)
      → IF an unacceptable ACK arrives THEN the system SHALL discard it and not advance SND.UNA. (unwanted)

### TIME-WAIT linger / incarnation
- [x] S-050 TIME-WAIT は 2MSL 経過前に CLOSED へ飛ばない (R-059/INV-011, 9293:1653)
      → WHILE the 2MSL timer has not expired the system SHALL NOT leave TIME-WAIT for CLOSED. (state)
- [x] S-051 SYN-RCVD 由来 (passive/active) で RST 受信時の遷移先が変わる (R-041/R-074/INV-014, 9293:1308)
      → WHILE in SYN-RCVD the system SHALL remember whether its origin is passive or active. (state)

## トレーサビリティ表

| 仕様条項 (S) | 要件 (req台帳) | 形式手法 (TLA+) | テスト (Gherkin/Go 候補) |
|---|---|---|---|
| S-001 | R-030 | Next: ActiveOpen | T-021 connect→SYN-SENT |
| S-002 | R-031 | Next: PassiveOpen | T-021 listen |
| S-003 | R-032 | Next: RcvSynInListen | T-022 |
| S-004 | R-034 | Next: RcvRstInListen | T-040 |
| S-005 | R-037 | Next: RcvSynAck | T-023 3way active |
| S-006 | R-038 | Next: SimOpen | T-024 simultaneous open |
| S-007 | R-113,R-035 | Next: RcvRstSynSent | T-041 |
| S-008 | R-040 | Next: RcvAckSynRcvd | T-025 3way passive |
| S-009 | R-074 | Next: RcvRstSynRcvdPassive / INV-014 | T-042 |
| S-010 | R-074 | Next: RcvRstSynRcvdActive / INV-014 | T-043 |
| S-011 | R-050 | Next: CloseEstab | T-030 active close |
| S-012 | R-054 | Next: RcvFinEstab | T-031 passive close |
| S-013 | R-053 | Next: RcvAckFW1 | T-032 |
| S-014 | R-055 | Next: RcvFinFW1Closing | T-033 sim close |
| S-015 | R-055 | Next: RcvFinAckFW1 | T-034 |
| S-016 | R-056 | Next: RcvFinFW2 | T-035 |
| S-017 | R-052 | Next: CloseCloseWait | T-036 |
| S-018 | R-057 | Next: RcvAckClosing | T-037 |
| S-019 | R-058 | Next: RcvAckLastAck | T-038 |
| S-020 | R-059 | Next: TimeWaitExpire / INV-011 | T-039 |
| S-021 | R-060 | Next: RcvFinTimeWait | T-044 |
| S-022 | R-073 | Next: RcvRstTimeWait | T-045 |
| S-023 | R-054 | Next: RcvFinSynRcvd | T-046 |
| S-024 | R-051 | Next: CloseSynRcvd | T-047 |
| S-025 | R-073,R-111 | Next: RcvRstEstab / INV-005 | T-048 |
| S-030 | R-110 | Next: RstOutOfWindow / INV-005 | T-049 RST out-of-window |
| S-031 | R-111 | Next: RstAtRcvNxt / INV-005 | T-050 RST at RCV.NXT |
| S-032 | R-112 | Next: RstInWindowNotNxt / INV-005 | T-051 challenge |
| S-033 | R-114 | Next: SynChallenge / INV-006 | T-052 SYN challenge |
| S-034 | R-115 | Next: AckRangeCheck / INV-007 | T-053 data injection |
| S-040 | R-080 | Next: RcvAckData / INV-001,002 | T-040..049 (ack) |
| S-041 | R-081 | Next: RcvData / INV-003,004 | T-054 |
| S-042 | R-080,R-084 | Next: RcvBadAck / INV-007 | T-055 |
| S-050 | R-059 | INV-011 (Inv) | T-039 |
| S-051 | R-041 | VARIABLE origin / INV-014 | T-042,043 |

## 検査する不変条件 / 性質(TLA+ へ落とす)

安全性:
- INV-A   状態遷移は許可辺のみ(不正遷移なし) — TransOK
- INV-001 SND.UNA <= SND.NXT
- INV-005 同期で SEG.SEQ=RCV.NXT の RST のみ reset(窓内≠NXT・窓外では reset しない)
- INV-006 同期で SYN は reset しない(challenge のみ)
- INV-007 ACK 受理範囲外のデータは適用されない
- INV-011 TIME-WAIT は 2MSL を経ずに CLOSED へ飛ばない
- INV-014 SYN-RCVD 由来で RST 受信時の遷移先が変わる(passive→LISTEN, active→CLOSED)

活性:
- LIVE-1 能動オープンした側はいつか ESTABLISHED か CLOSED に到達
- LIVE-2 close 後はいつか CLOSED に到達(TIME-WAIT で永久滞留しない)

## 対象外(スコープ注記)
- checksum (R-100..103/INV-010): バイト計算は状態機械の安全性に無関係。Lean/通常テスト領分。
- 輻輳制御・SWS・Nagle・zero-window probe (R-090..094): 別レイヤ、INV 非対象。
- challenge ACK throttling のレート (R-117/INV-013): 時間レート、状態安全性でなく DoS 緩和。抽象では「challenge を送るが状態不変」だけ表現。
- ISN clock の実値 (R-020/021): 抽象 seq ドメインで「単調 incarnation」だけ表現対象。
- security/compartment, Diffserv, URG: no-op 素通し。
- seq/ack の完全 2^32 空間: 小有限ドメイン + ラップ1ケースに抽象化。

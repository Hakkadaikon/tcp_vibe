# 抽出台帳: 輻輳制御 cwnd 状態機械 + RTO バックオフ

元仕様: `tasks/loopeng/req-congestion.md` (R-CC-xxx / INV-CC-xxx)、RFC 5681 §3.1-3.2/§4.3、RFC 6298 §5.5/§2.5。
スコープ (YAGNI): cwnd/ssthresh/state(SS/CA/FR)の遷移と RTO 倍化の **安全性**。
SRTT/RTTVAR の estimator 計算、seq 空間詳細、rwnd 連動の送信上限は対象外(別 spec / 通常テスト)。

抽象化方針: SMSS=1 単位。cwnd, ssthresh, flightSize を小有限整数 (1..N) に。
RTO は実数秒でなく「倍化段階」整数 rtoStage (0=base, 各満了で +1, 上限 stageMax)。
dupAckCount は 0..3 で飽和 (3 で FR 入り後はカウンタ意味を持たないので保持/リセット)。
retransmittedThisLoss は「この損失エピソードで初回再送済みか」のフラグ (R-CC-053/054 の保持判定)。

## 台帳 (走査アンカー = 各 R-CC / INV)

### 状態選択・初期化
- [x] S-001 R-CC-046「cwnd<ssthresh=slow start, >=ssthresh=congestion avoidance」
      → WHILE cwnd < ssthresh the system SHALL be in SlowStart. (state)
      → WHILE cwnd > ssthresh the system SHALL be in CongestionAvoidance. (state)
      → 注: cwnd=ssthresh は SS/CA どちらでもよい (RFC5681 L289)。INV-CC-009 は厳格な < / > のみ縛る。
- [x] S-002 R-CC-044 / RFC5681§3.1「ssthresh 初期は高く」
      → The system SHALL initialise ssthresh to a high value (>= 2*SMSS). (ubiquitous)
- [x] S-003 R-CC-041 IW (初期 cwnd) は SMSS により 2..4*SMSS
      → WHEN the connection opens the system SHALL set cwnd to the initial window. (event)
      → 抽象: cwnd 初期 = ssthresh 未満となる小値(SS から開始)。倍化検証に IW 値の厳密さは不要。

### Slow Start / Congestion Avoidance の増加 (積極性上限)
- [x] S-004 R-CC-047 / RFC5681 式(2)「SS: 新規ACKごと cwnd += min(N, SMSS)」
      → WHILE in SlowStart WHEN a new ACK arrives the system SHALL increase cwnd by at most SMSS. (state+event)
      → INV-CC-010: SS の 1 ACK 増分 <= SMSS。
- [x] S-005 R-CC-049/050 / RFC5681 式(3)「CA: 1 SMSS/RTT」
      → WHILE in CongestionAvoidance WHEN a full window is ACKed the system SHALL increase cwnd by at most SMSS per RTT. (state+event)
      → INV-CC-011: CA の 1 RTT 増分 <= SMSS。抽象: CA の NewAck 1 回で +1*SMSS を上限とする。
- [x] S-005b 状態遷移: SS で cwnd が ssthresh に達したら CA へ。
      → WHILE in SlowStart WHEN cwnd reaches ssthresh the system SHALL transition to CongestionAvoidance. (state+event)

### 3 dup ACK → Fast Retransmit/Fast Recovery
- [x] S-006 R-CC-039「重複ACK定義」(未確認データあり/データ無/SYN&FIN off/ACK=最大ACK/窓同一)
      → IF an ACK matches the duplicate-ACK conditions THEN the system SHALL count it as a duplicate ACK. (unwanted/分類)
      → 抽象: DupAck アクションが発火可能 = 未確認データありの状態 (flightSize > 0)。
- [x] S-007 RFC5681§3.2 step1「1,2番目の dup ACK は Limited Transmit、cwnd 不変」
      → WHILE dupAckCount < 3 WHEN a duplicate ACK arrives the system SHALL NOT change cwnd. (state+event)
- [x] S-008 R-CC-059 fast retransmit「3 dup ACK で再送タイマ待たず再送」
      → WHEN the third duplicate ACK arrives the system SHALL retransmit the lost segment. (event)
- [x] S-009 R-CC-061/062 / RFC5681§3.2 step2「3dup: ssthresh=max(FlightSize/2,2*SMSS); cwnd=ssthresh+3*SMSS」
      → WHEN entering FastRecovery the system SHALL set ssthresh to max(FlightSize/2, 2*SMSS). (event)
      → WHEN entering FastRecovery the system SHALL set cwnd to ssthresh + 3*SMSS (inflate) and enter FastRecovery. (event)
- [x] S-010 R-CC-063 / RFC5681§3.2 step3「追加 dup ACK ごと cwnd += SMSS」
      → WHILE in FastRecovery WHEN an additional duplicate ACK arrives the system SHALL increase cwnd by SMSS. (state+event)
- [x] S-011 R-CC-066 / RFC5681§3.2 step5/6「新規ACK(回復完了): cwnd=ssthresh (deflate)」
      → WHEN in FastRecovery a new ACK acknowledges the recovery the system SHALL set cwnd to ssthresh and transition to CongestionAvoidance. (event)
      → INV-CC-016: FR 終了時 cwnd = ssthresh。

### RTO 満了 (損失) → Slow Start
- [x] S-012 R-CC-053 / RFC5681 式(4)「RTO損失: ssthresh=max(FlightSize/2, 2*SMSS) (初回再送のみ)」
      → WHEN the RTO expires and no segment has yet been retransmitted in this loss episode
        the system SHALL set ssthresh to max(FlightSize/2, 2*SMSS). (event)
- [x] S-013 R-CC-054 / RFC5681§4.3「再送済みは ssthresh を保持」
      → IF the RTO expires and a segment was already retransmitted in this loss episode
        THEN the system SHALL hold ssthresh constant. (unwanted/条件分岐)
      → 注: これが mutation の狙い目 (初回半減を消す vs 保持を消す)。
- [x] S-014 R-CC-056 / RFC5681§3.1「RTO: cwnd=LW=1*SMSS」
      → WHEN the RTO expires the system SHALL set cwnd to the loss window 1*SMSS. (event)
- [x] S-015 RTO 満了で SlowStart へ戻る
      → WHEN the RTO expires the system SHALL transition to SlowStart. (event)
- [x] S-016 R-CC-018/019/020 + RFC6298§5.5「満了→RTO*=2 (back off)」
      → WHEN the RTO expires the system SHALL double the RTO (back off the timer). (event)
- [x] S-017 R-CC-008 上限 / RFC6298§2.5「RTO 上限 >=60s」(倍化は上限で飽和)
      → WHILE the RTO is at its maximum WHEN it expires again the system SHALL keep the RTO at the maximum. (state+event)
      → INV-CC-008: 連続満了で RTO 段階は単調非減少に倍化 (上限まで)。

### 回復・再送再開 / アイドル
- [x] S-018 新規 ACK が来れば再送が進み損失エピソード解消、rtoStage を base に戻す
      → WHEN a new ACK arrives the system SHALL reset the loss-episode retransmit flag and reset the RTO backoff stage. (event)
      → RFC6298§5: ACK 受信で RTO を再計算しbackoff から復帰。抽象: rtoStage→0, retransmittedThisLoss→FALSE。
- [x] S-019 LIVE-CC 損失後いつか回復 (FR→CA or RTO→SS) し送信再開、cwnd が永久に 1*SMSS に張り付かない
      → WHEN ACKs keep arriving the system SHALL eventually increase cwnd above the loss window. (liveness)
      → 公平性: NewAck (SS/CA の新規ACK) に weak fairness を張る。

### 安全性不変条件 (常時)
- [x] S-020 INV-CC-001「cwnd >= 1*SMSS が常に成立」
      → The system SHALL always keep cwnd >= 1*SMSS. (ubiquitous/safety)
- [x] S-021 INV-CC-003「ssthresh >= 2*SMSS が常に成立」
      → The system SHALL always keep ssthresh >= 2*SMSS. (ubiquitous/safety)
- [x] S-022 INV-CC-002「輻輳指標のたび ssthresh <= max(flight/2, 2*SMSS) へ (初回半減; 再送済み保持)」
      → 安全側の表現: 損失で ssthresh を下げるときその値は max(flight/2, 2*SMSS) を超えない。(safety)
      → spec では S-009/S-012 の代入式そのものが等式なので、設定値の上界として TypeOK+代入で担保。
- [x] S-023 INV-CC-009「cwnd<ssthresh⇒SlowStart、cwnd>ssthresh⇒CongestionAvoidance」(状態選択一貫性)
      → The system SHALL keep state consistent: cwnd<ssthresh implies SlowStart, cwnd>ssthresh implies CongestionAvoidance. (safety)
      → 注: FastRecovery 中は cwnd が inflate して ssthresh を超えるため、この不変条件は FR を除外して述べる
        (RFC5681§3.2 で FR 中の cwnd は人工的に膨らんでいる)。FR 中は別途 cwnd>=ssthresh を要求。
- [x] S-024 INV-CC-016「FastRecovery 終了時 cwnd = ssthresh」(deflate)
      → 終了遷移 S-011 の事後条件として等式で担保。実行中の不変条件ではなく遷移の事後。(safety on transition)
- [x] S-025 INV-CC-008「連続 RTO 満了で RTO 段階は単調非減少に倍化 (上限まで)」
      → 安全側: rtoStage は満了で増えこそすれ、満了アクションで減らない。減るのは NewAck のときだけ。(safety)
      → 表現: RtoExpire は rtoStage' >= rtoStage を満たす。

## トレーサビリティ表

| 仕様条項 (S) | 要件 (EARS) | TLA+ (Action / Inv)               | テスト (Gherkin / T-ID) |
|--------------|-------------|-----------------------------------|--------------------------|
| S-001        | state SS/CA | StateSelectInv (Inv)              | cc.feature 正常系        |
| S-002        | ubiquitous  | Init (ssthresh)                   | T-CC-INIT                |
| S-003        | event open  | Init (cwnd)                       | T-CC-INIT                |
| S-004        | state+event | NewAckSS                          | T-CC-SS-GROW             |
| S-005        | state+event | NewAckCA                          | T-CC-CA-GROW             |
| S-005b       | state+event | NewAckSS (SS→CA)                  | T-CC-SS2CA               |
| S-006        | 分類        | DupAck (guard flightSize>0)       | -                        |
| S-007        | state+event | DupAckLimited                     | T-CC-LIMITED-XMIT        |
| S-008        | event       | EnterFR (retransmit)              | T-CC-FASTRETX            |
| S-009        | event       | EnterFR (ssthresh,cwnd inflate)   | T-CC-ENTER-FR            |
| S-010        | state+event | DupAckInFR                        | T-CC-FR-INFLATE          |
| S-011        | event       | ExitFR (deflate)                  | T-CC-DEFLATE             |
| S-012        | event       | RtoExpire (初回半減)              | T-CC-RTO-HALVE           |
| S-013        | unwanted    | RtoExpire (再送済み保持)          | T-CC-RTO-HOLD            |
| S-014        | event       | RtoExpire (cwnd=1)                | T-CC-RTO-LW              |
| S-015        | event       | RtoExpire (→SS)                   | T-CC-RTO-SS              |
| S-016        | event       | RtoExpire (rtoStage+1)            | T-CC-RTO-BACKOFF         |
| S-017        | state+event | RtoExpire (飽和 stageMax)         | T-CC-RTO-CAP             |
| S-018        | event       | NewAck* (rtoStage→0, flag→FALSE)  | T-CC-RECOVER             |
| S-019        | liveness    | Liveness (WF on NewAck)           | T-CC-LIVE                |
| S-020        | safety      | CwndLowerInv (Inv)                | T-CC-INV-CWND            |
| S-021        | safety      | SsthreshLowerInv (Inv)            | T-CC-INV-SSTHRESH        |
| S-022        | safety      | SsthreshBoundInv (Inv)            | T-CC-INV-SSBOUND         |
| S-023        | safety      | StateSelectInv (Inv)              | T-CC-INV-STATE           |
| S-024        | safety/遷移 | DeflateInv (Inv, FR 事後)         | T-CC-INV-DEFLATE         |
| S-025        | safety      | RtoMonotoneProp (action property) | T-CC-INV-RTOMONO         |

全 S-001..S-025 が `[x]`、欠番なし。

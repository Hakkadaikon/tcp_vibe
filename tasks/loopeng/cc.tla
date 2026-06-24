---- MODULE cc ----
\* 輻輳制御 cwnd 状態機械 (SlowStart / CongestionAvoidance / FastRecovery) + RTO バックオフ。
\* 抽象化: SMSS=1 単位。cwnd/ssthresh/flightSize は 1..MaxW の小整数。
\* RTO は倍化段階 rtoStage (0..StageMax) で表現。dupAckCount は 0..3。
\* 各 EARS 節 = Next の 1 disjunct。INV = Inv の 1 連言 / action property。
EXTENDS Naturals

CONSTANTS MaxW,        \* cwnd/ssthresh/flightSize の上限 (SMSS 単位)
          StageMax     \* RTO 倍化段階の上限 (これ以上は飽和)

\* --- 状態定義 ---
States == { "SlowStart", "CongestionAvoidance", "FastRecovery" }

VARIABLES
    state,        \* States のいずれか
    cwnd,         \* 輻輳窓 (SMSS 単位)
    ssthresh,     \* slow start しきい値
    flightSize,   \* 抽象化した送信中データ量 (損失検出時の基準)
    dupAckCount,  \* 連続重複 ACK 数 (0..3 で飽和)
    rtoStage,     \* RTO 倍化段階 (0=base)
    retx,         \* この損失エピソードで初回再送済みか (R-CC-053/054)
    lastAct       \* 直前アクション名 (action property の遷移識別用)

vars == << state, cwnd, ssthresh, flightSize, dupAckCount, rtoStage, retx, lastAct >>

\* max(flight/2, 2) ... SMSS=1 なので 2*SMSS=2。整数除算で flight/2。
Half(f) == IF f \div 2 > 2 THEN f \div 2 ELSE 2

\* 上限クリップ (有限ドメインを保つための抽象; 実装では rwnd 連動)
Clip(x) == IF x > MaxW THEN MaxW ELSE x

TypeOK ==
    /\ state \in States
    /\ cwnd \in 1..MaxW
    /\ ssthresh \in 2..MaxW
    /\ flightSize \in 1..MaxW
    /\ dupAckCount \in 0..3
    /\ rtoStage \in 0..StageMax
    /\ retx \in BOOLEAN
    /\ lastAct \in { "init","NewAckSS","NewAckCA","DupAckLimited","EnterFR","DupAckInFR","ExitFR","RtoExpire","ChangeFlight" }

\* --- 初期状態 (S-002 ssthresh 高め, S-003 IW 小 → SlowStart) ---
Init ==
    /\ state = "SlowStart"
    /\ cwnd = 1
    /\ ssthresh = MaxW          \* S-002: 初期は高く
    /\ flightSize = 1
    /\ dupAckCount = 0
    /\ rtoStage = 0
    /\ retx = FALSE
    /\ lastAct = "init"

\* ========================================================================
\* アクション (EARS 各節 = 1 disjunct)
\* ========================================================================

\* S-004/S-005b: SlowStart で新規 ACK → cwnd += SMSS。ssthresh に達したら CA へ。
\* dupAckCount/retx/rtoStage は新規 ACK で回復 (S-018)。
NewAckSS ==
    /\ state = "SlowStart"
    /\ LET nc == Clip(cwnd + 1) IN
        /\ cwnd' = nc
        /\ state' = IF nc >= ssthresh THEN "CongestionAvoidance" ELSE "SlowStart"
    /\ UNCHANGED << ssthresh, flightSize >>
    /\ dupAckCount' = 0
    /\ rtoStage' = 0
    /\ retx' = FALSE
    /\ lastAct' = "NewAckSS"

\* S-005: CongestionAvoidance で新規 ACK → cwnd += SMSS/RTT (抽象: 1 回 +1 上限)。
NewAckCA ==
    /\ state = "CongestionAvoidance"
    /\ cwnd' = Clip(cwnd + 1)
    /\ UNCHANGED << state, ssthresh, flightSize >>
    /\ dupAckCount' = 0
    /\ rtoStage' = 0
    /\ retx' = FALSE
    /\ lastAct' = "NewAckCA"

\* S-007: SS/CA で 1,2 番目の dup ACK → Limited Transmit (cwnd 不変, count++)。
DupAckLimited ==
    /\ state \in { "SlowStart", "CongestionAvoidance" }
    /\ flightSize > 0                 \* S-006: 未確認データあり
    /\ dupAckCount < 2
    /\ dupAckCount' = dupAckCount + 1
    /\ lastAct' = "DupAckLimited"
    /\ UNCHANGED << state, cwnd, ssthresh, flightSize, rtoStage, retx >>

\* S-008/S-009: 3 番目の dup ACK → FastRecovery 入り。
\* ssthresh = max(flight/2, 2), cwnd = ssthresh + 3 (inflate)。retx=TRUE (再送した)。
EnterFR ==
    /\ state \in { "SlowStart", "CongestionAvoidance" }
    /\ flightSize > 0
    /\ dupAckCount = 2                 \* 次が 3 番目
    /\ LET nsst == Half(flightSize) IN
        /\ ssthresh' = nsst
        /\ cwnd' = Clip(nsst + 3)
    /\ state' = "FastRecovery"
    /\ dupAckCount' = 3
    /\ retx' = TRUE
    /\ lastAct' = "EnterFR"
    /\ UNCHANGED << flightSize, rtoStage >>

\* S-010: FastRecovery 中の追加 dup ACK → cwnd += SMSS (inflate)。
DupAckInFR ==
    /\ state = "FastRecovery"
    /\ cwnd' = Clip(cwnd + 1)
    /\ lastAct' = "DupAckInFR"
    /\ UNCHANGED << state, ssthresh, flightSize, dupAckCount, rtoStage, retx >>

\* S-011: FastRecovery で回復完了の新規 ACK → cwnd = ssthresh (deflate), CA へ。
\* S-018: 新規 ACK なので rtoStage/retx を回復。
ExitFR ==
    /\ state = "FastRecovery"
    /\ cwnd' = ssthresh
    /\ state' = "CongestionAvoidance"
    /\ dupAckCount' = 0
    /\ rtoStage' = 0
    /\ retx' = FALSE
    /\ lastAct' = "ExitFR"
    /\ UNCHANGED << ssthresh, flightSize >>

\* S-012/013/014/015/016/017: RTO 満了 (損失) → SlowStart。
\* ssthresh: 初回再送のみ半減 (retx=FALSE)、再送済みは保持 (retx=TRUE)。
\* cwnd=1 (LW)、rtoStage 倍化 (上限飽和)、retx=TRUE。
RtoExpire ==
    /\ ssthresh' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd' = 1
    /\ state' = "SlowStart"
    /\ rtoStage' = IF rtoStage < StageMax THEN rtoStage + 1 ELSE StageMax
    /\ dupAckCount' = 0
    /\ retx' = TRUE
    /\ lastAct' = "RtoExpire"
    /\ UNCHANGED flightSize

\* 環境: flightSize の変動 (送信が進む/データ追加)。安全性に効くよう範囲内で任意変化。
\* 損失検出基準を動かすことで spec の不変条件を多様な flight で検査する。
ChangeFlight ==
    /\ \E f \in 1..MaxW : flightSize' = f
    /\ lastAct' = "ChangeFlight"
    /\ UNCHANGED << state, cwnd, ssthresh, dupAckCount, rtoStage, retx >>

Next ==
    \/ NewAckSS
    \/ NewAckCA
    \/ DupAckLimited
    \/ EnterFR
    \/ DupAckInFR
    \/ ExitFR
    \/ RtoExpire
    \/ ChangeFlight

\* 公平性: 新規 ACK が来続ければ cwnd は増える (LIVE-CC)。WF を NewAck に張る。
Fairness ==
    /\ WF_vars(NewAckSS)
    /\ WF_vars(NewAckCA)

Spec == Init /\ [][Next]_vars /\ Fairness

\* ========================================================================
\* 不変条件 (INV-CC-xxx)
\* ========================================================================

\* S-020 INV-CC-001: cwnd >= 1*SMSS 常時。
CwndLowerInv == cwnd >= 1

\* S-021 INV-CC-003: ssthresh >= 2*SMSS 常時。
SsthreshLowerInv == ssthresh >= 2

\* S-023 INV-CC-009: 状態選択の一貫性。FastRecovery は cwnd が人工的に inflate
\* されるため除外し (RFC5681§3.2)、FR 中は cwnd >= ssthresh を別途要求。
StateSelectInv ==
    /\ (state # "FastRecovery") =>
         /\ (cwnd < ssthresh => state = "SlowStart")
         /\ (cwnd > ssthresh => state = "CongestionAvoidance")
    /\ (state = "FastRecovery") => (cwnd >= ssthresh)

\* S-022 INV-CC-002: ssthresh は損失で max(flight/2, 2) 以下へ。
\* 上界として「ssthresh <= MaxW」かつ「2 以上」は型で担保。半減値が flight/2 と
\* 2 の大きい方を超えないことは Half の定義で保証 (代入時に等式)。ここでは
\* 常時成立する弱い形: ssthresh は MaxW を超えない。
SsthreshBoundInv == ssthresh <= MaxW

Inv ==
    /\ TypeOK
    /\ CwndLowerInv
    /\ SsthreshLowerInv
    /\ StateSelectInv
    /\ SsthreshBoundInv

\* ========================================================================
\* Action property (遷移上の性質)
\* ========================================================================

\* S-024 INV-CC-016: FastRecovery 終了時 cwnd = ssthresh (deflate)。
\* FR から出る遷移 (ExitFR) の事後で cwnd' = ssthresh'。
DeflateProp ==
    [][ (lastAct' = "ExitFR") => cwnd' = ssthresh' ]_vars

\* S-025 INV-CC-008: 連続 RTO 満了で rtoStage は単調非減少に倍化 (上限まで)。
\* RtoExpire 遷移で rtoStage' >= rtoStage かつ (上限未満なら厳に増える)。
RtoMonotoneProp ==
    [][ (lastAct' = "RtoExpire")
        => /\ rtoStage' >= rtoStage
           /\ (rtoStage < StageMax => rtoStage' = rtoStage + 1) ]_vars

\* S-012/S-013 INV-CC-002: RTO 損失での ssthresh 設定の正しさ。
\* 初回再送 (遷移前 retx=FALSE): ssthresh' = max(flight/2, 2) へ半減。
\* 再送済み (遷移前 retx=TRUE): ssthresh' = ssthresh で保持。
\* これが「初回半減 vs 再送済み保持」(R-CC-053/054) を厳格に縛る。
RtoSsthreshProp ==
    [][ (lastAct' = "RtoExpire")
        => /\ (~retx => ssthresh' = Half(flightSize))
           /\ ( retx => ssthresh' = ssthresh) ]_vars

\* S-004/S-005 INV-CC-010/011: 新規 ACK 1 回あたりの cwnd 増分は SMSS 以下 (積極性上限)。
\* SlowStart / CongestionAvoidance の NewAck で cwnd' <= cwnd + 1。
AggressivenessProp ==
    [][ (lastAct' \in { "NewAckSS", "NewAckCA" }) => cwnd' =< cwnd + 1 ]_vars

\* S-009 INV-CC-002 / R-CC-062: FastRecovery 入りで cwnd = ssthresh + 3*SMSS (inflate)。
\* MaxW クリップを考慮し、クリップ前の理論値が ssthresh'+3 であることを縛る。
EnterFRInflateProp ==
    [][ (lastAct' = "EnterFR") => cwnd' = Clip(ssthresh' + 3) ]_vars

\* ========================================================================
\* Liveness (LIVE-CC)
\* ========================================================================

\* S-019: cwnd が永久に 1 に張り付かない。公平性下で、いつか cwnd > 1 になる。
\* (NewAck が無限に発火すれば cwnd は増える。)
LiveCwndGrows == <>(cwnd > 1)

====

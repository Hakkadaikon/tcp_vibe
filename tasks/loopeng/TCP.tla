---------------------------- MODULE TCP ----------------------------
(***************************************************************************)
(* TCP 接続状態機械 (RFC 9293 + RFC 5961) の設計検査モデル。              *)
(* 源泉: tasks/loopeng/TCP.extract.md / requirements.md。                 *)
(* スコープ: 1 ピアの状態機械 + 敵対的環境(相手 + 順序入替/重複/欠落    *)
(* しうるネットワーク)。任意のセグメントがガード下でいつでも到達しうる   *)
(* 近似で「敵対環境下でも安全性が保たれる」を網羅探索する。              *)
(*                                                                       *)
(* seq/ack は小有限ドメイン 0..SeqMax に抽象化。受理性の本質(窓外 /     *)
(* =RCV.NXT / 窓内≠NXT)を seq 値で表現する。                           *)
(***************************************************************************)
EXTENDS Naturals

CONSTANTS SeqMax,    \* seq/ack 抽象ドメインの上限 (例 4)。ラップ確認に最小値。
          RcvWnd     \* 受信窓幅 (例 2)。窓内/窓外の境界を作る。

VARIABLES
    st,        \* 接続状態 (下記 11 状態のいずれか)
    origin,    \* SYN-RCVD の由来: "passive" | "active" | "none"  (INV-014/S-051)
    sndUna,    \* SND.UNA
    sndNxt,    \* SND.NXT
    rcvNxt,    \* RCV.NXT
    finAcked,  \* 自分の FIN が ack されたか (FIN-WAIT-1 の分岐用)
    twTimer,   \* TIME-WAIT の 2MSL カウンタ (Max..0、0 で満了)  (INV-011/S-050)
    didReset,  \* 直近の遷移で RST により reset したか (mutation oracle 観測用)
    didChallenge, \* 直近の遷移で challenge ACK を送ったか (観測用)
    rstKind    \* reset の根拠: "none"|"at_nxt"|"oow"|"inwin"|"synsent"|"synrcvd"|"timewait"
               \* INV-005: 同期で reset してよいのは "at_nxt" 由来のみ(窓外/窓内≠NXT は不可)

vars == << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer,
           didReset, didChallenge, rstKind >>

MSL == 2  \* 2MSL = TwInit ステップ。TIME-WAIT linger の抽象。
TwInit == 2

States == { "CLOSED", "LISTEN", "SYN_SENT", "SYN_RCVD", "ESTAB",
            "FIN_WAIT_1", "FIN_WAIT_2", "CLOSE_WAIT", "CLOSING",
            "LAST_ACK", "TIME_WAIT" }

Synchronized == { "ESTAB", "FIN_WAIT_1", "FIN_WAIT_2", "CLOSE_WAIT",
                  "CLOSING", "LAST_ACK", "TIME_WAIT" }

(***************************************************************************)
(* 許可された状態遷移辺 (TCP.extract.md §状態遷移 / requirements.md §2)。 *)
(* INV-A はこの集合に属する辺だけが起きることを保証する。               *)
(***************************************************************************)
AllowedEdges ==
    { <<"CLOSED","SYN_SENT">>, <<"CLOSED","LISTEN">>, <<"CLOSED","CLOSED">>,
      <<"LISTEN","SYN_RCVD">>, <<"LISTEN","LISTEN">>, <<"LISTEN","SYN_SENT">>,
      <<"SYN_SENT","ESTAB">>, <<"SYN_SENT","SYN_RCVD">>, <<"SYN_SENT","CLOSED">>,
      <<"SYN_SENT","SYN_SENT">>,
      <<"SYN_RCVD","ESTAB">>, <<"SYN_RCVD","LISTEN">>, <<"SYN_RCVD","CLOSED">>,
      <<"SYN_RCVD","FIN_WAIT_1">>, <<"SYN_RCVD","CLOSE_WAIT">>,
      <<"SYN_RCVD","SYN_RCVD">>,
      <<"ESTAB","FIN_WAIT_1">>, <<"ESTAB","CLOSE_WAIT">>, <<"ESTAB","CLOSED">>,
      <<"ESTAB","ESTAB">>,
      <<"FIN_WAIT_1","FIN_WAIT_2">>, <<"FIN_WAIT_1","CLOSING">>,
      <<"FIN_WAIT_1","TIME_WAIT">>, <<"FIN_WAIT_1","CLOSED">>,
      <<"FIN_WAIT_1","FIN_WAIT_1">>,
      <<"FIN_WAIT_2","TIME_WAIT">>, <<"FIN_WAIT_2","CLOSED">>,
      <<"FIN_WAIT_2","FIN_WAIT_2">>,
      <<"CLOSE_WAIT","LAST_ACK">>, <<"CLOSE_WAIT","CLOSED">>,
      <<"CLOSE_WAIT","CLOSE_WAIT">>,
      <<"CLOSING","TIME_WAIT">>, <<"CLOSING","CLOSED">>, <<"CLOSING","CLOSING">>,
      <<"LAST_ACK","CLOSED">>, <<"LAST_ACK","LAST_ACK">>,
      <<"TIME_WAIT","CLOSED">>, <<"TIME_WAIT","TIME_WAIT">> }

TypeOK ==
    /\ st \in States
    /\ origin \in { "passive", "active", "none" }
    /\ sndUna \in 0..SeqMax
    /\ sndNxt \in 0..SeqMax
    /\ rcvNxt \in 0..SeqMax
    /\ finAcked \in BOOLEAN
    /\ twTimer \in 0..TwInit
    /\ didReset \in BOOLEAN
    /\ didChallenge \in BOOLEAN
    /\ rstKind \in { "none","at_nxt","oow","inwin","synsent","synrcvd","timewait" }

Init ==
    /\ st = "CLOSED"
    /\ origin = "none"
    /\ sndUna = 0
    /\ sndNxt = 0
    /\ rcvNxt = 0
    /\ finAcked = FALSE
    /\ twTimer = 0
    /\ didReset = FALSE
    /\ didChallenge = FALSE
    /\ rstKind = "none"

(***************************************************************************)
(* 受理性の抽象: あるセグメント seq s が受信窓に入るか。                  *)
(* RcvWnd=0 のときは s=rcvNxt のみ受理 (R-001)。                          *)
(***************************************************************************)
InWindow(s) ==
    IF RcvWnd = 0 THEN s = rcvNxt
    ELSE (rcvNxt <= s) /\ (s < rcvNxt + RcvWnd)

\* acceptable ACK: SND.UNA < ack <= SND.NXT  (R-011/INV-002)
AcceptableAck(a) == (sndUna < a) /\ (a =< sndNxt)

\* RFC5961 ACK 受理範囲: (SND.UNA - MAXWND) <= ack <= SND.NXT。
\* 抽象では MAXWND を SeqMax 全域とみなさず、ここでは下端 0 まで許容して
\* 「上端 SND.NXT 超えは不可」を本質として表現 (R-115/INV-007)。
InAckRange(a) == a =< sndNxt

\* 観測フラグをリセットしつつ遷移する補助 (非 reset アクション用)
ClearFlags == /\ didReset' = FALSE /\ didChallenge' = FALSE /\ rstKind' = "none"

(***************************************************************************)
(* 確立 (3way / 同時オープン)                                            *)
(***************************************************************************)
ActiveOpen ==          \* S-001
    /\ st = "CLOSED"
    /\ st' = "SYN_SENT"
    /\ sndUna' = 0 /\ sndNxt' = 1   \* ISS=0, SND.NXT=ISS+1
    /\ origin' = "active"
    /\ UNCHANGED << rcvNxt, finAcked, twTimer >>
    /\ ClearFlags

PassiveOpen ==         \* S-002
    /\ st = "CLOSED"
    /\ st' = "LISTEN"
    /\ origin' = "passive"
    /\ UNCHANGED << sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ ClearFlags

RcvSynInListen ==      \* S-003
    /\ st = "LISTEN"
    /\ st' = "SYN_RCVD"
    /\ origin' = "passive"
    /\ rcvNxt' = 1              \* RCV.NXT = SEG.SEQ+1 (IRS=0 抽象)
    /\ sndUna' = 0 /\ sndNxt' = 1
    /\ UNCHANGED << finAcked, twTimer >>
    /\ ClearFlags

RcvRstInListen ==      \* S-004  RST は無視
    /\ st = "LISTEN"
    /\ st' = "LISTEN"
    /\ UNCHANGED << origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset' = FALSE        \* reset していないことを明示
    /\ didChallenge' = FALSE
    /\ rstKind' = "none"

RcvSynAck ==           \* S-005  自SYN を ack する SYN,ACK
    /\ st = "SYN_SENT"
    /\ AcceptableAck(sndNxt)    \* SYN,ACK が ISS+1 を ack
    /\ st' = "ESTAB"
    /\ sndUna' = sndNxt
    /\ rcvNxt' = 1
    /\ UNCHANGED << origin, sndNxt, finAcked, twTimer >>
    /\ ClearFlags

SimOpen ==             \* S-006  同時オープン (bare SYN)
    /\ st = "SYN_SENT"
    /\ st' = "SYN_RCVD"
    /\ origin' = "active"       \* active 由来を維持
    /\ rcvNxt' = 1
    /\ UNCHANGED << sndUna, sndNxt, finAcked, twTimer >>
    /\ ClearFlags

RcvRstSynSent ==       \* S-007  ACK が自SYN を確認する RST のみ受理
    /\ st = "SYN_SENT"
    /\ AcceptableAck(sndNxt)
    /\ st' = "CLOSED"
    /\ origin' = "none"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ twTimer' = 0
    /\ didReset' = TRUE
    /\ didChallenge' = FALSE
    /\ rstKind' = "synsent"

RcvAckSynRcvd ==       \* S-008  acceptable ACK of SYN
    /\ st = "SYN_RCVD"
    /\ AcceptableAck(sndNxt)
    /\ st' = "ESTAB"
    /\ sndUna' = sndNxt
    /\ UNCHANGED << origin, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ ClearFlags

RcvRstSynRcvdPassive ==  \* S-009  passive 由来 → LISTEN
    /\ st = "SYN_RCVD"
    /\ origin = "passive"
    /\ st' = "LISTEN"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ twTimer' = 0
    /\ UNCHANGED origin
    /\ didReset' = TRUE
    /\ didChallenge' = FALSE
    /\ rstKind' = "synrcvd"

RcvRstSynRcvdActive ==   \* S-010  active 由来 → CLOSED
    /\ st = "SYN_RCVD"
    /\ origin = "active"
    /\ st' = "CLOSED"
    /\ origin' = "none"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ twTimer' = 0
    /\ didReset' = TRUE
    /\ didChallenge' = FALSE
    /\ rstKind' = "synrcvd"

(***************************************************************************)
(* 終了                                                                   *)
(***************************************************************************)
CloseEstab ==          \* S-011
    /\ st = "ESTAB"
    /\ st' = "FIN_WAIT_1"
    /\ sndNxt' = (sndNxt + 1) % (SeqMax + 1)   \* FIN が 1 seq 消費
    /\ finAcked' = FALSE
    /\ UNCHANGED << origin, sndUna, rcvNxt, twTimer >>
    /\ ClearFlags

RcvFinEstab ==         \* S-012
    /\ st = "ESTAB"
    /\ st' = "CLOSE_WAIT"
    /\ rcvNxt' = (rcvNxt + 1) % (SeqMax + 1)
    /\ UNCHANGED << origin, sndUna, sndNxt, finAcked, twTimer >>
    /\ ClearFlags

CloseSynRcvd ==        \* S-024
    /\ st = "SYN_RCVD"
    /\ st' = "FIN_WAIT_1"
    /\ sndNxt' = (sndNxt + 1) % (SeqMax + 1)
    /\ finAcked' = FALSE
    /\ UNCHANGED << origin, sndUna, rcvNxt, twTimer >>
    /\ ClearFlags

RcvFinSynRcvd ==       \* S-023
    /\ st = "SYN_RCVD"
    /\ st' = "CLOSE_WAIT"
    /\ rcvNxt' = (rcvNxt + 1) % (SeqMax + 1)
    /\ UNCHANGED << origin, sndUna, sndNxt, finAcked, twTimer >>
    /\ ClearFlags

RcvAckFW1 ==           \* S-013  自FIN が ack された
    /\ st = "FIN_WAIT_1"
    /\ AcceptableAck(sndNxt)
    /\ st' = "FIN_WAIT_2"
    /\ sndUna' = sndNxt
    /\ finAcked' = TRUE
    /\ UNCHANGED << origin, sndNxt, rcvNxt, twTimer >>
    /\ ClearFlags

RcvFinFW1Closing ==    \* S-014  FIN 到来・自FIN 未ACK → CLOSING
    /\ st = "FIN_WAIT_1"
    /\ ~finAcked
    /\ st' = "CLOSING"
    /\ rcvNxt' = (rcvNxt + 1) % (SeqMax + 1)
    /\ UNCHANGED << origin, sndUna, sndNxt, finAcked, twTimer >>
    /\ ClearFlags

RcvFinAckFW1 ==        \* S-015  FIN+ACK(自FIN ACK済) → TIME-WAIT
    /\ st = "FIN_WAIT_1"
    /\ AcceptableAck(sndNxt)
    /\ st' = "TIME_WAIT"
    /\ sndUna' = sndNxt
    /\ finAcked' = TRUE
    /\ rcvNxt' = (rcvNxt + 1) % (SeqMax + 1)
    /\ twTimer' = TwInit
    /\ UNCHANGED << origin, sndNxt >>
    /\ ClearFlags

RcvFinFW2 ==           \* S-016
    /\ st = "FIN_WAIT_2"
    /\ st' = "TIME_WAIT"
    /\ rcvNxt' = (rcvNxt + 1) % (SeqMax + 1)
    /\ twTimer' = TwInit
    /\ UNCHANGED << origin, sndUna, sndNxt, finAcked >>
    /\ ClearFlags

CloseCloseWait ==      \* S-017
    /\ st = "CLOSE_WAIT"
    /\ st' = "LAST_ACK"
    /\ sndNxt' = (sndNxt + 1) % (SeqMax + 1)
    /\ finAcked' = FALSE
    /\ UNCHANGED << origin, sndUna, rcvNxt, twTimer >>
    /\ ClearFlags

RcvAckClosing ==       \* S-018
    /\ st = "CLOSING"
    /\ AcceptableAck(sndNxt)
    /\ st' = "TIME_WAIT"
    /\ sndUna' = sndNxt
    /\ finAcked' = TRUE
    /\ twTimer' = TwInit
    /\ UNCHANGED << origin, sndNxt, rcvNxt >>
    /\ ClearFlags

RcvAckLastAck ==       \* S-019
    /\ st = "LAST_ACK"
    /\ AcceptableAck(sndNxt)
    /\ st' = "CLOSED"
    /\ origin' = "none"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ twTimer' = 0
    /\ ClearFlags

(***************************************************************************)
(* TIME-WAIT linger (INV-011/S-050)                                       *)
(***************************************************************************)
TimeWaitTick ==        \* 2MSL カウントダウン。0 になるまで CLOSED へ行けない
    /\ st = "TIME_WAIT"
    /\ twTimer > 0
    /\ twTimer' = twTimer - 1
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked >>
    /\ ClearFlags

TimeWaitExpire ==      \* S-020  満了してから CLOSED
    /\ st = "TIME_WAIT"
    /\ twTimer = 0
    /\ st' = "CLOSED"
    /\ origin' = "none"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ UNCHANGED twTimer
    /\ ClearFlags

RcvFinTimeWait ==      \* S-021  FIN 再到来 → re-ack, 2MSL 再起動
    /\ st = "TIME_WAIT"
    /\ st' = "TIME_WAIT"
    /\ twTimer' = TwInit
    /\ UNCHANGED << origin, sndUna, sndNxt, rcvNxt, finAcked >>
    /\ ClearFlags

RcvRstTimeWait ==      \* S-022
    /\ st = "TIME_WAIT"
    /\ st' = "CLOSED"
    /\ origin' = "none"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ twTimer' = 0
    /\ didReset' = TRUE
    /\ didChallenge' = FALSE
    /\ rstKind' = "timewait"

(***************************************************************************)
(* RFC 5961 三チェック (同期状態)。敵対的に任意 seq の RST/SYN が届く。   *)
(***************************************************************************)
\* S-025/S-031: 同期 + RST + SEG.SEQ=RCV.NXT → reset。CLOSED へ。
RstAtRcvNxt ==
    /\ st \in Synchronized
    /\ st' = "CLOSED"
    /\ origin' = "none"
    /\ sndUna' = 0 /\ sndNxt' = 0 /\ rcvNxt' = 0 /\ finAcked' = FALSE
    /\ twTimer' = 0
    /\ didReset' = TRUE
    /\ didChallenge' = FALSE
    /\ rstKind' = "at_nxt"

\* S-030: 同期 + RST + 窓外 → silently drop, 状態不変, reset しない。
RstOutOfWindow ==
    /\ st \in Synchronized
    /\ \E s \in 0..SeqMax : ~InWindow(s)   \* 窓外 seq が存在しうる
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset' = FALSE
    /\ didChallenge' = FALSE
    /\ rstKind' = "oow"

\* S-032: 同期 + RST + 窓内 ≠ RCV.NXT → challenge ACK, 状態不変, reset しない。
RstInWindowNotNxt ==
    /\ st \in Synchronized
    /\ \E s \in 0..SeqMax : InWindow(s) /\ s # rcvNxt
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset' = FALSE
    /\ didChallenge' = TRUE
    /\ rstKind' = "inwin"

\* S-033: 同期 + SYN → challenge ACK, 状態不変, reset しない (INV-006)。
SynChallenge ==
    /\ st \in Synchronized
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset' = FALSE
    /\ didChallenge' = TRUE
    /\ rstKind' = "none"

(***************************************************************************)
(* データ転送・ACK (INV-001/002/007)                                     *)
(***************************************************************************)
\* S-040: acceptable ACK → SND.UNA 前進。sending 状態で。
RcvAckData ==
    /\ st \in { "ESTAB", "FIN_WAIT_1", "CLOSE_WAIT" }
    /\ \E a \in 0..SeqMax :
         /\ AcceptableAck(a)
         /\ sndUna' = a
    /\ UNCHANGED << st, origin, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ ClearFlags

\* S-041: 窓内データ → RCV.NXT 前進。
RcvData ==
    /\ st \in { "ESTAB", "FIN_WAIT_1", "FIN_WAIT_2" }
    /\ rcvNxt' = (rcvNxt + 1) % (SeqMax + 1)
    /\ UNCHANGED << st, origin, sndUna, sndNxt, finAcked, twTimer >>
    /\ ClearFlags

\* S-042/S-034: 受理範囲外 ACK → 破棄, SND.UNA 不変, reset/challenge せず ACK 返すのみ。
RcvBadAck ==
    /\ st \in Synchronized
    /\ \E a \in 0..SeqMax : ~InAckRange(a)   \* 範囲外 ACK が存在しうる
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset' = FALSE
    /\ didChallenge' = FALSE
    /\ rstKind' = "none"

(***************************************************************************)
(* Next: EARS 各節 = 1 disjunct。                                         *)
(***************************************************************************)
Next ==
    \/ ActiveOpen \/ PassiveOpen
    \/ RcvSynInListen \/ RcvRstInListen
    \/ RcvSynAck \/ SimOpen \/ RcvRstSynSent
    \/ RcvAckSynRcvd \/ RcvRstSynRcvdPassive \/ RcvRstSynRcvdActive
    \/ CloseSynRcvd \/ RcvFinSynRcvd
    \/ CloseEstab \/ RcvFinEstab
    \/ RcvAckFW1 \/ RcvFinFW1Closing \/ RcvFinAckFW1
    \/ RcvFinFW2
    \/ CloseCloseWait
    \/ RcvAckClosing
    \/ RcvAckLastAck
    \/ TimeWaitTick \/ TimeWaitExpire \/ RcvFinTimeWait \/ RcvRstTimeWait
    \/ RstAtRcvNxt \/ RstOutOfWindow \/ RstInWindowNotNxt \/ SynChallenge
    \/ RcvAckData \/ RcvData \/ RcvBadAck

(***************************************************************************)
(* 公平性: 活性に必要な遷移にのみ張る。                                   *)
(*  - WF(TimeWaitTick), WF(TimeWaitExpire): TIME-WAIT がいつか抜ける       *)
(*    (LIVE-2)。tick は減少し expire でいつか CLOSED。                     *)
(*  - WF(RcvSynAck) 等の確立遷移には張らない: 相手応答は環境依存で        *)
(*    保証できない。よって LIVE-1 は「ESTAB か CLOSED の到達可能性」を     *)
(*    弱い形(到達できる経路が存在)で見る。                              *)
(* 注意: RstOutOfWindow / RstInWindowNotNxt / SynChallenge は無限に        *)
(* stutter しうるため、これらに公平性を張らないことで活性の偽陽性を避ける。*)
(***************************************************************************)
Spec == Init /\ [][Next]_vars
            /\ WF_vars(TimeWaitTick)
            /\ WF_vars(TimeWaitExpire)

(***************************************************************************)
(* 活性検査用サブ仕様: 敵対的 FIN による 2MSL 再起動 (RcvFinTimeWait) を   *)
(* 除いた遷移系。これは「FIN 攻撃が止めば TIME-WAIT はいつか抜ける」を    *)
(* 検証する(条件付き活性)。無条件の LIVE-2 は敵対環境では成立しない     *)
(* (FIN を無限送出されれば滞留しうる = RFC 既知挙動)ことが、フル Next で *)
(* の反例で示される。                                                     *)
(***************************************************************************)
NextNoFinFlood ==
    \/ ActiveOpen \/ PassiveOpen
    \/ RcvSynInListen \/ RcvRstInListen
    \/ RcvSynAck \/ SimOpen \/ RcvRstSynSent
    \/ RcvAckSynRcvd \/ RcvRstSynRcvdPassive \/ RcvRstSynRcvdActive
    \/ CloseSynRcvd \/ RcvFinSynRcvd
    \/ CloseEstab \/ RcvFinEstab
    \/ RcvAckFW1 \/ RcvFinFW1Closing \/ RcvFinAckFW1
    \/ RcvFinFW2
    \/ CloseCloseWait
    \/ RcvAckClosing
    \/ RcvAckLastAck
    \/ TimeWaitTick \/ TimeWaitExpire \/ RcvRstTimeWait
    \/ RstAtRcvNxt \/ RstOutOfWindow \/ RstInWindowNotNxt \/ SynChallenge
    \/ RcvAckData \/ RcvData \/ RcvBadAck

SpecLive == Init /\ [][NextNoFinFlood]_vars
                 /\ WF_vars(TimeWaitTick)
                 /\ WF_vars(TimeWaitExpire)

(***************************************************************************)
(* 安全性不変条件                                                         *)
(***************************************************************************)
\* INV-A: 状態遷移は許可辺のみ
EdgeOK == LET e == <<st, st'>> IN e \in AllowedEdges
TransOK == [][ EdgeOK ]_vars

\* INV-001: 送信窓単調 SND.UNA <= SND.NXT。
\*   FIN により sndNxt が wrap した場合を除外するため、wrap してない通常域で見る。
\*   抽象モデルでは Close*/RcvFin* 以外で sndNxt は単調増加。ここは
\*   「acceptable ack でしか una が nxt を超えない」を見る。
InvUnaLeNxt == sndUna =< sndNxt

\* INV-005: 同期で reset したのは RstAtRcvNxt 由来のときだけ。
\*   = didReset=TRUE かつ 同期だったなら、窓外/窓内≠NXT の RST 由来ではない。
\*   モデル構造上 RstOutOfWindow/RstInWindowNotNxt は didReset'=FALSE なので、
\*   「同期状態から didReset で CLOSED に落ちる遷移は RstAtRcvNxt だけ」を
\*   action property で見る。ここでは状態述語版: reset 後は CLOSED。
InvResetGoesClosed == didReset => (st = "CLOSED" \/ st = "LISTEN")

\* INV-005 (強化): 窓外 RST・窓内≠NXT RST は決して reset しない。
\*   reset が立っているなら、その根拠は窓外/窓内≠NXT 由来であってはならない。
\*   (RstAtRcvNxt="at_nxt" や SYN-SENT/SYN-RCVD/TIME-WAIT 経路のみが reset 可)
InvRstStrict == didReset => (rstKind \notin { "oow", "inwin" })

\* INV-006: 同期で SYN は reset しない = challenge と reset は排他。
InvNoResetOnChallenge == ~(didReset /\ didChallenge)

\* INV-011: TIME-WAIT のタイマ満了経路 (RST 等の異常 abort でない) で
\*   CLOSED へ飛ぶのは twTimer=0 のときだけ。RST 受信 (didReset'=TRUE) は
\*   RFC 9293 §2 が許す正当な即時 abort なので除外する。
InvTwLinger ==
    [][ (st = "TIME_WAIT" /\ st' = "CLOSED" /\ ~didReset')
         => (twTimer = 0) ]_vars

\* INV-014: SYN-RCVD で reset 時の遷移先が由来で決まる。action property。
InvOriginRouting ==
    [][ (st = "SYN_RCVD" /\ didReset')
         => ( (origin = "passive" => st' = "LISTEN")
              /\ (origin = "active" => st' = "CLOSED") ) ]_vars

\* INV-014 (由来の正しさ): SYN-RCVD への入り口が由来を正しく刻む。
\*   LISTEN 経由 (受動) → passive、SYN-SENT 経由 (同時オープン=能動) → active。
\*   これを誤ると後段の RST routing は整合に見えても由来が偽になる。
InvOriginSource ==
    [][ (st' = "SYN_RCVD" /\ st # "SYN_RCVD")
         => ( (st = "LISTEN" => origin' = "passive")
              /\ (st = "SYN_SENT" => origin' = "active") ) ]_vars

\* INV-007: ACK でデータ適用(una 前進)するのは受理範囲内のみ。
InvAckRange == (sndUna =< sndNxt)

\* まとめた状態不変条件 (TLC INVARIANT 用)
Inv ==
    /\ TypeOK
    /\ InvUnaLeNxt
    /\ InvResetGoesClosed
    /\ InvRstStrict
    /\ InvNoResetOnChallenge

(***************************************************************************)
(* 活性 (PROPERTY 用)                                                     *)
(***************************************************************************)
\* LIVE-2: TIME-WAIT に入ったらいつか CLOSED へ抜ける(永久滞留しない)。
LiveTimeWait == (st = "TIME_WAIT") ~> (st = "CLOSED")

=============================================================================

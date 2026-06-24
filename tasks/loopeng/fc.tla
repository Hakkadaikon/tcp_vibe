---- MODULE fc ----
\* TCP フロー制御: 受信窓更新 / zero-window persist / SWS 回避 (送受) / Nagle / delayed ACK。
\* 安全性 (窓を縮めない・最大ACKのみ採用・SWS/Nagle 抑制・delayed ACK 上限) と
\* 活性 (persist / override / delAck タイマで zero-window / SWS / Nagle+delACK デッドロック回避)。
\* 抽象化: seq は小整数、タイマは boolean armed フラグ + 発火アクション。
\* 各 EARS 節 = Next の 1 disjunct。INV = Inv の 1 連言 / temporal property。
EXTENDS Naturals

CONSTANTS
    MSS,        \* フルセグメントサイズ (SWS/Nagle 閾値の基準)
    MaxData,    \* アプリが積める未送信データ D の上限
    MaxSeq,     \* SND.UNA/SND.NXT の上限 (有限化)
    RcvBuff     \* 受信バッファ容量 (受信 SWS の基準)

\* 送信 SWS 閾値 Fs*Max(SND.WND) と受信 SWS 閾値 Fr*RcvBuff は 1/2 を整数で。
\* MaxWnd を相手広告窓の最大とみなし、Fs 閾値 = MSS (フルセグ) を主基準にする
\* (Max(SND.WND) は履歴最大だが、抽象では MSS をフル閾値として扱う)。
Min(a, b) == IF a < b THEN a ELSE b
Max(a, b) == IF a > b THEN a ELSE b
RcvSwsThresh == Min(RcvBuff \div 2, MSS)   \* min(Fr*RCV.BUFF, MSS)

VARIABLES
    sndUna,        \* SND.UNA: 未確認境界
    sndNxt,        \* SND.NXT: 次送信 seq
    sndWnd,        \* 相手の広告窓 (0 含む)
    maxAckSeen,    \* これまで見た最大 ACK (古い窓更新を弾く基準)
    appData,       \* アプリがキューに積んだ未送信データ量 D
    persistArmed,  \* 窓0で persist timer 起動中
    persistStage,  \* persist バックオフ段階 (0=base, 上限飽和)
    overrideArmed, \* 送信側 SWS override timer 起動中 (溜めて待ち中)
    rcvNxt,        \* RCV.NXT: 受信境界
    rcvWnd,        \* 自分の広告窓
    rcvUser,       \* アプリ未読量 (rcvBuf 消費分; AppRead で減る)
    delAckCnt,     \* 未 ACK のフルセグメント数 (delayed ACK 上限検査)
    delAckArmed,   \* delayed ACK timer 起動中
    lastAct        \* 直前アクション名 (temporal/trace 識別)

vars == << sndUna, sndNxt, sndWnd, maxAckSeen, appData, persistArmed,
           persistStage, overrideArmed, rcvNxt, rcvWnd, rcvUser,
           delAckCnt, delAckArmed, lastAct >>

StageMax == 2   \* persist バックオフ段階上限 (有限化)

Acts == { "init","AppWrite","SendFull","SendOverride","RecvAck","WindowZero",
          "PersistArm","PersistFire","WindowUpdate","RecvData","AppRead",
          "DelAckArm","DelAckFire" }

\* 右窓端 (受信側が広告する右端)。窓を縮めない不変条件のアンカー。
RightEdge == rcvNxt + rcvWnd

\* 送信側 usable window U = SND.UNA + SND.WND - SND.NXT。
Usable == (sndUna + sndWnd) - sndNxt

\* Nagle: 未確認データが in flight (SND.NXT > SND.UNA)。
HasUnacked == sndNxt > sndUna

TypeOK ==
    /\ sndUna \in 0..MaxSeq
    /\ sndNxt \in 0..MaxSeq
    /\ sndUna <= sndNxt
    /\ sndWnd \in 0..(MSS * 2)
    /\ maxAckSeen \in 0..MaxSeq
    /\ appData \in 0..MaxData
    /\ persistArmed \in BOOLEAN
    /\ persistStage \in 0..StageMax
    /\ overrideArmed \in BOOLEAN
    /\ rcvNxt \in 0..MaxSeq
    /\ rcvWnd \in 0..RcvBuff
    /\ rcvUser \in 0..RcvBuff
    /\ delAckCnt \in 0..2
    /\ delAckArmed \in BOOLEAN
    /\ lastAct \in Acts

Init ==
    /\ sndUna = 0
    /\ sndNxt = 0
    /\ sndWnd = MSS            \* 初期は 1 フルセグ分の窓
    /\ maxAckSeen = 0
    /\ appData = 0
    /\ persistArmed = FALSE
    /\ persistStage = 0
    /\ overrideArmed = FALSE
    /\ rcvNxt = 0
    /\ rcvWnd = RcvBuff
    /\ rcvUser = 0
    /\ delAckCnt = 0
    /\ delAckArmed = FALSE
    /\ lastAct = "init"

\* ========================================================================
\* 送信側アクション
\* ========================================================================

\* S-005前提: アプリがデータを積む (D を増やす)。
AppWrite ==
    /\ appData < MaxData
    /\ appData' = appData + 1
    /\ lastAct' = "AppWrite"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, persistArmed,
                    persistStage, overrideArmed, rcvNxt, rcvWnd, rcvUser,
                    delAckCnt, delAckArmed >>

\* 1 セグメント送信で進める量 (フル送れるならフル、足りなければ残り)。
SegLen == Min(Min(appData, Usable), MSS)

\* S-005: SWS/Nagle を満たして送れるデータがある条件。
\* (1) フル: min(D,U) >= MSS、または
\* (2) idle (SND.NXT=SND.UNA): Nagle 抑制なし、Usable があれば部分送信可。
\* 未確認データ中 (HasUnacked) の sub-MSS は Nagle で抑制 → ここでは送らない。
CanSend ==
    /\ appData > 0
    /\ Usable >= 1
    /\ sndNxt < MaxSeq
    /\ ( \* 条件(1) フルセグ: フル分の seq 余地があること (Nagle 維持)
         (appData >= MSS /\ Usable >= MSS /\ sndNxt + MSS <= MaxSeq)
         \* 条件(2) idle push: 未確認なし → 部分送信可 (seq 残余 1 で足りる)
       \/ (sndNxt = sndUna) )

\* S-005: 送信。CanSend が連続して成立する限り WF で必ず発火 (フル/部分の分岐を
\* 1 アクションに統合: 窓振動でフル⇄部分が切り替わっても "送れる" が連続するため
\* 活性が崩れない)。1 回で min(appData, Usable, MSS) を送る。
SendData ==
    /\ CanSend
    /\ LET k == Min(Min(Min(appData, Usable), MSS), MaxSeq - sndNxt) IN
        /\ k >= 1                  \* seq 残余があれば 1 octet でも送る
        /\ sndNxt' = sndNxt + k
        /\ appData' = appData - k
    /\ persistArmed' = FALSE       \* 窓>0 で送れたので persist 解除
    /\ overrideArmed' = FALSE
    /\ lastAct' = "SendFull"
    /\ UNCHANGED << sndUna, sndWnd, maxAckSeen, persistStage,
                    rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* S-006: override timeout 発火 → 閾値未満でも溜めたデータを送る (活性確保)。
\* 送信側 SWS / Nagle で止まっていた sub-MSS を送り出す唯一の脱出路。
SendOverride ==
    /\ overrideArmed
    /\ appData > 0
    /\ Usable >= 1                 \* 相手窓があること (窓0は persist の領分)
    /\ LET k == Min(Min(Min(appData, Usable), MSS), MaxSeq - sndNxt) IN
        /\ k >= 1                  \* seq 残余があれば 1 octet でも送る
        /\ sndNxt' = sndNxt + k
        /\ appData' = appData - k
    /\ overrideArmed' = FALSE
    /\ persistArmed' = FALSE
    /\ lastAct' = "SendOverride"
    /\ UNCHANGED << sndUna, sndWnd, maxAckSeen, persistStage,
                    rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* S-007 / Nagle + S-005 送信SWS: 送るデータがあり相手窓もあるのに SendData が
\* フルセグを組めない (Nagle 抑制 or usable window 不足) → 送らず override timer
\* を起動 (溜めて待つ)。これが送信側 SWS / Nagle の「待ち」状態を作り、override
\* timeout (SendOverride) が唯一の脱出路になる。idle (sndNxt=sndUna) は SendData
\* が部分送信できるので対象外。
PersistArmOverride ==
    /\ ~overrideArmed
    /\ appData > 0
    /\ Usable >= 1
    /\ HasUnacked                            \* 未確認中 (idle は SendData が処理)
    /\ ~(appData >= MSS /\ Usable >= MSS /\ sndNxt + MSS <= MaxSeq) \* フル不可
    /\ overrideArmed' = TRUE
    /\ lastAct' = "PersistArm"  \* override timer 起動 (arm)
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData, persistArmed,
                    persistStage, rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* S-003 / INV-FC-014: 前進する ACK (SEG.ACK > SND.UNA) で sndUna 前進 + 窓更新。
\* 採用は最大 ACK のみ (a >= maxAckSeen)。送ったデータは再送保証で必ず ACK される
\* ので、未確認データがある間は AdvanceAck がいつか発火する (SF を張る = 活性の土台)。
AdvanceAck ==
    /\ HasUnacked                    \* 未確認データあり (ACK の余地)
    /\ \E a \in (sndUna + 1)..sndNxt :   \* SND.UNA < SEG.ACK <= SND.NXT
        /\ a >= maxAckSeen           \* 最大 ACK のみ採用
        /\ \E w \in 0..(MSS * 2) :   \* 相手が広告する窓値
            /\ sndUna' = a
            /\ maxAckSeen' = a
            /\ sndWnd' = w
            /\ persistArmed' = IF w = 0 THEN persistArmed ELSE FALSE
    /\ lastAct' = "RecvAck"
    /\ UNCHANGED << sndNxt, appData, persistStage, overrideArmed,
                    rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* 古い/重複 ACK の到着: sndUna は動かさず窓更新も採用しない (INV-FC-014: 古い
\* 小窓で上書きしない)。reorder 耐性の検査用。純粋な環境ノイズなので公平性なし。
StaleAck ==
    /\ \E w \in 0..(MSS * 2) :       \* 古い ACK が運ぶ窓 (採用しない)
        UNCHANGED << sndUna, maxAckSeen, sndWnd, persistArmed >>
    /\ lastAct' = "RecvAck"
    /\ UNCHANGED << sndNxt, appData, persistStage, overrideArmed,
                    rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* WindowZero: 相手窓が 0 になる (受信バッファ満杯)。
WindowZero ==
    /\ sndWnd > 0
    /\ sndWnd' = 0
    /\ lastAct' = "WindowZero"
    /\ UNCHANGED << sndUna, sndNxt, maxAckSeen, appData, persistArmed,
                    persistStage, overrideArmed, rcvNxt, rcvWnd, rcvUser,
                    delAckCnt, delAckArmed >>

\* S-009/S-010: 窓0 & 送るデータあり → persist timer 起動。
PersistArm ==
    /\ sndWnd = 0
    /\ appData > 0
    /\ ~persistArmed
    /\ persistArmed' = TRUE
    /\ lastAct' = "PersistArm"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData,
                    persistStage, overrideArmed, rcvNxt, rcvWnd, rcvUser,
                    delAckCnt, delAckArmed >>

\* S-010 / S-011 / R-FC-043: persist timer 発火 → 1 octet probe 送信 + バックオフ倍化。
\* 受信側は probe に「現在の窓 (RCV.NXT + 現窓)」を載せた ACK を必ず返す (R-FC-043)。
\* これが persist の核心: 自発 window-update (WindowUpdate) はロストしうるが、probe への
\* 応答は probe があって初めて返る = 窓再開を確実に伝える唯一の信頼経路。
\* 受信 SWS (S-004): 応答窓も閾値未満なら 0 のまま (小窓を広告しない)。
PersistFire ==
    /\ persistArmed
    /\ sndWnd = 0
    /\ persistStage' = IF persistStage < StageMax THEN persistStage + 1 ELSE StageMax
    \* probe 応答で受信側の現在窓を確実に伝える (ロストしない信頼経路)。
    /\ IF rcvWnd >= RcvSwsThresh
       THEN /\ sndWnd' = Min(rcvWnd, MSS * 2)   \* 窓再開を伝達
            /\ persistArmed' = FALSE            \* 窓>0 になれば persist 解除
       ELSE /\ sndWnd' = 0                      \* 受信 SWS: 小窓は広告しない
            /\ persistArmed' = TRUE             \* 窓0継続中は再 arm (停止しない)
    /\ lastAct' = "PersistFire"
    /\ UNCHANGED << sndUna, sndNxt, maxAckSeen, appData,
                    overrideArmed, rcvNxt, rcvWnd, rcvUser,
                    delAckCnt, delAckArmed >>

\* S-011 + 受信SWS: 受信側アプリが読み窓が開いたときの「自発的」 window-update。
\* これはロストしうる (公平性を張らない) ので、これだけに頼ると zero-window
\* デッドロックの種になる。確実な再開は PersistFire の probe 応答が担う。
\* S-004 受信SWS: 開いた量が閾値以上のときだけ広告する。
WindowUpdate ==
    /\ sndWnd = 0
    /\ rcvWnd > 0                  \* 受信側で窓が開いている (AppRead 後)
    /\ rcvWnd >= RcvSwsThresh      \* 受信 SWS: 閾値以上のみ広告
    /\ sndWnd' = Min(rcvWnd, MSS * 2)
    /\ persistStage' = 0           \* 窓再開で persist バックオフ解除
    /\ persistArmed' = FALSE
    /\ lastAct' = "WindowUpdate"
    /\ UNCHANGED << sndUna, sndNxt, maxAckSeen, appData,
                    overrideArmed, rcvNxt, rcvWnd, rcvUser,
                    delAckCnt, delAckArmed >>

\* ========================================================================
\* 受信側アクション
\* ========================================================================

\* RecvData: データ到着 → RCV.NXT 前進、窓消費 (S-002: 右端固定で rcvWnd 減)、
\* rcvUser 増 (アプリ未読)。delayed ACK カウンタ増 (フルセグ受信)。
RecvData ==
    /\ rcvWnd >= 1
    /\ rcvUser < RcvBuff
    /\ rcvNxt < MaxSeq               \* seq 空間上限ガード (有限化)
    /\ LET k == Min(rcvWnd, 1) IN   \* 1 単位ずつ受信 (抽象)
        /\ rcvNxt' = rcvNxt + k
        /\ rcvWnd' = rcvWnd - k     \* S-002: RCV.NXT+RCV.WND を一定に (右端固定)
        /\ rcvUser' = rcvUser + k
    \* S-013: フルセグ受信で delayed ACK カウンタ増 (上限 2)。
    /\ delAckCnt' = Min(delAckCnt + 1, 2)
    /\ delAckArmed' = TRUE
    /\ lastAct' = "RecvData"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData,
                    persistArmed, persistStage, overrideArmed >>

\* S-004 受信SWS + S-001: アプリが読む → rcvUser 減、窓が開く (右端を右へ)。
\* 受信 SWS: rcvWnd は閾値以上の増加のみ広告 (ここでは内部 rcvWnd を開け、
\* 広告は WindowUpdate が閾値判定。rcvWnd 自体は右端単調増で更新)。
AppRead ==
    /\ rcvUser > 0
    /\ rcvUser' = rcvUser - 1
    /\ rcvWnd' = Min(rcvWnd + 1, RcvBuff)   \* 窓が右へ開く (右端単調増)
    /\ lastAct' = "AppRead"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData,
                    persistArmed, persistStage, overrideArmed, rcvNxt,
                    delAckCnt, delAckArmed >>

\* S-012/S-013: delayed ACK timer 発火 → ACK 送信、カウンタリセット。
\* これが「受信側が ACK を遅延 → 発火で必ず ACK」= 活性の脱出路。
DelAckFire ==
    /\ delAckArmed
    /\ delAckArmed' = FALSE
    /\ delAckCnt' = 0
    /\ lastAct' = "DelAckFire"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData,
                    persistArmed, persistStage, overrideArmed, rcvNxt,
                    rcvWnd, rcvUser >>

\* S-013: 2 個目のフルセグ受信で即 ACK (上限到達で遅延せず ACK)。
DelAckPiggyback ==
    /\ delAckCnt >= 2
    /\ delAckArmed' = FALSE
    /\ delAckCnt' = 0
    /\ lastAct' = "DelAckFire"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData,
                    persistArmed, persistStage, overrideArmed, rcvNxt,
                    rcvWnd, rcvUser >>

Next ==
    \/ AppWrite
    \/ SendData
    \/ SendOverride
    \/ PersistArmOverride
    \/ AdvanceAck
    \/ StaleAck
    \/ WindowZero
    \/ PersistArm
    \/ PersistFire
    \/ WindowUpdate
    \/ RecvData
    \/ AppRead
    \/ DelAckFire
    \/ DelAckPiggyback

\* ========================================================================
\* 公平性 (活性の本命): タイマは必ず発火、アプリはいつか読む。
\* ========================================================================
Fairness ==
    /\ SF_vars(PersistArm)       \* 窓0で送るデータがあれば persist timer を起動する
    /\ SF_vars(PersistFire)      \* 窓0でも probe を送り続け、応答で窓再開を確実に伝える
    \* WindowUpdate (自発更新) は公平性を張らない: ロストしうるので zero-window
    \* 回復を保証するのは persist probe (PersistFire) のみ。これにより persist の
    \* 必要性が検証される (persist を消すと ZeroWindowProgress が破れる)。
    /\ SF_vars(PersistArmOverride) \* Nagle/SWS で止まったら override timer を必ず起動
    /\ SF_vars(SendOverride)     \* override で溜めたデータを送る
    /\ SF_vars(SendData)         \* 送れる機会が無限に来るなら送る (窓振動に頑健: SF)
    /\ WF_vars(DelAckFire)       \* delayed ACK は必ず発火 (<0.5s)
    /\ WF_vars(AppRead)          \* アプリはいつか読む (窓を開ける)
    /\ SF_vars(AdvanceAck)       \* 送ったデータは再送保証でいつか ACK が前進する

Spec == Init /\ [][Next]_vars /\ Fairness

\* override timer の必要性を立証する最悪ケース: ACK が前進しない (AdvanceAck の
\* 公平性を外す)。このとき in-flight データの ACK は来ないが、相手窓が残っていれば
\* (NagleStuck は Usable>=1)、override timer (SendOverride) が溜めた sub-MSS を
\* 送り出す唯一の脱出路になる。この Spec 下で NagleDelAckLive が成立し、override を
\* 消す (L2 変異) と破れる ことで override の役割が検証される。
FairnessAckStall ==
    /\ SF_vars(PersistArm)
    /\ SF_vars(PersistFire)
    /\ SF_vars(PersistArmOverride)
    /\ SF_vars(SendOverride)
    /\ SF_vars(SendData)
    /\ WF_vars(DelAckFire)
    /\ WF_vars(AppRead)
    \* AdvanceAck の公平性なし: ACK は前進しないかもしれない (最悪ケース)。

SpecAckStall == Init /\ [][Next]_vars /\ FairnessAckStall

\* ========================================================================
\* 安全性不変条件 (INV-FC-xxx)
\* ========================================================================

\* INV-FC-001 (S-001): 右窓端 (RCV.NXT+RCV.WND) は単調非減少。
\* 各アクションで RightEdge' >= RightEdge を要求 (action property)。
RightEdgeMonotone ==
    [][ RightEdge' >= RightEdge ]_vars

\* INV-FC-014 (S-003): 窓更新採用は最大 ACK のみ。maxAckSeen は単調非減少。
MaxAckMonotone ==
    [][ maxAckSeen' >= maxAckSeen ]_vars

\* INV-FC-014 補強: sndUna は後退しない (古い ACK で巻き戻らない)。
SndUnaMonotone ==
    [][ sndUna' >= sndUna ]_vars

\* INV-FC-005 (S-013): 未 ACK フルセグメントは高々 2。
DelAckBound == delAckCnt <= 2

\* INV-FC-006 (S-004): 受信 SWS — 自分の受信窓を広告で開くときは閾値以上のみ。
\* 自分が rcvWnd を相手へ広告する経路 (WindowUpdate / PersistFire の probe 応答) で
\* 窓を 0 から開くなら、開いた窓は閾値以上。AdvanceAck の「相手が広告した窓」は
\* 相手側の責務なので対象外 (混同しない)。
RcvSwsAvoid ==
    [][ ( lastAct' \in { "WindowUpdate", "PersistFire" }
          /\ sndWnd = 0 /\ sndWnd' > 0 )
        => sndWnd' >= RcvSwsThresh ]_vars

\* R-FC-007 (S-002): データ受信で RCV.NXT が前進するとき、右窓端は一定 (窓を消費)。
\* RecvData 遷移では RightEdge' = RightEdge を要求 (右端を右へずらさず窓を縮める)。
RecvKeepsRightEdge ==
    [][ (lastAct' = "RecvData") => RightEdge' = RightEdge ]_vars

\* INV-FC-008 (S-007): Nagle — 未確認データ中はフル未満を送らない。
\* SendFull は appData >= MSS のみ。SendOverride のみが sub-MSS を送れるが
\* それは override timer 経由 (許可された脱出)。即時の sub-MSS 送信は無い。
\* action property: 未確認中に sndNxt が MSS 未満だけ進む通常送信が無い。
NagleAvoid ==
    [][ ( HasUnacked /\ lastAct' = "SendFull" )
        => (sndNxt' - sndNxt) >= MSS ]_vars

\* 状態の健全性 (バッファ境界)。
BufferSane ==
    /\ rcvUser <= RcvBuff
    /\ rcvWnd <= RcvBuff

Inv ==
    /\ TypeOK
    /\ DelAckBound
    /\ BufferSane

\* 全 action property をまとめる (cfg の PROPERTY で個別指定)。

\* ========================================================================
\* Liveness (LIVE-FC: デッドロック回避)
\* ========================================================================

\* 送信が止まる「正当な (フロー制御と無関係な) 理由」: seq 空間の有限化上限に
\* 達した (実装では seq は wrap するが、本モデルは小有限なので飽和する)。
\* この境界はモデルの抽象であって設計の穴ではないので、活性の前提から除外する。
SeqExhausted == sndNxt + 1 > MaxSeq

\* LIVE-FC-1 (S-016): 送るデータがあれば、いつか送信が進むか、seq が枯渇する。
\* zero-window / SWS / Nagle / delayed ACK のどれで止まっても脱出する
\* (persist / override / delAck / AppRead / WindowUpdate の公平性が解く)。
\* 「進んだ」= 未送信データが減って未確認データになった (sndNxt > sndUna)。
\* seq 枯渇 (モデル抽象境界) は終端として許す。
ProgressLive ==
    (appData > 0) ~> (sndNxt > sndUna \/ SeqExhausted)

\* LIVE-FC-3 (S-018): Nagle + delayed ACK デッドロックの不在 (本命)。
\* 「未確認データ中 (Nagle 抑制下) に sub-MSS の送り残しがあり、相手窓もある」
\* 状態が永続しないこと。この状態は: 送信側は Nagle で送れず ACK を待ち、
\* 受信側は delayed ACK で ACK を遅延 → 双方が待つデッドロックの種。
\* override timer (SendOverride) と delAck timer (DelAckFire→RecvAck) が解く。
\* この状態から脱出 = 送り残しが減る (appData 減) か、未確認が解消 (sndUna 前進)。
NagleStuck ==
    /\ appData > 0
    /\ HasUnacked              \* 未確認データ in flight (Nagle 抑制中)
    /\ appData < MSS           \* 送り残しが sub-MSS (フルなら SendData が送る)
    /\ Usable >= 1             \* 相手窓はある (zero-window ではない)
    /\ ~SeqExhausted

\* 脱出 = 送り残しが減る / 未確認が解消 / seq 枯渇 / 相手窓を使い切った (Usable<1,
\* これは ACK=window-update 待ちで override の責務外、正当な待ち)。
NagleDelAckLive ==
    NagleStuck ~> (appData = 0 \/ ~HasUnacked \/ SeqExhausted \/ Usable < 1)

\* LIVE-FC-1 / S-009 (本命): zero-window デッドロックの不在。
\* 相手窓が 0 で送るデータがあるなら、いつか窓が再開する (sndWnd>0)。
\* 自発 window-update はロストしうるので、これを保証するのは persist probe の
\* 応答 (PersistFire, SF) のみ。受信側がいつか読む (AppRead, WF) ので rcvWnd は
\* 閾値以上に開き、次の probe 応答で sndWnd>0 が伝わる。
\* persist を止めると (再 arm しない / 応答しない) この property が破れる。
ZeroWindowProgress ==
    (sndWnd = 0 /\ appData > 0) ~> (sndWnd > 0 \/ SeqExhausted)

\* ACK 活性: 未確認データがあれば、いつか sndUna が前進する (ACK が届く)。
\* 送ったデータは再送保証で必ず ACK される (AdvanceAck, SF)。これが無いと
\* in-flight が永久に残り usable window が回復しない。ACK 前進を消す変異 (L4) を
\* この property が kill する。各 seq 位置で「未確認なら、いつか前進」を要求。
AckAdvances ==
    \A k \in 0..MaxSeq : (sndUna = k /\ sndNxt > k) ~> (sndUna > k)

\* LIVE-FC-1 強形: 窓0でも、いつか窓が再開して送信余地ができる。
\* persist + AppRead + WindowUpdate の公平性で、窓0は永続しない。
ZeroWindowRecover ==
    (sndWnd = 0) ~> (sndWnd > 0)

\* LIVE-FC-3 補: delayed ACK は永久に未送信のままにならない (いつか ACK)。
DelAckLive ==
    (delAckArmed) ~> (~delAckArmed)

====

---- MODULE fc_mut ----
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
    /\ LET k == Min(Min(appData, Usable), MSS) IN
        /\ k >= 1
        /\ sndNxt + k <= MaxSeq
        /\ sndNxt' = sndNxt + k
        /\ appData' = appData - k
    /\ overrideArmed' = FALSE
    /\ persistArmed' = FALSE
    /\ lastAct' = "SendOverride"
    /\ UNCHANGED << sndUna, sndWnd, maxAckSeen, persistStage,
                    rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* S-007 / Nagle + S-005 送信SWS: sub-MSS で未確認データ中、かつフル閾値未満
\* → 送らず override timer を起動 (溜める)。これが「待ち」状態を作る。
PersistArmOverride ==
    /\ ~overrideArmed
    /\ appData > 0
    /\ Usable >= 1
    \* Nagle: 未確認中で sub-MSS は送れない。または送信SWS: フル未満。
    /\ ( (HasUnacked /\ appData < MSS) \/ (Usable < MSS /\ appData < MSS) )
    /\ ~(appData >= MSS /\ Usable >= MSS)   \* フルで送れるなら override 不要
    /\ overrideArmed' = TRUE
    /\ lastAct' = "DelAckArm"   \* arm 系: override 起動 (識別子流用回避のため別名でも可)
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData, persistArmed,
                    persistStage, rcvNxt, rcvWnd, rcvUser, delAckCnt, delAckArmed >>

\* S-003 / INV-FC-014: ACK 到着で sndUna 前進 + 窓更新 (最大 ACK のみ採用)。
\* 環境が任意の ACK 値 a を渡すが、採用は maxAckSeen 以上 & 範囲内のみ。
RecvAck ==
    /\ \E a \in sndUna..sndNxt :     \* SND.UNA <= SEG.ACK <= SND.NXT
        /\ \E w \in 0..(MSS * 2) :   \* 相手が広告する窓値
            \* 最大 ACK のみ窓更新採用。古い ACK は無視 (sndUna/窓を動かさない)。
            IF a >= maxAckSeen
            THEN /\ sndUna' = a
                 /\ maxAckSeen' = a
                 /\ sndWnd' = w
                 \* 窓が開いたら persist/override 解除可能性 (発火側で判断)
                 /\ persistArmed' = IF w = 0 THEN persistArmed ELSE FALSE
            ELSE /\ UNCHANGED << sndUna, maxAckSeen, sndWnd, persistArmed >>
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

\* S-010: persist timer 発火 → 1 octet probe 送信 + バックオフ倍化。
\* probe は窓0でも送る (sndNxt は probe で +0 抽象: probe は再送扱いで前進しない)。
\* バックオフ段階を上げ、再 arm (窓0が続く限り発火し続ける = INV-FC-002)。
PersistFire ==
    /\ persistArmed
    /\ sndWnd = 0
    /\ persistStage' = IF persistStage < StageMax THEN persistStage + 1 ELSE StageMax
    /\ persistArmed' = TRUE        \* 窓0継続中は再 arm (停止しない)
    /\ lastAct' = "PersistFire"
    /\ UNCHANGED << sndUna, sndNxt, sndWnd, maxAckSeen, appData,
                    overrideArmed, rcvNxt, rcvWnd, rcvUser,
                    delAckCnt, delAckArmed >>

\* S-011 + 受信SWS: 受信側アプリが読み窓が開く → WindowUpdate で相手窓再開。
\* これが persist probe への ACK 応答 (窓再開) に相当。
\* S-004 受信SWS: 開いた量が閾値未満なら広告しない (右端固定 = 窓0据え置き)。
WindowUpdate ==
    /\ sndWnd = 0
    /\ rcvWnd > 0                  \* 受信側で窓が開いている (AppRead 後)
    \* 受信 SWS: 開いた窓が閾値以上のときだけ広告する。
    /\ rcvWnd >= RcvSwsThresh
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
    \/ RecvAck
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
    /\ SF_vars(PersistFire)      \* 窓0でも probe を送り続ける
    /\ SF_vars(WindowUpdate)     \* 窓が開いたら相手に伝える
    /\ TRUE \* MUT: override fairness removed
    /\ SF_vars(SendData)         \* 送れる機会が無限に来るなら送る (窓振動に頑健: SF)
    /\ TRUE \* MUT: delAck fairness removed
    /\ WF_vars(AppRead)          \* アプリはいつか読む (窓を開ける)
    /\ WF_vars(RecvAck)          \* ACK はいつか届く

Spec == Init /\ [][Next]_vars /\ Fairness

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

\* INV-FC-006 (S-004): 受信 SWS — 閾値未満の窓は広告しない。
\* WindowUpdate は rcvWnd >= RcvSwsThresh のときのみ sndWnd を開く。
\* 安全性として: sndWnd が 0 から正へ動くのは閾値以上のときだけ
\* (WindowUpdate 経由)。action property で表現。
RcvSwsAvoid ==
    [][ (lastAct' = "WindowUpdate" /\ sndWnd = 0)
        => sndWnd' >= RcvSwsThresh ]_vars

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

\* LIVE-FC-1 強形: 窓0でも、いつか窓が再開して送信余地ができる。
\* persist + AppRead + WindowUpdate の公平性で、窓0は永続しない。
ZeroWindowRecover ==
    (sndWnd = 0) ~> (sndWnd > 0)

\* LIVE-FC-3: delayed ACK は永久に未送信のままにならない (いつか ACK)。
DelAckLive ==
    (delAckArmed) ~> (~delAckArmed)

====

---- MODULE Mux ----
\* 接続多重化: 4-tuple demux + 接続テーブルの並行管理。
\* 0段台帳: Mux.extract.md (S-001..S-030)。手編集禁止、源泉 EARS+モデルから再生成。
\*
\* === モデル化の核 ===
\* 接続テーブルを「関数 tuple->TCB」にすると DOMAIN が集合なので各 tuple に高々1TCB が
\* 構造的に保証されてしまい、INV-MUX-001(一意性)を検査できない(test-and-set を壊しても
\* 違反が起きない)。そこで TCB の "集合" でモデル化する。各 TCB は自分の 4-tuple を持つ
\* レコードで、同一 tuple の TCB が複数共存しうる。これにより test-and-set を外すと
\* 一意性違反が実際に発生し、TLC が kill できる。
\*
\* 並行性: demux(受信)と OPEN/CLOSE(アプリ)は Next の別 disjunct。TLC が全 interleaving
\* を網羅探索する。test-and-set = insert アクションのガードで「非TW占有」を弾くこと。
\* ガードを外す = 並行 insert が競合して同一 tuple に2 TCB を作る、を表現する。

EXTENDS Naturals, FiniteSets

CONSTANTS
    LPorts,     \* local port の有限集合, 例 {"p1","p2"}
    Remotes,    \* remote endpoint の有限集合, 例 {"r1","r2"}
    MaxTcb      \* TCB 総数の上限(状態空間有限化)

Tuples == LPorts \X Remotes
TcbStates == { "SYN_SENT", "SYN_RCVD", "ESTAB", "TIME_WAIT" }
Origins == { "passive", "active" }

\* TCB レコード: 自分の 4-tuple(lport,remote)、状態、由来
TCB(lp, r, s, o) == [ lport |-> lp, remote |-> r, st |-> s, origin |-> o ]
AllTCBs == [ lport: LPorts, remote: Remotes, st: TcbStates, origin: Origins ]

VARIABLES
    tcbs,       \* SUBSET AllTCBs   ... 接続テーブル(TCB の集合, 同一tuple複数ありうる)
    listeners,  \* SUBSET LPorts    ... LISTEN 中の local port(remote ワイルド)
    rstOut,     \* 0..1             ... 直近 demux が生成した RST 本数(INV-MUX-004)
    lastEvent,  \* STRING           ... 直近イベント種別(Gherkin トレース用)
    seg         \* レコード         ... 直近 demux の入力/判定/出力(demux 照合の正当性検査)

vars == << tcbs, listeners, rstOut, lastEvent, seg >>

\* demux の足跡レコード。
\*   port: 処理した local port / inRst: 入力に RST が含まれていたか / outRst: RST を返したか
\*   matched: 完全一致 TCB に dispatch したか / listenHit: LISTEN にヒットしたか
NoSeg == [ port |-> "none", inRst |-> FALSE, outRst |-> FALSE, matched |-> FALSE, listenHit |-> FALSE ]

\* 指定 tuple を占有する TCB 集合
At(lp, r) == { c \in tcbs : c.lport = lp /\ c.remote = r }
\* 非 TIME_WAIT で占有しているか(test-and-set の核心。TIME_WAIT は新 incarnation を許す)
LiveOccupied(lp, r) == \E c \in At(lp, r) : c.st # "TIME_WAIT"
\* 何らかの TCB が占有しているか
Occupied(lp, r) == At(lp, r) # {}

----------------------------------------------------------------------------
TypeOK ==
    /\ tcbs \subseteq AllTCBs
    /\ listeners \subseteq LPorts
    /\ rstOut \in 0..1
    /\ lastEvent \in STRING
    /\ seg \in [ port: LPorts \cup {"none"}, inRst: BOOLEAN, outRst: BOOLEAN,
                 matched: BOOLEAN, listenHit: BOOLEAN ]

Init ==
    /\ tcbs = {}            \* 空 = 全 CLOSED (S-003: TCB 不在 = CLOSED)
    /\ listeners = {}
    /\ rstOut = 0
    /\ lastEvent = "init"
    /\ seg = NoSeg

----------------------------------------------------------------------------
\* === OPEN(アプリ → テーブル書込み) ===

\* S-004 passive OPEN: LISTEN port 生成、既存 TCB に影響しない
PassiveOpen ==
    /\ \E lp \in LPorts :
        /\ lp \notin listeners
        /\ listeners' = listeners \cup {lp}
    /\ UNCHANGED << tcbs, rstOut >>
    /\ lastEvent' = "passive_open"
    /\ seg' = NoSeg

\* S-005/S-006/S-022 active OPEN: test-and-set。非TW占有なら作らない(already-exists)。
\* TIME_WAIT が同 tuple にいれば new incarnation として置換する(全単射維持: 1 tuple 1 TCB)。
ActiveOpen ==
    /\ Cardinality(tcbs) < MaxTcb
    /\ \E lp \in LPorts, r \in Remotes :
        /\ ~LiveOccupied(lp, r)                    \* ★ test-and-set ガード(核心)
        \* 占有(TIME_WAIT)があれば除去してから挿入 = 置換。無ければ純挿入。
        /\ tcbs' = (tcbs \ At(lp, r)) \cup { TCB(lp, r, "SYN_SENT", "active") }
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "active_open"
    /\ seg' = NoSeg

----------------------------------------------------------------------------
\* === demux(受信 → テーブル read + 派生) ===
\* demux アクションは seg' に「処理した port / 入力RST有無 / RST応答有無 / 一致種別」を記録し、
\* 照合の正当性を Inv で縛る(完全一致→LISTEN→RST の順序と、各分岐の出力規則)。

\* S-007/S-016 active SYN_SENT が SYN,ACK を受け ESTAB(完全一致 dispatch)
EstablishActive ==
    /\ \E c \in tcbs :
        /\ c.st = "SYN_SENT"
        /\ tcbs' = (tcbs \ {c}) \cup { TCB(c.lport, c.remote, "ESTAB", c.origin) }
        /\ seg' = [ port |-> c.lport, inRst |-> FALSE, outRst |-> FALSE,
                    matched |-> TRUE, listenHit |-> FALSE ]
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "establish_active"

\* S-008/S-009/S-023 SYN→LISTEN 派生: 新 TCB を SYN_RCVD(passive)で作る。
\* 完全一致が無い(~LiveOccupied)前提で LISTEN にヒット。test-and-set で非TW占有には派生しない。
\* LISTEN は listeners に残る(非破壊=S-009)。
SynToListener ==
    /\ Cardinality(tcbs) < MaxTcb
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)                    \* ★ test-and-set ガード(核心)
        \* TIME_WAIT が居れば new incarnation として置換、無ければ純挿入(全単射維持)
        /\ tcbs' = (tcbs \ At(lp, r)) \cup { TCB(lp, r, "SYN_RCVD", "passive") }
        /\ seg' = [ port |-> lp, inRst |-> FALSE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ UNCHANGED << listeners, rstOut >>           \* listeners 不変 = LISTEN 残存(S-009)
    /\ lastEvent' = "syn_to_listener"

\* S-010 完全一致なし + LISTEN に ACK(非SYN)→ RST を返す。LISTEN にヒットしたが派生せず RST。
SegArriveListenAck ==
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)
        /\ seg' = [ port |-> lp, inRst |-> FALSE, outRst |-> TRUE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ rstOut' = 1
    /\ UNCHANGED << tcbs, listeners >>
    /\ lastEvent' = "listen_ack_rst"

\* S-011 完全一致なし + LISTEN に RST → 無視(drop, 無応答)
SegArriveListenRst ==
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)
        /\ seg' = [ port |-> lp, inRst |-> TRUE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ rstOut' = 0
    /\ UNCHANGED << tcbs, listeners >>
    /\ lastEvent' = "listen_rst_drop"

\* S-015 派生 TCB(SYN_RCVD)が ESTAB へ前進(完全一致 dispatch)
Establish ==
    /\ \E c \in tcbs :
        /\ c.st = "SYN_RCVD"
        /\ tcbs' = (tcbs \ {c}) \cup { TCB(c.lport, c.remote, "ESTAB", c.origin) }
        /\ seg' = [ port |-> c.lport, inRst |-> FALSE, outRst |-> FALSE,
                    matched |-> TRUE, listenHit |-> FALSE ]
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "establish"

\* S-020 SYN_RCVD(passive 由来)に RST → LISTEN 復帰(TCB 削除, listeners 維持)。完全一致 dispatch。
RstSynRcvdPassive ==
    /\ \E c \in tcbs :
        /\ c.st = "SYN_RCVD"
        /\ c.origin = "passive"
        /\ tcbs' = tcbs \ {c}
        /\ seg' = [ port |-> c.lport, inRst |-> TRUE, outRst |-> FALSE,
                    matched |-> TRUE, listenHit |-> FALSE ]
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "rst_synrcvd_passive"

\* S-012/S-028 完全一致も LISTEN も無し(CLOSED)+ 非RST → RST を 1 つ生成
SegArriveNoMatch ==
    /\ \E lp \in LPorts, r \in Remotes :
        /\ ~Occupied(lp, r)
        /\ lp \notin listeners                     \* LISTEN も無い
        /\ seg' = [ port |-> lp, inRst |-> FALSE, outRst |-> TRUE,
                    matched |-> FALSE, listenHit |-> FALSE ]
    /\ rstOut' = 1                                  \* RST ちょうど 1 つ
    /\ UNCHANGED << tcbs, listeners >>
    /\ lastEvent' = "nomatch_rst"

\* S-013 完全一致も LISTEN も無し + RST 含み → 破棄(無応答 = rstOut 0)
SegArriveNoMatchRst ==
    /\ \E lp \in LPorts, r \in Remotes :
        /\ ~Occupied(lp, r)
        /\ lp \notin listeners
        /\ seg' = [ port |-> lp, inRst |-> TRUE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> FALSE ]
    /\ rstOut' = 0                                  \* RST には RST を返さない
    /\ UNCHANGED << tcbs, listeners >>
    /\ lastEvent' = "nomatch_drop"

\* S-014 bad src の SYN は破棄(状態不変, 無応答)
SegArriveBadSrc ==
    /\ UNCHANGED << tcbs, listeners, rstOut >>
    /\ seg' = [ port |-> "none", inRst |-> FALSE, outRst |-> FALSE,
               matched |-> FALSE, listenHit |-> FALSE ]
    /\ lastEvent' = "badsrc_drop"

----------------------------------------------------------------------------
\* === teardown / incarnation ===

\* S-017 ESTAB を CLOSE → TIME_WAIT(4-tuple 予約)
Close ==
    /\ \E c \in tcbs :
        /\ c.st = "ESTAB"
        /\ tcbs' = (tcbs \ {c}) \cup { TCB(c.lport, c.remote, "TIME_WAIT", c.origin) }
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "close"
    /\ seg' = NoSeg

\* S-018 TIME_WAIT 満了 → TCB 削除(CLOSED)
TimeWaitTimeout ==
    /\ \E c \in tcbs :
        /\ c.st = "TIME_WAIT"
        /\ tcbs' = tcbs \ {c}
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "tw_timeout"
    /\ seg' = NoSeg

\* S-019/S-024 TIME_WAIT 中 tuple へ新 incarnation。TIME_WAIT TCB を置換し SYN_RCVD(passive)へ。
\* test-and-set は LiveOccupied(非TW)のみ弾くので TIME_WAIT は reopen 可。
ReopenFromTimeWait ==
    /\ \E c \in tcbs :
        /\ c.st = "TIME_WAIT"
        /\ c.lport \in listeners               \* 新 SYN は demux 経由 → 派生元 LISTEN が要る
        /\ tcbs' = (tcbs \ {c}) \cup { TCB(c.lport, c.remote, "SYN_RCVD", "passive") }
        /\ seg' = [ port |-> c.lport, inRst |-> FALSE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ UNCHANGED << listeners, rstOut >>
    /\ lastEvent' = "reopen_tw"

----------------------------------------------------------------------------
Next ==
    \/ PassiveOpen
    \/ ActiveOpen
    \/ EstablishActive
    \/ SynToListener
    \/ SegArriveListenAck
    \/ SegArriveListenRst
    \/ Establish
    \/ RstSynRcvdPassive
    \/ SegArriveNoMatch
    \/ SegArriveNoMatchRst
    \/ SegArriveBadSrc
    \/ Close
    \/ TimeWaitTimeout
    \/ ReopenFromTimeWait

Spec == Init /\ [][Next]_vars

----------------------------------------------------------------------------
\* === 安全性 不変条件 ===

\* S-025 INV-MUX-001(核心): 各 4-tuple に非 TIME_WAIT TCB は高々 1 つ。
\* TCB 集合表現なので同一 tuple に複数 TCB が共存しうる。test-and-set が壊れると
\* 同一 tuple に非TW TCB が2つでき、ここで違反になる。
InvUnique ==
    \A c1, c2 \in tcbs :
        ( c1.lport = c2.lport /\ c1.remote = c2.remote
          /\ c1.st # "TIME_WAIT" /\ c2.st # "TIME_WAIT" )
            => c1 = c2

\* S-026 INV-MUX-002: dispatch される TCB は 4-tuple 完全一致のもの。
\* 全 TCB が正規の Tuples 上にあり、lport/remote が有効値であること。
InvDispatchExact ==
    \A c \in tcbs : << c.lport, c.remote >> \in Tuples

\* S-027 INV-MUX-003: LISTEN は派生しても LISTEN のまま。
\* passive 由来 SYN_RCVD が存在 ⇒ その派生元 LISTEN(lport)が listeners に残っている。
InvListenStable ==
    \A c \in tcbs :
        ( c.st = "SYN_RCVD" /\ c.origin = "passive" ) => c.lport \in listeners

\* S-028 INV-MUX-004: 一致無し非RST には RST 1 つ。rstOut は 0/1 を超えない。
InvRstExactlyOne == rstOut \in 0..1

\* S-028/INV-MUX-004 強化: demux 足跡で照合の正当性を縛る。
\*   (a) RST を出した(outRst)なら、完全一致でない かつ LISTEN にもヒットしていない
\*       (= CLOSED) ときのみ。LISTEN がある tuple に RST を出してはならない(M5 を捕える)。
\*       例外: LISTEN への ACK(listenHit /\ outRst)は RST 正当(S-010)なので別扱い。
\*   (b) 入力に RST が含まれる(inRst)なら RST を返してはならない(M7 を捕える)。
InvDemuxRstOnlyUnmatched ==
    (seg.outRst /\ ~seg.listenHit) => (~seg.matched)
InvDemuxNoRstToRst ==
    seg.inRst => (~seg.outRst)
\* demux 照合順序 exact→LISTEN→RST の強制。
\*   (c) 各 demux 分岐は排他: 完全一致(matched)と LISTEN ヒット(listenHit)は同時に立たない
\*       (M11 を捕える: 照合の段が混ざると routing が壊れる)。
InvDemuxBranchExclusive ==
    ~(seg.matched /\ seg.listenHit)
\*   (d) 「LISTEN を見ずに RST を出した」(listenHit=FALSE /\ outRst /\ ~matched)なら、
\*       その port には実際に LISTEN が無いこと = exact→LISTEN→RST の順序遵守(M5 を捕える)。
\*       LISTEN があるのに飛ばして RST を出す照合をテーブル実態と突き合わせて禁止する。
InvDemuxOrderRespected ==
    (seg.outRst /\ ~seg.listenHit /\ ~seg.matched /\ seg.port # "none")
        => seg.port \notin listeners

\* S-029 INV-MUX-008: TCB ⇔ 接続 全単射。各 tuple を占有する TCB は高々1つ
\* (TIME_WAIT を含めても、incarnation は置換で1つに保つ)。TCB 無し = CLOSED。
InvBijection ==
    \A c1, c2 \in tcbs :
        ( c1.lport = c2.lport /\ c1.remote = c2.remote ) => c1 = c2

\* S-030 活性をデッドロックフリー安全性として(詳細は下の「活性」節コメント参照)。
\* 半端 TCB(SYN_SENT/SYN_RCVD/TIME_WAIT)は常に前進/解消アクションが enabled。
PendingCanProgress ==
    \A c \in tcbs :
        /\ ( c.st = "SYN_SENT"  => ENABLED EstablishActive )
        /\ ( c.st = "SYN_RCVD"  => (ENABLED Establish \/ ENABLED RstSynRcvdPassive) )
        /\ ( c.st = "TIME_WAIT" => ENABLED TimeWaitTimeout )

\* まとめ INV(TLC で一括検査)
Inv ==
    /\ InvUnique
    /\ InvDispatchExact
    /\ InvListenStable
    /\ InvRstExactlyOne
    /\ InvDemuxRstOnlyUnmatched
    /\ InvDemuxNoRstToRst
    /\ InvDemuxBranchExclusive
    /\ InvDemuxOrderRespected
    /\ InvBijection
    /\ PendingCanProgress

----------------------------------------------------------------------------
\* === 活性(公平性) ===
\* S-030 LIVE-MUX: OPEN した接続はいつか ESTAB か消滅(CLOSED)へ決着する。
\* test-and-set 競合でデッドロック(どの前進手段も失う停止)に陥らないこと。
\*
\* 注意: [] <> (全TCBが決着) は「環境が無限に新規 OPEN を注入し続ける」状況では決して
\* 成立しない(常に新しい SYN_SENT を作れる)ので活性の対象外。ここで保証したいのは
\* 「半端な TCB(SYN_SENT/SYN_RCVD)が存在するなら、それを前進/解消する手が必ず存在する」
\* = test-and-set 競合で前進手段を全て失う(進めないのに居続ける)ことが無い性質。
\* これを安全性(全到達状態で常に成り立つ)として書き、TLC が網羅検査する。
\* 定義は上の INV 群と一緒に置いた(PendingCanProgress)。

\* 公平性付き spec(temporal 検査用に保持)。
Fairness ==
    /\ WF_vars(EstablishActive)
    /\ WF_vars(Establish)
    /\ WF_vars(TimeWaitTimeout)
    /\ WF_vars(RstSynRcvdPassive)

FairSpec == Spec /\ Fairness

====

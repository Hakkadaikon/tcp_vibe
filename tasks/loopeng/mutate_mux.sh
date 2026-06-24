#!/usr/bin/env bash
# Mutation oracle for Mux.tla. 1 箇所ずつ機械変異し TLC が反例で kill するか確認。
# perl -0777 (slurp) で multi-line literal 置換。変異が当たったかを diff で必ず検証。
set -u
DIR=/home/hakkadaikon/repos/hakkadaikon/tcp_vibe/tasks/loopeng
cd "$DIR"
JAR=/nix/store/h340cym0zlka9ymki6909r9lannhw5kc-tlaplus-1.8.0/share/tlaplus/tla2tools.jar
JAVA=/nix/store/c3pl7bqrx3d2rc3dh98z6yaj0mv1p52g-openjdk-21.0.10+7/bin/java
TMP=/tmp/claude-1001
MUT_TIMEOUT="${MUT_TIMEOUT:-90}"
mkdir -p "$TMP"

run_mut () {
  local name="$1" from="$2" to="$3"
  local mf="$TMP/MUTMUX_${name}.tla"
  local cf="$TMP/MUTMUX_${name}.cfg"
  FROM="$from" TO="$to" perl -0777 -pe '
    s/---- MODULE Mux ----/---- MODULE MUTMUX_'"$name"' ----/;
    my $f = quotemeta($ENV{FROM});
    s/$f/$ENV{TO}/;
  ' Mux.tla > "$mf"
  if diff -q <(perl -0777 -pe 's/---- MODULE Mux ----/---- MODULE MUTMUX_'"$name"' ----/' Mux.tla) "$mf" >/dev/null; then
    echo "MUT ${name}: NOT-APPLIED (FROM pattern not found)"; return
  fi
  cp Mux.cfg "$cf"
  local md="$TMP/mdmux_${name}"; mkdir -p "$md"
  local out
  out=$(timeout "$MUT_TIMEOUT" "$JAVA" -Djava.io.tmpdir="$TMP" -XX:+UseParallelGC -cp "$JAR" tlc2.TLC \
        -metadir "$md" -config "$cf" "$mf" 2>&1)
  local rc=$?
  if [ $rc -eq 124 ]; then
    echo "MUT ${name}: TIMEOUT (diverged, killed by MUT_TIMEOUT=${MUT_TIMEOUT}s)"; return
  fi
  if echo "$out" | grep -qE 'is violated|Error: Invariant|Parsing or semantic|Unknown operator'; then
    local why=$(echo "$out" | grep -oE '[A-Za-z_]+ is violated|Parsing or semantic analysis failed|Unknown operator' | head -1)
    echo "MUT ${name}: KILLED (${why})"
  elif echo "$out" | grep -q 'No error has been found'; then
    echo "MUT ${name}: SURVIVOR (No error)"
  else
    echo "MUT ${name}: UNKNOWN"; echo "$out" | tail -4
  fi
}

echo "=== Mux mutation oracle ==="

# --- 核心: test-and-set を外すと 4-tuple 一意性 (InvUnique) が破れる ---

# M1: ActiveOpen の test-and-set ガード ~LiveOccupied を外す → 同一 tuple に SYN_SENT が複数 → InvUnique kill
run_mut M1_activeopen_no_tas \
'        /\ ~LiveOccupied(lp, r)                    \* ★ test-and-set ガード(核心)
        \* 占有(TIME_WAIT)があれば除去してから挿入 = 置換。無ければ純挿入。
        /\ tcbs'"'"' = (tcbs \ At(lp, r)) \cup { TCB(lp, r, "SYN_SENT", "active") }' \
'        /\ tcbs'"'"' = tcbs \cup { TCB(lp, r, "SYN_SENT", "active") }'

# M2: ActiveOpen を「占有を消さず純追加」だがガードは残す → TIME_WAIT と共存 → InvBijection kill
run_mut M2_activeopen_no_replace \
'        /\ ~LiveOccupied(lp, r)                    \* ★ test-and-set ガード(核心)
        \* 占有(TIME_WAIT)があれば除去してから挿入 = 置換。無ければ純挿入。
        /\ tcbs'"'"' = (tcbs \ At(lp, r)) \cup { TCB(lp, r, "SYN_SENT", "active") }' \
'        /\ ~LiveOccupied(lp, r)
        /\ tcbs'"'"' = tcbs \cup { TCB(lp, r, "SYN_SENT", "active") }'

# M3: SynToListener の test-and-set ガードを外す → LISTEN への並行 SYN で同 tuple 複数派生 → InvUnique kill
run_mut M3_syntolistener_no_tas \
'        /\ ~LiveOccupied(lp, r)                    \* ★ test-and-set ガード(核心)
        \* TIME_WAIT が居れば new incarnation として置換、無ければ純挿入(全単射維持)
        /\ tcbs'"'"' = (tcbs \ At(lp, r)) \cup { TCB(lp, r, "SYN_RCVD", "passive") }' \
'        /\ tcbs'"'"' = tcbs \cup { TCB(lp, r, "SYN_RCVD", "passive") }'

# --- INV-MUX-001: LiveOccupied の TIME_WAIT 例外を消すと reopen が壊れる(equivalent の疑いも検証) ---

# M4: LiveOccupied から TIME_WAIT 例外を消す(= 単なる Occupied)→ reopen 不能だが安全側、SURVIVOR 予想(equivalent寄り)
run_mut M4_liveocc_no_tw_exception \
'LiveOccupied(lp, r) == \E c \in At(lp, r) : c.st # "TIME_WAIT"' \
'LiveOccupied(lp, r) == \E c \in At(lp, r) : TRUE'

# --- demux 照合: 一致条件を緩める ---

# M5: SegArriveNoMatch のガード「LISTEN も無い」を外す → LISTEN があるのに RST 生成。
#     足跡 seg に listenHit=FALSE のまま outRst=TRUE を記録するが、LISTEN がある状態で起きうる。
#     InvDemuxRstOnlyUnmatched は (outRst /\ ~listenHit)=>~matched しか縛らないので、
#     「LISTEN がある tuple に対し listenHit=FALSE で RST」を捕えるには別の縛りが要る → 検証。
run_mut M5_nomatch_ignore_listen \
'        /\ ~Occupied(lp, r)
        /\ lp \notin listeners                     \* LISTEN も無い
        /\ seg'"'"' = [ port |-> lp, inRst |-> FALSE, outRst |-> TRUE,' \
'        /\ ~Occupied(lp, r)
        /\ seg'"'"' = [ port |-> lp, inRst |-> FALSE, outRst |-> TRUE,'

# M6: SegArriveNoMatch が RST を 2 本出す(rstOut'=2) → 型 0..1 違反/InvRstExactlyOne kill
run_mut M6_nomatch_two_rst \
'    /\ rstOut'"'"' = 1                                  \* RST ちょうど 1 つ' \
'    /\ rstOut'"'"' = 2'

# M7: SegArriveNoMatchRst が RST を返してしまう(outRst TRUE)→ RST 含みに応答 = INV-MUX-004 違反。
#     新 INV InvDemuxNoRstToRst(inRst=>~outRst)で kill されるはず。
run_mut M7_nomatch_rst_responds \
'        /\ seg'"'"' = [ port |-> lp, inRst |-> TRUE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> FALSE ]
    /\ rstOut'"'"' = 0                                  \* RST には RST を返さない' \
'        /\ seg'"'"' = [ port |-> lp, inRst |-> TRUE, outRst |-> TRUE,
                    matched |-> FALSE, listenHit |-> FALSE ]
    /\ rstOut'"'"' = 1'

# --- LISTEN 非破壊 (INV-MUX-003) ---

# M8: SynToListener が listeners を消す(LISTEN を破壊)→ InvListenStable kill
run_mut M8_syntolistener_drops_listen \
'        /\ seg'"'"' = [ port |-> lp, inRst |-> FALSE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ UNCHANGED << listeners, rstOut >>           \* listeners 不変 = LISTEN 残存(S-009)' \
'        /\ seg'"'"' = [ port |-> lp, inRst |-> FALSE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> TRUE ]
        /\ listeners'"'"' = listeners \ {lp}
    /\ UNCHANGED << rstOut >>'

# --- demux 足跡ベース INV の検証 ---

# M11: SegArriveListenAck が「matched=TRUE」と誤記しつつ RST を出す → InvDemuxRstOnlyUnmatched
#      は ~listenHit のときだけ縛るので listenHit=TRUE なら効かない。代わりに matched/listenHit
#      同時 TRUE が照合の排他性を壊す。それを捕える INV があるか検証(無ければ穴)。
run_mut M11_listenack_marks_matched \
'SegArriveListenAck ==
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)
        /\ seg'"'"' = [ port |-> lp, inRst |-> FALSE, outRst |-> TRUE,
                    matched |-> FALSE, listenHit |-> TRUE ]' \
'SegArriveListenAck ==
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)
        /\ seg'"'"' = [ port |-> lp, inRst |-> FALSE, outRst |-> TRUE,
                    matched |-> TRUE, listenHit |-> TRUE ]'

# M12: SegArriveListenRst が RST を返す(outRst TRUE, inRst TRUE)→ InvDemuxNoRstToRst kill
run_mut M12_listenrst_responds \
'SegArriveListenRst ==
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)
        /\ seg'"'"' = [ port |-> lp, inRst |-> TRUE, outRst |-> FALSE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ rstOut'"'"' = 0' \
'SegArriveListenRst ==
    /\ \E lp \in listeners, r \in Remotes :
        /\ ~LiveOccupied(lp, r)
        /\ seg'"'"' = [ port |-> lp, inRst |-> TRUE, outRst |-> TRUE,
                    matched |-> FALSE, listenHit |-> TRUE ]
    /\ rstOut'"'"' = 1'

# --- RST routing (INV-MUX 由来, S-020) ---

# M9: RstSynRcvdPassive が active 由来も削除する(origin ガード除去)→ active の RST 扱い破壊
#     active 由来 SYN_RCVD は CLOSED 行きが正(別仕様)。現モデルに active RST 遷移は無いので
#     ガード除去は「passive 専用のはずが any を削除」。安全側挙動なら SURVIVOR を記録。
run_mut M9_rst_synrcvd_any_origin \
'        /\ c.st = "SYN_RCVD"
        /\ c.origin = "passive"
        /\ tcbs'"'"' = tcbs \ {c}' \
'        /\ c.st = "SYN_RCVD"
        /\ tcbs'"'"' = tcbs \ {c}'

# --- 活性 (PendingCanProgress) ---

# M10: TimeWaitTimeout のガード st="TIME_WAIT" を ESTAB に取り違え → TIME_WAIT が解消手段を失う
#      → PendingCanProgress kill (TIME_WAIT があるのに ENABLED TimeWaitTimeout が false)
run_mut M10_tw_timeout_wrong_guard \
'TimeWaitTimeout ==
    /\ \E c \in tcbs :
        /\ c.st = "TIME_WAIT"
        /\ tcbs'"'"' = tcbs \ {c}' \
'TimeWaitTimeout ==
    /\ \E c \in tcbs :
        /\ c.st = "ESTAB"
        /\ tcbs'"'"' = tcbs \ {c}'

echo "=== Mux mutation run done ==="

# --- behavior-loss 検出 (safety INV では捕えられない「正当な振る舞いの喪失」) ---
# M4(LiveOccupied の TIME_WAIT 例外除去)は安全性違反を起こさず、active OPEN による
# TIME_WAIT 再利用という正当な振る舞いだけを失う。これは到達状態数の減少として現れる。
# original の到達状態数を基準に、mutant が減れば behavior-loss として KILLED 扱いにする。
echo "=== behavior-loss probe (reachable state count) ==="
base=$("$JAVA" -Djava.io.tmpdir="$TMP" -cp "$JAR" tlc2.TLC \
        -metadir "$TMP/md_base_cnt" -config Mux.cfg Mux.tla 2>&1 \
        | grep -oE '[0-9]+ distinct states found' | grep -oE '^[0-9]+')
echo "original distinct states = $base"
m4=$("$JAVA" -Djava.io.tmpdir="$TMP" -cp "$JAR" tlc2.TLC \
        -metadir "$TMP/md_m4_cnt2" -config "$TMP/MUTMUX_M4_liveocc_no_tw_exception.cfg" \
        "$TMP/MUTMUX_M4_liveocc_no_tw_exception.tla" 2>&1 \
        | grep -oE '[0-9]+ distinct states found' | grep -oE '^[0-9]+')
echo "M4 mutant distinct states = $m4"
if [ -n "$base" ] && [ -n "$m4" ] && [ "$m4" -lt "$base" ]; then
  echo "MUT M4_liveocc_no_tw_exception: KILLED (behavior-loss: $m4 < $base reachable states)"
else
  echo "MUT M4_liveocc_no_tw_exception: SURVIVOR (no behavior loss detected)"
fi

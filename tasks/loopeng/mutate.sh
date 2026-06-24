#!/usr/bin/env bash
# Mutation oracle. TCP.tla を1箇所ずつ機械変異し、TLC が反例で kill するか確認。
# perl -0777 (slurp) で multi-line literal 置換。変異が当たったかを diff で必ず検証。
set -u
DIR=/home/hakkadaikon/repos/hakkadaikon/tcp_vibe/tasks/loopeng
cd "$DIR"
JAR=/nix/store/h340cym0zlka9ymki6909r9lannhw5kc-tlaplus-1.8.0/share/tlaplus/tla2tools.jar
JAVA=/nix/store/c3pl7bqrx3d2rc3dh98z6yaj0mv1p52g-openjdk-21.0.10+7/bin/java
TMP=/tmp/claude-1001
mkdir -p "$TMP"

run_mut () {
  local name="$1" from="$2" to="$3"
  local mf="$TMP/MUT_${name}.tla"
  local cf="$TMP/MUT_${name}.cfg"
  FROM="$from" TO="$to" perl -0777 -pe '
    s/---- MODULE TCP ----/---- MODULE MUT_'"$name"' ----/;
    my $f = quotemeta($ENV{FROM});
    s/$f/$ENV{TO}/;
  ' TCP.tla > "$mf"
  # 変異が当たったか: 行数差ではなく内容差で検証
  if diff -q <(perl -0777 -pe 's/---- MODULE TCP ----/---- MODULE MUT_'"$name"' ----/' TCP.tla) "$mf" >/dev/null; then
    echo "MUT ${name}: NOT-APPLIED (FROM pattern not found)"; return
  fi
  cp TCP.cfg "$cf"
  local md="$TMP/md_${name}"; mkdir -p "$md"
  local out
  out=$("$JAVA" -Djava.io.tmpdir="$TMP" -XX:+UseParallelGC -cp "$JAR" tlc2.TLC \
        -metadir "$md" -config "$cf" "$mf" 2>&1)
  if echo "$out" | grep -qE 'is violated|Error: Invariant|Parsing or semantic'; then
    local why=$(echo "$out" | grep -oE '[A-Za-z_]+ is violated|Parsing or semantic analysis failed' | head -1)
    echo "MUT ${name}: KILLED (${why})"
  elif echo "$out" | grep -q 'No error has been found'; then
    echo "MUT ${name}: SURVIVOR (No error)"
  else
    echo "MUT ${name}: UNKNOWN"; echo "$out" | tail -4
  fi
}

run_mut M1_ack_relax \
'AcceptableAck(a) == (sndUna < a) /\ (a =< sndNxt)' \
'AcceptableAck(a) == (sndUna < a) /\ (a =< sndNxt + 1)'

run_mut M2_rstoow_reset \
'RstOutOfWindow ==
    /\ st \in Synchronized
    /\ \E s \in 0..SeqMax : ~InWindow(s)   \* 窓外 seq が存在しうる
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset'"'"' = FALSE
    /\ didChallenge'"'"' = FALSE' \
'RstOutOfWindow ==
    /\ st \in Synchronized
    /\ \E s \in 0..SeqMax : ~InWindow(s)
    /\ st'"'"' = "CLOSED"
    /\ UNCHANGED << origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset'"'"' = TRUE
    /\ didChallenge'"'"' = FALSE'

run_mut M3_syn_reset \
'SynChallenge ==
    /\ st \in Synchronized
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset'"'"' = FALSE
    /\ didChallenge'"'"' = TRUE' \
'SynChallenge ==
    /\ st \in Synchronized
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset'"'"' = TRUE
    /\ didChallenge'"'"' = TRUE'

run_mut M4_passive_to_closed \
'RcvRstSynRcvdPassive ==  \* S-009  passive 由来 → LISTEN
    /\ st = "SYN_RCVD"
    /\ origin = "passive"
    /\ st'"'"' = "LISTEN"' \
'RcvRstSynRcvdPassive ==  \* mutated
    /\ st = "SYN_RCVD"
    /\ origin = "passive"
    /\ st'"'"' = "CLOSED"'

run_mut M5_active_to_listen \
'RcvRstSynRcvdActive ==   \* S-010  active 由来 → CLOSED
    /\ st = "SYN_RCVD"
    /\ origin = "active"
    /\ st'"'"' = "CLOSED"' \
'RcvRstSynRcvdActive ==   \* mutated
    /\ st = "SYN_RCVD"
    /\ origin = "active"
    /\ st'"'"' = "LISTEN"'

run_mut M6_tw_guard_drop \
'TimeWaitExpire ==      \* S-020  満了してから CLOSED
    /\ st = "TIME_WAIT"
    /\ twTimer = 0' \
'TimeWaitExpire ==      \* mutated
    /\ st = "TIME_WAIT"
    /\ twTimer >= 0'

run_mut M7_bad_edge \
'CloseEstab ==          \* S-011
    /\ st = "ESTAB"
    /\ st'"'"' = "FIN_WAIT_1"' \
'CloseEstab ==          \* mutated
    /\ st = "ESTAB"
    /\ st'"'"' = "LAST_ACK"'

run_mut M8_fw1_una_over \
'RcvAckFW1 ==           \* S-013  自FIN が ack された
    /\ st = "FIN_WAIT_1"
    /\ AcceptableAck(sndNxt)
    /\ st'"'"' = "FIN_WAIT_2"
    /\ sndUna'"'"' = sndNxt' \
'RcvAckFW1 ==           \* mutated
    /\ st = "FIN_WAIT_1"
    /\ st'"'"' = "FIN_WAIT_2"
    /\ sndUna'"'"' = sndNxt + 1'

run_mut M9_rstnxt_noreset \
'RstAtRcvNxt ==
    /\ st \in Synchronized
    /\ st'"'"' = "CLOSED"
    /\ origin'"'"' = "none"
    /\ sndUna'"'"' = 0 /\ sndNxt'"'"' = 0 /\ rcvNxt'"'"' = 0 /\ finAcked'"'"' = FALSE
    /\ twTimer'"'"' = 0
    /\ didReset'"'"' = TRUE' \
'RstAtRcvNxt ==
    /\ st \in Synchronized
    /\ st'"'"' = "CLOSED"
    /\ origin'"'"' = "none"
    /\ sndUna'"'"' = 0 /\ sndNxt'"'"' = 0 /\ rcvNxt'"'"' = 0 /\ finAcked'"'"' = FALSE
    /\ twTimer'"'"' = 0
    /\ didReset'"'"' = FALSE'

echo "=== mutation run done ==="

# --- 追加変異 (oracle の網羅性確認) ---

# M10: RstAtRcvNxt が reset 根拠を "oow" と誤記 → INV-005(InvRstStrict)で直接 kill されるはず
run_mut M10_rstnxt_kind_oow \
'    /\ twTimer'"'"' = 0
    /\ didReset'"'"' = TRUE
    /\ didChallenge'"'"' = FALSE
    /\ rstKind'"'"' = "at_nxt"' \
'    /\ twTimer'"'"' = 0
    /\ didReset'"'"' = TRUE
    /\ didChallenge'"'"' = FALSE
    /\ rstKind'"'"' = "oow"'

# M11: SimOpen で origin を passive に取り違え → 同時オープン後 RST routing 破壊 (INV-014)
run_mut M11_simopen_passive \
'SimOpen ==             \* S-006  同時オープン (bare SYN)
    /\ st = "SYN_SENT"
    /\ st'"'"' = "SYN_RCVD"
    /\ origin'"'"' = "active"       \* active 由来を維持' \
'SimOpen ==             \* mutated
    /\ st = "SYN_SENT"
    /\ st'"'"' = "SYN_RCVD"
    /\ origin'"'"' = "passive"'

# M12: RcvAckData のガード AcceptableAck を外す → una が任意前進 (INV-001)
run_mut M12_ackdata_noguard \
'RcvAckData ==
    /\ st \in { "ESTAB", "FIN_WAIT_1", "CLOSE_WAIT" }
    /\ \E a \in 0..SeqMax :
         /\ AcceptableAck(a)
         /\ sndUna'"'"' = a' \
'RcvAckData ==
    /\ st \in { "ESTAB", "FIN_WAIT_1", "CLOSE_WAIT" }
    /\ \E a \in 0..SeqMax :
         /\ sndUna'"'"' = a'

# M13: RstInWindowNotNxt が reset してしまう (INV-005)
run_mut M13_inwin_reset \
'RstInWindowNotNxt ==
    /\ st \in Synchronized
    /\ \E s \in 0..SeqMax : InWindow(s) /\ s # rcvNxt
    /\ UNCHANGED << st, origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset'"'"' = FALSE
    /\ didChallenge'"'"' = TRUE
    /\ rstKind'"'"' = "inwin"' \
'RstInWindowNotNxt ==
    /\ st \in Synchronized
    /\ \E s \in 0..SeqMax : InWindow(s) /\ s # rcvNxt
    /\ st'"'"' = "CLOSED"
    /\ UNCHANGED << origin, sndUna, sndNxt, rcvNxt, finAcked, twTimer >>
    /\ didReset'"'"' = TRUE
    /\ didChallenge'"'"' = TRUE
    /\ rstKind'"'"' = "inwin"'

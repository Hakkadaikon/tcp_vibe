#!/usr/bin/env bash
# Mutation oracle for fc.tla. 1 箇所ずつ機械変異し TLC が反例で kill するか確認。
# 安全性変異は fc.cfg (高速)、活性変異は fc_live_p1/p2/nagle.cfg (低速) で oracle を回す。
# 変異が当たったかを diff で必ず検証 (NOT-APPLIED を見逃さない)。
set -u
DIR=/home/hakkadaikon/repos/hakkadaikon/tcp_vibe/tasks/loopeng
cd "$DIR"
JAR=/nix/store/h340cym0zlka9ymki6909r9lannhw5kc-tlaplus-1.8.0/share/tlaplus/tla2tools.jar
JAVA=/nix/store/c3pl7bqrx3d2rc3dh98z6yaj0mv1p52g-openjdk-21.0.10+7/bin/java
TMP=/tmp/claude-1001
MUT_TIMEOUT="${MUT_TIMEOUT:-300}"
mkdir -p "$TMP"

# run_mut <name> <cfg> <from> <to>
run_mut () {
  local name="$1" cfg="$2" from="$3" to="$4"
  local mf="$TMP/MUTfc_${name}.tla"
  local cf="$TMP/MUTfc_${name}.cfg"
  FROM="$from" TO="$to" perl -0777 -pe '
    s/---- MODULE fc ----/---- MODULE MUTfc_'"$name"' ----/;
    my $f = quotemeta($ENV{FROM});
    s/$f/$ENV{TO}/;
  ' fc.tla > "$mf"
  if diff -q <(perl -0777 -pe 's/---- MODULE fc ----/---- MODULE MUTfc_'"$name"' ----/' fc.tla) "$mf" >/dev/null; then
    echo "MUT ${name}: NOT-APPLIED (FROM pattern not found)"; return
  fi
  # cfg は SPECIFICATION/PROPERTY をそのまま流用 (module 名は中身不問)。
  cp "$cfg" "$cf"
  local md="$TMP/mdfc_${name}"; mkdir -p "$md"
  local out
  out=$(timeout "$MUT_TIMEOUT" "$JAVA" -Djava.io.tmpdir="$TMP" -XX:+UseParallelGC -cp "$JAR" tlc2.TLC \
        -metadir "$md" -config "$cf" "$mf" 2>&1)
  local rc=$?
  if [ $rc -eq 124 ]; then
    echo "MUT ${name}: TIMEOUT (>${MUT_TIMEOUT}s, diverging — treat as undecided)"; return
  fi
  if echo "$out" | grep -qE 'is violated|was violated|Error: Invariant|Parsing or semantic|Deadlock'; then
    local why=$(echo "$out" | grep -oE 'Temporal property [A-Za-z_]+ was violated|[A-Za-z_]+ is violated|Invariant [A-Za-z_]+ is violated|Deadlock reached|Parsing or semantic analysis failed' | head -1)
    echo "MUT ${name}: KILLED (${why})"
  elif echo "$out" | grep -q 'No error has been found'; then
    echo "MUT ${name}: SURVIVOR (No error)"
  else
    echo "MUT ${name}: UNKNOWN"; echo "$out" | tail -5
  fi
}

echo "=== 安全性 mutation (fc.cfg) ==="

# S1: 受信窓を縮める (RecvData の右端固定 rcvWnd-=k を、消費せず据え置きに)。
#     → 右窓端 = rcvNxt+rcvWnd が rcvNxt 前進分だけ増える = 右へ動くので RightEdgeMonotone は
#       破れない。むしろ「縮めない」を直接破るのは AppRead の窓開き量を負にする変異 (S2)。
#     ここは RecvData が rcvWnd を増やす (右端を左→右でなく過剰に動かす) 異常を入れる。
run_mut S1_recv_grow_wnd fc.cfg \
'        /\ rcvWnd'"'"' = rcvWnd - k     \* S-002: RCV.NXT+RCV.WND を一定に (右端固定)' \
'        /\ rcvWnd'"'"' = rcvWnd         \* MUT: 右端を rcvNxt 前進分だけ右へ (縮めはしない)'

# S2: 窓を縮める許可 — AppRead で rcvWnd を増やす代わりに減らす (右窓端を左へ)。
#     → RightEdgeMonotone (窓を縮めない) で kill されるはず。
run_mut S2_shrink_window fc.cfg \
'    /\ rcvWnd'"'"' = Min(rcvWnd + 1, RcvBuff)   \* 窓が右へ開く (右端単調増)' \
'    /\ rcvWnd'"'"' = IF rcvWnd > 0 THEN rcvWnd - 1 ELSE 0   \* MUT: 窓を縮める'

# S3: AdvanceAck の a>=maxAckSeen を外す。
#     → EQUIVALENT 予想: AdvanceAck は a>sndUna に限定され maxAckSeen<=sndUna が不変なので
#       a>sndUna>=maxAckSeen で常に成立 = 冗長ガード。古い小窓上書きは StaleAck (窓更新
#       しない) が別途防ぐので INV-FC-014 は維持される。survivor なら equivalent と記録。
run_mut S3_stale_ack_accept fc.cfg \
'    /\ \E a \in (sndUna + 1)..sndNxt :   \* SND.UNA < SEG.ACK <= SND.NXT
        /\ a >= maxAckSeen           \* 最大 ACK のみ採用' \
'    /\ \E a \in (sndUna + 1)..sndNxt :   \* SND.UNA < SEG.ACK <= SND.NXT
        /\ TRUE                      \* MUT: 最大 ACK 限定を外す'

# S4: delayed ACK 上限を外す (RecvData の delAckCnt を Min(.,2) でなく無制限に)。
#     → DelAckBound (delAckCnt<=2) / TypeOK (delAckCnt\in0..2) で kill。
run_mut S4_delack_no_cap fc.cfg \
'    /\ delAckCnt'"'"' = Min(delAckCnt + 1, 2)' \
'    /\ delAckCnt'"'"' = delAckCnt + 1'

# S5: 受信 SWS を外す (WindowUpdate の閾値ガード rcvWnd>=RcvSwsThresh を外す)。
#     → 小窓を広告できてしまう。RcvSwsAvoid (action property) で kill されるはず。
run_mut S5_no_recv_sws fc.cfg \
'    /\ rcvWnd >= RcvSwsThresh      \* 受信 SWS: 閾値以上のみ広告
    /\ sndWnd'"'"' = Min(rcvWnd, MSS * 2)' \
'    /\ rcvWnd >= 1                 \* MUT: 受信 SWS 閾値を 1 に緩める (小窓広告)
    /\ sndWnd'"'"' = Min(rcvWnd, MSS * 2)'

# S6: Nagle を外す (SendData の idle 限定を外し、未確認中でも sub-MSS を送る)。
#     → NagleAvoid (未確認中 SendFull は >=MSS) で kill されるはず。
run_mut S6_no_nagle fc.cfg \
'       \/ (sndNxt = sndUna) )' \
'       \/ (appData >= 1) )'

echo "=== 活性 mutation (persist: fc_live_nagle / override / delAck) ==="

# L1a: persist probe の PersistFire 内の再 arm を止める。
#     → EQUIVALENT 予想: PersistArm (SF) が窓0継続中に再び arm するので probe は継続。
#       再 arm の責務を PersistFire 内に置くか外部 PersistArm に置くかの実装自由度。
#       survivor なら equivalent-mutant として記録 (L1b/L1c が persist 本体を kill)。
run_mut L1a_persist_no_rearm fc_live_zw.cfg \
'       ELSE /\ sndWnd'"'"' = 0                      \* 受信 SWS: 小窓は広告しない
            /\ persistArmed'"'"' = TRUE             \* 窓0継続中は再 arm (停止しない)' \
'       ELSE /\ sndWnd'"'"' = 0
            /\ persistArmed'"'"' = FALSE            \* MUT: probe を止める'

# L1b: persist probe の応答 (窓再開伝達) を消す (probe しても窓を伝えない)。
#     → 自発 WindowUpdate はロストしうるので窓0から抜けない。ZeroWindowProgress で kill。
run_mut L1b_probe_no_reply fc_live_zw.cfg \
'       THEN /\ sndWnd'"'"' = Min(rcvWnd, MSS * 2)   \* 窓再開を伝達
            /\ persistArmed'"'"' = FALSE            \* 窓>0 になれば persist 解除' \
'       THEN /\ sndWnd'"'"' = 0                      \* MUT: probe 応答で窓を伝えない
            /\ persistArmed'"'"' = TRUE'

# L1c: PersistArm を起動しない (窓0でも persist timer を arm しない)。
#     → そもそも probe が始まらず窓0で停止。ZeroWindowProgress で kill。
run_mut L1c_no_persist_arm fc_live_zw.cfg \
'PersistArm ==
    /\ sndWnd = 0
    /\ appData > 0
    /\ ~persistArmed' \
'PersistArm ==
    /\ FALSE
    /\ sndWnd = 0
    /\ appData > 0
    /\ ~persistArmed'

# L2: override timer を消す (PersistArmOverride を無効化)。ACK 停滞 (SpecAckStall) 下で
#     のみ override の必要性が顕在化する → NagleDelAckLive で kill (fc_ackstall.cfg)。
#     正規 Spec (AdvanceAck SF あり) では AdvanceAck が代替脱出になり survivor (記録)。
run_mut L2_no_override fc_ackstall.cfg \
'    /\ HasUnacked                            \* 未確認中 (idle は SendData が処理)
    /\ ~(appData >= MSS /\ Usable >= MSS /\ sndNxt + MSS <= MaxSeq) \* フル不可' \
'    /\ FALSE                                 \* MUT: override timer を起動しない
    /\ ~(appData >= MSS /\ Usable >= MSS /\ sndNxt + MSS <= MaxSeq)'

# L2b: SendOverride を無効化 (arm はするが送らない) → 同上 ACK 停滞下で kill。
run_mut L2b_override_no_send fc_ackstall.cfg \
'SendOverride ==
    /\ overrideArmed
    /\ appData > 0' \
'SendOverride ==
    /\ FALSE
    /\ overrideArmed
    /\ appData > 0'

# L3: delayed ACK が永久に発火しない (DelAckFire を無効化)。
#     → DelAckLive で kill されるはず (fc_live_p2)。
run_mut L3_no_delack_fire fc_live_p2.cfg \
'DelAckFire ==
    /\ delAckArmed
    /\ delAckArmed'"'"' = FALSE' \
'DelAckFire ==
    /\ FALSE
    /\ delAckArmed'"'"' = FALSE'

# L4: AdvanceAck を無効化 (ACK が前進しない)。
#     → in-flight が永久に残り usable window が回復しない。AckAdvances で kill (fc_live_ack)。
run_mut L4_ack_no_advance fc_live_ack.cfg \
'    /\ HasUnacked                    \* 未確認データあり (ACK の余地)
    /\ \E a \in (sndUna + 1)..sndNxt :   \* SND.UNA < SEG.ACK <= SND.NXT' \
'    /\ FALSE                         \* MUT: ACK が前進しない
    /\ \E a \in (sndUna + 1)..sndNxt :'

echo "=== fc mutation run done ==="

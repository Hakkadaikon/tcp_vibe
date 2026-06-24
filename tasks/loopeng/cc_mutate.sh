#!/usr/bin/env bash
# Mutation oracle for cc.tla. 1 箇所ずつ機械変異し TLC が反例で kill するか確認。
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
  local mf="$TMP/MUTcc_${name}.tla"
  local cf="$TMP/MUTcc_${name}.cfg"
  FROM="$from" TO="$to" perl -0777 -pe '
    s/---- MODULE cc ----/---- MODULE MUTcc_'"$name"' ----/;
    my $f = quotemeta($ENV{FROM});
    s/$f/$ENV{TO}/;
  ' cc.tla > "$mf"
  if diff -q <(perl -0777 -pe 's/---- MODULE cc ----/---- MODULE MUTcc_'"$name"' ----/' cc.tla) "$mf" >/dev/null; then
    echo "MUT ${name}: NOT-APPLIED (FROM pattern not found)"; return
  fi
  cp cc.cfg "$cf"
  local md="$TMP/mdcc_${name}"; mkdir -p "$md"
  local out
  out=$("$JAVA" -Djava.io.tmpdir="$TMP" -XX:+UseParallelGC -cp "$JAR" tlc2.TLC \
        -metadir "$md" -config "$cf" "$mf" 2>&1)
  if echo "$out" | grep -qE 'is violated|Error: Invariant|Parsing or semantic'; then
    local why=$(echo "$out" | grep -oE '[A-Za-z_]+ is violated|Invariant [A-Za-z_]+ is violated|Parsing or semantic analysis failed' | head -1)
    echo "MUT ${name}: KILLED (${why})"
  elif echo "$out" | grep -q 'No error has been found'; then
    echo "MUT ${name}: SURVIVOR (No error)"
  else
    echo "MUT ${name}: UNKNOWN"; echo "$out" | tail -5
  fi
}

# M1: cwnd 下限を割る (RtoExpire の cwnd'=1 を 0 に) → CwndLowerInv で kill
run_mut M1_cwnd_zero \
'    /\ ssthresh'"'"' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd'"'"' = 1' \
'    /\ ssthresh'"'"' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd'"'"' = 0'

# M2: RTO 損失で ssthresh を常に保持 (初回半減を消す) → R-CC-053 取りこぼし
run_mut M2_no_halve \
'    /\ ssthresh'"'"' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd'"'"' = 1' \
'    /\ ssthresh'"'"' = ssthresh
    /\ cwnd'"'"' = 1'

# M3: RTO 損失で ssthresh を常に半減 (再送済み保持を消す) → R-CC-054 取りこぼし
run_mut M3_always_halve \
'    /\ ssthresh'"'"' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd'"'"' = 1' \
'    /\ ssthresh'"'"' = Half(flightSize)
    /\ cwnd'"'"' = 1'

# M4: 状態選択を反転 (SS↔CA) → StateSelectInv で kill
run_mut M4_state_flip \
'        /\ state'"'"' = IF nc >= ssthresh THEN "CongestionAvoidance" ELSE "SlowStart"' \
'        /\ state'"'"' = IF nc >= ssthresh THEN "SlowStart" ELSE "CongestionAvoidance"'

# M5: deflate を消す (ExitFR で cwnd を ssthresh でなく inflate 維持) → DeflateProp で kill
run_mut M5_no_deflate \
'ExitFR ==
    /\ state = "FastRecovery"
    /\ cwnd'"'"' = ssthresh' \
'ExitFR ==
    /\ state = "FastRecovery"
    /\ cwnd'"'"' = Clip(cwnd + 1)'

# M6: RTO 倍化を消す (rtoStage を据え置き) → RtoMonotoneProp で kill
run_mut M6_no_backoff \
'    /\ rtoStage'"'"' = IF rtoStage < StageMax THEN rtoStage + 1 ELSE StageMax' \
'    /\ rtoStage'"'"' = rtoStage'

# M7: SS の増分を SMSS 超に (cwnd+=2) → 積極性上限 (INV-CC-010) を縛れているか
run_mut M7_ss_overgrow \
'NewAckSS ==
    /\ state = "SlowStart"
    /\ LET nc == Clip(cwnd + 1) IN' \
'NewAckSS ==
    /\ state = "SlowStart"
    /\ LET nc == Clip(cwnd + 2) IN'

# M8: FR inflate を ssthresh-1 に (下限割れの恐れ) → CwndLowerInv / DeflateProp
run_mut M8_fr_underinflate \
'    /\ LET nsst == Half(flightSize) IN
        /\ ssthresh'"'"' = nsst
        /\ cwnd'"'"' = Clip(nsst + 3)' \
'    /\ LET nsst == Half(flightSize) IN
        /\ ssthresh'"'"' = nsst
        /\ cwnd'"'"' = nsst - 1'

# M9: ssthresh 下限を割る (Half の下限 2 を 1 に) → SsthreshLowerInv で kill
run_mut M9_ssthresh_floor \
'Half(f) == IF f \div 2 > 2 THEN f \div 2 ELSE 2' \
'Half(f) == IF f \div 2 > 2 THEN f \div 2 ELSE 1'

# M10: EnterFR の cwnd を deflate せず ssthresh のみに (3*SMSS inflate を消す)
#      → inflate が無くても StateSelectInv(FR中 cwnd>=ssthresh) は満たすので survivor 予想。
#         正常系の分類 (FR の積極性を直接縛る INV は今回スコープ外) として記録対象。
run_mut M10_no_fr_inflate \
'    /\ LET nsst == Half(flightSize) IN
        /\ ssthresh'"'"' = nsst
        /\ cwnd'"'"' = Clip(nsst + 3)' \
'    /\ LET nsst == Half(flightSize) IN
        /\ ssthresh'"'"' = nsst
        /\ cwnd'"'"' = nsst'

# M11: DupAckLimited で cwnd を inflate (Limited Transmit 違反) → StateSelectInv?
#      SS/CA 中に cwnd を増やすと状態整合が崩れうる。
run_mut M11_limited_inflate \
'    /\ dupAckCount'"'"' = dupAckCount + 1
    /\ lastAct'"'"' = "DupAckLimited"
    /\ UNCHANGED << state, cwnd, ssthresh, flightSize, rtoStage, retx >>' \
'    /\ dupAckCount'"'"' = dupAckCount + 1
    /\ lastAct'"'"' = "DupAckLimited"
    /\ cwnd'"'"' = Clip(cwnd + 1)
    /\ UNCHANGED << state, ssthresh, flightSize, rtoStage, retx >>'

# M12: RtoExpire で SlowStart でなく CA へ → StateSelectInv (cwnd=1<ssthresh なら SS のはず)
run_mut M12_rto_to_ca \
'    /\ ssthresh'"'"' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd'"'"' = 1
    /\ state'"'"' = "SlowStart"' \
'    /\ ssthresh'"'"' = IF retx THEN ssthresh ELSE Half(flightSize)
    /\ cwnd'"'"' = 1
    /\ state'"'"' = "CongestionAvoidance"'

# M13: FastRecovery 中の追加 dup ACK で cwnd を ssthresh-1 に下げる
#      → StateSelectInv の FR 分岐 (FR中 cwnd>=ssthresh) を割る
run_mut M13_fr_below_ssthresh \
'DupAckInFR ==
    /\ state = "FastRecovery"
    /\ cwnd'"'"' = Clip(cwnd + 1)' \
'DupAckInFR ==
    /\ state = "FastRecovery"
    /\ cwnd'"'"' = ssthresh - 1'

# M14: NewAckCA を SlowStart でも発火可能にする (状態ガード緩め)
#      → SS で +1 のはずが CA 経路で増えても増分は同じなので survivor 予想 (等価)。
#         状態ガードの厳密さ確認用。survivor なら equivalent と記録。
run_mut M14_ca_guard_relax \
'NewAckCA ==
    /\ state = "CongestionAvoidance"
    /\ cwnd'"'"' = Clip(cwnd + 1)' \
'NewAckCA ==
    /\ state \in { "CongestionAvoidance", "SlowStart" }
    /\ cwnd'"'"' = Clip(cwnd + 1)'

echo "=== cc mutation run done ==="

#!/usr/bin/env bash
# TLC 駆動ラッパ。sandbox の /tmp が read-only なので java.io.tmpdir を退避。
set -e
JAR=/nix/store/h340cym0zlka9ymki6909r9lannhw5kc-tlaplus-1.8.0/share/tlaplus/tla2tools.jar
JAVA=/nix/store/c3pl7bqrx3d2rc3dh98z6yaj0mv1p52g-openjdk-21.0.10+7/bin/java
TMP=/tmp/claude-1001
CFG="${1:-TCP.cfg}"
SPEC="${2:-TCP.tla}"
MD="${3:-$TMP/tlc-md-$$}"
mkdir -p "$TMP" "$MD"
exec "$JAVA" -Djava.io.tmpdir="$TMP" -XX:+UseParallelGC -cp "$JAR" tlc2.TLC \
    -metadir "$MD" -config "$CFG" "$SPEC"

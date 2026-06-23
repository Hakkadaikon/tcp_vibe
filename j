#!/usr/bin/env bash
# aqua 管理ツールで justfile を実行する薄いラッパ。
# aqua-proxy の鶏卵問題(初回起動時に環境未設定でレジストリを探しに行く)を避けるため、
# 環境を固めてから aqua 配下の実体 just を直接起動する。
# 使い方: ./j <recipe>   例) ./j test / ./j check
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export AQUA_ROOT_DIR="$here/.aqua"
export AQUA_GLOBAL_CONFIG="$here/aqua.yaml"
export PATH="$here/.aqua/bin:$PATH"
export GOCACHE="$here/.gocache"
export GOFLAGS="-mod=mod"

just_real="$here/.aqua/pkgs/github_release/github.com/casey/just/1.54.0/just-1.54.0-x86_64-unknown-linux-musl.tar.gz/just"
if [ ! -x "$just_real" ]; then
  echo "just not installed under .aqua — run: AQUA_ROOT_DIR=$here/.aqua AQUA_GLOBAL_CONFIG=$here/aqua.yaml aqua install" >&2
  exit 1
fi
exec "$just_real" --justfile "$here/justfile" --working-directory "$here" "$@"

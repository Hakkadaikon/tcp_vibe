# 自作 TCP スタック (RFC 9293 + RFC 5961) タスクランナー
# パッケージ管理は aqua。go/just は .aqua 配下に固定。

# aqua 配下のツールを PATH 先頭に。GOCACHE はサンドボックス read-only 回避で repo 内に。
export AQUA_ROOT_DIR := justfile_directory() + "/.aqua"
export AQUA_GLOBAL_CONFIG := justfile_directory() + "/aqua.yaml"
export PATH := justfile_directory() + "/.aqua/bin:" + env_var('PATH')
export GOCACHE := justfile_directory() + "/.gocache"
export GOFLAGS := "-mod=mod"

_default:
    @just --list

# 初回セットアップ: aqua で go/just を取得 (要 aqua 本体が PATH に)
setup:
    aqua install
    @echo "tools installed under .aqua"

# ビルド
build:
    go build ./...

# 全テスト (race 検出付き)
test:
    go test -race ./...

# flaky 検出: 複数回実行
test-flaky:
    go test -race -count=5 ./...

# 静的解析
vet:
    go vet ./...

# 整形 (.aqua 等のツール配下を避け、モジュール内パッケージのみ)
fmt:
    gofmt -w tcp cmd

# 整形チェック (CI 用, 差分があれば失敗)
fmt-check:
    test -z "$(gofmt -l tcp cmd)"

# property-based テストだけ多めに回す
test-pbt:
    go test -race -run 'Property|Quick|Frame' -count=20 ./...

# 検証ゲート全部 (提出前)
check: vet fmt-check test-flaky

# カバレッジ
cover:
    go test -coverprofile=.gocache/cover.out ./...
    go tool cover -func=.gocache/cover.out

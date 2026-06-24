# 自作 TCP スタック (RFC 9293 + RFC 5961) タスクランナー
# パッケージ管理は aqua。Go は .aqua 配下に固定し、各レシピで PATH を通す。
# `just <レシピ名>` で実行する (例: just test)。

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

# 実機デモのバイナリを作る (sudo で実行するため独立したファイルに出力)
demo-build:
    go build -o bin/tcpdemo ./cmd/tcpdemo
    @echo "built bin/tcpdemo (run with sudo on a host that has /dev/net/tun)"

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
    gofmt -w tcp cmd e2e

# 整形チェック (CI 用, 差分があれば失敗)
fmt-check:
    test -z "$(gofmt -l tcp cmd e2e)"

# property-based テストだけ多めに回す
test-pbt:
    go test -race -run 'Property|Quick|Frame' -count=20 ./...

# パーサ (ヘッダ/オプション) の fuzz を短時間探索する (target ごと既定 10s)。
# 通常テストでも seed corpus は実行され panic を検出する。長く回すなら time を増やす。
fuzz time="10s":
    go test -run '^$' -fuzz='^FuzzParseTCPHeader$' -fuzztime={{time}} ./tcp/
    go test -run '^$' -fuzz='^FuzzParseTCPOptions$' -fuzztime={{time}} ./tcp/
    go test -run '^$' -fuzz='^FuzzParseIPv4Header$' -fuzztime={{time}} ./tcp/

# 2 プロセス間の実通信 e2e (tcpdemo を server/client で起動し UDP トンネル越しに検証)
# build tag e2e で分離。-count=1 でキャッシュを無効化し毎回実プロセスを起動する。
e2e:
    go test -tags e2e -count=1 ./e2e/

# 検証ゲート全部 (提出前)。e2e は実プロセス起動で重く遅いため含めず、`just e2e` で個別に回す。
check: vet fmt-check test-flaky

# カバレッジ
cover:
    go test -coverprofile=.gocache/cover.out ./...
    go tool cover -func=.gocache/cover.out

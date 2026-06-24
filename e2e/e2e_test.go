//go:build e2e

// tcpdemo を server / client の 2 プロセスとして実際に起動し、UDP トンネル
// (root 不要) 越しに握手 → データ転送 → close が成立することを検証する e2e テスト。
// 通常の `go test` では走らず、`go test -tags e2e`（= `just e2e`）でのみ実行する。
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildDemo は tcpdemo バイナリをテスト用一時ディレクトリへビルドしてパスを返す。
func buildDemo(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "tcpdemo")
	out, err := exec.Command("go", "build", "-o", bin, "../cmd/tcpdemo").CombinedOutput()
	if err != nil {
		t.Fatalf("tcpdemo のビルド失敗: %v\n%s", err, out)
	}
	return bin
}

// runDemo は tcpdemo を 1 プロセス起動し、終了を待って exit code と全ログを返す。
// timeout を超えたら kill して失敗扱いの結果を返す。t.Cleanup で確実に後始末する。
func runDemo(t *testing.T, bin string, args ...string) (exitCode int, logs string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, bin, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("プロセス起動失敗 (%v): %v", args, err)
	}
	// テスト本体が assert 前に return しても、起き続けるプロセスを残さない。
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	err := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("プロセスがタイムアウト (%v)。ログ:\n%s", args, buf.String())
	}
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("プロセス待機失敗 (%v): %v", args, err)
		}
	}
	return code, buf.String()
}

// TestUDPHandshakeDataClose は UDP トンネル越しに 2 プロセスで握手 → データ → close
// が成立し、両プロセスが exit 0 で終わること、節目のログとバイト一致を検証する。
func TestUDPHandshakeDataClose(t *testing.T) {
	bin := buildDemo(t)

	// 固定ポートは連続実行や並行実行で衝突 (address already in use) しうる。
	// 2 プロセスが互いの相手ポートを起動時フラグで知る設計上 OS 自動割当 (port 0) は
	// 使えない (片方の割当ポートをもう片方へ事前に伝える手段が無い) ため、
	// 実行ごとに ephemeral 帯のランダムな連番ペアを選んで衝突確率を下げる。
	base := 40000 + rand.Intn(20000)
	portA := fmt.Sprintf("%d", base)   // server の UDP ローカル / client の相手
	portB := fmt.Sprintf("%d", base+1) // client の UDP ローカル / server の相手
	const want = "hello from client"   // client が送り server が受け取るデータ

	type result struct {
		code int
		logs string
	}
	var serverRes, clientRes result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serverRes.code, serverRes.logs = runDemo(t, bin,
			"--mode=server", "--link=udp",
			"--udp-local-port="+portA, "--udp-remote-port="+portB,
			"--local-ip=10.0.0.1", "--local-port=9000",
			"--remote-ip=10.0.0.2", "--remote-port=9001",
			"--msl=1s")
	}()

	// server の bind が済む猶予を与えてから client を起動する。
	time.Sleep(300 * time.Millisecond)
	clientRes.code, clientRes.logs = runDemo(t, bin,
		"--mode=client", "--link=udp",
		"--udp-local-port="+portB, "--udp-remote-port="+portA,
		"--local-ip=10.0.0.2", "--local-port=9001",
		"--remote-ip=10.0.0.1", "--remote-port=9000",
		"--msl=1s")
	wg.Wait()

	if serverRes.code != 0 {
		t.Errorf("server が非 0 終了 (code=%d)。ログ:\n%s", serverRes.code, serverRes.logs)
	}
	if clientRes.code != 0 {
		t.Errorf("client が非 0 終了 (code=%d)。ログ:\n%s", clientRes.code, clientRes.logs)
	}

	// 両プロセスが握手と close を完遂したことをログで確認する。
	for _, c := range []struct {
		name string
		logs string
	}{{"server", serverRes.logs}, {"client", clientRes.logs}} {
		if !strings.Contains(c.logs, "ESTABLISHED 到達") {
			t.Errorf("%s のログに ESTABLISHED 到達 が無い。ログ:\n%s", c.name, c.logs)
		}
		if !strings.Contains(c.logs, "CLOSED 到達") {
			t.Errorf("%s のログに CLOSED 到達 が無い。ログ:\n%s", c.name, c.logs)
		}
	}

	// データが server へバイト単位で正しく届いたことを確認する。
	if !strings.Contains(serverRes.logs, want) {
		t.Errorf("server が期待データ %q を受信していない。ログ:\n%s", want, serverRes.logs)
	}
}

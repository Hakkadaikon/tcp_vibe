//go:build linux

package tcp

import "testing"

// tunLink は Link インターフェースを満たす (コンパイル時保証)。
var _ Link = (*tunLink)(nil)

// TUN は /dev/net/tun と CAP_NET_ADMIN が要る。デバイス/権限が無い環境
// (このサンドボックス等) では NewTUNLink がエラーを返すことを確認する。
// 開けた場合は実機環境なので skip する (実通信テストは実機の責務)。
func TestTUNLink_RequiresCapability(t *testing.T) {
	link, err := NewTUNLink("tun0")
	if err == nil {
		link.Close()
		t.Skip("/dev/net/tun を開けた (実機環境)。実通信テストは別途")
	}
	t.Logf("NewTUNLink がエラーを返した (期待通り): %v", err)
}

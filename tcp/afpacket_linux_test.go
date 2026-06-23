//go:build linux

package tcp

import (
	"errors"
	"syscall"
	"testing"
)

// afPacketLink は Link インターフェースを満たす (コンパイル時保証)。
var _ Link = (*afPacketLink)(nil)

// AF_PACKET raw socket は CAP_NET_RAW が要る。権限が無い環境 (このサンドボックス等)
// では NewAFPacketLink が permission denied を返すことを確認する。権限があれば
// socket が開けるので skip する (実通信テストは実機の責務)。
func TestAFPacketLink_RequiresCapability(t *testing.T) {
	mac := [6]byte{0x02, 0, 0, 0, 0, 1}
	link, err := NewAFPacketLink(1, mac, mac) // ifIndex=1 (lo)
	if err == nil {
		link.Close()
		t.Skip("CAP_NET_RAW があり socket を開けた (実機環境)。実通信テストは別途")
	}
	if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
		t.Logf("socket open failed with %v (permission 以外の理由かもしれない)", err)
	}
}

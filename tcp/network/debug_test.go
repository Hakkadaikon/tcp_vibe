package network

import "testing"

// IPStr は [4]byte をドット区切りにする。
func TestIPStr(t *testing.T) {
	if got := IPStr([4]byte{10, 0, 0, 2}); got != "10.0.0.2" {
		t.Errorf("IPStr = %q, want 10.0.0.2", got)
	}
}

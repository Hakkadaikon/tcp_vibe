package tcp

import "testing"

// flagsStr は立っているビットを記号で連結し、無印は "-" を返す。
func TestFlagsStr(t *testing.T) {
	cases := []struct {
		in   Flags
		want string
	}{
		{0, "-"},
		{FlagSYN, "SYN"},
		{FlagSYN | FlagACK, "SYN|ACK"},
		{FlagFIN, "FIN"},
	}
	for _, c := range cases {
		if got := flagsStr(c.in); got != c.want {
			t.Errorf("flagsStr(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ipStr は [4]byte をドット区切りにする。
func TestIPStr(t *testing.T) {
	if got := ipStr([4]byte{10, 0, 0, 2}); got != "10.0.0.2" {
		t.Errorf("ipStr = %q, want 10.0.0.2", got)
	}
}

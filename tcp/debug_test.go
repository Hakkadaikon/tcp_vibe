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

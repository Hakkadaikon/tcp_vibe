package tcp

// flagsStr は制御ビットを "SYN|ACK" のような可読文字列にする (ログ用)。
// 立っていなければ "-" を返す。
func flagsStr(f Flags) string {
	names := []struct {
		bit  Flags
		name string
	}{
		{FlagSYN, "SYN"}, {FlagACK, "ACK"}, {FlagFIN, "FIN"},
		{FlagRST, "RST"}, {FlagPSH, "PSH"}, {FlagURG, "URG"},
	}
	s := ""
	for _, n := range names {
		if f.Has(n.bit) {
			if s != "" {
				s += "|"
			}
			s += n.name
		}
	}
	if s == "" {
		return "-"
	}
	return s
}

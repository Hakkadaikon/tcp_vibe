package network

import "fmt"

// Debug は診断ログのフック。既定 nil で何もしない (本番性能に影響させない)。
// アプリ層 (cmd) が代入すると、受信ループ・送信・TUN I/O の各所が経路の様子を
// このフック経由で出力する。挙動は変えず観測だけ足すための仕組み。
//
// 各呼び出し側は必ず if Debug != nil { Debug(...) } で囲み、nil 時のオーバヘッドを
// nil チェックのみに抑える。
var Debug func(format string, args ...any)

// Debugf は Debug が設定されているときだけフォーマットして渡す薄いヘルパ。
// nil チェックの記述を 1 箇所に集約する。
func Debugf(format string, args ...any) {
	if Debug != nil {
		Debug(format, args...)
	}
}

// IPStr は [4]byte の IPv4 アドレスをドット区切り文字列にする (ログ用)。
func IPStr(a [4]byte) string {
	return fmt.Sprintf("%d.%d.%d.%d", a[0], a[1], a[2], a[3])
}

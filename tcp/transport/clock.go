package transport

import "time"

// Clock は時刻取得の seam。本番は time.Now を渡し、テストは fake clock を注入する。
// 再送・TIME-WAIT の境界を決定論的に検証するために分離する。
type Clock func() time.Time

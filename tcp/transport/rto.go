package transport

// 動的 RTO 推定 (RFC 6298)。値はすべてミリ秒スケールの整数で扱う。
// a=1/8, b=1/4, K=4 はいずれも 2 のべきなので、EWMA は切り捨て除算による
// 厳密な整数演算になる (浮動小数を使わない)。
//
//	RTTVAR' = (3*RTTVAR + |SRTT-R'|) / 4   (RFC 6298 §2.3, (1-b)=3/4, b=1/4)
//	SRTT'   = (7*SRTT   + R')        / 8   ((1-a)=7/8, a=1/8)
//	RTO     = max(RTO_MIN, SRTT + max(G, K*RTTVAR))   (§2.2/§2.4)
const (
	rtoMinMS = 1000 // RTO 下限 (RFC 6298 §2.4)。ミリ秒。
	kRTTVAR  = 4    // RTTVAR の係数 K (RFC 6298 §2.3)。
)

// rttEstimator は RTT 推定の状態。srtt/rttvar/g はいずれもミリ秒。
type rttEstimator struct {
	srtt   uint32 // 平滑化 RTT
	rttvar uint32 // RTT の変動
	g      uint32 // クロック粒度 (granularity)
}

// initEst は初回サンプル R での初期化 (RFC 6298 §2.2): SRTT=R, RTTVAR=R/2。
func initEst(r, g uint32) rttEstimator {
	return rttEstimator{srtt: r, rttvar: r / 2, g: g}
}

// UpdateEst は 2 回目以降のサンプル R' で推定を更新する (RFC 6298 §2.3)。
// 順序厳守: RTTVAR は更新前 (旧) の SRTT を使い、そのあと SRTT を更新する。
func (e rttEstimator) UpdateEst(r uint32) rttEstimator {
	rttvar := (3*e.rttvar + absDiffU32(e.srtt, r)) / 4
	srtt := (7*e.srtt + r) / 8
	return rttEstimator{srtt: srtt, rttvar: rttvar, g: e.g}
}

// Rto は現在の推定から RTO (ミリ秒) を返す。下限 rtoMinMS でクランプする。
func (e rttEstimator) Rto() uint32 {
	raw := e.srtt + maxU32(e.g, kRTTVAR*e.rttvar)
	return maxU32(rtoMinMS, raw)
}

// backoff は満了時の指数バックオフ (RFC 6298 §5.5): cur を 2 倍、cap で飽和。
func backoff(cap, cur uint32) uint32 {
	return minU32(cap, cur*2)
}

func absDiffU32(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

func maxU32(a, b uint32) uint32 {
	if a > b {
		return a
	}
	return b
}

func minU32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

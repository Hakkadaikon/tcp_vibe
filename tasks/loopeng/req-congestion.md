# 輻輳制御 + RTO 要件 (RFC 5681 + RFC 6298)

## RTO 計算 (RFC6298)
- R-CC-001/002 RTT 測定前 RTO=1s (MAY 3s)
- R-CC-003 初回: SRTT=R, RTTVAR=R/2, RTO=SRTT+max(G,K*RTTVAR), K=4
- R-CC-004 以降(順序厳守): RTTVAR=(1-b)*RTTVAR+b*|SRTT-R'|; SRTT=(1-a)*SRTT+a*R'
- R-CC-005 a=1/8, b=1/4
- R-CC-007 RTO 下限 1s / R-CC-008 上限 >=60s
- R-CC-009 Karn: 再送セグメントから RTT 取らない (timestamp 時は例外 R-CC-010)
- R-CC-014〜017 タイマ管理: 送信でstart/全ACKでstop/新規ACKでrestart
- R-CC-018/019/020 満了→最古再送, RTO*=2, 倍化RTOで起動
- R-CC-021 SYN ロス満了で RTO<3s なら データ開始時 3s へ

## 輻輳制御 (RFC5681)
- R-CC-030 送信上限 = highest ACK + min(cwnd, rwnd)
- R-CC-041 IW: SMSS<=1095→4*SMSS; <=2190→3*SMSS; else 2*SMSS
- R-CC-044 ssthresh 初期は高く (例 最大窓)
- R-CC-046 cwnd<ssthresh=slow start, >=ssthresh=congestion avoidance
- R-CC-047 SS: 新規ACKごと cwnd += min(N, SMSS)
- R-CC-049/050 CA: 1 SMSS/RTT (byte counting 式2)
- R-CC-053 RTO損失: ssthresh=max(FlightSize/2, 2*SMSS) (初回再送のみ; R-CC-054 再送済みは保持)
- R-CC-056 RTO: cwnd=LW=1*SMSS
- R-CC-059 fast retransmit: 3 dup ACK で再送タイマ待たず再送
- R-CC-061/062 3dup: ssthresh=max(FlightSize/2,2*SMSS), 再送, cwnd=ssthresh+3*SMSS
- R-CC-063 追加dupごと cwnd+=SMSS
- R-CC-066 新規ACK(回復完了): cwnd=ssthresh (deflate)
- R-CC-039 重複ACK定義: 未確認データあり/データ無し/SYN&FIN off/ACK=最大ACK/窓同一

## cwnd 状態: SS ↔ CA ↔ FR (詳細は agent 抽出参照)

## INV
- INV-CC-001 cwnd >= 1*SMSS / INV-CC-003 ssthresh >= 2*SMSS
- INV-CC-004 RTO >= 1s / INV-CC-008 連続満了で RTO 単調倍化
- INV-CC-006 送信中データ <= min(cwnd, rwnd)
- INV-CC-012 estimator 更新順 RTTVAR→SRTT / INV-CC-014 Karn
- INV-CC-009 cwnd<ssthresh⇒SS, >⇒CA / INV-CC-016 FR 終了で cwnd=ssthresh

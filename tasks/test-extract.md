# TCP スタック テスト観点 網羅抽出 (test-extract)

技法凡例: EP=同値分割 BVA=境界値 DT=デシジョンテーブル PBT=property-based(testing/quick)
STT=状態遷移 MT=メタモルフィック GOLD=ゴールデン FAKE=fake clock 決定論

## A. ヘッダ encode/decode + checksum
- T-001 TCP ヘッダ往復 decode(encode(h))==h [PBT+GOLD] INV-encdec
- T-002 IPv4 ヘッダ往復 [PBT+GOLD]
- T-003 data offset 境界(5=20byte/6=opt/<5 エラー) [BVA]
- T-004 checksum 計算正当性(擬似ヘッダ込み) [GOLD+Lean] INV-cksum
- T-005 checksum 検証往復+1bit反転検出 [PBT+MT] INV-cksum
- T-006 16bit アラインメント(奇数長ゼロパディング) [BVA]
- T-007 end-around carry とゼロ表現(0xFFFF→0x0000) [BVA+Lean] INV-cksum
- T-008 control bit 個別保存(SYN/ACK/FIN/RST/PSH/URG) [DT+PBT]
- T-009 short read 拒否(20/19byte) [BVA]

## B. seq mod 2^32 比較
- T-010 SEG_LT 境界(0<1,2^32-1<0=true,0<2^32-1=false,x<x=false) [BVA+PBT] INV-seqorder
- T-011 SEG_LEQ/GT/GEQ 境界 [BVA]
- T-012 三分律/反対称(半周内) [PBT+Lean本命] INV-seqorder
- T-013 acceptable ACK SND.UNA<SEG.ACK=<SND.NXT 境界 [BVA+DT] INV-ackvalid
- T-014 seq 加算ラップ(2^32-1+1=0, panic無) [BVA+PBT]

## C. フレーミング再分割(主役 PBT)
- T-015 任意チャンク分割で同一パケット列復元 [PBT主役] INV-D
- T-016 部分読み(IP/TCP/payload 途中分断, 1byte feed) [BVA+STT] INV-D
- T-017 連結到着(2pkt 1buf, 1.5pkt 残保持) [BVA] INV-D
- T-018 length 確定(宣言ちょうど/不足は待つ/上限超エラー) [BVA+DT] INV-C
- T-019 分割不変(1回 feed == 任意分割 feed) [MT] INV-D
- T-020 ゼロ長/最小/最大セグメント [BVA]

## D. 11 状態遷移(許可+拒否)
- T-021 全許可遷移網羅(状態図 9293:795-838) [STT] INV-A
- T-022 許可されない遷移の拒否(負例マトリクス) [STT負例+DT] INV-A
- T-023 SYN-RCVD→LISTEN は passive 由来のみ(Note1) [DT] INV-A
- T-024 ESTAB 到達は正 3way のみ(不完全 handshake 不到達) [STT負例] INV-B
- T-025 RST 受信による各状態→CLOSED 強制遷移 [STT]

## E. 3way handshake
- T-026 能動オープン完了 [STT+GOLD] INV-B
- T-027 受動オープン完了 [STT]
- T-028 同時オープン [STT] INV-B
- T-029 ISN 検証/ack=ISN+1 境界 [BVA]
- T-030 half-open 検出 [DT]
- T-031 SYN-SENT RST 受理条件(ACK が SYN 確認) [DT]

## F. データ転送・受信窓・累積 ACK
- T-032 acceptability 4ケース表 [DT主+BVA] INV-C
- T-033 受信窓境界(下限ちょうど/直前/上限ちょうど/直後/跨ぎ) [BVA] INV-C
- T-034 累積 ACK で RCV.NXT 前進(順序保存 PBT) [STT+PBT]
- T-035 out-of-order(窓内先行→保持 or drop, dup ACK) [DT]
- T-036 重複セグメント(完全古い→ACK再送のみ/一部重複) [BVA]
- T-037 in-flight 集合増減(send で増 ACK で減, 幽霊無) [PBT+STT] INV-E
- T-038 ゼロウィンドウ + window update [DT]
- T-039 SEG.WND advertise と MAX.SND.WND 更新 [BVA]

## G. RFC 5961 challenge ACK(攻撃 1対1)
- T-040 RST seq=RCV.NXT → reset [DT+BVA] INV-F
- T-041 RST 窓内 !=RCV.NXT → challenge ACK, 接続維持 [DT] INV-F
- T-042 RST 窓外 → silently drop [BVA] INV-F
- T-043 RST 境界(NXT=reset/NXT+1=ch/上限窓内=ch/上限超=drop) [BVA] INV-F
- T-044 SYN in-window でも常に challenge ACK [DT] INV-G
- T-045 SYN 窓外でも challenge ACK(RST でない) [BVA] INV-G
- T-046 Data injection ACK 範囲チェック境界 [BVA+DT] INV-H
- T-047 範囲外 ACK でデータ適用されない [DT] INV-H
- T-048 spoofed FIN robustness(ACK 範囲外 FIN で閉じない) [DT]
- T-049 ACK throttling(5秒10個, 11個目抑制, 窓経過でリセット) [FAKE+BVA] INV-throttle
- T-050 throttling 設定可変(代表1ケース, YAGNI寄り) [EP]

## H. 再送タイマ(fake clock)
- T-051 RTO 到達で再送(ちょうど/直前/直後) [FAKE+BVA] LIVE-3
- T-052 ACK で再送タイマ解除 [FAKE+STT]
- T-053 指数バックオフ(RTO,2RTO,4RTO 境界) [FAKE+BVA]
- T-054 再送上限到達で接続中断 [FAKE+STT] LIVE-3
- T-055 RTT 計測(Karn: 再送分は計測しない) [FAKE+BVA]

## I. close / TIME-WAIT
- T-056 graceful close 4way [STT]
- T-057 simultaneous close(FW-1→CLOSING→TIME-WAIT) [STT] INV-A
- T-058 passive close(CW→LAST-ACK→CLOSED) [STT]
- T-059 TIME-WAIT 2MSL timeout→CLOSED(ちょうど/直前/直後) [FAKE+BVA] LIVE-2
- T-060 TIME-WAIT 中 FIN 再送→ACK 再送+タイマ再起動 [FAKE+STT]
- T-061 FIN seq 占有(ack=FIN-seq+1) [BVA]
- T-062 FIN-WAIT-1→TIME-WAIT 直行(Note2) [STT] INV-A

## J. 横断・活性
- T-063 LIVE-1: SYN は ESTAB か失敗で決着(宙吊り無) [FAKE]
- T-064 quiet time(YAGNI 優先度低) [FAKE]
- T-065 並行性: 受信単一 goroutine+送信 mutex, -race クリーン [go test -race] INV-E

## INV ↔ T-ID
- INV-A: T-021,022,023,025,057,062
- INV-B: T-024,026,028
- INV-C: T-032,033 / length:T-018
- INV-D: T-015,016,017,019
- INV-E: T-037,065
- INV-F: T-040,041,042,043
- INV-G: T-044,045
- INV-H: T-046,047
- INV-throttle: T-049
- INV-encdec/cksum/seqorder/ackvalid: T-001,002 / T-004,005,006,007 / T-010,011,012 / T-013
- LIVE-1: T-063 / LIVE-2: T-059 / LIVE-3: T-051,054

## 0段クローズ
T-001〜T-065 欠番なし。技法未割当 0 件。YAGNI 寄り(T-050,064)も優先度低で残置。

## 実装前の RFC 差分注意
- RFC 5961 は RFC 793 を置換。SYN in-window は旧 793=RST だが 5961=challenge ACK。
- challenge ACK は3攻撃同一形式 <SEQ=SND.NXT><ACK=RCV.NXT><CTL=ACK>。共通ヘルパでテスト可。
- throttling はタイマ不要、timestamp+counter。fake clock で counter リセット検証。

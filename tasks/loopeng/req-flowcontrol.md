# フロー制御要件 (RFC 9293 + RFC 1122)

## 受信窓動的更新
- R-FC-001 右窓端 (RCV.NXT+RCV.WND) を左へ動かさない (窓を縮めない)
- R-FC-007 RCV.NXT 前進時も RCV.NXT+RCV.WND 一定に保つ (窓端固定)
- R-FC-006 窓更新採用は SND.UNA<=SEG.ACK<=SND.NXT の最大 ACK のみ

## SWS 回避 受信側
- R-FC-012 reduction >= min(Fr*RCV.BUFF, Eff.snd.MSS) (Fr=1/2) まで窓固定、満たせば RCV.WND=RCV.BUFF-RCV.USER

## SWS 回避 送信側 (usable U = SND.UNA+SND.WND-SND.NXT)
- R-FC-023 送る条件: (1)min(D,U)>=MSS (2)[UNA=NXT &]PUSH & D<=U (3)[UNA=NXT &]min(D,U)>=Fs*Max(SND.WND) (4)override timeout
- R-FC-024 override timeout 0.1〜1.0s

## Nagle
- R-FC-030 未確認データ中 (SND.NXT>SND.UNA) は小データ溜める、確認 or フルMSS まで送らない
- R-FC-032 無効化手段必須 (TCP_NODELAY)

## Zero-window probe / persist
- R-FC-040 窓0でも persist で >=1 octet probe / R-FC-044 RTO後初回, 指数バックオフ
- R-FC-043 受信側は窓0でも RCV.NXT+窓(0) の ACK を返す

## Delayed ACK (RFC1122)
- R-FC-051 遅延 < 0.5s 必須 / R-FC-052 2 フルセグ毎に1 ACK
- R-FC-053 out-of-order/gap 埋めは即 ACK

## Keepalive (RFC1122)
- R-FC-061 既定 OFF / R-FC-063 間隔 >=2h / R-FC-064 単一 probe 無応答で切断しない
- R-FC-066 probe seq = SND.NXT-1

## INV
- INV-FC-001 右窓端 単調非減少 / INV-FC-002 窓0継続中 probe 停止しない
- INV-FC-004 delayed ACK <500ms / INV-FC-006 小窓広告しない
- INV-FC-008 Nagle: 未確認中フル未満送らない / INV-FC-009 override で活性確保
- INV-FC-013 keepalive 既定 OFF / INV-FC-014 窓更新は最大ACKのみ

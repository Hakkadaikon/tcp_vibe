# TCP オプション要件 (RFC 7323 + RFC 2018 + RFC 9293)

## 一般形式 (RFC9293)
- R-OPT-001/002 kind のみ(EOL=0,NOP=1) or kind+length+value。length は全体バイト数
- R-OPT-004 EOL 以降はゼロパディング / R-OPT-006 未知 option は length 分読み飛ばし無視
- R-OPT-007 EOL/NOP 以外は length 必須 / R-OPT-008 不正 length は弾く

## ワイヤ形式
| Option | Kind | Len | レイアウト |
|--|--|--|--|
| EOL | 0 | - | [0] |
| NOP | 1 | - | [1] |
| MSS | 2 | 4 | [2][4][MSS hi][MSS lo] |
| WScale | 3 | 3 | [3][3][shift] |
| SACK-Permitted | 4 | 2 | [4][2] |
| SACK | 5 | 8n+2 | [5][len][L1][R1]... |
| Timestamps | 8 | 10 | [8][10][TSval][TSecr] |

## MSS (RFC9293)
- R-OPT-012 SYN でのみ送る / R-OPT-016 未受信なら既定 536 (IPv4)
- R-OPT-017 Eff.snd.MSS = min(SendMSS, MMS_S) ベース

## Window Scale (RFC7323)
- R-OPT-021 SYN/SYN-ACK でのみ / R-OPT-023 両側が送った時のみ有効
- R-OPT-025 Snd.Wind.Shift=S(受信), Rcv.Wind.Shift=R(自分希望); 未受信は両0
- R-OPT-026 入力: SND.WND = SEG.WND << Snd.Wind.Shift
- R-OPT-027 出力: SEG.WND = RCV.WND >> Rcv.Wind.Shift
- R-OPT-028 SYN/SYN-ACK の window はスケールしない
- R-OPT-029/030 shift <= 14 (超は 14 に clamp)

## Timestamps + PAWS (RFC7323)
- R-OPT-043 ACK セット時 TSecr=TS.Recent を echo / R-OPT-042 ACK 無しは TSecr=0
- R-OPT-052 TS.Recent 更新: SEG.TSval>=TS.Recent && SEG.SEQ<=Last.ACK.sent のみ
- R-OPT-050 RTT = now - SEG.TSecr
- R-OPT-062 PAWS は acceptability より前 / R-OPT-063 SEG.TSval<TS.Recent(valid,非RST)→ACK返しdrop
- R-OPT-057 RST は PAWS 対象外, TSopt で状態更新しない

## SACK (RFC2018)
- R-OPT-080 SACK-Permitted は SYN のみ / R-OPT-085 折衝後のみ SACK 生成
- R-OPT-081 ブロック = [Left][Right] (32bit), R-OPT-083 最大4 (TS 併用3)
- R-OPT-090/091 SACKed フラグ立て, 再送でスキップ
- R-OPT-092 RTO 時は全 SACKed off, 左窓端必ず再送

## INV
- INV-OPT-001 parse は data offset 境界内 / INV-OPT-002 不正 length 弾く
- INV-OPT-008 shift<=14 clamp / INV-OPT-009 wscale 両側折衝のみ有効
- INV-OPT-010 SYN/SYN-ACK window 非スケール
- INV-OPT-011 PAWS で古い seg 受理しない / INV-OPT-012 TS.Recent 単調
- INV-OPT-015 SACK 折衝のみ / INV-OPT-019 RTO で SACKed off + 左窓端再送

# 接続多重化要件 (RFC 9293)

## 接続識別
- R-MUX-001 接続 = 4-tuple (local IP, local port, remote IP, remote port) で一意
- R-MUX-004 同一 local port に複数接続相乗り可 (remote 違えば別接続, MUST-42)
- R-MUX-005 LISTEN で local IP 未指定なら任意受け、成立時に確定 (MUST-45)

## demux (受信視点 key = (dst_ip,dst_port,src_ip,src_port))
1. 完全一致 TCB → dispatch
2. 無ければ LISTEN (local 一致, remote ワイルドカード) 探す
   - RST→無視, ACK→RST返す, SYN→新TCB派生 SYN-RECEIVED, else drop
3. どちらも無し (CLOSED): RST 無→RST生成 / RST 有→破棄
- R-MUX-009 broadcast/multicast/不正 src の SYN は破棄

## LISTEN→派生
- R-MUX-010 passive OPEN は LISTEN TCB 生成 (既存に影響しない, MUST-41)
- R-MUX-012 SYN 受信→新 TCB 派生 (LISTEN 自身は LISTEN のまま)
- R-MUX-021 既存 4-tuple への OPEN は "already exists"

## TCB / incarnation
- R-MUX-018 接続=1 TCB, TCB 不在=CLOSED
- R-MUX-024 TIME-WAIT 2MSL, 条件付きで新 SYN 受理 (新 ISS>前最大, MAY-2)
- R-MUX-027 SYN-RCVD passive 由来の RST→LISTEN 復帰

## INV
- INV-MUX-001 (核心) 各 4-tuple に非 TIME-WAIT TCB は高々1つ
- INV-MUX-002 demux は 4-tuple 完全一致の TCB にのみ届く
- INV-MUX-003 LISTEN は派生しても LISTEN のまま
- INV-MUX-004 一致無し非RST→RST 1つ生成 / INV-MUX-008 TCB⇔接続 全単射

## 並行性 (TLA+ 対象)
- 接続テーブルの並行 read/write (demux vs OPEN/CLOSE), insert は test-and-set
- LISTEN への同時 SYN で複数派生, TIME-WAIT 削除と新 TCB 生成の競合
- connTable: 4-tuple→TCB, 遷移: PassiveOpen/SegArriveListen(派生)/TimeWaitTimeout(削除)/ReopenFromTimeWait

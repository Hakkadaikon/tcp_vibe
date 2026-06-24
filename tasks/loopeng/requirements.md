# TCP プロトコルスタック自作 — 0段 要件抽出台帳

出典: RFC 9293 (`tasks/rfc/rfc9293.txt`)、RFC 5961 (`tasks/rfc/rfc5961.txt`)。行番号は両ファイルのもの。
原則: 過剰抽出は安全・漏れは危険。

## 0. 状態・変数の定義

状態 (9293:726-770): CLOSED(架空,TCB無), LISTEN, SYN-SENT, SYN-RECEIVED, ESTABLISHED,
FIN-WAIT-1, FIN-WAIT-2, CLOSE-WAIT, CLOSING, LAST-ACK, TIME-WAIT。
- 非同期 (non-synchronized): LISTEN, SYN-SENT, SYN-RECEIVED (9293:1315)
- 同期 (synchronized): ESTABLISHED, FIN-WAIT-1/2, CLOSE-WAIT, CLOSING, LAST-ACK, TIME-WAIT (9293:1317)

送信側変数 (9293:637-656): SND.UNA, SND.NXT, SND.WND, SND.UP, SND.WL1, SND.WL2, ISS。
受信側変数 (9293:658-668): RCV.NXT, RCV.WND, RCV.UP, IRS。
セグメント変数 (9293:707-720): SEG.SEQ, SEG.ACK, SEG.LEN(SYN+FIN含む), SEG.WND, SEG.UP。
RFC5961 追加 (5961:611-632, 9293:3723): MAX.SND.WND = peer から受信した過去最大窓(scale適用後)。
window scale 無しなら 65535 にハードコード可。

## 1. 採番付き要件 (EARS 風)

### 1.1 受理性テスト (acceptability) — 9293 §3.4 / §3.10.7.4
- R-001 [9293:956-962,3494] SEG.LEN=0 & RCV.WND=0 → SEG.SEQ=RCV.NXT の場合のみ受理可。
- R-002 [9293:958,3500] SEG.LEN=0 & RCV.WND>0 → RCV.NXT =< SEG.SEQ < RCV.NXT+RCV.WND。
- R-003 [9293:960,3503] SEG.LEN>0 & RCV.WND=0 → 受理不可。
- R-004 [9293:962-967,3505] SEG.LEN>0 & RCV.WND>0 → 始端 or 終端が窓内。
- R-005 [9293:972-977,3519] RCV.WND=0 でも有効な ACK/URG/RST は特例処理 (MUST-66)。
- R-006 [9293:3523-3530] 受理不可かつ RST 無 → <SEQ=SND.NXT><ACK=RCV.NXT><CTL=ACK> 送り破棄。
- R-007 [9293:3540-3547] straddle 時は窓外部分(SYN/FIN含む)をトリムし RCV.NXT 始まりのみ処理。

### 1.2 シーケンス番号算術 — 9293 §3.4
- R-010 [9293:871-878] 全 seq 算術は modulo 2^32 符号無し、=< は mod 2^32 で。
- R-011 [9293:912-915] acceptable ack: SND.UNA < SEG.ACK =< SND.NXT。
- R-012 [9293:917-919] 再送キュー除去: SEG.SEQ+SEG.LEN =< SEG.ACK で完全確認。
- R-013 [9293:398-403] window は符号無し 32bit (MUST-1)。
- R-014 [9293:979-994] SYN は先頭データ直前、FIN は最終データ直後。SEG.LEN に各1含む。
- R-015 [9293:3760-3770] 窓更新: SND.WL1<SEG.SEQ or (=SEG.SEQ & SND.WL2=<SEG.ACK) のみ。

### 1.3 ISN 選択 — 9293 §3.4.1
- R-020 [9293:1032-1041] clock 駆動 ISN (MUST-8), ISN=M+F(4tuple,key) (SHLD-1)。
- R-021 [9293:1019-1026] ISN clock は約4μsごと増加、約4.55h で一周。

### 1.4 確立 (3way) — 9293 §3.5 / §3.10
- R-030 [9293:2967] active OPEN → snd SYN(ISS), SND.UNA=ISS,SND.NXT=ISS+1, →SYN-SENT。
- R-031 [9293:2966] passive OPEN → LISTEN。
- R-032 [9293:3318-3338] LISTEN+rcv SYN → RCV.NXT=SEG.SEQ+1,IRS=SEG.SEQ, snd SYN,ACK, →SYN-RCVD。
- R-033 [9293:3308-3316] LISTEN+rcv ACK → snd RST(SEQ=SEG.ACK)。
- R-034 [9293:3303-3305] LISTEN+rcv RST → 無視。
- R-035 [9293:3354-3361] SYN-SENT で SEG.ACK=<ISS or >SND.NXT → (RST無なら)snd RST。
- R-036 [9293:3363-3368] SYN-SENT で SND.UNA<SEG.ACK=<SND.NXT → acceptable。
- R-037 [9293:3403-3428] SYN-SENT+rcv SYN,ACK(自SYN ACK済) → ESTAB, snd ACK。
- R-038 [9293:3429-3443] SYN-SENT+rcv SYN(自SYN未ACK=同時オープン) → SYN-RCVD, snd SYN,ACK。
- R-039 [9293:3454] SYN-SENT で SYN/RST 共に無 → 破棄。
- R-040 [9293:3729-3746] SYN-RCVD+rcv ACK(SND.UNA<SEG.ACK=<SND.NXT) → ESTAB。不可なら snd RST。
- R-041 [9293:1305-1310] 同時オープン支持 (MUST-10)、SYN-RCVD の由来(passive/active)記録 (MUST-11)。

### 1.5 終了 — 9293 §3.6 / §3.10
- R-050 [9293:3149] ESTAB+CLOSE → snd FIN, →FIN-WAIT-1。
- R-051 [9293:3143] SYN-RCVD+CLOSE(未送信無) → snd FIN, →FIN-WAIT-1。
- R-052 [9293:3164] CLOSE-WAIT+CLOSE → snd FIN, →LAST-ACK。
- R-053 [9293:3772] FIN-WAIT-1+rcv ACK of FIN → FIN-WAIT-2。
- R-054 [9293:3882-3898] ESTAB+rcv FIN → advance RCV.NXT, snd ACK, →CLOSE-WAIT。
- R-055 [9293:3900] FIN-WAIT-1+rcv FIN → 自FIN ACK済なら TIME-WAIT(2MSL起動), 未なら CLOSING。
- R-056 [9293:3906] FIN-WAIT-2+rcv FIN → TIME-WAIT, 2MSL 起動。
- R-057 [9293:3788] CLOSING+rcv ACK of FIN → TIME-WAIT。
- R-058 [9293:3794] LAST-ACK+rcv ACK of FIN → delete TCB, CLOSED。
- R-059 [9293:1653,834] TIME-WAIT は 2*MSL linger (MUST-13), timeout で CLOSED。
- R-060 [9293:3801,3923] TIME-WAIT+rcv FIN → ACK 再送, 2MSL 再起動。
- R-061 [9293:1655-1663] TIME-WAIT+新 SYN → 条件付き再開 (MAY-2), 新 ISS>前最大。
- R-062 [9293:3778] FIN-WAIT-2 で再送キュー空 → CLOSE を ok 確認できるが TCB は残す。

### 1.6 RST 生成・処理 — 9293 §3.5.2/3
- R-070 [9293:3278-3295] CLOSED+rcv 非RST → snd RST。ACK off→<SEQ=0><ACK=SEG.SEQ+SEG.LEN><RST,ACK>, on→<SEQ=SEG.ACK><RST>。
- R-071 [9293:1468-1490] 非同期で unacceptable ACK → snd RST。
- R-072 [9293:1492-1499] 同期で受理不可seg → snd 空ACK, 同状態維持。
- R-073 [9293:1507-1521] RST 受信(SYN-SENT以外): 窓内なら有効。LISTEN無視/passive SYN-RCVDはLISTEN/他同期はabort CLOSED。
- R-074 [9293:3580-3592] SYN-RCVD+rcv RST: passive由来→LISTEN(通知不要), active由来→"refused" CLOSED。再送キューflush。

### 1.7 データ転送・ACK — 9293 §3.8/§3.10.7.4
- R-080 [9293:3748-3758] ESTAB で SND.UNA<SEG.ACK=<SND.NXT → SND.UNA=SEG.ACK, 確認分除去。重複ACK無視/未送信ACKは破棄。
- R-081 [9293:3833-3866] ESTAB/FW-1/FW-2 で窓内データ → user buffer, advance RCV.NXT, snd ACK。
- R-082 [9293:3485-3489] ACK 集約 (MUST-58), キュー全処理後に ACK (MUST-59)。
- R-083 [9293:3807-3820] URG → RCV.UP=max(RCV.UP,SEG.UP), 通知。
- R-084 [9293:3711] 同期で ACK off → 破棄。

### 1.8 再送・タイムアウト — 9293 §3.8.1/3/§3.10.8
- R-090 [9293:1926] RTO は RFC 6298 (Karn 含む) (MUST-18)。
- R-091 [9293:3939-3944] RTO 発火 → 再送キュー先頭を再送、タイマ再初期化。
- R-092 [9293:1962] slow start/CA/指数バックオフ (MUST-19)。
- R-093 [9293:1984-2004] 送信回数 R2 到達 → 接続を閉じる (MUST-20)。R2 設定可 (MUST-21)。
- R-094 [9293:3932-3937] user timeout → flush, "aborted", delete TCB, CLOSED。

### 1.9 チェックサム — 9293 §3.1
- R-100 [9293:405-415] 擬似ヘッダ+TCPヘッダ+本文の 16bit ワード ones'-comp sum の ones'-comp。
  計算時 checksum 欄 0, 奇数オクテットは右に 0 パディング(送信せず)。
- R-101 [9293:417-448] IPv4 擬似ヘッダ(src,dst,zero,PTCL,TCP Length の96bit)。TCP Length=ヘッダ+データ長。
- R-102 [9293:450-456] IPv6 は RFC8200 8.1 擬似ヘッダ。
- R-103 [9293:458] 送信時必ず生成 (MUST-2), 受信時必ず検証 (MUST-3)。

### 1.10 RFC 5961 緩和策
- R-110 [5961:380-385,9293:3559] 同期+RST+窓外 → silently drop。
- R-111 [5961:383-385,9293:3562] 同期+RST+SEG.SEQ=RCV.NXT → reset (MUST)。
- R-112 [5961:399-408,9293:3567] 同期+RST+窓内だが !=RCV.NXT → challenge ACK <SEQ=SND.NXT><ACK=RCV.NXT><ACK>, 破棄 (MUST)。
- R-113 [5961:421-423,9293:3370] SYN-SENT+RST → ACK が自SYN確認時のみ受理、他は discard。
- R-114 [5961:479-486,9293:3675] 同期+SYN → seq によらず challenge ACK, 破棄 (MUST)。
- R-115 [5961:593-609,9293:3715] ACK 受理: (SND.UNA-MAX.SND.WND)=<SEG.ACK=<SND.NXT のみ。外は破棄して ACK 返す。
- R-116 [5961:611-632] MAX.SND.WND 保持。scale 無しなら 65535。
- R-117 [5961:657-683,9293:3576] challenge ACK throttling (例: 5秒で10個)。timestamp+counter 実装可。

## 2. 状態遷移表 (現状態 → 次状態 / イベント・アクション)

| 現状態 | イベント | アクション | 次状態 | 出典 |
|---|---|---|---|---|
| CLOSED | active OPEN | snd SYN(ISS) | SYN-SENT | 9293:2967 |
| CLOSED | passive OPEN | create TCB | LISTEN | 9293:2966 |
| CLOSED | rcv 非RST | snd RST | CLOSED | 9293:3278 |
| LISTEN | rcv SYN | snd SYN,ACK | SYN-RCVD | 9293:3318 |
| LISTEN | rcv ACK持ち | snd RST | LISTEN | 9293:3308 |
| LISTEN | rcv RST | 無視 | LISTEN | 9293:3303 |
| LISTEN | CLOSE | delete TCB | CLOSED | 9293:3133 |
| SYN-SENT | rcv SYN,ACK(自SYN ACK済) | snd ACK | ESTAB | 9293:3414 |
| SYN-SENT | rcv SYN(同時) | snd SYN,ACK | SYN-RCVD | 9293:3429 |
| SYN-SENT | rcv RST(ACK確認) | "reset", del TCB | CLOSED | 9293:3370 |
| SYN-SENT | CLOSE | del TCB | CLOSED | 9293:3138 |
| SYN-RCVD | rcv ACK of SYN | SND.WND設定 | ESTAB | 9293:3729 |
| SYN-RCVD | rcv RST(passive) | — | LISTEN | 9293:3580 |
| SYN-RCVD | rcv RST(active) | "refused", del | CLOSED | 9293:3587 |
| SYN-RCVD | CLOSE | snd FIN | FIN-WAIT-1 | 9293:3143 |
| SYN-RCVD | rcv FIN | snd ACK | CLOSE-WAIT | 9293:3894 |
| ESTAB | CLOSE | snd FIN | FIN-WAIT-1 | 9293:3149 |
| ESTAB | rcv FIN | snd ACK | CLOSE-WAIT | 9293:3896 |
| ESTAB | rcv RST | abort, del | CLOSED | 9293:3602 |
| FIN-WAIT-1 | rcv ACK of FIN | — | FIN-WAIT-2 | 9293:3772 |
| FIN-WAIT-1 | rcv FIN(自FIN未ACK) | snd ACK | CLOSING | 9293:3900 |
| FIN-WAIT-1 | rcv FIN+ACK(自FIN ACK済) | snd ACK, 2MSL | TIME-WAIT | 9293:848 |
| FIN-WAIT-2 | rcv FIN | snd ACK, 2MSL | TIME-WAIT | 9293:3906 |
| CLOSE-WAIT | CLOSE | snd FIN | LAST-ACK | 9293:3164 |
| CLOSING | rcv ACK of FIN | 2MSL | TIME-WAIT | 9293:3788 |
| LAST-ACK | rcv ACK of FIN | del TCB | CLOSED | 9293:3794 |
| TIME-WAIT | 2MSL timeout | del TCB | CLOSED | 9293:3946 |
| TIME-WAIT | rcv FIN | snd ACK, 2MSL再起動 | TIME-WAIT | 9293:3923 |
| TIME-WAIT | rcv RST | del TCB | CLOSED | 9293:3612 |
| 任意(SYN-RCVD〜CW) | ABORT | snd RST, flush | CLOSED | 9293:3197 |
| 任意 | user timeout | flush, del | CLOSED | 9293:3932 |

Note1 (9293:844): SYN-RCVD→LISTEN は passive 由来のみ。
Note2 (9293:848): FIN-WAIT-1→TIME-WAIT は FIN受信かつ自FIN ACK済の合成遷移。

## 3. RFC 5961 三チェック擬似コード

```
# (a) Blind RST (5961 3.2): 同期状態。SYN-SENT は R-113 別ルール。
on RST in synchronized:
    if not (RCV.NXT <= SEG.SEQ < RCV.NXT+RCV.WND): silently_drop(); return   # R-110
    elif SEG.SEQ == RCV.NXT: reset_connection(); return                       # R-111 MUST
    else: send_challenge_ack(); throttle(); drop(); return                    # R-112 MUST

# (b) Blind SYN (5961 4.2): 同期状態。
on SYN in synchronized:
    send_challenge_ack(); throttle(); drop(); return                          # R-114 MUST

# (c) Blind data injection (5961 5.2): ACK on の全 seg, ACK field check 前段。
on ACK set:
    if not ((SND.UNA - MAX.SND.WND) <= SEG.ACK <= SND.NXT):                    # R-115
        discard(); send(<SEQ=SND.NXT><ACK=RCV.NXT><ACK>); return
    else: proceed_per_state_ack()
# MAX.SND.WND = 過去最大窓, 既定 65535                                          # R-116
```
challenge ACK は3攻撃とも同一形式 <SEQ=SND.NXT><ACK=RCV.NXT><CTL=ACK> (5961:404,483)。
比較は全て modulo 2^32。

## 4. 処理順序 (9293 §3.10.7.4, 固定)
1 sequence check → 2 RST → 3 security → 4 SYN → 5 ACK field(5961 data injection 含む)
→ 6 URG → 7 text → 8 FIN。順序が安全性に効く。

## 5. 安全性不変条件 (INV)

- INV-001 送信窓単調: 常に SND.UNA =< SND.NXT。UNA は acceptable ack でのみ前進。[9293:898-915]
- INV-002 acceptable ack 範囲: UNA 更新 ACK は SND.UNA<SEG.ACK=<SND.NXT。[9293:912]
- INV-003 受信窓左端: RCV.NXT は処理済み分だけ前進、後退しない。[9293:3852]
- INV-004 受理データのみ user へ: 窓外データは渡らない。[9293:934-968]
- INV-005 RST 厳格化: 同期で SEG.SEQ=RCV.NXT の RST のみ reset。[5961:383-419]
- INV-006 SYN で reset 不可: 同期で SYN は challenge ACK のみ。[5961:479]
- INV-007 ACK 受理範囲: (SND.UNA-MAX.SND.WND)=<SEG.ACK=<SND.NXT。[5961:593]
- INV-008 窓更新単調: SND.WL1<SEG.SEQ or (=& WL2=<ACK) のみ。[9293:3760]
- INV-009 SYN/FIN seq 一意消費: 各1 seq, 再送でも一度だけ act。[9293:979]
- INV-010 checksum 健全性: 不一致 seg は状態機械に到達しない。[9293:458]
- INV-011 TIME-WAIT linger: active close は TCB 削除前 2*MSL 待つ。[9293:1653]
- INV-012 ISS 単調(再incarnation): 新 ISS>前最大 seq。[9293:1658]
- INV-013 challenge ACK レート上限。[5961:657]
- INV-014 passive/active 由来追跡: 遷移先(LISTEN vs CLOSED)が由来に依存。[9293:1308]
- INV-015 RCV.WND=0 例外: RST/URG/ACK は処理。[9293:972]
- INV-016 非同期 unacceptable ACK → RST。[9293:1479]

## 6. スコープ注記
- security/compartment, Diffserv, URG は処理順序のため no-op 素通し(R 番号振らず)。
- 輻輳制御(slow start)・SWS/Nagle・zero-window probe は別レイヤ、INV 非対象(R-092/093 に最小限)。

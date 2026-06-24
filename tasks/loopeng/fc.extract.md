# 抽出台帳: フロー制御 (受信窓更新 / zero-window persist / SWS 回避 / Nagle / delayed ACK)

元仕様: `tasks/loopeng/req-flowcontrol.md` (R-FC-xxx / INV-FC-xxx)、RFC 9293 §3.8.6 (SWS/persist/Nagle)、RFC 1122 §4.2.3 (delayed ACK)。
スコープ (YAGNI): フロー制御の **安全性 + 活性 (デッドロック回避)** に絞る。
keepalive (R-FC-061..066) は窓制御のループから独立した別タイマ機構なので、この spec では対象外
(状態遷移として窓制御の活性に絡まない。INV-FC-013 既定 OFF は静的設定で通常テスト領分)。
RTT/RTO の実数秒、seq 空間の wrap、MSS option ネゴ詳細は対象外 (別 spec / 通常テスト)。

抽象化方針:
- SND.UNA / SND.NXT は小整数。送信データ量は `D` (アプリ書込で増える未送信量) として抽象。
- sndWnd = 相手の広告窓 (0 含む小整数)。rcvWnd = 自分の広告窓。
- rcvBuf 消費は rcvUser (アプリ未読量)、rcvBuff は固定容量。RCV.NXT は受信済み境界。
- タイマは boolean フラグ + 「発火アクション」で表現 (実数秒は段階で抽象)。
  persistArmed / overrideArmed / delAckArmed。
- Nagle 状態は「未確認データ有無」= (SND.NXT > SND.UNA)。
- MSS は CONSTANT。Fr=1/2 (受信 SWS)、Fs=1/2 (送信 SWS) は閾値 = MSS の半分相当を整数で。

## 台帳 (走査アンカー = 各 R-FC / INV)

### 受信窓の動的更新 (窓を縮めない)
- [x] S-001 R-FC-001 / RFC9293§3.8.6.2.1 (SHLD-14, MUST-34)「右窓端を左へ動かさない」
      → The system SHALL NOT move the right window edge (RCV.NXT+RCV.WND) to the left. (ubiquitous/安全)
      → INV-FC-001: 右窓端 単調非減少。
- [x] S-002 R-FC-007「RCV.NXT 前進時も RCV.NXT+RCV.WND を一定に保つ」
      → WHEN RCV.NXT advances on receive the system SHALL keep RCV.NXT+RCV.WND constant (shrink RCV.WND). (event)
      → 受信は窓を消費する: RCV.NXT += k なら RCV.WND -= k で右端固定。
- [x] S-003 R-FC-006 / INV-FC-014 / RFC9293 (window update 規則)「窓更新採用は SND.UNA<=SEG.ACK<=SND.NXT の最大 ACK のみ」
      → WHEN an ACK segment arrives the system SHALL update SND.WND only if SND.UNA <= SEG.ACK <= SND.NXT and SEG.ACK is the largest ACK seen. (event)
      → IF an ACK is older (SEG.ACK < largest seen) THEN the system SHALL NOT overwrite SND.WND with its window. (unwanted)
      → 古い小窓で上書きしない (reorder 耐性)。

### SWS 回避 受信側
- [x] S-004 R-FC-012 / INV-FC-006 / RFC9293§3.8.6.2.2 (MUST-39)「reduction >= min(Fr*RCV.BUFF, Eff.snd.MSS) まで窓固定」
      → WHILE the window increase is below min(Fr*RCV.BUFF, MSS) the system SHALL keep advertising the old (small) window, i.e. SHALL NOT advertise the tiny increase. (state)
      → IF a window opening is smaller than the SWS threshold THEN the system SHALL advertise zero increase (advertise old right edge). (unwanted)
      → INV-FC-006: 小窓 (閾値未満の増加) を広告しない。

### SWS 回避 送信側 (usable U = SND.UNA+SND.WND-SND.NXT)
- [x] S-005 R-FC-023 / RFC9293§3.8.6.2.1「送る条件」(下記いずれか)
      → WHEN data is queued the system SHALL send a segment IF min(D,U) >= MSS. (event, full segment)
      → WHEN data is queued the system SHALL send IF (SND.NXT=SND.UNA AND PUSH set AND D<=U). (event, idle push)
      → WHEN data is queued the system SHALL send IF (SND.NXT=SND.UNA AND min(D,U) >= Fs*Max(SND.WND)). (event, half max window)
      → IF none of the send conditions hold THEN the system SHALL withhold the segment (avoid SWS). (unwanted)
- [x] S-006 R-FC-024 / INV-FC-009「override timeout 0.1〜1.0s で活性確保」
      → WHEN the send-SWS / override timer fires the system SHALL send the queued data even if below threshold. (event)
      → INV-FC-009: override でいつか送る (送信側 SWS デッドロック回避)。

### Nagle
- [x] S-007 R-FC-030 / INV-FC-008 / RFC9293§3.7.4 (Nagle)「未確認データ中はフル未満を送らない」
      → WHILE there is unacknowledged data in flight (SND.NXT > SND.UNA) the system SHALL NOT send a sub-MSS segment. (state)
      → IF data < MSS AND unacked data outstanding THEN the system SHALL buffer it until ACK arrives or a full segment accumulates. (unwanted)
      → INV-FC-008: Nagle 中はフル未満送らない。
- [x] S-008 R-FC-032「無効化手段必須 (TCP_NODELAY)」
      → WHERE TCP_NODELAY is set the system SHALL send sub-MSS data immediately. (optional)
      → 注: 静的設定オプション。デッドロック検査には Nagle ON 経路のみで十分。実装フラグとして記録、モデルは ON 経路を検査。

### Zero-window probe / persist
- [x] S-009 R-FC-040 / INV-FC-002 / RFC9293§3.8.6.1 (MUST-36)「窓0でも persist で >=1 octet probe」
      → WHILE the peer window is zero the system SHALL keep sending a >=1 octet probe (persist). (state)
      → INV-FC-002: 窓0継続中 probe を停止しない。
      → LIVE-FC-1: 窓0でも probe を送り続け、窓再開でいつか送信が進む (zero-window デッドロック回避)。
- [x] S-010 R-FC-044 / RFC9293§3.8.6.1「RTO後初回, 指数バックオフ」
      → WHEN the persist timer fires the system SHALL send a probe and back off the timer exponentially (capped). (event)
      → persist timer は RTO 後に初回起動、満了ごとにバックオフ段階を倍化 (上限飽和)。
- [x] S-011 R-FC-043「受信側は窓0でも RCV.NXT+窓(0) の ACK を返す」
      → WHEN a zero-window probe arrives the system SHALL reply with an ACK carrying RCV.NXT and current (possibly zero) window. (event)
      → probe 受信で ACK を返す。窓が再開していれば WindowUpdate として相手に伝わる。

### Delayed ACK (RFC 1122 §4.2.3.2)
- [x] S-012 R-FC-051 / INV-FC-004 / RFC1122 (delayed ACK <0.5s 必須)
      → WHILE an ACK is delayed the system SHALL send it within 0.5s. (state)
      → INV-FC-004: delayed ACK 遅延 < 500ms。抽象: delAck タイマは必ず発火 (WF)。
- [x] S-013 R-FC-052 / INV-FC-005「2 フルセグ毎に1 ACK」
      → WHILE full segments arrive the system SHALL send an ACK at least every second full segment. (state)
      → INV-FC-005: 未 ACK フルセグメントは高々 2 (2 個目までに ACK)。
- [x] S-014 R-FC-053「out-of-order/gap 埋めは即 ACK」
      → IF an out-of-order segment or a gap-filling segment arrives THEN the system SHALL send an ACK immediately (no delay). (unwanted/即時)
      → 注: gap/reorder は本 spec の窓抽象では境界外 (seq 空間詳細)。delayed ACK の上限規律 (S-013) と <0.5s (S-012) を主検査。即時 ACK 経路は実装フラグとして記録。

### Keepalive (スコープ外、記録のみ)
- [x] S-015 R-FC-061/063/064/066 / INV-FC-013「keepalive 既定 OFF, 間隔>=2h, 単一無応答で切らない, probe seq=SND.NXT-1」
      → WHERE keepalive is enabled the system SHALL ... (optional)
      → スコープ外: 窓制御の活性ループに絡まない独立タイマ。実装側で通常テスト。本 spec では検査しない (未検査と明示)。

### 活性 (本命: デッドロック回避)
- [x] S-016 LIVE-FC-1 (persist)「窓0でも persist を送り続け、窓再開でいつか送信が進む」
      → temporal: 窓0が続いても (公平な persist 発火 + いつか WindowUpdate で) <>(送信が進む)。
- [x] S-017 LIVE-FC-2 (override/SWS)「送信側 SWS で止まっても override timeout でいつか送る」
      → temporal: queued data があり閾値未満で止まっても (公平な override 発火で) <>(送信が進む)。
- [x] S-018 LIVE-FC-3 (Nagle + delayed ACK 相互作用)「双方が待つデッドロックが起きないか」
      → temporal: Nagle で送信を溜め、受信が ACK を遅延しても、override / delAck タイマ発火で <>(送信が進む)。
      → このデッドロックを実際に TLC で検出できるか試す。起きるなら override/delAck がどう解くか反例で示す。

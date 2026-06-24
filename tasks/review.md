# goal 後半: 甘さレビュー結果 (3観点)

複数観点で裏が取れた critical を優先。レビュー: 実装正しさ / テスト設計 / 形式検証。

## 最優先 critical (実害バグ、複数観点で裏付け)

- [x] BUG-1 SYN-RECEIVED が AcceptableAck 不成立でも ESTABLISHED 昇格 (statemachine.go advanceStateOnAck)
      → handleAck の広い 5961 範囲だけで昇格。AcceptableAck 成立を要求すべき。RFC9293 R-040。
- [x] BUG-2 受信窓 uint16 + wscale=7 で 64KB 超バッファで窓破壊 (tcb.go rcv.wnd, flowcontrol.go)
      → rcvVars.wnd を uint32 化、出力時のみ右シフト。テストレビューも「スケール適用未テスト」と裏付け。
- [x] BUG-3 dispatch が IPv4 TotalLength 無視 → パディング混入で checksum 誤判定 (recvloop.go, listener.go demux, ipv4.go)
      → TotalLength を検証し segment = pkt[IHL*4 : TotalLength]。
- [x] BUG-4 CLOSED 接続がテーブル未回収 → 4-tuple 再利用不能 + goroutine リーク (conntable.go, statemachine.go, listener.go)
      → TIME-WAIT満了/LAST-ACK完了/RST で remove。ロック順 ct.mu→c.mu を守る (逆順デッドロック注意)。

## 重要 major (実害)
- [x] BUG-5 TIME-WAIT 置換に ISS ゲート無し (conntable.go insertIfAbsent) RFC9293 R-061
- [x] BUG-6 TIME-WAIT で FIN 再送に応答できない (in-order ガードで潰れる) R-060
- [x] BUG-7 fast retransmit 後 RTO 未リセット → 二重送出 (statemachine.go fastRetransmit)
- [x] BUG-8 Listener.Close が accept チャネル閉じず Accept 永久ブロック (listener.go)
- [x] BUG-9 非同期状態 (LISTEN/SYN-SENT) の CLOSE 未実装 (statemachine.go Close)

## 形式検証の甘さ (未検証 critical)
- [x] FV-1 out-of-order 再組立ての健全性が TLA+/Lean どちらも未検証 (data.go の核心ロジック)
      → Lean で「順不同・重複・トリムで元ストリームのプレフィックスに一致、二重取り込み無し」を証明 → テスト配線。
- [x] FV-2 cwnd 増加則 (SS 指数/CA byte-counting) が TLA+ SMSS=1 抽象で潰れている
      → Lean で byte-counting の数式不変条件を証明 (CA 1RTT<=SMSS, カウンタ非負/リセット)。
- [x] FV-3 Lean seqLT (msb) と Go SeqLT (距離<2^31) が対蹠点で別関数 (橋渡し健全性)
      → Lean に Go 一致定義を置き、窓<2^31 仮説下で 2 モデル一致を証明。
- [x] FV-4 (任意) Audit.lean に Rto/Paws/WScale/Mux を追加 (隠れ公理監査の網)

## テスト設計の甘さ (漏れ)
- [x] T-1 window scale のスケール演算の実適用が未テスト (保存値しか見ていない) → 入出力シフトの振る舞いテスト
- [x] T-2 SYN/SYN-ACK の window 非スケール未テスト
- [x] T-3 ACK 受理範囲 (5961 data injection) 下端・境界・wrap が未テスト (上端1点のみ)
- [x] T-4 受信窓右端境界 + acceptable() 4ケース (RCV.WND=0 系) 未テスト
- [x] T-5 RST 境界 NXT+1/上端ちょうど/上端直前 未テスト
- [x] T-6 MSS/SACK-Permitted の SYN 限定 否定テスト無し
- [x] T-7 parse 系 (header/options/ipv4) に Go fuzz target
- [x] T-8 flaky リスク: 「来ないこと」を実時間判定する demux テスト、e2e 固定ポート

## 実装と要件の乖離 (YAGNI 判断して明記 or 実装)
- SACK 受信処理 (生成/選択再送/RTOクリア) 未実装。req-options に明記 or 最小実装。
- keepalive 未実装。req-flowcontrol に明記 (YAGNI 妥当、既定 OFF だけ示す)。

## 修正方針
1. 実害バグ BUG-1〜9 を先に直す (テスト追加込み、TDD で各 Red→Green)。
2. FV-1〜3 を prover で固め、Go テストに橋渡し。
3. テスト漏れ T-1〜8 を埋める。
4. SACK/keepalive は YAGNI 明記 (or 最小)。


## 完了サマリ (goal 後半)
全項目対応済み。commit:
- BUG-1〜9 実害バグ修正: 549902c
- FV-1〜3 (out-of-order/cwnd/seqLT 橋渡し) Lean 96定理 + Go property: d56e18e
- テスト漏れ T-1〜8 (wscale適用/ACK・RST境界/acceptable 4ケース/SYN限定/fuzz/flaky低減): 6cd48c1
- SACK ブロック生成 + keepalive (折衝のみ→機能化): 89babdb
最終: just check (race x5) + e2e 全 PASS、本体 ~3854 LOC、235 テスト/fuzz。
SACK 送信側選択再送は明記の上スコープ外 (受信側広告は実装)。

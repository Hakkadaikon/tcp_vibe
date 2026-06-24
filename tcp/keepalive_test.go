package tcp

import (
	"testing"
	"time"
)

const testKAIdle = 10 * time.Second

// collectSegs は peer を閉じて溜まった送出セグメントのヘッダを全て返す
// (非ブロッキング)。Close するので 1 テストにつき最後に 1 回だけ呼ぶ。
func collectSegs(t *testing.T, peer Link) []TCPHeader {
	t.Helper()
	peer.Close()
	var out []TCPHeader
	for {
		pkt, err := peer.ReadPacket()
		if err != nil {
			return out
		}
		ip, err := ParseIPv4Header(pkt)
		if err != nil {
			continue
		}
		h, err := ParseTCPHeader(pkt[int(ip.IHL)*4:])
		if err != nil {
			continue
		}
		out = append(out, h)
	}
}

// 既定 OFF: SetKeepAlive しなければ idle が長くても probe を送らない (RFC 1122 MUST)。
func TestKeepAliveDefaultOff(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	fc.advance(2 * time.Hour)
	c.Tick()
	if segs := collectSegs(t, peer); len(segs) != 0 {
		t.Fatalf("既定では keepalive probe を送ってはいけない: %v", segs)
	}
}

// ON + idle 超過 → probe 送出 (seq=SND.NXT-1, データ無し)。idle 直前では送らない。
func TestKeepAliveProbeOnIdle(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	c.SetKeepAlive(true, testKAIdle)
	fc.advance(testKAIdle - time.Nanosecond)
	c.Tick() // idle 未満: 送らない
	fc.advance(time.Nanosecond)
	c.Tick() // idle ちょうど: 送る
	segs := collectSegs(t, peer)
	if len(segs) != 1 {
		t.Fatalf("probe が 1 個だけ出るはず: got %d", len(segs))
	}
	if segs[0].SeqNum != c.SndNxt()-1 {
		t.Fatalf("probe の seq が SND.NXT-1 でない: got %d want %d", segs[0].SeqNum, c.SndNxt()-1)
	}
	if !segs[0].Flags.Has(FlagACK) {
		t.Fatal("probe は ACK を立てるべき")
	}
}

// probe に ACK → 接続維持、idle タイマがリセットされ再び idle まで送らない。
func TestKeepAliveAckResetsIdle(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	c.SetKeepAlive(true, testKAIdle)
	fc.advance(testKAIdle)
	c.Tick() // probe 1
	// 相手が ACK を返す → idle リセット。
	c.onSegment(TCPHeader{Flags: Flags(FlagACK), SeqNum: 5001, AckNum: 1001, Window: maxWindow}, nil)
	// idle 未満しか進めない → 追加 probe は出ない。
	fc.advance(testKAIdle - time.Nanosecond)
	c.Tick()
	if c.State() != Established {
		t.Fatalf("接続が維持されていない: %v", c.State())
	}
	// 出たのは最初の probe 1 個だけ (ACK 後は出ていない)。
	if segs := collectSegs(t, peer); len(segs) != 1 {
		t.Fatalf("ACK 後に余分な probe が出た: got %d", len(segs))
	}
}

// 単一 probe 無応答では切断しない (RFC 1122 MUST、ロスト ACK 耐性)。
func TestKeepAliveSingleProbeNoDisconnect(t *testing.T) {
	c, _, fc := establishedConn(t, maxWindow)
	c.SetKeepAlive(true, testKAIdle)
	fc.advance(testKAIdle)
	c.Tick() // probe 1, 応答なし
	if c.State() != Established {
		t.Fatalf("単一 probe 無応答で切断した: %v", c.State())
	}
}

// probe 回数上限到達で接続を閉じる (実装定義の上限)。
func TestKeepAliveMaxProbesCloses(t *testing.T) {
	c, _, fc := establishedConn(t, maxWindow)
	c.SetKeepAlive(true, testKAIdle)
	for i := 0; i <= keepaliveMaxProbes; i++ {
		fc.advance(testKAIdle)
		c.Tick()
	}
	if c.State() != Closed {
		t.Fatalf("probe 上限超過でも閉じない: %v", c.State())
	}
}

// 未確認データがある間は keepalive probe を送らない (idle でない)。
func TestKeepAliveNotIdleWithUnackedData(t *testing.T) {
	c, peer, fc := establishedConn(t, maxWindow)
	c.SetKeepAlive(true, testKAIdle)
	if _, err := c.Send([]byte("data")); err != nil {
		t.Fatalf("Send 失敗: %v", err)
	}
	fc.advance(testKAIdle)
	c.Tick()
	// 未確認データありなので keepalive probe (seq=SND.NXT-1) は出ない。
	for _, h := range collectSegs(t, peer) {
		if h.SeqNum == c.SndNxt()-1 && len(h.Options) == 0 {
			t.Fatalf("未確認データ中に keepalive probe を送った: seq=%d", h.SeqNum)
		}
	}
}

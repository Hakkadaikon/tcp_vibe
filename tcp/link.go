package tcp

import (
	"errors"
	"sync"
	"time"
)

// Link は IP パケット単位の双方向リンク層。状態機械はこの口を通して
// IP パケットを送受信する。実装はメモリ仮想リンク (テスト) や、権限が
// 取れる環境での AF_PACKET / TUN ドライバ (本番) に差し替えられる。
type Link interface {
	// WritePacket は 1 つの IP パケットを送る。
	WritePacket(pkt []byte) error
	// ReadPacket は 1 つの IP パケットを受け取る。閉じられたら ErrLinkClosed。
	ReadPacket() ([]byte, error)
	// Close はリンクを閉じる。冪等。
	Close() error
}

// ErrLinkClosed は閉じたリンクへの操作で返る。
var ErrLinkClosed = errors.New("tcp: link closed")

// Clock は時刻取得の seam。既定は time.Now、テストは fake clock を注入する。
// 再送・TIME-WAIT の境界を決定論的に検証するために分離する。
type Clock func() time.Time

// SystemClock は実時刻を返す既定の Clock。
func SystemClock() time.Time { return time.Now() }

// pipeLink はメモリ上でパケットを運ぶ Link。NewPipeLink で対になって生成され、
// 一方の WritePacket が他方の ReadPacket に届く。
// ponytail: 順序入替・欠落の注入は今は無し。TLA+ 側で敵対環境を網羅検査するので、
// 必要になったら decorator で挟む。
type pipeLink struct {
	mu     sync.Mutex
	cond   *sync.Cond
	inbox  [][]byte // 自分宛に届いたパケット
	closed bool
	peer   *pipeLink
}

// NewPipeLink は双方向に繋がった 2 つの Link を返す。
func NewPipeLink() (Link, Link) {
	a := &pipeLink{}
	b := &pipeLink{}
	a.cond = sync.NewCond(&a.mu)
	b.cond = sync.NewCond(&b.mu)
	a.peer = b
	b.peer = a
	return a, b
}

func (l *pipeLink) WritePacket(pkt []byte) error {
	l.mu.Lock()
	closed := l.closed
	l.mu.Unlock()
	if closed {
		return ErrLinkClosed
	}
	// パケットはコピーして相手の inbox に積む (呼び出し側のバッファ再利用に耐える)。
	cp := make([]byte, len(pkt))
	copy(cp, pkt)
	p := l.peer
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrLinkClosed
	}
	p.inbox = append(p.inbox, cp)
	p.cond.Signal()
	return nil
}

func (l *pipeLink) ReadPacket() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.inbox) == 0 && !l.closed {
		l.cond.Wait()
	}
	if len(l.inbox) == 0 { // closed かつ空
		return nil, ErrLinkClosed
	}
	pkt := l.inbox[0]
	l.inbox = l.inbox[1:]
	return pkt, nil
}

func (l *pipeLink) Close() error {
	l.mu.Lock()
	l.closed = true
	l.cond.Broadcast()
	l.mu.Unlock()
	// 双方向リンクの片側が閉じたら相手も終端する。待機中の Read を起こして
	// (inbox が空なら) ErrLinkClosed を返させる。
	if p := l.peer; p != nil {
		p.mu.Lock()
		p.closed = true
		p.cond.Broadcast()
		p.mu.Unlock()
	}
	return nil
}

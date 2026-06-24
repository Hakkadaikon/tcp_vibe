//go:build linux

package link

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
)

// ランデブーサーバ (STUN ライク) は UDP hole punching の足場。NAT 内の 2 端は
// 互いの「NAT 通過後に外から見えるグローバル IP:ポート」を直接は知れない。
// そこで両端が同じ session ID でこのサーバへ登録すると、サーバは各登録パケットの
// 送信元アドレス (= NAT 通過後にサーバから見えた見え方 = グローバルアドレス) を
// 記録し、2 端が揃ったら互いのアドレスを相手へ返す。これが STUN の本質
// (アドレスを変換するのは NAT で、サーバは「どう見えたか」を教えるだけ)。
//
// プロトコルは最小のテキスト 1 行:
//   登録 (client -> server): "REG <sessionID>"
//   応答 (server -> client): "PEER <ip>:<port>"  (相手のグローバルアドレス)
// session ID ごとに先着 2 端をペアにし、2 端目が来た時点で両端へ PEER を返す。
//
// ponytail: ペアは 2 端固定・無期限保持の最小実装。同 ID に 3 端目が来たら
// エラー応答するだけ。TTL/GC が要るのは長時間動かす本番だけ。

const (
	regPrefix  = "REG "
	peerPrefix = "PEER "
	regBufSize = 256
)

// Rendezvous は session ID ごとに 2 端のグローバルアドレスをペアリングする UDP サーバ。
type Rendezvous struct {
	fd int

	mu       sync.Mutex
	sessions map[string]*syscall.SockaddrInet4 // 先着 1 端目のアドレスを保持
	closed   bool
}

// NewRendezvous は :port に UDP ソケットを束ねたサーバを開く。port=0 で自動割当。
func NewRendezvous(port uint16) (*Rendezvous, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, err
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Port: int(port)}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &Rendezvous{fd: fd, sessions: map[string]*syscall.SockaddrInet4{}}, nil
}

// LocalPort は実際に束ねたポートを返す (port=0 の自動割当確認用)。
func (r *Rendezvous) LocalPort() (uint16, error) {
	sa, err := syscall.Getsockname(r.fd)
	if err != nil {
		return 0, err
	}
	in4, ok := sa.(*syscall.SockaddrInet4)
	if !ok {
		return 0, errors.New("rendezvous: getsockname が IPv4 でない")
	}
	return uint16(in4.Port), nil
}

// Serve は登録パケットを受け続け、2 端揃ったペアへ相手アドレスを返す。Close まで
// ブロックするので goroutine で起動する。Close 由来のエラーは nil で正常終了する。
func (r *Rendezvous) Serve() error {
	buf := make([]byte, regBufSize)
	for {
		r.mu.Lock()
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return nil
		}
		n, from, err := syscall.Recvfrom(r.fd, buf, 0)
		if err != nil {
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.EINVAL) {
				return nil
			}
			return err
		}
		in4, ok := from.(*syscall.SockaddrInet4)
		if !ok {
			continue
		}
		r.handle(string(buf[:n]), in4)
	}
}

// handle は 1 登録パケットを処理する。session ID で先着とペアにし、揃ったら両端へ
// 互いのアドレスを返す。
func (r *Rendezvous) handle(msg string, from *syscall.SockaddrInet4) {
	if !strings.HasPrefix(msg, regPrefix) {
		return // 不明なパケットは黙って捨てる
	}
	sid := strings.TrimSpace(strings.TrimPrefix(msg, regPrefix))
	if sid == "" {
		return
	}
	r.mu.Lock()
	first, ok := r.sessions[sid]
	if !ok {
		// 1 端目。相手待ち。アドレスを記録するだけ (まだ返さない)。
		r.sessions[sid] = from
		r.mu.Unlock()
		network.Debugf("rendezvous: session %q 1端目 %s:%d 登録", sid, network.IPStr(from.Addr), from.Port)
		return
	}
	if first.Addr == from.Addr && first.Port == from.Port {
		// 同一端からの再送 (UDP の欠落対策で client は登録を繰り返す)。
		// 自分自身とペアにしないよう無視する。
		r.mu.Unlock()
		network.Debugf("rendezvous: session %q 1端目の再送を無視 %s:%d", sid, network.IPStr(from.Addr), from.Port)
		return
	}
	// 2 端目が揃った。ペアを消費し、両端へ相手アドレスを返す。
	delete(r.sessions, sid)
	r.mu.Unlock()
	network.Debugf("rendezvous: session %q 2端目 %s:%d。ペア成立", sid, network.IPStr(from.Addr), from.Port)
	r.reply(first, from) // 1 端目へ 2 端目のアドレス
	r.reply(from, first) // 2 端目へ 1 端目のアドレス
}

// reply は to へ peer のグローバルアドレスを通知する。
func (r *Rendezvous) reply(to, peer *syscall.SockaddrInet4) {
	msg := fmt.Sprintf("%s%s:%d", peerPrefix, network.IPStr(peer.Addr), peer.Port)
	_ = syscall.Sendto(r.fd, []byte(msg), 0, to)
}

// Close はサーバを閉じる。冪等。ブロック中の Recvfrom を起こす。
func (r *Rendezvous) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	_ = syscall.Shutdown(r.fd, syscall.SHUT_RDWR)
	return syscall.Close(r.fd)
}

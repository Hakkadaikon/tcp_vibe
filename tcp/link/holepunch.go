//go:build linux

package link

import "github.com/hakkadaikon/tcp_vibe/tcp/network"

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// UDP hole punching による NAT 越え。WireGuard / WebRTC と同じ手口を、自作 TCP を
// UDP の土管に包んでいる利点を活かして実装する。
//
// 手順:
//  1. ローカル UDP ソケットを開く (この 1 つのソケットでランデブー連絡も punch も
//     データ転送もこなす。NAT のマッピングを 1 つに保つため終始同じソケットを使う)。
//  2. ランデブーサーバへ "REG <sessionID>" を送る。NAT 通過後にサーバから見えた
//     送信元 (= グローバル IP:ポート) がサーバに記録される。
//  3. サーバから相手のグローバル IP:ポート ("PEER <ip>:<port>") を受け取る。
//  4. 相手のグローバルアドレスへ punch パケットを連射する。相手も同時に同じことを
//     する。先に送った punch は相手 NAT に弾かれても、自分 NAT には「相手宛に送った」
//     マッピングが開く。両者が送り合うことで双方向の穴が開く。
//  5. 相手からの punch を 1 つでも受けたら確立。そのソケットを Link として返す。
//
// 限界 (正直に明記): hole punching は full-cone / restricted-cone / port-restricted
// NAT には効くが、対称 NAT (宛先ごとに別ポートを割り当てる NAT) では相手から見える
// ポートが事前に予測できず効きにくい。その場合は TURN 的なリレーが要る (本実装には
// 無い)。また localhost 検証は NAT が無いので「手順が正しく流れること」の実証であり、
// 実 NAT の通過可否は環境依存。

const (
	punchMarker  = "PUNCH" // punch パケットの識別子 (IPv4 ヘッダ 0x4_ と衝突しない)
	punchPayload = punchMarker
)

// ErrPunchTimeout は相手の punch を時間内に受けられなかったときに返る。
var ErrPunchTimeout = errors.New("tcp: hole punch がタイムアウト (相手不在/NAT が穴を開けられない)")

// isPunchPacket は受信データが punch パケットかを判定する (udplink のフィルタが使う)。
func isPunchPacket(b []byte) bool {
	return bytes.HasPrefix(b, []byte(punchMarker))
}

// DialHolePunch はランデブーサーバ経由で相手のアドレスを学習し、UDP hole punching で
// 直接 UDP 通信を確立して、その土管を Link として返す。両端が同じ sessionID で
// (ほぼ同時に) 呼ぶ。確立できなければ ErrPunchTimeout 等を返す。
//
// localPort=0 で OS 自動割当。timeout は punch フェーズ全体の上限。
func DialHolePunch(rendezvousIP [4]byte, rendezvousPort uint16, sessionID string, localPort uint16, timeout time.Duration) (Link, error) {
	link, err := NewUDPLinkPunch(localPort)
	if err != nil {
		return nil, err
	}
	fd := link.fd

	peerIP, peerPort, err := registerAndWaitPeer(fd, rendezvousIP, rendezvousPort, sessionID, timeout)
	if err != nil {
		link.Close()
		return nil, err
	}
	network.Debugf("holepunch: peer learned %s:%d", network.IPStr(peerIP), peerPort)

	// 相手のグローバルアドレスを宛先に固定し、双方向に穴を開ける。
	if err := punchUntilEstablished(fd, peerIP, peerPort, timeout); err != nil {
		link.Close()
		return nil, err
	}
	// punch フェーズで設定した受信タイムアウトを解除し、Link としてブロッキング
	// 受信に戻す (udpLink.ReadPacket は無期限ブロックを前提にしている)。
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{})
	link.setRemote(peerIP, peerPort)
	network.Debugf("holepunch: established with %s:%d", network.IPStr(peerIP), peerPort)
	return link, nil
}

// registerAndWaitPeer はランデブーサーバへ登録し、相手アドレスの通知を待つ。
// サーバ応答が来るまで登録を一定間隔で再送する (UDP なので登録パケットの欠落に備える)。
func registerAndWaitPeer(fd int, srvIP [4]byte, srvPort uint16, sessionID string, timeout time.Duration) ([4]byte, uint16, error) {
	srv := &syscall.SockaddrInet4{Port: int(srvPort), Addr: srvIP}
	reg := []byte(regPrefix + sessionID)

	// 受信は短いタイムアウトでポーリングし、その間に登録を再送する。
	tv := syscall.Timeval{Sec: 0, Usec: 200000}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	deadline := time.Now().Add(timeout)
	buf := make([]byte, regBufSize)
	for time.Now().Before(deadline) {
		if err := syscall.Sendto(fd, reg, 0, srv); err != nil {
			return [4]byte{}, 0, err
		}
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if isTimeoutErr(err) || errors.Is(err, syscall.EINTR) {
				continue // まだ相手が揃っていない。再送して待つ。
			}
			return [4]byte{}, 0, err
		}
		ip, port, ok := parsePeer(string(buf[:n]))
		if ok {
			return ip, port, nil
		}
		// PEER 以外 (相手の早すぎる punch 等) は無視して待ち続ける。
	}
	return [4]byte{}, 0, ErrPunchTimeout
}

// punchUntilEstablished は相手へ punch を連射しつつ、相手からの punch を待つ。
// 相手の punch を 1 つでも受けたら確立とみなす。
func punchUntilEstablished(fd int, peerIP [4]byte, peerPort uint16, timeout time.Duration) error {
	peer := &syscall.SockaddrInet4{Port: int(peerPort), Addr: peerIP}
	tv := syscall.Timeval{Sec: 0, Usec: 100000}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	deadline := time.Now().Add(timeout)
	buf := make([]byte, regBufSize)
	for time.Now().Before(deadline) {
		// punch を撃つ (相手 NAT に弾かれても自分側のマッピングが開く)。
		_ = syscall.Sendto(fd, []byte(punchPayload), 0, peer)
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if isTimeoutErr(err) || errors.Is(err, syscall.EINTR) {
				continue
			}
			return err
		}
		// 相手 (= peer) からの punch なら確立。送信元の検証で別ホストの紛れ込みを弾く。
		if isPunchPacket(buf[:n]) {
			if in4, ok := from.(*syscall.SockaddrInet4); ok &&
				in4.Addr == peerIP && uint16(in4.Port) == peerPort {
				// 相手が punch を受け取りそびれないよう、確立後にもう数発撃っておく。
				for i := 0; i < 3; i++ {
					_ = syscall.Sendto(fd, []byte(punchPayload), 0, peer)
				}
				return nil
			}
		}
	}
	return ErrPunchTimeout
}

// parsePeer は "PEER <ip>:<port>" を解釈する。
func parsePeer(msg string) ([4]byte, uint16, bool) {
	if !strings.HasPrefix(msg, peerPrefix) {
		return [4]byte{}, 0, false
	}
	addr := strings.TrimSpace(strings.TrimPrefix(msg, peerPrefix))
	host, portStr, ok := strings.Cut(addr, ":")
	if !ok {
		return [4]byte{}, 0, false
	}
	ip, ok := parseDottedIPv4(host)
	if !ok {
		return [4]byte{}, 0, false
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return [4]byte{}, 0, false
	}
	return ip, uint16(port), true
}

// parseDottedIPv4 は "a.b.c.d" を [4]byte にする (net 非依存・最小実装)。
func parseDottedIPv4(s string) ([4]byte, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return [4]byte{}, false
	}
	var ip [4]byte
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 10, 8)
		if err != nil {
			return [4]byte{}, false
		}
		ip[i] = byte(v)
	}
	return ip, true
}

// isTimeoutErr は SO_RCVTIMEO による受信タイムアウト (EAGAIN/EWOULDBLOCK) を判定する。
func isTimeoutErr(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK)
}

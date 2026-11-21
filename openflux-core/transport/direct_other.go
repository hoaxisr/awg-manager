//go:build !linux && !android

package transport

import (
	"net"
	"time"
)

// setDeadPeerTimeout is Linux's TCP_USER_TIMEOUT; elsewhere the keepalive
// alone finds a dead peer.
func setDeadPeerTimeout(net.Conn, time.Duration) {}

// carrierSendBuffer is the send buffer given to the carrier socket where there
// is no TCP_NOTSENT_LOWAT. Windows sizes it from the receiver's window and
// the first round trips; on a path with a long round trip (72 ms to a VPS) a
// connection often settled at a small window for good and uploaded at 8-40
// Mbit/s where the same path carried 60-70 with a 4 MiB buffer.
const carrierSendBuffer = 4 << 20

// setNotSentLowat is Linux's TCP_NOTSENT_LOWAT; elsewhere the send buffer is
// raised instead (see carrierSendBuffer).
func setNotSentLowat(c net.Conn, _ int) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetWriteBuffer(carrierSendBuffer)
	}
}

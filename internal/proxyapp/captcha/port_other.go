//go:build !linux

package captcha

import (
	"net"
	"strconv"
	"time"
)

const dialProbe = 200 * time.Millisecond

// socketListenerPIDAmong is only implemented on Linux (production routers).
// Dev builds on Windows/macOS fall back to a dial probe without PID attribution.
func socketListenerPIDAmong(host string, port int, _ []int) (int, bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), dialProbe)
	if err != nil {
		return 0, false
	}
	_ = conn.Close()
	return 0, true
}

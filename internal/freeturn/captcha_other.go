//go:build !linux

package freeturn

import (
	"net"
	"strconv"
	"time"
)

const captchaDialProbe = 200 * time.Millisecond

// socketListenerPID is only implemented on Linux (production routers).
// Dev builds on Windows/macOS fall back to a dial probe without PID attribution.
func socketListenerPID(host string, port int) (int, bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), captchaDialProbe)
	if err != nil {
		return 0, false
	}
	_ = conn.Close()
	return 0, true
}

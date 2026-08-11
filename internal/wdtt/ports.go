package wdtt

import (
	"net"
	"strconv"
	"strings"
)

// defaultRawListenAddr picks host from DTLS listen and port = DTLS + 1 (qWDTT 1.4 convention).
func defaultRawListenAddr(dtlsListen string) string {
	dtlsListen = strings.TrimSpace(dtlsListen)
	if dtlsListen == "" {
		return "0.0.0.0:56003"
	}
	host, port, err := net.SplitHostPort(dtlsListen)
	if err != nil {
		return "0.0.0.0:56003"
	}
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p >= 65535 {
		return net.JoinHostPort(host, "56003")
	}
	return net.JoinHostPort(host, strconv.Itoa(p+1))
}

// EffectiveRawListen returns -listen-raw address (explicit or DTLS+1).
func (c ServerConfig) EffectiveRawListen() string {
	if addr := strings.TrimSpace(c.RawListen); addr != "" {
		return addr
	}
	return defaultRawListenAddr(c.Listen)
}

// EffectiveDirectListen returns -listen-direct (explicit only).
func (c ServerConfig) EffectiveDirectListen() string {
	return strings.TrimSpace(c.DirectListen)
}

// LinkListenPort is the WAN port clients should use in peer (Direct/DTLS or Raw).
// mode: wg → direct or DTLS; raw → -listen-raw.
func (c ServerConfig) LinkListenPort() int {
	return c.LinkListenPortForMode(c.RelayMode)
}

func (c ServerConfig) LinkListenPortForMode(mode string) int {
	if normalizeConnMode(mode) == ConnModeRaw {
		addr := c.EffectiveRawListen()
		if p, err := listenPort(addr); err == nil {
			return p
		}
		return 56003
	}
	addr := c.Listen
	if direct := c.EffectiveDirectListen(); direct != "" {
		addr = direct
	}
	if p, err := listenPort(addr); err == nil {
		return p
	}
	return 56002
}

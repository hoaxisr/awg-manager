package procport

import (
	"fmt"
	"strings"
)

// Proto is the socket protocol for listener lookup.
type Proto string

const (
	ProtoTCP Proto = "tcp"
	ProtoUDP Proto = "udp"
)

// ListenerInfo describes a bound local socket.
type ListenerInfo struct {
	Open  bool   `json:"open"`
	PID   int    `json:"pid,omitempty"`
	Comm  string `json:"comm,omitempty"`
	Proto string `json:"proto"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
}

func NormalizeProto(raw string) Proto {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "tcp":
		return ProtoTCP
	default:
		return ProtoUDP
	}
}

// ParseListenHostPort splits "host:port" or ":port" (defaultHost used when host empty).
func ParseListenHostPort(addr, defaultHost string) (host string, port int, ok bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0, false
	}
	host = defaultHost
	if strings.HasPrefix(addr, ":") {
		addr = addr[1:]
	} else if h, p, found := strings.Cut(addr, ":"); found {
		if strings.Contains(h, ":") {
			if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
				host = h[1 : len(h)-1]
			} else {
				host = h
			}
		} else {
			host = h
		}
		addr = p
	}
	if host == "" {
		host = defaultHost
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	n, err := parsePort(addr)
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return host, n, true
}

func parsePort(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errInvalidPort
	}
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errInvalidPort
		}
		n = n*10 + int(c-'0')
		if n > 65535 {
			return 0, errInvalidPort
		}
	}
	return n, nil
}

var errInvalidPort = errorString("invalid port")

type errorString string

func (e errorString) Error() string { return string(e) }

// EnrichBindError appends PID/comm when lastError looks like a bind conflict.
func EnrichBindError(lastError, listen string, proto Proto) string {
	if lastError == "" || !strings.Contains(strings.ToLower(lastError), "address already in use") {
		return lastError
	}
	host, port, ok := ParseListenHostPort(listen, "127.0.0.1")
	if !ok {
		return lastError
	}
	info, err := LookupListener(host, port, proto)
	if err != nil || !info.Open || info.PID <= 0 {
		return lastError
	}
	suffix := fmt.Sprintf(" (порт занят PID %d", info.PID)
	if info.Comm != "" {
		suffix += ", " + info.Comm
	}
	return lastError + suffix + ")"
}

package freeturn

import (
	"net"
	"strconv"
	"strings"
)

// LocalListenPortChecker reports localhost listen ports already claimed outside
// freeturn (e.g. AWG tunnel peer endpoints on 127.0.0.1:PORT).
type LocalListenPortChecker interface {
	OccupiedLocalListenPorts(excludeWdttClientID, excludeFreeTurnClientID string) (map[int]bool, error)
}

func isLocalHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func localListenPort(addr string) (int, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, false
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	if !isLocalHost(host) {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// LocalListenPort parses host:port and returns the port when host is localhost.
func LocalListenPort(addr string) (int, bool) {
	return localListenPort(addr)
}

func mergeListenPorts(maps ...map[int]bool) map[int]bool {
	out := map[int]bool{}
	for _, m := range maps {
		for port := range m {
			out[port] = true
		}
	}
	return out
}

func clientListenPorts(clients []ClientInstance) map[int]bool {
	used := map[int]bool{}
	for _, c := range clients {
		if port, err := listenPort(c.Config.Listen); err == nil {
			used[port] = true
		}
	}
	return used
}

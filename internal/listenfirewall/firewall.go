package listenfirewall

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const Comment = "AWGM_PROXY_LISTEN"

// PortSpec is one UDP/TCP listen port that should stay open in INPUT.
type PortSpec struct {
	Port  int
	Proto string // "udp" or "tcp"
}

// OpenEnabled treats nil as true (default on for legacy configs).
func OpenEnabled(open *bool) bool {
	if open == nil {
		return true
	}
	return *open
}

// WANListenPort returns the port when listen binds all interfaces (0.0.0.0 / ::).
// Localhost-only binds do not need a WAN firewall rule.
func WANListenPort(listen string) (int, bool) {
	addr := strings.TrimSpace(listen)
	if addr == "" {
		return 0, false
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	host = strings.TrimSpace(host)
	if host != "" && host != "0.0.0.0" && host != "::" && host != "[::]" {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// MergePortSpecs deduplicates by port+proto.
func MergePortSpecs(in []PortSpec) []PortSpec {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]PortSpec, 0, len(in))
	for _, spec := range in {
		proto := strings.ToLower(strings.TrimSpace(spec.Proto))
		if proto == "" {
			proto = "udp"
		}
		if spec.Port <= 0 {
			continue
		}
		key := fmt.Sprintf("%s:%d", proto, spec.Port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, PortSpec{Port: spec.Port, Proto: proto})
	}
	return out
}

package router

import (
	"sort"
	"strings"
)

// parseDefaultIfaces extracts all interface names appearing as `dev X`
// in `default ...` rows of `ip route show table all` output. Returns
// sorted, deduplicated list. Empty input yields empty slice.
func parseDefaultIfaces(routeOutput string) []string {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(routeOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "default ") {
			continue
		}
		// Tokens: ["default", "dev", "X", ...]
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				seen[fields[i+1]] = struct{}{}
				break
			}
		}
	}
	out := make([]string, 0, len(seen))
	for iface := range seen {
		out = append(out, iface)
	}
	sort.Strings(out)
	return out
}

// parseInetAddrs extracts IPv4 addresses from `ip -4 addr show dev X`
// output. Each address is normalized to "X.X.X.X/32". The peer field
// (point-to-point partner address) is deliberately ignored: it's the
// remote end's address, not ours, and bypassing it would silently
// route policy-bound traffic past TPROXY.
func parseInetAddrs(addrOutput string) []string {
	var out []string
	for _, line := range strings.Split(addrOutput, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "inet ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// fields[1] is either "X.X.X.X" or "X.X.X.X/N".
		addr := fields[1]
		if idx := strings.IndexByte(addr, '/'); idx >= 0 {
			addr = addr[:idx]
		}
		if addr == "" {
			continue
		}
		out = append(out, addr+"/32")
	}
	return out
}

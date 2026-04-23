package diagnostics

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// anonymize replaces sensitive data in the report with deterministic aliases.
// Same real value maps to the same alias within a single report (preserves correlation).
func anonymize(report *Report) {
	a := newAnonymizer()

	// Phase 1: Register all known sensitive values
	a.registerFromReport(report)

	// Phase 2: Walk the entire report and replace all occurrences
	data, err := json.Marshal(report)
	if err != nil {
		return
	}

	result := string(data)
	// Replace longer values first to avoid partial matches
	for _, r := range a.sortedReplacements() {
		result = strings.ReplaceAll(result, r.original, r.alias)
	}

	_ = json.Unmarshal([]byte(result), report)
}

type replacement struct {
	original string
	alias    string
}

type anonymizer struct {
	ips       map[string]string // real IP -> alias
	keys      map[string]string // real key -> alias
	hosts     map[string]string // real hostname -> alias
	ipCount   int
	epCount   int
	keyCount  int
	hostCount int
}

func newAnonymizer() *anonymizer {
	return &anonymizer{
		ips:   make(map[string]string),
		keys:  make(map[string]string),
		hosts: make(map[string]string),
	}
}

func (a *anonymizer) registerIP(ip string) {
	if ip == "" || a.ips[ip] != "" {
		return
	}
	if isPrivateIP(ip) {
		return // Keep private IPs
	}
	a.ipCount++
	a.ips[ip] = fmt.Sprintf("PUBLIC-IP-%d", a.ipCount)
}

func (a *anonymizer) registerEndpoint(ip string) {
	if ip == "" || a.ips[ip] != "" {
		return
	}
	if isPrivateIP(ip) {
		return
	}
	a.epCount++
	a.ips[ip] = fmt.Sprintf("ENDPOINT-%d", a.epCount)
}

func (a *anonymizer) registerKey(key string) {
	if key == "" || key == "[REDACTED]" || a.keys[key] != "" {
		return
	}
	a.keyCount++
	a.keys[key] = fmt.Sprintf("PUBKEY-%d", a.keyCount)
}

func (a *anonymizer) registerHost(host string) {
	if host == "" || a.hosts[host] != "" {
		return
	}
	a.hostCount++
	a.hosts[host] = fmt.Sprintf("HOST-%d", a.hostCount)
}

func (a *anonymizer) registerFromReport(report *Report) {
	for i := range report.Tunnels {
		t := &report.Tunnels[i]

		// Extract endpoint host and IP from "host:port" format
		if host, _, err := net.SplitHostPort(extractEndpointFromConfig(t.ConfigFile)); err == nil {
			if net.ParseIP(host) != nil {
				a.registerEndpoint(host)
			} else {
				a.registerHost(host)
			}
		}

		// Public keys from config
		for _, line := range strings.Split(t.ConfigFile, "\n") {
			if strings.HasPrefix(line, "PublicKey = ") {
				key := strings.TrimPrefix(line, "PublicKey = ")
				a.registerKey(strings.TrimSpace(key))
			}
		}

		// Scan Connection.RawOutput for public IPs (NativeWG NDMS output may contain them)
		a.registerPublicIPsFromOutput(t.Connection.RawOutput)

		// Scan ProxyInfo fields
		if t.Proxy != nil {
			a.registerPublicIPsFromOutput(t.Proxy.RawListEntry)
			a.registerIP(t.Proxy.ActualRouteVia)
		}
	}

	// Register public IPs found in ip route / ip addr output
	a.registerPublicIPsFromOutput(report.WAN.IPRouteTable)
	a.registerPublicIPsFromOutput(report.WAN.IPAddr)
}

func (a *anonymizer) registerPublicIPsFromOutput(output string) {
	for _, word := range strings.Fields(output) {
		// Strip /prefix if present
		ipStr := strings.Split(word, "/")[0]
		if ip := net.ParseIP(ipStr); ip != nil {
			a.registerIP(ipStr)
		}
	}
}

func (a *anonymizer) sortedReplacements() []replacement {
	var result []replacement
	for orig, alias := range a.ips {
		result = append(result, replacement{orig, alias})
	}
	for orig, alias := range a.keys {
		result = append(result, replacement{orig, alias})
	}
	for orig, alias := range a.hosts {
		result = append(result, replacement{orig, alias})
	}
	// Sort by length descending (longer first to avoid partial matches)
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if len(result[j].original) > len(result[i].original) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

func extractEndpointFromConfig(config string) string {
	for _, line := range strings.Split(config, "\n") {
		if strings.HasPrefix(line, "Endpoint = ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Endpoint = "))
		}
	}
	return ""
}

func isPrivateIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	// RFC 1918 + link-local + loopback
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"127.0.0.0/8",
		"fc00::/7",  // IPv6 ULA
		"fe80::/10", // IPv6 link-local
		"::1/128",   // IPv6 loopback
	}
	for _, cidr := range privateRanges {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

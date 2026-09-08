package diagnostics

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

var (
	macAddressPattern = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(:[0-9a-f]{2}){5}\b`)
	// WG public keys are 43 base64 chars + "=" padding (44 chars total).
	// Leading \b anchors at a word boundary; trailing = is not a \w char
	// so \b after = never matches in RE2 — omit trailing boundary.
	wgPublicKeyPattern = regexp.MustCompile(`\b[A-Za-z0-9+/]{43}=`)
	// Domain-like public hostnames that may appear in diagnostics logs
	// (sing-box server names, SNI, connectivity checks, dial targets).
	// Requires at least one dot and an alphabetic TLD to avoid matching
	// IP addresses, versions, tunnel IDs, and local interface names.
	hostnamePattern = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}\b`)

	// Technical dotted names are common in diagnostics output but are not
	// public hostnames/SNI/server names. Preserve them for report usefulness.
	technicalDottedNameSuffixes = map[string]struct{}{
		"service": {},
		"socket":  {},
		"target":  {},
		"timer":   {},
		"mount":   {},
		"path":    {},
		"slice":   {},
		"scope":   {},
		"conf":    {},
		"json":    {},
		"log":     {},
		"yaml":    {},
		"yml":     {},
		"toml":    {},
		"go":      {},
		"svelte":  {},
		"js":      {},
		"ts":      {},
		"db":      {},
		"srs":     {},
	}
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
	macs      map[string]string // real MAC -> masked MAC
	wgKeys    map[string]string // real WireGuard public key -> masked key
	ipCount   int
	epCount   int
	keyCount  int
	hostCount int
	macCount  int
	wgCount   int
}

func newAnonymizer() *anonymizer {
	return &anonymizer{
		ips:    make(map[string]string),
		keys:   make(map[string]string),
		hosts:  make(map[string]string),
		macs:   make(map[string]string),
		wgKeys: make(map[string]string),
	}
}

func isNonSensitiveSpecialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsUnspecified() {
		return true // 0.0.0.0, ::
	}
	if ip.Equal(net.IPv4bcast) {
		return true // 255.255.255.255
	}
	return false
}

func (a *anonymizer) registerIP(ip string) {
	if ip == "" || a.ips[ip] != "" {
		return
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return
	}
	if isNonSensitiveSpecialIP(parsed) {
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
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return
	}
	if isNonSensitiveSpecialIP(parsed) {
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
	host = strings.TrimSpace(strings.Trim(host, "."))
	if host == "" {
		return
	}

	canonical := strings.ToLower(host)

	if alias := a.hosts[host]; alias != "" {
		return
	}
	if alias := a.hosts[canonical]; alias != "" {
		a.hosts[host] = alias
		return
	}

	a.hostCount++
	alias := fmt.Sprintf("HOST-%d", a.hostCount)

	// Store both canonical and original spellings. This makes aliases stable
	// for Example.COM/example.com while still replacing the exact spelling
	// that appeared in the JSON report.
	a.hosts[canonical] = alias
	a.hosts[host] = alias
}

func (a *anonymizer) registerHostsFromOutput(output string) {
	for _, host := range hostnamePattern.FindAllString(output, -1) {
		host = strings.TrimSpace(host)
		host = strings.Trim(host, ".")
		if host == "" {
			continue
		}
		// Defensive guard: the regexp should not match IPs, but keep this
		// check so future regexp edits do not start aliasing IPs as HOST-*.
		if net.ParseIP(host) != nil {
			continue
		}
		if isTechnicalDottedName(host) {
			continue
		}
		a.registerHost(host)
	}
}

func isTechnicalDottedName(host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.Trim(host, ".")))
	if host == "" {
		return false
	}

	parts := strings.Split(host, ".")
	if len(parts) != 2 {
		return false
	}

	suffix := parts[1]
	_, ok := technicalDottedNameSuffixes[suffix]
	return ok
}

func maskMAC(mac string) string {
	parts := strings.Split(strings.ToLower(mac), ":")
	if len(parts) != 6 {
		return "MAC-**:**:**"
	}
	return fmt.Sprintf("%s:%s:**:**:**:%s", parts[0], parts[1], parts[5])
}

func maskWGKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 12 {
		return "WGKEY-****"
	}
	return key[:6] + "****" + key[len(key)-6:]
}

func (a *anonymizer) registerMAC(mac string) {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" || a.macs[mac] != "" {
		return
	}
	a.macCount++
	a.macs[mac] = maskMAC(mac)
}

func (a *anonymizer) registerWGKey(key string) {
	key = strings.TrimSpace(key)
	if key == "" || key == "[REDACTED]" || a.wgKeys[key] != "" {
		return
	}
	a.wgCount++
	a.wgKeys[key] = maskWGKey(key)
}

func (a *anonymizer) registerMACsFromOutput(output string) {
	for _, mac := range macAddressPattern.FindAllString(output, -1) {
		a.registerMAC(mac)
	}
}

func (a *anonymizer) registerWGKeysFromOutput(output string) {
	for _, key := range wgPublicKeyPattern.FindAllString(output, -1) {
		a.registerWGKey(key)
	}
}

func (a *anonymizer) registerSensitiveFromLogEntry(entry logging.LogEntry) {
	a.registerPublicIPsFromOutput(entry.Target)
	a.registerPublicIPsFromOutput(entry.Message)
	a.registerHostsFromOutput(entry.Target)
	a.registerHostsFromOutput(entry.Message)
	a.registerMACsFromOutput(entry.Target)
	a.registerMACsFromOutput(entry.Message)
	a.registerWGKeysFromOutput(entry.Target)
	a.registerWGKeysFromOutput(entry.Message)
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

		// Scan free-text tunnel snapshots for MAC addresses and WireGuard public keys.
		a.registerMACsFromOutput(t.Interface.NDMSState)
		a.registerMACsFromOutput(t.Connection.RawOutput)
		a.registerMACsFromOutput(t.ConfigFile)

		a.registerWGKeysFromOutput(t.Interface.NDMSState)
		a.registerWGKeysFromOutput(t.Connection.RawOutput)
		a.registerWGKeysFromOutput(t.ConfigFile)

		a.registerHostsFromOutput(t.Interface.NDMSState)
		a.registerHostsFromOutput(t.Connection.RawOutput)
		a.registerHostsFromOutput(t.ConfigFile)

		// Scan Connection.RawOutput for public IPs (NativeWG NDMS output may contain them)
		a.registerPublicIPsFromOutput(t.Connection.RawOutput)

		// Scan ProxyInfo fields
		if t.Proxy != nil {
			a.registerPublicIPsFromOutput(t.Proxy.RawListEntry)
			a.registerHostsFromOutput(t.Proxy.RawListEntry)
			a.registerIP(t.Proxy.ActualRouteVia)
		}

		// Structured tunnel fields may contain public IPs, MACs, or WG keys too.
		a.registerPublicIPsFromOutput(t.Interface.KernelAddr)
		a.registerPublicIPsFromOutput(t.Interface.KernelIPv6)
		a.registerPublicIPsFromOutput(t.Routes.EndpointRoute)
		a.registerPublicIPsFromOutput(t.Routes.DefaultRoute)
		a.registerPublicIPsFromOutput(t.Settings.DNS)
		a.registerPublicIPsFromOutput(strings.Join(t.Settings.AllowedIPs, " "))

		a.registerHostsFromOutput(t.Interface.KernelAddr)
		a.registerHostsFromOutput(t.Interface.KernelIPv6)
		a.registerHostsFromOutput(t.Routes.EndpointRoute)
		a.registerHostsFromOutput(t.Routes.DefaultRoute)
		a.registerHostsFromOutput(t.Settings.DNS)

		a.registerMACsFromOutput(t.Interface.KernelAddr)
		a.registerMACsFromOutput(t.Interface.KernelIPv6)
		a.registerMACsFromOutput(t.Routes.EndpointRoute)
		a.registerMACsFromOutput(t.Routes.DefaultRoute)
		a.registerMACsFromOutput(t.Settings.DNS)

		a.registerWGKeysFromOutput(t.Interface.KernelAddr)
		a.registerWGKeysFromOutput(t.Interface.KernelIPv6)
		a.registerWGKeysFromOutput(t.Routes.EndpointRoute)
		a.registerWGKeysFromOutput(t.Routes.DefaultRoute)
		a.registerWGKeysFromOutput(t.Settings.DNS)

		if t.Settings.PingCheckConfig != nil {
			a.registerPublicIPsFromOutput(t.Settings.PingCheckConfig.Target)
			a.registerHostsFromOutput(t.Settings.PingCheckConfig.Target)
			a.registerMACsFromOutput(t.Settings.PingCheckConfig.Target)
			a.registerWGKeysFromOutput(t.Settings.PingCheckConfig.Target)
		}
	}

	// Register sensitive values found in WAN route/address outputs.
	a.registerPublicIPsFromOutput(report.WAN.NDMSRouteTable)
	a.registerPublicIPsFromOutput(report.WAN.IPRouteTable)
	a.registerPublicIPsFromOutput(report.WAN.IPAddr)

	a.registerHostsFromOutput(report.WAN.NDMSRouteTable)
	a.registerHostsFromOutput(report.WAN.IPRouteTable)
	a.registerHostsFromOutput(report.WAN.IPAddr)

	a.registerMACsFromOutput(report.WAN.NDMSRouteTable)
	a.registerMACsFromOutput(report.WAN.IPRouteTable)
	a.registerMACsFromOutput(report.WAN.IPAddr)

	a.registerWGKeysFromOutput(report.WAN.NDMSRouteTable)
	a.registerWGKeysFromOutput(report.WAN.IPRouteTable)
	a.registerWGKeysFromOutput(report.WAN.IPAddr)

	// AWGProxyModule fields are free-text and may contain endpoint IPs.

	a.registerPublicIPsFromOutput(report.AWGProxyModule.RawList)
	for _, line := range report.AWGProxyModule.DmesgLines {
		a.registerPublicIPsFromOutput(line)
	}
	a.registerHostsFromOutput(report.AWGProxyModule.RawList)
	for _, line := range report.AWGProxyModule.DmesgLines {
		a.registerHostsFromOutput(line)
	}
	// Scan AWGProxyModule for MACs and WG keys.
	a.registerMACsFromOutput(report.AWGProxyModule.RawList)
	a.registerWGKeysFromOutput(report.AWGProxyModule.RawList)
	for _, line := range report.AWGProxyModule.DmesgLines {
		a.registerMACsFromOutput(line)
		a.registerWGKeysFromOutput(line)
	}

	// Sing-box config may contain server UUIDs, REALITY keys, short IDs, hostnames.
	if report.SingboxConfig != nil && report.SingboxConfig.Config != nil {
		if b, err := json.Marshal(report.SingboxConfig.Config); err == nil {
			raw := string(b)
			a.registerPublicIPsFromOutput(raw)
			a.registerHostsFromOutput(raw)
			a.registerMACsFromOutput(raw)
			a.registerWGKeysFromOutput(raw)
		}
	}

	// JournalWarnings contains app and sing-box WARN/ERROR entries.
	// These entries may contain public IPs, hostnames, MACs, or WireGuard public keys.
	if report.JournalWarnings != nil {
		for _, entry := range report.JournalWarnings.AWGM.Entries {
			a.registerSensitiveFromLogEntry(entry)
		}
		for _, entry := range report.JournalWarnings.Singbox.Entries {
			a.registerSensitiveFromLogEntry(entry)
		}
	}

	// Test results may contain public IPs, hostnames, MACs, or WG keys.
	for _, test := range report.Tests {
		a.registerPublicIPsFromOutput(test.Detail)
		a.registerPublicIPsFromOutput(test.Description)
		a.registerPublicIPsFromOutput(test.TunnelID)
		a.registerPublicIPsFromOutput(test.TunnelName)

		a.registerHostsFromOutput(test.Detail)
		a.registerHostsFromOutput(test.Description)
		a.registerHostsFromOutput(test.TunnelID)
		a.registerHostsFromOutput(test.TunnelName)

		a.registerMACsFromOutput(test.Detail)
		a.registerMACsFromOutput(test.Description)
		a.registerMACsFromOutput(test.TunnelID)
		a.registerMACsFromOutput(test.TunnelName)

		a.registerWGKeysFromOutput(test.Detail)
		a.registerWGKeysFromOutput(test.Description)
		a.registerWGKeysFromOutput(test.TunnelID)
		a.registerWGKeysFromOutput(test.TunnelName)
	}
}

func (a *anonymizer) registerPublicIPsFromOutput(output string) {
	for _, word := range strings.Fields(output) {
		// Strip /prefix if present (CIDR notation)
		ipStr := strings.Split(word, "/")[0]
		// Strip :port if present (host:port notation)
		if host, _, err := net.SplitHostPort(ipStr); err == nil {
			ipStr = host
		}
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
	for orig, alias := range a.macs {
		result = append(result, replacement{orig, alias})
	}
	for orig, alias := range a.wgKeys {
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

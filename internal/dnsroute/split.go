package dnsroute

import (
	"net"
	"strings"
)

// splitDomainsAndSubnets separates a raw user-provided list into DNS-style
// domains (including geosite: tags) and network-style subnets (CIDR and
// geoip: tags). Order is preserved within each output slice.
//
// Classification:
//   - "geosite:TAG"       → domains
//   - "geoip:TAG"         → subnets
//   - valid CIDR          → subnets (IPv4 and IPv6)
//   - everything else     → domains (incl. bare IPs without /mask)
func splitDomainsAndSubnets(input []string) (domains, subnets []string) {
	for _, raw := range input {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "geoip:") {
			subnets = append(subnets, s)
			continue
		}
		if strings.HasPrefix(s, "geosite:") {
			domains = append(domains, s)
			continue
		}
		if _, _, err := net.ParseCIDR(s); err == nil {
			subnets = append(subnets, s)
			continue
		}
		domains = append(domains, s)
	}
	return domains, subnets
}

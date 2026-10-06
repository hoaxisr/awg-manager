// Package ipfamily tells IPv6 entries of a domain list apart from the rest.
// It is a leaf package so the router sync (internal/dnsroute) and
// explain_route (internal/mcp, which must stay free of daemon dependencies)
// share one rule instead of two copies that could drift.
package ipfamily

import (
	"net/netip"
	"strings"
)

// IsIPv6 reports whether a list entry is an IPv6 network or address: a CIDR
// such as 2001:db8::/32, or a bare address such as 2001:db8::1 — bare IPs
// land among the domains of a list. IPv4-mapped forms (::ffff:10.0.0.0/104,
// ::ffff:1.2.3.4) count as IPv6: that is how they are written, and how they
// reach the router. Tags and domain names are never IPv6.
func IsIPv6(entry string) bool {
	s := strings.TrimSpace(entry)
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr().Is6()
	}
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is6()
}

// WithoutIPv6 returns entries minus the IPv6 ones, keeping their order.
func WithoutIPv6(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !IsIPv6(e) {
			out = append(out, e)
		}
	}
	return out
}

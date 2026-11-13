// Package domain maps an observed hostname to its safe registered zone.
package domain

import (
	"net"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// Zone returns the registered domain used by candidates and domain_suffix.
// Exact SNI remains available separately for the active probe. If no safe
// registered zone exists, the exact hostname is the narrowest fallback.
func Zone(host string) string {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}
	zone, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || zone == "" {
		return host
	}
	return zone
}

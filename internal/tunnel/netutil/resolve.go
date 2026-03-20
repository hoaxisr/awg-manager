package netutil

import (
	"fmt"
	"net"
)

// ResolveEndpointIP extracts or resolves IP from endpoint string (host:port).
func ResolveEndpointIP(endpoint string) (string, error) {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		// Try as host without port
		host = endpoint
	}

	// Check if it's already an IP
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}

	// Resolve hostname
	addrs, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}

	// Prefer IPv4
	for _, addr := range addrs {
		if addr.To4() != nil {
			return addr.String(), nil
		}
	}

	if len(addrs) > 0 {
		return addrs[0].String(), nil
	}

	return "", fmt.Errorf("no IP found for %s", host)
}

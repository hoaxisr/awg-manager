package netutil

import (
	"fmt"
	"net"
	"strconv"
)

// preferIPv4 picks the first IPv4 address from a list.
// Falls back to the first address (IPv6) if no IPv4 found.
// Returns nil for empty input.
func preferIPv4(addrs []net.IP) net.IP {
	for _, addr := range addrs {
		if addr.To4() != nil {
			return addr
		}
	}
	if len(addrs) > 0 {
		return addrs[0]
	}
	return nil
}

// ResolveHost resolves a hostname to a single IP address, preferring IPv4.
// If host is already an IP literal, returns it directly.
func ResolveHost(host string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("empty host")
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}

	addrs, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}

	ip := preferIPv4(addrs)
	if ip == nil {
		return "", fmt.Errorf("no IP found for %s", host)
	}
	return ip.String(), nil
}

// ResolveEndpoint parses "host:port" and resolves hostname to IP, preferring IPv4.
// Accepts IPv4 ("1.2.3.4:51820"), IPv6 ("[::1]:51820"), and hostname ("vpn.example.com:51820").
// Port must be in range 1-65535.
func ResolveEndpoint(endpoint string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, fmt.Errorf("split endpoint %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}
	if port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("port %d out of range 1-65535", port)
	}

	ip, err := ResolveHost(host)
	if err != nil {
		return "", 0, err
	}
	return ip, port, nil
}

// LookupAllIPs resolves a hostname to all IP addresses (both A and AAAA).
// If host is already an IP literal, returns a single-element slice.
// Used for diagnostics where all DNS records should be visible.
func LookupAllIPs(host string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}

	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}

	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no IPs for %s", host)
	}

	result := make([]string, len(addrs))
	for i, addr := range addrs {
		result[i] = addr.String()
	}
	return result, nil
}

// ResolveEndpointIP extracts or resolves IP from endpoint string (host:port).
// Wrapper over ResolveEndpoint for callsites that don't need port.
// NOTE: unlike the previous version, this no longer falls back to treating
// the input as a bare host when SplitHostPort fails. All callers pass
// stored.Peer.Endpoint which is always "host:port" format.
func ResolveEndpointIP(endpoint string) (string, error) {
	ip, _, err := ResolveEndpoint(endpoint)
	return ip, err
}

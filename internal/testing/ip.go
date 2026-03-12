package testing

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// defaultIPCheckServices is the built-in list of IP detection services.
var defaultIPCheckServices = []IPCheckService{
	{Label: "2ip", URL: "https://2ip.ru"},
	{Label: "wtfismyip", URL: "https://wtfismyip.com/text"},
	{Label: "ipinfo", URL: "https://ipinfo.io/ip"},
}

const (
	directIPTimeout   = 10 * time.Second
	vpnIPTimeout      = 20 * time.Second
	perServiceTimeout = 4
)

// GetIPCheckServices returns the list of available IP check services.
func (s *Service) GetIPCheckServices() []IPCheckService {
	return defaultIPCheckServices
}

// CheckIP tests if traffic goes through tunnel by comparing direct and VPN IPs.
// If serviceURL is non-empty, only that service is used (no fallback).
func (s *Service) CheckIP(ctx context.Context, tunnelID string, serviceURL string) (*IPResult, error) {
	if err := s.CheckTunnelRunning(tunnelID); err != nil {
		return nil, err
	}

	// Build direct IP options: bind to WAN interface if known
	var directOpts []string
	if wanIface := s.GetWANInterface(tunnelID); wanIface != "" {
		directOpts = []string{"--interface", wanIface}
	}

	// Get direct IP (through WAN, bypassing tunnel default route)
	directCtx, directCancel := context.WithTimeout(ctx, directIPTimeout)
	defer directCancel()

	directIP, err := s.fetchIPAuto(directCtx, serviceURL, directOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to get WAN IP: %w", err)
	}

	// Get VPN IP (through tunnel)
	curlOpts, err := s.GetCurlOptions(tunnelID)
	if err != nil {
		return nil, err
	}

	vpnCtx, vpnCancel := context.WithTimeout(ctx, vpnIPTimeout)
	defer vpnCancel()

	vpnIP, err := s.fetchIPAuto(vpnCtx, serviceURL, curlOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to get IP through tunnel: %w", err)
	}

	endpointIP := s.GetEndpointIP(tunnelID)

	return &IPResult{
		DirectIP:   directIP,
		VpnIP:      vpnIP,
		EndpointIP: endpointIP,
		IPChanged:  directIP != vpnIP,
	}, nil
}

// fetchIPAuto fetches IP using a specific service or falls back through the default list.
func (s *Service) fetchIPAuto(ctx context.Context, serviceURL string, extraCurlOpts []string) (string, error) {
	if serviceURL != "" {
		return fetchIP(ctx, serviceURL, extraCurlOpts)
	}

	var lastErr error
	for _, svc := range defaultIPCheckServices {
		ip, err := fetchIP(ctx, svc.URL, extraCurlOpts)
		if err != nil {
			lastErr = err
			continue
		}
		return ip, nil
	}

	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("all IP services failed")
}

// fetchIP queries a single IP check service.
func fetchIP(ctx context.Context, url string, extraCurlOpts []string) (string, error) {
	args := []string{"-s", "--max-time", fmt.Sprintf("%d", perServiceTimeout)}
	args = append(args, extraCurlOpts...)
	args = append(args, url)

	result, err := exec.Run(ctx, "/opt/bin/curl", args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, exec.FormatError(result, err))
	}

	ip := strings.TrimSpace(result.Stdout)
	if isValidIP(ip) {
		return ip, nil
	}

	return "", fmt.Errorf("%s: invalid response %q", url, truncate(ip, 80))
}

// isValidIP checks if the string is a valid IPv4 or IPv6 address.
func isValidIP(s string) bool {
	return net.ParseIP(s) != nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

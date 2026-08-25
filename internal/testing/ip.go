package testing

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// defaultIPCheckServices is the built-in list of IP detection services.
var defaultIPCheckServices = []IPCheckService{
	{Label: "2ip", URL: "https://2ip.io"},
	{Label: "wtfismyip", URL: "https://wtfismyip.com/text"},
	{Label: "ipinfo", URL: "https://ipinfo.io/ip"},
}

const (
	directIPTimeout   = 10 * time.Second
	vpnIPTimeout      = 20 * time.Second
	perServiceTimeout = 4 * time.Second
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

	// Determine WAN interface for direct (non-VPN) check.
	var wanIface string
	if w := s.GetWANInterface(tunnelID); w != "" {
		wanIface = w
	}

	// Get direct IP (through WAN, bypassing tunnel default route).
	directCtx, directCancel := context.WithTimeout(ctx, directIPTimeout)
	defer directCancel()

	directIP, err := s.fetchIPAuto(directCtx, serviceURL, wanIface)
	if err != nil {
		return nil, fmt.Errorf("failed to get WAN IP: %w", err)
	}

	// Get VPN IP (through tunnel).
	iface, err := s.GetInterfaceName(tunnelID)
	if err != nil {
		return nil, err
	}

	vpnCtx, vpnCancel := context.WithTimeout(ctx, vpnIPTimeout)
	defer vpnCancel()

	vpnIP, err := s.fetchIPAuto(vpnCtx, serviceURL, iface)
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

// fetchIPAuto delegates to the standalone fetchIPAuto function.
func (s *Service) fetchIPAuto(ctx context.Context, serviceURL string, iface string) (string, error) {
	return fetchIPAuto(ctx, serviceURL, iface)
}

// fetchIP queries a single IP check service.
func fetchIP(ctx context.Context, url string, iface string) (string, error) {
	res, err := httpclient.DefaultClient.Do(ctx, httpclient.CallConfig{
		URL:       url,
		Interface: iface,
		MaxTime:   perServiceTimeout,
	})
	if err != nil {
		return "", fmt.Errorf("%s: %w", url, err)
	}

	ip := strings.TrimSpace(res.Body)
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

// CheckIPByInterface tests IP through a kernel interface directly.
// Used for system tunnels. Fetches both direct (WAN) and VPN IPs, compares them.
func CheckIPByInterface(ctx context.Context, ifaceName string, serviceURL string) (*IPResult, error) {
	// Get direct IP (without interface binding — through default route).
	directCtx, directCancel := context.WithTimeout(ctx, directIPTimeout)
	defer directCancel()

	directIP, err := fetchIPAuto(directCtx, serviceURL, "")
	if err != nil {
		return nil, fmt.Errorf("failed to get WAN IP: %w", err)
	}

	// Get VPN IP (through the specified interface).
	vpnCtx, vpnCancel := context.WithTimeout(ctx, vpnIPTimeout)
	defer vpnCancel()

	vpnIP, err := fetchIPAuto(vpnCtx, serviceURL, ifaceName)
	if err != nil {
		return nil, fmt.Errorf("failed to get IP through interface: %w", err)
	}

	return &IPResult{
		DirectIP:  directIP,
		VpnIP:     vpnIP,
		IPChanged: directIP != vpnIP,
	}, nil
}

// WANIPFallback returns an IP to use when external IP probes fail.
// Typical implementation reads the default-gateway interface's IPv4
// address from NDMS — not truly "external" if the router is behind
// CGNAT, but accurate for straight PPPoE/DHCP and better than nothing.
type WANIPFallback func(ctx context.Context) (string, error)

// GetWANIPWithFallback tries external probes first. If they all fail
// (DNS down, no internet, upstream block), it calls the fallback and
// returns whatever it produces. Errors from the fallback are surfaced;
// the original external-probe error is only returned if fallback is nil.
func GetWANIPWithFallback(ctx context.Context, fallback WANIPFallback) (string, error) {
	return GetWANIPBound(ctx, "", fallback)
}

// GetWANIPBound probes external IP check services, optionally binding outbound
// traffic to a kernel WAN interface (SO_BINDTODEVICE). When iface is empty,
// the default route is used. On probe failure, fallback (typically the NDMS
// address on the default-gateway interface) is tried.
func GetWANIPBound(ctx context.Context, iface string, fallback WANIPFallback) (string, error) {
	ip, err := fetchIPAuto(ctx, "", iface)
	if err == nil && ip != "" {
		return ip, nil
	}
	if fallback == nil {
		if err != nil {
			return "", err
		}
		return "", fmt.Errorf("empty WAN IP")
	}
	fip, ferr := fallback(ctx)
	if ferr == nil && fip != "" {
		return fip, nil
	}
	if err != nil {
		return "", err
	}
	return "", ferr
}

// fetchIPAuto fetches IP using a specific service or falls back through the default list.
func fetchIPAuto(ctx context.Context, serviceURL string, iface string) (string, error) {
	if serviceURL != "" {
		return fetchIP(ctx, serviceURL, iface)
	}

	var lastErr error
	for _, svc := range defaultIPCheckServices {
		ip, err := fetchIP(ctx, svc.URL, iface)
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

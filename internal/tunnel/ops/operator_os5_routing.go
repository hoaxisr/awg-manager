package ops

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

// endpointWithResolvedIP substitutes a pre-resolved IP into the endpoint string.
// This avoids DNS re-resolution in SetupEndpointRoute, which can fail
// right after tunnel start when awg show has no endpoint yet and
// Go's pure-Go resolver can't resolve the domain on the router.
func endpointWithResolvedIP(endpoint, resolvedIP string) string {
	if resolvedIP == "" {
		return endpoint
	}
	_, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}
	return net.JoinHostPort(resolvedIP, port)
}

// getEndpointIPFromWG gets resolved endpoint IP from awg show.
// WireGuard resolves DNS when establishing connection, so we can get
// the already-resolved IP instead of doing another DNS lookup.
// Falls back to DNS resolve if awg show fails.
func (o *OperatorOS5Impl) getEndpointIPFromWG(ctx context.Context, tunnelID, fallbackEndpoint string) (string, error) {
	names := tunnel.NewNames(tunnelID)

	// Try to get from awg show (already resolved by WireGuard)
	if result, err := o.wg.Show(ctx, names.IfaceName); err == nil && result.Endpoint != "" {
		// Endpoint format is "IP:Port", extract just the IP
		host, _, splitErr := net.SplitHostPort(result.Endpoint)
		if splitErr == nil && host != "" {
			o.logInfo("resolve", tunnelID, "Got endpoint IP from awg show: "+host)
			return host, nil
		}
	}

	// Fallback to DNS resolve
	o.logInfo("resolve", tunnelID, "Falling back to DNS resolve for endpoint")
	return netutil.ResolveEndpointIP(fallbackEndpoint)
}

// SetupEndpointRoute adds a route to the VPN endpoint via kernel device.
// kernelDevice is the kernel interface name (e.g., "eth3") for oif constraint;
// empty string means no constraint (ip route get picks the best route).
// Returns the resolved endpoint IP on success, error on failure.
// Endpoint route failure is always fatal — prevents routing loops.
func (o *OperatorOS5Impl) SetupEndpointRoute(ctx context.Context, tunnelID, endpoint, kernelDevice, ispName string) (string, error) {
	if endpoint == "" {
		return "", nil
	}

	// Get endpoint IP (prefer awg show, fallback to DNS resolve)
	endpointIP, err := o.getEndpointIPFromWG(ctx, tunnelID, endpoint)
	if err != nil {
		o.logWarn("setup_route", tunnelID, "Failed to resolve endpoint: "+err.Error())
		return "", fmt.Errorf("resolve endpoint: %w", err)
	}

	// Resolve route target from kernel routing table.
	// oif constraint ensures we route via the intended WAN device.
	gateway, device, err := o.resolveKernelRouteTarget(ctx, endpointIP, kernelDevice)
	if err != nil {
		o.logWarn("setup_route", tunnelID, "Failed to resolve kernel route: "+err.Error())
		return "", fmt.Errorf("resolve kernel route for %s: %w", endpointIP, err)
	}

	// Build route target: prefer gateway IP, fallback to device (PPPoE/point-to-point).
	routeTarget := gateway
	routeDevice := "" // explicit dev for link-local gateways
	if routeTarget == "" {
		routeTarget = device
	} else if isIPv6LinkLocal(routeTarget) {
		// IPv6 link-local gateway requires explicit device
		routeDevice = device
	}

	if err := o.addKernelHostRoute(ctx, endpointIP, routeTarget, routeDevice); err != nil {
		o.logWarn("setup_route", tunnelID, "Failed to add kernel endpoint route: "+err.Error())
		o.appLogWarn("start", tunnelID, "Маршрут до endpoint "+endpointIP+": "+err.Error())
		return "", fmt.Errorf("add kernel host route to %s: %w", endpointIP, err)
	}

	// Track for cleanup
	o.endpointRoutesMu.Lock()
	o.endpointRoutes[tunnelID] = endpointIP
	o.endpointRoutesMu.Unlock()

	logSuffix := ""
	if ispName != "" {
		logSuffix = " (" + ispName + ")"
	}
	o.logInfo("setup_route", tunnelID, "Added endpoint route to "+endpointIP+" via "+routeTarget+logSuffix)
	o.appLog("start", tunnelID, "Маршрут до endpoint "+endpointIP+" через "+routeTarget+logSuffix)
	return endpointIP, nil
}

// CleanupEndpointRoute removes the endpoint route for a tunnel.
func (o *OperatorOS5Impl) CleanupEndpointRoute(ctx context.Context, tunnelID string) error {
	o.endpointRoutesMu.Lock()
	endpointIP, exists := o.endpointRoutes[tunnelID]
	if exists {
		delete(o.endpointRoutes, tunnelID)
	}
	o.endpointRoutesMu.Unlock()

	// Clear resolved ISP tracking
	o.resolvedISPMu.Lock()
	delete(o.resolvedISP, tunnelID)
	o.resolvedISPMu.Unlock()

	if !exists || endpointIP == "" {
		return nil
	}

	// Check if another tunnel uses the same IP (reference counting)
	o.endpointRoutesMu.RLock()
	stillInUse := false
	for _, ip := range o.endpointRoutes {
		if ip == endpointIP {
			stillInUse = true
			break
		}
	}
	o.endpointRoutesMu.RUnlock()

	if stillInUse {
		o.logInfo("cleanup_route", tunnelID, "IP "+endpointIP+" still in use by another tunnel")
		return nil
	}

	// Remove kernel route + NDMS route (NDMS caches kernel routes but doesn't track their removal)
	o.delKernelHostRoute(ctx, endpointIP)
	_ = o.ndms.RemoveHostRoute(ctx, endpointIP)
	o.logInfo("cleanup_route", tunnelID, "Removed kernel endpoint route to "+endpointIP)
	o.appLog("stop", tunnelID, "Маршрут до endpoint "+endpointIP+" удалён")

	return nil
}

// RestoreEndpointTracking restores endpoint route tracking without creating the route.
// Used on daemon restart for tunnels that are already running.
// Returns the resolved endpoint IP on success, empty string on non-fatal failure.
func (o *OperatorOS5Impl) RestoreEndpointTracking(ctx context.Context, tunnelID, endpoint, ispInterface string) (string, error) {
	if endpoint == "" {
		return "", nil
	}

	// Get endpoint IP (prefer awg show, fallback to DNS resolve)
	endpointIP, err := o.getEndpointIPFromWG(ctx, tunnelID, endpoint)
	if err != nil {
		o.logWarn("restore_tracking", tunnelID, "Failed to resolve endpoint: "+err.Error())
		return "", nil // Non-fatal
	}

	// Add to tracking map (route already exists in system)
	o.endpointRoutesMu.Lock()
	o.endpointRoutes[tunnelID] = endpointIP
	o.endpointRoutesMu.Unlock()

	// Restore resolved ISP for dashboard display
	if ispInterface != "" {
		o.resolvedISPMu.Lock()
		o.resolvedISP[tunnelID] = ispInterface
		o.resolvedISPMu.Unlock()
	}

	o.logInfo("restore_tracking", tunnelID, "Restored endpoint tracking for "+endpointIP)
	return endpointIP, nil
}

// GetTrackedEndpointIP returns the currently tracked endpoint IP for a tunnel.
func (o *OperatorOS5Impl) GetTrackedEndpointIP(tunnelID string) string {
	o.endpointRoutesMu.RLock()
	defer o.endpointRoutesMu.RUnlock()
	return o.endpointRoutes[tunnelID]
}

// === Kernel route helpers (bypass NDMS) ===

// isIPv6 returns true if the given IP string is an IPv6 address.
func isIPv6(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.To4() == nil
}

// addKernelHostRoute adds a host route via ip route command.
// routeTarget is either a gateway IP or a kernel interface name (for tunnels/PPPoE).
// device is optional; required when routeTarget is an IPv6 link-local address (fe80::).
func (o *OperatorOS5Impl) addKernelHostRoute(ctx context.Context, endpointIP, routeTarget, device string) error {
	prefix := "/32"
	ipCmd := "/opt/sbin/ip"
	family := []string{}
	if isIPv6(endpointIP) {
		prefix = "/128"
		family = []string{"-6"}
	}

	// Remove stale first (idempotent)
	args := append([]string{}, family...)
	args = append(args, "route", "del", endpointIP+prefix)
	o.ipRun(ctx, ipCmd, args...)

	if net.ParseIP(routeTarget) != nil {
		// Gateway is an IP — route via gateway
		args = append([]string{}, family...)
		args = append(args, "route", "add", endpointIP+prefix, "via", routeTarget)
		// IPv6 link-local gateways (fe80::) require explicit device
		if device != "" && isIPv6LinkLocal(routeTarget) {
			args = append(args, "dev", device)
		}
		result, err := o.ipRun(ctx, ipCmd, args...)
		if err != nil {
			return fmt.Errorf("ip route add %s%s via %s: %w", endpointIP, prefix, routeTarget, exec.FormatError(result, err))
		}
	} else {
		// Gateway is an interface name (tunnel chaining, PPPoE)
		args = append([]string{}, family...)
		args = append(args, "route", "add", endpointIP+prefix, "dev", routeTarget)
		result, err := o.ipRun(ctx, ipCmd, args...)
		if err != nil {
			return fmt.Errorf("ip route add %s%s dev %s: %w", endpointIP, prefix, routeTarget, exec.FormatError(result, err))
		}
	}
	return nil
}

// isIPv6LinkLocal returns true if the IP is an IPv6 link-local address (fe80::/10).
func isIPv6LinkLocal(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLinkLocalUnicast() && parsed.To4() == nil
}

// delKernelHostRoute removes a host route.
func (o *OperatorOS5Impl) delKernelHostRoute(ctx context.Context, endpointIP string) {
	prefix := "/32"
	family := []string{}
	if isIPv6(endpointIP) {
		prefix = "/128"
		family = []string{"-6"}
	}
	args := append([]string{}, family...)
	args = append(args, "route", "del", endpointIP+prefix)
	o.ipRun(ctx, "/opt/sbin/ip", args...)
}

// resolveKernelRouteTarget determines how the kernel currently routes to dstIP.
// When oifDevice is non-empty, constrains the lookup to that specific interface
// (ip route get <dstIP> oif <device>), ensuring the route uses the intended WAN.
// Returns either a gateway IP (DHCP WANs) or a device name (PPPoE).
func (o *OperatorOS5Impl) resolveKernelRouteTarget(ctx context.Context, dstIP, oifDevice string) (gateway, device string, err error) {
	args := []string{}
	if isIPv6(dstIP) {
		args = append(args, "-6")
	}
	args = append(args, "route", "get", dstIP)
	if oifDevice != "" {
		args = append(args, "oif", oifDevice)
	}
	result, runErr := o.ipRun(ctx, "/opt/sbin/ip", args...)
	if runErr != nil {
		return "", "", fmt.Errorf("ip route get %s: %w", dstIP, exec.FormatError(result, runErr))
	}
	// Output: "1.2.3.4 via 10.0.0.1 dev eth0 src 192.168.1.2"
	// or:     "1.2.3.4 dev ppp0 src 10.64.0.2" (point-to-point)
	// IPv6:   "2a00::1 from :: via fe80::1 dev eth0 src 2a00::2"
	// Format is the same for both families ("via <gw> dev <dev>").
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	for i, f := range fields {
		if f == "via" && i+1 < len(fields) {
			gateway = fields[i+1]
		}
		if f == "dev" && i+1 < len(fields) {
			device = fields[i+1]
		}
	}
	if device == "" {
		return "", "", fmt.Errorf("no device in ip route get output")
	}
	// Safety net: if the resolved device is one of our tunnel interfaces,
	// we'd create a routing loop (endpoint traffic going through the tunnel itself).
	// This can happen if the tunnel is first in Keenetic access policy and oif is empty.
	if strings.HasPrefix(device, "opkgtun") || strings.HasPrefix(device, "awgm") {
		return "", "", fmt.Errorf("routing loop detected: endpoint resolves to tunnel device %s", device)
	}
	return gateway, device, nil
}

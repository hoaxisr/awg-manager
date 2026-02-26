package ndms

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

const (
	defaultTimeout  = 10 * time.Second
	retryCount      = 5
	retryDelay      = 3 * time.Second
)

// ClientImpl is the NDMS client implementation.
type ClientImpl struct {
	timeout    time.Duration
	httpClient *http.Client
}

// New creates a new NDMS client.
func New() *ClientImpl {
	return &ClientImpl{
		timeout:    defaultTimeout,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
}

// NewWithTimeout creates a new NDMS client with custom timeout.
func NewWithTimeout(timeout time.Duration) *ClientImpl {
	return &ClientImpl{
		timeout:    timeout,
		httpClient: &http.Client{Timeout: timeout},
	}
}

// ndmc executes an NDMS command via ndmc -c.
func (c *ClientImpl) ndmc(ctx context.Context, command string) (string, error) {
	result, err := exec.RunWithOptions(ctx, "/bin/ndmc", []string{"-c", command}, exec.Options{
		Timeout: c.timeout,
	})
	if err != nil {
		return "", fmt.Errorf("ndmc %q: %w", command, exec.FormatError(result, err))
	}
	return result.Stdout, nil
}

// ShowInterface returns raw interface data as JSON string (from RCI).
// Consumed by ParseInterfaceInfo which auto-detects JSON vs text format.
func (c *ClientImpl) ShowInterface(ctx context.Context, name string) (string, error) {
	var raw json.RawMessage
	if err := rciGet(ctx, c.httpClient, "/show/interface/"+name, &raw); err != nil {
		return "", err
	}
	return string(raw), nil
}

// CreateOpkgTun creates an OpkgTun interface in NDMS.
func (c *ClientImpl) CreateOpkgTun(ctx context.Context, name, description string) error {
	// Create interface
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s", name)); err != nil {
		return fmt.Errorf("create interface: %w", err)
	}

	// Set description (sanitized)
	safeDesc := sanitizeDescription(description)
	if safeDesc == "" {
		safeDesc = name
	}
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s description %s", name, safeDesc)); err != nil {
		return fmt.Errorf("set description: %w", err)
	}

	// Set security level
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s security-level public", name)); err != nil {
		return fmt.Errorf("set security-level: %w", err)
	}

	// Set IP global
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s ip global auto", name)); err != nil {
		return fmt.Errorf("set ip global: %w", err)
	}

	return nil
}

// DeleteOpkgTun removes an OpkgTun interface from NDMS.
func (c *ClientImpl) DeleteOpkgTun(ctx context.Context, name string) error {
	// Remove default route first (ignore errors)
	_, _ = c.ndmc(ctx, fmt.Sprintf("no ip route default %s", name))

	// Remove interface
	_, _ = c.ndmc(ctx, fmt.Sprintf("no interface %s", name))

	// Save
	_, _ = c.ndmc(ctx, "system configuration save")

	return nil
}

// OpkgTunExists checks if an OpkgTun interface exists in NDMS.
func (c *ClientImpl) OpkgTunExists(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	var info rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/"+name, &info); err != nil {
		return false
	}
	return info.InterfaceName != ""
}

// SetAddress sets the IPv4 address of an interface.
// Address can be in CIDR notation (10.0.0.2/32) or plain IP (10.0.0.2).
// If plain IP, /32 is assumed for point-to-point tunnels.
func (c *ClientImpl) SetAddress(ctx context.Context, name, address string) error {
	// Remove old address first
	_, _ = c.ndmc(ctx, fmt.Sprintf("no interface %s ip address", name))

	// If address doesn't contain CIDR prefix, add /32
	if !strings.Contains(address, "/") {
		address = address + "/32"
	}

	// Set new address (NDMS accepts CIDR notation directly)
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s ip address %s", name, address)); err != nil {
		return err
	}
	return nil
}

// SetIPv6Address sets the IPv6 address of an interface.
func (c *ClientImpl) SetIPv6Address(ctx context.Context, name, address string) error {
	// Remove old addresses first
	_, _ = c.ndmc(ctx, fmt.Sprintf("no interface %s ipv6 address", name))

	// Set new address with /128 prefix
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s ipv6 address %s/128", name, address)); err != nil {
		return err
	}
	return nil
}

// ClearIPv6Address removes all IPv6 addresses from an interface.
func (c *ClientImpl) ClearIPv6Address(ctx context.Context, name string) {
	_, _ = c.ndmc(ctx, fmt.Sprintf("no interface %s ipv6 address", name))
}

// SetMTU sets the MTU of an interface.
func (c *ClientImpl) SetMTU(ctx context.Context, name string, mtu int) error {
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s ip mtu %d", name, mtu)); err != nil {
		return fmt.Errorf("set mtu: %w", err)
	}
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s ip tcp adjust-mss pmtu", name)); err != nil {
		return fmt.Errorf("set tcp adjust-mss: %w", err)
	}
	return nil
}

// SetDescription sets the description of an interface.
func (c *ClientImpl) SetDescription(ctx context.Context, name, description string) error {
	safeDesc := sanitizeDescription(description)
	if safeDesc == "" {
		safeDesc = name
	}
	if _, err := c.ndmc(ctx, fmt.Sprintf("interface %s description %s", name, safeDesc)); err != nil {
		return err
	}
	return nil
}

// InterfaceUp brings an interface up.
func (c *ClientImpl) InterfaceUp(ctx context.Context, name string) error {
	_, err := c.ndmc(ctx, fmt.Sprintf("interface %s up", name))
	return err
}

// InterfaceDown brings an interface down.
func (c *ClientImpl) InterfaceDown(ctx context.Context, name string) error {
	_, err := c.ndmc(ctx, fmt.Sprintf("interface %s down", name))
	return err
}

// IsInterfaceUp checks if an interface is in an active state.
func (c *ClientImpl) IsInterfaceUp(ctx context.Context, name string) bool {
	var info rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/"+name, &info); err != nil {
		return false
	}
	return info.State == "up" || info.State == "connected"
}

// SetDefaultRoute sets the default IPv4 route via an interface.
func (c *ClientImpl) SetDefaultRoute(ctx context.Context, name string) error {
	if _, err := c.ndmc(ctx, fmt.Sprintf("ip route default %s", name)); err != nil {
		return fmt.Errorf("set default route: %w", err)
	}
	return nil
}

// RemoveDefaultRoute removes the default IPv4 route for an interface.
func (c *ClientImpl) RemoveDefaultRoute(ctx context.Context, name string) error {
	if _, err := c.ndmc(ctx, fmt.Sprintf("no ip route default %s", name)); err != nil {
		return fmt.Errorf("remove default route: %w", err)
	}
	return nil
}

// AddHostRoute adds a route to a specific host via a gateway IP.
// Gateway must be an IP address (e.g., "95.31.140.129"), not an interface name.
func (c *ClientImpl) AddHostRoute(ctx context.Context, host, gateway string) error {
	if _, err := c.ndmc(ctx, fmt.Sprintf("ip route %s %s", host, gateway)); err != nil {
		return fmt.Errorf("add host route: %w", err)
	}
	// NOT saving - endpoint routes are temporary
	return nil
}

// RemoveHostRoute removes a host route.
// Detects IPv6 addresses and uses the appropriate NDMS command.
func (c *ClientImpl) RemoveHostRoute(ctx context.Context, host string) error {
	cmd := fmt.Sprintf("no ip route %s", host)
	if parsed := net.ParseIP(host); parsed != nil && parsed.To4() == nil {
		cmd = fmt.Sprintf("no ipv6 route %s", host)
	}
	_, _ = c.ndmc(ctx, cmd)
	return nil
}

// SetIPv6DefaultRoute sets the default IPv6 route via an interface.
func (c *ClientImpl) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	if _, err := c.ndmc(ctx, fmt.Sprintf("ipv6 route default %s", name)); err != nil {
		return err
	}
	return nil
}

// RemoveIPv6DefaultRoute removes the default IPv6 route for an interface.
func (c *ClientImpl) RemoveIPv6DefaultRoute(ctx context.Context, name string) {
	_, _ = c.ndmc(ctx, fmt.Sprintf("no ipv6 route default %s", name))
}

// GetDefaultIPv6Gateway returns the IPv6 gateway and interface name for the default route.
func (c *ClientImpl) GetDefaultIPv6Gateway(ctx context.Context) (string, string, error) {
	routes, err := c.getIPv6Routes(ctx)
	if err != nil {
		return "", "", err
	}

	for _, r := range routes {
		if r.Destination == "::/0" {
			if isNonISPInterface(r.Interface) {
				continue
			}
			gateway := r.Gateway
			if gateway == "" {
				continue
			}
			if gateway == "::" {
				// Point-to-point (PPPoE): no gateway IP, route via interface name
				gateway = r.Interface
			}
			return gateway, r.Interface, nil
		}
	}

	return "", "", fmt.Errorf("no default IPv6 gateway found (excluding tunnels)")
}

// getIPv6Routes fetches IPv6 routes from RCI.
// Handles the edge case where /rci/show/ipv6/route returns {} (empty object)
// instead of [] (empty array) when no routes exist.
func (c *ClientImpl) getIPv6Routes(ctx context.Context) ([]rciIPv6RouteEntry, error) {
	var raw json.RawMessage
	if err := rciGet(ctx, c.httpClient, "/show/ipv6/route", &raw); err != nil {
		return nil, fmt.Errorf("show ipv6 route: %w", err)
	}
	// Empty object {} means no routes
	if len(raw) == 0 || raw[0] == '{' {
		return nil, nil
	}
	var routes []rciIPv6RouteEntry
	if err := json.Unmarshal(raw, &routes); err != nil {
		return nil, fmt.Errorf("parse ipv6 routes: %w", err)
	}
	return routes, nil
}

// GetDefaultIPv6GatewayWithRetry returns the IPv6 gateway and interface with retry logic.
func (c *ClientImpl) GetDefaultIPv6GatewayWithRetry(ctx context.Context) (string, string, error) {
	var lastErr error
	for i := 0; i < retryCount; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		gateway, iface, err := c.GetDefaultIPv6Gateway(ctx)
		if err == nil && gateway != "" {
			return gateway, iface, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", "", fmt.Errorf("after %d retries: %w", retryCount, lastErr)
	}
	return "", "", fmt.Errorf("no default IPv6 gateway found after %d retries", retryCount)
}

// GetIPv6GatewayForInterface returns the IPv6 gateway used by a specific interface.
// The iface parameter is an NDMS logical name resolved to kernel name via
// show/interface/system-name (kernel name is the same for IPv4 and IPv6).
func (c *ClientImpl) GetIPv6GatewayForInterface(ctx context.Context, iface string) (string, error) {
	routes, err := c.getIPv6Routes(ctx)
	if err != nil {
		return "", err
	}

	routeIface := c.getSystemName(ctx, iface)

	for _, r := range routes {
		if r.Interface == routeIface {
			gateway := r.Gateway
			if gateway == "" {
				continue
			}
			if gateway == "::" {
				return iface, nil
			}
			return gateway, nil
		}
	}

	// Point-to-point (PPPoE): kernel name may not appear in IPv6 route table.
	if _, _, err := c.GetInterfaceAddress(ctx, iface); err == nil {
		return iface, nil
	}

	return "", fmt.Errorf("no IPv6 gateway found for interface %s", iface)
}

// GetDefaultGatewayInterface returns the current default gateway interface.
func (c *ClientImpl) GetDefaultGatewayInterface(ctx context.Context) (string, error) {
	routes, err := c.getIPv4Routes(ctx)
	if err != nil {
		return "", err
	}

	for _, r := range routes {
		if r.Destination == "0.0.0.0/0" {
			if isNonISPInterface(r.Interface) {
				continue
			}
			return r.Interface, nil
		}
	}

	return "", fmt.Errorf("no default gateway found (excluding tunnels)")
}

// getIPv4Routes fetches IPv4 routes from RCI.
func (c *ClientImpl) getIPv4Routes(ctx context.Context) ([]rciRouteEntry, error) {
	var routes []rciRouteEntry
	if err := rciGet(ctx, c.httpClient, "/show/ip/route", &routes); err != nil {
		return nil, fmt.Errorf("show ip route: %w", err)
	}
	return routes, nil
}

// GetDefaultGatewayInterfaceWithRetry returns the default gateway with retry logic.
func (c *ClientImpl) GetDefaultGatewayInterfaceWithRetry(ctx context.Context) (string, error) {
	var lastErr error
	for i := 0; i < retryCount; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		iface, err := c.GetDefaultGatewayInterface(ctx)
		if err == nil && iface != "" {
			return iface, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", fmt.Errorf("after %d retries: %w", retryCount, lastErr)
	}
	return "", fmt.Errorf("no default gateway found after %d retries", retryCount)
}

// GetDefaultGateway returns the gateway IP and interface name for the default route.
func (c *ClientImpl) GetDefaultGateway(ctx context.Context) (string, string, error) {
	routes, err := c.getIPv4Routes(ctx)
	if err != nil {
		return "", "", err
	}

	for _, r := range routes {
		if r.Destination == "0.0.0.0/0" {
			if isNonISPInterface(r.Interface) {
				continue
			}
			gateway := r.Gateway
			if gateway == "" {
				continue
			}
			if gateway == "0.0.0.0" {
				// PPPoE/point-to-point: no gateway IP, route via interface name
				gateway = r.Interface
			}
			return gateway, r.Interface, nil
		}
	}

	return "", "", fmt.Errorf("no default gateway found (excluding tunnels)")
}

// GetDefaultGatewayWithRetry returns the gateway IP and interface name with retry logic.
func (c *ClientImpl) GetDefaultGatewayWithRetry(ctx context.Context) (string, string, error) {
	var lastErr error
	for i := 0; i < retryCount; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		gateway, iface, err := c.GetDefaultGateway(ctx)
		if err == nil && gateway != "" {
			return gateway, iface, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return "", "", fmt.Errorf("after %d retries: %w", retryCount, lastErr)
	}
	return "", "", fmt.Errorf("no default gateway found after %d retries", retryCount)
}

// GetGatewayForInterface returns the gateway IP used by a specific interface.
// The iface parameter is an NDMS logical name (e.g., "ISP") which is resolved
// to the kernel name (e.g., "eth3") via show/interface/system-name before searching.
func (c *ClientImpl) GetGatewayForInterface(ctx context.Context, iface string) (string, error) {
	routes, err := c.getIPv4Routes(ctx)
	if err != nil {
		return "", err
	}

	routeIface := c.getSystemName(ctx, iface)

	for _, r := range routes {
		if r.Interface == routeIface {
			gateway := r.Gateway
			if gateway == "" {
				continue
			}
			if gateway == "0.0.0.0" {
				// Check if this is a DHCP interface (not PPPoE)
				if c.IsDHCPClientBound(ctx, iface) {
					// DHCP interface: compute gateway from IP + mask
					addr, mask, err := c.GetInterfaceAddress(ctx, iface)
					if err == nil {
						if gw, err := computeSubnetFirstIP(addr, mask); err == nil {
							return gw, nil
						}
					}
				}
				// Fallback: PPPoE or failed DHCP lookup → route via interface name
				return iface, nil
			}
			return gateway, nil
		}
	}

	// Point-to-point (PPPoE): kernel name may not appear in route table
	// (PPP routes are often shown differently). If the NDMS interface exists,
	// return its name as gateway placeholder — caller resolves via ip route get.
	if _, _, err := c.GetInterfaceAddress(ctx, iface); err == nil {
		return iface, nil
	}

	return "", fmt.Errorf("no gateway found for interface %s", iface)
}

// IsDHCPClientBound checks if a DHCP client is active for the given interface.
// Matches by id or name field. Returns true if state is "bound" or "renew".
func (c *ClientImpl) IsDHCPClientBound(ctx context.Context, iface string) bool {
	var clients []rciDHCPClient
	if err := rciGet(ctx, c.httpClient, "/show/ip/dhcp/client", &clients); err != nil {
		return false
	}

	for _, cl := range clients {
		if cl.ID == iface || cl.Name == iface {
			return cl.State == "bound" || cl.State == "renew"
		}
	}
	return false
}

// GetInterfaceAddress returns the IPv4 address and mask of an interface.
func (c *ClientImpl) GetInterfaceAddress(ctx context.Context, iface string) (string, string, error) {
	var info rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/"+iface, &info); err != nil {
		return "", "", fmt.Errorf("show interface %s: %w", iface, err)
	}
	if info.Address == "" || info.Mask == "" {
		return "", "", fmt.Errorf("address or mask not found for interface %s", iface)
	}
	return info.Address, info.Mask, nil
}

// getSystemName resolves an NDMS logical name (e.g., "ISP") to the kernel
// interface name (e.g., "eth3") via RCI /show/interface/system-name.
// Returns ndmsName unchanged if the RCI call fails.
func (c *ClientImpl) getSystemName(ctx context.Context, ndmsName string) string {
	var sysName string
	if err := rciGet(ctx, c.httpClient, "/show/interface/system-name?name="+ndmsName, &sysName); err != nil {
		return ndmsName
	}
	if sysName == "" {
		return ndmsName
	}
	return sysName
}

// computeSubnetFirstIP computes the first usable IP in a subnet (typically the gateway).
// Given address "192.168.20.55" and mask "255.255.255.0", returns "192.168.20.1".
func computeSubnetFirstIP(address, mask string) (string, error) {
	ip := net.ParseIP(address).To4()
	if ip == nil {
		return "", fmt.Errorf("invalid IP address: %s", address)
	}

	maskIP := net.ParseIP(mask).To4()
	if maskIP == nil {
		return "", fmt.Errorf("invalid mask: %s", mask)
	}
	ipMask := net.IPv4Mask(maskIP[0], maskIP[1], maskIP[2], maskIP[3])

	// Network address = IP AND mask
	network := ip.Mask(ipMask)

	// First usable IP = network + 1
	gateway := make(net.IP, 4)
	copy(gateway, network)
	gateway[3]++

	return gateway.String(), nil
}

// Save saves the current configuration.
func (c *ClientImpl) Save(ctx context.Context) error {
	_, err := c.ndmc(ctx, "system configuration save")
	return err
}

// QueryAllWANInterfaces returns all WAN interfaces.
// Uses a single RCI call to /show/interface/ which returns all interfaces
// as a JSON object keyed by interface ID, with full summary data per interface.
// Filters by security-level: public (NDMS designation for WAN-facing interfaces)
// and excludes VPN tunnels via isNonISPInterface.
func (c *ClientImpl) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	var allIfaces map[string]rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/", &allIfaces); err != nil {
		return nil, fmt.Errorf("show interface: %w", err)
	}

	var result []wan.Interface
	for _, info := range allIfaces {
		if info.SecurityLevel != "public" {
			continue
		}
		if isNonISPInterface(info.InterfaceName) {
			continue
		}
		result = append(result, wan.Interface{
			Name:     info.InterfaceName,
			Type:     info.Type,
			Label:    wanInterfaceLabel(info.Type, info.InterfaceName, info.Description),
			Up:       info.State == "up" && info.Summary.Layer.IPv4 == "running",
			Priority: info.Priority,
		})
	}
	return result, nil
}

// HasWANIPv6 checks if a WAN interface has a global IPv6 address (ipv6 layer == "running").
func (c *ClientImpl) HasWANIPv6(ctx context.Context, ifaceName string) bool {
	var info rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/"+ifaceName, &info); err != nil {
		return false
	}
	return info.Summary.Layer.IPv6 == "running"
}

// sanitizeDescription replaces spaces and special characters.
func sanitizeDescription(desc string) string {
	desc = strings.ReplaceAll(desc, " ", "-")
	var result strings.Builder
	for _, r := range desc {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// wanInterfaceLabel generates a human-readable display name for a WAN interface.
// If NDMS has a user-set description, it's used as the label.
// Otherwise, a label is generated from the interface type.
func wanInterfaceLabel(ifaceType, name, description string) string {
	// User-set description from NDMS (e.g., "Letai") takes priority
	if description != "" && description != name {
		return description
	}

	// Generate from interface type
	switch ifaceType {
	case "WifiStation":
		// WifiMaster0/WifiStation0 = 2.4 GHz, WifiMaster1/WifiStation0 = 5 GHz
		if strings.HasPrefix(name, "WifiMaster1") {
			return "Wi-Fi клиент 5 ГГц"
		}
		return "Wi-Fi клиент 2.4 ГГц"
	case "GigabitEthernet":
		return "Ethernet"
	case "FastEthernet":
		return "Ethernet"
	case "PPPoE":
		return "PPPoE"
	case "PPTP":
		return "PPTP"
	case "L2TP":
		return "L2TP"
	case "IPoE":
		return "IPoE"
	case "UsbModem", "CdcEthernet", "UsbLte", "UsbQmi":
		return "USB-модем"
	case "Vlan":
		return "VLAN"
	}

	// Fallback: use interface name
	return name
}

// GetHotspotClients returns LAN devices from the router's hotspot table.
func (c *ClientImpl) GetHotspotClients(ctx context.Context) ([]HotspotClient, error) {
	var resp rciHotspotResponse
	if err := rciGet(ctx, c.httpClient, "/show/ip/hotspot", &resp); err != nil {
		return nil, fmt.Errorf("show ip hotspot: %w", err)
	}

	var clients []HotspotClient
	for _, h := range resp.Host {
		if h.IP == "" || h.IP == "0.0.0.0" {
			continue
		}
		hostname := h.Name
		if hostname == "" {
			hostname = h.Hostname
		}
		clients = append(clients, HotspotClient{
			IP:       h.IP,
			MAC:      h.MAC,
			Hostname: hostname,
			Online:   isActiveHost(h.Active),
		})
	}
	return clients, nil
}

// isActiveHost checks the "active" field which may be bool or string depending on firmware.
func isActiveHost(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return val == "yes"
	}
	return true // assume active if field absent
}

// isNonISPInterface checks if the interface is a VPN tunnel (not a real ISP connection).
func isNonISPInterface(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "opkgtun") ||
		strings.HasPrefix(name, "awg") ||
		strings.HasPrefix(name, "nwg") ||
		strings.HasPrefix(name, "wg") ||
		strings.HasPrefix(name, "wireguard") ||
		strings.HasPrefix(name, "ipsec") ||
		strings.HasPrefix(name, "sstp") ||
		strings.HasPrefix(name, "openvpn")
}

// Ensure ClientImpl implements Client interface.
var _ Client = (*ClientImpl)(nil)

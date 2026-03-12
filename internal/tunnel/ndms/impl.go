package ndms

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

const (
	defaultTimeout = 10 * time.Second
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

// DumpIPv4Routes returns NDMS IPv4 route table as a formatted string for diagnostics.
func (c *ClientImpl) DumpIPv4Routes(ctx context.Context) string {
	routes, err := c.getIPv4Routes(ctx)
	if err != nil {
		return "error: " + err.Error()
	}
	var buf strings.Builder
	for _, r := range routes {
		gw := r.Gateway
		if gw == "" {
			gw = "*"
		}
		fmt.Fprintf(&buf, "%s via %s dev %s\n", r.Destination, gw, r.Interface)
	}
	return buf.String()
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

// GetSystemName resolves an NDMS logical name (e.g., "ISP") to the kernel
// interface name (e.g., "eth3") via RCI /show/interface/system-name.
// Returns ndmsName unchanged if the RCI call fails.
func (c *ClientImpl) GetSystemName(ctx context.Context, ndmsName string) string {
	var sysName string
	if err := rciGet(ctx, c.httpClient, "/show/interface/system-name?name="+ndmsName, &sysName); err != nil {
		return ndmsName
	}
	if sysName == "" {
		return ndmsName
	}
	return sysName
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
	for id, info := range allIfaces {
		if info.SecurityLevel != "public" {
			continue
		}
		if isNonISPInterface(info.InterfaceName) {
			continue
		}
		result = append(result, wan.Interface{
			Name:     c.GetSystemName(ctx, id),
			ID:       id,
			Label:    wanInterfaceLabel(info.Type, info.InterfaceName, info.Description),
			Up:       info.State == "up" && info.Summary.Layer.IPv4 == "running",
			Priority: info.Priority,
		})
	}
	return result, nil
}

// QueryAllInterfaces returns all router interfaces without security-level filtering.
func (c *ClientImpl) QueryAllInterfaces(ctx context.Context) ([]AllInterface, error) {
	var allIfaces map[string]rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/", &allIfaces); err != nil {
		return nil, fmt.Errorf("show interface: %w", err)
	}

	var result []AllInterface
	for id, info := range allIfaces {
		if info.InterfaceName == "" {
			continue
		}
		// Exclude our own managed tunnels
		if isOwnTunnel(info.InterfaceName) {
			continue
		}
		result = append(result, AllInterface{
			Name:  c.GetSystemName(ctx, id),
			Label: allInterfaceLabel(info.Type, info.InterfaceName, info.Description),
			Up:    info.State == "up" && info.Summary.Layer.IPv4 == "running",
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// isOwnTunnel checks if the interface is managed by awg-manager.
// Only excludes our tunnels, not other VPNs (user might want to route through them).
func isOwnTunnel(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "opkgtun") ||
		strings.HasPrefix(name, "awgm")
}

// allInterfaceLabel generates a label for any router interface.
func allInterfaceLabel(ifaceType, name, description string) string {
	if description != "" && description != name {
		return description
	}
	switch ifaceType {
	case "Bridge":
		return "Bridge"
	case "Loopback":
		return "Loopback"
	case "GigabitEthernet", "FastEthernet":
		return "Ethernet"
	case "WifiStation":
		if strings.HasPrefix(name, "WifiMaster1") {
			return "Wi-Fi клиент 5 ГГц"
		}
		return "Wi-Fi клиент 2.4 ГГц"
	case "WifiMaster":
		return "Wi-Fi"
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
	return name
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
// Only excludes protocols that are NEVER used by ISPs:
//   - opkgtun/awg: our own managed tunnels
//   - wireguard/nwg/wg: WireGuard (Keenetic native or third-party)
//   - ipsec/sstp/openvpn: pure VPN protocols
//
// NOT excluded (ISPs do use these): PPTP, L2TP, GRE, IPIP, EoIP, PPPoE, IPoE.
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

// RCIPost sends a JSON payload to RCI via HTTP POST.
func (c *ClientImpl) RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error) {
	return rciPost(ctx, c.httpClient, "/", payload)
}

// ShowObjectGroupFQDN returns all FQDN object groups from the router.
// The response has nested structure that differs from the public ObjectGroupFQDN type.
func (c *ClientImpl) ShowObjectGroupFQDN(ctx context.Context) ([]ObjectGroupFQDN, error) {
	var raw struct {
		Group []struct {
			GroupName     string `json:"group-name"`
			Entry         []struct {
				FQDN string `json:"fqdn"`
			} `json:"entry"`
			ExcludedFQDNs []struct {
				Address string `json:"address"`
			} `json:"excluded-fqdns"`
		} `json:"group"`
	}
	if err := rciGet(ctx, c.httpClient, "/show/object-group/fqdn", &raw); err != nil {
		return nil, err
	}

	groups := make([]ObjectGroupFQDN, 0, len(raw.Group))
	for _, g := range raw.Group {
		og := ObjectGroupFQDN{Name: g.GroupName}
		for _, e := range g.Entry {
			og.Includes = append(og.Includes, e.FQDN)
		}
		for _, e := range g.ExcludedFQDNs {
			og.Excludes = append(og.Excludes, e.Address)
		}
		groups = append(groups, og)
	}
	return groups, nil
}

// ShowDnsProxyRoute returns all dns-proxy route entries.
func (c *ClientImpl) ShowDnsProxyRoute(ctx context.Context) ([]DnsProxyRoute, error) {
	var routes []DnsProxyRoute
	if err := rciGet(ctx, c.httpClient, "/show/rc/dns-proxy/route", &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// ListWireguardInterfaces queries RCI for all interfaces and returns those
// with tunnel-like types: Wireguard, Proxy (SSTP/L2TP/PPTP), OpkgTun.
// These are interfaces that can be used as DNS route targets.
func (c *ClientImpl) ListWireguardInterfaces(ctx context.Context) ([]WireguardInterfaceInfo, error) {
	var allIfaces map[string]rciInterfaceInfo
	if err := rciGet(ctx, c.httpClient, "/show/interface/", &allIfaces); err != nil {
		return nil, err
	}

	var result []WireguardInterfaceInfo
	for _, iface := range allIfaces {
		t := strings.ToLower(iface.Type)
		if t != "wireguard" && t != "proxy" && t != "opkgtun" {
			continue
		}
		name := iface.InterfaceName
		if name == "" {
			continue
		}
		result = append(result, WireguardInterfaceInfo{
			Name:        name,
			Description: iface.Description,
		})
	}
	return result, nil
}

// Ensure ClientImpl implements Client interface.
var _ Client = (*ClientImpl)(nil)

package ndms

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// ClientImpl is the NDMS client implementation.
type ClientImpl struct {
	rci *rci.Client
}

// New creates a new NDMS client.
func New() *ClientImpl {
	return &ClientImpl{
		rci: rci.New(),
	}
}

// NewWithTimeout creates a new NDMS client with custom timeout.
func NewWithTimeout(timeout time.Duration) *ClientImpl {
	return &ClientImpl{
		rci: rci.NewWithTimeout(timeout),
	}
}

// RCITransport returns the shared HTTP transport for RCI connections.
// Used by other packages that also query localhost:79.
func RCITransport() *http.Transport {
	return rci.Transport()
}

// ShowInterface returns raw interface data as JSON string (from RCI).
// Consumed by ParseInterfaceInfo which auto-detects JSON vs text format.
func (c *ClientImpl) ShowInterface(ctx context.Context, name string) (string, error) {
	raw, err := c.rci.GetRaw(ctx, "/show/interface/"+name)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DeleteOpkgTun removes an OpkgTun interface from NDMS.
// "no interface" removes everything: routes, DNS, address, security-level, ip global.
// Caller is responsible for calling Save() separately.
func (c *ClientImpl) DeleteOpkgTun(ctx context.Context, name string) error {
	_, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{"no": true},
		},
	})
	return err
}

// OpkgTunExists checks if an OpkgTun interface exists in NDMS.
func (c *ClientImpl) OpkgTunExists(ctx context.Context, name string) bool {
	if name == "" {
		return false
	}
	var info rci.InterfaceInfo
	if err := c.rci.Get(ctx, "/show/interface/"+name, &info); err != nil {
		return false
	}
	return info.InterfaceName != ""
}

// SetAddress sets the IPv4 address of an interface.
// Address can be in CIDR notation (10.0.0.2/32) or plain IP (10.0.0.2).
// If plain IP, /32 is assumed for point-to-point tunnels.
func (c *ClientImpl) SetAddress(ctx context.Context, name, address string) error {
	// Remove old address first
	_, _ = c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"ip": map[string]interface{}{
					"address": map[string]interface{}{"no": true},
				},
			},
		},
	})

	// Parse CIDR to get separate address and mask for RCI
	if !strings.Contains(address, "/") {
		address = address + "/32"
	}
	ip, ipNet, err := net.ParseCIDR(address)
	if err != nil {
		return fmt.Errorf("parse address %q: %w", address, err)
	}
	mask := net.IP(ipNet.Mask).String()

	// Set new address with separate address + mask (RCI rejects CIDR notation)
	_, err = c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"ip": map[string]interface{}{
					"address": map[string]interface{}{
						"address": ip.String(),
						"mask":    mask,
					},
				},
			},
		},
	})
	return err
}

// SetIPv6Address sets the IPv6 address of an interface.
// Uses name-as-key format ({"interface": {"OpkgTun0": {...}}}) for OpkgTun compatibility.
// The [{}, {"block": "addr/128"}] array clears old addresses + sets new in one call.
func (c *ClientImpl) SetIPv6Address(ctx context.Context, name, address string) error {
	_, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"ipv6": map[string]interface{}{
					"address": []interface{}{
						map[string]interface{}{},
						map[string]interface{}{"block": address + "/128"},
					},
				},
			},
		},
	})
	return err
}

// ClearIPv6Address removes all IPv6 addresses from an interface.
func (c *ClientImpl) ClearIPv6Address(ctx context.Context, name string) {
	_, _ = c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"ipv6": map[string]interface{}{
					"address": map[string]interface{}{"no": true},
				},
			},
		},
	})
}

// SetMTU sets the MTU of an interface.
func (c *ClientImpl) SetMTU(ctx context.Context, name string, mtu int) error {
	_, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"ip": map[string]interface{}{
					"mtu": mtu,
					"tcp": map[string]interface{}{
						"adjust-mss": map[string]interface{}{
							"pmtu": true,
						},
					},
				},
			},
		},
	})
	return err
}

// SetDNS registers DNS servers for a tunnel interface.
// Each server gets its own RCI call.
// Validates that each server is a valid IP address.
func (c *ClientImpl) SetDNS(ctx context.Context, name string, servers []string) error {
	for _, dns := range servers {
		dns = strings.TrimSpace(dns)
		if dns == "" {
			continue
		}
		if net.ParseIP(dns) == nil {
			return fmt.Errorf("invalid DNS server IP: %q", dns)
		}
		if _, err := c.rci.Post(ctx, map[string]interface{}{
			"ip": map[string]interface{}{
				"name-server": map[string]interface{}{
					"address":   dns,
					"interface": name,
				},
			},
		}); err != nil {
			return fmt.Errorf("set dns %s: %w", dns, err)
		}
	}
	return nil
}

// ClearDNS removes DNS servers registered for a tunnel interface.
// Validates IPs for defense-in-depth even though values come from in-memory tracking.
func (c *ClientImpl) ClearDNS(ctx context.Context, name string, servers []string) error {
	for _, dns := range servers {
		dns = strings.TrimSpace(dns)
		if dns == "" {
			continue
		}
		if net.ParseIP(dns) == nil {
			continue
		}
		// Best-effort: "no ip name-server" may fail if already removed
		_, _ = c.rci.Post(ctx, map[string]interface{}{
			"ip": map[string]interface{}{
				"name-server": map[string]interface{}{
					"no":        true,
					"address":   dns,
					"interface": name,
				},
			},
		})
	}
	return nil
}

// SetDescription sets the description of an interface.
func (c *ClientImpl) SetDescription(ctx context.Context, name, description string) error {
	safeDesc := sanitizeDescription(description)
	if safeDesc == "" {
		safeDesc = name
	}
	_, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"description": safeDesc,
			},
		},
	})
	return err
}

// InterfaceUp brings an interface up.
func (c *ClientImpl) InterfaceUp(ctx context.Context, name string) error {
	_, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{"up": true},
		},
	})
	return err
}

// SetDefaultRoute sets the default IPv4 route via an interface.
func (c *ClientImpl) SetDefaultRoute(ctx context.Context, name string) error {
	if _, err := c.rci.Post(ctx, rci.CmdSetDefaultRoute(name)); err != nil {
		return fmt.Errorf("set default route: %w", err)
	}
	return nil
}

// RemoveDefaultRoute removes the default IPv4 route for an interface.
func (c *ClientImpl) RemoveDefaultRoute(ctx context.Context, name string) error {
	if _, err := c.rci.Post(ctx, rci.CmdRemoveDefaultRoute(name)); err != nil {
		return fmt.Errorf("remove default route: %w", err)
	}
	return nil
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
func (c *ClientImpl) getIPv4Routes(ctx context.Context) ([]rci.RouteEntry, error) {
	var routes []rci.RouteEntry
	if err := c.rci.Get(ctx, "/show/ip/route", &routes); err != nil {
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

// GetSystemName resolves an NDMS logical name (e.g., "ISP") to the kernel
// interface name (e.g., "eth3") via RCI /show/interface/system-name.
// Returns ndmsName unchanged if the RCI call fails.
func (c *ClientImpl) GetSystemName(ctx context.Context, ndmsName string) string {
	var sysName string
	if err := c.rci.Get(ctx, "/show/interface/system-name?name="+ndmsName, &sysName); err != nil {
		return ndmsName
	}
	if sysName == "" {
		return ndmsName
	}
	return sysName
}

// Save saves the current configuration.
func (c *ClientImpl) Save(ctx context.Context) error {
	_, err := c.rci.Post(ctx, map[string]interface{}{
		"system": map[string]interface{}{
			"configuration": map[string]interface{}{
				"save": true,
			},
		},
	})
	return err
}

// QueryAllWANInterfaces returns all WAN interfaces.
// Uses a single RCI call to /show/interface/ which returns all interfaces
// as a JSON object keyed by interface ID, with full summary data per interface.
// Filters by security-level: public (NDMS designation for WAN-facing interfaces)
// and excludes VPN tunnels via isNonISPInterface.
func (c *ClientImpl) QueryAllWANInterfaces(ctx context.Context) ([]wan.Interface, error) {
	var allIfaces map[string]rci.InterfaceInfo
	if err := c.rci.Get(ctx, "/show/interface/", &allIfaces); err != nil {
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
	var allIfaces map[string]rci.InterfaceInfo
	if err := c.rci.Get(ctx, "/show/interface/", &allIfaces); err != nil {
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
	var info rci.InterfaceInfo
	if err := c.rci.Get(ctx, "/show/interface/"+ifaceName, &info); err != nil {
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

// isNonISPInterface checks if the interface is a VPN tunnel (not a real ISP connection).
// Only excludes protocols that are NEVER used by ISPs:
//   - opkgtun/awg: our own managed tunnels
//   - wireguard/nwg/wg: WireGuard (Keenetic native or third-party)
//   - ipsec/sstp/openvpn: pure VPN protocols
//   - proxy: Keenetic proxy interfaces (t2s), depend on underlying WAN
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
		strings.HasPrefix(name, "openvpn") ||
		strings.HasPrefix(name, "proxy")
}

// RCIPost sends a JSON payload to RCI via HTTP POST.
func (c *ClientImpl) RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error) {
	return c.rci.Post(ctx, payload)
}

// RCIGet performs an HTTP GET to an RCI path and returns raw JSON.
func (c *ClientImpl) RCIGet(ctx context.Context, path string) (json.RawMessage, error) {
	raw, err := c.rci.GetRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
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
	if err := c.rci.Get(ctx, "/show/object-group/fqdn", &raw); err != nil {
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
	if err := c.rci.Get(ctx, "/show/rc/dns-proxy/route", &routes); err != nil {
		return nil, err
	}
	return routes, nil
}

// ListWireguardInterfaces queries RCI for all interfaces and returns those
// with tunnel-like types: Wireguard, Proxy (SSTP/L2TP/PPTP), OpkgTun.
// These are interfaces that can be used as DNS route targets.
func (c *ClientImpl) ListWireguardInterfaces(ctx context.Context) ([]WireguardInterfaceInfo, error) {
	var allIfaces map[string]rci.InterfaceInfo
	if err := c.rci.Get(ctx, "/show/interface/", &allIfaces); err != nil {
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

// ListSystemWireguardTunnels returns all system Wireguard interfaces with full peer info.
func (c *ClientImpl) ListSystemWireguardTunnels(ctx context.Context) ([]SystemWireguardTunnel, error) {
	// RCI /show/interface/ returns map[string]json.RawMessage
	var allIfaces map[string]json.RawMessage
	if err := c.rci.Get(ctx, "/show/interface/", &allIfaces); err != nil {
		return nil, fmt.Errorf("list system wireguard: %w", err)
	}

	var tunnels []SystemWireguardTunnel
	for _, raw := range allIfaces {
		var typeCheck struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &typeCheck); err != nil {
			continue
		}
		if !strings.EqualFold(typeCheck.Type, "Wireguard") {
			continue
		}

		var detail rciWireguardDetail
		if err := json.Unmarshal(raw, &detail); err != nil {
			continue
		}

		// Skip Keenetic's built-in WireGuard VPN Server interface
		if detail.Description == "Wireguard VPN Server" {
			continue
		}

		t := rciToSystemTunnel(detail)
		t.InterfaceName = c.GetSystemName(ctx, t.ID)
		tunnels = append(tunnels, t)
	}

	// Stable sort by ID to prevent card reordering on each poll
	sort.Slice(tunnels, func(i, j int) bool {
		return tunnels[i].ID < tunnels[j].ID
	})

	return tunnels, nil
}

// GetSystemWireguardTunnel returns details for a single system Wireguard interface.
func (c *ClientImpl) GetSystemWireguardTunnel(ctx context.Context, name string) (*SystemWireguardTunnel, error) {
	if !IsValidWireguardName(name) {
		return nil, fmt.Errorf("invalid wireguard name: %s", name)
	}

	var detail rciWireguardDetail
	if err := c.rci.Get(ctx, "/show/interface/"+name, &detail); err != nil {
		return nil, fmt.Errorf("get system wireguard %s: %w", name, err)
	}

	t := rciToSystemTunnel(detail)
	t.InterfaceName = c.GetSystemName(ctx, name)
	return &t, nil
}

// GetWireguardServer returns a server view of a WireGuard interface with all peers.
func (c *ClientImpl) GetWireguardServer(ctx context.Context, name string) (*WireguardServer, error) {
	if !IsValidWireguardName(name) {
		return nil, fmt.Errorf("invalid wireguard name: %s", name)
	}

	var detail rciWireguardDetail
	if err := c.rci.Get(ctx, "/show/interface/"+name, &detail); err != nil {
		return nil, fmt.Errorf("get wireguard server %s: %w", name, err)
	}

	server := rciToWireguardServer(detail)
	server.InterfaceName = c.GetSystemName(ctx, name)
	return &server, nil
}

// GetWireguardServerConfig returns RC configuration for .conf generation.
func (c *ClientImpl) GetWireguardServerConfig(ctx context.Context, name string) (*WireguardServerConfig, error) {
	if !IsValidWireguardName(name) {
		return nil, fmt.Errorf("invalid wireguard name: %s", name)
	}

	// Get runtime data for public key
	var detail rciWireguardDetail
	if err := c.rci.Get(ctx, "/show/interface/"+name, &detail); err != nil {
		return nil, fmt.Errorf("get wireguard server %s: %w", name, err)
	}

	var publicKey string
	if detail.Wireguard != nil {
		publicKey = detail.Wireguard.PublicKey
	}

	// Get RC config for peer details (preshared keys, allowed IPs)
	var rc rciRCInterface
	if err := c.rci.Get(ctx, "/show/rc/interface/"+name, &rc); err != nil {
		return nil, fmt.Errorf("get wireguard server config %s: %w", name, err)
	}

	cfg := rciRCToServerConfig(rc, publicKey)
	return &cfg, nil
}

// ListAllWireguardServers returns all WireGuard interfaces as server views (with all peers).
func (c *ClientImpl) ListAllWireguardServers(ctx context.Context) ([]WireguardServer, error) {
	var allIfaces map[string]json.RawMessage
	if err := c.rci.Get(ctx, "/show/interface/", &allIfaces); err != nil {
		return nil, fmt.Errorf("list wireguard servers: %w", err)
	}

	var servers []WireguardServer
	for _, raw := range allIfaces {
		var typeCheck struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &typeCheck); err != nil {
			continue
		}
		if !strings.EqualFold(typeCheck.Type, "Wireguard") {
			continue
		}

		var detail rciWireguardDetail
		if err := json.Unmarshal(raw, &detail); err != nil {
			continue
		}

		server := rciToWireguardServer(detail)
		server.InterfaceName = c.GetSystemName(ctx, server.ID)
		servers = append(servers, server)
	}

	sort.Slice(servers, func(i, j int) bool {
		return servers[i].ID < servers[j].ID
	})

	// Enrich peers with AllowedIPs from config (RC) data.
	// One RCI call per server — acceptable since list is small (1-3 servers).
	for i := range servers {
		var rc rciRCInterface
		if err := c.rci.Get(ctx, "/show/rc/interface/"+servers[i].ID, &rc); err != nil {
			continue
		}
		if rc.Wireguard == nil {
			continue
		}
		// Build pubkey → allowedIPs map from config
		allowedByKey := make(map[string][]string)
		for _, rp := range rc.Wireguard.Peer {
			var ips []string
			for _, a := range rp.AllowIPs {
				if a.Mask != "" {
					ips = append(ips, a.Address+"/"+a.Mask)
				} else {
					ips = append(ips, a.Address)
				}
			}
			allowedByKey[rp.Key] = ips
		}
		// Merge into runtime peers
		for j := range servers[i].Peers {
			if ips, ok := allowedByKey[servers[i].Peers[j].PublicKey]; ok {
				servers[i].Peers[j].AllowedIPs = ips
			}
		}
	}

	return servers, nil
}

// GetASCParams returns AWG obfuscation parameters for a system Wireguard interface.
// Always returns ASCParamsExtended on firmware that supports ASC, even when
// S3/S4/I1-I5 haven't been set yet (RCI omits unset fields from the response).
func (c *ClientImpl) GetASCParams(ctx context.Context, name string) (json.RawMessage, error) {
	if !IsValidWireguardName(name) {
		return nil, fmt.Errorf("invalid wireguard name: %s", name)
	}

	var raw map[string]string
	path := "/show/rc/interface/" + name + "/wireguard/asc"
	if err := c.rci.Get(ctx, path, &raw); err != nil {
		return nil, fmt.Errorf("get ASC params %s: %w", name, err)
	}

	// Firmware capability determines the response type, not the RCI response.
	// RCI omits fields that haven't been set yet, but the firmware still supports them.
	if osdetect.AtLeast(5, 1) {
		params := ASCParamsExtended{
			ASCParams: ASCParams{
				Jc: atoiSafe(raw["jc"]), Jmin: atoiSafe(raw["jmin"]), Jmax: atoiSafe(raw["jmax"]),
				S1: atoiSafe(raw["s1"]), S2: atoiSafe(raw["s2"]),
				H1: raw["h1"], H2: raw["h2"], H3: raw["h3"], H4: raw["h4"],
			},
			S3: atoiSafe(raw["s3"]), S4: atoiSafe(raw["s4"]),
			I1: raw["i1"], I2: raw["i2"], I3: raw["i3"], I4: raw["i4"], I5: raw["i5"],
		}
		return json.Marshal(params)
	}

	params := ASCParams{
		Jc: atoiSafe(raw["jc"]), Jmin: atoiSafe(raw["jmin"]), Jmax: atoiSafe(raw["jmax"]),
		S1: atoiSafe(raw["s1"]), S2: atoiSafe(raw["s2"]),
		H1: raw["h1"], H2: raw["h2"], H3: raw["h3"], H4: raw["h4"],
	}
	return json.Marshal(params)
}

// atoiSafe converts a string to int, returning 0 on failure.
func atoiSafe(s string) int {
	v, _ := strconv.Atoi(s)
	return v
}

// SetASCParams sets AWG obfuscation parameters on a system Wireguard interface.
// Extended params (S3, S4, I1-I5) are only sent on firmware >= 5.1.
func (c *ClientImpl) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	if !IsValidWireguardName(name) {
		return fmt.Errorf("invalid wireguard name: %s", name)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return fmt.Errorf("parse ASC params: %w", err)
	}

	_, hasExtended := raw["s3"]
	supportsExtended := osdetect.AtLeast(5, 1)

	// Build ASC payload for RCI
	ascPayload := map[string]interface{}{}

	if hasExtended && supportsExtended {
		var p ASCParamsExtended
		if err := json.Unmarshal(params, &p); err != nil {
			return fmt.Errorf("parse extended ASC params: %w", err)
		}
		ascPayload["jc"] = strconv.Itoa(p.Jc)
		ascPayload["jmin"] = strconv.Itoa(p.Jmin)
		ascPayload["jmax"] = strconv.Itoa(p.Jmax)
		ascPayload["s1"] = strconv.Itoa(p.S1)
		ascPayload["s2"] = strconv.Itoa(p.S2)
		ascPayload["h1"] = p.H1
		ascPayload["h2"] = p.H2
		ascPayload["h3"] = p.H3
		ascPayload["h4"] = p.H4
		ascPayload["s3"] = strconv.Itoa(p.S3)
		ascPayload["s4"] = strconv.Itoa(p.S4)
		ascPayload["i1"] = p.I1
		ascPayload["i2"] = p.I2
		ascPayload["i3"] = p.I3
		ascPayload["i4"] = p.I4
		ascPayload["i5"] = p.I5
	} else {
		var p ASCParams
		if err := json.Unmarshal(params, &p); err != nil {
			return fmt.Errorf("parse ASC params: %w", err)
		}
		ascPayload["jc"] = strconv.Itoa(p.Jc)
		ascPayload["jmin"] = strconv.Itoa(p.Jmin)
		ascPayload["jmax"] = strconv.Itoa(p.Jmax)
		ascPayload["s1"] = strconv.Itoa(p.S1)
		ascPayload["s2"] = strconv.Itoa(p.S2)
		ascPayload["h1"] = p.H1
		ascPayload["h2"] = p.H2
		ascPayload["h3"] = p.H3
		ascPayload["h4"] = p.H4
	}

	if _, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			name: map[string]interface{}{
				"wireguard": map[string]interface{}{
					"asc": ascPayload,
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("set ASC params %s: %w", name, err)
	}

	return c.Save(ctx)
}

// FindFreeWireguardIndex returns the next free WireguardN index.
func (c *ClientImpl) FindFreeWireguardIndex(ctx context.Context) (int, error) {
	var allIfaces map[string]json.RawMessage
	if err := c.rci.Get(ctx, "/show/interface/", &allIfaces); err != nil {
		return 0, fmt.Errorf("list interfaces: %w", err)
	}

	used := make(map[int]bool)
	for name := range allIfaces {
		if strings.HasPrefix(name, "Wireguard") {
			numStr := strings.TrimPrefix(name, "Wireguard")
			if n, err := strconv.Atoi(numStr); err == nil {
				used[n] = true
			}
		}
	}

	for i := 1; i < 100; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free Wireguard index found")
}

// ConfigurePingCheck creates/updates a ping-check profile and binds it to an interface.
// Always removes the old profile first to ensure clean state.
func (c *ClientImpl) ConfigurePingCheck(ctx context.Context, profile, ifaceName string, cfg PingCheckConfig) error {
	// Remove old profile first (NDMS doesn't cleanly update existing profiles)
	c.removePingCheckRCI(ctx, profile, ifaceName)

	// Build profile config
	mode := cfg.Mode
	if mode == "" || mode == "uri" {
		mode = "icmp" // URI mode is unstable in NDMS, fallback to ICMP
	}
	profileCfg := map[string]interface{}{
		"host":            cfg.Host,
		"mode":            mode,
		"update-interval": map[string]interface{}{"seconds": clamp(cfg.UpdateInterval, 3, 3600, 10)},
		"timeout":         clamp(cfg.Timeout, 1, 10, 2),
	}
	if cfg.MaxFails > 0 {
		profileCfg["max-fails"] = map[string]interface{}{"count": clamp(cfg.MaxFails, 1, 10, 5)}
	}
	if cfg.MinSuccess > 0 {
		profileCfg["min-success"] = map[string]interface{}{"count": clamp(cfg.MinSuccess, 1, 10, 5)}
	}
	if cfg.Port > 0 && (cfg.Mode == "connect" || cfg.Mode == "tls") {
		profileCfg["port"] = cfg.Port
	}

	// Create profile + set all params in one RCI call
	if _, err := c.rci.Post(ctx, map[string]interface{}{
		"ping-check": map[string]interface{}{
			"profile": map[string]interface{}{
				profile: profileCfg,
			},
		},
	}); err != nil {
		return fmt.Errorf("create profile: %w", err)
	}

	// Bind to interface + set restart in one RCI call
	if _, err := c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"ping-check": map[string]interface{}{
					"profile": profile,
					"restart": cfg.Restart,
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("bind profile: %w", err)
	}

	// Save config
	c.rciSave(ctx)

	return nil
}

// RemovePingCheck removes a ping-check profile and its interface binding.
func (c *ClientImpl) RemovePingCheck(ctx context.Context, profile, ifaceName string) error {
	c.removePingCheckRCI(ctx, profile, ifaceName)
	c.rciSave(ctx)
	return nil
}

// removePingCheckRCI removes restart + unbinds profile + deletes profile via RCI.
func (c *ClientImpl) removePingCheckRCI(ctx context.Context, profile, ifaceName string) {
	// Disable restart first (must happen before profile unbind)
	_, _ = c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"ping-check": map[string]interface{}{
					"restart": map[string]interface{}{"no": true},
				},
			},
		},
	})
	// Unbind profile from interface
	_, _ = c.rci.Post(ctx, map[string]interface{}{
		"interface": map[string]interface{}{
			ifaceName: map[string]interface{}{
				"ping-check": map[string]interface{}{
					"profile": map[string]interface{}{"no": true, "profile": profile},
				},
			},
		},
	})
	// Delete profile
	_, _ = c.rci.Post(ctx, map[string]interface{}{
		"ping-check": map[string]interface{}{
			"profile": map[string]interface{}{
				profile: map[string]interface{}{"no": true},
			},
		},
	})
}

// rciSave saves configuration via RCI.
func (c *ClientImpl) rciSave(ctx context.Context) {
	_, _ = c.rci.Post(ctx, map[string]interface{}{
		"system": map[string]interface{}{
			"configuration": map[string]interface{}{
				"save": true,
			},
		},
	})
}

// ShowPingCheck returns the current status of a ping-check profile.
// Queries /show/ping-check/ (list all) and finds the matching profile.
func (c *ClientImpl) ShowPingCheck(ctx context.Context, profile string) (*PingCheckStatus, error) {
	var resp rci.PingCheckListResponse
	if err := c.rci.Get(ctx, "/show/ping-check/", &resp); err != nil {
		return &PingCheckStatus{Exists: false}, nil
	}

	for _, p := range resp.PingCheck {
		if p.Profile != profile {
			continue
		}

		var host string
		if len(p.Host) > 0 {
			host = p.Host[0]
		}

		status := &PingCheckStatus{
			Exists:     true,
			Host:       host,
			Mode:       p.Mode,
			Interval:   p.UpdateInterval,
			MaxFails:   p.MaxFails,
			MinSuccess: p.MinSuccess,
			Timeout:    p.Timeout,
			Port:       p.Port,
		}

		for _, ifStatus := range p.Interface {
			status.Bound = true
			status.Status = ifStatus.Status
			status.FailCount = ifStatus.FailCount
			status.SuccessCount = ifStatus.SuccessCount
			break
		}

		return status, nil
	}

	return &PingCheckStatus{Exists: false}, nil
}


// clamp returns v clamped to [min, max], or def if v <= 0.
func clamp(v, min, max, def int) int {
	if v <= 0 {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// Ensure ClientImpl implements Client interface.
var _ Client = (*ClientImpl)(nil)

package uplink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// ErrNoExplicitProviderDNS indicates that a provider has no unscoped DNS entry
// in the queried NDMS catalogue.
var ErrNoExplicitProviderDNS = errors.New("no explicit DNS servers for provider")

type providerDNSRow struct {
	Address   string `json:"address"`
	Interface string `json:"interface"`
	Domain    string `json:"domain"`
}

func filterProviderDNS(rows []providerDNSRow, providerID string) ([]string, error) {
	result := []string{}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Interface != providerID || r.Domain != "" {
			continue
		}
		server, err := ParseDNSServer(r.Address)
		if err != nil {
			continue
		}
		value := server.String()
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w %q", ErrNoExplicitProviderDNS, providerID)
	}
	return result, nil
}

// Binding is an explicit egress snapshot. It contains no tunnel credentials.
// DNS servers must be literals: bootstrapping them through system DNS would
// bypass the selected provider before the tunnel even connects.
type Binding struct {
	Device     string   `json:"device"`
	IPv4       string   `json:"ipv4,omitempty"`
	IPv6       string   `json:"ipv6,omitempty"`
	DNSServers []string `json:"dnsServers,omitempty"`
}

type DNSForDevice func(context.Context, string) ([]string, error)

// BindingNeedsRefresh detects DHCP/PPPoE source changes without a network
// request. Link-down is handled by the WAN model, not by restoring auto-route.
func BindingNeedsRefresh(b Binding) bool {
	if b.Device == "" {
		return false
	}
	iface, err := net.InterfaceByName(b.Device)
	if err != nil {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	assigned := map[string]bool{}
	for _, a := range addrs {
		ip, _, err := net.ParseCIDR(a.String())
		if err == nil {
			assigned[ip.String()] = true
		}
	}
	return (b.IPv4 != "" && !assigned[b.IPv4]) || (b.IPv6 != "" && !assigned[b.IPv6])
}

func InspectBinding(device string) (Binding, error) {
	iface, err := net.InterfaceByName(device)
	if err != nil {
		return Binding{}, err
	}
	if iface.Flags&net.FlagUp == 0 {
		return Binding{}, errors.New("provider interface is down")
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return Binding{}, err
	}
	b := Binding{Device: device}
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil || !ip.IsGlobalUnicast() {
			continue
		}
		if ip.To4() != nil {
			if b.IPv4 == "" {
				b.IPv4 = ip.String()
			}
		} else if b.IPv6 == "" {
			b.IPv6 = ip.String()
		}
	}
	if err := b.Validate(); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func (b Binding) Validate() error {
	if b.Device == "" || len(b.Device) > 15 || strings.ContainsAny(b.Device, "/\\ \t\r\n\x00") {
		return errors.New("invalid provider device")
	}
	if b.IPv4 == "" && b.IPv6 == "" {
		return errors.New("provider has no usable source address")
	}
	for _, pair := range []struct {
		ip string
		v4 bool
	}{{b.IPv4, true}, {b.IPv6, false}} {
		if pair.ip == "" {
			continue
		}
		ip, err := netip.ParseAddr(pair.ip)
		if err != nil || !ip.IsGlobalUnicast() || ip.Zone() != "" || ip.Is4In6() || ip.Is4() != pair.v4 {
			return errors.New("invalid provider source address")
		}
	}
	for _, server := range b.DNSServers {
		if _, err := ParseDNSServer(server); err != nil {
			return err
		}
	}
	return nil
}

func ParseDNSServer(server string) (netip.AddrPort, error) {
	var value netip.AddrPort
	if ip, err := netip.ParseAddr(server); err == nil {
		value = netip.AddrPortFrom(ip, 53)
	} else {
		var err error
		value, err = netip.ParseAddrPort(server)
		if err != nil {
			return value, errors.New("provider DNS must be an IP address with optional port")
		}
	}
	value = netip.AddrPortFrom(value.Addr().Unmap(), value.Port())
	if value.Port() == 0 || value.Addr().Zone() != "" || !value.Addr().IsGlobalUnicast() {
		return value, errors.New("invalid provider DNS address")
	}
	return value, nil
}

// ProviderDNS accepts only unscoped name servers explicitly assigned to the
// requested NDMS provider. Global and split-DNS records are never substituted.
func ProviderDNS(raw []byte, providerID string) ([]string, error) {
	if providerID == "" {
		return nil, errors.New("provider ID is empty")
	}
	var rows []providerDNSRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode provider DNS: %w", err)
	}
	return filterProviderDNS(rows, providerID)
}

// ProviderRuntimeDNS extracts provider-scoped unscoped DNS servers from NDMS
// runtime /show/ip/name-server (e.g. DHCP-assigned ISP resolvers). Global
// (empty interface) and split-DNS records are still rejected.
func ProviderRuntimeDNS(raw []byte, providerID string) ([]string, error) {
	if providerID == "" {
		return nil, errors.New("provider ID is empty")
	}
	var doc struct {
		Server []providerDNSRow `json:"server"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode runtime provider DNS: %w", err)
	}
	return filterProviderDNS(doc.Server, providerID)
}

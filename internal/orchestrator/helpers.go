package orchestrator

import (
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// StoredToConfig converts storage.AWGTunnel to tunnel.Config. Shared
// with internal/tunnel/service — this package owns the canonical
// storage→runtime translation since the orchestrator drives the
// lifecycle that consumes tunnel.Config.
func StoredToConfig(stored *storage.AWGTunnel) tunnel.Config {
	names := tunnel.NewNames(stored.ID)
	ipv4, ipv6 := SplitAddresses(stored.Interface.Address)
	prefix := AddressPrefixOf(stored.Interface.Address)
	var dns []string
	if stored.Interface.DNS != "" {
		for _, part := range strings.Split(stored.Interface.DNS, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				dns = append(dns, part)
			}
		}
	}
	return tunnel.Config{
		ID:            stored.ID,
		Name:          stored.Name,
		Address:       ipv4,
		AddressPrefix: prefix,
		AddressIPv6:   ipv6,
		MTU:           stored.Interface.MTU,
		DNS:           dns,
		ConfPath:      names.ConfPath,
		ISPInterface:  stored.ISPInterface,
	}
}

// AddressPrefixOf возвращает длину префикса IPv4-адреса из поля Address
// записи ("10.55.0.2/24" → 24). Ноль — префикс не задан либо не разбирается.
//
// Отдельно от SplitAddresses намеренно: та отдаёт адрес БЕЗ маски, потому что
// её результат сравнивают с другими адресами, и префикс там всё ломал бы.
func AddressPrefixOf(address string) int {
	prefix := 0
	for _, part := range strings.Split(address, ",") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "/")
		host := part
		if idx != -1 {
			host = part[:idx]
		}
		if host == "" || strings.Contains(host, ":") {
			continue
		}
		// Побеждает ПОСЛЕДНЯЯ IPv4-часть — та же, что выбирает
		// SplitAddresses. Иначе при двух адресах взяли бы адрес одной части
		// и маску другой.
		prefix = 0
		if idx == -1 {
			continue
		}
		if n, err := strconv.Atoi(part[idx+1:]); err == nil && n >= 0 && n <= 32 {
			prefix = n
		}
	}
	return prefix
}

// SplitAddresses splits a WireGuard Address field (which may contain
// comma-separated IPv4 and IPv6 addresses) into separate values.
// The CIDR prefix is stripped — operators add the mask themselves.
func SplitAddresses(address string) (ipv4, ipv6 string) {
	for _, part := range strings.Split(address, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		host := part
		if idx := strings.Index(part, "/"); idx != -1 {
			host = part[:idx]
		}
		if strings.Contains(host, ":") {
			ipv6 = host
		} else {
			ipv4 = host
		}
	}
	return
}

// ifaceNameForTunnel returns the kernel interface name for a tunnel.
func ifaceNameForTunnel(stored *storage.AWGTunnel) string {
	if stored.Backend == "nativewg" {
		return nwg.NewNWGNames(stored.NWGIndex).IfaceName
	}
	return tunnel.NewNames(stored.ID).IfaceName
}

// collectManagedIfaceNames returns interface names for all stored tunnels.
func collectManagedIfaceNames(store *storage.AWGTunnelStore) []string {
	tunnels, err := store.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(tunnels))
	for _, t := range tunnels {
		names = append(names, ifaceNameForTunnel(&t))
	}
	return names
}

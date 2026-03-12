package service

import (
	"fmt"
	"net"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// checkStoredAddressConflicts checks if any stored tunnel shares an IP address
// with the given address string. Returns human-readable warning messages.
// excludeID is skipped (used for Update to avoid warning about self).
func checkStoredAddressConflicts(store *storage.AWGTunnelStore, address, excludeID string) []string {
	newIPv4, newIPv6 := splitAddresses(address)
	if newIPv4 == "" && newIPv6 == "" {
		return nil
	}

	tunnels, err := store.List()
	if err != nil {
		return nil
	}

	var warnings []string
	for _, t := range tunnels {
		if t.ID == excludeID {
			continue
		}
		storedIPv4, storedIPv6 := splitAddresses(t.Interface.Address)

		if newIPv4 != "" && storedIPv4 == newIPv4 {
			names := tunnel.NewNames(t.ID)
			warnings = append(warnings, fmt.Sprintf(
				"Адрес %s совпадает с туннелем \"%s\" (%s). Одновременный запуск невозможен",
				newIPv4, t.Name, names.IfaceName,
			))
		}
		if newIPv6 != "" && storedIPv6 == newIPv6 {
			names := tunnel.NewNames(t.ID)
			warnings = append(warnings, fmt.Sprintf(
				"Адрес %s совпадает с туннелем \"%s\" (%s). Одновременный запуск невозможен",
				newIPv6, t.Name, names.IfaceName,
			))
		}
	}
	return warnings
}

// checkSystemAddressConflict checks if ipv4 or ipv6 is already assigned to any
// system network interface. excludeIfaceNames are excluded from the check
// (all managed tunnel interfaces — avoids false positives when addresses linger
// after incomplete cleanup). Returns ErrAddressInUse if a conflict is found.
func checkSystemAddressConflict(ipv4, ipv6 string, excludeIfaceNames []string) error {
	if ipv4 == "" && ipv6 == "" {
		return nil
	}

	excludeSet := make(map[string]struct{}, len(excludeIfaceNames))
	for _, name := range excludeIfaceNames {
		excludeSet[name] = struct{}{}
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return nil // can't check — don't block start
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if _, ok := excludeSet[iface.Name]; ok {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.String()

			if ipv4 != "" && ip == ipv4 {
				return fmt.Errorf("%w: адрес %s уже назначен интерфейсу %s", tunnel.ErrAddressInUse, ipv4, iface.Name)
			}
			if ipv6 != "" && ip == ipv6 {
				return fmt.Errorf("%w: адрес %s уже назначен интерфейсу %s", tunnel.ErrAddressInUse, ipv6, iface.Name)
			}
		}
	}

	return nil
}

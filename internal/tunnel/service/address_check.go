package service

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// checkStoredAddressConflicts checks if any stored tunnel shares an IP address
// with the given address string. Returns human-readable warning messages.
// excludeID is skipped (used for Update to avoid warning about self).
func checkStoredAddressConflicts(store *storage.AWGTunnelStore, address, excludeID string) []string {
	newIPv4, newIPv6 := orchestrator.SplitAddresses(address)
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
		storedIPv4, storedIPv6 := orchestrator.SplitAddresses(t.Interface.Address)

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


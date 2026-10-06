package service

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// StoredAddressConflicts checks if any stored tunnel shares an IP address
// with the given address string. Returns human-readable warning messages.
// excludeID is skipped (used for Update to avoid warning about self).
// Экспортирована для проводки proxyTunnelImporter (wdttlink.TunnelImporter):
// проверка нужна ДО создания записи (#869).
func StoredAddressConflicts(store *storage.AWGTunnelStore, address, excludeID string) []string {
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

		// Имя интерфейса — по backend'у (nwgN у NativeWG, живое имя у
		// wdtt-raw): tunnel.NewNames по ID дал бы чужой opkgtunN (F589).
		ifaceName, _ := storedIfaceNames(&t)

		if newIPv4 != "" && storedIPv4 == newIPv4 {
			warnings = append(warnings, fmt.Sprintf(
				"Адрес %s совпадает с туннелем \"%s\" (%s). Одновременный запуск невозможен",
				newIPv4, t.Name, ifaceName,
			))
		}
		if newIPv6 != "" && storedIPv6 == newIPv6 {
			warnings = append(warnings, fmt.Sprintf(
				"Адрес %s совпадает с туннелем \"%s\" (%s). Одновременный запуск невозможен",
				newIPv6, t.Name, ifaceName,
			))
		}
	}
	return warnings
}

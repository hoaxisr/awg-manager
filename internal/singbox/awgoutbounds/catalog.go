// internal/singbox/awgoutbounds/catalog.go
package awgoutbounds

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AWGTunnelInfo is the projection of one managed AWG tunnel that the
// catalog needs. Adapter lives in cmd/awg-manager and pulls fields
// from storage.AWGTunnel + tunnel.NewNames / nwg.NewNWGNames.
type AWGTunnelInfo struct {
	ID           string
	Name         string
	BackendIface string // resolved kernel iface name (t2sN for kernel, nwgN for NativeWG)
}

// SystemTunnelInfo is the projection of one Keenetic-native (NDMS)
// WireGuard tunnel. Mirrors deviceproxy.SystemTunnel field-by-field.
type SystemTunnelInfo struct {
	ID            string
	InterfaceName string
	Description   string
}

// AWGTunnelStore returns managed AWG tunnels in storage. Implementation
// in cmd/awg-manager wraps storage.AWGTunnelStore.List().
type AWGTunnelStore interface {
	List(ctx context.Context) ([]AWGTunnelInfo, error)
}

// SystemTunnelQuery returns Keenetic-native WireGuard tunnels. Same
// shape as deviceproxy.SystemTunnelQuery — DI in main.go injects the
// same backing implementation into both.
type SystemTunnelQuery interface {
	List(ctx context.Context) ([]SystemTunnelInfo, error)
}

// enumerate combines managed and system tunnels into the canonical
// AWGEntry list, filtering out tunnels whose kernel iface is missing
// from /sys/class/net (would FATAL sing-box on bind_interface).
//
// Dedup rule: when an iface name appears in both stores, the managed
// entry wins (it has a stable storage ID; system listing depends on
// NDMS state which can flap).
func (s *ServiceImpl) enumerate(ctx context.Context) ([]AWGEntry, error) {
	var out []AWGEntry
	seen := make(map[string]bool)

	if s.deps.AWGTunnels != nil {
		tuns, err := s.deps.AWGTunnels.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range tuns {
			if t.BackendIface == "" {
				continue
			}
			if !s.ifaceExists(t.BackendIface) {
				continue
			}
			if seen[t.BackendIface] {
				continue
			}
			seen[t.BackendIface] = true
			out = append(out, AWGEntry{
				Tag:   ManagedTag(t.ID),
				Label: t.Name,
				Kind:  "managed",
				Iface: t.BackendIface,
			})
		}
	}

	if s.deps.SystemTunnels != nil {
		tuns, err := s.deps.SystemTunnels.List(ctx)
		if err != nil {
			// Отказ системного стора возвращается ВМЕСТЕ с уже собранной
			// managed-частью: у двух потребителей enumerate разные требования.
			// writeFile нужна ПОЛНОТА — он на ошибке прекращает запись, иначе
			// транзиентный сбой NDMS переписал бы 15-awg.json без всех
			// awg-sys-* и триггернул reload на усечённом конфиге (F83).
			// ListTags нужно ЛУЧШЕЕ ИЗ ДОСТУПНОГО — глухой отказ уносил бы и
			// managed-теги, роняя в fallback инстансы deviceproxy, которые к
			// системным туннелям вообще не привязаны.
			return out, fmt.Errorf("system tunnels: %w", err)
		}

		// Build a fast-lookup set of managed-server interface names so we can
		// skip them in the system-tunnel branch. Keenetic's NDMS exposes our
		// servers identically to user-created system tunnels — only the
		// awg-manager settings know which is which.
		managedSet := make(map[string]bool)
		if s.deps.ManagedServers != nil {
			for _, name := range s.deps.ManagedServers.ManagedServerInterfaceNames(ctx) {
				if name != "" {
					managedSet[name] = true
				}
			}
		}

		for _, t := range tuns {
			if t.InterfaceName == "" {
				continue
			}
			if !s.ifaceExists(t.InterfaceName) {
				continue
			}
			if seen[t.InterfaceName] {
				continue
			}
			if managedSet[t.InterfaceName] {
				continue
			}
			// Defense-in-depth: an awg-manager-created server interface is
			// always tagged with description "AWGM ..." (see
			// internal/managed/types.go::ManagedServerDescription). If the
			// managedSet check above missed it (e.g., legacy storage entry
			// without InterfaceName populated), fall back to description
			// prefix match.
			if strings.HasPrefix(t.Description, "AWGM") {
				continue
			}
			seen[t.InterfaceName] = true
			label := t.Description
			if label == "" {
				label = t.ID
			}
			out = append(out, AWGEntry{
				Tag:   SystemTag(t.ID),
				Label: label,
				Kind:  "system",
				Iface: t.InterfaceName,
			})
		}
	}

	return out, nil
}

// ifaceExists checks /sys/class/net/<name>. Override `sysClassNet`
// in tests to point at a tempdir.
func (s *ServiceImpl) ifaceExists(name string) bool {
	root := s.sysClassNet
	if root == "" {
		root = "/sys/class/net"
	}
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

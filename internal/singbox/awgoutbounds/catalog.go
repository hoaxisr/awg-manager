// internal/singbox/awgoutbounds/catalog.go
package awgoutbounds

import (
	"context"
	"os"
	"path/filepath"
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
			// System failure is not fatal — managed-only output is still
			// useful. Caller can log via app log; we don't have it here.
			return out, nil
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

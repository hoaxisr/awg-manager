// internal/routing/catalog.go
package routing

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// TunnelEntry represents a tunnel or interface available for routing.
type TunnelEntry struct {
	ID        string `json:"id"`        // "awgm0", "system:Wireguard0", "wan:apcli1"
	Name      string `json:"name"`      // "WARPm2_88", "Wireguard0", "gpon5G_2"
	Type      string `json:"type"`      // "managed", "system", "wan"
	Status    string `json:"status"`    // "running", "stopped", "disabled", "up", "down"
	Available bool   `json:"available"` // can route traffic right now
}

// RoutingSnapshot holds all routing data for SSE snapshots.
type RoutingSnapshot struct {
	DnsRoutes        interface{} `json:"dnsRoutes"`
	StaticRoutes     interface{} `json:"staticRoutes"`
	Tunnels          interface{} `json:"tunnels"`
	AccessPolicies   interface{} `json:"accessPolicies"`
	PolicyDevices    interface{} `json:"policyDevices"`
	PolicyInterfaces interface{} `json:"policyInterfaces"`
	ClientRoutes     interface{} `json:"clientRoutes"`
}

// Catalog provides a unified tunnel listing and ID resolution for all routing subsystems.
type Catalog interface {
	// ListAll returns deduplicated list for UI dropdowns.
	ListAll(ctx context.Context) []TunnelEntry

	// ResolveInterface maps tunnelID to interface name for routing commands.
	// Returns NDMS name on OS5, kernel name on OS4.
	ResolveInterface(ctx context.Context, tunnelID string) (string, error)

	// Exists checks if tunnelID refers to a valid tunnel or interface.
	Exists(ctx context.Context, tunnelID string) bool

	// GetKernelIface resolves tunnelID to kernel interface name.
	// Returns empty string and false if tunnel is not running.
	GetKernelIface(ctx context.Context, tunnelID string) (ifaceName string, running bool)

	// SnapshotAll collects all routing data for SSE snapshot.
	SnapshotAll(ctx context.Context) *RoutingSnapshot

	// GetKernelIfaceName resolves tunnelID to the kernel-level interface name
	// for HydraRoute DirectRoute (not NDMS name).
	GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error)
}

// TunnelWithStatus is the tunnel info Catalog needs from the provider.
type TunnelWithStatus struct {
	ID       string
	Name     string
	Backend  string       // "kernel" or "nativewg"
	State    tunnel.State
	NWGIndex int          // only for nativewg
}

// TunnelProvider abstracts the tunnel service for Catalog.
type TunnelProvider interface {
	ListTunnels(ctx context.Context) ([]TunnelWithStatus, error)
	GetState(ctx context.Context, tunnelID string) tunnel.StateInfo
	WANModel() *wan.Model
}

// NDMSClient is the subset of ndms.Client used by Catalog.
type NDMSClient interface {
	ListWireguardInterfaces(ctx context.Context) ([]ndms.WireguardInterfaceInfo, error)
	GetSystemName(ctx context.Context, ndmsName string) string
}

// StoreClient is the subset of storage used by Catalog.
type StoreClient interface {
	Get(id string) (StoreEntry, error)
	Exists(id string) bool
}

// StoreEntry holds the fields Catalog needs from a stored tunnel.
type StoreEntry struct {
	Backend  string
	NWGIndex int
}

// SnapshotFunc is a function that returns a piece of routing data for the snapshot.
// Returns nil on error or if the service is unavailable.
type SnapshotFunc func(ctx context.Context) interface{}

// CatalogImpl implements the Catalog interface.
type CatalogImpl struct {
	provider TunnelProvider
	ndms     NDMSClient
	store    StoreClient

	// Snapshot providers (nil-safe). Set via SetSnapshotProvider.
	snapDnsRoutes        SnapshotFunc
	snapStaticRoutes     SnapshotFunc
	snapAccessPolicies   SnapshotFunc
	snapPolicyDevices    SnapshotFunc
	snapPolicyInterfaces SnapshotFunc
	snapClientRoutes     SnapshotFunc
}

// NewCatalog creates a new CatalogImpl.
func NewCatalog(provider TunnelProvider, ndms NDMSClient, store StoreClient) *CatalogImpl {
	return &CatalogImpl{provider: provider, ndms: ndms, store: store}
}

// ListAll returns a deduplicated list of all tunnels and interfaces for UI dropdowns.
func (c *CatalogImpl) ListAll(ctx context.Context) []TunnelEntry {
	var result []TunnelEntry
	managed := make(map[string]bool)

	// 1. Managed tunnels
	tunnels, err := c.provider.ListTunnels(ctx)
	if err == nil {
		for _, t := range tunnels {
			ndmsName := c.resolveNDMSName(t)
			if ndmsName == "" {
				continue
			}
			managed[ndmsName] = true

			name := ndmsName
			if t.Name != "" {
				name = t.Name
			}

			result = append(result, TunnelEntry{
				ID:        t.ID,
				Name:      name,
				Type:      "managed",
				Status:    t.State.String(),
				Available: true, // always selectable — route activates when tunnel starts
			})
		}
	}

	// 2. System interfaces (unmanaged WireGuard)
	if c.ndms != nil {
		wgIfaces, err := c.ndms.ListWireguardInterfaces(ctx)
		if err == nil {
			for _, iface := range wgIfaces {
				if managed[iface.Name] {
					continue
				}
				name := iface.Name
				if iface.Description != "" {
					name = iface.Description
				}
				result = append(result, TunnelEntry{
					ID:        "system:" + iface.Name,
					Name:      name,
					Type:      "system",
					Status:    "up",
					Available: true,
				})
			}
		}
	}

	// 3. WAN interfaces
	wanModel := c.provider.WANModel()
	if wanModel != nil {
		for _, iface := range wanModel.ForUI() {
			name := iface.Name
			if iface.Label != "" {
				name = iface.Label
			}
			status := "down"
			if iface.Up {
				status = "up"
			}
			result = append(result, TunnelEntry{
				ID:        "wan:" + iface.Name,
				Name:      name,
				Type:      "wan",
				Status:    status,
				Available: iface.Up,
			})
		}
	}

	// Never return nil — always return empty slice.
	if result == nil {
		return []TunnelEntry{}
	}
	return result
}

// ResolveInterface maps tunnelID to the interface name used in routing commands.
// Returns NDMS name on OS5, kernel name on OS4.
func (c *CatalogImpl) ResolveInterface(ctx context.Context, tunnelID string) (string, error) {
	// WAN: "wan:ppp0" → NDMS ID via WAN model
	if strings.HasPrefix(tunnelID, "wan:") {
		kernelName := strings.TrimPrefix(tunnelID, "wan:")
		wanModel := c.provider.WANModel()
		if wanModel == nil {
			return "", fmt.Errorf("WAN model not available")
		}
		if ndmsID := wanModel.IDFor(kernelName); ndmsID != "" {
			return ndmsID, nil
		}
		return "", fmt.Errorf("WAN interface %s not found", kernelName)
	}

	// System: "system:Wireguard0" → "Wireguard0"
	if tunnel.IsSystemTunnel(tunnelID) {
		return tunnel.SystemTunnelName(tunnelID), nil
	}

	// Managed: check NativeWG first
	if entry, err := c.store.Get(tunnelID); err == nil && entry.Backend == "nativewg" {
		return nwg.NewNWGNames(entry.NWGIndex).NDMSName, nil
	}

	// Kernel tunnel
	names := tunnel.NewNames(tunnelID)
	if names.NDMSName == "" {
		return names.IfaceName, nil // OS4: "awgm0"
	}
	return names.NDMSName, nil // OS5: "OpkgTun10"
}

// Exists checks if tunnelID refers to a valid tunnel or interface.
func (c *CatalogImpl) Exists(ctx context.Context, tunnelID string) bool {
	if strings.HasPrefix(tunnelID, "wan:") {
		kernelName := strings.TrimPrefix(tunnelID, "wan:")
		wanModel := c.provider.WANModel()
		return wanModel != nil && wanModel.IDFor(kernelName) != ""
	}
	if tunnel.IsSystemTunnel(tunnelID) {
		ndmsName := tunnel.SystemTunnelName(tunnelID)
		kernelName := c.ndms.GetSystemName(ctx, ndmsName)
		return kernelName != "" && kernelName != ndmsName
	}
	return c.store.Exists(tunnelID)
}

// GetKernelIface resolves tunnelID to kernel interface name.
// Returns empty string and false if tunnel is not running.
func (c *CatalogImpl) GetKernelIface(ctx context.Context, tunnelID string) (string, bool) {
	if tunnel.IsSystemTunnel(tunnelID) {
		ndmsName := tunnel.SystemTunnelName(tunnelID)
		kernelName := c.ndms.GetSystemName(ctx, ndmsName)
		if kernelName == "" || kernelName == ndmsName {
			return "", false
		}
		return kernelName, true
	}

	si := c.provider.GetState(ctx, tunnelID)
	if si.State != tunnel.StateRunning {
		return "", false
	}

	if entry, err := c.store.Get(tunnelID); err == nil && entry.Backend == "nativewg" {
		return nwg.NewNWGNames(entry.NWGIndex).IfaceName, true
	}
	return tunnel.NewNames(tunnelID).IfaceName, true
}

// GetKernelIfaceName resolves tunnelID to the kernel-level interface name
// for HydraRoute DirectRoute (not NDMS name).
func (c *CatalogImpl) GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error) {
	// WAN: "wan:ppp0" → "ppp0"
	if strings.HasPrefix(tunnelID, "wan:") {
		return strings.TrimPrefix(tunnelID, "wan:"), nil
	}
	// System: "system:Wireguard0" → "Wireguard0"
	if tunnel.IsSystemTunnel(tunnelID) {
		return tunnel.SystemTunnelName(tunnelID), nil
	}
	// NativeWG: kernel iface is "nwgX"
	if entry, err := c.store.Get(tunnelID); err == nil && entry.Backend == "nativewg" {
		return nwg.NewNWGNames(entry.NWGIndex).IfaceName, nil
	}
	// Managed kernel: OS4 "awgm0" → "awgm0", OS5 "awg10" → "opkgtun10"
	names := tunnel.NewNames(tunnelID)
	return names.IfaceName, nil
}

// SetSnapshotProvider registers a named snapshot provider function.
// Valid names: "dnsRoutes", "staticRoutes", "accessPolicies",
// "policyDevices", "policyInterfaces", "clientRoutes".
func (c *CatalogImpl) SetSnapshotProvider(name string, fn SnapshotFunc) {
	switch name {
	case "dnsRoutes":
		c.snapDnsRoutes = fn
	case "staticRoutes":
		c.snapStaticRoutes = fn
	case "accessPolicies":
		c.snapAccessPolicies = fn
	case "policyDevices":
		c.snapPolicyDevices = fn
	case "policyInterfaces":
		c.snapPolicyInterfaces = fn
	case "clientRoutes":
		c.snapClientRoutes = fn
	}
}

// SnapshotAll collects all routing data for SSE snapshot.
// Providers that are nil or return nil produce empty slices (never null in JSON).
func (c *CatalogImpl) SnapshotAll(ctx context.Context) *RoutingSnapshot {
	empty := []interface{}{}
	snap := &RoutingSnapshot{
		DnsRoutes:        empty,
		StaticRoutes:     empty,
		Tunnels:          c.ListAll(ctx),
		AccessPolicies:   empty,
		PolicyDevices:    empty,
		PolicyInterfaces: empty,
		ClientRoutes:     empty,
	}

	if v := c.callSnap(ctx, c.snapDnsRoutes); v != nil {
		snap.DnsRoutes = v
	}
	if v := c.callSnap(ctx, c.snapStaticRoutes); v != nil {
		snap.StaticRoutes = v
	}
	if v := c.callSnap(ctx, c.snapAccessPolicies); v != nil {
		snap.AccessPolicies = v
	}
	if v := c.callSnap(ctx, c.snapPolicyDevices); v != nil {
		snap.PolicyDevices = v
	}
	if v := c.callSnap(ctx, c.snapPolicyInterfaces); v != nil {
		snap.PolicyInterfaces = v
	}
	if v := c.callSnap(ctx, c.snapClientRoutes); v != nil {
		snap.ClientRoutes = v
	}

	return snap
}

// callSnap safely calls a snapshot function, returning nil if fn is nil.
func (c *CatalogImpl) callSnap(ctx context.Context, fn SnapshotFunc) interface{} {
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// resolveNDMSName returns the NDMS or kernel interface name for a managed tunnel.
func (c *CatalogImpl) resolveNDMSName(t TunnelWithStatus) string {
	if t.Backend == "nativewg" {
		return nwg.NewNWGNames(t.NWGIndex).NDMSName
	}
	names := tunnel.NewNames(t.ID)
	if names.NDMSName != "" {
		return names.NDMSName
	}
	return names.IfaceName // OS4 kernel: "awgm0"
}

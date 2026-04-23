// internal/routing/catalog.go
package routing

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// TunnelEntry represents a tunnel or interface available for routing.
type TunnelEntry struct {
	ID        string `json:"id"`        // "awgm0", "system:Wireguard0", "wan:apcli1"
	Name      string `json:"name"`      // "WARPm2_88", "Wireguard0", "gpon5G_2"
	Iface     string `json:"iface"`     // kernel interface name ("nwg0", "opkgtun10", "ppp0", "Wireguard0")
	Type      string `json:"type"`      // "managed", "system", "wan"
	Status    string `json:"status"`    // "running", "stopped", "disabled", "up", "down"
	Available bool   `json:"available"` // can route traffic right now
}

// RoutingSnapshot holds all routing data for SSE snapshots.
//
// Missing lists the section keys whose provider failed or was unavailable
// when this snapshot was built. The corresponding payload field falls back
// to an empty slice so the frontend can safely render; Missing distinguishes
// "successfully empty" from "could not load" per section.
type RoutingSnapshot struct {
	DnsRoutes        interface{} `json:"dnsRoutes"`
	StaticRoutes     interface{} `json:"staticRoutes"`
	Tunnels          interface{} `json:"tunnels"`
	AccessPolicies   interface{} `json:"accessPolicies"`
	PolicyDevices    interface{} `json:"policyDevices"`
	PolicyInterfaces interface{} `json:"policyInterfaces"`
	ClientRoutes     interface{} `json:"clientRoutes"`
	HydraRouteStatus interface{} `json:"hydrarouteStatus,omitempty"`
	Missing          []string    `json:"missing"`
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

// interfaceQueries is the subset of *query.Queries.Interfaces used by Catalog.
// Narrow interface — easy to mock, insulates catalog from query store details.
type interfaceQueries interface {
	List(ctx context.Context) ([]ndms.Interface, error)
	ResolveSystemName(ctx context.Context, ndmsName string) string
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

// SnapshotFunc returns one piece of routing data for a snapshot.
// A non-nil error signals that the section could not be loaded — the caller
// records this in RoutingSnapshot.Missing so the UI can surface a
// "not loaded" state distinct from a successful empty result.
type SnapshotFunc func(ctx context.Context) (interface{}, error)

// CatalogImpl implements the Catalog interface.
type CatalogImpl struct {
	provider TunnelProvider
	ifaces   interfaceQueries
	store    StoreClient

	// Snapshot providers (nil-safe). Set via SetSnapshotProvider.
	snapDnsRoutes        SnapshotFunc
	snapStaticRoutes     SnapshotFunc
	snapAccessPolicies   SnapshotFunc
	snapPolicyDevices    SnapshotFunc
	snapPolicyInterfaces SnapshotFunc
	snapClientRoutes         SnapshotFunc
	snapHydraRouteStatus     SnapshotFunc
}

// NewCatalog creates a new CatalogImpl.
func NewCatalog(provider TunnelProvider, ifaces interfaceQueries, store StoreClient) *CatalogImpl {
	return &CatalogImpl{provider: provider, ifaces: ifaces, store: store}
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

			iface, _ := c.GetKernelIfaceName(ctx, t.ID)
			result = append(result, TunnelEntry{
				ID:        t.ID,
				Name:      name,
				Iface:     iface,
				Type:      "managed",
				Status:    t.State.String(),
				Available: true, // always selectable — route activates when tunnel starts
			})
		}
	}

	// 2. System interfaces (unmanaged WireGuard/Proxy/OpkgTun)
	if c.ifaces != nil {
		all, err := c.ifaces.List(ctx)
		if err == nil {
			for _, iface := range all {
				t := strings.ToLower(iface.Type)
				if t != "wireguard" && t != "proxy" && t != "opkgtun" {
					continue
				}
				if managed[iface.ID] {
					continue
				}
				name := iface.ID
				if iface.Description != "" {
					name = iface.Description
				}
				result = append(result, TunnelEntry{
					ID:        "system:" + iface.ID,
					Name:      name,
					Iface:     iface.ID,
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
				Iface:     iface.Name,
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
		kernelName := c.ifaces.ResolveSystemName(ctx, ndmsName)
		return kernelName != "" && kernelName != ndmsName
	}
	return c.store.Exists(tunnelID)
}

// GetKernelIface resolves tunnelID to kernel interface name.
// Returns empty string and false if tunnel is not running.
func (c *CatalogImpl) GetKernelIface(ctx context.Context, tunnelID string) (string, bool) {
	if tunnel.IsSystemTunnel(tunnelID) {
		ndmsName := tunnel.SystemTunnelName(tunnelID)
		kernelName := c.ifaces.ResolveSystemName(ctx, ndmsName)
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
//
// Returns an error if tunnelID doesn't resolve to any known tunnel — the
// caller must handle it (skip the rule, surface to the user) rather than
// silently write a garbage interface name into HydraRoute's domain.conf.
func (c *CatalogImpl) GetKernelIfaceName(ctx context.Context, tunnelID string) (string, error) {
	// WAN: "wan:ppp0" → "ppp0"
	if strings.HasPrefix(tunnelID, "wan:") {
		return strings.TrimPrefix(tunnelID, "wan:"), nil
	}
	// System: "system:Wireguard0" → "Wireguard0"
	if tunnel.IsSystemTunnel(tunnelID) {
		return tunnel.SystemTunnelName(tunnelID), nil
	}
	// Managed tunnels must exist in our storage. This guards against stale
	// rule references (e.g. a policy name leaking into TunnelID) that would
	// otherwise be silently mis-resolved by tunnel.NewNames.
	entry, err := c.store.Get(tunnelID)
	if err != nil {
		return "", fmt.Errorf("unknown tunnel %q: %w", tunnelID, err)
	}
	// NativeWG: kernel iface is "nwgX"
	if entry.Backend == "nativewg" {
		return nwg.NewNWGNames(entry.NWGIndex).IfaceName, nil
	}
	// Managed kernel: OS4 "awgm0" → "awgm0", OS5 "awg10" → "opkgtun10"
	return tunnel.NewNames(tunnelID).IfaceName, nil
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
	case "hydrarouteStatus":
		c.snapHydraRouteStatus = fn
	}
}

// SnapshotAll collects all routing data for SSE snapshot. For each registered
// provider, data fields fall back to an empty slice and the section key is
// recorded in Missing when the provider errors. Unregistered providers are
// neither filled nor reported (they are not expected to produce data).
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
		Missing:          []string{},
	}

	c.fillSection(ctx, "dnsRoutes", c.snapDnsRoutes, &snap.DnsRoutes, &snap.Missing)
	c.fillSection(ctx, "staticRoutes", c.snapStaticRoutes, &snap.StaticRoutes, &snap.Missing)
	c.fillSection(ctx, "accessPolicies", c.snapAccessPolicies, &snap.AccessPolicies, &snap.Missing)
	c.fillSection(ctx, "policyDevices", c.snapPolicyDevices, &snap.PolicyDevices, &snap.Missing)
	c.fillSection(ctx, "policyInterfaces", c.snapPolicyInterfaces, &snap.PolicyInterfaces, &snap.Missing)
	c.fillSection(ctx, "clientRoutes", c.snapClientRoutes, &snap.ClientRoutes, &snap.Missing)
	c.fillSection(ctx, "hydrarouteStatus", c.snapHydraRouteStatus, &snap.HydraRouteStatus, &snap.Missing)

	return snap
}

// fillSection runs a single provider and either assigns its value to dst or
// appends key to missing when the provider is registered but failed. Nil
// providers are skipped silently (not every section is wired in every build).
func (c *CatalogImpl) fillSection(ctx context.Context, key string, fn SnapshotFunc, dst *interface{}, missing *[]string) {
	if fn == nil {
		return
	}
	v, err := fn(ctx)
	if err != nil {
		*missing = append(*missing, key)
		return
	}
	if v != nil {
		*dst = v
	}
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

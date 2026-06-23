package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/tunnel/sysinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// Compile-time guarantee that routerAccessPolicyAdapter satisfies
// router.AccessPolicyProvider — catches interface drift at the
// declaration line instead of at the wiring callsite in main.go.
var _ router.AccessPolicyProvider = (*routerAccessPolicyAdapter)(nil)

// routerAccessPolicyAdapter projects the accesspolicy.Service surface
// into router.AccessPolicyProvider. main.go owns this projection so
// router doesn't import accesspolicy types directly.
type routerAccessPolicyAdapter struct {
	svc accesspolicy.Service
	wan *wan.Model
}

func (a *routerAccessPolicyAdapter) GetPolicyMark(ctx context.Context, name string) (string, error) {
	return a.svc.GetPolicyMark(ctx, name)
}

func (a *routerAccessPolicyAdapter) AssignDevice(ctx context.Context, mac, name string) error {
	return a.svc.AssignDevice(ctx, mac, name)
}

func (a *routerAccessPolicyAdapter) UnassignDevice(ctx context.Context, mac string) error {
	return a.svc.UnassignDevice(ctx, mac)
}

func (a *routerAccessPolicyAdapter) ListDevicesForPolicy(ctx context.Context, policyName string) ([]router.PolicyDevice, error) {
	devices, err := a.svc.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]router.PolicyDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, router.PolicyDevice{
			MAC:   d.MAC,
			IP:    d.IP,
			Name:  d.Name,
			Bound: d.Policy == policyName,
		})
	}
	return out, nil
}

func (a *routerAccessPolicyAdapter) ListPolicies(ctx context.Context) ([]router.PolicyInfo, error) {
	policies, err := a.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]router.PolicyInfo, 0, len(policies))
	for _, p := range policies {
		// Drop NDMS policies that don't belong to the built-in
		// Policy0..PolicyN pool (e.g. HR-NEO creates user-named ones).
		// They share NDMS storage but use a different policy model and
		// must not appear in the singbox-router policy picker.
		if !accesspolicy.IsStandardPolicyName(p.Name) {
			continue
		}
		mark, _ := a.svc.GetPolicyMark(ctx, p.Name) // best-effort; empty is fine
		out = append(out, router.PolicyInfo{
			Name:         p.Name,
			Description:  p.Description,
			Mark:         mark,
			DeviceCount:  p.DeviceCount,
			IsOurDefault: p.Description == "awgm-router",
		})
	}
	return out, nil
}

func (a *routerAccessPolicyAdapter) CreatePolicy(ctx context.Context, description string) (router.PolicyInfo, error) {
	// NDMS won't issue a fwmark for a policy that has no permitted
	// interface, so we MUST resolve a default WAN before creating the
	// policy. Failing fast here yields a clean diagnostic for the user
	// instead of a half-broken policy that later fails on Enable with
	// the cryptic ErrPolicyMissing.
	if a.wan == nil {
		return router.PolicyInfo{}, fmt.Errorf("WAN model unavailable; cannot auto-permit a default WAN for new policy")
	}
	iface, ok := a.wan.PreferredUp()
	if !ok {
		return router.PolicyInfo{}, fmt.Errorf("no WAN interface is up; bring up a WAN connection before creating a router policy")
	}
	ndmsID := a.wan.IDFor(iface)
	if ndmsID == "" {
		return router.PolicyInfo{}, fmt.Errorf("WAN interface %q has no NDMS id; cannot auto-permit", iface)
	}

	p, err := a.svc.Create(ctx, description)
	if err != nil {
		return router.PolicyInfo{}, err
	}
	if err := a.svc.PermitInterface(ctx, p.Name, ndmsID, 100); err != nil {
		// Best-effort cleanup: the policy was created but is now stuck
		// without a permit. Surface the error so the user knows; the
		// orphaned policy stays in NDMS for them to clean up via the
		// Access Policies UI.
		return router.PolicyInfo{}, fmt.Errorf("permit WAN %s on policy %s: %w", ndmsID, p.Name, err)
	}
	mark, _ := a.svc.GetPolicyMark(ctx, p.Name)
	return router.PolicyInfo{
		Name:         p.Name,
		Description:  p.Description,
		Mark:         mark,
		DeviceCount:  0,
		IsOurDefault: p.Description == "awgm-router",
	}, nil
}

// Compile-time guarantees for the WAN-interface and bindable-interface listers.
var _ router.WANInterfaceLister = (*routerWANInterfaceAdapter)(nil)
var _ router.BindableInterfaceLister = (*routerWANInterfaceAdapter)(nil)

// routerWANInterfaceAdapter bridges ndmsquery.InterfaceStore's ListWAN
// (returns []wan.Interface) into router.WANInterfaceLister (returns
// []router.WANInterfaceInfo). router is decoupled from concrete ndms types
// via consumer-owned interfaces (DIP), so the projection lives in main
// alongside the other router adapters.
type routerWANInterfaceAdapter struct {
	store *ndmsquery.InterfaceStore
	// nativeProxies returns kernel names of KeenOS-native (non-ours) proxy
	// interfaces. Only set on the bindable-interfaces instance; nil on the
	// WAN instance. Used by ListBindable to surface native SOCKS proxies (#323).
	nativeProxies func(context.Context) ([]string, error)
	// occupiedBinds returns kernel names already bound by an existing direct
	// outbound, excluded from the bindable list. Bindable-instance only (#323).
	occupiedBinds func(context.Context) (map[string]bool, error)
}

func (a *routerWANInterfaceAdapter) ListWAN(ctx context.Context) ([]router.WANInterfaceInfo, error) {
	ifaces, err := a.store.ListWAN(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]router.WANInterfaceInfo, 0, len(ifaces))
	for _, iface := range ifaces {
		out = append(out, router.WANInterfaceInfo{
			Name:     iface.Name,
			ID:       iface.ID,
			Label:    iface.Label,
			Up:       iface.Up,
			Priority: iface.Priority,
		})
	}
	return out, nil
}

var _ router.IngressResolver = (*routerIngressResolverAdapter)(nil)

// routerIngressResolverAdapter резолвит "managed:WireguardN" → kernel-имя
// ("nwgN") через InterfaceStore.ResolveSystemName. iface:-ref'ы router
// резолвит сам без адаптера. Живёт в main — router декаплен от конкретных
// типов internal/ndms через consumer-owned контракты (DIP), как и WAN-адаптер.
type routerIngressResolverAdapter struct {
	store *ndmsquery.InterfaceStore
}

func (a *routerIngressResolverAdapter) Resolve(ctx context.Context, ref string) string {
	const prefix = "managed:"
	if !strings.HasPrefix(ref, prefix) {
		return ""
	}
	return a.store.ResolveSystemName(ctx, strings.TrimPrefix(ref, prefix))
}

// Compile-time satisfaction for the directly-wired fakeip provisioner:
// *InterfaceCommands implements the router interface structurally, so it's
// injected without an adapter. This assertion surfaces any ndms
// method-signature drift at this declaration line.
var _ router.OpkgTunProvisioner = (*ndmscommand.InterfaceCommands)(nil)

var _ router.StaticRouteProvider = (*routerStaticRouteAdapter)(nil)

// routerStaticRouteAdapter translates router.StaticRouteSpec (router-local
// mirror) into ndmscommand.StaticRouteSpec field-for-field. The router keeps
// its own spec to stay decoupled from concrete ndms command types (DIP), so
// it's duplicated and bridged here.
type routerStaticRouteAdapter struct{ routes *ndmscommand.RouteCommands }

// toNDMSRoute translates the router-local StaticRouteSpec mirror into the
// concrete ndmscommand.StaticRouteSpec field-for-field (including V6).
func toNDMSRoute(r router.StaticRouteSpec) ndmscommand.StaticRouteSpec {
	return ndmscommand.StaticRouteSpec{
		Interface: r.Interface,
		Host:      r.Host,
		Network:   r.Network,
		Mask:      r.Mask,
		Reject:    r.Reject,
		Comment:   r.Comment,
		V6:        r.V6,
	}
}

func (a *routerStaticRouteAdapter) AddStaticRoute(ctx context.Context, r router.StaticRouteSpec) error {
	return a.routes.AddStaticRoute(ctx, toNDMSRoute(r))
}

func (a *routerStaticRouteAdapter) RemoveStaticRoute(ctx context.Context, r router.StaticRouteSpec) error {
	return a.routes.RemoveStaticRoute(ctx, toNDMSRoute(r))
}

var _ router.OpkgTunIndexLister = (*routerOpkgTunIndexAdapter)(nil)

// routerOpkgTunIndexAdapter unions kernel /sys opkgtun indices with NDMS-known
// interface names so the fakeip index allocator sees every occupied slot.
type routerOpkgTunIndexAdapter struct {
	store *ndmsquery.InterfaceStore
	log   *logging.ScopedLogger
}

func (a *routerOpkgTunIndexAdapter) LiveOpkgTunIndices(ctx context.Context) (map[int]bool, error) {
	sysNums, err := sysinfo.ListSystemInterfaces()
	if err != nil {
		// A /sys read failure under-counts occupied opkgtun indices — the
		// one direction that can cause an index collision — so log it,
		// then degrade to NDMS-only names.
		if a.log != nil {
			a.log.Warn("opkgtun-index", "", "list system interfaces failed: "+err.Error())
		}
		sysNums = nil
	}
	all, err := a.store.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for _, i := range all {
		names = append(names, i.Name)
	}
	return router.UnionOpkgTunIndices(sysNums, names), nil
}

// ListBindable returns router interfaces a user can bind a direct outbound to:
// egress-capable (security-level "public"), minus our own auto-managed
// interfaces — except KeenOS-native proxies (kernel t2sN whose NDMS ProxyN is
// not ours), which are rescued from the auto-managed exclusion (#323).
func (a *routerWANInterfaceAdapter) ListBindable(ctx context.Context) ([]router.WANInterfaceInfo, error) {
	ifaces, err := a.store.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	// On lookup error, leave native empty (fail-safe: don't offer a proxy we
	// can't prove is non-ours, avoiding a routing loop through our own t2s).
	native := map[string]bool{}
	if a.nativeProxies != nil {
		if names, e := a.nativeProxies(ctx); e == nil {
			for _, n := range names {
				native[n] = true
			}
		}
	}
	// Interfaces already bound by an existing direct outbound — don't offer
	// them again. On lookup error treat as none (a duplicate bind is harmless,
	// so fail toward offering rather than hiding).
	occupied := map[string]bool{}
	if a.occupiedBinds != nil {
		if set, e := a.occupiedBinds(ctx); e == nil {
			occupied = set
		}
	}
	return filterBindable(ifaces, native, occupied), nil
}

// filterBindable keeps egress interfaces (security-level "public") minus our
// own auto-managed ones and minus already-bound interfaces, rescuing
// KeenOS-native proxies in the native set.
func filterBindable(ifaces []ndms.AllInterface, native, occupied map[string]bool) []router.WANInterfaceInfo {
	out := make([]router.WANInterfaceInfo, 0, len(ifaces))
	for _, iface := range ifaces {
		// Egress only: drops LAN bridges, Wi-Fi APs, switch ports, LAN VLANs.
		if iface.SecurityLevel != "public" {
			continue
		}
		// Already bound by an existing direct outbound — skip the duplicate.
		if occupied[iface.Name] {
			continue
		}
		// Our own auto-managed interfaces already have outbounds; exclude them
		// unless this is a native proxy we explicitly rescue.
		if router.IsAutoManagedIface(iface.Name) && !native[iface.Name] {
			continue
		}
		out = append(out, router.WANInterfaceInfo{
			Name:  iface.Name,
			Label: iface.Label,
			Up:    iface.Up,
			Type:  iface.Type,
		})
	}
	return out
}

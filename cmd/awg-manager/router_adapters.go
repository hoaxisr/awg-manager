package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
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
// []router.WANInterfaceInfo). router can't import internal/ndms
// directly — would cycle through internal/tunnel/wan — so the
// projection lives in main alongside the other router adapters.
type routerWANInterfaceAdapter struct {
	store *ndmsquery.InterfaceStore
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
// резолвит сам без адаптера. Живёт в main — router не может импортить
// internal/ndms (цикл через internal/tunnel/wan), как и WAN-адаптер.
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

func (a *routerWANInterfaceAdapter) ListBindable(ctx context.Context) ([]router.WANInterfaceInfo, error) {
	ifaces, err := a.store.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]router.WANInterfaceInfo, 0, len(ifaces))
	for _, iface := range ifaces {
		if router.IsAutoManagedIface(iface.Name) {
			continue
		}
		out = append(out, router.WANInterfaceInfo{
			Name:  iface.Name,
			Label: iface.Label,
			Up:    iface.Up,
			Type:  iface.Type,
		})
	}
	return out, nil
}

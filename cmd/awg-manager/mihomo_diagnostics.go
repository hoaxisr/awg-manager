package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

type mihomoDiagnosticBridgeStore interface {
	ListBridges() []mihomonative.BridgeRef
}

type mihomoDiagnosticProxyLookup interface {
	LookupProxy(ctx context.Context, index int) (description string, exists bool, err error)
}

type mihomoDiagnosticKernelResolver interface {
	ResolveSystemName(ctx context.Context, ndmsName string) string
}

// mihomoDiagnosticResolver is the authoritative boundary between a persisted
// native resource and diagnostics bound to a live kernel interface. Persisted
// bridge metadata is only an allocation hint: publication gate state and the
// current NDMS owner must both agree before traffic is sent through it.
type mihomoDiagnosticResolver struct {
	store          mihomoDiagnosticBridgeStore
	proxies        mihomoDiagnosticProxyLookup
	kernel         mihomoDiagnosticKernelResolver
	exportsEnabled func() bool
	runtimeReady   func() bool
}

func (r *mihomoDiagnosticResolver) ResolveMihomoDiagnosticInterface(ctx context.Context, kind, resourceID string) (string, error) {
	kind = strings.TrimSpace(kind)
	resourceID = strings.TrimSpace(resourceID)
	if kind != "proxy" && kind != "subscription" {
		return "", fmt.Errorf("unsupported Mihomo resource kind %q", kind)
	}
	if resourceID == "" {
		return "", fmt.Errorf("Mihomo resource ID is empty")
	}
	if r == nil || r.store == nil || r.proxies == nil || r.kernel == nil {
		return "", fmt.Errorf("Mihomo bridge diagnostics are not initialized")
	}

	var target *mihomonative.BridgeRef
	for _, ref := range r.store.ListBridges() {
		if ref.Kind == kind && ref.ID == resourceID {
			copy := ref
			target = &copy
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("Mihomo %s %q has no persisted bridge", kind, resourceID)
	}
	if !target.Enabled {
		return "", fmt.Errorf("Mihomo %s %q is disabled", kind, resourceID)
	}
	if r.exportsEnabled == nil || !r.exportsEnabled() {
		return "", fmt.Errorf("Mihomo NDMS exports are disabled")
	}
	if r.runtimeReady == nil || !r.runtimeReady() {
		return "", fmt.Errorf("Mihomo bridge runtime is unavailable")
	}
	if target.Bridge.ProxyIndex < 0 {
		return "", fmt.Errorf("Mihomo %s %q has an invalid ProxyN allocation", kind, resourceID)
	}

	description, exists, err := r.proxies.LookupProxy(ctx, target.Bridge.ProxyIndex)
	if err != nil {
		return "", fmt.Errorf("inspect Mihomo Proxy%d: %w", target.Bridge.ProxyIndex, err)
	}
	wantOwner := mihomonative.BridgeOwnershipDescription(kind, resourceID)
	wantDisplayName := target.Label
	if !exists || (description != wantOwner && (wantDisplayName == "" || description != wantDisplayName)) {
		return "", fmt.Errorf("Mihomo Proxy%d is missing or owned by another resource", target.Bridge.ProxyIndex)
	}

	// Derive the NDMS name from the verified numeric allocation rather than
	// trusting ProxyInterface/KernelInterface persisted in native.json.
	ndmsName := fmt.Sprintf("Proxy%d", target.Bridge.ProxyIndex)
	iface := strings.TrimSpace(r.kernel.ResolveSystemName(ctx, ndmsName))
	if iface == "" {
		return "", fmt.Errorf("Mihomo %s %q has no live kernel interface", kind, resourceID)
	}
	return iface, nil
}

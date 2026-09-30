package singbox

import (
	"context"
	"fmt"
	"os"
)

// subscriptionProxies returns the current subscription composite proxies, or
// nil when no enumerator is wired.
func (o *Operator) subscriptionProxies() []SubscriptionProxy {
	if o.subProxies == nil {
		return nil
	}
	return o.subProxies.SubscriptionProxies()
}

func subscriptionProxyOwnership(proxies []SubscriptionProxy, includeBindable bool) (map[int]bool, map[int]map[string]bool) {
	legacyIndices := make(map[int]bool)
	owners := make(map[int]map[string]bool)
	for _, proxy := range proxies {
		if !includeBindable && proxy.Bindable {
			continue
		}
		if proxy.OwnerDescription == "" {
			legacyIndices[proxy.Index] = true
			continue
		}
		accepted := map[string]bool{proxy.OwnerDescription: true}
		if proxy.Label != "" {
			accepted[proxy.Label] = true
		}
		owners[proxy.Index] = accepted
	}
	return legacyIndices, owners
}

// MarkNeedsOrphanCleanup поднимает one-shot флаг для Reconcile —
// при следующем тике он почистит зомби-ProxyN, оставшиеся в NDMS
// после перехода в disabled-режим. CAS гарантирует ровно один sweep
// на сигнал. Вызывается из MigrateOff и из main.go на старте, если
// settings уже в disabled.
func (o *Operator) MarkNeedsOrphanCleanup() { o.needsOrphanCleanup.Store(true) }

// removeOrphanSingboxProxies собирает known tunnel tags и port-slots
// из текущего config.json и делегирует в ProxyManager. Best-effort.
func (o *Operator) removeOrphanSingboxProxies(ctx context.Context) error {
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	tunnelTags := map[string]bool{}
	portSlots := map[int]bool{}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			tunnelTags[t.Tag] = true
			slot := t.ListenPort - firstPort
			if slot >= 0 {
				portSlots[slot] = true
			}
		}
	}
	legacyProxyIdx, proxyOwners := subscriptionProxyOwnership(o.subscriptionProxies(), true)
	return o.proxyMgr.RemoveOrphanSingboxProxiesOwned(ctx, tunnelTags, portSlots, legacyProxyIdx, proxyOwners)
}

// ListNativeProxies returns kernel names of KeenOS-native (non-ours) NDMS
// Proxy interfaces — bind targets for router direct outbounds (#323). Assembles
// the same ownership sets as removeOrphanSingboxProxies and delegates.
func (o *Operator) ListNativeProxies(ctx context.Context) ([]string, error) {
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tunnelTags := map[string]bool{}
	portSlots := map[int]bool{}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			tunnelTags[t.Tag] = true
			slot := t.ListenPort - firstPort
			if slot >= 0 {
				portSlots[slot] = true
			}
		}
	}
	// Bindable managed exports (Mihomo bridges) must remain visible here so
	// sing-box router direct outbounds and HR Neo can select their t2sN.
	legacyProxyIdx, proxyOwners := subscriptionProxyOwnership(o.subscriptionProxies(), false)
	return o.proxyMgr.ListNativeProxiesOwned(ctx, tunnelTags, portSlots, legacyProxyIdx, proxyOwners)
}

func parseProxyIdx(name string) (int, error) {
	if name == "" {
		// Sentinel: tunnel has no NDMS Proxy (NDMS-proxy disabled mode).
		// Callers MUST check idx >= 0 before invoking ProxyManager.
		return -1, nil
	}
	var idx int
	n, err := fmt.Sscanf(name, proxyIfacePrefix+"%d", &idx)
	if err != nil {
		return 0, fmt.Errorf("parse proxy idx %q: %w", name, err)
	}
	if n != 1 {
		return 0, fmt.Errorf("parse proxy idx %q: expected %s<N>", name, proxyIfacePrefix)
	}
	return idx, nil
}

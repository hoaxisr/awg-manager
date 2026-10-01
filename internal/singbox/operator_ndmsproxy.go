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
	// Subscription composites are tracked by explicit proxy index (their
	// description is the user label, not a tunnel tag).
	subProxyIdx := map[int]bool{}
	for _, sp := range o.subscriptionProxies() {
		subProxyIdx[sp.Index] = true
	}
	return o.proxyMgr.RemoveOrphanSingboxProxies(ctx, tunnelTags, portSlots, subProxyIdx)
}

// deferProxyRemoval ставит метку «ProxyN туннеля tag не снят» (F562).
func (o *Operator) deferProxyRemoval(tag string) {
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	if o.deferredProxyTags == nil {
		o.deferredProxyTags = map[string]bool{}
	}
	o.deferredProxyTags[tag] = true
}

// retryDeferredProxyRemovals добирает ProxyN, снос которых RemoveTunnel
// отложил (F562). Без метки — ни конфига, ни списка (R36). Владение — то же
// правило, что у removeOrphanSingboxProxies: description == тег туннеля.
// Тег, снова занятый туннелем или меткой подписки, снимается с метки без
// команд: запись с таким description уже не отличить от живой. Снос — через
// RemoveProxy (свежий список + Confirmed); ошибка — метка остаётся до
// следующего тика.
func (o *Operator) retryDeferredProxyRemovals(ctx context.Context) {
	o.deferredProxyMu.Lock()
	tags := make(map[string]bool, len(o.deferredProxyTags))
	for t := range o.deferredProxyTags {
		tags[t] = true
	}
	o.deferredProxyMu.Unlock()
	if len(tags) == 0 {
		return
	}
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		o.log.Warn("deferred proxy removal: load config", "err", err)
		return
	}
	live := map[string]bool{}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			live[t.Tag] = true
		}
	}
	for _, sp := range o.subscriptionProxies() {
		live[sp.Label] = true
	}
	sweep := map[string]bool{}
	for t := range tags {
		if !live[t] {
			sweep[t] = true
		}
	}
	if len(sweep) > 0 {
		if err := o.proxyMgr.RemoveOrphanSingboxProxies(ctx, sweep, nil, nil); err != nil {
			o.log.Warn("deferred proxy removal failed", "err", err)
			return
		}
	}
	o.deferredProxyMu.Lock()
	for t := range tags {
		delete(o.deferredProxyTags, t)
	}
	o.deferredProxyMu.Unlock()
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
	subProxyIdx := map[int]bool{}
	for _, sp := range o.subscriptionProxies() {
		subProxyIdx[sp.Index] = true
	}
	return o.proxyMgr.ListNativeProxies(ctx, tunnelTags, portSlots, subProxyIdx)
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

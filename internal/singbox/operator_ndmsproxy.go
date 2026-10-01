package singbox

import (
	"context"
	"fmt"
	"os"
	"time"
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

// Выдержка повтора отложенного сноса: 30 с, 1 мин, 2 мин … не больше 15 мин
// (F562). Без неё отказ сноса не из-за списка давал бы список и RCI на
// каждом тике сторожа.
const (
	deferredProxyBaseDelay = 30 * time.Second
	deferredProxyMaxDelay  = 15 * time.Minute
)

// deferredProxy — состояние отложенного сноса одного description.
type deferredProxy struct {
	next  time.Time     // раньше — не пробовать; ноль — на ближайшем тике
	delay time.Duration // выдержка после последнего отказа
}

func (o *Operator) deferredClock() time.Time {
	if o.deferredNow != nil {
		return o.deferredNow()
	}
	return time.Now()
}

// deferProxyRemoval ставит метку «ProxyN с description tag не снят» (F562).
// Уже стоящая метка не сбрасывается — выдержка копится.
func (o *Operator) deferProxyRemoval(tag string) {
	if tag == "" {
		return // пустой description правилом владения не узнаётся
	}
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	if o.deferredProxies == nil {
		o.deferredProxies = map[string]*deferredProxy{}
	}
	if o.deferredProxies[tag] == nil {
		o.deferredProxies[tag] = &deferredProxy{}
	}
}

// retryDeferredProxyRemovals добирает ProxyN, снос которых отложен (F562).
// Без созревшей метки — ни конфига, ни списка (R36). Владение — то же
// правило, что у removeOrphanSingboxProxies: description == тег. При
// включённом режиме NDMS Proxy тег, снова занятый туннелем или меткой
// подписки, снимается с метки без команд: запись с таким description уже не
// отличить от живой. При выключенном живых ProxyN у нас нет вовсе (как в
// removeOrphanSingboxProxies) — сносится всё помеченное. Снос — через
// RemoveProxy (свежий список + Confirmed), по тегу отдельно: отказ одного
// откладывает только его.
func (o *Operator) retryDeferredProxyRemovals(ctx context.Context) {
	now := o.deferredClock()
	o.deferredProxyMu.Lock()
	var due []string
	for t, d := range o.deferredProxies {
		if !now.Before(d.next) {
			due = append(due, t)
		}
	}
	o.deferredProxyMu.Unlock()
	if len(due) == 0 {
		return
	}
	live := map[string]bool{}
	if o.isNDMSProxyEnabled() {
		cfg, err := o.loadConfig()
		if err != nil && !os.IsNotExist(err) {
			o.log.Warn("deferred proxy removal: load config", "err", err)
			return
		}
		if cfg != nil {
			for _, t := range cfg.Tunnels() {
				live[t.Tag] = true
			}
		}
		for _, sp := range o.subscriptionProxies() {
			live[sp.Label] = true
		}
	}
	for _, t := range due {
		if !live[t] {
			if err := o.proxyMgr.RemoveOrphanSingboxProxies(ctx, map[string]bool{t: true}, nil, nil); err != nil {
				o.deferredProxyFailed(t, now, err)
				continue
			}
		}
		o.deferredProxyMu.Lock()
		delete(o.deferredProxies, t)
		o.deferredProxyMu.Unlock()
	}
}

// deferredProxyFailed удваивает выдержку тега (до потолка). Warn — только
// когда выдержка выросла; на потолке — Debug, чтобы не писать на каждом
// повторе.
func (o *Operator) deferredProxyFailed(tag string, now time.Time, err error) {
	o.deferredProxyMu.Lock()
	d := o.deferredProxies[tag]
	if d == nil {
		o.deferredProxyMu.Unlock()
		return
	}
	prev := d.delay
	switch {
	case d.delay == 0:
		d.delay = deferredProxyBaseDelay
	case d.delay < deferredProxyMaxDelay:
		d.delay = min(2*d.delay, deferredProxyMaxDelay)
	}
	d.next = now.Add(d.delay)
	delay := d.delay
	o.deferredProxyMu.Unlock()
	if delay != prev {
		o.log.Warn("deferred proxy removal failed", "tag", tag, "retry_in", delay, "err", err)
	} else {
		o.log.Debug("deferred proxy removal failed", "tag", tag, "retry_in", delay, "err", err)
	}
}

// SubscriptionProxyRegistrar — pm для адаптера подписок, у которого отказ
// сноса ProxyN ставит ту же метку отложенного сноса, что у RemoveTunnel
// (F562): удаление подписки/группы и откаты создания глотают ошибку
// RemoveProxy. Метка — description записи (Label), найденная по индексу в
// перечне подписок: строка ещё в store, когда сервис зовёт RemoveProxy.
func (o *Operator) SubscriptionProxyRegistrar(pm *ProxyManager) *SubscriptionProxyRegistrar {
	return &SubscriptionProxyRegistrar{ProxyManager: pm, op: o}
}

// SubscriptionProxyRegistrar — см. Operator.SubscriptionProxyRegistrar.
type SubscriptionProxyRegistrar struct {
	*ProxyManager
	op *Operator
}

func (r *SubscriptionProxyRegistrar) RemoveProxy(ctx context.Context, idx int) error {
	label := ""
	for _, sp := range r.op.subscriptionProxies() {
		if sp.Index == idx {
			label = sp.Label
			break
		}
	}
	err := r.ProxyManager.RemoveProxy(ctx, idx)
	if err != nil {
		r.op.log.Warn("subscription proxy removal failed, deferred", "idx", idx, "label", label, "err", err)
		r.op.deferProxyRemoval(label)
	}
	return err
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

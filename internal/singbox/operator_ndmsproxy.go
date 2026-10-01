package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// subscriptionProxies returns the current subscription composite proxies, or
// nil when no enumerator is wired.
func (o *Operator) subscriptionProxies() []SubscriptionProxy {
	if o.subProxies == nil {
		return nil
	}
	return o.subProxies.SubscriptionProxies()
}

// MarkNeedsOrphanCleanup поднимает флаг уборки режима NDMS Proxy off:
// ближайший тик сторожа помечает наши ProxyN к сносу (orphanCleanupIfFlagged).
// Вызывается из MigrateOff и из main.go на старте, если settings уже в
// disabled.
func (o *Operator) MarkNeedsOrphanCleanup() { o.needsOrphanCleanup.Store(true) }

// orphanCleanupIfFlagged — уборка по флагу в режиме NDMS Proxy off, с тика
// сторожа при любом состоянии sing-box (F562 ревью F4). Сама ничего не
// сносит: наши ProxyN из свежего списка уходят в метки отложенного сноса, и
// снос идёт тем же путём с той же выдержкой (F2). Флаг снимается только
// после прочитанного списка; без флага — ни одного чтения.
func (o *Operator) orphanCleanupIfFlagged(ctx context.Context) {
	if !o.needsOrphanCleanup.Load() || o.isNDMSProxyEnabled() {
		return
	}
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if !o.needsOrphanCleanup.CompareAndSwap(true, false) {
		return
	}
	marks, err := o.ownedProxyMarks(ctx)
	if err != nil {
		o.needsOrphanCleanup.Store(true)
		o.log.Warn("orphan proxy cleanup: list", "err", err)
		return
	}
	for _, m := range marks {
		o.deferProxyRemoval(m.Name, m.Desc)
	}
}

// ownedProxyMarks — наши ProxyN (туннели конфига по имени+тегу, подписки по
// индексу) по свежему списку.
func (o *Operator) ownedProxyMarks(ctx context.Context) ([]ProxyMark, error) {
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tunnelProxies := map[string]string{}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			if t.ProxyInterface != "" {
				tunnelProxies[t.ProxyInterface] = t.Tag
			}
		}
	}
	subProxyIdx := map[int]bool{}
	for _, sp := range o.subscriptionProxies() {
		subProxyIdx[sp.Index] = true
	}
	return o.proxyMgr.OwnedProxies(ctx, tunnelProxies, subProxyIdx)
}

// Выдержка повтора отложенного сноса: 30 с, 1 мин, 2 мин … не больше 15 мин
// (F562). Без неё отказ сноса давал бы список и `no interface` на каждом
// тике сторожа.
const (
	deferredProxyBaseDelay = 30 * time.Second
	deferredProxyMaxDelay  = 15 * time.Minute
)

// deferredProxy — метка отложенного сноса одного ProxyN (ключ — имя).
type deferredProxy struct {
	desc  string        // description на момент метки — второе условие сноса
	next  time.Time     // раньше — не пробовать; ноль — на ближайшем тике
	delay time.Duration // выдержка после последнего отказа
	gen   uint64        // растёт на каждой метке: снятие по устаревшей попытке её не теряет
}

func (o *Operator) deferredClock() time.Time {
	if o.deferredNow != nil {
		return o.deferredNow()
	}
	return time.Now()
}

// proxyName — имя ProxyN по индексу.
func proxyName(idx int) string { return fmt.Sprintf("%s%d", proxyIfacePrefix, idx) }

// deferProxyRemoval ставит метку «ProxyN name с description desc не снят»
// (F562). Повторная метка того же имени сливается: выдержка сохраняется,
// description — последний известный, поколение растёт (F5).
func (o *Operator) deferProxyRemoval(name, desc string) {
	if name == "" {
		return
	}
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	if o.deferredProxies == nil {
		o.deferredProxies = map[string]*deferredProxy{}
	}
	d := o.deferredProxies[name]
	switch {
	case d == nil:
		d = &deferredProxy{desc: desc}
		o.deferredProxies[name] = d
		o.saveDeferredLocked()
	case d.desc != desc:
		d.desc = desc
		o.saveDeferredLocked()
	}
	d.gen++
}

// proxyRemovalDeferred — имя уже ждёт снос с выдержкой.
func (o *Operator) proxyRemovalDeferred(name string) bool {
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	return o.deferredProxies[name] != nil
}

// deferredProxiesFile — метки отложенного сноса на флеше (R38): после
// рестарта первый тик сторожа добирает их тем же путём. Выдержка не
// хранится — после рестарта первая попытка сразу.
const deferredProxiesFile = "deferred-proxy-removals.json"

// saveDeferredLocked пишет метки — зовётся ТОЛЬКО при смене набора (новая
// метка, другой description, снятие), не на каждом отказе: запись = износ
// флеша. Пустой набор — файла нет. Под deferredProxyMu.
func (o *Operator) saveDeferredLocked() {
	path := filepath.Join(o.dir, deferredProxiesFile)
	if len(o.deferredProxies) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			o.log.Warn("deferred proxy removals: remove file", "err", err)
		}
		return
	}
	marks := make([]ProxyMark, 0, len(o.deferredProxies))
	for name, d := range o.deferredProxies {
		marks = append(marks, ProxyMark{Name: name, Desc: d.desc})
	}
	slices.SortFunc(marks, func(a, b ProxyMark) int { return strings.Compare(a.Name, b.Name) })
	b, _ := json.Marshal(marks)
	if err := storage.AtomicWrite(path, b); err != nil {
		o.log.Warn("deferred proxy removals: write file", "err", err)
	}
}

// loadDeferredProxies — на старте. Нет файла — набор пуст (обычное
// состояние: пустой набор файла не держит); битый — пуст и один Warn.
func (o *Operator) loadDeferredProxies() {
	b, err := os.ReadFile(filepath.Join(o.dir, deferredProxiesFile))
	if os.IsNotExist(err) {
		return
	}
	var marks []ProxyMark
	if err == nil {
		err = json.Unmarshal(b, &marks)
	}
	if err != nil {
		o.log.Warn("deferred proxy removals: file unreadable, starting empty", "err", err)
		return
	}
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	o.deferredProxies = make(map[string]*deferredProxy, len(marks))
	for _, m := range marks {
		if m.Name != "" {
			o.deferredProxies[m.Name] = &deferredProxy{desc: m.Desc}
		}
	}
}

// desiredProxyNames — ProxyN, которые сейчас нужны: при включённом режиме
// NDMS Proxy — туннелей из конфига и подписок; при выключенном — никакие.
func (o *Operator) desiredProxyNames() (map[string]bool, error) {
	want := map[string]bool{}
	if !o.isNDMSProxyEnabled() {
		return want, nil
	}
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			if t.ProxyInterface != "" {
				want[t.ProxyInterface] = true
			}
		}
	}
	for _, sp := range o.subscriptionProxies() {
		want[proxyName(sp.Index)] = true
	}
	return want, nil
}

// retryDeferredProxyRemovals добирает созревшие метки (F562). Без них — ни
// конфига, ни списка (R36). Под migrationMu, как AddTunnels/RemoveTunnel/
// Migrate* (держат его через RCI): нужность имени проверяется под ним прямо
// перед сносом — свежедобавленный туннель с тем же ProxyN не снесётся (F3).
// Подписки migrationMu не берут, но индекс пишут в store ДО EnsureProxy, а
// перечень читается перед каждым сносом. Решение — по свежему списку:
// снос только при совпадении имени и description; нет записи или
// description другой — метка снимается (Info). Отказ — выдержка.
func (o *Operator) retryDeferredProxyRemovals(ctx context.Context) {
	now := o.deferredClock()
	type dueMark struct {
		ProxyMark
		gen uint64
	}
	o.deferredProxyMu.Lock()
	var due []dueMark
	for name, d := range o.deferredProxies {
		if !now.Before(d.next) {
			due = append(due, dueMark{ProxyMark{Name: name, Desc: d.desc}, d.gen})
		}
	}
	o.deferredProxyMu.Unlock()
	if len(due) == 0 {
		return
	}
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	var done []dueMark
	for _, m := range due {
		want, err := o.desiredProxyNames()
		if err != nil {
			o.log.Warn("deferred proxy removal: load config", "err", err)
			break
		}
		if want[m.Name] {
			o.log.Info("deferred proxy removal dropped: proxy is in use", "name", m.Name, "desc", m.Desc)
			done = append(done, m)
			continue
		}
		out, err := o.proxyMgr.RemoveMarkedProxy(ctx, m.ProxyMark)
		if err != nil {
			o.deferredProxyFailed(m.Name, now, err)
			continue
		}
		switch out {
		case MarkedAbsent:
			o.log.Info("deferred proxy removal dropped: proxy is gone", "name", m.Name)
		case MarkedForeign:
			o.log.Info("deferred proxy removal dropped: description changed", "name", m.Name, "desc", m.Desc)
		}
		done = append(done, m)
	}
	if len(done) == 0 {
		return
	}
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	changed := false
	for _, m := range done {
		if d := o.deferredProxies[m.Name]; d != nil && d.gen == m.gen {
			delete(o.deferredProxies, m.Name)
			changed = true
		}
	}
	if changed {
		o.saveDeferredLocked()
	}
}

// deferredProxyFailed удваивает выдержку имени (до потолка). Warn — только
// когда выдержка выросла; на потолке — Debug, чтобы не писать на каждом
// повторе.
func (o *Operator) deferredProxyFailed(name string, now time.Time, err error) {
	o.deferredProxyMu.Lock()
	d := o.deferredProxies[name]
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
		o.log.Warn("deferred proxy removal failed", "name", name, "retry_in", delay, "err", err)
	} else {
		o.log.Debug("deferred proxy removal failed", "name", name, "retry_in", delay, "err", err)
	}
}

// SubscriptionProxyRegistrar — pm для адаптера подписок, у которого отказ
// сноса ProxyN ставит ту же метку отложенного сноса, что у RemoveTunnel
// (F562): удаление подписки/группы и откаты создания глотают ошибку
// RemoveProxy. description метки — Label, найденный по индексу в перечне
// подписок: строка ещё в store, когда сервис зовёт RemoveProxy. Label не
// найден — метки нет: без description снос по паре невозможен.
func (o *Operator) SubscriptionProxyRegistrar(pm *ProxyManager) *SubscriptionProxyRegistrar {
	return &SubscriptionProxyRegistrar{ProxyManager: pm, op: o}
}

// SubscriptionProxyRegistrar — см. Operator.SubscriptionProxyRegistrar.
type SubscriptionProxyRegistrar struct {
	*ProxyManager
	op *Operator
}

func (r *SubscriptionProxyRegistrar) RemoveProxy(ctx context.Context, idx int) error {
	label, found := "", false
	for _, sp := range r.op.subscriptionProxies() {
		if sp.Index == idx {
			label, found = sp.Label, true
			break
		}
	}
	err := r.ProxyManager.RemoveProxy(ctx, idx)
	if err != nil {
		r.op.log.Warn("subscription proxy removal failed", "idx", idx, "label", label, "deferred", found, "err", err)
		if found {
			r.op.deferProxyRemoval(proxyName(idx), label)
		}
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

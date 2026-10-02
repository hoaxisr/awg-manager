package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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
// disabled. Общую выдержку тика не ждёт (wakeProxyCleanupLocked).
func (o *Operator) MarkNeedsOrphanCleanup() {
	o.needsOrphanCleanup.Store(true)
	o.deferredProxyMu.Lock()
	o.wakeProxyCleanupLocked()
	o.deferredProxyMu.Unlock()
}

// wakeProxyCleanupLocked — новая работа уборки (метка, флаг) пробуется на
// ближайшем тике, а не на сроке общей выдержки (до 15 мин): голая запись
// живого владельца иначе ждала бы усыновления. Ступень (cleanupDelay) не
// сбрасывается — новый отказ чтения продолжает лестницу (F597 ревью M1).
// Под deferredProxyMu.
func (o *Operator) wakeProxyCleanupLocked() { o.cleanupNext = time.Time{} }

// proxyCleanupTick — уборка ProxyN с тика сторожа (F562): флаг режима off,
// затем созревшие метки. Один полный список на обе уборки (список действия,
// F597); неудача его чтения двигает общую выдержку тика (proxyCleanupResult),
// и до её срока обе уборки не читают ничего. Успех выдержку сбрасывает.
// Отказ NDMS в сносе — своя выдержка метки (deferredProxyFailed).
func (o *Operator) proxyCleanupTick(ctx context.Context) {
	now := o.deferredClock()
	o.deferredProxyMu.Lock()
	wait := now.Before(o.cleanupNext)
	o.deferredProxyMu.Unlock()
	if wait {
		return
	}
	ctx = query.WithActionList(ctx)
	err := o.orphanCleanupIfFlagged(ctx)
	if err == nil {
		err = o.retryDeferredProxyRemovals(ctx, now)
	}
	o.proxyCleanupResult(now, err)
}

// proxyCleanupResult — итог тика уборки: err (список не прочитан) удваивает
// общую выдержку до потолка, nil её сбрасывает. Warn — только когда
// выдержка выросла; на потолке — Debug.
func (o *Operator) proxyCleanupResult(now time.Time, err error) {
	o.deferredProxyMu.Lock()
	if err == nil {
		o.cleanupDelay, o.cleanupNext = 0, time.Time{}
		o.deferredProxyMu.Unlock()
		return
	}
	prev := o.cleanupDelay
	o.cleanupDelay = nextDeferredDelay(prev)
	o.cleanupNext = now.Add(o.cleanupDelay)
	delay := o.cleanupDelay
	o.deferredProxyMu.Unlock()
	if delay != prev {
		o.log.Warn("proxy cleanup: interface list unread", "retry_in", delay, "err", err)
	} else {
		o.log.Debug("proxy cleanup: interface list unread", "retry_in", delay, "err", err)
	}
}

// nextDeferredDelay — следующая ступень выдержки: 30 с, удвоение, потолок.
func nextDeferredDelay(d time.Duration) time.Duration {
	if d == 0 {
		return deferredProxyBaseDelay
	}
	return min(2*d, deferredProxyMaxDelay)
}

// orphanCleanupIfFlagged — уборка по флагу в режиме NDMS Proxy off, с тика
// сторожа при любом состоянии sing-box (F562 ревью F4). Сама ничего не
// сносит: наши ProxyN из свежего списка уходят в метки отложенного сноса, и
// снос идёт тем же путём с той же выдержкой (F2). Флаг снимается только
// после прочитанного списка; без флага — ни одного чтения. Ошибка — список
// не прочитан.
func (o *Operator) orphanCleanupIfFlagged(ctx context.Context) error {
	if !o.needsOrphanCleanup.Load() || o.isNDMSProxyEnabled() {
		return nil
	}
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	if !o.needsOrphanCleanup.CompareAndSwap(true, false) {
		return nil
	}
	marks, err := o.ownedProxyMarks(ctx)
	if err != nil {
		o.needsOrphanCleanup.Store(true)
		return fmt.Errorf("orphan proxy cleanup: %w", err)
	}
	for _, m := range marks {
		o.deferProxyRemoval(m.Name, m.Desc)
	}
	return nil
}

// ownedProxyMarks — наши ProxyN (туннели конфига по имени+тегу, подписки по
// имени+Label) по свежему списку.
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
	subProxies := map[string]string{}
	for _, sp := range o.subscriptionProxies() {
		subProxies[proxyName(sp.Index)] = sp.Label
	}
	return o.proxyMgr.OwnedProxies(ctx, tunnelProxies, subProxies)
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
// description — последний известный, поколение растёт (F5). Исключение —
// голая метка (name, ""): её непустой description не затирает (L1). Она —
// доказательство своей голой записи (F577), и тик сам разберёт оба исхода
// (усыновит при владельце, снесёт без него); затёртая тегом, она указала бы
// на description, которого у записи нет, — тик снял бы метку, а голая
// сирота стала бы чужой навсегда. Гард здесь, а не у вызывающих: их
// несколько (RemoveTunnel, SubscriptionProxyRegistrar, MigrateOff, …).
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
		o.wakeProxyCleanupLocked()
	case d.desc != desc && d.desc != "":
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

// proxyOwner — кому нужен ProxyN: description (тег туннеля / Label
// подписки) и порт inbound — то, с чем его настраивает владелец.
type proxyOwner struct {
	desc string
	port int
}

// desiredProxyNames — ProxyN, которые сейчас нужны, и их владельцы: при
// включённом режиме NDMS Proxy — туннелей из конфига и подписок; при
// выключенном — никакие.
func (o *Operator) desiredProxyNames() (map[string]proxyOwner, error) {
	want := map[string]proxyOwner{}
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
				want[t.ProxyInterface] = proxyOwner{desc: t.Tag, port: t.ListenPort}
			}
		}
	}
	for _, sp := range o.subscriptionProxies() {
		want[proxyName(sp.Index)] = proxyOwner{desc: sp.Label, port: sp.Port}
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
// description другой — метка снимается (Info). Отказ NDMS — выдержка метки;
// список не прочитан — ошибка (общая выдержка тика, proxyCleanupTick), метки
// без изменений.
func (o *Operator) retryDeferredProxyRemovals(ctx context.Context, now time.Time) error {
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
		return nil
	}
	o.migrationMu.Lock()
	defer o.migrationMu.Unlock()
	var done []dueMark
	var listErr error
	for _, m := range due {
		want, err := o.desiredProxyNames()
		if err != nil {
			o.log.Warn("deferred proxy removal: load config", "err", err)
			break
		}
		owner, inUse := want[m.Name]
		if inUse && m.Desc != "" {
			o.log.Info("deferred proxy removal dropped: proxy is in use", "name", m.Name, "desc", m.Desc)
			done = append(done, m)
			continue
		}
		var out MarkedOutcome
		if inUse {
			// Наша голая сирота на имени живого владельца: не сносится, а
			// усыновляется здесь же — настройки владельца по свежему списку,
			// не дожидаясь Sync (при живом sing-box Reconcile не зовётся).
			// Записи нет или description не пуст — метка снимается без
			// команд (F577 R1, N1, N2).
			out, err = o.proxyMgr.AdoptBareProxy(ctx, m.Name, owner.port, owner.desc)
		} else {
			out, err = o.proxyMgr.RemoveMarkedProxy(ctx, m.ProxyMark)
		}
		if errors.Is(err, errProxyListUnread) {
			listErr = err // остальные упрутся в тот же список: один отказ на тик
			break
		}
		if err != nil {
			o.deferredProxyFailed(m.Name, now, err)
			continue
		}
		switch out {
		case MarkedAbsent:
			o.log.Info("deferred proxy removal dropped: proxy is gone", "name", m.Name)
		case MarkedForeign:
			o.log.Info("deferred proxy removal dropped: description changed", "name", m.Name, "desc", m.Desc)
		case MarkedAdopted:
			o.log.Info("deferred bare proxy adopted by owner", "name", m.Name, "desc", owner.desc)
		}
		done = append(done, m)
	}
	if len(done) == 0 {
		return listErr
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
	return listErr
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
	d.delay = nextDeferredDelay(prev)
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
// RemoveProxy. description метки — Label, который передал сервис (он же —
// условие сноса); чужая запись (ErrProxyForeign) метки не получает. pm
// получает метки оператора: созданное и оставленное на роутере метится, голая
// сирота с меткой усыновляется (F577).
func (o *Operator) SubscriptionProxyRegistrar(pm *ProxyManager) *SubscriptionProxyRegistrar {
	pm.marks = o
	return &SubscriptionProxyRegistrar{ProxyManager: pm, op: o}
}

// SubscriptionProxyRegistrar — см. Operator.SubscriptionProxyRegistrar.
type SubscriptionProxyRegistrar struct {
	*ProxyManager
	op *Operator
}

func (r *SubscriptionProxyRegistrar) RemoveProxy(ctx context.Context, idx int, label string) error {
	err := r.ProxyManager.RemoveProxy(ctx, idx, label)
	if err != nil {
		foreign := errors.Is(err, ErrProxyForeign)
		r.op.log.Warn("subscription proxy removal failed", "idx", idx, "label", label, "deferred", !foreign, "err", err)
		if !foreign {
			r.op.deferProxyRemoval(proxyName(idx), label)
		}
	}
	return err
}

// deferLeftProxy — созданный и оставленный на роутере ProxyN (подтверждения
// нет или снос не прошёл) уходит в метку отложенного сноса с description,
// который у него на роутере ("" у голого, F577 N1). Голая запись на имени,
// которое нужно туннелю или подписке, не сносится, а усыновляется владельцем
// (bareMarked).
func (o *Operator) deferLeftProxy(name, desc string) {
	o.log.Warn("created proxy left on router, deferred", "name", name, "desc", desc)
	o.deferProxyRemoval(name, desc)
}

// bareMarked — есть метка голой записи (name, ""): мы её создали (F577 R1).
func (o *Operator) bareMarked(name string) bool {
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	d := o.deferredProxies[name]
	return d != nil && d.desc == ""
}

// clearBareMark — голая запись name усыновлена владельцем: метка снимается.
func (o *Operator) clearBareMark(name string) {
	o.deferredProxyMu.Lock()
	defer o.deferredProxyMu.Unlock()
	if d := o.deferredProxies[name]; d != nil && d.desc == "" {
		delete(o.deferredProxies, name)
		o.saveDeferredLocked()
	}
}

// ListNativeProxies returns kernel names of KeenOS-native (non-ours) NDMS
// Proxy interfaces — bind targets for router direct outbounds (#323). Assembles
// the proxyIsOurs ownership sets and delegates.
func (o *Operator) ListNativeProxies(ctx context.Context) ([]string, error) {
	cfg, err := o.loadConfig()
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	tunnelTags := map[string]bool{}
	if cfg != nil {
		for _, t := range cfg.Tunnels() {
			tunnelTags[t.Tag] = true
		}
	}
	subProxyIdx := map[int]bool{}
	for _, sp := range o.subscriptionProxies() {
		subProxyIdx[sp.Index] = true
	}
	return o.proxyMgr.ListNativeProxies(ctx, tunnelTags, subProxyIdx)
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

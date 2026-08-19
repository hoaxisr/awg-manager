package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// SlotSubscriptionsMeta is the SlotMeta registered on startup.
// The slot constant itself lives in internal/singbox/orchestrator as
// SlotSubscriptions ("subscriptions" / 40-subscriptions.json).
var SlotSubscriptionsMeta = orchestrator.SlotMeta{
	Slot:     orchestrator.SlotSubscriptions,
	Filename: "40-subscriptions.json",
	AlwaysOn: false,
}

// subscriptionPortBase is the first listen_port reserved for subscription
// mixed inbounds. Subscription ports live in [11000, 11999] — well clear
// of the 1080-based per-tunnel range used by 10-tunnels.json.
const subscriptionPortBase = 11000

// subscriptionPortMax is the inclusive upper bound of the subscription port range.
const subscriptionPortMax = 11999

// slotConfig is the in-memory shape persisted to 40-subscriptions.json.
// It intentionally omits log/dns/experimental (those are in 00-base.json).
type slotConfig struct {
	Inbounds  []any          `json:"inbounds"`
	Outbounds []any          `json:"outbounds"`
	Route     map[string]any `json:"route"`
}

// ProxyRegistrar is the narrow interface for NDMS ProxyN management. The
// real implementation is *singbox.ProxyManager. Using a local interface avoids
// a circular import between the subscription sub-package and the parent singbox
// package.
type ProxyRegistrar interface {
	NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error)
	EnsureProxy(ctx context.Context, idx, port int, description string) error
	RemoveProxy(ctx context.Context, idx int) error
}

// ClashSelector is the narrow interface for switching a selector outbound's
// active member via the sing-box Clash API. The real implementation is
// *singbox.ClashClient. A local interface avoids circular import.
type ClashSelector interface {
	SetSelector(selectorTag, memberTag string) error
	SelectorActive(selectorTag string) (string, error)
}

// OperatorAdapter implements ConfigMutator by maintaining its own
// config slot (40-subscriptions.json) written through the orchestrator.
//
// Mutations (Add*/Update*/Remove*) only accumulate in-memory; the full slot
// is validated, written via orch.Save and SIGHUP'd ONCE when Reload() commits
// the batch (#331 — committing per mutation ran the sing-box validator O(N^2)
// times while materialising an N-server subscription). Rollback() discards an
// uncommitted batch.
//
// The adapter is safe for concurrent use — a single mutex guards all
// state reads and writes.
type OperatorAdapter struct {
	orch  *orchestrator.Orchestrator
	pm    ProxyRegistrar
	clash ClashSelector

	// singboxFeaturesFn, when non-nil, is called once per flush() Pass 1 to
	// fetch the current installed sing-box build tags (from
	// sing-box version → Tags: line). Used for cheap pre-filtering of
	// outbounds whose type requires an optional build tag (naive,
	// mieru, naive) — we drop them in Pass 1 with a human-readable reason
	// instead of letting `sing-box check` report unknown type.
	//
	// nil-safe: tests leave it unset and preFilterOutbounds treats nil as
	// "skip feature gating" (same as empty slice but preserves back-compat
	// with pre-feature-gate tests).
	singboxFeaturesFn func() []string

	mu              sync.Mutex
	cfg             slotConfig
	lastDropped     []DropReason // outbounds filtered out of the most recent flush
	preFlushDropped []DropReason // Pass-1 rejects from Add/Update before the next flush

	// pending holds a snapshot of the committed config taken at the first
	// mutation of an uncommitted batch (#331). Add*/Remove* accumulate into
	// a.cfg WITHOUT flushing; Reload() commits the whole batch with a single
	// validate+save+reload, and on failure restores this snapshot. Rollback()
	// discards an uncommitted batch (failed Create). nil = no open batch.
	pending *slotSnapshot
}

// SetSingboxFeaturesFn registers the late-bound closure used during flush()
// Pass 1 to obtain the current sing-box build tags. Pass nil to disable
// feature-based pre-filtering (tests).
func (a *OperatorAdapter) SetSingboxFeaturesFn(fn func() []string) {
	a.singboxFeaturesFn = fn
}

// slotSnapshot is a shallow copy of the mutable slot state. Shallow is enough
// because mutations replace slice elements / the rules slice wholesale and
// never edit inner outbound maps in place.
type slotSnapshot struct {
	inbounds        []any
	outbounds       []any
	rules           []any
	lastDropped     []DropReason
	preFlushDropped []DropReason
}

// beginIfNeededLocked snapshots the committed state on the first mutation of a
// batch. Caller MUST hold a.mu.
func (a *OperatorAdapter) beginIfNeededLocked() {
	if a.pending != nil {
		return
	}
	a.pending = &slotSnapshot{
		inbounds:        append([]any(nil), a.cfg.Inbounds...),
		outbounds:       append([]any(nil), a.cfg.Outbounds...),
		rules:           append([]any(nil), a.routeRules()...),
		lastDropped:     append([]DropReason(nil), a.lastDropped...),
		preFlushDropped: append([]DropReason(nil), a.preFlushDropped...),
	}
}

// restoreLocked reverts a.cfg to a snapshot. Caller MUST hold a.mu.
func (a *OperatorAdapter) restoreLocked(s *slotSnapshot) {
	a.cfg.Inbounds = s.inbounds
	a.cfg.Outbounds = s.outbounds
	a.setRouteRules(s.rules)
	a.lastDropped = s.lastDropped
	a.preFlushDropped = s.preFlushDropped
}

// Rollback discards an uncommitted batch, restoring the last committed config.
// Used by the Service when a Create fails before commit so the in-memory slot
// cannot keep a partial that the next operation would flush. No-op if no batch
// is open.
func (a *OperatorAdapter) Rollback() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending != nil {
		a.restoreLocked(a.pending)
		a.pending = nil
	}
}

// NewOperatorAdapter constructs the adapter. In production the subscription
// slot is registered via singboxorch.KnownSlots() before Bootstrap; in unit
// tests the adapter registers it itself (Register is idempotent — duplicate
// calls return ErrSlotAlreadyRegistered which is silently ignored here).
//
// clash is used by SelectClashProxy to switch the active selector member at
// runtime via the sing-box Clash API. Pass nil only in tests that don't
// exercise SetActiveMember.
func NewOperatorAdapter(orch *orchestrator.Orchestrator, pm ProxyRegistrar, clash ClashSelector) *OperatorAdapter {
	_ = orch.Register(SlotSubscriptionsMeta)
	return &OperatorAdapter{
		orch:  orch,
		pm:    pm,
		clash: clash,
		cfg:   newEmptySlot(),
	}
}

// LoadFromDisk reads an existing 40-subscriptions.json (if any) into the
// in-memory config. Call once after orch.Bootstrap() so the adapter is
// consistent with what is on disk. Missing file is treated as an empty
// slot (not an error).
func (a *OperatorAdapter) LoadFromDisk(configDir string) error {
	path := fmt.Sprintf("%s/40-subscriptions.json", configDir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("subscription adapter: load %s: %w", path, err)
	}
	if len(b) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var cfg slotConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("subscription adapter: parse %s: %w", path, err)
	}
	if cfg.Inbounds == nil {
		cfg.Inbounds = []any{}
	}
	if cfg.Outbounds == nil {
		cfg.Outbounds = []any{}
	}
	if cfg.Route == nil {
		cfg.Route = map[string]any{"rules": []any{}}
	}
	a.cfg = cfg
	return nil
}

func newEmptySlot() slotConfig {
	return slotConfig{
		Inbounds:  []any{},
		Outbounds: []any{},
		Route:     map[string]any{"rules": []any{}},
	}
}

// AllocListenPort finds the lowest free port in [subscriptionPortBase, subscriptionPortMax]
// not already used by another subscription inbound in this slot.
func (a *OperatorAdapter) AllocListenPort() (uint16, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	used := map[int]bool{}
	for _, v := range a.cfg.Inbounds {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := toAnyInt(ib["listen_port"]); ok {
			used[p] = true
		}
	}
	for p := subscriptionPortBase; p <= subscriptionPortMax; p++ {
		if !used[p] {
			return uint16(p), nil
		}
	}
	return 0, fmt.Errorf("subscription adapter: no free port in range %d-%d", subscriptionPortBase, subscriptionPortMax)
}

// AddOutbound inserts or replaces an outbound by tag.
func (a *OperatorAdapter) AddOutbound(tag string, jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	var ob map[string]any
	if err := json.Unmarshal(jsonBody, &ob); err != nil {
		return fmt.Errorf("subscription adapter: AddOutbound %q: bad json: %w", tag, err)
	}
	ob["tag"] = tag
	// Компат-фиксы наравне с 10-tunnels (naive udp_over_tcp, hysteria2
	// chrome-parrot): подписочный слот пишется мимо Config.Save, без этого
	// вызова приехавший подпиской туннель остаётся несовместимым. Map только
	// что распарсена — ни с кем не разделена, инвариант shallow-снапшота
	// батча не нарушается.
	singbox.EnsureOutboundCompat(ob)
	if reason := classifyOutbound(ob); reason != "" {
		a.preFlushDropped = append(a.preFlushDropped, DropReason{Tag: tag, Reason: reason})
		return nil
	}
	a.upsertOutbound(tag, ob)
	return nil
}

// UpdateOutbound replaces the outbound JSON for an existing tag.
func (a *OperatorAdapter) UpdateOutbound(tag string, jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	var ob map[string]any
	if err := json.Unmarshal(jsonBody, &ob); err != nil {
		return fmt.Errorf("subscription adapter: UpdateOutbound %q: bad json: %w", tag, err)
	}
	ob["tag"] = tag
	// См. AddOutbound: тот же компат-фикс на свежераспарсенной map.
	singbox.EnsureOutboundCompat(ob)
	if reason := classifyOutbound(ob); reason != "" {
		a.preFlushDropped = append(a.preFlushDropped, DropReason{Tag: tag, Reason: reason})
		return nil
	}
	a.upsertOutbound(tag, ob)
	return nil
}

// RemoveOutbound strips the outbound with the given tag. No-op if absent.
func (a *OperatorAdapter) RemoveOutbound(tag string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	obs := a.cfg.Outbounds
	out := obs[:0:0]
	for _, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if t, _ := ob["tag"].(string); t == tag {
			continue
		}
		out = append(out, v)
	}
	a.cfg.Outbounds = out
	return nil
}

// SubscriptionOutbounds returns a snapshot of every outbound currently
// stored in this slot. The returned slice is freshly allocated; the
// inner maps are shared (callers MUST treat them as read-only). Callers
// outside this package use this to surface subscription-managed
// composites in UI/API contexts that historically only saw the router
// slot's composites.
func (a *OperatorAdapter) SubscriptionOutbounds() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()

	out := make([]map[string]any, 0, len(a.cfg.Outbounds))
	for _, v := range a.cfg.Outbounds {
		if ob, ok := v.(map[string]any); ok {
			out = append(out, ob)
		}
	}
	return out
}

// AddInbound inserts the inbound if its tag is not already present.
// Idempotent — re-adding an existing tag is a no-op.
func (a *OperatorAdapter) AddInbound(tag string, jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	var ib map[string]any
	if err := json.Unmarshal(jsonBody, &ib); err != nil {
		return fmt.Errorf("subscription adapter: AddInbound %q: bad json: %w", tag, err)
	}
	ib["tag"] = tag
	newPort, hasPort := toAnyInt(ib["listen_port"])

	for _, v := range a.cfg.Inbounds {
		existing, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := existing["tag"].(string); t == tag {
			return nil // already present (same tag) — idempotent
		}
		// Defense-in-depth (issue #287): reject a second inbound on a
		// listen_port already taken by a different tag. Two subscription
		// inbounds on one port make the merged config structurally invalid,
		// which the flush's index-based outbound-drop cannot repair.
		if hasPort {
			if p, ok := toAnyInt(existing["listen_port"]); ok && p == newPort {
				et, _ := existing["tag"].(string)
				return fmt.Errorf("subscription adapter: AddInbound %q: listen_port %d already used by inbound %q", tag, newPort, et)
			}
		}
	}
	a.cfg.Inbounds = append(a.cfg.Inbounds, ib)
	return nil
}

// RemoveInbound strips the inbound with the given tag. No-op if absent.
func (a *OperatorAdapter) RemoveInbound(tag string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	ibs := a.cfg.Inbounds
	out := ibs[:0:0]
	for _, v := range ibs {
		ib, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if t, _ := ib["tag"].(string); t == tag {
			continue
		}
		out = append(out, v)
	}
	a.cfg.Inbounds = out
	return nil
}

// AddRouteRule inserts the route rule described by jsonBody if not already present.
// Idempotent — duplicate inbound+outbound pairs are silently skipped.
func (a *OperatorAdapter) AddRouteRule(jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	var rule map[string]any
	if err := json.Unmarshal(jsonBody, &rule); err != nil {
		return fmt.Errorf("subscription adapter: AddRouteRule: bad json: %w", err)
	}
	newIn, _ := rule["inbound"].(string)
	newOut, _ := rule["outbound"].(string)

	rules := a.routeRules()
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if r["inbound"] == newIn && r["outbound"] == newOut {
			return nil // already present
		}
	}
	a.setRouteRules(append(rules, rule))
	return nil
}

// RemoveRouteRule removes the route rule matching the given inbound and outbound tags.
func (a *OperatorAdapter) RemoveRouteRule(inboundTag, outboundTag string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginIfNeededLocked()

	rules := a.routeRules()
	out := rules[:0:0]
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if r["inbound"] == inboundTag && r["outbound"] == outboundTag {
			continue
		}
		out = append(out, v)
	}
	a.setRouteRules(out)
	return nil
}

// Reload commits the accumulated batch: a single flush (validate + save +
// debounced SIGHUP) for every mutation since the last commit. This replaces
// the old flush-per-mutation behaviour that ran the sing-box validator O(N^2)
// times while materialising an N-server subscription (#331 — 15 min + pinned
// CPU on a 199-server sub). On flush failure the batch is rolled back so a.cfg
// cannot keep a partial/half-dropped state for the next operation. No-op when
// no batch is open.
func (a *OperatorAdapter) Reload(_ context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		return nil
	}
	err := a.flush()
	if err != nil {
		a.restoreLocked(a.pending)
	}
	a.pending = nil
	return err
}

// SelectClashProxy hits the running sing-box Clash API to switch the
// selector's active member at runtime, without triggering a config reload.
func (a *OperatorAdapter) SelectClashProxy(selectorTag, memberTag string) error {
	if a.clash == nil {
		return fmt.Errorf("subscription adapter: ClashSelector not configured")
	}
	return a.clash.SetSelector(selectorTag, memberTag)
}

// GetClashSelectorActive queries the running sing-box Clash API to read
// the currently-active member of a selector/urltest outbound. Returns
// ("", nil) when Clash is unreachable or the selector isn't yet known
// — callers treat this as "no live data" rather than as an error.
func (a *OperatorAdapter) GetClashSelectorActive(selectorTag string) (string, error) {
	if a.clash == nil {
		return "", nil
	}
	return a.clash.SelectorActive(selectorTag)
}

// --- internal helpers (caller must hold a.mu) ---

func (a *OperatorAdapter) routeRules() []any {
	route, _ := a.cfg.Route["rules"].([]any)
	return route
}

func (a *OperatorAdapter) setRouteRules(rules []any) {
	if a.cfg.Route == nil {
		a.cfg.Route = map[string]any{}
	}
	a.cfg.Route["rules"] = rules
}

func (a *OperatorAdapter) upsertOutbound(tag string, ob map[string]any) {
	obs := a.cfg.Outbounds
	for i, v := range obs {
		existing, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := existing["tag"].(string); t == tag {
			obs[i] = ob
			a.cfg.Outbounds = obs
			return
		}
	}
	a.cfg.Outbounds = append(obs, ob)
}

// flush persists the in-memory subscription slot via a two-pass
// validation pipeline (issue #221) so a single broken outbound cannot
// kill sing-box and orphan the DNS TPROXY rule:
//
//	Pass 1 (in-Go, ~µs)
//	  Drop outbounds violating structural rules we are confident will
//	  not move upstream — reality-requires-uTLS, malformed uuid, missing
//	  required fields. See preFilterOutbounds.
//
//	Pass 2 (sing-box check, ~100–300ms one-shot)
//	  orch.CheckMerged runs the daemon's own validator against a tmpdir
//	  snapshot of all enabled slots with our content overlaid and
//	  attributes the failing outbound index to a slot (initialize errors
//	  index the MERGED outbounds array; decode errors index one file).
//	  When attributed to OUR slot, drop that outbound, cascade reference
//	  cleanup (selectors / urltests / route rules), then drain the
//	  remaining per-outbound rejects via cheap STANDALONE single-slot
//	  checks (drainStandaloneRejects, issue #491) before re-checking
//	  merged — bounded by initial outbound count.
//
// Finally orch.Save commits the cleaned bytes and SetEnabled flips the
// slot on. Dropped outbounds are logged so the user (UI / /logs) sees
// which servers were skipped and why; the subscription is not rejected
// wholesale unless every outbound failed.
// dropLoopMaxDrops / dropLoopBudget ограничивают drainStandaloneRejects
// (issue #491): каждый отброшенный сервер — один спавн `sing-box check`, и
// патологический список из сотен мусорных записей не должен держать Create
// (и per-subscription мьютекс) минутами. Vars, не consts — тесты ужимают.
var (
	dropLoopMaxDrops = 64
	dropLoopBudget   = 90 * time.Second
)

// dropLoopExhaustedErr — честная ошибка вместо бесконечного «крутится»:
// подписка с сотнями неподдерживаемых записей прерывается с внятным
// объяснением и списком уже отброшенного.
func dropLoopExhaustedErr(dropped []DropReason) error {
	return fmt.Errorf("%w: подписка содержит слишком много неподдерживаемых серверов — проверка прервана после %d отброшенных. Сузьте подписку фильтрами (filterInclude/filterExclude). Отброшено: %s",
		ErrValidation, len(dropped), formatDropList(dropped))
}

func (a *OperatorAdapter) flush() error {
	dropped := append([]DropReason(nil), a.preFlushDropped...)
	a.preFlushDropped = nil

	// Resolve sing-box build tags once per flush: the closure is expected to
	// be cheap (Operator caches probes under a mtime+size fingerprint). Nil
	// → pass nil into preFilterOutbounds, which skips feature gating (tests
	// and pre-bootstrap).
	var features []string
	if a.singboxFeaturesFn != nil {
		features = a.singboxFeaturesFn()
	}

	// Pass 1 — structural pre-filter + build-tag gating.
	kept, p1Dropped := preFilterOutbounds(a.cfg.Outbounds, features)
	a.cfg.Outbounds = kept
	dropped = append(dropped, p1Dropped...)
	for _, d := range p1Dropped {
		// Remove dangling refs in selectors / urltests / routes for each
		// Pass-1 drop. Tag may be empty (non-object outbound); skip those.
		if d.Tag != "" {
			cleanReferencesToTag(&a.cfg, d.Tag)
		}
	}

	// Pass 2 — merged sing-box check with iterative drop: cross-slot
	// classes (duplicate tags vs 90-user, dangling group refs) plus
	// per-outbound rejects. A clean batch costs exactly one merged check
	// (#331); when a reject IS attributed to our slot, the remaining bad
	// entries are drained via cheap standalone checks (issue #491) before
	// the next merged re-check. Cap iterations to outbound count so a
	// parser bug cannot loop forever.
	maxIter := len(a.cfg.Outbounds) + 1
	xSlotCleaned := map[string]bool{} // cross-slot tags already cleaned — guards against re-reporting a ref we can't actually remove
	for iter := 0; iter < maxIter; iter++ {
		data, err := json.MarshalIndent(a.cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("subscription adapter: marshal slot: %w", err)
		}
		res, err := a.orch.CheckMerged(orchestrator.SlotSubscriptions, data)
		if err != nil {
			return fmt.Errorf("subscription adapter: check-merged: %w", err)
		}
		if res.Ok() {
			break
		}
		// Ошибки, атрибутированные user-слоту (90-user.json), drop-циклом
		// не лечатся: конфликтующий outbound живёт в пользовательском слоте,
		// который пишет только сам пользователь через эксперт-редактор —
		// удалять оттуда нечего и нельзя. Честно падаем сразу с понятной
		// причиной вместо непрозрачного «could not isolate outbound» после
		// исчерпания ретраев.
		if ve, ok := userSlotBlockingError(res); ok {
			if strings.HasPrefix(ve.Kind, "duplicate-") {
				return fmt.Errorf("%w: конфликт с пользовательским слотом 90-user.json: тег %q уже занят — переименуйте outbound в редакторе конфигурации", ErrValidation, ve.Tag)
			}
			return fmt.Errorf("%w: конфликт с пользовательским слотом 90-user.json: %s — правьте в редакторе конфигурации", ErrValidation, ve.Error())
		}
		idx, ok := subscriptionsOutboundIndex(res)
		if !ok {
			// Not a sing-box index error. It may be a cross-slot
			// unknown-outbound: a selector/urltest in our slot referencing a
			// member tag that no slot declares (dangling group member, e.g.
			// after a subscription update changed server tags). The
			// index-based loop can't reach these; self-heal by dropping the
			// dangling member refs, then retry. Only give up if nothing was
			// cleanable.
			// Only retry on genuinely-new progress: a tag we already cleaned
			// reappearing means cleanReferencesToTag couldn't actually remove
			// the ref (e.g. a reference path it doesn't walk) — looping would
			// spin to the cap and then save a still-broken config. Bail instead.
			progress := false
			for _, t := range cleanCrossSlotUnknownRefs(&a.cfg, res) {
				if !xSlotCleaned[t] {
					xSlotCleaned[t] = true
					progress = true
					dropped = append(dropped, DropReason{Tag: t, Reason: "висячая ссылка в группе: ни один слот не объявляет этот outbound"})
				}
			}
			if progress {
				continue
			}
			// Unknown error class — cannot isolate, give up.
			return fmt.Errorf("%w: could not isolate outbound: %s", ErrValidation, res.Error())
		}
		tag, err := dropOutboundAndCleanRefs(&a.cfg, idx)
		if err != nil {
			return fmt.Errorf("subscription adapter: drop idx %d: %w", idx, err)
		}
		reason := strings.TrimSpace(res.Error())
		dropped = append(dropped, DropReason{Tag: tag, Reason: reason})
		// Остальные пер-outbound отбраковки собираем дешёвыми standalone-
		// проверками (issue #491), чтобы не пересобирать merged-снапшот
		// на каждый мусорный сервер из большой публичной подписки.
		if err := a.drainStandaloneRejects(&dropped); err != nil {
			return err
		}
	}

	// An empty slot is fatal only for an additive op (Create/Refresh) where
	// every server was dropped as invalid (dropped > 0). A deliberate teardown
	// — deleting the last subscription / member, nothing dropped — commits an
	// empty slot and disables it below. Erroring here would block deleteLocked's
	// store.Delete and Reload would restore the just-removed config, leaving the
	// subscription undeletable (#331 regression).
	if len(a.cfg.Outbounds) == 0 && len(dropped) > 0 {
		return fmt.Errorf("%w: no valid outbounds left after filtering (dropped: %s)", ErrValidation, formatDropList(dropped))
	}

	// Commit; enable a populated slot, disable an emptied one.
	data, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("subscription adapter: marshal slot: %w", err)
	}
	if err := a.orch.Save(orchestrator.SlotSubscriptions, data); err != nil {
		return fmt.Errorf("subscription adapter: save slot: %w", err)
	}
	_ = a.orch.SetEnabled(orchestrator.SlotSubscriptions, len(a.cfg.Outbounds) > 0)

	a.lastDropped = dropped
	return nil
}

// drainStandaloneRejects — drop-цикл пер-outbound отбраковок поверх
// STANDALONE-снапшота одного нашего слота (issue #491). Публичные списки
// share-ссылок регулярно несут десятки записей, которые sing-box
// отвергает; каждый дроп — один спавн `sing-box check`, и прогон каждого
// спавна по merged-снапшоту (все слоты копируются и парсятся заново на
// каждой итерации) превращал Create большой подписки в минуты немого
// спиннера на MIPS/ARM-роутере. Cross-slot классы ошибок здесь невидимы
// по построению — merged-цикл в flush() по-прежнему владеет ими: выход по
// Ok или по неиндексируемой ошибке возвращает управление ему. Budget-cap:
// патологический список падает с честной ошибкой вместо перемалывания.
//
// Известное ограничение: standalone строже merged для outbound с detour
// на тег ЧУЖОГО слота (возможно только в sb-JSON подписке, вручную
// сшитой под конкретный router/user-конфиг) — такой outbound здесь
// отбросится с причиной в LastFilterDrops, хотя merged-проверка его бы
// приняла. Встроенные парсеры (vlink/mieru/Clash) внешних detour не
// порождают; отказ не тихий, поэтому осознанно принимаем.
func (a *OperatorAdapter) drainStandaloneRejects(dropped *[]DropReason) error {
	deadline := time.Now().Add(dropLoopBudget)
	for iter := 0; iter < dropLoopMaxDrops; iter++ {
		data, err := json.MarshalIndent(a.cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("subscription adapter: marshal slot: %w", err)
		}
		res, err := a.orch.CheckSlotAlone(orchestrator.SlotSubscriptions, data)
		if err != nil {
			return fmt.Errorf("subscription adapter: check-slot: %w", err)
		}
		if res.Ok() {
			return nil
		}
		idx, ok := subscriptionsOutboundIndex(res)
		if !ok {
			// Не индексируемая по outbound ошибка — оставляем merged-циклу,
			// который различает конфликты с user-слотом и висячие
			// cross-slot ссылки.
			return nil
		}
		if time.Now().After(deadline) {
			return dropLoopExhaustedErr(*dropped)
		}
		tag, err := dropOutboundAndCleanRefs(&a.cfg, idx)
		if err != nil {
			return fmt.Errorf("subscription adapter: drop idx %d: %w", idx, err)
		}
		*dropped = append(*dropped, DropReason{Tag: tag, Reason: strings.TrimSpace(res.Error())})
	}
	return dropLoopExhaustedErr(*dropped)
}

// DeclaredOutboundTags returns the outbound tags present in the committed
// subscriptions slot (after the last flush). The Service uses it to prune
// stored MemberTags down to servers that actually materialized, so
// flush-dropped servers don't linger as dangling group members.
func (a *OperatorAdapter) DeclaredOutboundTags() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	tags := make([]string, 0, len(a.cfg.Outbounds))
	for _, raw := range a.cfg.Outbounds {
		ob, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if t := outboundTag(ob); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// cleanCrossSlotUnknownRefs removes member references (selector/urltest
// "outbounds") to outbound tags that the cross-slot validator reported as
// unknown-outbound for the subscriptions slot. This self-heals dangling
// group members that the sing-box index-based isolation loop in flush()
// cannot reach (it only understands sing-box "initialize outbound[N]"
// errors, not our own cross-slot validation result). Only the
// subscriptions slot is touched — unknown-outbounds reported for other
// slots aren't ours to fix here. Returns the distinct tags cleaned.
func cleanCrossSlotUnknownRefs(cfg *slotConfig, res orchestrator.ValidationResult) []string {
	seen := map[string]bool{}
	var cleaned []string
	for _, e := range res.Errors {
		if e.Slot != orchestrator.SlotSubscriptions || e.Kind != "unknown-outbound" || e.Tag == "" || seen[e.Tag] {
			continue
		}
		seen[e.Tag] = true
		cleanReferencesToTag(cfg, e.Tag)
		cleaned = append(cleaned, e.Tag)
	}
	return cleaned
}

// userSlotBlockingError returns the first BLOCKING validation error
// attributed to the user slot (90-user.json). Advisory-предупреждения
// (SeverityWarning) пропускаются — они не мешают применению. Типичный
// случай: подписка материализует outbound с тегом, который пользователь
// уже объявил в эксперт-редакторе — валидатор сканирует user-слот (90-…)
// после subscriptions (40-…) и вешает duplicate-outbound на user.
func userSlotBlockingError(res orchestrator.ValidationResult) (orchestrator.ValidationError, bool) {
	for _, e := range res.Errors {
		if e.Slot == orchestrator.SlotUser && e.Severity != orchestrator.SeverityWarning {
			return e, true
		}
	}
	return orchestrator.ValidationError{}, false
}

// LastFilterDrops returns the outbounds filtered out of the most recent
// flush (issue #221 — subscription validation pipeline). Empty when the
// last save accepted every outbound. UI / API handlers read this to
// surface "we accepted your subscription but skipped N servers" to the
// user along with reasons. Snapshot is copied — caller cannot mutate
// the adapter's view.
func (a *OperatorAdapter) LastFilterDrops() []DropReason {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.lastDropped) == 0 {
		return nil
	}
	out := make([]DropReason, len(a.lastDropped))
	copy(out, a.lastDropped)
	return out
}

// AllocProxyIndex returns the next free NDMS Proxy slot index. Delegates to
// ProxyRegistrar.NextFreeIndex with an empty reserved set (subscriptions are
// created one at a time, so no batch allocation is needed here).
func (a *OperatorAdapter) AllocProxyIndex(ctx context.Context) (int, error) {
	if a.pm == nil {
		return -1, fmt.Errorf("subscription adapter: ProxyRegistrar not configured")
	}
	return a.pm.NextFreeIndex(ctx, nil)
}

// EnsureProxy creates or refreshes the NDMS ProxyN interface at the given index.
func (a *OperatorAdapter) EnsureProxy(ctx context.Context, idx, port int, description string) error {
	if a.pm == nil {
		return fmt.Errorf("subscription adapter: ProxyRegistrar not configured")
	}
	return a.pm.EnsureProxy(ctx, idx, port, description)
}

// RemoveProxy tears down the NDMS ProxyN interface at the given index.
func (a *OperatorAdapter) RemoveProxy(ctx context.Context, idx int) error {
	if a.pm == nil {
		return fmt.Errorf("subscription adapter: ProxyRegistrar not configured")
	}
	return a.pm.RemoveProxy(ctx, idx)
}

// toAnyInt extracts an integer from json-decoded interface values (float64, int, int64).
func toAnyInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}

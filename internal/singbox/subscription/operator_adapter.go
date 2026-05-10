package subscription

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

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
	Inbounds  []any                    `json:"inbounds"`
	Outbounds []any                    `json:"outbounds"`
	Route     map[string]any           `json:"route"`
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
// Every mutation is applied in-memory then the full slot is flushed via
// orch.Save, which schedules a debounced sing-box SIGHUP. Reload() is a
// no-op because Save already arms the reload timer.
//
// The adapter is safe for concurrent use — a single mutex guards all
// state reads and writes.
type OperatorAdapter struct {
	orch  *orchestrator.Orchestrator
	pm    ProxyRegistrar
	clash ClashSelector

	mu  sync.Mutex
	cfg slotConfig
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

	var ob map[string]any
	if err := json.Unmarshal(jsonBody, &ob); err != nil {
		return fmt.Errorf("subscription adapter: AddOutbound %q: bad json: %w", tag, err)
	}
	ob["tag"] = tag
	a.upsertOutbound(tag, ob)
	return a.flush()
}

// UpdateOutbound replaces the outbound JSON for an existing tag.
func (a *OperatorAdapter) UpdateOutbound(tag string, jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var ob map[string]any
	if err := json.Unmarshal(jsonBody, &ob); err != nil {
		return fmt.Errorf("subscription adapter: UpdateOutbound %q: bad json: %w", tag, err)
	}
	ob["tag"] = tag
	a.upsertOutbound(tag, ob)
	return a.flush()
}

// RemoveOutbound strips the outbound with the given tag. No-op if absent.
func (a *OperatorAdapter) RemoveOutbound(tag string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	return a.flush()
}

// AddInbound inserts the inbound if its tag is not already present.
// Idempotent — re-adding an existing tag is a no-op.
func (a *OperatorAdapter) AddInbound(tag string, jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, v := range a.cfg.Inbounds {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ib["tag"].(string); t == tag {
			return nil // already present
		}
	}
	var ib map[string]any
	if err := json.Unmarshal(jsonBody, &ib); err != nil {
		return fmt.Errorf("subscription adapter: AddInbound %q: bad json: %w", tag, err)
	}
	ib["tag"] = tag
	a.cfg.Inbounds = append(a.cfg.Inbounds, ib)
	return a.flush()
}

// RemoveInbound strips the inbound with the given tag. No-op if absent.
func (a *OperatorAdapter) RemoveInbound(tag string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	return a.flush()
}

// AddRouteRule inserts the route rule described by jsonBody if not already present.
// Idempotent — duplicate inbound+outbound pairs are silently skipped.
func (a *OperatorAdapter) AddRouteRule(jsonBody []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	return a.flush()
}

// RemoveRouteRule removes the route rule matching the given inbound and outbound tags.
func (a *OperatorAdapter) RemoveRouteRule(inboundTag, outboundTag string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

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
	return a.flush()
}

// Reload is a no-op: every mutation calls flush() which invokes orch.Save,
// and that already schedules the debounced SIGHUP. Callers that invoke
// Reload() after a batch of mutations do not need a second trigger.
func (a *OperatorAdapter) Reload(_ context.Context) error {
	return nil
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

// flush marshals the in-memory slot and writes it via orch.Save (which
// schedules a debounced reload). Also enables the slot so sing-box
// picks it up on first write.
func (a *OperatorAdapter) flush() error {
	data, err := json.MarshalIndent(a.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("subscription adapter: marshal slot: %w", err)
	}
	if err := a.orch.Save(orchestrator.SlotSubscriptions, data); err != nil {
		return fmt.Errorf("subscription adapter: save slot: %w", err)
	}
	// Enable the slot so sing-box includes it. SetEnabled is idempotent
	// when already enabled.
	_ = a.orch.SetEnabled(orchestrator.SlotSubscriptions, true)
	return nil
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

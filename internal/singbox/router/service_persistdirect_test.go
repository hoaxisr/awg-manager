package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// These tests cover the direct-save path that replaced SaveDraft for
// system-driven config writes (Enable / Disable legacy / healTProxyInbound).
// The bug they regress against: on every router reboot a `pending/20-router.json`
// appeared because the boot-time Reconcile→Enable cycle staged its
// idempotently-regenerated config as if it were a user edit, leaving the
// UI banner "Несохранённые правки" stuck until the user clicked Apply.
//
// The fix splits persistConfig (still staged) from persistConfigDirect
// (direct write to active, with byte-equal short-circuit). Boot recovery
// goes through persistConfigDirect → no pending → no banner.

func TestPersistConfigDirect_NoOpWhenActiveMatches(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// Active file pre-exists with what materializing+marshalling NewEmptyConfig
	// would produce — Bootstrap below sees it and marks the slot enabled.
	cfg := NewEmptyConfig()
	materialized, err := svc.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		t.Fatalf("materializeConfig: %v", err)
	}
	bytesNow, err := json.MarshalIndent(materialized, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	activePath := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(activePath, bytesNow, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	// Re-bootstrap so the orchestrator picks up the active file.
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Capture mtime to verify atomic rewrite did NOT happen.
	before, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat active: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // separate possible mtime windows

	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	after, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat active after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("active should not be re-written when bytes match (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("pending must not exist after byte-equal direct save: %v", err)
	}
}

func TestPersistConfigDirect_WritesActiveWhenDifferent(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// Seed active with stale bytes (different from what marshalling our
	// cfg below will produce). Bootstrap marks the slot enabled.
	activePath := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(activePath, []byte(`{"stale": true}`), 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	cfg := NewEmptyConfig()
	cfg.Route.Rules = append(cfg.Route.Rules, Rule{Action: "route", Outbound: "direct"})

	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	got, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	materialized, err := svc.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		t.Fatalf("materializeConfig: %v", err)
	}
	want, _ := json.MarshalIndent(materialized, "", "  ")
	if string(got) != string(want) {
		t.Errorf("active not overwritten with new bytes\nwant: %s\ngot:  %s", want, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("pending must not exist after direct save: %v", err)
	}
}

func TestPersistConfigDirect_WritesActiveWhenAbsent(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// No active file. Bootstrap sees nothing → enabled=false; explicit
	// SetEnabled flips it to true so orch.Save targets activePath.
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := svc.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
		t.Fatalf("SetEnabled true: %v", err)
	}

	cfg := NewEmptyConfig()
	cfg.Route.Rules = append(cfg.Route.Rules, Rule{Action: "route", Outbound: "direct"})

	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatalf("persistConfigDirect: %v", err)
	}

	activePath := filepath.Join(dir, "20-router.json")
	got, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	materialized, err := svc.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		t.Fatalf("materializeConfig: %v", err)
	}
	want, _ := json.MarshalIndent(materialized, "", "  ")
	if string(got) != string(want) {
		t.Errorf("active not created with expected bytes\nwant: %s\ngot:  %s", want, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending", "20-router.json")); !os.IsNotExist(err) {
		t.Errorf("pending must not exist after direct save: %v", err)
	}
}

// Regression: changing the UDP timeout on a running engine goes through
// Reconcile→reconcileInstalled→healTProxyInbound. The old heal returned early
// whenever a tproxy-in inbound was present, so a changed udpTimeout was never
// written to the config (UI showed "1 час" while the file kept "3m0s").
// #554: the system route-options rule must be brought to spec by the SAME
// heal — it used to be regenerated only by Enable, so a changed timeout
// stayed stale in the rule until the engine was toggled off/on.
func TestHealTProxyInbound_AppliesChangedUDPTimeout(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// Seed active config with a tproxy-in AND the route-options rule at the
	// default (5m0s) timeout.
	cfg := NewEmptyConfig()
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, "")
	cfg.EnsureUDPTimeoutRule(resolveUDPTimeout(""))
	seed, _ := json.MarshalIndent(cfg, "", "  ")
	activePath := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(activePath, seed, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if err := svc.healTProxyInbound(context.Background(), "1h0m0s"); err != nil {
		t.Fatalf("healTProxyInbound: %v", err)
	}

	raw, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("read active: %v", err)
	}
	var got RouterConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var found bool
	for _, in := range got.Inbounds {
		if in.Tag == "tproxy-in" {
			found = true
			if in.UDPTimeout != "1h0m0s" {
				t.Errorf("inbound udp_timeout not applied: want 1h0m0s, got %q", in.UDPTimeout)
			}
		}
	}
	if !found {
		t.Fatal("tproxy-in inbound missing after heal")
	}
	var ruleFound bool
	for _, r := range got.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			ruleFound = true
			if r.UDPTimeout != "1h0m0s" {
				t.Errorf("route-options udp_timeout not applied (#554): want 1h0m0s, got %q", r.UDPTimeout)
			}
		}
	}
	if !ruleFound {
		t.Fatal("system route-options rule missing after heal")
	}
}

// Heal must judge and rewrite the APPLIED config, never the user's staged
// pending draft: loadRouterConfig reads pending-first, so healing from it
// would materialize an unvalidated draft into active/ (bypassing ApplyDraft)
// while the pending banner keeps hanging over an already-applied config.
func TestHealTProxyInbound_IgnoresPendingDraft(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	// Active: drifted timeout (heal must rewrite it). Pending: a user draft
	// with a marker rule that must NOT leak into active.
	active := NewEmptyConfig()
	active.Inbounds = ensureTProxyInbound(active.Inbounds, "")
	active.EnsureUDPTimeoutRule(resolveUDPTimeout(""))
	seed, _ := json.MarshalIndent(active, "", "  ")
	activePath := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(activePath, seed, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	draft := NewEmptyConfig()
	draft.Inbounds = ensureTProxyInbound(draft.Inbounds, "")
	draft.EnsureUDPTimeoutRule(resolveUDPTimeout(""))
	draft.Route.Rules = append(draft.Route.Rules, Rule{Action: "route", Outbound: "draft-marker", Domain: []string{"draft.example"}})
	draftBytes, _ := json.MarshalIndent(draft, "", "  ")
	if err := os.MkdirAll(filepath.Join(dir, "pending"), 0755); err != nil {
		t.Fatalf("mkdir pending: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pending", "20-router.json"), draftBytes, 0644); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if err := svc.healTProxyInbound(context.Background(), "1h0m0s"); err != nil {
		t.Fatalf("healTProxyInbound: %v", err)
	}

	raw, _ := os.ReadFile(activePath)
	if strings.Contains(string(raw), "draft-marker") {
		t.Fatal("pending draft content leaked into active via heal (must heal the APPLIED config)")
	}
	var got RouterConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, in := range got.Inbounds {
		if in.Tag == "tproxy-in" && in.UDPTimeout != "1h0m0s" {
			t.Errorf("active inbound not healed: got %q", in.UDPTimeout)
		}
	}
	pendingRaw, err := os.ReadFile(filepath.Join(dir, "pending", "20-router.json"))
	if err != nil || !strings.Contains(string(pendingRaw), "draft-marker") {
		t.Errorf("pending draft must survive heal untouched (err=%v)", err)
	}
}

// #554: the rule can drift alone (the inbound is already at spec — e.g. a
// pre-fix build applied the inbound but never the rule). The heal must still
// rewrite the config.
func TestHealTProxyInbound_HealsRuleWhenOnlyRuleDrifted(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	cfg := NewEmptyConfig()
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, "1h0m0s")
	// Rule deliberately absent — the drifted-carrier case.
	seed, _ := json.MarshalIndent(cfg, "", "  ")
	activePath := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(activePath, seed, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if err := svc.healTProxyInbound(context.Background(), "1h0m0s"); err != nil {
		t.Fatalf("healTProxyInbound: %v", err)
	}

	raw, _ := os.ReadFile(activePath)
	var got RouterConfig
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, r := range got.Route.Rules {
		if isSystemUDPTimeoutRule(r) && r.UDPTimeout == "1h0m0s" {
			return
		}
	}
	t.Fatal("missing route-options rule was not healed")
}

// The cheap steady-state guard: when the timeout already matches, heal must
// not rewrite the active file (no spurious SIGHUP every reconcile tick).
func TestHealTProxyInbound_NoOpWhenTimeoutMatches(t *testing.T) {
	svc, dir := newOrchedTestService(t)

	cfg := NewEmptyConfig()
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, "1h0m0s")
	cfg.EnsureUDPTimeoutRule("1h0m0s")
	seed, _ := json.MarshalIndent(cfg, "", "  ")
	activePath := filepath.Join(dir, "20-router.json")
	if err := os.WriteFile(activePath, seed, 0644); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	if err := svc.deps.Orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	before, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	if err := svc.healTProxyInbound(context.Background(), "1h0m0s"); err != nil {
		t.Fatalf("healTProxyInbound: %v", err)
	}

	after, err := os.Stat(activePath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("active rewritten despite matching timeout (before=%v after=%v)", before.ModTime(), after.ModTime())
	}
}

func TestWaitForSingbox_ReturnsWhenRunning(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	stubListeningProbe(t, func() bool { return true })

	calls := 0
	svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) {
		calls++
		return calls >= 3, 1234 // false, false, true
	}

	start := time.Now()
	if err := svc.waitForSingbox(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("waitForSingbox: %v", err)
	}
	if calls < 3 {
		t.Errorf("expected at least 3 polls, got %d", calls)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waitForSingbox took unexpectedly long: %v", elapsed)
	}
}

func TestWaitForSingbox_TimesOutWhenNeverRunning(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	// Default fakeSingbox.IsRunning returns (false, 0) — perfect for this case.

	start := time.Now()
	err := svc.waitForSingbox(context.Background(), 250*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("waitForSingbox returned too early: %v", elapsed)
	}
	if elapsed > 1*time.Second {
		t.Errorf("waitForSingbox returned too late: %v", elapsed)
	}
}

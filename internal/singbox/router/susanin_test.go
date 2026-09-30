package router

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestEnrichMaterializedConfig_SusaninToggle(t *testing.T) {
	svc := &ServiceImpl{}

	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		SusaninEnabled:  true,
		SusaninOutbound: "my-tunnel",
	})
	svc.deps.Settings = store

	cfg := &RouterConfig{
		Route: Route{
			Rules: []Rule{
				{Action: "sniff"},
				{Domain: []string{"example.com"}, Action: "route", Outbound: "direct"},
			},
		},
	}

	// 1. Enrich with SusaninEnabled = true
	svc.enrichMaterializedConfig(cfg)

	hasRule := false
	for _, r := range cfg.Route.Rules {
		for _, rs := range r.RuleSet {
			if rs == "susanin" {
				hasRule = true
				if r.Outbound != "my-tunnel" {
					t.Fatalf("expected Outbound to be 'my-tunnel', got %s", r.Outbound)
				}
			}
		}
	}
	if !hasRule {
		t.Fatal("expected susanin rule to be added")
	}

	hasSet := false
	for _, rs := range cfg.Route.RuleSet {
		if rs.Tag == "susanin" {
			hasSet = true
		}
	}
	if !hasSet {
		t.Fatal("expected susanin rule_set to be added")
	}

	// 2. Change outbound to "new-tunnel"
	_ = store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.SusaninOutbound = "new-tunnel"
		return nil
	})
	svc.enrichMaterializedConfig(cfg)
	for _, r := range cfg.Route.Rules {
		for _, rs := range r.RuleSet {
			if rs == "susanin" {
				if r.Outbound != "new-tunnel" {
					t.Fatalf("expected updated Outbound to be 'new-tunnel', got %s", r.Outbound)
				}
			}
		}
	}

	// 3. Disable Susanin: SusaninEnabled = false
	_ = store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.SusaninEnabled = false
		return nil
	})
	svc.enrichMaterializedConfig(cfg)

	for _, r := range cfg.Route.Rules {
		for _, rs := range r.RuleSet {
			if rs == "susanin" {
				t.Fatalf("susanin rule was not stripped when disabled: %+v", r)
			}
		}
	}
	for _, rs := range cfg.Route.RuleSet {
		if rs.Tag == "susanin" {
			t.Fatalf("susanin rule_set was not stripped when disabled: %+v", rs)
		}
	}
}

func TestListRules_SusaninFilteredWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"})
	_ = orch.SetEnabled(orchestrator.SlotRouter, true)

	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		SusaninEnabled: false,
	})
	svc := &ServiceImpl{
		deps: Deps{
			Orch:     orch,
			Settings: store,
		},
	}
	cfg := &RouterConfig{
		Route: Route{
			Rules: []Rule{
				{RuleSet: []string{"susanin"}, Action: "route", Outbound: "direct"},
				{Domain: []string{"google.com"}, Action: "route", Outbound: "direct"},
			},
			RuleSet: []RuleSet{
				{Tag: "susanin", Type: "local", Format: "source", Path: "/path"},
				{Tag: "custom", Type: "remote", Format: "binary"},
			},
		},
	}
	// Seed orchestrator with config containing susanin
	data, _ := json.Marshal(cfg)
	_ = orch.Save(orchestrator.SlotRouter, data)

	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	for _, r := range rules {
		for _, rs := range r.RuleSet {
			if rs == "susanin" {
				t.Fatalf("ListRules returned susanin rule when disabled: %+v", r)
			}
		}
	}

	ruleSets, err := svc.ListRuleSets(context.Background())
	if err != nil {
		t.Fatalf("ListRuleSets: %v", err)
	}
	for _, rs := range ruleSets {
		if rs.Tag == "susanin" {
			t.Fatalf("ListRuleSets returned susanin rule-set when disabled: %+v", rs)
		}
	}
}

func TestUpdateSettings_SusaninDisabledReappliesSlotRouter(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"})
	_ = orch.SetEnabled(orchestrator.SlotRouter, true)

	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		SusaninEnabled:  true,
		SusaninOutbound: "my-tunnel",
	})
	svc := &ServiceImpl{
		deps: Deps{
			Orch:     orch,
			Settings: store,
			IPTables: newStubIPTables(func(context.Context, string) error { return nil }),
		},
	}
	cfg := &RouterConfig{
		Route: Route{
			Rules: []Rule{
				{RuleSet: []string{"susanin"}, Action: "route", Outbound: "my-tunnel"},
			},
			RuleSet: []RuleSet{
				{Tag: "susanin", Type: "local", Format: "source", Path: "/path"},
			},
		},
	}
	data, _ := json.Marshal(cfg)
	_ = orch.Save(orchestrator.SlotRouter, data)

	// Now call UpdateSettings with SusaninEnabled = false
	err := svc.UpdateSettings(context.Background(), storage.SingboxRouterSettings{
		WANAutoDetect:  true,
		SusaninEnabled: false,
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	// Verify that the on-disk/active SlotRouter config now has susanin removed
	appliedData, err := orch.LoadApplied(orchestrator.SlotRouter)
	if err != nil {
		t.Fatalf("LoadApplied: %v", err)
	}
	appliedCfg, err := parseRouterConfigBytes(appliedData)
	if err != nil {
		t.Fatalf("parseRouterConfigBytes: %v", err)
	}

	for _, r := range appliedCfg.Route.Rules {
		for _, rs := range r.RuleSet {
			if rs == "susanin" {
				t.Fatalf("expected susanin rule to be stripped from persisted SlotRouter, found: %+v", r)
			}
		}
	}
	for _, rs := range appliedCfg.Route.RuleSet {
		if rs.Tag == "susanin" {
			t.Fatalf("expected susanin rule_set to be stripped from persisted SlotRouter, found: %+v", rs)
		}
	}
}

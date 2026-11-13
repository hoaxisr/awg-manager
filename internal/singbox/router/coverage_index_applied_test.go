package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func TestCoverageIndexFakeIPRuleSetHasNoNDMSWrites(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	sourcePath := filepath.Join(t.TempDir(), "fakeip-set.json")
	source := []byte(`{"version":1,"rules":[{"domain_suffix":["fakeip.example"],"ip_cidr":["198.18.0.0/15"]}]}`)
	if err := os.WriteFile(sourcePath, source, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{Tag: "fakeip-set", Type: "local", Format: "source", Path: sourcePath}}
	cfg.Route.Rules = []Rule{{Action: "route", RuleSet: []string{"fakeip-set"}, Outbound: "fakeip-out"}}
	if err := svc.persistSlotDirect(orchestrator.SlotFakeIP, cfg, true); err != nil {
		t.Fatal(err)
	}

	log := &callLog{}
	routes := &recStaticRoutes{log: log}
	svc.deps.StaticRoutes = routes
	svc.deps.OpkgTun = &recOpkgTun{log: log}
	svc.deps.OpkgTunIndices = &recIndices{}
	snapshot, err := svc.CoverageIndexSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Slot != string(orchestrator.SlotFakeIP) || len(snapshot.RuleSets) != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	// Once published, even deleting the source cannot affect the immutable
	// warm snapshot. This proves hit/negative matching performs no file read.
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"a.fakeip.example", "198.18.0.1"} {
		matched, err := svc.MatchRuleSetsIndexed(context.Background(), input, []string{"fakeip-set"})
		if err != nil || !matched.Results[0].Matched || !matched.Results[0].Supported {
			t.Fatalf("fakeip match %s=%+v err=%v", input, matched, err)
		}
	}
	negative, err := svc.MatchRuleSetsIndexed(context.Background(), "other.example", []string{"fakeip-set"})
	if err != nil || negative.Results[0].Matched {
		t.Fatalf("fakeip negative=%+v err=%v", negative, err)
	}
	if len(log.calls) != 0 || routes.binds != 0 {
		t.Fatalf("indexed reads touched NDMS: calls=%v binds=%d", log.calls, routes.binds)
	}
}

func TestCoverageIndexFakeIPDownloadDetourPort(t *testing.T) {
	sets := withRuleSetFetchVia(orchestrator.SlotFakeIP, []RuleSet{{
		Tag: "remote", Type: "remote", URL: "https://example.invalid/set.srs",
		HTTPClient: &RuleSetHTTPClient{Detour: "vpn"},
	}})
	if len(sets) != 1 || !strings.Contains(sets[0].fetchVia, ":51276") {
		t.Fatalf("fakeip fetchVia=%+v", sets)
	}
	routerSets := withRuleSetFetchVia(orchestrator.SlotRouter, []RuleSet{{
		Tag: "remote", Type: "remote", URL: "https://example.invalid/set.srs",
		HTTPClient: &RuleSetHTTPClient{Detour: "vpn"},
	}})
	if !strings.Contains(routerSets[0].fetchVia, ":51275") {
		t.Fatalf("router fetchVia=%+v", routerSets)
	}
}

func TestCoverageIndexUsesAppliedActiveSlotNotDraft(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	seedInspectSlots(t, svc)
	ctx := context.Background()

	snapshot, err := svc.CoverageIndexSnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Slot != string(orchestrator.SlotFakeIP) || len(snapshot.Rules) == 0 ||
		snapshot.Rules[0].Outbound != "fakeip-out" {
		t.Fatalf("fakeip snapshot=%+v", snapshot)
	}

	// A pending fakeip draft is not live coverage evidence.
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{{Action: "route", DomainSuffix: []string{"draft.example"}, Outbound: "draft-out"}}
	draft, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.deps.Orch.SaveDraft(orchestrator.SlotFakeIP, draft); err != nil {
		t.Fatal(err)
	}
	again, err := svc.CoverageIndexSnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != snapshot.Generation || again.Rules[0].Outbound != "fakeip-out" {
		t.Fatalf("draft changed applied snapshot: before=%+v after=%+v", snapshot, again)
	}

	setRoutingMode(t, svc, "tproxy")
	svc.notifyRoutingSlotsChanged()
	routerSnapshot, err := svc.CoverageIndexSnapshot(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if routerSnapshot.Slot != string(orchestrator.SlotRouter) || len(routerSnapshot.Rules) == 0 ||
		routerSnapshot.Rules[0].Outbound != "tproxy-out" {
		t.Fatalf("tproxy snapshot=%+v", routerSnapshot)
	}
	if routerSnapshot.Generation == snapshot.Generation {
		t.Fatal("slot switch did not create a generation")
	}
}

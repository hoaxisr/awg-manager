package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/selective"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestBuildSelectiveIPRules_GroupsByOutbound(t *testing.T) {
	rules := buildSelectiveIPRules(map[string][]string{
		"awg-tunnel": {"188.40.167.82/32"},
	})
	if len(rules) != 1 {
		t.Fatalf("got %d rules", len(rules))
	}
	if rules[0].Outbound != "awg-tunnel" || rules[0].IPCIDR[0] != "188.40.167.82/32" {
		t.Fatalf("unexpected rule: %+v", rules[0])
	}
}

// TestBuildSelectiveIPRules_DeterministicBytes guards the changed/unchanged
// byte-compare in syncSelectiveRoutesSlot: marshalling the same accumulator
// twice must be byte-identical, or every identical rebuild reports «changed»
// and the skip-SIGHUP optimisation never fires.
func TestBuildSelectiveIPRules_DeterministicBytes(t *testing.T) {
	acc := selective.NewRouteAccumulator()
	for i := 0; i < 60; i++ {
		acc.Add("awg-a", fmt.Sprintf("10.0.%d.%d/32", i%7, i))
		acc.Add("awg-b", fmt.Sprintf("172.16.%d.%d/32", i%5, i))
		acc.Add("awg-c", fmt.Sprintf("192.168.%d.%d/32", i%3, i))
	}
	first, err := marshalSelectiveRoutesSlot(buildSelectiveIPRules(acc.RulesByOutbound()))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := marshalSelectiveRoutesSlot(buildSelectiveIPRules(acc.RulesByOutbound()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("marshal %d differs from first:\n%s\nvs\n%s", i, first, again)
		}
	}
}

func TestStripSelectiveManagedRules(t *testing.T) {
	rules := []Rule{
		{AwgmManaged: selectiveIPRuleManaged, Action: "route", Outbound: "x", IPCIDR: []string{"1.2.3.4/32"}},
		{Action: "route", Outbound: "direct", DomainSuffix: []string{"example.com"}},
	}
	filtered, changed := stripSelectiveManagedRules(rules)
	if !changed || len(filtered) != 1 || filtered[0].DomainSuffix[0] != "example.com" {
		t.Fatalf("got %+v changed=%v", filtered, changed)
	}
}

func TestMarshalSelectiveRoutesSlot_RouteOnly(t *testing.T) {
	data, err := marshalSelectiveRoutesSlot(nil)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"outbounds", "inbounds", "dns"} {
		if _, ok := m[forbidden]; ok {
			t.Fatalf("slot JSON must not contain %q: %s", forbidden, string(data))
		}
	}
}

func TestSyncSelectiveRoutesSlot_ReportsChangedOnlyOnDiff(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotSelectiveRoutes, Filename: "19-selective-routes.json"})

	svc := &ServiceImpl{deps: Deps{Orch: orch}}
	ctx := context.Background()
	routes := map[string][]string{"tun": {"1.2.3.4/32"}}

	changed, err := svc.syncSelectiveRoutesSlot(ctx, routes)
	if err != nil || !changed {
		t.Fatalf("first sync: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "19-selective-routes.json")); err != nil {
		t.Fatalf("active slot file missing: %v", err)
	}

	// Identical routes → byte-equal slot → no reload needed.
	changed, err = svc.syncSelectiveRoutesSlot(ctx, routes)
	if err != nil || changed {
		t.Fatalf("no-op sync must report changed=false, got changed=%v err=%v", changed, err)
	}

	// Different routes → changed again.
	changed, err = svc.syncSelectiveRoutesSlot(ctx, map[string][]string{"tun": {"5.6.7.8/32"}})
	if err != nil || !changed {
		t.Fatalf("diff sync: changed=%v err=%v", changed, err)
	}
}

func TestSyncSelectiveRoutesSlot_EmptyRoutes(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotSelectiveRoutes, Filename: "19-selective-routes.json"})

	svc := &ServiceImpl{deps: Deps{Orch: orch}}
	ctx := context.Background()

	// Never-enabled slot + zero routes: already in target shape, no reload.
	changed, err := svc.syncSelectiveRoutesSlot(ctx, nil)
	if err != nil || changed {
		t.Fatalf("empty sync on empty slot: changed=%v err=%v", changed, err)
	}

	// Enable with routes, then clear them: slot disable IS a config change.
	if changed, err = svc.syncSelectiveRoutesSlot(ctx, map[string][]string{"tun": {"1.2.3.4/32"}}); err != nil || !changed {
		t.Fatalf("populate: changed=%v err=%v", changed, err)
	}
	if changed, err = svc.syncSelectiveRoutesSlot(ctx, nil); err != nil || !changed {
		t.Fatalf("clear: changed=%v err=%v", changed, err)
	}
	// And clearing again is a no-op.
	if changed, err = svc.syncSelectiveRoutesSlot(ctx, nil); err != nil || changed {
		t.Fatalf("second clear: changed=%v err=%v", changed, err)
	}
}

func TestHealLegacySelectiveRoutesSlotIfNeeded(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotSelectiveRoutes, Filename: "19-selective-routes.json"})
	_ = orch.SetEnabled(orchestrator.SlotSelectiveRoutes, true)

	broken := []byte(`{
  "inbounds": null,
  "outbounds": null,
  "route": {"rules": [{"action": "route", "outbound": "tun", "ip_cidr": ["1.2.3.4/32"]}]}
}`)
	if err := os.WriteFile(filepath.Join(dir, "19-selective-routes.json"), broken, 0644); err != nil {
		t.Fatal(err)
	}

	svc := &ServiceImpl{deps: Deps{Orch: orch}}
	if err := svc.healLegacySelectiveRoutesSlotIfNeeded(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixed, err := os.ReadFile(filepath.Join(dir, "19-selective-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytesContainsLegacyNullSlotKeys(fixed) {
		t.Fatalf("file still broken: %s", fixed)
	}
}

func TestHealLegacySelectiveRoutesSlot_StripsAwgmManaged(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotSelectiveRoutes, Filename: "19-selective-routes.json"})
	_ = orch.SetEnabled(orchestrator.SlotSelectiveRoutes, true)

	broken := []byte(`{
  "route": {"rules": [{"action": "route", "outbound": "tun", "ip_cidr": ["1.2.3.4/32"], "awgm_managed": "selective-ip"}]}
}`)
	if err := os.WriteFile(filepath.Join(dir, "19-selective-routes.json"), broken, 0644); err != nil {
		t.Fatal(err)
	}

	svc := &ServiceImpl{deps: Deps{Orch: orch}}
	if err := svc.healLegacySelectiveRoutesSlotIfNeeded(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixed, err := os.ReadFile(filepath.Join(dir, "19-selective-routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fixed), "awgm_managed") {
		t.Fatalf("awgm_managed still present: %s", fixed)
	}
}

func TestStripLegacySelectiveRulesFromRouter(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouting, Filename: "21-routing.json"})
	_ = orch.SetEnabled(orchestrator.SlotRouting, true)

	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{AwgmManaged: selectiveIPRuleManaged, Action: "route", Outbound: "tun", IPCIDR: []string{"188.40.167.82/32"}},
		{Action: "route", Outbound: "tun", DomainSuffix: []string{"2ip.ru"}},
	}
	svc := &ServiceImpl{deps: Deps{Orch: orch}}
	if err := svc.persistConfigDirect(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}

	changed, err := svc.stripLegacySelectiveRulesFromRouter(context.Background())
	if err != nil || !changed {
		t.Fatalf("strip: changed=%v err=%v", changed, err)
	}
	rules, err := svc.ListRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].DomainSuffix[0] != "2ip.ru" {
		t.Fatalf("unexpected rules after strip: %+v", rules)
	}
}

// #564: «спящий» селектив (флаг взведён, но режим fakeip-tun) НЕ должен
// включать слот 19-selective-routes.json — завершившаяся пересборка иначе
// вытаскивала припаркованный слот из disabled/ и её правила мерджились в
// работающий fakeip-конфиг.
func TestSyncSelectiveRoutesSlot_DormantSelectiveKeepsSlotParked(t *testing.T) {
	dir := t.TempDir()
	orch := orchestrator.New(dir, nil)
	_ = orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotSelectiveRoutes, Filename: "19-selective-routes.json"})

	store := storage.NewSettingsStore(t.TempDir())
	st, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	st.SingboxRouter.Enabled = true
	st.SingboxRouter.RoutingMode = "fakeip-tun"
	st.SingboxRouter.SelectiveBypass = true // dormant: унаследован из tproxy
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}

	svc := &ServiceImpl{deps: Deps{Orch: orch, Settings: store}}
	routes := map[string][]string{"tun": {"1.2.3.4/32"}}
	if _, err := svc.syncSelectiveRoutesSlot(context.Background(), routes); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "19-selective-routes.json")); err == nil {
		t.Fatal("dormant selective must NOT materialize the slot in config.d/")
	}
	// Содержимое пишется в disabled/ (паркуется), не теряется.
	if _, err := os.Stat(filepath.Join(dir, "disabled", "19-selective-routes.json")); err != nil {
		t.Fatalf("slot content must be parked in disabled/: %v", err)
	}

	// Активный селектив (tproxy) — прежнее поведение: слот включается.
	st.SingboxRouter.RoutingMode = "tproxy"
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	changed, err := svc.syncSelectiveRoutesSlot(context.Background(), routes)
	if err != nil || !changed {
		t.Fatalf("active selective sync: changed=%v err=%v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "19-selective-routes.json")); err != nil {
		t.Fatalf("active selective must enable the slot: %v", err)
	}
}

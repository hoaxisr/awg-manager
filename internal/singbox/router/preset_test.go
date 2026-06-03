package router

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

func testCatalog(t *testing.T) *presets.Catalog {
	t.Helper()
	return presets.NewCatalog(presets.NewStore(t.TempDir()))
}

func TestPresetFromUnifiedFiltersDNSOnly(t *testing.T) {
	dnsOnly := presets.Preset{ID: "x", Engines: presets.Engines{DNS: &presets.DNSEngine{Domains: []string{"a.com"}}}}
	if _, ok := presetFromUnified(dnsOnly); ok {
		t.Error("DNS-only preset must not be router-applicable")
	}
	sb := presets.Preset{
		ID: "y", Name: "Y", Category: "social", IconSlug: "y",
		Engines: presets.Engines{Singbox: &presets.SingboxEngine{
			RuleSets: []presets.RuleRef{{Tag: "geosite-y", URL: "u/y.srs"}}, Action: "tunnel"}},
	}
	rp, ok := presetFromUnified(sb)
	if !ok || len(rp.RuleSets) != 1 || rp.RuleSets[0].Tag != "geosite-y" {
		t.Fatalf("ruleset mapping: %+v", rp)
	}
	if len(rp.Rules) != 1 || rp.Rules[0].RuleSetRef != "geosite-y" || rp.Rules[0].ActionTarget != "tunnel" {
		t.Fatalf("rule synthesis: %+v", rp.Rules)
	}
}

func TestListRouterPresetsFiltersAndCount(t *testing.T) {
	list, err := listRouterPresets(testCatalog(t))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := map[string]bool{}
	for _, p := range list {
		ids[p.ID] = true
		if len(p.RuleSets) == 0 {
			t.Errorf("router preset %q has no rule-sets", p.ID)
		}
	}
	if ids["russian-services"] || ids["atlassian"] {
		t.Error("DNS-only presets must be filtered out of the router catalog")
	}
	if !ids["youtube"] || !ids["meta"] {
		t.Error("sing-box presets must be present")
	}
	if len(list) < 60 {
		t.Fatalf("expected ~74 router presets, got %d", len(list))
	}
}

func TestApplyPresetBackwardCompat(t *testing.T) {
	cat := testCatalog(t)

	yt, err := findRouterPreset(cat, "youtube")
	if err != nil {
		t.Fatalf("find youtube: %v", err)
	}
	cfg := &RouterConfig{}
	if err := ApplyPresetToConfig(cfg, yt, "myout"); err != nil {
		t.Fatalf("apply youtube: %v", err)
	}
	if len(cfg.Route.RuleSet) != 1 || cfg.Route.RuleSet[0].Tag != "geosite-youtube" ||
		cfg.Route.RuleSet[0].Type != "remote" || cfg.Route.RuleSet[0].Format != "binary" {
		t.Fatalf("youtube rule-set: %+v", cfg.Route.RuleSet)
	}
	if len(cfg.Route.Rules) != 1 || cfg.Route.Rules[0].Action != "route" ||
		cfg.Route.Rules[0].Outbound != "myout" || len(cfg.Route.Rules[0].RuleSet) != 1 ||
		cfg.Route.Rules[0].RuleSet[0] != "geosite-youtube" {
		t.Fatalf("youtube rule: %+v", cfg.Route.Rules)
	}

	ads, err := findRouterPreset(cat, "ads")
	if err != nil {
		t.Fatalf("find ads: %v", err)
	}
	cfg2 := &RouterConfig{}
	if err := ApplyPresetToConfig(cfg2, ads, ""); err != nil {
		t.Fatalf("apply ads: %v", err)
	}
	if cfg2.Route.RuleSet[0].Tag != "geosite-category-ads-all" {
		t.Fatalf("ads rule-set: %+v", cfg2.Route.RuleSet)
	}
	if cfg2.Route.Rules[0].Action != "reject" || cfg2.Route.Rules[0].Outbound != "" {
		t.Fatalf("ads rule: %+v", cfg2.Route.Rules)
	}
}

func TestApplyPresetTunnelRequiresOutbound(t *testing.T) {
	yt, _ := findRouterPreset(testCatalog(t), "youtube")
	if err := ApplyPresetToConfig(&RouterConfig{}, yt, ""); err == nil {
		t.Error("tunnel preset without outbound must error")
	}
}

func TestNilCatalogReturnsErrorNotPanic(t *testing.T) {
	if _, err := listRouterPresets(nil); err == nil {
		t.Error("listRouterPresets(nil) must return an error, not panic")
	}
	if _, err := findRouterPreset(nil, "youtube"); err == nil {
		t.Error("findRouterPreset(nil) must return an error, not panic")
	}
}

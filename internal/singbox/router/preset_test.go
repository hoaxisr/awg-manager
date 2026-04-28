package router

import (
	"testing"
)

func TestListPresetsContainsYoutube(t *testing.T) {
	presets := ListPresets()
	var found bool
	for _, p := range presets {
		if p.ID == "youtube" {
			found = true
			if len(p.RuleSets) == 0 || len(p.Rules) == 0 {
				t.Error("youtube preset missing rulesets or rules")
			}
		}
	}
	if !found {
		t.Error("youtube preset not in list")
	}
}

func TestPresetYoutubeAppliesRuleSetAndRule(t *testing.T) {
	cfg := NewEmptyConfig()
	if err := ApplyPresetToConfig(cfg, "youtube", "Germany VLESS"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 1 || cfg.Route.RuleSet[0].Tag != "geosite-youtube" {
		t.Errorf("rule_set: %+v", cfg.Route.RuleSet)
	}
	if len(cfg.Route.Rules) != 1 || cfg.Route.Rules[0].Outbound != "Germany VLESS" {
		t.Errorf("rules: %+v", cfg.Route.Rules)
	}
}

func TestPresetAdsAppliesReject(t *testing.T) {
	cfg := NewEmptyConfig()
	if err := ApplyPresetToConfig(cfg, "ads", ""); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.Rules) != 1 || cfg.Route.Rules[0].Action != "reject" {
		t.Errorf("rules: %+v", cfg.Route.Rules)
	}
}

func TestPresetTunnelRequiresOutbound(t *testing.T) {
	cfg := NewEmptyConfig()
	err := ApplyPresetToConfig(cfg, "youtube", "")
	if err == nil {
		t.Error("expected error when outbound empty for tunnel preset")
	}
}

func TestPresetReAddRuleSet(t *testing.T) {
	cfg := NewEmptyConfig()
	if err := ApplyPresetToConfig(cfg, "youtube", "Germany"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPresetToConfig(cfg, "youtube", "France"); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 1 {
		t.Errorf("rule_set should not duplicate: %+v", cfg.Route.RuleSet)
	}
	if len(cfg.Route.Rules) != 2 {
		t.Errorf("expected 2 rules, got %d", len(cfg.Route.Rules))
	}
}

func TestPresetUnknown(t *testing.T) {
	cfg := NewEmptyConfig()
	err := ApplyPresetToConfig(cfg, "nonexistent", "")
	if err == nil || !isSubstring(err.Error(), "not found") {
		t.Errorf("expected not found error, got %v", err)
	}
}

func isSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

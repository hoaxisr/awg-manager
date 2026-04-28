package router

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/internalpresets"
)

type Preset = internalpresets.Preset

func ListPresets() []Preset {
	return internalpresets.All()
}

func ApplyPresetToConfig(cfg *RouterConfig, presetID, outboundTag string) error {
	p, err := findPreset(presetID)
	if err != nil {
		return err
	}
	for _, rs := range p.RuleSets {
		if hasRuleSet(cfg.Route.RuleSet, rs.Tag) {
			continue
		}
		cfg.Route.RuleSet = append(cfg.Route.RuleSet, RuleSet{
			Tag:            rs.Tag,
			Type:           "remote",
			Format:         "binary",
			URL:            rs.URL,
			UpdateInterval: "24h",
		})
	}
	for _, pr := range p.Rules {
		rule := Rule{
			RuleSet: []string{pr.RuleSetRef},
			Action:  actionFor(pr.ActionTarget),
		}
		if pr.ActionTarget == "tunnel" {
			if outboundTag == "" {
				return fmt.Errorf("preset %q: outbound required for tunnel target", presetID)
			}
			rule.Outbound = outboundTag
		} else if pr.ActionTarget == "direct" {
			rule.Outbound = "direct"
		}
		if err := cfg.AddRule(rule); err != nil {
			return err
		}
	}
	return nil
}

func findPreset(id string) (Preset, error) {
	for _, p := range ListPresets() {
		if p.ID == id {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("preset %q not found", id)
}

func hasRuleSet(existing []RuleSet, tag string) bool {
	for _, rs := range existing {
		if rs.Tag == tag {
			return true
		}
	}
	return false
}

func actionFor(target string) string {
	switch target {
	case "reject":
		return "reject"
	default:
		return "route"
	}
}

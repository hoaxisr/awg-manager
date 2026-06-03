package router

import (
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

type RuleRef struct {
	Tag string `json:"tag"`
	URL string `json:"url"`
}

type RuleLink struct {
	RuleSetRef   string `json:"ruleSetRef"`
	ActionTarget string `json:"actionTarget"`
}

type Preset struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Category  string     `json:"category,omitempty"`
	IconSlug  string     `json:"iconSlug,omitempty"`
	RuleSets  []RuleRef  `json:"ruleSets"`
	Rules     []RuleLink `json:"rules"`
	Notice    string     `json:"notice,omitempty"`
	Featured  bool       `json:"featured,omitempty"`
	Sensitive bool       `json:"sensitive,omitempty"`
}

// presetFromUnified maps a unified catalog preset to the router view. Returns
// false for DNS-only presets (no sing-box engine) — they aren't router-applicable.
func presetFromUnified(p presets.Preset) (Preset, bool) {
	sb := p.Engines.Singbox
	if sb == nil {
		return Preset{}, false
	}
	out := Preset{
		ID: p.ID, Name: p.Name, Category: p.Category, IconSlug: p.IconSlug,
		Notice: p.Notice, Featured: p.Featured, Sensitive: p.Sensitive,
	}
	for _, rs := range sb.RuleSets {
		out.RuleSets = append(out.RuleSets, RuleRef{Tag: rs.Tag, URL: rs.URL})
		out.Rules = append(out.Rules, RuleLink{RuleSetRef: rs.Tag, ActionTarget: sb.Action})
	}
	return out, true
}

func listRouterPresets(cat *presets.Catalog) ([]Preset, error) {
	if cat == nil {
		return nil, fmt.Errorf("preset catalog not configured")
	}
	all, err := cat.List()
	if err != nil {
		return nil, err
	}
	out := make([]Preset, 0, len(all))
	for _, p := range all {
		if rp, ok := presetFromUnified(p); ok {
			out = append(out, rp)
		}
	}
	return out, nil
}

func findRouterPreset(cat *presets.Catalog, id string) (Preset, error) {
	if cat == nil {
		return Preset{}, fmt.Errorf("preset catalog not configured")
	}
	all, err := cat.List()
	if err != nil {
		return Preset{}, err
	}
	for _, p := range all {
		if p.ID != id {
			continue
		}
		if rp, ok := presetFromUnified(p); ok {
			return rp, nil
		}
		return Preset{}, fmt.Errorf("preset %q is not router-applicable", id)
	}
	return Preset{}, fmt.Errorf("preset %q not found", id)
}

func ApplyPresetToConfig(cfg *RouterConfig, p Preset, outboundTag string) error {
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
				return fmt.Errorf("preset %q: outbound required for tunnel target", p.ID)
			}
			rule.Outbound = outboundTag
		} else if pr.ActionTarget == "direct" {
			rule.Outbound = "direct"
		}
		dup := false
		for _, existing := range cfg.Route.Rules {
			if ruleEqual(existing, rule) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		if err := cfg.AddRule(rule); err != nil {
			return err
		}
	}
	return nil
}

func hasRuleSet(existing []RuleSet, tag string) bool {
	for _, rs := range existing {
		if rs.Tag == tag {
			return true
		}
	}
	return false
}

func ruleEqual(a, b Rule) bool {
	if a.Action != b.Action {
		return false
	}
	if a.Outbound != b.Outbound {
		return false
	}
	if len(a.RuleSet) != len(b.RuleSet) {
		return false
	}
	for i := range a.RuleSet {
		if a.RuleSet[i] != b.RuleSet[i] {
			return false
		}
	}
	return true
}

func actionFor(target string) string {
	switch target {
	case "reject":
		return "reject"
	default:
		return "route"
	}
}

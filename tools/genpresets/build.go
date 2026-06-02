package main

import (
	"sort"

	"github.com/hoaxisr/awg-manager/internal/presets"
)

const dnsInlineCap = 500

type decompiler func(url string) (domains, subnets []string, err error)

// build maintains the unified catalog: starts from the committed base, refreshes
// DNS for every sing-box preset by re-decompiling its rule-sets, and appends any
// `additions` not already present. Non-DNS fields of base presets are preserved.
func build(base []presets.Preset, adds []addition, dc decompiler) []presets.Preset {
	out := make([]presets.Preset, len(base))
	copy(out, base)

	have := map[string]bool{}
	for i := range out {
		have[out[i].ID] = true
		if sb := out[i].Engines.Singbox; sb != nil && len(sb.RuleSets) > 0 {
			refreshDNS(&out[i], sb.RuleSets, dc)
		}
	}

	for _, a := range adds {
		if have[a.id] {
			continue
		}
		p := presets.Preset{
			ID: a.id, Name: a.name, IconSlug: a.iconSlug, Category: a.category,
			Engines: presets.Engines{Singbox: &presets.SingboxEngine{
				RuleSets: []presets.RuleRef{{Tag: "geosite-" + a.id, URL: a.srsURL}},
				Action:   a.action,
			}},
		}
		refreshDNS(&p, p.Engines.Singbox.RuleSets, dc)
		out = append(out, p)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// refreshDNS replaces p.Engines.DNS with freshly decompiled domains/subnets
// (deduped across rule-sets), or clears it when empty or over the cap.
func refreshDNS(p *presets.Preset, sets []presets.RuleRef, dc decompiler) {
	var domains, subnets []string
	seenD, seenS := map[string]bool{}, map[string]bool{}
	for _, rs := range sets {
		d, s, err := dc(rs.URL)
		if err != nil {
			panic("decompile " + rs.URL + ": " + err.Error())
		}
		for _, x := range d {
			addUnique(&domains, seenD, x)
		}
		for _, x := range s {
			addUnique(&subnets, seenS, x)
		}
	}
	if len(domains)+len(subnets) == 0 || len(domains)+len(subnets) > dnsInlineCap {
		p.Engines.DNS = nil
		return
	}
	p.Engines.DNS = &presets.DNSEngine{Domains: domains, Subnets: subnets}
}

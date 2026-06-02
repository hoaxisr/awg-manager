package main

import (
	"sort"

	"github.com/hoaxisr/awg-manager/internal/presets"
	ip "github.com/hoaxisr/awg-manager/internal/singbox/router/internalpresets"
)

// dnsInlineCap: decompiled lists larger than this are not inlined as DNS
// (the preset stays sing-box-only).
const dnsInlineCap = 500

// servicePreset mirrors the exported SERVICE_PRESETS JSON.
type servicePreset struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Domains         []string `json:"domains"`
	SubscriptionURL string   `json:"subscriptionUrl"`
	Covers          []string `json:"covers"`
	Notice          string   `json:"notice"`
}

// decompiler fetches a .srs URL and returns its DNS-compatible domains+subnets.
// Injected so tests need no network/sing-box.
type decompiler func(url string) (domains, subnets []string, err error)

// build assembles the unified catalog. It panics on decompile error (a generator
// is a dev tool — a hard failure is the right signal).
func build(router []ip.Preset, svc []servicePreset, adds []addition, dc decompiler) []presets.Preset {
	out := map[string]*presets.Preset{}
	order := []string{}
	ensure := func(id string) *presets.Preset {
		if p, ok := out[id]; ok {
			return p
		}
		p := &presets.Preset{ID: id, Engines: presets.Engines{}}
		out[id] = p
		order = append(order, id)
		return p
	}

	for _, r := range router {
		p := ensure(r.ID)
		p.Name, p.Category, p.IconSlug, p.Notice, p.Sensitive, p.Featured = r.Name, r.Category, r.IconSlug, r.Notice, r.Sensitive, r.Featured
		if len(r.RuleSets) > 0 {
			action := "tunnel"
			if len(r.Rules) > 0 && r.Rules[0].ActionTarget != "" {
				action = r.Rules[0].ActionTarget
			}
			refs := make([]presets.RuleRef, len(r.RuleSets))
			for i, rs := range r.RuleSets {
				refs[i] = presets.RuleRef{Tag: rs.Tag, URL: rs.URL}
			}
			p.Engines.Singbox = &presets.SingboxEngine{RuleSets: refs, Action: action}
			applyDecompiledDNS(p, r.RuleSets, dc)
		}
	}

	for _, s := range svc {
		if isDroppedSourceID(s.ID) {
			continue
		}
		id := canonicalID(s.ID)
		if _, isRouter := out[id]; isRouter {
			continue
		}
		meta, ok := dnsOnlyMeta[id]
		if !ok {
			continue
		}
		p := ensure(id)
		p.Name, p.IconSlug, p.Category, p.Notice = meta.name, meta.iconSlug, meta.category, s.Notice
		dom, sub := splitDomainsCIDR(s.Domains)
		p.Engines.DNS = &presets.DNSEngine{Domains: dom, Subnets: sub, SubscriptionURL: s.SubscriptionURL}
	}

	for _, a := range adds {
		p := ensure(a.id)
		p.Name, p.IconSlug, p.Category = a.name, a.iconSlug, a.category
		p.Engines.Singbox = &presets.SingboxEngine{
			RuleSets: []presets.RuleRef{{Tag: "geosite-" + a.id, URL: a.srsURL}},
			Action:   a.action,
		}
		applyDecompiledDNS(p, []ip.RuleRef{{Tag: "geosite-" + a.id, URL: a.srsURL}}, dc)
	}

	result := make([]presets.Preset, 0, len(order))
	for _, id := range order {
		result = append(result, *out[id])
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// applyDecompiledDNS decompiles all rule-sets, concatenates, and inlines DNS
// only when the total is within the cap.
func applyDecompiledDNS(p *presets.Preset, sets []ip.RuleRef, dc decompiler) {
	var domains, subnets []string
	seenD, seenS := map[string]bool{}, map[string]bool{}
	for _, rs := range sets {
		d, s, err := dc(rs.URL)
		if err != nil {
			panic("decompile " + rs.URL + ": " + err.Error())
		}
		// Dedup across rule-sets so a shared domain isn't double-counted
		// (which could push the total over dnsInlineCap) or duplicated inline.
		for _, x := range d {
			addUnique(&domains, seenD, x)
		}
		for _, x := range s {
			addUnique(&subnets, seenS, x)
		}
	}
	if len(domains)+len(subnets) == 0 || len(domains)+len(subnets) > dnsInlineCap {
		return
	}
	p.Engines.DNS = &presets.DNSEngine{Domains: domains, Subnets: subnets}
}

// splitDomainsCIDR separates a mixed SERVICE_PRESETS list into FQDNs vs CIDR.
func splitDomainsCIDR(in []string) (domains, subnets []string) {
	for _, e := range in {
		if isCIDR(e) {
			subnets = append(subnets, e)
		} else {
			domains = append(domains, e)
		}
	}
	return
}

func isCIDR(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return true
		}
	}
	return false
}

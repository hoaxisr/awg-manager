package router

import (
	"context"
	"fmt"
	"net/netip"
)

// fakeIPCIDRRouteComment labels specific dst-CIDR routes installed for proxy-
// routed rules. Distinct from fakeIPPoolRouteComment so the two route families
// are independently recognizable in NDMS running-config.
const fakeIPCIDRRouteComment = "awgm fakeip cidr"

// isProxyRoute reports whether a fakeip route rule sends matched traffic to a
// proxy (non-direct) outbound. Only such rules' dst ip_cidr become tun routes:
// the same rule proxies them in sing-box, so they never fall to route.final=
// direct and never loop. reject rules (no Outbound) and direct rules are excluded.
func isProxyRoute(r Rule) bool {
	return r.ActionIsRoute() && r.Outbound != "" && r.Outbound != "direct"
}

// loopSafeProxyRule reports whether a proxy route-rule's SHAPE is safe: the ONLY
// matchers are ip_cidr and/or rule_set. Any narrowing matcher (port,
// source_ip_cidr, domain_suffix, protocol, nested logical, ip_is_private) makes a
// by-IP packet potentially fall through to route.final (seeded "direct") → the
// kernel re-routes it to the tun via our own CIDR route → loop; such rules
// contribute no routes.
// NB: shape alone is not sufficient once a rule combines its own ip_cidr with a
// rule_set — sing-box 1.14 merges a set into the referencing rule only when the
// set is "mergeable" (see mergeableRuleSetRule). The merged-matching gate lives
// in desiredTunCIDRs / remoteTunCIDRs, which need the rule-set bodies.
// INVARIANT (guarded by TestLoopSafeProxyRule_AllMatchersCovered): if you add a new matcher field to Rule, add it to the exclusion below or the loop-safety hole reopens.
func loopSafeProxyRule(r Rule) bool {
	return isProxyRoute(r) &&
		r.Type == "" && r.Mode == "" && len(r.Rules) == 0 &&
		len(r.DomainSuffix) == 0 && len(r.Domain) == 0 && len(r.SourceIPCIDR) == 0 &&
		len(r.SourceMACAddress) == 0 &&
		len(r.Port) == 0 && r.Protocol == "" && r.IPIsPrivate == nil &&
		len(r.Inbound) == 0 && r.Network == "" && r.UDPTimeout == "" &&
		r.AwgmManaged == ""
}

// cgnat is RFC 6598 shared address space (100.64.0.0/10) — never a valid proxy
// target and NOT classified private by net/netip, so excluded explicitly.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// excludedAddr reports addresses that must never become tun routes: RFC1918/
// loopback/link-local/multicast (already covered by the ip_is_private→direct
// overlay) plus CGNAT. NB: the fakeip pool (e.g. 198.18.0.0/15) is intentionally
// NOT excluded — a user CIDR overlapping the pool routes to the SAME tun as the
// pool route, so it is benign (no conflicting destination).
func excludedAddr(a netip.Addr) bool {
	return a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || cgnat.Contains(a)
}

// normalizeCIDR canonicalizes a sing-box ip_cidr entry (CIDR or bare address)
// to a masked prefix string and reports its family. Returns ok=false for
// malformed input or for excludedAddr ranges.
func normalizeCIDR(c string) (norm string, is4 bool, ok bool) {
	if pfx, err := netip.ParsePrefix(c); err == nil {
		a := pfx.Addr()
		if excludedAddr(a) {
			return "", false, false
		}
		return pfx.Masked().String(), a.Is4(), true
	}
	if a, err := netip.ParseAddr(c); err == nil {
		if excludedAddr(a) {
			return "", false, false
		}
		bits := 32
		if a.Is6() {
			bits = 128
		}
		return netip.PrefixFrom(a, bits).String(), a.Is4(), true
	}
	return "", false, false
}

// ruleSetCIDRs extracts ip_cidr values from an inline/materialized rule-set's
// Rules ([]map[string]any). After restoreConfig, inline + managed-local .srs sets
// carry their rules here; true remote sets have empty Rules (handled elsewhere).
// Values arrive as []any (JSON).
func ruleSetCIDRs(rs RuleSet) []string {
	var out []string
	for _, m := range rs.Rules {
		// Loop-safety at the inline-rule level: a rule-set matches by OR of its
		// rules, so an inline rule is only safe to route to the tun if it is PURE
		// — its sole key is ip_cidr. If it ANDs ip_cidr with any other matcher
		// (port, network, domain*, source_ip_cidr, invert, ip_version, …), a
		// raw-IP packet to the CIDR may not match the rule-set → it falls through
		// to route.final=direct → the kernel re-routes it to the tun via our own
		// CIDR route → loop. So skip mixed inline rules (their by-IP simply isn't
		// caught; the domain matcher still routes those flows via fakeip DNS).
		if len(m) != 1 {
			continue
		}
		switch arr := m["ip_cidr"].(type) {
		case []any:
			for _, e := range arr {
				if s, ok := e.(string); ok {
					out = append(out, s)
				}
			}
		case []string:
			out = append(out, arr...)
		case string:
			out = append(out, arr)
		}
	}
	return out
}

// destinationAddressKeys — ключи headless-правила, попадающие в группу
// destinationAddress sing-box (route/rule/rule_headless.go). Внутри группы
// условия OR-ятся, между группами — AND. Правило, состоящее ТОЛЬКО из этих
// ключей, при мерже не добавляет требований сверх destinationAddress: если
// внешний ip_cidr её уже удовлетворил, объединение групп «done».
var destinationAddressKeys = map[string]bool{
	"domain": true, "domain_suffix": true, "domain_keyword": true,
	"domain_regex": true, "ip_cidr": true,
}

// mergeableRuleSetRule — зеркало апстримного mergeableRuleIn
// (route/rule/rule_item_rule_set.go:97, sing-box 1.14.0-beta.1) для исходной
// (JSON) формы набора. Набор вливается в ссылающееся правило, только если
// состоит РОВНО из одного default-правила без invert и без вложенного rule_set;
// возвращает это правило, иначе nil. Не-mergeable набор в beta.1 матчится
// отдельно и только при выполненных собственных условиях внешнего правила
// (`outerDone && ruleSet.Match(...)`) — в 1.13 состояние внешнего правила
// передавалось внутрь набора любой формы, и «мерж» работал всегда.
// Remote-набор в конфиге несёт пустой Rules → консервативно немержим.
func mergeableRuleSetRule(rs RuleSet) map[string]any {
	if len(rs.Rules) != 1 {
		return nil
	}
	m := rs.Rules[0]
	if t, ok := m["type"].(string); ok && t != "" && t != "default" {
		return nil
	}
	if inv, ok := m["invert"].(bool); ok && inv {
		return nil
	}
	if nested, ok := m["rule_set"]; ok && nested != nil {
		return nil
	}
	return m
}

// ownIPCIDRLoopSafe reports whether a rule's OWN ip_cidr values still guarantee a
// match under beta.1 merged matching. Without a rule_set they trivially do. With
// one, the packet's own-CIDR hit satisfies only the destinationAddress group, so
// the rule matches only via a set that (a) merges and (b) adds nothing outside
// that group — matching upstream scenario A/G. rule_set tags are OR-ed, so one
// such set is enough.
func ownIPCIDRLoopSafe(r Rule, byTag map[string]RuleSet) bool {
	if len(r.RuleSet) == 0 {
		return true
	}
	for _, tag := range r.RuleSet {
		rs, ok := byTag[tag]
		if !ok {
			continue
		}
		m := mergeableRuleSetRule(rs)
		if m == nil {
			continue
		}
		destOnly := true
		for k := range m {
			if k != "type" && !destinationAddressKeys[k] {
				destOnly = false
				break
			}
		}
		if destOnly {
			return true
		}
	}
	return false
}

// addressOrBranches recognizes the shape normalizeAddressOrRule produces —
// logical(or) over exactly two branches, one carrying only rule_set tags and
// the other only destination-address matchers — and returns the flat
// equivalent. Each branch matches on its own there, so a by-IP packet to
// either side is guaranteed to proxy: no mergeability gate needed, unlike the
// flat rule_set+ip_cidr form. ok=false for any other shape (including the
// logical(and) wrapper a narrowing matcher produces — that one CAN miss).
func addressOrBranches(r Rule) (Rule, bool) {
	if r.Type != "logical" || r.Mode != "or" || len(r.Rules) != 2 {
		return Rule{}, false
	}
	sets, addrs := r.Rules[0], r.Rules[1]
	if len(sets.RuleSet) == 0 || !onlyMatchers(sets, func(x *Rule) { x.RuleSet = nil }) {
		return Rule{}, false
	}
	if !onlyMatchers(addrs, func(x *Rule) {
		x.Domain, x.DomainSuffix, x.IPCIDR, x.IPIsPrivate = nil, nil, nil, nil
	}) {
		return Rule{}, false
	}
	return Rule{
		RuleSet: sets.RuleSet, Domain: addrs.Domain,
		DomainSuffix: addrs.DomainSuffix, IPCIDR: addrs.IPCIDR,
	}, true
}

// onlyMatchers reports whether clearing the listed matcher fields leaves the
// rule without any matcher at all. Expressed through hasAnyMatcher so a matcher
// field added to Rule later is accounted for here without an edit.
func onlyMatchers(r Rule, clear func(*Rule)) bool {
	rest := r
	clear(&rest)
	return !rest.hasAnyMatcher()
}

// desiredTunCIDRs returns the deduped, normalized dst ip_cidr values that proxy
// route-rules select — directly via ip_cidr and via referenced inline/managed-local
// rule-sets — split into v4 and v6. These get specific NDMS routes to the tun.
func desiredTunCIDRs(cfg *RouterConfig) (v4 []string, v6 []string) {
	if cfg == nil {
		return nil, nil
	}
	byTag := make(map[string]RuleSet, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		byTag[rs.Tag] = rs
	}
	seen := map[string]bool{}
	add := func(c string) {
		norm, is4, ok := normalizeCIDR(c)
		if !ok || seen[norm] {
			return
		}
		seen[norm] = true
		if is4 {
			v4 = append(v4, norm)
		} else {
			v6 = append(v6, norm)
		}
	}
	for _, r := range cfg.Route.Rules {
		if flat, isOr := addressOrBranches(r); isOr {
			if !isProxyRoute(r) {
				continue
			}
			for _, c := range flat.IPCIDR {
				add(c)
			}
			for _, tag := range flat.RuleSet {
				for _, c := range ruleSetCIDRs(byTag[tag]) {
					add(c)
				}
			}
			continue
		}
		if !loopSafeProxyRule(r) {
			continue
		}
		if ownIPCIDRLoopSafe(r, byTag) {
			for _, c := range r.IPCIDR {
				add(c)
			}
		}
		for _, tag := range r.RuleSet {
			rs, ok := byTag[tag]
			if !ok {
				continue
			}
			// beta.1: не-mergeable набор матчится самостоятельно и только при
			// выполненных условиях внешнего правила. Пакет по CIDR набора
			// собственный ip_cidr правила не удовлетворяет → правило не
			// совпадёт → route.final=direct → петля (сценарий C).
			if len(r.IPCIDR) > 0 && mergeableRuleSetRule(rs) == nil {
				continue
			}
			for _, c := range ruleSetCIDRs(rs) {
				add(c)
			}
		}
	}
	return v4, v6
}

// addCIDRRoute installs one specific dst route to the tun. v4 routes carry the
// CIDR comment (recognizable in NDMS config); the v6 form emits only
// network+interface (see StaticRouteSpec.V6).
func (s *ServiceImpl) addCIDRRoute(ctx context.Context, ndmsName, cidr string, v6 bool) error {
	if v6 {
		return s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
			V6: true, Network: cidr, Interface: ndmsName,
		})
	}
	net4, mask4, err := poolV4NetMask(cidr)
	if err != nil {
		return err
	}
	return s.deps.StaticRoutes.AddStaticRoute(ctx, StaticRouteSpec{
		Network: net4, Mask: mask4, Interface: ndmsName, Comment: fakeIPCIDRRouteComment,
	})
}

// removeCIDRRoute deletes one specific dst route from the tun.
func (s *ServiceImpl) removeCIDRRoute(ctx context.Context, ndmsName, cidr string, v6 bool) error {
	if v6 {
		return s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
			V6: true, Network: cidr, Interface: ndmsName,
		})
	}
	net4, mask4, err := poolV4NetMask(cidr)
	if err != nil {
		return err
	}
	return s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
		Network: net4, Mask: mask4, Interface: ndmsName,
	})
}

// syncTunCIDRRoutes converges the tun's specific CIDR routes from the previous
// config to the new one: adds CIDRs newly proxy-routed, removes ones no longer
// proxy-routed. Best-effort — a failed NDMS call logs and continues (reconcile
// re-asserts); a route POST failure must not roll back an otherwise-valid config
// persist. Logs the synced/desired counts (observability for route-scale).
func (s *ServiceImpl) syncTunCIDRRoutes(ctx context.Context, ndmsName string, before, after *RouterConfig) {
	if s.deps.StaticRoutes == nil {
		return
	}
	prevV4, prevV6 := desiredTunCIDRs(before)
	nextV4, nextV6 := desiredTunCIDRs(after)
	s.applyCIDRRouteDiff(ctx, ndmsName, prevV4, nextV4, false)
	s.applyCIDRRouteDiff(ctx, ndmsName, prevV6, nextV6, true)
}

func (s *ServiceImpl) applyCIDRRouteDiff(ctx context.Context, ndmsName string, prev, next []string, v6 bool) {
	prevSet := make(map[string]bool, len(prev))
	for _, c := range prev {
		prevSet[c] = true
	}
	nextSet := make(map[string]bool, len(next))
	for _, c := range next {
		nextSet[c] = true
	}

	adds, removes := 0, 0
	for _, c := range next {
		if prevSet[c] {
			continue
		}
		if err := s.addCIDRRoute(ctx, ndmsName, c, v6); err != nil {
			s.appLog.Warn("fakeip-cidr", ndmsName, "add cidr route "+c+": "+err.Error())
			continue
		}
		adds++
	}
	for _, c := range prev {
		if nextSet[c] {
			continue
		}
		if err := s.removeCIDRRoute(ctx, ndmsName, c, v6); err != nil {
			s.appLog.Warn("fakeip-cidr", ndmsName, "remove cidr route "+c+": "+err.Error())
			continue
		}
		removes++
	}
	if adds > 0 || removes > 0 {
		s.appLog.Info("fakeip-cidr", ndmsName,
			fmt.Sprintf("cidr routes synced: +%d -%d (v6=%v, desired=%d)", adds, removes, v6, len(next)))
	}
}

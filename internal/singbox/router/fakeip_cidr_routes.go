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
	return r.Action == "route" && r.Outbound != "" && r.Outbound != "direct"
}

// loopSafeProxyRule reports whether a proxy route-rule's dst CIDRs are safe to
// route to the tun. Safe iff the ONLY matchers are ip_cidr and/or rule_set: then
// any raw-IP packet to a routed CIDR is guaranteed to match this rule and be
// proxied, so it never falls through to route.final (seeded "direct") and loops
// back to the tun. Any narrowing matcher (port, source_ip_cidr, domain_suffix,
// protocol, nested logical, ip_is_private) makes a by-IP packet potentially
// fall through → loop; such rules contribute no routes.
// INVARIANT (guarded by TestLoopSafeProxyRule_AllMatchersCovered): if you add a new matcher field to Rule, add it to the exclusion below or the loop-safety hole reopens.
func loopSafeProxyRule(r Rule) bool {
	return isProxyRoute(r) &&
		r.Type == "" && r.Mode == "" && len(r.Rules) == 0 &&
		len(r.DomainSuffix) == 0 && len(r.SourceIPCIDR) == 0 &&
		len(r.Port) == 0 && r.Protocol == "" && r.IPIsPrivate == nil
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
		if !loopSafeProxyRule(r) {
			continue
		}
		for _, c := range r.IPCIDR {
			add(c)
		}
		for _, tag := range r.RuleSet {
			if rs, ok := byTag[tag]; ok {
				for _, c := range ruleSetCIDRs(rs) {
					add(c)
				}
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

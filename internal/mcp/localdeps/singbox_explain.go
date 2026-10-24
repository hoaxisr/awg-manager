package localdeps

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// ListSingboxConnections reads sing-box's connections once. The host is
// the destination's name as sing-box learned it — the user's own traffic —
// and still goes through hostShaped, as every address here does.
func (l *Local) ListSingboxConnections(_ context.Context, q mcpsrv.SingboxConnectionsQuery) ([]mcpsrv.SingboxConnection, int, error) {
	if l.c.Clash == nil {
		return nil, 0, errUnavailable("sing-box")
	}
	conns, err := l.c.Clash.GetConnections()
	if err != nil {
		return nil, 0, fmt.Errorf("sing-box did not answer, so its connections are unknown (it may be stopped): %w", err)
	}
	var out []mcpsrv.SingboxConnection
	total := 0
	for _, c := range conns {
		m := c.Metadata
		if q.ClientIP != "" && m.SourceIP != q.ClientIP {
			continue
		}
		if q.Host != "" && !strings.Contains(strings.ToLower(m.Host), strings.ToLower(q.Host)) {
			continue
		}
		if q.Outbound != "" && !slices.Contains(c.Chains, q.Outbound) {
			continue
		}
		total++
		if len(out) >= q.Limit {
			continue
		}
		conn := mcpsrv.SingboxConnection{
			Network: token(m.Network), Src: hostShaped(m.SourceIP), SrcPort: token(string(m.SourcePort)),
			Host: hostShaped(m.Host), Dst: hostShaped(m.DestinationIP), DstPort: token(string(m.DestinationPort)),
			Rule: sanitizeText(ruleName(c.Rule, c.RulePayload), maxMessageRunes), Upload: c.Upload, Download: c.Download,
		}
		// The Clash API lists the chain from the outbound carrying the
		// connection back to the one the rule chose; reversed, it reads in
		// the order the traffic is handed down.
		for i := len(c.Chains) - 1; i >= 0; i-- {
			conn.Chain = append(conn.Chain, c.Chains[i])
		}
		if len(c.Chains) > 0 {
			conn.Outbound = c.Chains[0]
		}
		out = append(out, conn)
	}
	return out, total, nil
}

func ruleName(rule, payload string) string {
	if payload == "" {
		return rule
	}
	return rule + " " + payload
}

// tri is a three-valued match: a condition MCP cannot decide (a port, a
// sniffed protocol, a remote rule set) is unknown, not false — reporting it
// false would name a later rule as the one that wins.
type tri int

const (
	triNo tri = iota
	triYes
	triUnknown
)

// explainQuery is the destination being judged.
type explainQuery struct {
	domain string
	ips    []net.IP
	// ipsKnown: ips is the destination's address set as far as it goes —
	// the target is an address, or the lookup answered. A failed lookup
	// leaves IP conditions undecided, not unmatched. Only IPv4 is looked
	// up, so for a name an IPv6 CIDR stays undecided too.
	ipsKnown bool
	literal  bool
	clientIP net.IP
	sets     map[string]router.RuleSet
}

// anyOf is OR over three values: one yes is yes, otherwise one unknown is
// unknown.
func anyOf(vals ...tri) tri {
	out := triNo
	for _, v := range vals {
		if v == triYes {
			return triYes
		}
		if v == triUnknown {
			out = triUnknown
		}
	}
	return out
}

// allOf is AND over three values: one no is no, otherwise one unknown is
// unknown.
func allOf(vals ...tri) tri {
	out := triYes
	for _, v := range vals {
		if v == triNo {
			return triNo
		}
		if v == triUnknown {
			out = triUnknown
		}
	}
	return out
}

// matchDomainSuffix follows sing-box's domain matcher: a suffix with a
// leading dot matches subdomains only, one without matches the name itself
// and its subdomains.
func matchDomainSuffix(domain string, suffixes []string) tri {
	if domain == "" {
		return triNo
	}
	for _, s := range suffixes {
		s = strings.ToLower(s)
		if strings.HasPrefix(s, ".") {
			if strings.HasSuffix(domain, s) {
				return triYes
			}
			continue
		}
		if domain == s || strings.HasSuffix(domain, "."+s) {
			return triYes
		}
	}
	return triNo
}

func matchDomain(domain string, names []string) tri {
	for _, n := range names {
		if domain != "" && strings.EqualFold(domain, n) {
			return triYes
		}
	}
	return triNo
}

// matchCIDRs judges a destination CIDR list. It is "no" only when the
// addresses are known; see explainQuery.ipsKnown.
func (q explainQuery) matchCIDRs(cidrs []string) tri {
	got := matchCIDRs(q.ips, cidrs)
	if got == triYes {
		return got
	}
	if !q.ipsKnown {
		return triUnknown
	}
	if !q.literal {
		for _, c := range cidrs {
			if strings.Contains(c, ":") {
				return triUnknown
			}
		}
	}
	return got
}

func matchCIDRs(ips []net.IP, cidrs []string) tri {
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			if ip := net.ParseIP(c); ip != nil {
				n = &net.IPNet{IP: ip, Mask: net.CIDRMask(len(ip)*8, len(ip)*8)}
			} else {
				continue
			}
		}
		for _, ip := range ips {
			if n.Contains(ip) {
				return triYes
			}
		}
	}
	return triNo
}

// matchInlineRule judges one headless rule of an inline set over the
// fields sing-box rule sets carry for destinations; any other field makes
// it unknown.
func matchInlineRule(rule map[string]any, q explainQuery) tri {
	var dest []tri
	unknown := false
	for key, raw := range rule {
		values := stringsOf(raw)
		switch key {
		case "domain_suffix":
			dest = append(dest, matchDomainSuffix(q.domain, values))
		case "domain":
			dest = append(dest, matchDomain(q.domain, values))
		case "domain_keyword":
			hit := triNo
			for _, k := range values {
				if q.domain != "" && strings.Contains(q.domain, strings.ToLower(k)) {
					hit = triYes
				}
			}
			dest = append(dest, hit)
		case "domain_regex":
			hit := triNo
			for _, rx := range values {
				if re, err := regexp.Compile(rx); err == nil && q.domain != "" && re.MatchString(q.domain) {
					hit = triYes
				}
			}
			dest = append(dest, hit)
		case "ip_cidr":
			dest = append(dest, q.matchCIDRs(values))
		default:
			unknown = true
		}
	}
	// A headless rule combines like a default one: the destination fields
	// are alternatives, any other field must hold too.
	got := anyOf(dest...)
	if len(dest) == 0 {
		got = triYes
	}
	if unknown {
		return allOf(got, triUnknown)
	}
	return got
}

func stringsOf(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// matchRuleSet judges one rule set: an inline one by its rules (any of
// them), anything else as unknown — its content is downloaded by sing-box.
func matchRuleSet(tag string, q explainQuery) (tri, string) {
	rs, ok := q.sets[tag]
	if !ok || rs.Type != "inline" {
		return triUnknown, "rule set " + tag + " is downloaded by sing-box; its content is not checked here"
	}
	var vals []tri
	for _, r := range rs.Rules {
		vals = append(vals, matchInlineRule(r, q))
	}
	got := anyOf(vals...)
	if got == triUnknown {
		return got, "rule set " + tag + " has conditions not checked here"
	}
	return got, ""
}

// evalRule judges one routing rule the way sing-box combines a default
// rule (configuration/route/rule): destination fields are alternatives, the
// groups and every other field must all hold. sing-box folds a rule set
// into the destination alternatives only when the set holds exactly one
// destination-only rule (mergeableRuleIn, route/rule/rule_item_rule_set.go;
// see router.normalizeAddressOrRule and issue #699); any other set is a
// condition of its own, AND-ed with the rest. A set whose content is not
// read here (a downloaded one) may be either, so it decides nothing when
// the rule has destination fields of its own.
func evalRule(r router.Rule, q explainQuery) (tri, string) {
	if r.Type == "logical" {
		var vals []tri
		var why string
		for _, sub := range r.Rules {
			v, w := evalRule(sub, q)
			vals = append(vals, v)
			if v == triUnknown && why == "" {
				why = w
			}
		}
		if strings.EqualFold(r.Mode, "and") {
			return allOf(vals...), why
		}
		return anyOf(vals...), why
	}
	var groups []tri
	var why string
	note := func(v tri, w string) {
		if v == triUnknown && why == "" {
			why = w
		}
	}
	dest := []tri{}
	if len(r.DomainSuffix) > 0 {
		dest = append(dest, matchDomainSuffix(q.domain, r.DomainSuffix))
	}
	if len(r.Domain) > 0 {
		dest = append(dest, matchDomain(q.domain, r.Domain))
	}
	if len(r.IPCIDR) > 0 {
		v := q.matchCIDRs(r.IPCIDR)
		note(v, "the destination's addresses are not all known (lookup failed, or IPv6)")
		dest = append(dest, v)
	}
	if r.IPIsPrivate != nil && *r.IPIsPrivate {
		private := triNo
		if !q.ipsKnown {
			private = triUnknown
			note(private, "the destination's addresses are not known (the lookup failed)")
		}
		for _, ip := range q.ips {
			if router.IsPrivateAddr(ip) {
				private = triYes
			}
		}
		dest = append(dest, private)
	}
	// Whether the rule has destination alternatives at all — its own fields
	// or a set folded into them — decides an unread set, wherever it stands
	// in rule_set.
	hasDest := len(dest) > 0
	for _, tag := range r.RuleSet {
		if rs, known := q.sets[tag]; known && rs.Type == "inline" && mergeableSet(rs) {
			hasDest = true
		}
	}
	var setConds []tri
	for _, tag := range r.RuleSet {
		v, w := matchRuleSet(tag, q)
		note(v, w)
		rs, known := q.sets[tag]
		switch {
		case known && rs.Type == "inline" && mergeableSet(rs):
			dest = append(dest, v)
		case known && rs.Type == "inline":
			setConds = append(setConds, v)
		case hasDest:
			// Merged or AND-ed, unknown which: OR would give anyOf(dest, ?),
			// AND would give allOf(anyOf(dest), ?) — they never agree on a
			// definite answer, so the set leaves the rule undecided.
			dest = append(dest, triUnknown)
			setConds = append(setConds, triUnknown)
		default:
			setConds = append(setConds, v)
		}
	}
	if len(dest) > 0 {
		groups = append(groups, anyOf(dest...))
	}
	if len(setConds) > 0 {
		// Different rule sets keep OR semantics among themselves.
		groups = append(groups, anyOf(setConds...))
	}
	if len(r.SourceIPCIDR) > 0 {
		if q.clientIP == nil {
			groups = append(groups, triUnknown)
			note(triUnknown, "the rule depends on the device; pass clientIp")
		} else {
			groups = append(groups, matchCIDRs([]net.IP{q.clientIP}, r.SourceIPCIDR))
		}
	}
	for _, c := range []struct {
		set  bool
		what string
	}{
		{len(r.Port) > 0, "the destination port"},
		{r.Network != "", "the network (tcp or udp)"},
		{r.Protocol != "", "the sniffed protocol"},
		{len(r.Inbound) > 0, "the inbound"},
		{len(r.SourceMACAddress) > 0, "the device's MAC address"},
	} {
		if c.set {
			groups = append(groups, triUnknown)
			note(triUnknown, "the rule depends on "+c.what)
		}
	}
	if len(groups) == 0 {
		return triYes, ""
	}
	return allOf(groups...), why
}

// mergeableSet reports an inline set of exactly one rule holding only
// destination fields — the one shape sing-box folds into the rule.
func mergeableSet(rs router.RuleSet) bool {
	if len(rs.Rules) != 1 {
		return false
	}
	for key := range rs.Rules[0] {
		switch key {
		case "domain", "domain_suffix", "domain_keyword", "domain_regex", "ip_cidr":
		default:
			return false
		}
	}
	return true
}

// maxMaybeRules caps the undecided rules reported before the match.
const maxMaybeRules = 10

// ExplainSingboxRoute walks the rules in order. Only rules that end the
// walk for a connection count: route (the default action) and reject;
// sniff, DNS hijacking and route options do not decide where traffic goes.
func (l *Local) ExplainSingboxRoute(ctx context.Context, domain string, ips []string, clientIP string) (mcpsrv.SingboxRouteExplanation, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxRouteExplanation{}, errUnavailable("sing-box router")
	}
	settings, err := l.c.Router.GetSettings(ctx)
	if err != nil {
		return mcpsrv.SingboxRouteExplanation{}, err
	}
	rr, err := l.routerReads(ctx)
	if err != nil {
		return mcpsrv.SingboxRouteExplanation{}, err
	}
	rules, err := rr.rules(ctx)
	if err != nil {
		return mcpsrv.SingboxRouteExplanation{}, err
	}
	sets, err := rr.ruleSets(ctx)
	if err != nil {
		return mcpsrv.SingboxRouteExplanation{}, err
	}
	q := explainQuery{domain: strings.ToLower(strings.TrimSuffix(domain, ".")), sets: map[string]router.RuleSet{}}
	for _, s := range ips {
		if ip := net.ParseIP(s); ip != nil {
			q.ips = append(q.ips, ip)
		}
	}
	// explain_route passes no domain for an address target; for a name,
	// no addresses means the lookup failed.
	q.literal = q.domain == ""
	q.ipsKnown = len(q.ips) > 0
	if clientIP != "" {
		q.clientIP = net.ParseIP(clientIP)
	}
	for _, rs := range sets {
		q.sets[rs.Tag] = rs
	}
	out := mcpsrv.SingboxRouteExplanation{RoutingMode: token(settings.RoutingMode), HasDraft: l.hasDraft(ctx, rr.fakeIP)}
	for i, r := range rules {
		if r.Action != "" && r.Action != "route" && r.Action != "reject" {
			continue
		}
		v, why := evalRule(r, q)
		if v == triNo {
			continue
		}
		m := singboxRule(i, r)
		entry := mcpsrv.SingboxRuleMatch{Index: i, Match: m.Match, Action: m.Action, Outbound: r.Outbound}
		if v == triYes {
			out.Match = &entry
			break
		}
		if len(out.MaybeEarlier) < maxMaybeRules {
			entry.Reason = why
			out.MaybeEarlier = append(out.MaybeEarlier, entry)
		}
	}
	return out, nil
}

package router

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// InspectDNSInput is the user-supplied query for the DNS-resolution branch
// of the inspector. It mirrors InspectInput but over dns.rules instead of
// route.rules.
//
//   - Domain is the domain being resolved (an IP literal makes little sense
//     for a DNS query, but we tolerate it — domain matchers simply won't
//     fire and the query falls to the DNS final server).
//   - QueryType is the DNS query type ("A"/"AAAA"/…); empty means "not
//     specified" and matches any rule's query_type matcher permissively
//     (sing-box treats an absent query type as "could be anything").
//   - SourceIP is the optional client IP that feeds source_ip_cidr matching
//     (per-device DNS routing). Empty means "no source given" — source_ip_cidr
//     matchers are then recorded as skipped, not matched.
type InspectDNSInput struct {
	Domain    string
	QueryType string
	SourceIP  string
}

// DNSRuleMatchResult mirrors RuleMatchResult but targets a DNS server tag
// (Server) instead of a route outbound. Conditions describes what we
// evaluated for the human reader; Reason explains the decision.
type DNSRuleMatchResult struct {
	Index      int      `json:"index"`
	Matched    bool     `json:"matched"`
	Server     string   `json:"server,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// InspectDNSResult is the public response of the DNS-branch inspector.
//   - Matches[i].Index == i, in dns-rule order.
//   - MatchedRule is the index of the first matching DNS rule; -1 when no
//     rule matched and the query fell through to the DNS final server.
//   - Server is the resolved DNS-server tag (matched rule's server, or the
//     final server).
//   - Classification ∈ {"fakeip", "real", "local"} — how the resolved
//     server answers: fakeip → synthetic IP from a pool (→ tunnel); local →
//     the router resolves locally; real → an upstream resolver returns the
//     real IP.
//   - Pool is the fakeip server's inet4_range (+ inet6_range) when
//     Classification == "fakeip"; empty otherwise.
//   - Note carries free-form caveats (e.g. unsupported rule_set features).
type InspectDNSResult struct {
	Input          string               `json:"input"`
	InputType      string               `json:"inputType"`
	Matches        []DNSRuleMatchResult `json:"matches"`
	MatchedRule    int                  `json:"matchedRule"`
	Server         string               `json:"server"`
	Classification string               `json:"classification"`
	Pool           string               `json:"pool,omitempty"`
	Final          string               `json:"final"`
	Note           string               `json:"note,omitempty"`
}

// InspectDNS walks dnsRules in priority order, evaluates each rule's matchers
// against the input, and returns a result describing both the per-rule
// decisions and the resolved DNS server + how it classifies the resolution.
//
// Matcher semantics (AND across present matchers, mirroring sing-box and the
// route inspector's evaluateRule):
//   - domain_suffix / domain / domain_keyword / domain_regex: the input must
//     be a domain; matches per the respective matcher family.
//   - rule_set: a rule's `rule_set: [a, b]` is OR — delegated to
//     `sing-box rule-set match` via matchRuleSet, exactly as in the route
//     inspector. Unevaluatable rule_sets degrade to no-match + a Note.
//   - query_type: matches if input.QueryType is in the rule's list, or when
//     input.QueryType is empty (not specified → permissive, like port==0 in
//     route except here we DO match because an unspecified query type can be
//     anything and DNS rules without query_type apply to all types anyway).
//   - source_ip_cidr: matches if input.SourceIP is inside any listed CIDR.
//     Empty input.SourceIP records the matcher as skipped (no source given)
//     and counts as no-match, mirroring the route inspector's port handling.
//
// First full match wins → that rule's Server. No match → dnsFinal. The
// resolved server is then looked up by tag to classify + extract the pool.
//
// singboxBinary may be empty and cache may be nil — matchRuleSet degrades
// gracefully (see the route inspector docs).
func InspectDNS(input InspectDNSInput, dnsRules []DNSRule, dnsServers []DNSServer, ruleSets []RuleSet, dnsFinal string, singboxBinary string, cache *ruleSetCache) InspectDNSResult {
	res := InspectDNSResult{
		Input:       input.Domain,
		Matches:     []DNSRuleMatchResult{},
		MatchedRule: -1,
		Final:       dnsFinal,
	}

	// Classify input — IP literal vs domain. Mirrors the route inspector.
	parsedIP := net.ParseIP(input.Domain)
	if parsedIP != nil {
		res.InputType = "ip"
	} else {
		res.InputType = "domain"
	}

	env := &inspectEnv{
		ruleSetByTag:  make(map[string]RuleSet, len(ruleSets)),
		singboxBinary: singboxBinary,
		cache:         cache,
	}
	for _, rs := range ruleSets {
		env.ruleSetByTag[rs.Tag] = rs
	}

	for i, rule := range dnsRules {
		match := evaluateDNSRule(input, parsedIP, rule, env)
		match.Index = i
		res.Matches = append(res.Matches, match)
		if !match.Matched {
			continue
		}
		if res.MatchedRule == -1 {
			res.MatchedRule = i
			res.Server = rule.Server
		}
	}

	// No rule matched → fall to the DNS final server.
	if res.MatchedRule == -1 {
		res.Server = dnsFinal
	}

	// Classify the resolved server + extract the fakeip pool.
	classifyDNSServer(&res, res.Server, dnsServers)

	if len(env.unsupported) > 0 {
		seen := make(map[string]struct{}, len(env.unsupported))
		uniq := make([]string, 0, len(env.unsupported))
		for _, s := range env.unsupported {
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			uniq = append(uniq, s)
		}
		res.Note = "Не удалось проверить rule_set: " + strings.Join(uniq, "; ")
	}

	return res
}

// classifyDNSServer looks up serverTag among dnsServers and fills
// res.Classification (+ res.Pool for fakeip). An unknown/empty tag is
// classified "real" (an external resolver we cannot inspect) — the safest
// default, since only an explicit type=='fakeip'|'local' changes routing
// behaviour the user needs to know about.
func classifyDNSServer(res *InspectDNSResult, serverTag string, dnsServers []DNSServer) {
	for _, srv := range dnsServers {
		if srv.Tag != serverTag {
			continue
		}
		switch srv.Type {
		case "fakeip":
			res.Classification = "fakeip"
			pools := make([]string, 0, 2)
			if srv.Inet4Range != "" {
				pools = append(pools, srv.Inet4Range)
			}
			if srv.Inet6Range != "" {
				pools = append(pools, srv.Inet6Range)
			}
			res.Pool = strings.Join(pools, ", ")
		case "local":
			res.Classification = "local"
		default:
			res.Classification = "real"
		}
		return
	}
	// Tag not found among declared servers — treat as a real upstream we
	// cannot inspect further.
	res.Classification = "real"
}

// evaluateDNSRule returns the per-rule decision for a DNS rule. Empty rule
// (no matchers) is treated as no-match — same defensive stance as the route
// inspector's evaluateRule.
func evaluateDNSRule(input InspectDNSInput, parsedIP net.IP, rule DNSRule, env *inspectEnv) DNSRuleMatchResult {
	out := DNSRuleMatchResult{
		Server: rule.Server,
	}

	type partial struct{ present, hit bool }
	var (
		domainPart    partial
		ruleSetPart   partial
		queryTypePart partial
		sourcePart    partial
	)

	// rule_set — OR semantics, first hit wins. Mirrors evaluateRule.
	if len(rule.RuleSet) > 0 {
		ruleSetPart.present = true
		probeInput := input.Domain
		for _, tag := range rule.RuleSet {
			rs, known := env.ruleSetByTag[tag]
			if !known {
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → не определён", tag))
				if env != nil {
					env.unsupported = append(env.unsupported, fmt.Sprintf("%s (не определён в rule_set[])", tag))
				}
				continue
			}
			matched, supported, mErr := matchRuleSet(probeInput, rs, env.singboxBinary, env.cache, nil)
			switch {
			case !supported:
				reason := "не удалось проверить (нет sing-box или файла)"
				if mErr != nil {
					reason = fmt.Sprintf("ошибка: %v", mErr)
				}
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → %s", tag, reason))
				if env != nil {
					env.unsupported = append(env.unsupported, fmt.Sprintf("%s (%s)", tag, reason))
				}
			case matched:
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → совпало", tag))
				ruleSetPart.hit = true
			default:
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → не совпало", tag))
			}
			if ruleSetPart.hit {
				break
			}
		}
	}

	// Domain matchers (domain_suffix / domain / domain_keyword / domain_regex).
	// They share one logical "domain" matcher slot: any one family hitting
	// makes the domain matcher TRUE (sing-box ORs these), but the slot is
	// only "present" when at least one family is configured.
	lower := ""
	if parsedIP == nil {
		lower = strings.ToLower(input.Domain)
	}
	if len(rule.DomainSuffix) > 0 {
		domainPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("domain_suffix: [%s]", strings.Join(rule.DomainSuffix, ", ")))
		if parsedIP == nil {
			for _, suffix := range rule.DomainSuffix {
				if matchesDomainSuffix(lower, suffix) {
					domainPart.hit = true
					break
				}
			}
		}
	}
	if len(rule.Domain) > 0 {
		domainPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("domain: [%s]", strings.Join(rule.Domain, ", ")))
		if parsedIP == nil && !domainPart.hit {
			for _, d := range rule.Domain {
				if strings.EqualFold(lower, strings.TrimPrefix(d, ".")) {
					domainPart.hit = true
					break
				}
			}
		}
	}
	if len(rule.DomainKeyword) > 0 {
		domainPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("domain_keyword: [%s]", strings.Join(rule.DomainKeyword, ", ")))
		if parsedIP == nil && !domainPart.hit {
			for _, kw := range rule.DomainKeyword {
				if kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
					domainPart.hit = true
					break
				}
			}
		}
	}
	if len(rule.DomainRegex) > 0 {
		domainPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("domain_regex: [%s]", strings.Join(rule.DomainRegex, ", ")))
		if parsedIP == nil && !domainPart.hit {
			for _, re := range rule.DomainRegex {
				if matchesDomainRegex(lower, re) {
					domainPart.hit = true
					break
				}
			}
		}
	}

	// query_type — matches if the input type is listed, OR when the input
	// query type is unspecified (empty input matches any rule, since DNS
	// rules without an explicit type apply to all queries and an unknown
	// probe type cannot be excluded).
	if len(rule.QueryType) > 0 {
		queryTypePart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("query_type: [%s]", strings.Join(rule.QueryType, ", ")))
		if input.QueryType == "" {
			// Unspecified — permissive match.
			queryTypePart.hit = true
		} else {
			for _, qt := range rule.QueryType {
				if strings.EqualFold(qt, input.QueryType) {
					queryTypePart.hit = true
					break
				}
			}
		}
	}

	// source_ip_cidr — matches if the source IP is inside any listed CIDR.
	// Empty source records the matcher as skipped (counts as no-match),
	// mirroring how the route inspector treats an unverifiable port.
	if len(rule.SourceIPCIDR) > 0 {
		sourcePart.present = true
		if input.SourceIP == "" {
			out.Conditions = append(out.Conditions, fmt.Sprintf("source_ip_cidr: [%s] (пропущено — источник не задан)", strings.Join(rule.SourceIPCIDR, ", ")))
		} else {
			out.Conditions = append(out.Conditions, fmt.Sprintf("source_ip_cidr: [%s]", strings.Join(rule.SourceIPCIDR, ", ")))
			if srcIP := net.ParseIP(input.SourceIP); srcIP != nil {
				for _, c := range rule.SourceIPCIDR {
					if cidrContains(c, srcIP) {
						sourcePart.hit = true
						break
					}
				}
			}
		}
	}

	anyPresent := domainPart.present || ruleSetPart.present || queryTypePart.present || sourcePart.present
	if !anyPresent {
		out.Reason = "пустое правило — пропущено"
		return out
	}

	matched := true
	if domainPart.present && !domainPart.hit {
		matched = false
	}
	if ruleSetPart.present && !ruleSetPart.hit {
		matched = false
	}
	if queryTypePart.present && !queryTypePart.hit {
		matched = false
	}
	if sourcePart.present && !sourcePart.hit {
		matched = false
	}

	out.Matched = matched
	if matched {
		var hits []string
		if domainPart.hit {
			hits = append(hits, "домен")
		}
		if ruleSetPart.hit {
			hits = append(hits, "rule_set")
		}
		if queryTypePart.hit {
			hits = append(hits, "query_type")
		}
		if sourcePart.hit {
			hits = append(hits, "source_ip_cidr")
		}
		out.Reason = "совпало по: " + strings.Join(hits, ", ")
	} else {
		out.Reason = "нет совпадения"
	}
	return out
}

// matchesDomainRegex reports whether domain matches the regex re. A regex
// that fails to compile is treated as no-match (defensive — a malformed
// config matcher should not crash the inspector). domain is expected to be
// lowercase already.
func matchesDomainRegex(domain, re string) bool {
	if re == "" {
		return false
	}
	compiled, err := regexp.Compile(re)
	if err != nil {
		return false
	}
	return compiled.MatchString(domain)
}

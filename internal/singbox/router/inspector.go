package router

import (
	"fmt"
	"net"
	"strings"
)

// InspectInput is the user-supplied query for the route inspector.
// Domain is either a domain or an IP literal — Inspect classifies which.
// Port == 0 means "no port match" (Port matchers are skipped, recorded
// as not-matched without claiming a hit). Protocol is "tcp"/"udp"; empty
// means "skip protocol matchers".
type InspectInput struct {
	Domain   string
	Port     int
	Protocol string
}

// RuleMatchResult captures the outcome of evaluating one rule against
// the input. Conditions describes what we actually evaluated for the
// human reader; Reason explains the decision.
type RuleMatchResult struct {
	Index      int      `json:"index"`
	Matched    bool     `json:"matched"`
	Action     string   `json:"action"`
	Outbound   string   `json:"outbound,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	// notEvaluated marks a rule (or a logical branch) that carries nothing
	// this inspector can judge — source_ip_cidr only, say. A flat rule like
	// that has always been reported as no-match, but as a branch of a
	// logical(and) it must not veto the siblings: before normalization the
	// same condition sat next to the others and was simply ignored.
	notEvaluated bool
}

// InspectResult is the public response of the inspector.
//   - Matches[i].Index == i, in route-rule order.
//   - Destination is the resolved final outbound (or "REJECT", or the
//     route.final value when no rule matches).
//   - MatchedRule is the index of the first *terminal* match (route or
//     reject); -1 when no rule produced a final destination.
//   - Note carries free-form caveats (e.g. unsupported features).
type InspectResult struct {
	Input       string            `json:"input"`
	InputType   string            `json:"inputType"`
	Matches     []RuleMatchResult `json:"matches"`
	Destination string            `json:"destination"`
	MatchedRule int               `json:"matchedRule"`
	Final       string            `json:"final"`
	Note        string            `json:"note,omitempty"`
}

type InspectProgress struct {
	Phase        string `json:"phase"`
	Message      string `json:"message"`
	RuleIndex    *int   `json:"ruleIndex,omitempty"`
	RuleTotal    *int   `json:"ruleTotal,omitempty"`
	RuleSetTag   string `json:"ruleSetTag,omitempty"`
	RuleSetIndex *int   `json:"ruleSetIndex,omitempty"`
	RuleSetTotal *int   `json:"ruleSetTotal,omitempty"`
	Final        string `json:"final,omitempty"`
	UsingDraft   bool   `json:"usingDraft,omitempty"`
}

type InspectProgressFunc func(InspectProgress)

func intPtr(v int) *int { return &v }

// inspectEnv bundles the dependencies the rule_set matcher needs at
// evaluation time. Kept as an internal struct so Inspect's public
// signature stays narrow — callers thread these via Inspect's params.
type inspectEnv struct {
	ruleSetByTag  map[string]RuleSet
	singboxBinary string
	cache         *ruleSetCache
	// unsupported accumulates human-readable notes for rule_sets we
	// could not evaluate (binary missing, file missing, etc.). The
	// resulting strings are joined into InspectResult.Note so the user
	// understands why some rule_set matchers are reported as no-match.
	unsupported []string
}

// Inspect walks rules in priority order, evaluates each rule's matchers
// against the input, and returns a result describing both the per-rule
// decisions and the final destination outbound.
//
// Matcher semantics mirror sing-box: matchers are grouped, groups are
// ANDed, members of a group are ORed (route/rule/rule_abstract.go).
//   - destination address group — Domain, DomainSuffix, IPCIDR, and a
//     RuleSet standing next to any of them. Domain matches the input
//     exactly, DomainSuffix matches it as a tail (both case-insensitive,
//     domain input only); IPCIDR matches an IP input, bare IPs counting
//     as /32 or /128. ANY member hitting satisfies the group — a rule
//     listing both own domains and own subnets matches by either.
//   - RuleSet as the rule's ONLY address matcher stays a condition of its
//     own. `rule_set: [a, b]` is OR — any one of the listed sets matching
//     makes it TRUE. The match itself is delegated to `sing-box rule-set
//     match` shelled out via singboxBinary; when the binary is missing or
//     the rule-set file cannot be obtained the matcher degrades to
//     no-match and a note is appended so the user is not silently misled.
//   - Port: its own group. Matches if input.Port is in the list. When
//     input.Port==0 we skip the matcher and record it as not evaluated —
//     that is a "no input given" signal, not a match.
//   - Network: the L4 matcher, compared against input.Protocol — the probe
//     input named "protocol" carries tcp/udp and nothing else.
//   - Protocol / Inbound: recorded but never counted as a hit. The sniffed
//     application protocol and the listener tag cannot be supplied by a
//     manual probe, so a rule requiring them stays a no-match (the same
//     conservative line the port matcher takes without an input port).
//   - SourceIPCIDR, SourceMACAddress: skipped (irrelevant for this
//     inspector — there is no "source IP"/"source MAC" in a manual probe).
//   - logical rules (`type:"logical"`) recurse: every branch is evaluated
//     and the results combined by Mode ("and" / "or").
//
// First terminal match (action == "route" with non-empty Outbound, or
// action == "reject") wins. Non-terminal actions ("sniff", "hijack-dns")
// continue the walk. If nothing matches, Destination falls back to
// route.final (or "direct" when final is empty).
//
// singboxBinary may be empty (dev machine without sing-box installed) —
// matchRuleSet treats that as "unsupported" and the matcher reports
// no-match with an explanatory reason. cache may be nil; in that case
// remote rule_sets are skipped as unsupported but local ones still work.
func Inspect(input InspectInput, rules []Rule, ruleSets []RuleSet, final string, singboxBinary string, cache *ruleSetCache) InspectResult {
	return InspectWithProgress(input, rules, ruleSets, final, singboxBinary, cache, nil)
}

func InspectWithProgress(input InspectInput, rules []Rule, ruleSets []RuleSet, final string, singboxBinary string, cache *ruleSetCache, emit InspectProgressFunc) InspectResult {
	res := InspectResult{
		Input:       input.Domain,
		Matches:     []RuleMatchResult{},
		MatchedRule: -1,
		Final:       final,
	}

	// Classify input — IP literal vs domain.
	parsedIP := net.ParseIP(input.Domain)
	if parsedIP != nil {
		res.InputType = "ip"
		if emit != nil {
			emit(InspectProgress{Phase: "classify_input", Message: "Ввод распознан как IP"})
		}
	} else {
		res.InputType = "domain"
		if emit != nil {
			emit(InspectProgress{Phase: "classify_input", Message: "Ввод распознан как домен"})
		}
	}

	env := &inspectEnv{
		ruleSetByTag:  make(map[string]RuleSet, len(ruleSets)),
		singboxBinary: singboxBinary,
		cache:         cache,
	}
	for _, rs := range ruleSets {
		env.ruleSetByTag[rs.Tag] = rs
	}

	if emit != nil {
		emit(InspectProgress{Phase: "rule_walk_started", Message: fmt.Sprintf("Начинаем проверку %d правил маршрутизации", len(rules)), RuleTotal: intPtr(len(rules))})
	}
	for i, rule := range rules {
		if emit != nil {
			emit(InspectProgress{Phase: "rule_start", Message: fmt.Sprintf("Проверяем правило #%d из %d", i, len(rules)), RuleIndex: intPtr(i), RuleTotal: intPtr(len(rules))})
		}
		match := evaluateRule(input, parsedIP, rule, env, emit, i, len(rules))
		match.Index = i
		res.Matches = append(res.Matches, match)
		if emit != nil {
			phase := "rule_done"
			msg := fmt.Sprintf("Правило #%d не совпало", i)
			if match.Matched {
				msg = fmt.Sprintf("Правило #%d совпало", i)
			}
			emit(InspectProgress{Phase: phase, Message: msg, RuleIndex: intPtr(i), RuleTotal: intPtr(len(rules))})
		}

		if !match.Matched {
			continue
		}

		// Terminal vs non-terminal action handling.
		switch rule.Action {
		case "route":
			if res.MatchedRule == -1 {
				res.MatchedRule = i
				if rule.Outbound != "" {
					res.Destination = rule.Outbound
				} else {
					res.Destination = "DIRECT"
				}
				if emit != nil {
					emit(InspectProgress{Phase: "terminal_match", Message: fmt.Sprintf("Найдено финальное правило #%d → route", i), RuleIndex: intPtr(i), RuleTotal: intPtr(len(rules))})
				}
			}
		case "reject":
			if res.MatchedRule == -1 {
				res.MatchedRule = i
				res.Destination = "REJECT"
				if emit != nil {
					emit(InspectProgress{Phase: "terminal_match", Message: fmt.Sprintf("Найдено финальное правило #%d → reject", i), RuleIndex: intPtr(i), RuleTotal: intPtr(len(rules))})
				}
			}
		case "sniff", "hijack-dns", "route-options", "resolve":
			if emit != nil {
				emit(InspectProgress{Phase: "non_terminal_match", Message: fmt.Sprintf("Нефинальное совпадение в правиле #%d", i), RuleIndex: intPtr(i), RuleTotal: intPtr(len(rules))})
			}
			// Non-terminal: matched but does not set Destination; walk
			// continues so a later rule (or final) can claim it. The system
			// UDP-timeout rule (`route-options` + network:udp) sits in every
			// router's prefix and matches every UDP probe — treating it as
			// terminal made the inspector answer "DIRECT" for all of them and
			// hid every user rule behind it.
		default:
			// Unknown action — be conservative, treat as terminal route
			// on the rule's outbound to surface it in the UI.
			if res.MatchedRule == -1 {
				res.MatchedRule = i
				if rule.Outbound != "" {
					res.Destination = rule.Outbound
				} else {
					res.Destination = "DIRECT"
				}
			}
		}
	}

	if res.Destination == "" {
		if final != "" {
			res.Destination = final
		} else {
			res.Destination = "direct"
			res.Final = "direct"
		}
	}

	if len(env.unsupported) > 0 {
		// Dedupe — the same rule_set may appear in many rules.
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
	if emit != nil {
		emit(InspectProgress{Phase: "done", Message: "Инспектор завершил проверку"})
	}

	return res
}

// evaluateRule returns the per-rule decision. Empty rule (no matchers)
// is defensively treated as no-match — it would otherwise sweep every
// query into a "match" bucket and confuse the UI.
func evaluateRule(input InspectInput, parsedIP net.IP, rule Rule, env *inspectEnv, emit InspectProgressFunc, ruleIndex, ruleTotal int) RuleMatchResult {
	if rule.Type == "logical" {
		return evaluateLogicalRule(input, parsedIP, rule, env, emit, ruleIndex, ruleTotal)
	}
	return evaluateDefaultRule(input, parsedIP, rule, env, emit, ruleIndex, ruleTotal)
}

// evaluateLogicalRule mirrors sing-box's abstractLogicalRule.Match: every
// nested rule is evaluated against a private copy of the request and the
// results are combined by Mode. Unlike sing-box we do NOT short-circuit —
// the inspector's job is to explain every branch, not to be fast.
func evaluateLogicalRule(input InspectInput, parsedIP net.IP, rule Rule, env *inspectEnv, emit InspectProgressFunc, ruleIndex, ruleTotal int) RuleMatchResult {
	out := RuleMatchResult{Action: rule.Action, Outbound: rule.Outbound}
	if rule.Mode != "and" && rule.Mode != "or" {
		out.Reason = fmt.Sprintf("логическое правило с непонятным mode %q — пропущено", rule.Mode)
		return out
	}
	if len(rule.Rules) == 0 {
		out.Reason = "логическое правило без веток — пропущено"
		return out
	}
	matched := rule.Mode == "and"
	judged := false
	var hits []string
	for i, nested := range rule.Rules {
		sub := evaluateRule(input, parsedIP, nested, env, emit, ruleIndex, ruleTotal)
		for _, c := range sub.Conditions {
			out.Conditions = append(out.Conditions, fmt.Sprintf("ветка %d: %s", i+1, c))
		}
		if sub.notEvaluated {
			// Ветка целиком из непроверяемых условий — не голосует.
			// В mode=and иначе она обнулила бы всё правило, хотя те же
			// условия в плоской форме просто игнорировались.
			continue
		}
		judged = true
		if sub.Matched {
			hits = append(hits, fmt.Sprintf("%d", i+1))
		}
		if rule.Mode == "and" {
			matched = matched && sub.Matched
		} else {
			matched = matched || sub.Matched
		}
	}
	if !judged {
		out.notEvaluated = true
		out.Reason = "нечего проверять — пропущено"
		return out
	}
	out.Matched = matched
	switch {
	case !matched:
		out.Reason = "нет совпадения"
	case rule.Mode == "and":
		out.Reason = "совпали все ветки"
	default:
		out.Reason = "совпало по ветке: " + strings.Join(hits, ", ")
	}
	return out
}

func evaluateDefaultRule(input InspectInput, parsedIP net.IP, rule Rule, env *inspectEnv, emit InspectProgressFunc, ruleIndex, ruleTotal int) RuleMatchResult {
	out := RuleMatchResult{
		Action:   rule.Action,
		Outbound: rule.Outbound,
	}

	// SourceIPCIDR is a context we don't have for a manual probe.
	// Record it as N/A but neither match nor block.
	if len(rule.SourceIPCIDR) > 0 {
		out.Conditions = append(out.Conditions, fmt.Sprintf("source_ip_cidr: %s (пропущено — нет источника)", strings.Join(rule.SourceIPCIDR, ", ")))
	}
	if len(rule.SourceMACAddress) > 0 {
		out.Conditions = append(out.Conditions, fmt.Sprintf("source_mac_address: %s (пропущено — нет источника)", strings.Join(rule.SourceMACAddress, ", ")))
	}

	// Track each matcher's outcome. Groups are ANDed between themselves;
	// inside the destination-address group (domain / domain_suffix /
	// ip_cidr) sing-box ORs the members — see evaluateGroups in the fork's
	// route/rule/rule_abstract.go. A flat AND here would report a working
	// rule as dead (issue #699).
	//
	// Matchers a manual probe cannot supply (inbound, protocol) are recorded
	// as present-but-unverified, which keeps the rule a no-match: the same
	// conservative line the port matcher already takes when no port is given.
	// Claiming a hit instead would sweep every query into, say, a managed
	// QoS-DSCP rule that only matches its own listener.
	type partial struct{ present, hit bool }
	var (
		domainPart   partial
		ipPart       partial
		privatePart  partial
		portPart     partial
		networkPart  partial
		protocolPart partial
		inboundPart  partial
		ruleSetPart  partial
	)

	if len(rule.Inbound) > 0 {
		inboundPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("inbound: %s (не проверяется — вход недоступен при ручной проверке)", strings.Join(rule.Inbound, ", ")))
	}

	// rule_set: a rule's `rule_set: [a, b]` is OR — any one matching
	// makes the matcher TRUE. We probe each tag in turn and stop on the
	// first hit. Unevaluatable tags (binary missing, file missing) are
	// recorded as unsupported and counted as no-match for that tag, but
	// do not prevent other tags in the same rule from matching.
	if len(rule.RuleSet) > 0 {
		ruleSetPart.present = true
		probeInput := input.Domain
		for rsIdx, tag := range rule.RuleSet {
			if emit != nil {
				emit(InspectProgress{
					Phase:        "rule_set_start",
					Message:      fmt.Sprintf("Проверяем rule_set %s", tag),
					RuleIndex:    intPtr(ruleIndex),
					RuleTotal:    intPtr(ruleTotal),
					RuleSetTag:   tag,
					RuleSetIndex: intPtr(rsIdx),
					RuleSetTotal: intPtr(len(rule.RuleSet)),
				})
			}
			rs, known := env.ruleSetByTag[tag]
			if !known {
				if emit != nil {
					emit(InspectProgress{Phase: "rule_set_undefined", Message: fmt.Sprintf("rule_set %s не определён", tag), RuleSetTag: tag})
				}
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → не определён", tag))
				if env != nil {
					env.unsupported = append(env.unsupported, fmt.Sprintf("%s (не определён в rule_set[])", tag))
				}
				continue
			}
			matched, supported, mErr := matchRuleSet(probeInput, rs, env.singboxBinary, env.cache, emit)
			switch {
			case !supported:
				if emit != nil {
					emit(InspectProgress{Phase: "rule_set_match_error", Message: fmt.Sprintf("rule_set %s не удалось проверить", tag), RuleSetTag: tag})
				}
				reason := "не удалось проверить (нет sing-box или файла)"
				if mErr != nil {
					reason = fmt.Sprintf("ошибка: %v", mErr)
				}
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → %s", tag, reason))
				if env != nil {
					env.unsupported = append(env.unsupported, fmt.Sprintf("%s (%s)", tag, reason))
				}
			case matched:
				if emit != nil {
					emit(InspectProgress{Phase: "rule_set_match_done", Message: fmt.Sprintf("rule_set %s совпал", tag), RuleSetTag: tag})
				}
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → совпало", tag))
				ruleSetPart.hit = true
			default:
				if emit != nil {
					emit(InspectProgress{Phase: "rule_set_match_done", Message: fmt.Sprintf("rule_set %s не совпал", tag), RuleSetTag: tag})
				}
				out.Conditions = append(out.Conditions, fmt.Sprintf("rule_set %q → не совпало", tag))
			}
			if ruleSetPart.hit {
				// Short-circuit: OR semantics — first hit wins. Remaining
				// tags are neither evaluated nor reported (mirrors how
				// sing-box itself bails out at runtime).
				break
			}
		}
	}

	// Domain (exact) and DomainSuffix — one matcher in sing-box
	// (NewDomainItem takes both lists), so one entry here too.
	if len(rule.Domain) > 0 || len(rule.DomainSuffix) > 0 {
		domainPart.present = true
		if len(rule.Domain) > 0 {
			out.Conditions = append(out.Conditions, fmt.Sprintf("domain: [%s]", strings.Join(rule.Domain, ", ")))
		}
		if len(rule.DomainSuffix) > 0 {
			out.Conditions = append(out.Conditions, fmt.Sprintf("domain_suffix: [%s]", strings.Join(rule.DomainSuffix, ", ")))
		}
		if parsedIP == nil {
			lower := strings.ToLower(input.Domain)
			for _, d := range rule.Domain {
				if lower == strings.ToLower(strings.TrimSpace(d)) {
					domainPart.hit = true
					break
				}
			}
			for _, suffix := range rule.DomainSuffix {
				if domainPart.hit {
					break
				}
				if matchesDomainSuffix(lower, suffix) {
					domainPart.hit = true
				}
			}
		}
	}

	// IPCIDR
	if len(rule.IPCIDR) > 0 {
		ipPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("ip_cidr: [%s]", strings.Join(rule.IPCIDR, ", ")))
		if parsedIP != nil {
			for _, c := range rule.IPCIDR {
				if cidrContains(c, parsedIP) {
					ipPart.hit = true
					break
				}
			}
		}
	}

	// IPIsPrivate belongs to the destination-address group too: sing-box puts
	// its item in destinationIPCIDRItems (fork rule_default.go:154), so it is
	// OR-ed with ip_cidr and the domain matchers, not AND-ed against them.
	if rule.IPIsPrivate != nil && *rule.IPIsPrivate {
		privatePart.present = true
		out.Conditions = append(out.Conditions, "ip_is_private")
		privatePart.hit = parsedIP != nil && !isPublicAddr(parsedIP)
	}

	// Port — if no input port given, mark present-but-not-evaluated
	// so AND logic does not declare a match without verification.
	if len(rule.Port) > 0 {
		portPart.present = true
		ports := make([]string, 0, len(rule.Port))
		for _, p := range rule.Port {
			ports = append(ports, fmt.Sprintf("%d", p))
		}
		if input.Port == 0 {
			out.Conditions = append(out.Conditions, fmt.Sprintf("port: [%s] (пропущено — порт не задан)", strings.Join(ports, ", ")))
		} else {
			out.Conditions = append(out.Conditions, fmt.Sprintf("port: [%s]", strings.Join(ports, ", ")))
			for _, p := range rule.Port {
				if p == input.Port {
					portPart.hit = true
					break
				}
			}
		}
	}

	// Network — the L4 matcher, and the one the probe's "protocol" input
	// actually carries: the API accepts only tcp/udp there
	// (validateInspectParams). Skipping it let a udp-only rule report a
	// match for a TCP probe.
	if rule.Network != "" {
		networkPart.present = true
		if input.Protocol == "" {
			out.Conditions = append(out.Conditions, fmt.Sprintf("network: %s (пропущено — протокол не задан)", rule.Network))
		} else {
			out.Conditions = append(out.Conditions, fmt.Sprintf("network: %s", rule.Network))
			networkPart.hit = strings.EqualFold(rule.Network, input.Protocol)
		}
	}

	// Protocol is the SNIFFED application protocol (tls / http / quic / dns
	// / …), not L4 — sing-box fills it from the sniffer, and a manual probe
	// has no sniffer. Comparing it against the tcp/udp input (as this did)
	// both missed real app-protocol rules and claimed a match for the
	// nonsensical `protocol: "tcp"`, which sing-box itself never matches.
	if rule.Protocol != "" {
		protocolPart.present = true
		out.Conditions = append(out.Conditions, fmt.Sprintf("protocol: %s (не проверяется — прикладной протокол определяет сниффер)", rule.Protocol))
	}

	// Destination-address group: domain*, ip_cidr and ip_is_private are OR-ed.
	// A rule_set standing next to the rule's OWN address matchers joins that
	// group: normalizeAddressOrRule stores such a rule as logical(or), so this
	// is what the engine runs.
	//
	// A rule still flat here escaped normalization (hand-written slot, or a
	// field our struct does not model, which the migration skips on purpose).
	// We read it as OR anyway — that is what it becomes once normalized, and
	// what the engine already does whenever the referenced set is mergeable,
	// which holds for all but four of the sets we ship. With a non-mergeable
	// set the engine ANDs it instead, and the inspector is optimistic there.
	//
	// A rule_set that is the rule's ONLY address matcher stays an
	// independent condition.
	addr := partial{
		present: domainPart.present || ipPart.present || privatePart.present,
		hit:     domainPart.hit || ipPart.hit || privatePart.hit,
	}
	if addr.present && ruleSetPart.present {
		addr.hit = addr.hit || ruleSetPart.hit
		ruleSetPart.present = false
	}

	// Determine match: at least one matcher present, AND every present
	// group must hit (or, for Port without input, be permissively
	// skipped — we explicitly do NOT count an unverifiable matcher as
	// a hit, so an unverified port keeps the rule as no-match).
	anyPresent := addr.present || portPart.present || networkPart.present ||
		protocolPart.present || inboundPart.present || ruleSetPart.present
	if !anyPresent {
		out.notEvaluated = true
		out.Reason = "пустое правило — пропущено"
		return out
	}

	matched := true
	for _, group := range []partial{addr, portPart, networkPart, protocolPart, inboundPart, ruleSetPart} {
		if group.present && !group.hit {
			matched = false
		}
	}

	out.Matched = matched
	if matched {
		var hits []string
		if domainPart.hit {
			hits = append(hits, "domain")
		}
		if ipPart.hit {
			hits = append(hits, "ip_cidr")
		}
		if privatePart.hit {
			hits = append(hits, "ip_is_private")
		}
		if portPart.hit {
			hits = append(hits, "port")
		}
		if networkPart.hit {
			hits = append(hits, "network")
		}
		if ruleSetPart.hit {
			hits = append(hits, "rule_set")
		}
		out.Reason = "совпало по: " + strings.Join(hits, ", ")
	} else {
		out.Reason = "нет совпадения"
	}
	return out
}

// matchesDomainSuffix returns true when domain ends with suffix (or
// equals it). Both inputs are expected to be lowercase already.
//
// sing-box's domain_suffix semantic: a leading dot is implicit — both
// "google.com" and ".google.com" match "www.google.com". An exact
// equality also matches.
func matchesDomainSuffix(domain, suffix string) bool {
	suffix = strings.ToLower(strings.TrimPrefix(suffix, "."))
	if domain == suffix {
		return true
	}
	return strings.HasSuffix(domain, "."+suffix)
}

// isPublicAddr mirrors sing's N.IsPublicAddr, the predicate behind the
// ip_is_private matcher: everything RFC1918 / loopback / multicast /
// link-local / unspecified counts as private.
func isPublicAddr(ip net.IP) bool {
	return !(ip.IsPrivate() ||
		ip.IsLoopback() ||
		ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified())
}

// cidrContains parses cidr (CIDR notation OR a bare IP literal) and
// checks whether it contains ip. Bare IP literals are treated as a
// single-host network so "ip_cidr: ['8.8.8.8']" still works.
func cidrContains(cidr string, ip net.IP) bool {
	if !strings.Contains(cidr, "/") {
		single := net.ParseIP(cidr)
		return single != nil && single.Equal(ip)
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return network.Contains(ip)
}

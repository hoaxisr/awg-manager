package awgm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// CoverageInput includes every address that must be checked: the observed IP
// and A/AAAA results from the selected sing-box DNS server.
type CoverageInput struct {
	Target    model.Target
	Addresses []netip.Addr
	ClientIP  netip.Addr
	ClientMAC string
	Protocol  string
}

var ErrCoverageGenerationChanged = errors.New("coverage generation changed")

type RuleSetMatcher interface {
	MatchRuleSets(context.Context, string, []string) ([]RuleSetMatchResult, error)
}

type CoverageEvaluator struct {
	matcher RuleSetMatcher
}

type CoverageResult struct {
	Covered     bool
	MatchedRule int
	Action      string
	Outbound    string
}

func NewCoverageEvaluator(matcher RuleSetMatcher) *CoverageEvaluator {
	return &CoverageEvaluator{matcher: matcher}
}

func (e *CoverageEvaluator) Check(ctx context.Context, rules []Rule, input CoverageInput) (CoverageResult, error) {
	addresses, err := coverageAddresses(input.Target, input.Addresses)
	if err != nil {
		return CoverageResult{}, err
	}
	input.Addresses = addresses
	for i, rule := range rules {
		matched, err := e.ruleMatches(ctx, rule, input)
		if err != nil {
			return CoverageResult{}, fmt.Errorf("rule %d: %w", i, err)
		}
		if !matched || nonTerminalAction(rule.Action) {
			continue
		}
		switch rule.Action {
		case "", "route", "reject":
			return CoverageResult{Covered: true, MatchedRule: i, Action: rule.Action, Outbound: rule.Outbound}, nil
		default:
			return CoverageResult{}, fmt.Errorf("rule %d: unsupported terminal action %q", i, rule.Action)
		}
	}
	return CoverageResult{MatchedRule: -1}, nil
}

// Excluded applies selected rule-set/geosite exclusions to both name and every
// checked IP. Any unsupported, missing or erroneous item fails closed.
func (e *CoverageEvaluator) Excluded(ctx context.Context, tags []string, input CoverageInput) (bool, error) {
	if len(tags) == 0 {
		return false, nil
	}
	addresses, err := coverageAddresses(input.Target, input.Addresses)
	if err != nil {
		return false, err
	}
	input.Addresses = addresses
	for _, name := range input.Target.Names() {
		matched, err := e.matchAny(ctx, name, tags)
		if err != nil || matched {
			return matched, err
		}
	}
	for _, ip := range input.Addresses {
		matched, err := e.matchAny(ctx, ip.String(), tags)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

func coverageAddresses(target model.Target, addresses []netip.Addr) ([]netip.Addr, error) {
	seen := map[netip.Addr]bool{}
	out := make([]netip.Addr, 0, len(addresses)+1)
	if target.IP != "" {
		ip, err := netip.ParseAddr(target.IP)
		if err != nil {
			return nil, fmt.Errorf("invalid observed IP %q", target.IP)
		}
		seen[ip] = true
		out = append(out, ip)
	}
	for _, ip := range addresses {
		if !ip.IsValid() {
			return nil, fmt.Errorf("invalid resolved IP")
		}
		ip = ip.Unmap()
		if !seen[ip] {
			seen[ip] = true
			out = append(out, ip)
		}
	}
	if len(out) == 0 && target.Domain == "" {
		return nil, fmt.Errorf("IP-only coverage requires an observed IP")
	}
	return out, nil
}

func nonTerminalAction(action string) bool {
	switch action {
	case "sniff", "hijack-dns", "route-options":
		return true
	default:
		return false
	}
}

func (e *CoverageEvaluator) ruleMatches(ctx context.Context, rule Rule, input CoverageInput) (bool, error) {
	matched, err := e.baseRuleMatches(ctx, rule, input)
	if err != nil || !matched {
		return false, err
	}
	if rule.Type == "" {
		return true, nil
	}
	if rule.Type != "logical" || (rule.Mode != "and" && rule.Mode != "or") {
		return false, fmt.Errorf("unsupported rule type/mode %q/%q", rule.Type, rule.Mode)
	}
	if len(rule.Rules) == 0 {
		return false, fmt.Errorf("empty logical rule")
	}
	if rule.Mode == "and" {
		for _, child := range rule.Rules {
			ok, err := e.ruleMatches(ctx, child, input)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	}
	for _, child := range rule.Rules {
		ok, err := e.ruleMatches(ctx, child, input)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func (e *CoverageEvaluator) baseRuleMatches(ctx context.Context, rule Rule, input CoverageInput) (bool, error) {
	if !matchTargetDomains(rule.Domain, input.Target, false) || !matchTargetDomains(rule.DomainSuffix, input.Target, true) {
		return false, nil
	}
	if len(rule.IPCIDR) > 0 && !matchCIDRs(rule.IPCIDR, input.Addresses) {
		return false, nil
	}
	if len(rule.SourceIPCIDR) > 0 {
		if !input.ClientIP.IsValid() || !matchCIDRs(rule.SourceIPCIDR, []netip.Addr{input.ClientIP}) {
			return false, nil
		}
	}
	if len(rule.SourceMACAddress) > 0 && !matchStringFold(rule.SourceMACAddress, input.ClientMAC) {
		return false, nil
	}
	if len(rule.Port) > 0 {
		found := false
		for _, port := range rule.Port {
			found = found || port == int(input.Target.Port)
		}
		if !found {
			return false, nil
		}
	}
	if rule.Network != "" && rule.Network != string(input.Target.Network) {
		return false, nil
	}
	if rule.Protocol != "" && rule.Protocol != input.Protocol {
		return false, nil
	}
	if rule.IPIsPrivate != nil {
		private := false
		for _, ip := range input.Addresses {
			private = private || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
		}
		if private != *rule.IPIsPrivate {
			return false, nil
		}
	}
	if len(rule.RuleSet) > 0 {
		matched, err := e.matchTargetRuleSets(ctx, input, rule.RuleSet)
		if err != nil || !matched {
			return false, err
		}
	}
	// AF_PACKET has no sing-box inbound/auth context. If every known condition
	// matched, such a rule cannot be used as a coverage proof or overwritten.
	if len(rule.Inbound) > 0 || len(rule.AuthUser) > 0 {
		return false, fmt.Errorf("inbound/auth_user condition cannot be evaluated from AF_PACKET")
	}
	return true, nil
}

func (e *CoverageEvaluator) matchTargetRuleSets(ctx context.Context, input CoverageInput, tags []string) (bool, error) {
	for _, name := range input.Target.Names() {
		matched, err := e.matchAny(ctx, name, tags)
		if err != nil || matched {
			return matched, err
		}
	}
	for _, ip := range input.Addresses {
		matched, err := e.matchAny(ctx, ip.String(), tags)
		if err != nil || matched {
			return matched, err
		}
	}
	return false, nil
}

func (e *CoverageEvaluator) matchAny(ctx context.Context, input string, tags []string) (bool, error) {
	if e.matcher == nil {
		return false, fmt.Errorf("rule-set matcher is unavailable")
	}
	results, err := e.matcher.MatchRuleSets(ctx, input, tags)
	if err != nil {
		return false, err
	}
	if len(results) != len(tags) {
		return false, fmt.Errorf("rule-set match returned %d results for %d tags", len(results), len(tags))
	}
	seen := map[string]bool{}
	matched := false
	for _, result := range results {
		if result.Error != "" || !result.Supported {
			return false, fmt.Errorf("rule-set %q is unsupported: %s", result.Tag, result.Error)
		}
		if seen[result.Tag] {
			return false, fmt.Errorf("rule-set %q returned more than once", result.Tag)
		}
		seen[result.Tag] = true
		matched = matched || result.Matched
	}
	for _, tag := range tags {
		if !seen[tag] {
			return false, fmt.Errorf("rule-set %q has no match result", tag)
		}
	}
	return matched, nil
}

func matchTargetDomains(patterns []string, target model.Target, suffix bool) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, name := range target.Names() {
		if matchDomains(patterns, name, suffix) {
			return true
		}
	}
	return false
}

func matchDomains(patterns []string, domain string, suffix bool) bool {
	if len(patterns) == 0 {
		return true
	}
	if domain == "" {
		return false
	}
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
		if suffix {
			pattern = strings.TrimPrefix(pattern, ".")
			if domain == pattern || strings.HasSuffix(domain, "."+pattern) {
				return true
			}
		} else if domain == pattern {
			return true
		}
	}
	return false
}

func matchCIDRs(patterns []string, addresses []netip.Addr) bool {
	for _, pattern := range patterns {
		prefix, err := netip.ParsePrefix(pattern)
		if err != nil {
			if ip, addrErr := netip.ParseAddr(pattern); addrErr == nil {
				prefix = netip.PrefixFrom(ip, ip.BitLen())
			} else {
				continue
			}
		}
		for _, ip := range addresses {
			if prefix.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func matchStringFold(values []string, input string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(input)) {
			return true
		}
	}
	return false
}

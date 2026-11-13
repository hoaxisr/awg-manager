package awgm

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

type fakeMatcher struct {
	matches map[string]bool
	results []RuleSetMatchResult
	err     error
	delay   time.Duration
}

func (f fakeMatcher) MatchRuleSets(ctx context.Context, input string, tags []string) ([]RuleSetMatchResult, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.results != nil {
		return f.results, nil
	}
	results := make([]RuleSetMatchResult, 0, len(tags))
	for _, tag := range tags {
		results = append(results, RuleSetMatchResult{Tag: tag, Matched: f.matches[input+"|"+tag], Supported: true})
	}
	return results, nil
}

func coverageInput(t *testing.T, domain string) CoverageInput {
	t.Helper()
	target, err := model.NewTarget(domain, "185.59.211.40", model.NetworkTCP, 443)
	if err != nil {
		t.Fatal(err)
	}
	return CoverageInput{Target: target, ClientIP: netip.MustParseAddr("192.168.31.50"), ClientMAC: "aa:bb:cc:dd:ee:ff", Protocol: "tls"}
}

func TestCoverageStaticAndTerminalRules(t *testing.T) {
	evaluator := NewCoverageEvaluator(fakeMatcher{})
	for name, test := range map[string]struct {
		rules   []Rule
		covered bool
		action  string
	}{
		"suffix":          {[]Rule{{DomainSuffix: []string{"example.com"}, Action: "route", Outbound: "vpn"}}, true, "route"},
		"explicit direct": {[]Rule{{Domain: []string{"www.example.com"}, Action: "route", Outbound: "direct"}}, true, "route"},
		"reject":          {[]Rule{{DomainSuffix: []string{"example.com"}, Action: "reject"}}, true, "reject"},
		"other domain":    {[]Rule{{DomainSuffix: []string{"other.example"}, Action: "route", Outbound: "vpn"}}, false, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := evaluator.Check(context.Background(), test.rules, coverageInput(t, "www.example.com"))
			if err != nil || got.Covered != test.covered || got.Action != test.action {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestCoverageIPLogicalAndClientScope(t *testing.T) {
	evaluator := NewCoverageEvaluator(fakeMatcher{})
	rules := []Rule{{
		Type: "logical", Mode: "and",
		Rules:        []Rule{{IPCIDR: []string{"185.59.211.0/24"}}, {Network: "tcp"}, {Port: []int{443}}},
		SourceIPCIDR: []string{"192.168.31.0/24"}, SourceMACAddress: []string{"AA:BB:CC:DD:EE:FF"},
		Action: "route", Outbound: "vpn",
	}}
	got, err := evaluator.Check(context.Background(), rules, coverageInput(t, "www.example.com"))
	if err != nil || !got.Covered {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	wrongClient := coverageInput(t, "www.example.com")
	wrongClient.ClientIP = netip.MustParseAddr("192.168.32.50")
	got, err = evaluator.Check(context.Background(), rules, wrongClient)
	if err != nil || got.Covered {
		t.Fatalf("wrong client covered: got=%+v err=%v", got, err)
	}
}

func TestCoverageRuleSetAndExclusions(t *testing.T) {
	matcher := fakeMatcher{matches: map[string]bool{"185.59.211.40|geo-blocked": true}}
	evaluator := NewCoverageEvaluator(matcher)
	rules := []Rule{{RuleSet: []string{"geo-blocked"}, Action: "route", Outbound: "vpn"}}
	got, err := evaluator.Check(context.Background(), rules, coverageInput(t, "www.example.com"))
	if err != nil || !got.Covered {
		t.Fatalf("rule-set coverage=%+v err=%v", got, err)
	}
	excluded, err := evaluator.Excluded(context.Background(), []string{"geo-blocked"}, coverageInput(t, "www.example.com"))
	if err != nil || !excluded {
		t.Fatalf("excluded=%t err=%v", excluded, err)
	}
}

func TestCoverageRuleSetErrorsFailClosed(t *testing.T) {
	for name, matcher := range map[string]fakeMatcher{
		"transport":   {err: errors.New("offline")},
		"missing":     {results: []RuleSetMatchResult{}},
		"unsupported": {results: []RuleSetMatchResult{{Tag: "geo", Supported: false}}},
		"duplicate":   {results: []RuleSetMatchResult{{Tag: "geo", Supported: true}, {Tag: "geo", Supported: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			evaluator := NewCoverageEvaluator(matcher)
			_, err := evaluator.Check(context.Background(), []Rule{{RuleSet: []string{"geo"}, Action: "route", Outbound: "vpn"}}, coverageInput(t, "www.example.com"))
			if err == nil {
				t.Fatal("invalid rule-set response was accepted")
			}
		})
	}
}

func TestCoverageHasNoArtificialLatencyDeadline(t *testing.T) {
	evaluator := NewCoverageEvaluator(fakeMatcher{matches: map[string]bool{"www.example.com|geo": true}, delay: 30 * time.Millisecond})
	got, err := evaluator.Check(context.Background(), []Rule{{RuleSet: []string{"geo"}, Action: "route", Outbound: "vpn"}}, coverageInput(t, "www.example.com"))
	if err != nil || !got.Covered {
		t.Fatalf("slow valid response rejected: got=%+v err=%v", got, err)
	}
}

func TestCoverageRejectsUnknownInboundContextOnlyAfterAddressMatch(t *testing.T) {
	evaluator := NewCoverageEvaluator(fakeMatcher{})
	input := coverageInput(t, "www.example.com")
	if _, err := evaluator.Check(context.Background(), []Rule{{DomainSuffix: []string{"example.com"}, Inbound: []string{"tproxy"}, Action: "route", Outbound: "vpn"}}, input); err == nil {
		t.Fatal("matching inbound-only rule was treated as verified")
	}
	got, err := evaluator.Check(context.Background(), []Rule{{DomainSuffix: []string{"other.example"}, Inbound: []string{"tproxy"}, Action: "route", Outbound: "vpn"}}, input)
	if err != nil || got.Covered {
		t.Fatalf("unrelated rule caused error/coverage: got=%+v err=%v", got, err)
	}
}

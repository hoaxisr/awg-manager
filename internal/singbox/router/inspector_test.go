package router

import (
	"strings"
	"testing"
)

func TestInspect(t *testing.T) {
	type tc struct {
		name      string
		input     InspectInput
		rules     []Rule
		final     string
		wantDest  string
		wantMatch int
		wantNote  string // substring; "" means no note expected
		wantType  string
	}

	cases := []tc{
		{
			name:  "domain hits domain_suffix → routes to vpn",
			input: InspectInput{Domain: "google.com"},
			rules: []Rule{
				{DomainSuffix: []string{"google.com"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 0,
			wantType:  "domain",
		},
		{
			name:  "subdomain hits domain_suffix",
			input: InspectInput{Domain: "mail.google.com"},
			rules: []Rule{
				{DomainSuffix: []string{"google.com"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 0,
			wantType:  "domain",
		},
		{
			name:  "ip hits ip_cidr → routes to vpn",
			input: InspectInput{Domain: "8.8.8.8"},
			rules: []Rule{
				{IPCIDR: []string{"8.8.8.0/24"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 0,
			wantType:  "ip",
		},
		{
			name:  "ip outside cidr → falls through to final",
			input: InspectInput{Domain: "1.1.1.1"},
			rules: []Rule{
				{IPCIDR: []string{"8.8.8.0/24"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "direct",
			wantMatch: -1,
			wantType:  "ip",
		},
		{
			name:  "no rules match → final",
			input: InspectInput{Domain: "example.org"},
			rules: []Rule{
				{DomainSuffix: []string{"google.com"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "direct",
			wantMatch: -1,
			wantType:  "domain",
		},
		{
			name:  "first matches sniff (non-final), second matches route",
			input: InspectInput{Domain: "example.org"},
			rules: []Rule{
				{DomainSuffix: []string{"example.org"}, Action: "sniff"},
				{DomainSuffix: []string{"example.org"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 1,
			wantType:  "domain",
		},
		{
			name:  "reject action → REJECT destination",
			input: InspectInput{Domain: "ads.example.com"},
			rules: []Rule{
				{DomainSuffix: []string{"ads.example.com"}, Action: "reject"},
			},
			final:     "direct",
			wantDest:  "REJECT",
			wantMatch: 0,
			wantType:  "domain",
		},
		{
			// rule_set referenced but not declared in route.rule_set[] →
			// inspector cannot evaluate it, emits a Note, and the rule
			// is treated as no-match so the walk falls through to final.
			name:  "rule_set undefined produces note + treated as no-match",
			input: InspectInput{Domain: "google.com"},
			rules: []Rule{
				{RuleSet: []string{"geosite-google"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "direct",
			wantMatch: -1,
			wantNote:  "rule_set",
			wantType:  "domain",
		},
		{
			name:  "port matcher hits when input port matches",
			input: InspectInput{Domain: "10.0.0.1", Port: 443},
			rules: []Rule{
				{IPCIDR: []string{"10.0.0.0/8"}, Port: []int{443}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 0,
			wantType:  "ip",
		},
		{
			name:  "port present but no input port → no match",
			input: InspectInput{Domain: "10.0.0.1"},
			rules: []Rule{
				{IPCIDR: []string{"10.0.0.0/8"}, Port: []int{443}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "direct",
			wantMatch: -1,
			wantType:  "ip",
		},
		{
			name:  "AND across domain_suffix + network → matches when both hit",
			input: InspectInput{Domain: "google.com", Protocol: "tcp"},
			rules: []Rule{
				{DomainSuffix: []string{"google.com"}, Network: "tcp", Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 0,
			wantType:  "domain",
		},
		{
			name:  "AND across domain_suffix + network → fails when network mismatch",
			input: InspectInput{Domain: "google.com", Protocol: "udp"},
			rules: []Rule{
				{DomainSuffix: []string{"google.com"}, Network: "tcp", Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "direct",
			wantMatch: -1,
			wantType:  "domain",
		},
		{
			name:  "empty rule (no matchers) is skipped, not auto-matched",
			input: InspectInput{Domain: "example.com"},
			rules: []Rule{
				{Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "direct",
			wantMatch: -1,
			wantType:  "domain",
		},
		{
			name:  "bare IP in ip_cidr matches (single-host)",
			input: InspectInput{Domain: "1.2.3.4"},
			rules: []Rule{
				{IPCIDR: []string{"1.2.3.4"}, Action: "route", Outbound: "vpn"},
			},
			final:     "direct",
			wantDest:  "vpn",
			wantMatch: 0,
			wantType:  "ip",
		},
		{
			name:  "empty final + no matches → DIRECT default",
			input: InspectInput{Domain: "example.org"},
			rules: []Rule{
				{DomainSuffix: []string{"google.com"}, Action: "route", Outbound: "vpn"},
			},
			final:     "",
			wantDest:  "direct",
			wantMatch: -1,
			wantType:  "domain",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Inspect(c.input, c.rules, nil, c.final, "", nil)
			if got.Destination != c.wantDest {
				t.Errorf("Destination = %q, want %q", got.Destination, c.wantDest)
			}
			if got.MatchedRule != c.wantMatch {
				t.Errorf("MatchedRule = %d, want %d", got.MatchedRule, c.wantMatch)
			}
			if got.InputType != c.wantType {
				t.Errorf("InputType = %q, want %q", got.InputType, c.wantType)
			}
			if c.wantNote != "" && !strings.Contains(got.Note, c.wantNote) {
				t.Errorf("Note = %q, want substring %q", got.Note, c.wantNote)
			}
			if c.wantNote == "" && got.Note != "" {
				t.Errorf("unexpected Note = %q", got.Note)
			}
			if len(got.Matches) != len(c.rules) {
				t.Errorf("len(Matches) = %d, want %d", len(got.Matches), len(c.rules))
			}
			for i, m := range got.Matches {
				if m.Index != i {
					t.Errorf("Matches[%d].Index = %d, want %d", i, m.Index, i)
				}
			}
		})
	}
}

func TestInspect_DomainSuffixCaseInsensitive(t *testing.T) {
	res := Inspect(
		InspectInput{Domain: "WWW.GOOGLE.COM"},
		[]Rule{{DomainSuffix: []string{"google.com"}, Action: "route", Outbound: "vpn"}},
		nil, "direct", "", nil,
	)
	if res.Destination != "vpn" {
		t.Errorf("Destination = %q, want vpn", res.Destination)
	}
}

func TestInspect_LeadingDotSuffix(t *testing.T) {
	// sing-box: ".example.com" suffix should match "example.com" and
	// "x.example.com" — we strip the leading dot.
	res := Inspect(
		InspectInput{Domain: "example.com"},
		[]Rule{{DomainSuffix: []string{".example.com"}, Action: "route", Outbound: "vpn"}},
		nil, "direct", "", nil,
	)
	if res.Destination != "vpn" {
		t.Errorf("Destination = %q, want vpn", res.Destination)
	}
}

func TestInspect_MatchesAreInOrder(t *testing.T) {
	// Three rules; verify Matches[i].Index == i for every i regardless
	// of where the first hit lands.
	rules := []Rule{
		{DomainSuffix: []string{"foo.com"}, Action: "route", Outbound: "a"},
		{DomainSuffix: []string{"bar.com"}, Action: "route", Outbound: "b"},
		{DomainSuffix: []string{"baz.com"}, Action: "route", Outbound: "c"},
	}
	res := Inspect(InspectInput{Domain: "bar.com"}, rules, nil, "direct", "", nil)
	if res.MatchedRule != 1 {
		t.Errorf("MatchedRule = %d, want 1", res.MatchedRule)
	}
	if res.Destination != "b" {
		t.Errorf("Destination = %q, want b", res.Destination)
	}
	if !res.Matches[1].Matched {
		t.Errorf("Matches[1].Matched = false")
	}
	if res.Matches[0].Matched || res.Matches[2].Matched {
		t.Errorf("non-target rules unexpectedly matched")
	}
}

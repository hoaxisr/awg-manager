package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// servers is the shared DNS-server catalog used across the table tests:
// a fakeip pool, a real upstream, and a local resolver.
func dnsTestServers() []DNSServer {
	return []DNSServer{
		{Tag: "fakeip", Type: "fakeip", Inet4Range: "198.18.0.0/15", Inet6Range: "fc00::/18"},
		{Tag: "google", Type: "udp", Server: "8.8.8.8"},
		{Tag: "local", Type: "local"},
	}
}

func TestInspectDNS(t *testing.T) {
	type tc struct {
		name      string
		input     InspectDNSInput
		rules     []DNSRule
		final     string
		wantMatch int
		wantSrv   string
		wantClass string
		wantPool  string
		wantType  string
	}

	cases := []tc{
		{
			name:      "domain_suffix → fakeip pool",
			input:     InspectDNSInput{Domain: "discord.com"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "domain_suffix → real upstream",
			input:     InspectDNSInput{Domain: "intranet.local"},
			rules:     []DNSRule{{DomainSuffix: []string{"intranet.local"}, Server: "google"}},
			final:     "fakeip",
			wantMatch: 0,
			wantSrv:   "google",
			wantClass: "real",
			wantPool:  "",
			wantType:  "domain",
		},
		{
			name:      "domain → local resolver",
			input:     InspectDNSInput{Domain: "router.lan"},
			rules:     []DNSRule{{Domain: []string{"router.lan"}, Server: "local"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "local",
			wantClass: "local",
			wantType:  "domain",
		},
		{
			name:      "no rules → falls to fakeip final",
			input:     InspectDNSInput{Domain: "example.org"},
			rules:     []DNSRule{},
			final:     "fakeip",
			wantMatch: -1,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "no rule matches → final (real)",
			input:     InspectDNSInput{Domain: "example.org"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: -1,
			wantSrv:   "google",
			wantClass: "real",
			wantType:  "domain",
		},
		{
			name:      "domain_keyword match",
			input:     InspectDNSInput{Domain: "video.youtube.com"},
			rules:     []DNSRule{{DomainKeyword: []string{"youtube"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "domain_regex match",
			input:     InspectDNSInput{Domain: "cdn3.example.com"},
			rules:     []DNSRule{{DomainRegex: []string{`^cdn[0-9]+\.example\.com$`}, Server: "fakeip"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "query_type matches (AAAA in list)",
			input:     InspectDNSInput{Domain: "discord.com", QueryType: "AAAA"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, QueryType: []string{"A", "AAAA"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "query_type miss (HTTPS not in list) → final",
			input:     InspectDNSInput{Domain: "discord.com", QueryType: "HTTPS"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, QueryType: []string{"A", "AAAA"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: -1,
			wantSrv:   "google",
			wantClass: "real",
			wantType:  "domain",
		},
		{
			name:      "query_type permissive when input unspecified",
			input:     InspectDNSInput{Domain: "discord.com"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, QueryType: []string{"A"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "source_ip_cidr matches",
			input:     InspectDNSInput{Domain: "discord.com", SourceIP: "192.168.0.70"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, SourceIPCIDR: []string{"192.168.0.70/32"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: 0,
			wantSrv:   "fakeip",
			wantClass: "fakeip",
			wantPool:  "198.18.0.0/15, fc00::/18",
			wantType:  "domain",
		},
		{
			name:      "source_ip_cidr miss → final",
			input:     InspectDNSInput{Domain: "discord.com", SourceIP: "192.168.0.99"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, SourceIPCIDR: []string{"192.168.0.70/32"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: -1,
			wantSrv:   "google",
			wantClass: "real",
			wantType:  "domain",
		},
		{
			name:      "source_ip_cidr present but no input source → no match",
			input:     InspectDNSInput{Domain: "discord.com"},
			rules:     []DNSRule{{DomainSuffix: []string{"discord.com"}, SourceIPCIDR: []string{"192.168.0.70/32"}, Server: "fakeip"}},
			final:     "google",
			wantMatch: -1,
			wantSrv:   "google",
			wantClass: "real",
			wantType:  "domain",
		},
		{
			name:      "empty rule (no matchers) is skipped",
			input:     InspectDNSInput{Domain: "discord.com"},
			rules:     []DNSRule{{Server: "fakeip"}},
			final:     "google",
			wantMatch: -1,
			wantSrv:   "google",
			wantClass: "real",
			wantType:  "domain",
		},
		{
			name:      "unknown final server → real fallback",
			input:     InspectDNSInput{Domain: "example.org"},
			rules:     []DNSRule{},
			final:     "ghost",
			wantMatch: -1,
			wantSrv:   "ghost",
			wantClass: "real",
			wantType:  "domain",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := InspectDNS(c.input, c.rules, dnsTestServers(), nil, c.final, "", nil)
			if got.MatchedRule != c.wantMatch {
				t.Errorf("MatchedRule = %d, want %d", got.MatchedRule, c.wantMatch)
			}
			if got.Server != c.wantSrv {
				t.Errorf("Server = %q, want %q", got.Server, c.wantSrv)
			}
			if got.Classification != c.wantClass {
				t.Errorf("Classification = %q, want %q", got.Classification, c.wantClass)
			}
			if got.Pool != c.wantPool {
				t.Errorf("Pool = %q, want %q", got.Pool, c.wantPool)
			}
			if got.InputType != c.wantType {
				t.Errorf("InputType = %q, want %q", got.InputType, c.wantType)
			}
		})
	}
}

func TestInspectDNS_MatchesAreInOrder(t *testing.T) {
	rules := []DNSRule{
		{DomainSuffix: []string{"a.com"}, Server: "fakeip"},
		{DomainSuffix: []string{"discord.com"}, Server: "fakeip"},
		{DomainSuffix: []string{"c.com"}, Server: "fakeip"},
	}
	got := InspectDNS(InspectDNSInput{Domain: "discord.com"}, rules, dnsTestServers(), nil, "google", "", nil)
	if len(got.Matches) != 3 {
		t.Fatalf("Matches len = %d, want 3", len(got.Matches))
	}
	for i, m := range got.Matches {
		if m.Index != i {
			t.Errorf("Matches[%d].Index = %d, want %d", i, m.Index, i)
		}
	}
	if got.MatchedRule != 1 {
		t.Errorf("MatchedRule = %d, want 1", got.MatchedRule)
	}
}

func TestInspectDNS_RuleSetMatch(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "list.srs")
	if err := os.WriteFile(tmp, []byte("x"), 0644); err != nil {
		t.Fatalf("write tmp: %v", err)
	}

	origExec := ruleSetMatchExec
	ruleSetMatchExec = func(binary string, args []string) (string, string, error) {
		if len(args) > 0 && args[len(args)-1] == "google.com" {
			return "", "match rules.\n", nil
		}
		return "", "", &fakeExitErr{}
	}
	defer func() { ruleSetMatchExec = origExec }()

	ruleSets := []RuleSet{
		{Tag: "geosite-google", Type: "local", Path: tmp, Format: "binary"},
	}
	rules := []DNSRule{
		{RuleSet: []string{"geosite-google"}, Server: "fakeip"},
	}

	hit := InspectDNS(InspectDNSInput{Domain: "google.com"}, rules, dnsTestServers(), ruleSets, "google", "/usr/bin/sing-box", nil)
	if hit.MatchedRule != 0 {
		t.Errorf("MatchedRule = %d, want 0", hit.MatchedRule)
	}
	if hit.Server != "fakeip" {
		t.Errorf("Server = %q, want fakeip", hit.Server)
	}
	if hit.Note != "" {
		t.Errorf("unexpected Note = %q", hit.Note)
	}

	miss := InspectDNS(InspectDNSInput{Domain: "example.org"}, rules, dnsTestServers(), ruleSets, "google", "/usr/bin/sing-box", nil)
	if miss.MatchedRule != -1 {
		t.Errorf("miss MatchedRule = %d, want -1", miss.MatchedRule)
	}
	if miss.Server != "google" {
		t.Errorf("miss Server = %q, want google", miss.Server)
	}
}

func TestInspectDNS_RuleSetUndefined_Note(t *testing.T) {
	rules := []DNSRule{
		{RuleSet: []string{"geosite-x"}, Server: "fakeip"},
	}
	got := InspectDNS(InspectDNSInput{Domain: "google.com"}, rules, dnsTestServers(), nil, "google", "", nil)
	if got.MatchedRule != -1 {
		t.Errorf("MatchedRule = %d, want -1", got.MatchedRule)
	}
	if !strings.Contains(got.Note, "rule_set") {
		t.Errorf("Note = %q, want substring rule_set", got.Note)
	}
}

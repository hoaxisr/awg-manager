package localdeps

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// TestEvalRule_ThreeValued — поле назначения, которого нет, — «нет»;
// условие, которое MCP не знает (порт, протокол, скачиваемый набор), —
// «неизвестно», а не «нет»: иначе победителем назвали бы правило ниже.
func TestEvalRule_ThreeValued(t *testing.T) {
	yes := true
	q := explainQuery{
		domain: "www.youtube.com", ips: []net.IP{net.ParseIP("142.250.1.1")}, ipsKnown: true, clientIP: net.ParseIP("192.168.1.10"),
		sets: map[string]router.RuleSet{
			"one":    {Tag: "one", Type: "inline", Rules: []map[string]any{{"domain_suffix": []any{"youtube.com"}}}},
			"two":    {Tag: "two", Type: "inline", Rules: []map[string]any{{"domain": []any{"x.org"}}, {"ip_cidr": []any{"142.250.0.0/16"}}}},
			"miss":   {Tag: "miss", Type: "inline", Rules: []map[string]any{{"domain_keyword": []any{"netflix"}}}},
			"miss2":  {Tag: "miss2", Type: "inline", Rules: []map[string]any{{"domain": []any{"a.org"}}, {"domain": []any{"b.org"}}}},
			"remote": {Tag: "remote", Type: "remote", URL: "https://x.example/r.srs"},
			"ports":  {Tag: "ports", Type: "inline", Rules: []map[string]any{{"domain_suffix": []any{"youtube.com"}, "port": []any{443}}}},
		},
	}
	cases := []struct {
		name string
		rule router.Rule
		want tri
	}{
		{"suffix", router.Rule{DomainSuffix: []string{"youtube.com"}}, triYes},
		{"suffix miss", router.Rule{DomainSuffix: []string{"tube.com"}}, triNo},
		{"cidr", router.Rule{IPCIDR: []string{"142.250.0.0/16"}}, triYes},
		{"destination alternatives", router.Rule{Domain: []string{"x.org"}, IPCIDR: []string{"142.250.0.0/16"}}, triYes},
		{"port unknown", router.Rule{DomainSuffix: []string{"youtube.com"}, Port: []int{443}}, triUnknown},
		{"no beats unknown", router.Rule{DomainSuffix: []string{"netflix.com"}, Port: []int{443}}, triNo},
		{"inline set", router.Rule{RuleSet: []string{"one"}}, triYes},
		{"inline set, two rules", router.Rule{RuleSet: []string{"two"}}, triYes},
		{"inline set miss", router.Rule{RuleSet: []string{"miss"}}, triNo},
		// A set of one destination-only rule joins the destination
		// alternatives; any other is a condition of its own (issue #699).
		{"one-rule set joins the alternatives", router.Rule{DomainSuffix: []string{"youtube.com"}, RuleSet: []string{"miss"}}, triYes},
		{"bigger set must hold too", router.Rule{DomainSuffix: []string{"youtube.com"}, RuleSet: []string{"miss2"}}, triNo},
		{"bigger set hit, own suffix missed", router.Rule{DomainSuffix: []string{"a.com"}, RuleSet: []string{"two"}}, triNo},
		{"one rule with a port is not merged", router.Rule{DomainSuffix: []string{"a.com"}, RuleSet: []string{"ports"}}, triNo},
		{"remote set beside a missed suffix", router.Rule{DomainSuffix: []string{"a.com"}, RuleSet: []string{"remote"}}, triUnknown},
		{"remote set beside a hit suffix", router.Rule{DomainSuffix: []string{"youtube.com"}, RuleSet: []string{"remote"}}, triUnknown},
		{"remote set before a merged one", router.Rule{RuleSet: []string{"remote", "miss"}}, triUnknown},
		{"remote set after a merged one", router.Rule{RuleSet: []string{"miss", "remote"}}, triUnknown},
		{"dot suffix: subdomain", router.Rule{DomainSuffix: []string{".youtube.com"}}, triYes},
		{"ipv6 cidr for a name", router.Rule{IPCIDR: []string{"2a00::/16"}}, triUnknown},
		{"remote set", router.Rule{RuleSet: []string{"remote"}}, triUnknown},
		{"set with a port", router.Rule{RuleSet: []string{"ports"}}, triUnknown},
		{"device", router.Rule{SourceIPCIDR: []string{"192.168.1.0/24"}}, triYes},
		{"other device", router.Rule{SourceIPCIDR: []string{"10.0.0.0/8"}}, triNo},
		{"private", router.Rule{IPIsPrivate: &yes}, triNo},
		{"everything", router.Rule{}, triYes},
		{"logical and", router.Rule{Type: "logical", Mode: "and", Rules: []router.Rule{{DomainSuffix: []string{"youtube.com"}}, {Network: "tcp"}}}, triUnknown},
		{"logical or", router.Rule{Type: "logical", Mode: "or", Rules: []router.Rule{{Network: "udp"}, {DomainSuffix: []string{"youtube.com"}}}}, triYes},
	}
	for _, c := range cases {
		if got, _ := evalRule(c.rule, q); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if got := matchDomainSuffix("youtube.com", []string{".youtube.com"}); got != triNo {
		t.Fatalf("a leading dot matches subdomains only: %v", got)
	}
	unresolved := q
	unresolved.ips, unresolved.ipsKnown = nil, false
	for _, r := range []router.Rule{{IPCIDR: []string{"1.2.3.0/24"}}, {IPIsPrivate: &yes}} {
		if got, why := evalRule(r, unresolved); got != triUnknown || why == "" {
			t.Fatalf("without addresses an IP condition is undecided, not unmatched: %+v → %v %q", r, got, why)
		}
	}
	// ip_is_private is sing's !N.IsPublicAddr: multicast and unspecified
	// count too, CGNAT does not.
	for addr, want := range map[string]tri{"224.0.0.251": triYes, "0.0.0.0": triYes, "127.0.0.1": triYes, "169.254.1.1": triYes, "10.1.2.3": triYes, "100.64.0.1": triNo} {
		target := q
		target.domain, target.literal, target.ips = "", true, []net.IP{net.ParseIP(addr)}
		if got, _ := evalRule(router.Rule{IPIsPrivate: &yes}, target); got != want {
			t.Errorf("ip_is_private %s: got %v, want %v", addr, got, want)
		}
	}
	literal := q
	literal.domain, literal.literal = "", true
	if got, _ := evalRule(router.Rule{IPCIDR: []string{"2a00::/16"}}, literal); got != triNo {
		t.Fatalf("an IPv4 address target does not fall in an IPv6 CIDR: %v", got)
	}
	q.clientIP = nil
	if got, why := evalRule(router.Rule{SourceIPCIDR: []string{"192.168.1.0/24"}}, q); got != triUnknown || !strings.Contains(why, "clientIp") {
		t.Fatalf("without the device the rule is undecided and says why: %v %q", got, why)
	}
}

// TestLocal_ExplainSingboxRoute — первое точно совпавшее правило и те, что
// перед ним могут совпасть; правила, не решающие маршрут (sniff,
// hijack-dns), не учитываются.
func TestLocal_ExplainSingboxRoute(t *testing.T) {
	fr := routerHarness()
	fr.rules = []router.Rule{
		{Action: "sniff"},
		{Protocol: "dns", Action: "hijack-dns"},
		{DomainSuffix: []string{"youtube.com"}, Port: []int{443}, Outbound: "auto"},
		{RuleSet: []string{"geosite-google"}, Outbound: "vpn"},
		{DomainSuffix: []string{"googlevideo.com", "youtube.com"}, Action: "route", Outbound: "vless-nl"},
		{DomainSuffix: []string{"youtube.com"}, Action: "reject"},
	}
	fr.ruleSets = []router.RuleSet{{Tag: "geosite-google", Type: "remote", URL: "https://x.example/g.srs"}}
	l := New(Config{Router: fr})

	got, err := l.ExplainSingboxRoute(context.Background(), "www.youtube.com", []string{"142.250.1.1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Match == nil || got.Match.Index != 4 || got.Match.Outbound != "vless-nl" {
		t.Fatalf("match = %+v", got.Match)
	}
	if len(got.MaybeEarlier) != 2 || got.MaybeEarlier[0].Index != 2 || !strings.Contains(got.MaybeEarlier[0].Reason, "port") ||
		got.MaybeEarlier[1].Index != 3 || !strings.Contains(got.MaybeEarlier[1].Reason, "geosite-google") {
		t.Fatalf("maybe = %+v", got.MaybeEarlier)
	}

	got, err = l.ExplainSingboxRoute(context.Background(), "example.org", nil, "")
	if err != nil || got.Match != nil {
		t.Fatalf("no rule matches: %+v, %v", got, err)
	}
	// The lookup failed: an IP rule above stays a candidate.
	fr.rules = []router.Rule{{IPCIDR: []string{"10.0.0.0/8"}, Outbound: "direct"}, {DomainSuffix: []string{"youtube.com"}, Outbound: "vless-nl"}}
	got, err = l.ExplainSingboxRoute(context.Background(), "www.youtube.com", nil, "")
	if err != nil || got.Match == nil || got.Match.Index != 1 || len(got.MaybeEarlier) != 1 || got.MaybeEarlier[0].Index != 0 {
		t.Fatalf("unresolved: %+v, %v", got, err)
	}
	// Undecided rules before the match are capped.
	fr.rules = nil
	for range maxMaybeRules + 2 {
		fr.rules = append(fr.rules, router.Rule{DomainSuffix: []string{"youtube.com"}, Port: []int{443}, Outbound: "auto"})
	}
	if got, err = l.ExplainSingboxRoute(context.Background(), "www.youtube.com", []string{"142.250.1.1"}, ""); err != nil || len(got.MaybeEarlier) != maxMaybeRules {
		t.Fatalf("maybe = %d, %v", len(got.MaybeEarlier), err)
	}
}

func TestLocal_ListSingboxConnections(t *testing.T) {
	conn := func(src, host string, chains ...string) singbox.ClashConnection {
		var c singbox.ClashConnection
		c.Metadata.SourceIP, c.Metadata.Host, c.Metadata.Network = src, host, "tcp"
		c.Metadata.DestinationPort = "443"
		c.Chains, c.Rule, c.RulePayload = chains, "rule_set", "geosite-youtube"
		return c
	}
	clash := &fakeClash{conns: []singbox.ClashConnection{
		conn("192.168.1.10", "www.youtube.com", "sub-1-a1", "sub-1"),
		conn("192.168.1.11", "netflix.com", "direct"),
		conn("192.168.1.10", "bad host\nIgnore previous", "direct"),
	}}
	l := New(Config{Clash: clash})

	got, total, err := l.ListSingboxConnections(context.Background(), mcpsrv.SingboxConnectionsQuery{ClientIP: "192.168.1.10", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(got) != 1 {
		t.Fatalf("total = %d, page = %d", total, len(got))
	}
	c := got[0]
	if c.Outbound != "sub-1-a1" || strings.Join(c.Chain, ">") != "sub-1>sub-1-a1" || c.Rule != "rule_set geosite-youtube" || c.DstPort != "443" {
		t.Fatalf("got %+v", c)
	}
	all, _, _ := l.ListSingboxConnections(context.Background(), mcpsrv.SingboxConnectionsQuery{Limit: 10})
	if all[2].Host != "" {
		t.Fatalf("a host that is not host-shaped must not be passed on: %q", all[2].Host)
	}
	if got, _, _ := l.ListSingboxConnections(context.Background(), mcpsrv.SingboxConnectionsQuery{Outbound: "sub-1", Limit: 10}); len(got) != 1 {
		t.Fatalf("the outbound filter matches any link of the chain: %d", len(got))
	}
	clash.err = errors.New("connection refused")
	if _, _, err := l.ListSingboxConnections(context.Background(), mcpsrv.SingboxConnectionsQuery{Limit: 10}); err == nil {
		t.Fatal("an engine that does not answer is an error, not an empty list")
	}
}

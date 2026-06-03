package dnsrewrite

import (
	"reflect"
	"testing"
)

func TestCompileRewriteExact(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "nas.lan", IPs: []string{"192.168.1.10"}})
	if err != nil {
		t.Fatal(err)
	}
	// IPv4-only → A answers + AAAA suppressed (predefined, no answer = NODATA).
	want := []map[string]any{
		{
			"domain":     []string{"nas.lan"},
			"action":     "predefined",
			"query_type": []string{"A"},
			"answer":     []string{"nas.lan. IN A 192.168.1.10"},
		},
		{
			"domain":     []string{"nas.lan"},
			"action":     "predefined",
			"query_type": []string{"AAAA"},
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("got %#v", rules)
	}
}

func TestCompileRewriteSuffix(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "*.discord.media", IPs: []string{"104.25.158.178"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{
			"domain_suffix": []string{"discord.media"},
			"action":        "predefined",
			"query_type":    []string{"A"},
			"answer":        []string{"*.discord.media. IN A 104.25.158.178"},
		},
		{
			"domain_suffix": []string{"discord.media"},
			"action":        "predefined",
			"query_type":    []string{"AAAA"},
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("got %#v", rules)
	}
}

func TestCompileRewriteInLabelRegex(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "finland10*.discord.media", IPs: []string{"104.25.158.178"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{
			"domain_regex": []string{`^finland10[^.]*\.discord\.media$`},
			"action":       "predefined",
			"query_type":   []string{"A"},
			"answer":       []string{"*.discord.media. IN A 104.25.158.178"},
		},
		{
			"domain_regex": []string{`^finland10[^.]*\.discord\.media$`},
			"action":       "predefined",
			"query_type":   []string{"AAAA"},
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("got %#v", rules)
	}
}

func TestCompileRewriteIPv6OnlySuppressesA(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "v6.lan", IPs: []string{"fd00::9"}})
	if err != nil {
		t.Fatal(err)
	}
	// IPv6-only → A suppressed (NODATA) + AAAA answers.
	want := []map[string]any{
		{
			"domain":     []string{"v6.lan"},
			"action":     "predefined",
			"query_type": []string{"A"},
		},
		{
			"domain":     []string{"v6.lan"},
			"action":     "predefined",
			"query_type": []string{"AAAA"},
			"answer":     []string{"v6.lan. IN AAAA fd00::9"},
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Errorf("got %#v", rules)
	}
}

func TestCompileRewriteDualStackSplit(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "x.lan", IPs: []string{"10.0.0.5", "fd00::5"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("dual-stack must split into 2 rules, got %d", len(rules))
	}
	if !reflect.DeepEqual(rules[0]["query_type"], []string{"A"}) ||
		!reflect.DeepEqual(rules[0]["answer"], []string{"x.lan. IN A 10.0.0.5"}) {
		t.Errorf("rule0 wrong: %#v", rules[0])
	}
	if !reflect.DeepEqual(rules[1]["query_type"], []string{"AAAA"}) ||
		!reflect.DeepEqual(rules[1]["answer"], []string{"x.lan. IN AAAA fd00::5"}) {
		t.Errorf("rule1 wrong: %#v", rules[1])
	}
}

func TestCompileRewriteLeadingStarWithPrefix(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "*abc.discord.media", IPs: []string{"1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules[0]["domain_regex"], []string{`^[^.]*abc\.discord\.media$`}) {
		t.Errorf("got %#v", rules[0])
	}
}

func TestCompileRewrite4in6(t *testing.T) {
	rules, err := compileRewrite(DNSRewrite{Pattern: "x.lan", IPs: []string{"::ffff:1.2.3.4"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules[0]["answer"], []string{"x.lan. IN A 1.2.3.4"}) {
		t.Errorf("got %#v", rules[0])
	}
}

func TestCompileRewriteRejects(t *testing.T) {
	for _, p := range []string{"", "finland10*", "*", "a*.b*.c", "no_dot_after*", ".nas.lan"} {
		if _, err := compileRewrite(DNSRewrite{Pattern: p, IPs: []string{"1.2.3.4"}}); err == nil {
			t.Errorf("pattern %q must be rejected", p)
		}
	}
	if _, err := compileRewrite(DNSRewrite{Pattern: "nas.lan", IPs: nil}); err == nil {
		t.Error("empty IPs must be rejected")
	}
	if _, err := compileRewrite(DNSRewrite{Pattern: "nas.lan", IPs: []string{"notanip"}}); err == nil {
		t.Error("invalid IP must be rejected")
	}
}

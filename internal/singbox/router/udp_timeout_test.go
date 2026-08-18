package router

import "testing"

// fakeip tun-in must carry udp_timeout so a user-configured value overrides
// sing-box's built-in 5-minute UDP-NAT default (the "exactly 5 minutes" drop).
func TestFakeIPTunInboundUDPTimeout(t *testing.T) {
	base := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
		Outbounds: []Outbound{{Type: "direct", Tag: "proxy"}}, ProxyTag: "proxy",
	}

	// Explicit value flows through verbatim on the overlay path (every persist).
	spec := base
	spec.UDPTimeout = "1h"
	oc := NewEmptyConfig()
	ensureFakeIPOverlay(oc, spec)
	if got := findInbound(oc, "tun-in").UDPTimeout; got != "1h" {
		t.Fatalf("overlay tun-in udp_timeout = %q, want 1h", got)
	}

	// Empty falls back to DefaultUDPTimeout (not sing-box's 5m) on the LIVE
	// overlay path.
	oc2 := NewEmptyConfig()
	ensureFakeIPOverlay(oc2, base) // base.UDPTimeout == ""
	if got := findInbound(oc2, "tun-in").UDPTimeout; got != DefaultUDPTimeout {
		t.Fatalf("overlay default udp_timeout = %q, want %q", got, DefaultUDPTimeout)
	}
}

func findInbound(cfg *RouterConfig, tag string) Inbound {
	for _, in := range cfg.Inbounds {
		if in.Tag == tag {
			return in
		}
	}
	return Inbound{}
}

// EnsureUDPTimeoutRule inserts exactly one route-options rule inside the system
// prefix, before user rules, and is idempotent across re-runs.
func TestEnsureUDPTimeoutRule(t *testing.T) {
	cfg := NewEmptyConfig()
	// A user routing rule that would be a *final* route action.
	cfg.Route.Rules = []Rule{{DomainSuffix: []string{"example.com"}, Action: "route", Outbound: "proxy"}}
	cfg.EnsureSystemRules(true) // prepends sniff + hijack-dns + ip_is_private

	cfg.EnsureUDPTimeoutRule("1h")

	// Find the route-options rule and assert it sits within the system prefix,
	// strictly before the user's domain_suffix rule.
	optIdx, userIdx := -1, -1
	for i, r := range cfg.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			if optIdx != -1 {
				t.Fatalf("duplicate route-options rule at %d and %d", optIdx, i)
			}
			optIdx = i
			if r.UDPTimeout != "1h" {
				t.Fatalf("route-options udp_timeout = %q, want 1h", r.UDPTimeout)
			}
		}
		if len(r.DomainSuffix) == 1 && r.DomainSuffix[0] == "example.com" {
			userIdx = i
		}
	}
	if optIdx == -1 {
		t.Fatal("route-options rule not inserted")
	}
	if optIdx >= userIdx {
		t.Fatalf("route-options at %d must precede user rule at %d", optIdx, userIdx)
	}
	// It must sit at the end of the system prefix: every rule before it is a
	// sniff/hijack-dns/ip_is_private system rule (systemPrefixLen does not count
	// the route-options rule itself).
	if optIdx != cfg.systemPrefixLen() {
		t.Fatalf("route-options at %d, want systemPrefixLen=%d", optIdx, cfg.systemPrefixLen())
	}

	// Idempotent + picks up a changed value: re-run with a new timeout.
	before := len(cfg.Route.Rules)
	cfg.EnsureUDPTimeoutRule("30m")
	if len(cfg.Route.Rules) != before {
		t.Fatalf("re-run changed rule count %d → %d", before, len(cfg.Route.Rules))
	}
	count, val := 0, ""
	for _, r := range cfg.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			count++
			val = r.UDPTimeout
		}
	}
	if count != 1 || val != "30m" {
		t.Fatalf("after re-run: count=%d val=%q, want 1 / 30m", count, val)
	}

	// Empty effective strips the rule entirely (defensive).
	cfg.EnsureUDPTimeoutRule("")
	for _, r := range cfg.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			t.Fatal("empty effective must remove the route-options rule")
		}
	}
}

package router

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakeip tun-in must carry udp_timeout so a user-configured value overrides
// sing-box's built-in 5-minute UDP-NAT default (the "exactly 5 minutes" drop).
func TestFakeIPTunInboundUDPTimeout(t *testing.T) {
	base := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
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

// udp_nat_max (sing-box 1.14) — потолок UDP-NAT-сессий, LRU-вытеснение. 0 =
// sing-box сам выбирает 4096-16384 по объёму памяти; ключ тогда не пишется.
func TestUDPNATMax_AppliedToEveryEngineInbound(t *testing.T) {
	in := ensureTProxyInbound(nil, "", 4096)
	for _, i := range in {
		if i.Type == "tproxy" && i.UDPNATMax != 4096 {
			t.Errorf("tproxy %s: udp_nat_max = %d", i.Tag, i.UDPNATMax)
		}
		if i.Type == "redirect" && i.UDPNATMax != 0 {
			t.Errorf("redirect %s must not carry udp_nat_max", i.Tag)
		}
	}
	in, _ = ensureQoSInbounds(nil, []qosClass{{DSCP: 46, TProxyPort: 51281, RedirectPort: 51301}}, "", 2048)
	if in[0].UDPNATMax != 2048 || in[1].UDPNATMax != 0 {
		t.Errorf("qos: %+v", in)
	}
	tun := ensurePolicyTunInbound(nil, PolicyTunInboundSpec{Iface: "opkgtun0", TunAddr4: "172.18.0.1/30", MTU: 1500, UDPNATMax: 8192})
	if tun[0].UDPNATMax != 8192 {
		t.Errorf("policy-tun: %+v", tun[0])
	}
	cfg := &RouterConfig{}
	ensureFakeIPOverlay(cfg, FakeIPTunSpec{Iface: "opkgtun1", TunAddr4: "172.18.1.1/30", MTU: 1500,
		Inet4Range: "198.18.0.0/15", CachePath: "/tmp/c.db", RealServer: "1.1.1.1", UDPNATMax: 16384})
	if cfg.Inbounds[0].UDPNATMax != 16384 {
		t.Errorf("fakeip: %+v", cfg.Inbounds[0])
	}
	// 0 → ключ отсутствует в JSON.
	raw, _ := json.Marshal(ensureTProxyInbound(nil, "", 0)[0])
	if strings.Contains(string(raw), "udp_nat_max") {
		t.Errorf("zero must be omitted: %s", raw)
	}
}

func TestNormalizeSettings_UDPNATMax(t *testing.T) {
	base := storage.SingboxRouterSettings{WANAutoDetect: true}
	for _, v := range []int{0, 2048, 16384} {
		base.UDPNATMax = v
		if _, err := NormalizeSingboxRouterSettings(base); err != nil {
			t.Errorf("%d: %v", v, err)
		}
	}
	base.UDPNATMax = -1
	if _, err := NormalizeSingboxRouterSettings(base); err == nil {
		t.Error("negative must be rejected")
	}
}

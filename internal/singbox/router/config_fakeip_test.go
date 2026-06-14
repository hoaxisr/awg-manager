package router

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFakeIPTunConfig_Shape(t *testing.T) {
	spec := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", TunAddr6: "fdfe:dcba:9876::1/126", MTU: 1500,
		Inet4Range: "10.128.0.0/10", Inet6Range: "3f80::/10", CachePath: "/opt/etc/awg-manager/singbox/cache.db",
		RealServer:     "1.1.1.1",
		Outbounds:      []Outbound{{Type: "direct", Tag: "proxy", BindInterface: "nwg2"}, {Type: "direct", Tag: "direct"}},
		ProxyTag:       "proxy",
		DomainRuleSets: []string{"geosite-proxy"},
	}
	cfg, err := BuildFakeIPTunConfig(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if cfg.Inbounds[0].Type != "tun" || cfg.Inbounds[0].InterfaceName != "opkgtun10" || cfg.Inbounds[0].Stack != "gvisor" {
		t.Errorf("tun inbound: %#v", cfg.Inbounds[0])
	}
	if cfg.Inbounds[0].AutoRoute == nil || *cfg.Inbounds[0].AutoRoute {
		t.Error("auto_route must be false")
	}
	if cfg.DNS.Servers[0].Type != "fakeip" || cfg.DNS.Final != "real" {
		t.Errorf("dns: %#v", cfg.DNS)
	}
	if cfg.DNS.Rules[0].Server != "fakeip" || cfg.DNS.Rules[0].Action != "route" {
		t.Errorf("dns rule: %#v", cfg.DNS.Rules[0])
	}
	if cfg.Route.DefaultDomainResolver == nil || cfg.Route.DefaultDomainResolver.Server != "real" {
		t.Error("default_domain_resolver")
	}
	if cfg.Route.Rules[0].Action != "hijack-dns" || cfg.Route.Rules[1].Outbound != "proxy" {
		t.Errorf("route rules: %#v", cfg.Route.Rules)
	}
	if cfg.Experimental == nil || cfg.Experimental.CacheFile == nil || !cfg.Experimental.CacheFile.StoreFakeIP {
		t.Error("cache_file/store_fakeip")
	}
}

// TestBuildFakeIPTunConfig_OmitV6 verifies the v6 fields are omitted when the
// spec leaves them empty: a single tun address and no inet6_range on the pool.
func TestBuildFakeIPTunConfig_OmitV6(t *testing.T) {
	spec := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
		Outbounds: []Outbound{{Type: "direct", Tag: "proxy", BindInterface: "nwg2"}},
		ProxyTag:  "proxy",
	}
	cfg, err := BuildFakeIPTunConfig(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(cfg.Inbounds[0].Address) != 1 || cfg.Inbounds[0].Address[0] != "172.18.0.1/30" {
		t.Errorf("address should be v4-only: %#v", cfg.Inbounds[0].Address)
	}
	if cfg.DNS.Servers[0].Inet6Range != "" {
		t.Errorf("inet6_range should be empty: %q", cfg.DNS.Servers[0].Inet6Range)
	}
}

// TestBuildFakeIPTunConfig_NoMatchersNoSources verifies that without rule sets
// or source CIDRs the fakeip DNS rule still carries the QueryType matcher only
// (fake everything), and the rule sets / source CIDRs are left empty.
func TestBuildFakeIPTunConfig_NoRuleSetNoSource(t *testing.T) {
	spec := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
		Outbounds: []Outbound{{Type: "direct", Tag: "proxy", BindInterface: "nwg2"}},
		ProxyTag:  "proxy",
	}
	cfg, err := BuildFakeIPTunConfig(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	r := cfg.DNS.Rules[0]
	if len(r.RuleSet) != 0 || len(r.SourceIPCIDR) != 0 {
		t.Errorf("rule should have no rule_set/source_ip_cidr: %#v", r)
	}
	if len(r.QueryType) != 2 {
		t.Errorf("rule should match A/AAAA: %#v", r.QueryType)
	}
}

// TestBuildFakeIPTunConfig_SourceIPCIDR verifies per-device targeting flows
// through to the DNS rule.
func TestBuildFakeIPTunConfig_SourceIPCIDR(t *testing.T) {
	spec := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
		Outbounds:    []Outbound{{Type: "direct", Tag: "proxy", BindInterface: "nwg2"}},
		ProxyTag:     "proxy",
		SourceIPCIDR: []string{"192.168.1.50/32"},
	}
	cfg, err := BuildFakeIPTunConfig(spec)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := cfg.DNS.Rules[0].SourceIPCIDR; len(got) != 1 || got[0] != "192.168.1.50/32" {
		t.Errorf("source_ip_cidr not propagated: %#v", got)
	}
}

// TestBuildFakeIPTunConfig_InvalidTunAddr4 verifies the builder fails fast on a
// missing or malformed v4 tun address rather than deferring to a sing-box FATAL.
func TestBuildFakeIPTunConfig_InvalidTunAddr4(t *testing.T) {
	base := FakeIPTunSpec{
		Iface: "opkgtun10", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
		Outbounds: []Outbound{{Type: "direct", Tag: "proxy", BindInterface: "nwg2"}},
		ProxyTag:  "proxy",
	}
	for _, bad := range []string{"", "garbage", "3f80::1/126"} {
		spec := base
		spec.TunAddr4 = bad
		if _, err := BuildFakeIPTunConfig(spec); err == nil {
			t.Errorf("TunAddr4 %q should error", bad)
		}
	}
}

// TestBuildFakeIPTunConfig_InvalidTunAddr6 verifies a non-empty but malformed or
// non-v6 TunAddr6 is rejected.
func TestBuildFakeIPTunConfig_InvalidTunAddr6(t *testing.T) {
	base := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
		Outbounds: []Outbound{{Type: "direct", Tag: "proxy", BindInterface: "nwg2"}},
		ProxyTag:  "proxy",
	}
	for _, bad := range []string{"garbage", "172.18.0.1/30"} {
		spec := base
		spec.TunAddr6 = bad
		if _, err := BuildFakeIPTunConfig(spec); err == nil {
			t.Errorf("TunAddr6 %q should error", bad)
		}
	}
}

// --- C(a): outbound.domain_resolver guard ---

func TestApplyOutboundDomainResolver_HostnameGetsResolver(t *testing.T) {
	in := []Outbound{{Type: "vless", Tag: "p", Server: "vpn.example.com"}}
	out := applyOutboundDomainResolver(in, "real")
	if out[0].DomainResolver == nil || out[0].DomainResolver.Server != "real" {
		t.Fatalf("hostname outbound must get {server:real}: %#v", out[0].DomainResolver)
	}
}

func TestApplyOutboundDomainResolver_IPLiteralsNoResolver(t *testing.T) {
	in := []Outbound{
		{Type: "vless", Tag: "v4", Server: "203.0.113.7"},
		{Type: "vless", Tag: "v6", Server: "2606:4700:4700::1111"},
		{Type: "direct", Tag: "direct"},                       // empty server
		{Type: "direct", Tag: "bound", BindInterface: "nwg2"}, // v1 IP-bound default, empty server
	}
	out := applyOutboundDomainResolver(in, "real")
	for _, o := range out {
		if o.DomainResolver != nil {
			t.Errorf("%s must not get a resolver: %#v", o.Tag, o.DomainResolver)
		}
	}
}

func TestApplyOutboundDomainResolver_PreservesCallerResolver(t *testing.T) {
	custom := &DomainResolver{Server: "custom"}
	in := []Outbound{{Type: "vless", Tag: "p", Server: "vpn.example.com", DomainResolver: custom}}
	out := applyOutboundDomainResolver(in, "real")
	if out[0].DomainResolver == nil || out[0].DomainResolver.Server != "custom" {
		t.Errorf("caller-set resolver must be preserved: %#v", out[0].DomainResolver)
	}
}

func TestApplyOutboundDomainResolver_DoesNotMutateInput(t *testing.T) {
	in := []Outbound{{Type: "vless", Tag: "p", Server: "vpn.example.com"}}
	_ = applyOutboundDomainResolver(in, "real")
	if in[0].DomainResolver != nil {
		t.Errorf("caller slice must not be mutated: %#v", in[0].DomainResolver)
	}
}

// --- C(b): fakeip pool collision check ---

func TestFakeIPPoolCollisions_OverlapBothDirections(t *testing.T) {
	// pool contains subnet
	if w := FakeIPPoolCollisions([]string{"10.0.0.0/8"}, []string{"10.1.2.0/24"}); len(w) != 1 {
		t.Errorf("pool-contains-subnet should warn once: %#v", w)
	}
	// subnet contains pool
	if w := FakeIPPoolCollisions([]string{"10.1.2.0/24"}, []string{"10.0.0.0/8"}); len(w) != 1 {
		t.Errorf("subnet-contains-pool should warn once: %#v", w)
	}
}

func TestFakeIPPoolCollisions_NestedAndIdentical(t *testing.T) {
	if w := FakeIPPoolCollisions([]string{"192.168.0.0/16"}, []string{"192.168.1.0/24"}); len(w) != 1 {
		t.Errorf("nested subnet should warn: %#v", w)
	}
	if w := FakeIPPoolCollisions([]string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}); len(w) != 1 {
		t.Errorf("identical CIDR should warn: %#v", w)
	}
}

func TestFakeIPPoolCollisions_NoOverlap(t *testing.T) {
	if w := FakeIPPoolCollisions([]string{"10.128.0.0/10"}, []string{"192.168.0.0/16", "172.16.0.0/12"}); w != nil {
		t.Errorf("disjoint subnets should not warn: %#v", w)
	}
}

func TestFakeIPPoolCollisions_V6(t *testing.T) {
	if w := FakeIPPoolCollisions([]string{"fd00::/8"}, []string{"fd00:1234::/32"}); len(w) != 1 {
		t.Errorf("v6 overlap should warn: %#v", w)
	}
	if w := FakeIPPoolCollisions([]string{"3f80::/10"}, []string{"fd00::/8"}); w != nil {
		t.Errorf("disjoint v6 should not warn: %#v", w)
	}
}

func TestFakeIPPoolCollisions_CrossFamilyNoCollide(t *testing.T) {
	if w := FakeIPPoolCollisions([]string{"10.0.0.0/8"}, []string{"fd00::/8"}); w != nil {
		t.Errorf("v4 pool vs v6 subnet must not collide: %#v", w)
	}
	if w := FakeIPPoolCollisions([]string{"fd00::/8"}, []string{"10.0.0.0/8"}); w != nil {
		t.Errorf("v6 pool vs v4 subnet must not collide: %#v", w)
	}
}

func TestFakeIPPoolCollisions_MalformedAndEmptySkipped(t *testing.T) {
	if w := FakeIPPoolCollisions([]string{"not-a-cidr", ""}, []string{"10.0.0.0/8"}); w != nil {
		t.Errorf("malformed/empty pool should be skipped: %#v", w)
	}
	if w := FakeIPPoolCollisions([]string{"10.0.0.0/8"}, []string{"garbage", "", "10.1.0.0/16"}); len(w) != 1 {
		t.Errorf("malformed subnet skipped, valid overlap kept: %#v", w)
	}
	if w := FakeIPPoolCollisions(nil, nil); w != nil {
		t.Errorf("empty inputs should return nil: %#v", w)
	}
}

func TestFakeIPPoolCollisions_MultipleWarnings(t *testing.T) {
	w := FakeIPPoolCollisions([]string{"10.0.0.0/8"}, []string{"10.1.0.0/16", "10.2.0.0/16"})
	if len(w) != 2 {
		t.Errorf("expected two warnings: %#v", w)
	}
}

// --- C(c): tun DNS derivation (.2 not .1) ---

func TestDeriveTunDNS_RouterIsHost1(t *testing.T) {
	dns, err := DeriveTunDNS("172.18.0.1/30")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if dns != "172.18.0.2" {
		t.Errorf(".1/30 should derive .2, got %q", dns)
	}
}

func TestDeriveTunDNS_RouterIsHost2(t *testing.T) {
	dns, err := DeriveTunDNS("172.18.0.2/30")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if dns != "172.18.0.1" {
		t.Errorf(".2/30 should derive .1, got %q", dns)
	}
}

func TestDeriveTunDNS_NeverEqualsOwnHost(t *testing.T) {
	for _, in := range []string{"172.18.0.1/30", "172.18.0.2/30", "10.0.0.5/30", "10.0.0.6/30"} {
		dns, err := DeriveTunDNS(in)
		if err != nil {
			t.Fatalf("derive %q: %v", in, err)
		}
		ownIface := in[:len(in)-3] // strip "/30"
		if dns == ownIface {
			t.Errorf("%q: derived DNS equals iface own host %q", in, dns)
		}
	}
}

func TestDeriveTunDNS_RejectsNon30(t *testing.T) {
	for _, in := range []string{"172.18.0.1/24", "172.18.0.1/29", "172.18.0.1/31", "172.18.0.1/32"} {
		if _, err := DeriveTunDNS(in); err == nil {
			t.Errorf("%q: expected error for non-/30", in)
		}
	}
}

func TestDeriveTunDNS_RejectsNetworkAndBroadcast(t *testing.T) {
	if _, err := DeriveTunDNS("172.18.0.0/30"); err == nil {
		t.Error("network address should be rejected")
	}
	if _, err := DeriveTunDNS("172.18.0.3/30"); err == nil {
		t.Error("broadcast address should be rejected")
	}
}

func TestDeriveTunDNS_RejectsIPv6(t *testing.T) {
	if _, err := DeriveTunDNS("fdfe:dcba:9876::1/126"); err == nil {
		t.Error("IPv6 should be rejected")
	}
}

func TestDeriveTunDNS_RejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "garbage", "172.18.0.1", "172.18.0.999/30", "172.18.0.1/33"} {
		if _, err := DeriveTunDNS(in); err == nil {
			t.Errorf("%q: expected error for malformed input", in)
		}
	}
}

// --- 1B.3: fakeip cache_file invalidation on pool change ---

func TestFakeIPCacheNeedsReset_IdenticalRanges(t *testing.T) {
	// Exact equal, both families.
	if FakeIPCacheNeedsReset("10.128.0.0/10", "3f80::/10", "10.128.0.0/10", "3f80::/10") {
		t.Error("identical ranges must not need reset")
	}
	// Cosmetically different but equal after masking → still no reset.
	if FakeIPCacheNeedsReset("10.128.0.5/10", "", "10.128.0.0/10", "") {
		t.Error("non-normalized-but-equal v4 ranges must not need reset")
	}
}

func TestFakeIPCacheNeedsReset_ChangedV4(t *testing.T) {
	if !FakeIPCacheNeedsReset("10.128.0.0/10", "3f80::/10", "10.64.0.0/10", "3f80::/10") {
		t.Error("changed v4 range must need reset")
	}
}

func TestFakeIPCacheNeedsReset_ChangedV6(t *testing.T) {
	if !FakeIPCacheNeedsReset("10.128.0.0/10", "3f80::/10", "10.128.0.0/10", "fc00::/10") {
		t.Error("changed v6 range must need reset")
	}
}

func TestFakeIPCacheNeedsReset_BothEmpty(t *testing.T) {
	if FakeIPCacheNeedsReset("", "", "", "") {
		t.Error("both-empty must not need reset")
	}
}

func TestFakeIPCacheNeedsReset_FirstProvision(t *testing.T) {
	// Empty stored, configured set → force a clean cache.
	if !FakeIPCacheNeedsReset("", "", "10.128.0.0/10", "") {
		t.Error("first-provision (stored empty, configured set) must need reset")
	}
	if !FakeIPCacheNeedsReset("", "", "", "3f80::/10") {
		t.Error("first-provision v6 must need reset")
	}
}

func TestFakeIPCacheNeedsReset_MalformedFallsBackToStringCompare(t *testing.T) {
	// Both unparseable but byte-equal → string compare says equal → no reset.
	if FakeIPCacheNeedsReset("garbage", "", "garbage", "") {
		t.Error("equal malformed v4 should compare equal (no reset)")
	}
	// Unparseable and different → string compare says differ → reset; no panic.
	if !FakeIPCacheNeedsReset("garbage", "", "other", "") {
		t.Error("differing malformed v4 should need reset")
	}
	// One side malformed, one parseable, trimmed-unequal → reset; no panic.
	if !FakeIPCacheNeedsReset("not-a-cidr", "", "10.128.0.0/10", "") {
		t.Error("malformed-vs-valid should need reset without panic")
	}
}

func TestFakeIPCacheNeedsReset_WhitespaceTrimmed(t *testing.T) {
	// Whitespace-padded but equal → no reset (parse path trims via netip; fallback trims too).
	if FakeIPCacheNeedsReset("  10.128.0.0/10  ", "", "10.128.0.0/10", "") {
		t.Error("whitespace-padded equal range must not need reset")
	}
}

func TestResetFakeIPCache_RemovesExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := ResetFakeIPCache(path); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file must be gone after reset, stat err=%v", err)
	}
}

func TestResetFakeIPCache_MissingFileIsNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.db")
	if err := ResetFakeIPCache(path); err != nil {
		t.Errorf("removing a missing file must be a no-op, got %v", err)
	}
}

func TestResetFakeIPCache_EmptyPathIsNoError(t *testing.T) {
	// Empty path resolves to a non-existent file → treated as already-absent.
	if err := ResetFakeIPCache(""); err != nil {
		t.Errorf("empty path must be a no-op, got %v", err)
	}
}

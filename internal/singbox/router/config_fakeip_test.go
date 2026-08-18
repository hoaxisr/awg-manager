package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// TestEnsureFakeIPOverlay_StackAndGSO covers the stack + GSO safety matrix on
// the LIVE overlay path: empty Stack defaults to "gvisor" and emits NO gso flag
// (nil), while "system" REQUIRES an explicit gso:false — the system stack with
// GSO panics sing-tun on this router's 4.9 kernel. The explicit false must also
// survive JSON marshaling (omitempty on a plain bool would drop it and sing-box
// would apply its own non-false default).
func TestEnsureFakeIPOverlay_StackAndGSO(t *testing.T) {
	base := FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
	}
	cases := []struct {
		name    string
		stack   string
		wantStk string
		wantGSO *bool // nil = field omitted
	}{
		{"empty defaults to gvisor, no gso", "", "gvisor", nil},
		{"gvisor emits no gso", "gvisor", "gvisor", nil},
		{"system forces gso false", "system", "system", boolPtr(false)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := base
			spec.Stack = tc.stack
			cfg := NewEmptyConfig()
			ensureFakeIPOverlay(cfg, spec)
			in := findInbound(cfg, "tun-in")
			if in.Stack != tc.wantStk {
				t.Errorf("Stack = %q, want %q", in.Stack, tc.wantStk)
			}
			switch {
			case tc.wantGSO == nil && in.GSO != nil:
				t.Errorf("GSO = %v, want nil (omitted)", *in.GSO)
			case tc.wantGSO != nil && in.GSO == nil:
				t.Errorf("GSO = nil, want %v", *tc.wantGSO)
			case tc.wantGSO != nil && *in.GSO != *tc.wantGSO:
				t.Errorf("GSO = %v, want %v", *in.GSO, *tc.wantGSO)
			}
		})
	}

	spec := base
	spec.Stack = "system"
	cfg := NewEmptyConfig()
	ensureFakeIPOverlay(cfg, spec)
	raw, err := json.Marshal(findInbound(cfg, "tun-in"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"gso":false`) {
		t.Errorf(`marshaled tun inbound must carry "gso":false, got: %s`, raw)
	}
}

// TestEnsureFakeIPOverlay_OmitV6 verifies the v6 fields are omitted on the LIVE
// overlay path when the spec leaves them empty: a single tun address and no
// inet6_range on the fakeip pool.
func TestEnsureFakeIPOverlay_OmitV6(t *testing.T) {
	cfg := NewEmptyConfig()
	ensureFakeIPOverlay(cfg, FakeIPTunSpec{
		Iface: "opkgtun10", TunAddr4: "172.18.0.1/30", MTU: 1500,
		Inet4Range: "10.128.0.0/10", CachePath: "/c.db", RealServer: "1.1.1.1",
	})
	in := findInbound(cfg, "tun-in")
	if len(in.Address) != 1 || in.Address[0] != "172.18.0.1/30" {
		t.Errorf("address should be v4-only: %#v", in.Address)
	}
	for _, sv := range cfg.DNS.Servers {
		if sv.Type == "fakeip" && sv.Inet6Range != "" {
			t.Errorf("inet6_range should be empty: %q", sv.Inet6Range)
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

// --- ensureFakeIPOverlay ---

func TestEnsureFakeIPOverlay_HijackDNSForcedFirst(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{{Action: "route", Outbound: "awg-awg10"}}
	ensureFakeIPOverlay(cfg, FakeIPTunSpec{Iface: "opkgtun0", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"})
	if cfg.Route.Rules[0].Action != "hijack-dns" {
		t.Fatalf("hijack-dns must be route.rules[0], got %+v", cfg.Route.Rules)
	}
}

func TestEnsureFakeIPOverlay_Idempotent(t *testing.T) {
	cfg := NewEmptyConfig()
	spec := FakeIPTunSpec{Iface: "opkgtun0", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"}
	ensureFakeIPOverlay(cfg, spec)
	ensureFakeIPOverlay(cfg, spec)
	tunCount, fakeipCount, hijackCount := 0, 0, 0
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "tun-in" {
			tunCount++
		}
	}
	for _, sv := range cfg.DNS.Servers {
		if sv.Type == "fakeip" {
			fakeipCount++
		}
	}
	for _, r := range cfg.Route.Rules {
		if r.Action == "hijack-dns" {
			hijackCount++
		}
	}
	if tunCount != 1 || fakeipCount != 1 || hijackCount != 1 {
		t.Fatalf("overlay not idempotent: tun=%d fakeip=%d hijack=%d", tunCount, fakeipCount, hijackCount)
	}
}

func TestEnsureFakeIPOverlay_RefreshesTunName(t *testing.T) {
	cfg := NewEmptyConfig()
	ensureFakeIPOverlay(cfg, FakeIPTunSpec{Iface: "opkgtun0", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"})
	ensureFakeIPOverlay(cfg, FakeIPTunSpec{Iface: "opkgtun3", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"})
	for _, ib := range cfg.Inbounds {
		if ib.Tag == "tun-in" && ib.InterfaceName != "opkgtun3" {
			t.Fatalf("tun name not refreshed: %s", ib.InterfaceName)
		}
	}
}

func TestEnsureFakeIPOverlay_ScalarLockedBits(t *testing.T) {
	cfg := NewEmptyConfig()
	// Seed a user DNS rule that must survive the overlay.
	cfg.DNS.Rules = append(cfg.DNS.Rules, DNSRule{Action: "route", Server: "fakeip", QueryType: []string{"A", "AAAA"}})
	spec := FakeIPTunSpec{Iface: "opkgtun0", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"}
	ensureFakeIPOverlay(cfg, spec)

	if cfg.DNS.Final != "real" {
		t.Errorf("DNS.Final must be \"real\", got %q", cfg.DNS.Final)
	}
	if cfg.Route.DefaultDomainResolver == nil || cfg.Route.DefaultDomainResolver.Server != "real" {
		t.Errorf("DefaultDomainResolver must be {server:real}, got %+v", cfg.Route.DefaultDomainResolver)
	}
	if cfg.Experimental == nil || cfg.Experimental.CacheFile == nil {
		t.Fatal("Experimental.CacheFile must be set")
	}
	if cfg.Experimental.CacheFile.Path != "/x" {
		t.Errorf("CacheFile.Path must be /x, got %q", cfg.Experimental.CacheFile.Path)
	}
	if !cfg.Experimental.CacheFile.Enabled || !cfg.Experimental.CacheFile.StoreFakeIP {
		t.Errorf("CacheFile must have Enabled+StoreFakeIP true: %+v", cfg.Experimental.CacheFile)
	}
	// The pre-existing user DNS rule must still be present.
	found := false
	for _, r := range cfg.DNS.Rules {
		if r.Action == "route" && r.Server == "fakeip" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("pre-existing DNS rule was lost after overlay; rules: %+v", cfg.DNS.Rules)
	}
}

// TestEnsureFakeIPOverlay_PrivateBypassAfterHijack verifies that the overlay
// inserts an ip_is_private→direct rule at route.rules[1] (right after hijack-dns).
func TestEnsureFakeIPOverlay_PrivateBypassAfterHijack(t *testing.T) {
	spec := FakeIPTunSpec{Iface: "opkgtun0", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"}

	t.Run("added at index 1", func(t *testing.T) {
		cfg := NewEmptyConfig()
		cfg.Route.Rules = []Rule{{Action: "route", Outbound: "proxy"}}
		ensureFakeIPOverlay(cfg, spec)
		if len(cfg.Route.Rules) < 2 {
			t.Fatalf("expected at least 2 route rules, got %d", len(cfg.Route.Rules))
		}
		if cfg.Route.Rules[0].Action != "hijack-dns" {
			t.Errorf("route.rules[0] must be hijack-dns, got %+v", cfg.Route.Rules[0])
		}
		r1 := cfg.Route.Rules[1]
		if r1.IPIsPrivate == nil || !*r1.IPIsPrivate || r1.Outbound != "direct" {
			t.Errorf("route.rules[1] must be {ip_is_private:true, outbound:direct}, got %+v", r1)
		}
	})

	t.Run("idempotent: double overlay = exactly one private rule", func(t *testing.T) {
		cfg := NewEmptyConfig()
		ensureFakeIPOverlay(cfg, spec)
		ensureFakeIPOverlay(cfg, spec)
		count := 0
		for _, r := range cfg.Route.Rules {
			if r.IPIsPrivate != nil && *r.IPIsPrivate {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 ip_is_private rule after double overlay, got %d; rules: %+v", count, cfg.Route.Rules)
		}
	})

	t.Run("user already has ip_is_private rule: no duplicate", func(t *testing.T) {
		cfg := NewEmptyConfig()
		trueVal := true
		cfg.Route.Rules = []Rule{
			{IPIsPrivate: &trueVal, Outbound: "custom-direct"},
		}
		ensureFakeIPOverlay(cfg, spec)
		count := 0
		for _, r := range cfg.Route.Rules {
			if r.IPIsPrivate != nil && *r.IPIsPrivate {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected exactly 1 ip_is_private rule when user already has one, got %d; rules: %+v", count, cfg.Route.Rules)
		}
	})
}

// TestEnsureFakeIPOverlay_FakeIPRuleRestrictedToAAAA verifies that the overlay
// normalizes DNS rules pointing to "fakeip" to query_type=["A","AAAA"].
func TestEnsureFakeIPOverlay_FakeIPRuleRestrictedToAAAA(t *testing.T) {
	spec := FakeIPTunSpec{Iface: "opkgtun0", Inet4Range: "10.128.0.0/10", TunAddr4: "172.18.0.1/30", RealServer: "1.1.1.1", CachePath: "/x"}

	t.Run("no query_type → set to [A,AAAA]", func(t *testing.T) {
		cfg := NewEmptyConfig()
		cfg.DNS.Rules = []DNSRule{{Action: "route", Server: "fakeip", Domain: []string{"x.com"}}}
		ensureFakeIPOverlay(cfg, spec)
		var found *DNSRule
		for i := range cfg.DNS.Rules {
			if cfg.DNS.Rules[i].Server == "fakeip" && len(cfg.DNS.Rules[i].Domain) > 0 {
				found = &cfg.DNS.Rules[i]
				break
			}
		}
		if found == nil {
			t.Fatal("fakeip DNS rule not found after overlay")
		}
		if len(found.QueryType) != 2 || found.QueryType[0] != "A" || found.QueryType[1] != "AAAA" {
			t.Errorf("QueryType must be [A,AAAA], got %v", found.QueryType)
		}
	})

	t.Run("query_type with HTTPS → narrowed to [A,AAAA]", func(t *testing.T) {
		cfg := NewEmptyConfig()
		cfg.DNS.Rules = []DNSRule{{Action: "route", Server: "fakeip", QueryType: []string{"A", "AAAA", "HTTPS"}}}
		ensureFakeIPOverlay(cfg, spec)
		r := cfg.DNS.Rules[0]
		if len(r.QueryType) != 2 || r.QueryType[0] != "A" || r.QueryType[1] != "AAAA" {
			t.Errorf("QueryType must be narrowed to [A,AAAA], got %v", r.QueryType)
		}
	})

	t.Run("non-fakeip rule: query_type untouched", func(t *testing.T) {
		cfg := NewEmptyConfig()
		cfg.DNS.Rules = []DNSRule{{Action: "route", Server: "real", QueryType: []string{"HTTPS"}}}
		ensureFakeIPOverlay(cfg, spec)
		r := cfg.DNS.Rules[0]
		if len(r.QueryType) != 1 || r.QueryType[0] != "HTTPS" {
			t.Errorf("non-fakeip rule query_type must be untouched, got %v", r.QueryType)
		}
	})
}

// ---------------------------------------------------------------------------
// fakeipWithConfig / loadFakeIPConfig / persistFakeIPConfig CRUD tests
// ---------------------------------------------------------------------------

// newFakeIPTestService builds an orch-wired ServiceImpl with SlotFakeIP
// registered and enabled, FakeIPTun wired with a non-empty CachePath, and
// settings seeded with FakeIPState{Provisioned:true,Index:0}.
func newFakeIPTestService(t *testing.T) (*ServiceImpl, string) {
	t.Helper()
	dir := t.TempDir()

	orch := orchestrator.New(dir, nil)
	if err := orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotRouter,
		Filename: "20-router.json",
	}); err != nil {
		t.Fatalf("orch.Register SlotRouter: %v", err)
	}
	if err := orch.Register(orchestrator.SlotMeta{
		Slot:     orchestrator.SlotFakeIP,
		Filename: "21-fakeip.json",
	}); err != nil {
		t.Fatalf("orch.Register SlotFakeIP: %v", err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("orch.Bootstrap: %v", err)
	}
	// Enable SlotFakeIP so Save targets active path, not disabled/.
	if err := orch.SetEnabled(orchestrator.SlotFakeIP, true); err != nil {
		t.Fatalf("orch.SetEnabled SlotFakeIP: %v", err)
	}

	settingsStore := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode: "fakeip-tun",
	})
	// Seed FakeIPState so ensureFakeIPOverlayFromState can resolve the iface.
	all, err := settingsStore.Load()
	if err != nil {
		t.Fatalf("settingsStore.Load: %v", err)
	}
	all.FakeIP = &storage.FakeIPState{Provisioned: true, Index: 0}
	if err := settingsStore.Save(all); err != nil {
		t.Fatalf("settingsStore.Save: %v", err)
	}

	params := DefaultFakeIPTunParams()
	params.CachePath = "/tmp/fakeip-test.db"

	svc := &ServiceImpl{
		deps: Deps{
			Settings:  settingsStore,
			Singbox:   &fakeSingbox{dir: dir},
			Orch:      orch,
			FakeIPTun: params,
		},
	}
	return svc, dir
}

// TestFakeipWithConfig_OverlayAndPersist is the TDD target for fakeipWithConfig.
// It:
//   - Calls fakeipWithConfig with a user mutation (add a DNS rule).
//   - Re-loads via loadFakeIPConfig and asserts the user rule survived.
//   - Asserts the engine-locked overlay bits are present (hijack-dns first rule,
//     fakeip DNS server, DNS.Final=="real").
//   - Asserts the file landed at the active path (21-fakeip.json), not pending/.
func TestFakeipWithConfig_OverlayAndPersist(t *testing.T) {
	svc, dir := newFakeIPTestService(t)
	ctx := context.Background()

	err := svc.fakeipWithConfig(ctx, "all", func(cfg *RouterConfig) error {
		cfg.DNS.Rules = append(cfg.DNS.Rules, DNSRule{
			Action:    "route",
			Server:    "fakeip",
			QueryType: []string{"A"},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("fakeipWithConfig: %v", err)
	}

	// Re-load to confirm persistence.
	loaded, err := svc.loadFakeIPConfig()
	if err != nil {
		t.Fatalf("loadFakeIPConfig: %v", err)
	}

	// User DNS rule survived.
	foundUserRule := false
	for _, r := range loaded.DNS.Rules {
		if r.Action == "route" && r.Server == "fakeip" && len(r.QueryType) == 1 && r.QueryType[0] == "A" {
			foundUserRule = true
			break
		}
	}
	if !foundUserRule {
		t.Errorf("user DNS rule not found after reload; dns.rules: %+v", loaded.DNS.Rules)
	}

	// Overlay locked bits: hijack-dns at route.rules[0].
	if len(loaded.Route.Rules) == 0 || loaded.Route.Rules[0].Action != "hijack-dns" {
		t.Errorf("route.rules[0] must be hijack-dns; got: %+v", loaded.Route.Rules)
	}

	// Overlay: fakeip DNS server present.
	foundFakeIPServer := false
	for _, sv := range loaded.DNS.Servers {
		if sv.Type == "fakeip" {
			foundFakeIPServer = true
			break
		}
	}
	if !foundFakeIPServer {
		t.Errorf("fakeip DNS server not found after overlay; servers: %+v", loaded.DNS.Servers)
	}

	// Overlay: DNS.Final == "real".
	if loaded.DNS.Final != "real" {
		t.Errorf(`DNS.Final must be "real", got %q`, loaded.DNS.Final)
	}

	// File landed at active path, not pending/.
	activePath := filepath.Join(dir, "21-fakeip.json")
	if _, err := os.Stat(activePath); err != nil {
		t.Errorf("21-fakeip.json must exist at active path %s: %v", activePath, err)
	}
	pendingPath := filepath.Join(dir, "pending", "21-fakeip.json")
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Errorf("21-fakeip.json must NOT be in pending/; stat err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// guardFakeIPLocked / ErrFakeIPLockedField tests
// ---------------------------------------------------------------------------

// seedFakeIPLocked runs a no-op fakeipWithConfig to establish the locked bits
// in the slot (first provision writes fakeip/real servers, DNS.Final, etc.).
func seedFakeIPLocked(t *testing.T, svc *ServiceImpl) {
	t.Helper()
	if err := svc.fakeipWithConfig(context.Background(), "seed", func(*RouterConfig) error { return nil }); err != nil {
		t.Fatalf("seed fakeipWithConfig: %v", err)
	}
}

// TestFakeipGuard_RejectsDeletingRealServer verifies that an edit removing the
// "real" DNS server from an already-provisioned fakeip config is rejected with
// ErrFakeIPLockedField.
func TestFakeipGuard_RejectsDeletingRealServer(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	seedFakeIPLocked(t, svc)

	err := svc.fakeipWithConfig(context.Background(), "all", func(c *RouterConfig) error {
		out := c.DNS.Servers[:0]
		for _, sv := range c.DNS.Servers {
			if sv.Tag != "real" {
				out = append(out, sv)
			}
		}
		c.DNS.Servers = out
		return nil
	})
	if !errors.Is(err, ErrFakeIPLockedField) {
		t.Fatalf("expected ErrFakeIPLockedField, got %v", err)
	}
	if err != nil && !strings.Contains(err.Error(), "real") {
		t.Errorf("error message should mention \"real\", got: %v", err)
	}
}

// TestFakeipGuard_RejectsChangingDNSFinal verifies that an edit changing
// DNS.Final away from "real" on an established fakeip config is rejected with
// ErrFakeIPLockedField.
func TestFakeipGuard_RejectsChangingDNSFinal(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	seedFakeIPLocked(t, svc)

	err := svc.fakeipWithConfig(context.Background(), "all", func(c *RouterConfig) error {
		c.DNS.Final = "fakeip"
		return nil
	})
	if !errors.Is(err, ErrFakeIPLockedField) {
		t.Fatalf("expected ErrFakeIPLockedField, got %v", err)
	}
}

// TestFakeipGuard_AllowsAppendingUserDNSRule verifies that a legitimate edit
// (appending a user DNS rule that touches no locked bit) is NOT rejected.
// This guards against over-rejection by guardFakeIPLocked.
func TestFakeipGuard_AllowsAppendingUserDNSRule(t *testing.T) {
	svc, _ := newFakeIPTestService(t)
	seedFakeIPLocked(t, svc)

	err := svc.fakeipWithConfig(context.Background(), "all", func(c *RouterConfig) error {
		c.DNS.Rules = append(c.DNS.Rules, DNSRule{
			Action: "route",
			Server: "real",
			Domain: []string{"internal.corp"},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("appending a user DNS rule must not be rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetStatus slot-fork: fakeip-tun mode must read SlotFakeIP, not SlotRouter
// ---------------------------------------------------------------------------

// TestGetStatus_FakeIPMode_ReadsFakeIPSlot seeds SlotRouter with 0 rule_sets
// and final="router-final", and SlotFakeIP with 2 rule_sets and
// final="fakeip-final", then asserts that GetStatus in fakeip-tun mode
// returns the SlotFakeIP counts (RuleSetCount==2, Final=="fakeip-final").
func TestGetStatus_FakeIPMode_ReadsFakeIPSlot(t *testing.T) {
	svc, dir := newFakeIPTestService(t)

	// Wire IPTables stub so Probe() doesn't panic (errProbeIPTables pattern).
	svc.deps.IPTables = &IPTables{
		runIPTables:    func(context.Context, ...string) error { return errors.New("no chain") },
		runIPTablesOut: func(context.Context, ...string) (string, error) { return "", errors.New("no chain") },
	}

	// Stub fakeip-tun seams so GetStatus.Active path doesn't panic.
	stubTunReadyProbe(t, func(string) bool { return false })
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return false })

	// SlotRouter: 0 rule_sets, final="router-final".
	routerJSON := `{"route":{"rules":[],"rule_set":[],"final":"router-final"}}`
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), []byte(routerJSON), 0644); err != nil {
		t.Fatalf("write SlotRouter: %v", err)
	}

	// SlotFakeIP: 2 rule_sets, final="fakeip-final".
	fakeipJSON := `{"route":{"rules":[],"rule_set":[{"tag":"rs1","type":"remote","format":"binary","url":"https://example.com/1.srs"},{"tag":"rs2","type":"remote","format":"binary","url":"https://example.com/2.srs"}],"final":"fakeip-final"}}`
	if err := os.WriteFile(filepath.Join(dir, "21-fakeip.json"), []byte(fakeipJSON), 0644); err != nil {
		t.Fatalf("write SlotFakeIP: %v", err)
	}

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.RuleSetCount != 2 {
		t.Errorf("RuleSetCount = %d, want 2 (must read SlotFakeIP, not SlotRouter)", st.RuleSetCount)
	}
	if st.Final != "fakeip-final" {
		t.Errorf("Final = %q, want %q (must read SlotFakeIP, not SlotRouter)", st.Final, "fakeip-final")
	}
}

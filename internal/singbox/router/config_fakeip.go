package router

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
)

// FakeIPTunSpec is the input to BuildFakeIPTunConfig — every value the
// fakeip-tun mode needs that the builder cannot derive on its own. It carries
// the kernel tun device, the fakeip pools, the real upstream resolver and the
// already-built outbound list (proxy + direct).
type FakeIPTunSpec struct {
	Iface          string     // kernel iface name, e.g. "opkgtun10"
	TunAddr4       string     // e.g. "172.18.0.1/30"
	TunAddr6       string     // e.g. "fdfe:dcba:9876::1/126" (empty to omit v6)
	MTU            int        //
	Inet4Range     string     // fakeip v4 pool
	Inet6Range     string     // fakeip v6 pool (empty to omit v6)
	CachePath      string     //
	RealServer     string     // real upstream resolver, e.g. "1.1.1.1"
	Outbounds      []Outbound // proxy + direct
	ProxyTag       string     // outbound tag that tun-in routes to
	DomainRuleSets []string   // .srs tags for domains to fakeip (empty = fake all A/AAAA)
	SourceIPCIDR   []string   // optional per-device targeting (empty = all sources)
	// Stack selects the sing-tun stack: "gvisor" (default; empty → gvisor) or
	// "system". When "system" the builder forces gso:false on the tun inbound —
	// the only stable system-stack combo on this router's kernel (4.9), where the
	// system stack with GSO panics sing-tun under load (PoC-proven 2026-06-13).
	Stack string
}

// boolPtr returns a pointer to v. The tun inbound's auto_route / auto_redirect /
// strict_route / endpoint_independent_nat fields are *bool so that an explicit
// false survives JSON marshaling (omitempty on a plain bool would drop it, and
// sing-box would then apply its own non-false defaults — e.g. auto_route true,
// which is exactly what fakeip-tun must NOT enable because NDMS owns routing).
func boolPtr(v bool) *bool { return &v }

// BuildFakeIPTunConfig assembles a complete RouterConfig for sing-box's
// fakeip-tun mode from spec.
//
// Shape:
//   - tun inbound "tun-in" on s.Iface, gvisor stack, every auto-* flag forced
//     false (NDMS owns routing/redirect; fakeip-tun must not let sing-box touch
//     the kernel routing table).
//   - outbounds: s.Outbounds verbatim, after applying the domain_resolver guard
//     (see applyOutboundDomainResolver) on a defensive copy.
//   - DNS: a "fakeip" server (the pool) plus a "real" server (true upstream),
//     final → "real". A single route rule sends A/AAAA queries to "fakeip",
//     optionally narrowed by rule_set (domains) and/or source_ip_cidr (devices).
//   - route: hijack-dns first, then everything to the proxy tag; outbound
//     hostnames resolve via "real" (default_domain_resolver) so the proxy
//     endpoint never gets a fake address.
//   - experimental.cache_file persists the fakeip name↔address map across
//     restarts so existing connections keep their address.
func BuildFakeIPTunConfig(s FakeIPTunSpec) (*RouterConfig, error) {
	if p, err := netip.ParsePrefix(s.TunAddr4); err != nil {
		return nil, fmt.Errorf("fakeip-tun: invalid TunAddr4 %q: %w", s.TunAddr4, err)
	} else if !p.Addr().Is4() {
		return nil, fmt.Errorf("fakeip-tun: TunAddr4 %q is not IPv4", s.TunAddr4)
	}
	if s.TunAddr6 != "" {
		if p, err := netip.ParsePrefix(s.TunAddr6); err != nil {
			return nil, fmt.Errorf("fakeip-tun: invalid TunAddr6 %q: %w", s.TunAddr6, err)
		} else if p.Addr().Is4() {
			return nil, fmt.Errorf("fakeip-tun: TunAddr6 %q is not IPv6", s.TunAddr6)
		}
	}

	cfg := NewEmptyConfig()

	addrs := []string{s.TunAddr4}
	if s.TunAddr6 != "" {
		addrs = append(addrs, s.TunAddr6)
	}
	// Stack: empty defaults to gvisor (robust, no gso flag). system REQUIRES
	// gso:false on this router's kernel (4.9) — the system stack with GSO panics
	// sing-tun under load (PoC-proven 2026-06-13). gvisor needs no gso flag, so
	// GSO stays nil (omitted) and only system emits the explicit false.
	stack := s.Stack
	if stack == "" {
		stack = "gvisor"
	}
	in := Inbound{
		Type:                   "tun",
		Tag:                    "tun-in",
		InterfaceName:          s.Iface,
		Address:                addrs,
		MTU:                    s.MTU,
		AutoRoute:              boolPtr(false),
		AutoRedirect:           boolPtr(false),
		StrictRoute:            boolPtr(false),
		Stack:                  stack,
		EndpointIndependentNAT: boolPtr(false),
	}
	if stack == "system" {
		in.GSO = boolPtr(false)
	}
	cfg.Inbounds = []Inbound{in}

	// Full outbound pipeline: strip auto-managed direct outbounds (awg/nwg/
	// wireguard bind_interface) — they live in 15-awg.json and are merged by
	// sing-box across config.d, so re-emitting them here would FATAL the merged
	// config with "duplicate outbound tag" (stand-verified 2026-06-15). ProxyTag
	// may reference one of them by tag; sing-box resolves it from 15-awg.json.
	// Then apply the domain_resolver guard on the survivors. Mirrors the tproxy
	// path (service.go: stripAutoManagedDirect).
	cfg.Outbounds = applyOutboundDomainResolver(stripAutoManagedDirect(s.Outbounds), "real")

	fakeip := DNSServer{
		Tag:        "fakeip",
		Type:       "fakeip",
		Inet4Range: s.Inet4Range,
	}
	if s.Inet6Range != "" {
		fakeip.Inet6Range = s.Inet6Range
	}
	if err := cfg.AddDNSServer(fakeip); err != nil {
		return nil, err
	}
	if err := cfg.AddDNSServer(DNSServer{Tag: "real", Type: "udp", Server: s.RealServer}); err != nil {
		return nil, err
	}
	cfg.DNS.Final = "real"

	rule := DNSRule{
		Action:    "route",
		Server:    "fakeip",
		QueryType: []string{"A", "AAAA"},
	}
	if len(s.DomainRuleSets) > 0 {
		rule.RuleSet = s.DomainRuleSets
	}
	if len(s.SourceIPCIDR) > 0 {
		rule.SourceIPCIDR = s.SourceIPCIDR
	}
	if err := cfg.AddDNSRule(rule); err != nil {
		return nil, err
	}

	cfg.Route.Rules = []Rule{
		{Action: "hijack-dns", Protocol: "dns"},
		{Action: "route", Outbound: s.ProxyTag},
	}
	cfg.Route.Final = s.ProxyTag
	cfg.Route.DefaultDomainResolver = &DomainResolver{Server: "real"}

	cfg.Experimental = &Experimental{CacheFile: &CacheFile{
		Enabled:     true,
		StoreFakeIP: true,
		Path:        s.CachePath,
	}}

	return cfg, nil
}

// ensureFakeIPOverlay injects/normalizes the ENGINE-LOCKED bits of fakeip-tun
// mode into a user-edited *RouterConfig. It is idempotent: calling it multiple
// times with the same spec produces exactly one of each locked element.
//
// Locked bits (keyed as noted):
//   - tun-in Inbound (upserted by Tag=="tun-in") — all boolPtr flags, stack,
//     address list from spec, InterfaceName ALWAYS refreshed to spec.Iface.
//   - fakeip DNSServer (upserted by Type=="fakeip") — pool ranges from spec.
//   - real DNSServer (upserted by Tag=="real") — upstream from spec.RealServer.
//   - cfg.DNS.Final = "real".
//   - cfg.Route.DefaultDomainResolver = &DomainResolver{Server:"real"}.
//   - cfg.Experimental = &Experimental{CacheFile:{Enabled,StoreFakeIP,Path}}.
//   - hijack-dns Rule forced at route.rules[0] (all existing hijack-dns rules
//     removed, then one prepended), preserving all other user rules in order.
//
// The caller is responsible for validating spec fields (TunAddr4 etc.) before
// calling. ensureFakeIPOverlay is a pure in-place mutator; it does not validate.
func ensureFakeIPOverlay(cfg *RouterConfig, spec FakeIPTunSpec) {
	// --- tun-in inbound ---
	addrs := []string{spec.TunAddr4}
	if spec.TunAddr6 != "" {
		addrs = append(addrs, spec.TunAddr6)
	}
	stack := spec.Stack
	if stack == "" {
		stack = "gvisor"
	}
	in := Inbound{
		Type:                   "tun",
		Tag:                    "tun-in",
		InterfaceName:          spec.Iface,
		Address:                addrs,
		MTU:                    spec.MTU,
		AutoRoute:              boolPtr(false),
		AutoRedirect:           boolPtr(false),
		StrictRoute:            boolPtr(false),
		Stack:                  stack,
		EndpointIndependentNAT: boolPtr(false),
	}
	if stack == "system" {
		in.GSO = boolPtr(false)
	}
	upsertInbound(cfg, in)

	// --- fakeip DNS server ---
	fakeip := DNSServer{
		Tag:        "fakeip",
		Type:       "fakeip",
		Inet4Range: spec.Inet4Range,
	}
	if spec.Inet6Range != "" {
		fakeip.Inet6Range = spec.Inet6Range
	}
	upsertDNSServerByType(cfg, fakeip)

	// --- real DNS server ---
	upsertDNSServerByTag(cfg, DNSServer{Tag: "real", Type: "udp", Server: spec.RealServer})

	// --- scalar locked bits ---
	cfg.DNS.Final = "real"
	cfg.Route.DefaultDomainResolver = &DomainResolver{Server: "real"}
	cfg.Experimental = &Experimental{CacheFile: &CacheFile{
		Enabled:     true,
		StoreFakeIP: true,
		Path:        spec.CachePath,
	}}

	// --- hijack-dns forced at index 0 ---
	forceHijackFirst(cfg)

	// --- ip_is_private→direct at index 1 (mirrors tproxy EnsureSystemRules) ---
	forcePrivateBypassAfterHijack(cfg)

	// --- fakeip DNS rules: restrict query_type to A/AAAA ---
	normalizeFakeIPDNSRules(cfg)
}

// upsertInbound replaces the first Inbound with the same Tag in-place, or
// appends if none exists. Inbound is a value type; the slice element is
// replaced by index to avoid aliasing.
func upsertInbound(cfg *RouterConfig, in Inbound) {
	for i, existing := range cfg.Inbounds {
		if existing.Tag == in.Tag {
			cfg.Inbounds[i] = in
			return
		}
	}
	cfg.Inbounds = append(cfg.Inbounds, in)
}

// upsertDNSServerByTag replaces the first DNSServer with the same Tag
// in-place, or appends if none exists.
func upsertDNSServerByTag(cfg *RouterConfig, sv DNSServer) {
	for i, existing := range cfg.DNS.Servers {
		if existing.Tag == sv.Tag {
			cfg.DNS.Servers[i] = sv
			return
		}
	}
	cfg.DNS.Servers = append(cfg.DNS.Servers, sv)
}

// upsertDNSServerByType replaces the first DNSServer with the same Type
// in-place, or appends if none exists. Used for "fakeip" (there is only
// ever one fakeip server).
func upsertDNSServerByType(cfg *RouterConfig, sv DNSServer) {
	for i, existing := range cfg.DNS.Servers {
		if existing.Type == sv.Type {
			cfg.DNS.Servers[i] = sv
			return
		}
	}
	cfg.DNS.Servers = append(cfg.DNS.Servers, sv)
}

// forceHijackFirst removes every Rule with Action=="hijack-dns" from
// cfg.Route.Rules and prepends exactly one {Action:"hijack-dns",
// Protocol:"dns"} at index 0. All other rules are preserved in order.
func forceHijackFirst(cfg *RouterConfig) {
	filtered := cfg.Route.Rules[:0]
	for _, r := range cfg.Route.Rules {
		if r.Action != "hijack-dns" {
			filtered = append(filtered, r)
		}
	}
	hijack := Rule{Action: "hijack-dns", Protocol: "dns"}
	cfg.Route.Rules = append([]Rule{hijack}, filtered...)
}

// forcePrivateBypassAfterHijack ensures exactly one {ip_is_private:true,
// outbound:"direct"} rule exists in cfg.Route.Rules right after the
// hijack-dns rule (at index 1). Idempotent: if any ip_is_private rule
// already exists anywhere in route.rules, nothing is added. Mirrors the
// private-bypass insertion in EnsureSystemRules (config.go).
func forcePrivateBypassAfterHijack(cfg *RouterConfig) {
	for _, r := range cfg.Route.Rules {
		if r.IPIsPrivate != nil && *r.IPIsPrivate {
			return // already present — don't duplicate
		}
	}
	// Find the hijack-dns rule position (forceHijackFirst already ran, so it
	// is at index 0 in the normal case, but we scan defensively).
	insertPos := 1
	for i, r := range cfg.Route.Rules {
		if r.Action == "hijack-dns" {
			insertPos = i + 1
			break
		}
	}
	truePtr := true
	privateRule := Rule{IPIsPrivate: &truePtr, Outbound: "direct"}
	newRules := make([]Rule, 0, len(cfg.Route.Rules)+1)
	newRules = append(newRules, cfg.Route.Rules[:insertPos]...)
	newRules = append(newRules, privateRule)
	newRules = append(newRules, cfg.Route.Rules[insertPos:]...)
	cfg.Route.Rules = newRules
}

// normalizeFakeIPDNSRules sets QueryType to ["A","AAAA"] on every DNS rule
// whose Server is "fakeip". When QueryType is empty it is set; when it
// contains entries outside {A,AAAA} those are filtered out. This enforces
// the engine invariant that fakeip only handles IP-type queries — HTTPS and
// other record types cause sing-box to log "only IP queries are supported by
// fakeip" and must fall through to dns.final="real" instead.
func normalizeFakeIPDNSRules(cfg *RouterConfig) {
	for i, r := range cfg.DNS.Rules {
		if r.Server != "fakeip" {
			continue
		}
		if len(r.QueryType) == 0 {
			cfg.DNS.Rules[i].QueryType = []string{"A", "AAAA"}
			continue
		}
		// Intersect: keep only A and AAAA.
		filtered := cfg.DNS.Rules[i].QueryType[:0]
		for _, qt := range r.QueryType {
			if qt == "A" || qt == "AAAA" {
				filtered = append(filtered, qt)
			}
		}
		if len(filtered) == 0 {
			cfg.DNS.Rules[i].QueryType = []string{"A", "AAAA"}
		} else {
			cfg.DNS.Rules[i].QueryType = filtered
		}
	}
}

// applyOutboundDomainResolver returns a copy of outbounds in which every
// outbound carrying a HOSTNAME Server (non-empty, not a parseable IP literal)
// is given DomainResolver{Server: resolver} — unless the caller already set a
// resolver, which is preserved. The input slice and its elements are not
// mutated (a fresh slice is returned; Outbound is a value type so element
// copies are independent).
//
// Why: in fakeip-tun mode the system DNS path returns fake addresses for
// hostnames. A proxy endpoint specified by hostname (e.g. server: "vpn.foo.io")
// would otherwise resolve to a fake address and the tunnel could never connect
// — a self-deadlock. Pinning such outbounds to the "real" resolver makes the
// proxy endpoint resolve to its true address.
//
// No-op cases (left untouched): empty Server (the v1 default for IP-bound
// `direct` + `bind_interface` outbounds — they carry no hostname to poison),
// and Server holding a v4 or v6 IP literal (no DNS resolution happens, so no
// fakeip risk).
func applyOutboundDomainResolver(outbounds []Outbound, resolver string) []Outbound {
	out := make([]Outbound, len(outbounds))
	copy(out, outbounds)
	for i := range out {
		if out[i].DomainResolver != nil {
			continue // respect caller's choice
		}
		if isHostname(out[i].Server) {
			out[i].DomainResolver = &DomainResolver{Server: resolver}
		}
	}
	return out
}

// isHostname reports whether s is a non-empty string that is NOT a bare IP
// literal — i.e. something that must go through DNS resolution. An empty string
// (no server) and any parseable v4/v6 address return false.
func isHostname(s string) bool {
	if s == "" {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return false // it's an IP literal, not a hostname
	}
	return true
}

// FakeIPPoolCollisions checks every fakeip pool CIDR against every configured
// LAN/tunnel subnet CIDR and returns one human-readable warning per TRUE
// overlap (netip.Prefix.Overlaps — containment in either direction or partial
// overlap, NOT string equality). Both address families are handled; a v4 pool
// never collides with a v6 subnet and vice versa (Overlaps is false across
// families). Empty and malformed entries are skipped silently. Returns nil when
// no overlap is found.
//
// Purpose: the fakeip pool MUST be a synthetic range that does not coincide
// with any real subnet the router serves, otherwise a fake address would shadow
// (or be shadowed by) a real destination and routing would break. The caller
// surfaces these warnings at config time.
func FakeIPPoolCollisions(pools []string, subnets []string) []string {
	parsedPools := parsePrefixes(pools)
	parsedSubnets := parsePrefixes(subnets)

	var warnings []string
	for _, p := range parsedPools {
		for _, sub := range parsedSubnets {
			if p.prefix.Overlaps(sub.prefix) {
				warnings = append(warnings, fmt.Sprintf(
					"fakeip pool %s overlaps configured subnet %s", p.raw, sub.raw))
			}
		}
	}
	return warnings
}

// namedPrefix pairs a parsed prefix with its original (canonicalized) text so
// collision warnings can echo what the caller passed in.
type namedPrefix struct {
	prefix netip.Prefix
	raw    string
}

// parsePrefixes parses each CIDR, skipping empty/malformed entries. Masked()
// canonicalizes (e.g. "10.0.0.5/24" → "10.0.0.0/24") so Overlaps is exact and
// the echoed text is the network form.
func parsePrefixes(cidrs []string) []namedPrefix {
	out := make([]namedPrefix, 0, len(cidrs))
	for _, c := range cidrs {
		if c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			continue // skip malformed gracefully
		}
		masked := p.Masked()
		out = append(out, namedPrefix{prefix: masked, raw: masked.String()})
	}
	return out
}

// DeriveTunDNS computes the fakeip-tun client DNS address for a /30, given the
// tun interface's own address in CIDR form (e.g. "172.18.0.1/30", where .1 is
// the router's host on the link). It returns the OTHER usable host of the /30
// (the sing-box tun side), e.g. "172.18.0.2". This is the address surfaced for
// manual client DNS configuration (awg-manager does not push it via DHCP).
//
// A /30 has exactly four addresses: network, two usable hosts, broadcast. The
// router owns one usable host; the DNS address is the other usable host so a
// client DNS pointed at it lands inside the tun where sing-box listens — NOT
// the router's own .1 (which would loop DNS back to the router, bypassing
// fakeip).
//
// Errors on: non-/30 prefixes, IPv6 (the client DNS address is v4), the
// network or broadcast address given as the iface host, a result that equals
// the iface's own host (defensive — should be impossible for a valid /30 host),
// and malformed input.
func DeriveTunDNS(ifaceCIDR string) (string, error) {
	p, err := netip.ParsePrefix(ifaceCIDR)
	if err != nil {
		return "", fmt.Errorf("derive tun dns: parse %q: %w", ifaceCIDR, err)
	}
	if !p.Addr().Is4() {
		return "", fmt.Errorf("derive tun dns: %q is not IPv4", ifaceCIDR)
	}
	if p.Bits() != 30 {
		return "", fmt.Errorf("derive tun dns: %q is not a /30", ifaceCIDR)
	}

	own := p.Addr()
	network := p.Masked().Addr() // .0
	host1 := network.Next()      // .1 (first usable)
	host2 := host1.Next()        // .2 (second usable)
	broadcast := host2.Next()    // .3

	if own == network || own == broadcast {
		return "", fmt.Errorf("derive tun dns: %q is the network or broadcast address", ifaceCIDR)
	}

	var dns netip.Addr
	switch own {
	case host1:
		dns = host2
	case host2:
		dns = host1
	default:
		// Unreachable for a valid /30 host, but guard anyway.
		return "", fmt.Errorf("derive tun dns: %q is not a usable host of its /30", ifaceCIDR)
	}

	if dns == own {
		return "", fmt.Errorf("derive tun dns: derived DNS equals iface address %q", own)
	}
	return dns.String(), nil
}

// FakeIPCacheNeedsReset reports whether the persisted fakeip cache must be
// discarded because the configured pool ranges no longer match the ranges the
// cache was last built with.
//
// fakeip persists a name↔synthetic-address map in sing-box's cache_file
// (store_fakeip). If the pool RANGE changes, the persisted map becomes stale:
// it would hand clients addresses drawn from the OLD pool and resolve them to
// the wrong domains (spec §3.6). Detecting a mismatch lets the caller wipe the
// cache before starting sing-box so the map is rebuilt against the new pool.
//
// Both families are compared (v4 stored-vs-configured AND v6 stored-vs-
// configured); a difference in EITHER returns true. Each non-empty range is
// normalized via netip.ParsePrefix(...).Masked().String() before comparison so
// cosmetically-different-but-equal CIDRs (e.g. "198.18.0.5/15" vs
// "198.18.0.0/15") compare equal. If a value fails to parse, that pair falls
// back to a trimmed exact-string compare (no panic). A family that is empty on
// both sides counts as unchanged; an empty stored side against a non-empty
// configured side (first provision) counts as changed, forcing a clean cache.
func FakeIPCacheNeedsReset(storedInet4, storedInet6, configuredInet4, configuredInet6 string) bool {
	return rangeChanged(storedInet4, configuredInet4) ||
		rangeChanged(storedInet6, configuredInet6)
}

// rangeChanged reports whether stored and configured describe a different pool
// range for one family. See FakeIPCacheNeedsReset for the normalization and
// empty-side semantics.
func rangeChanged(stored, configured string) bool {
	s := strings.TrimSpace(stored)
	c := strings.TrimSpace(configured)
	if s == "" && c == "" {
		return false // family absent on both sides
	}
	if s == "" || c == "" {
		return true // first provision (or removal) — force a clean cache
	}
	return normalizeRange(s) != normalizeRange(c)
}

// normalizeRange canonicalizes a CIDR to its masked network form so equal pools
// written differently compare equal. Unparseable input falls back to the
// trimmed original string (compared verbatim against the other side).
func normalizeRange(cidr string) string {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return strings.TrimSpace(cidr)
	}
	return p.Masked().String()
}

// ResetFakeIPCache removes the sing-box cache file at path so the next start
// rebuilds fakeip mappings from scratch. Idempotent: a missing file is not an
// error. The caller passes the configured cache path (e.g. the operator's
// defaultCacheDBPath); this helper stays path-agnostic and side-effect-pure
// beyond the single file removal.
func ResetFakeIPCache(path string) error {
	err := os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("reset fakeip cache %q: %w", path, err)
}

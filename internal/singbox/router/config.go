package router

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func NewEmptyConfig() *RouterConfig {
	return &RouterConfig{
		Inbounds:  []Inbound{},
		Outbounds: []Outbound{},
		DNS: DNS{
			Servers: []DNSServer{},
			Rules:   []DNSRule{},
		},
		Route: Route{
			RuleSet: []RuleSet{},
			Rules:   []Rule{},
			Final:   "direct",
		},
	}
}

func LoadConfig(path string) (*RouterConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewEmptyConfig(), nil
		}
		return nil, err
	}
	cfg := NewEmptyConfig()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Inbounds == nil {
		cfg.Inbounds = []Inbound{}
	}
	if cfg.Outbounds == nil {
		cfg.Outbounds = []Outbound{}
	}
	if cfg.Route.RuleSet == nil {
		cfg.Route.RuleSet = []RuleSet{}
	}
	if cfg.Route.Rules == nil {
		cfg.Route.Rules = []Rule{}
	}
	if cfg.DNS.Servers == nil {
		cfg.DNS.Servers = []DNSServer{}
	}
	if cfg.DNS.Rules == nil {
		cfg.DNS.Rules = []DNSRule{}
	}
	SanitizeDNSConfig(cfg)
	return cfg, nil
}

func SaveConfig(path string, cfg *RouterConfig) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(path, raw)
}

func (c *RouterConfig) AddRuleSet(rs RuleSet) error {
	if err := validateRuleSet(rs); err != nil {
		return err
	}
	for _, existing := range c.Route.RuleSet {
		if existing.Tag == rs.Tag {
			return fmt.Errorf("%w: %q", ErrRuleSetTagConflict, rs.Tag)
		}
	}
	c.Route.RuleSet = append(c.Route.RuleSet, rs)
	return nil
}

func (c *RouterConfig) UpdateRuleSet(tag string, next RuleSet) error {
	if next.Tag == "" {
		next.Tag = tag
	}
	if err := validateRuleSet(next); err != nil {
		return err
	}
	idx := -1
	for i, existing := range c.Route.RuleSet {
		if existing.Tag == tag {
			idx = i
			continue
		}
		if existing.Tag == next.Tag && tag != next.Tag {
			return fmt.Errorf("%w: %q", ErrRuleSetTagConflict, next.Tag)
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrRuleSetNotFound, tag)
	}
	c.Route.RuleSet[idx] = next
	if tag != next.Tag {
		c.renameRuleSetReferences(tag, next.Tag)
	}
	return nil
}

func (c *RouterConfig) DeleteRuleSet(tag string, force bool) error {
	tags := ruleSetTagsWithCompanion(tag)
	refs := c.rulesReferencingRuleSets(tags)
	if len(refs.route) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by route rules %v", ErrRuleSetReferenced, tag, refs.route)
	}
	if len(refs.dns) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by dns rules %v", ErrRuleSetReferenced, tag, refs.dns)
	}
	remove := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		remove[t] = struct{}{}
	}
	if force {
		// A rule left without a single matcher must GO, not stay: sing-box
		// treats a matcher-less rule as "matches everything"
		// (abstractDefaultRule.Match returns true on an empty item list), so
		// «пресет → VPN» whose set was force-deleted would quietly start
		// routing ALL traffic into that outbound. The rule existed for the
		// set alone; without it there is nothing left to keep.
		keptRules := make([]Rule, 0, len(c.Route.Rules))
		for _, r := range c.Route.Rules {
			referenced := ruleReferencesAnyRuleSet(r, remove)
			removeRuleSetRefsInRule(&r, remove)
			r = collapseNestedRules(r)
			// Выбрасываем ТОЛЬКО то, что осиротила эта операция: правило без
			// матчеров, не связанное с удаляемым набором, — чужое решение
			// (и в DNS matcher-less catch-all вообще легитимен).
			if referenced && !r.hasAnyMatcher() && !isSystemRule(r) {
				continue
			}
			keptRules = append(keptRules, r)
		}
		c.Route.Rules = keptRules

		keptDNS := make([]DNSRule, 0, len(c.DNS.Rules))
		for _, r := range c.DNS.Rules {
			// removeRuleSetRefs фильтрует на месте (tags[:0]) — звать её
			// можно ровно один раз, длину считаем до вызова.
			before := len(r.RuleSet)
			r.RuleSet = removeRuleSetRefs(r.RuleSet, remove)
			referenced := len(r.RuleSet) != before
			if referenced && !dnsRuleHasMatcher(r) {
				continue
			}
			keptDNS = append(keptDNS, r)
		}
		c.DNS.Rules = keptDNS
	}
	filtered := make([]RuleSet, 0, len(c.Route.RuleSet))
	for _, rs := range c.Route.RuleSet {
		if _, drop := remove[rs.Tag]; drop {
			continue
		}
		filtered = append(filtered, rs)
	}
	c.Route.RuleSet = filtered
	return nil
}

func (c *RouterConfig) renameRuleSetReferences(oldTag, newTag string) {
	for i := range c.Route.Rules {
		renameRuleSetRefsInRule(&c.Route.Rules[i], oldTag, newTag)
	}
	for i := range c.DNS.Rules {
		c.DNS.Rules[i].RuleSet = rewriteTagSlice(c.DNS.Rules[i].RuleSet, oldTag, newTag)
	}
}

func renameRuleSetRefsInRule(r *Rule, oldTag, newTag string) {
	r.RuleSet = rewriteTagSlice(r.RuleSet, oldTag, newTag)
	for i := range r.Rules {
		renameRuleSetRefsInRule(&r.Rules[i], oldTag, newTag)
	}
}

func removeRuleSetRefsInRule(r *Rule, remove map[string]struct{}) {
	r.RuleSet = removeRuleSetRefs(r.RuleSet, remove)
	for i := range r.Rules {
		removeRuleSetRefsInRule(&r.Rules[i], remove)
	}
}

// collapseNestedRules drops logical branches left without a single matcher and
// unwraps a logical rule down to its last surviving branch. A force rule-set
// delete strips tags out of nested branches (removeRuleSetRefsInRule), and the
// branch of a normalized rule holds nothing but that tag — leaving `{}` behind
// would cost the whole slot: sing-box rejects a config with an empty sub-rule
// (DefaultHeadlessRule.IsValid → "missing conditions"), not just that rule.
//
// The surviving branch inherits the parent's action so the rule keeps routing
// where it did — the shape shrinks, the intent does not.
func collapseNestedRules(r Rule) Rule {
	if len(r.Rules) == 0 {
		return r
	}
	kept := make([]Rule, 0, len(r.Rules))
	for _, nested := range r.Rules {
		nested = collapseNestedRules(nested)
		if !nested.hasAnyMatcher() {
			continue
		}
		kept = append(kept, nested)
	}
	r.Rules = kept
	if r.Type != "logical" {
		return r
	}
	switch len(kept) {
	case 0:
		r.Type, r.Mode, r.Rules = "", "", nil
	case 1:
		only := kept[0]
		only.Action, only.Outbound = r.Action, r.Outbound
		only.UDPTimeout, only.AwgmManaged = r.UDPTimeout, r.AwgmManaged
		return only
	}
	return r
}

func removeRuleSetRefs(tags []string, remove map[string]struct{}) []string {
	if len(tags) == 0 {
		return nil
	}
	filtered := tags[:0]
	for _, tag := range tags {
		if _, drop := remove[tag]; drop {
			continue
		}
		filtered = append(filtered, tag)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

type ruleSetRefIndices struct {
	route []int
	dns   []int
}

// ruleSetTagsInRule collects every rule_set tag the rule references, including
// the ones sitting in nested logical branches (normalizeAddressOrRule puts them
// there). Readers that only look at the top-level RuleSet field go blind on a
// normalized rule — the set then looks orphaned to the issue detector, its
// artifacts look unreferenced to the GC, and deleting it is not blocked.
func ruleSetTagsInRule(r Rule, out []string) []string {
	out = append(out, r.RuleSet...)
	for _, nested := range r.Rules {
		out = ruleSetTagsInRule(nested, out)
	}
	return out
}

func ruleReferencesAnyRuleSet(r Rule, want map[string]struct{}) bool {
	for _, tag := range ruleSetTagsInRule(r, nil) {
		if _, ok := want[tag]; ok {
			return true
		}
	}
	return false
}

func (c *RouterConfig) rulesReferencingRuleSets(tags []string) ruleSetRefIndices {
	want := make(map[string]struct{}, len(tags))
	for _, t := range tags {
		want[t] = struct{}{}
	}
	var out ruleSetRefIndices
	for i, r := range c.Route.Rules {
		if ruleReferencesAnyRuleSet(r, want) {
			out.route = append(out.route, i)
		}
	}
	for i, r := range c.DNS.Rules {
		for _, rsTag := range r.RuleSet {
			if _, ok := want[rsTag]; ok {
				out.dns = append(out.dns, i)
				break
			}
		}
	}
	return out
}

// normalizeAddressOrRule rewrites a flat rule that mixes a rule_set with the
// rule's OWN destination-address matchers (domain / domain_suffix / ip_cidr)
// into an explicit `logical(or)`.
//
// Why: sing-box folds a rule_set into the referencing rule's destination-address
// group ONLY when the set holds exactly one destination-only rule (mergeableRuleIn,
// route/rule/rule_item_rule_set.go). Any other set — and four of the presets we
// ship are exactly that, discord-full/telegram/roblox/whatsapp carry two rules —
// is matched separately and only after the rule's own matchers already hit, i.e.
// as an AND. A rule reading "preset OR my subnets" then matches almost nothing:
// a domain from the preset fails the ip_cidr half, an IP from the subnets fails
// the preset half (issue #699).
//
// The destination-address branch carries every matcher sing-box puts in that
// group: domain, domain_suffix, ip_cidr AND ip_is_private — the last one lands
// in destinationIPCIDRItems (fork route/rule/rule_default.go:154), so it is
// OR-ed with the addresses, not AND-ed against them.
//
// Narrowing matchers (port, network, protocol, inbound, source_ip_cidr,
// source_mac_address) keep their AND meaning: they move into a sibling
// branch of an outer `logical(and)`. Action/outbound stay on the outer
// rule — nested branches carry matchers only.
//
// Idempotent: an already-logical rule is returned untouched.
func normalizeAddressOrRule(r Rule) Rule {
	if r.Type != "" || len(r.RuleSet) == 0 {
		return r
	}
	// Only a TRUE ip_is_private counts as a matcher: `"ip_is_private": false`
	// is sing-box's way of writing "no such condition", and a branch holding
	// nothing else would come out empty — which costs the whole slot
	// ("missing conditions" rejects the config, not just the rule).
	private := r.IPIsPrivate != nil && *r.IPIsPrivate
	if len(r.Domain) == 0 && len(r.DomainSuffix) == 0 && len(r.IPCIDR) == 0 && !private {
		return r
	}

	addressBranch := Rule{Domain: r.Domain, DomainSuffix: r.DomainSuffix, IPCIDR: r.IPCIDR}
	if private {
		addressBranch.IPIsPrivate = r.IPIsPrivate
	}
	addressOr := Rule{Type: "logical", Mode: "or", Rules: []Rule{
		{RuleSet: r.RuleSet},
		addressBranch,
	}}

	narrowing := Rule{
		SourceIPCIDR:     r.SourceIPCIDR,
		SourceMACAddress: r.SourceMACAddress,
		Port:             r.Port,
		Protocol:         r.Protocol,
		Inbound:          r.Inbound,
		Network:          r.Network,
	}

	out := r
	out.RuleSet, out.Domain, out.DomainSuffix, out.IPCIDR = nil, nil, nil, nil
	out.SourceIPCIDR, out.Port, out.Protocol, out.Inbound = nil, nil, "", nil
	out.SourceMACAddress = nil
	out.Network, out.IPIsPrivate = "", nil
	out.Type, out.Mode = "logical", "or"
	out.Rules = addressOr.Rules
	if narrowing.hasAnyMatcher() {
		out.Mode = "and"
		out.Rules = []Rule{narrowing, addressOr}
	}
	return out
}

func (c *RouterConfig) AddRule(r Rule) error {
	if !r.hasAnyMatcher() && r.Action != "sniff" && r.Action != "hijack-dns" {
		return ErrInvalidMatchers
	}
	if err := validateRule(r); err != nil {
		return err
	}
	c.Route.Rules = append(c.Route.Rules, normalizeAddressOrRule(r))
	return nil
}

func (c *RouterConfig) UpdateRule(index int, r Rule) error {
	if index < 0 || index >= len(c.Route.Rules) {
		return ErrRuleIndexOutOfRange
	}
	if !r.hasAnyMatcher() && r.Action != "sniff" && r.Action != "hijack-dns" {
		return ErrInvalidMatchers
	}
	if err := validateRule(r); err != nil {
		return err
	}
	c.Route.Rules[index] = normalizeAddressOrRule(r)
	return nil
}

func (c *RouterConfig) DeleteRule(index int) error {
	if index < 0 || index >= len(c.Route.Rules) {
		return ErrRuleIndexOutOfRange
	}
	c.Route.Rules = append(c.Route.Rules[:index], c.Route.Rules[index+1:]...)
	return nil
}

func (c *RouterConfig) MoveRule(from, to int) error {
	n := len(c.Route.Rules)
	if from < 0 || from >= n || to < 0 || to >= n {
		return ErrRuleIndexOutOfRange
	}
	if from == to {
		return nil
	}
	r := c.Route.Rules[from]
	without := append(c.Route.Rules[:from:from], c.Route.Rules[from+1:]...)
	rules := make([]Rule, 0, n)
	rules = append(rules, without[:to]...)
	rules = append(rules, r)
	rules = append(rules, without[to:]...)
	c.Route.Rules = rules
	return nil
}

func (c *RouterConfig) EnsureSystemRules(snifferEnabled bool) {
	if c.Route.Final == "" {
		c.Route.Final = "direct"
	}
	hasSniff := false
	hasHijack := false
	hasPrivateBypass := false
	// Track existing hijack-dns position. ip_is_private MUST be inserted
	// right after hijack-dns; if we prepend it to position 0 instead,
	// LAN-IP DNS matches ip_is_private first and routes `direct`,
	// bypassing the hijack entirely and breaking DNS for in-policy
	// clients.
	hijackIdx := -1
	for i, r := range c.Route.Rules {
		if isSystemSniffRule(r) {
			hasSniff = true
		}
		// isSystemHijackRule detects both the legacy (`protocol:dns`) and
		// current (`logical(or){protocol:dns, port:53}`) forms so re-running
		// EnsureSystemRules doesn't stack duplicates on migrated configs.
		if isSystemHijackRule(r) {
			hasHijack = true
			if hijackIdx == -1 {
				hijackIdx = i
			}
		}
		// Any user-authored ip_is_private rule wins over the system one — we
		// just have to not duplicate (isSystemPrivateRule ignores outbound).
		if isSystemPrivateRule(r) {
			hasPrivateBypass = true
		}
	}

	if !snifferEnabled && hasSniff {
		filtered := c.Route.Rules[:0]
		for _, r := range c.Route.Rules {
			if isSystemSniffRule(r) {
				continue
			}
			filtered = append(filtered, r)
		}
		c.Route.Rules = filtered
		hasSniff = false
		if hijackIdx >= 0 {
			hijackIdx = -1
			for i, r := range c.Route.Rules {
				if isSystemHijackRule(r) {
					hijackIdx = i
					break
				}
			}
		}
	}

	// Phase 1: prepend sniff + hijack-dns to front if missing.
	// Predictable order inside the prepend block is [sniff, hijack-dns].
	prepend := make([]Rule, 0, 2)
	if snifferEnabled && !hasSniff {
		prepend = append(prepend, Rule{Action: "sniff"})
	}
	if !hasHijack {
		// Logical-or rule catches BOTH sniffed DNS (`protocol:dns`)
		// and any TCP/UDP traffic to port 53 (`port:53`). The latter
		// matters when sniffing missed the protocol (truncated buffer,
		// non-standard DNS payload) — port-based match guarantees
		// hijack still fires. SKeen ships the same form.
		prepend = append(prepend, Rule{
			Type: "logical",
			Mode: "or",
			Rules: []Rule{
				{Protocol: "dns"},
				{Port: []int{53}},
			},
			Action: "hijack-dns",
		})
		// Newly-prepended hijack ends up at the last slot of the
		// prepend block (after the optional sniff).
		hijackIdx = len(prepend) - 1
	} else {
		// Existing hijack shifts right by len(prepend) once prepend is
		// stitched in front.
		hijackIdx += len(prepend)
	}
	if len(prepend) > 0 {
		c.Route.Rules = append(prepend, c.Route.Rules...)
	}

	// Phase 2: insert ip_is_private at hijackIdx+1 — directly after the
	// hijack-dns rule, whether it was just prepended or already present.
	if !hasPrivateBypass {
		// Defense-in-depth: any packet that slips into sing-box with a
		// private destination (RFC1918, loopback, link-local, multicast —
		// CGNAT is NOT one of them, see Rule.IPIsPrivate) goes `direct`
		// instead of falling through to
		// `final: proxy`. Matters specifically for non-policy DNS that
		// the `hijack-dns` side-effect transparent listener picks up
		// from router LAN IPs — those packets arrive without TPROXY
		// ancillary data and would otherwise be silently dropped (no
		// reply, client sees timeout). Mirrors SKeen example config
		// (`reference/SKeen/examples/config.json:115`).
		truePtr := true
		privateRule := Rule{IPIsPrivate: &truePtr, Outbound: "direct"}
		insertPos := hijackIdx + 1
		newRules := make([]Rule, 0, len(c.Route.Rules)+1)
		newRules = append(newRules, c.Route.Rules[:insertPos]...)
		newRules = append(newRules, privateRule)
		newRules = append(newRules, c.Route.Rules[insertPos:]...)
		c.Route.Rules = newRules
	}
}

// EnsureRouteWAN applies the WAN-binding discriminator to route.
// Exactly one of `auto_detect_interface` / `default_interface` is written
// to the emitted config — never both — so sing-box never sees a
// contradictory state.
//
//   - autoDetect == true  → AutoDetectInterface = &true,
//     DefaultInterface = "".
//     kernelName MUST be empty here (validated upstream by
//     ValidateSingboxRouterSettings); the field is accepted as an
//     argument purely for the symmetric signature.
//   - autoDetect == false → DefaultInterface = kernelName,
//     AutoDetectInterface = nil.
//     kernelName MUST be a non-empty kernel system-name (e.g. "ppp0").
//     Same upstream validator enforces non-emptiness; this method does
//     not second-guess the caller.
//
// Called from Enable() after EnsureSystemRules. Re-running with the same
// arguments is a no-op idempotent update.
func (c *RouterConfig) EnsureRouteWAN(autoDetect bool, kernelName string) {
	if autoDetect {
		t := true
		c.Route.AutoDetectInterface = &t
		c.Route.DefaultInterface = ""
		return
	}
	c.Route.AutoDetectInterface = nil
	c.Route.DefaultInterface = kernelName
}

// SetRouteFinal updates route.final. Caller must validate the tag refers
// to a known outbound (or sing-box built-in: "direct", "block").
// Setting to "" is rejected — use "direct" for default fallback.
func (c *RouterConfig) SetRouteFinal(tag string) error {
	if tag == "" {
		return fmt.Errorf("route final cannot be empty (use 'direct' for default)")
	}
	c.Route.Final = tag
	return nil
}

func (c *RouterConfig) AddCompositeOutbound(o Outbound) error {
	if err := validateOutbound(o); err != nil {
		return err
	}
	for _, existing := range c.Outbounds {
		if existing.Tag == o.Tag {
			return fmt.Errorf("%w: %q", ErrOutboundTagConflict, o.Tag)
		}
	}
	next := append(append([]Outbound(nil), c.Outbounds...), o)
	if err := validateNoCompositeCycles(next); err != nil {
		return err
	}
	c.Outbounds = next
	return nil
}

func (c *RouterConfig) UpdateCompositeOutbound(tag string, o Outbound) error {
	if err := validateOutbound(o); err != nil {
		return err
	}
	idx := -1
	for i, existing := range c.Outbounds {
		if existing.Tag == tag {
			idx = i
			continue
		}
		if existing.Tag == o.Tag && tag != o.Tag {
			return fmt.Errorf("%w: %q", ErrOutboundTagConflict, o.Tag)
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrOutboundNotFound, tag)
	}
	next := append([]Outbound(nil), c.Outbounds...)
	next[idx] = o
	if err := validateNoCompositeCycles(next); err != nil {
		return err
	}
	c.Outbounds = next
	if tag != o.Tag {
		c.renameOutboundReferences(tag, o.Tag)
	}
	return nil
}

// validateCompositeOutbound rejects shapes that compile but produce
// surprising behavior at runtime. In particular `direct` as a member of
// a selector/urltest/loadbalance group lets traffic bypass the proxy
// silently — almost never what the user wants, and a known footgun in
// sing-box composite groups. Same for `default: "direct"`.
func validateCompositeOutbound(o Outbound) error {
	if strings.TrimSpace(o.Tag) == "" {
		return fmt.Errorf("outbound tag is required")
	}
	if len(o.Outbounds) == 0 {
		return fmt.Errorf("outbound %q: at least one member is required", o.Tag)
	}
	for _, m := range o.Outbounds {
		if strings.EqualFold(strings.TrimSpace(m), "direct") {
			return fmt.Errorf("outbound %q: member %q is not allowed in composite groups (would bypass proxy silently)", o.Tag, m)
		}
		// Exact match (not EqualFold): sing-box outbound tags are
		// case-sensitive keys, so "DE" and "de" are distinct outbounds.
		if strings.TrimSpace(m) == strings.TrimSpace(o.Tag) {
			return fmt.Errorf("outbound %q: member %q references itself (would create a circular dependency and crash sing-box)", o.Tag, m)
		}
	}
	if strings.EqualFold(strings.TrimSpace(o.Default), "direct") {
		return fmt.Errorf("outbound %q: default %q is not allowed in composite groups", o.Tag, o.Default)
	}
	return nil
}

// validateOutbound dispatches by Type: "direct" outbounds carry a
// bind_interface and no composite fields; selector/urltest go through the
// composite validator.
func validateOutbound(o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		return validateInterfaceOutbound(o)
	}
	return validateCompositeOutbound(o)
}

// validateInterfaceOutbound checks a user-created direct outbound bound to
// a network interface. Interface existence is verified in the service
// layer (needs the NDMS interface list); here we only check shape.
func validateInterfaceOutbound(o Outbound) error {
	if strings.TrimSpace(o.Tag) == "" {
		return fmt.Errorf("outbound tag is required")
	}
	if strings.TrimSpace(o.BindInterface) == "" {
		return fmt.Errorf("outbound %q: bind_interface is required for a direct outbound", o.Tag)
	}
	if len(o.Outbounds) > 0 || o.URL != "" || o.Interval != "" || o.Tolerance != 0 || o.Default != "" || o.Strategy != "" {
		return fmt.Errorf("outbound %q: direct outbound must not set composite fields (members/url/interval/tolerance/default/strategy)", o.Tag)
	}
	return nil
}

// validateNoCompositeCycles rejects a set of outbounds that contains a
// circular dependency between composite groups (e.g. DE -> DE, or A -> B
// -> A). sing-box only detects this at "start service" time — `sing-box
// check` passes — so without this guard a cyclic config persists and the
// process FATAL-loops on every start. Only composite->composite edges can
// form a cycle; leaf outbounds (awg/sub/sb tunnels, direct) are ignored.
//
// Scope is the passed slice (router composites from 20-router.json).
// Subscription-slot composites are not passed here, so they count as leaf
// members — which is sound: their members are subscription servers, never
// router composites, so no subscription->router edge (and thus no
// cross-slot cycle) can be formed through the UI.
func validateNoCompositeCycles(outbounds []Outbound) error {
	isComposite := make(map[string]bool, len(outbounds))
	for _, o := range outbounds {
		isComposite[o.Tag] = true
	}
	edges := make(map[string][]string, len(outbounds))
	for _, o := range outbounds {
		for _, m := range o.Outbounds {
			if isComposite[m] {
				edges[o.Tag] = append(edges[o.Tag], m)
			}
		}
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(outbounds))
	var path []string
	var visit func(tag string) error
	visit = func(tag string) error {
		color[tag] = gray
		path = append(path, tag)
		for _, next := range edges[tag] {
			switch color[next] {
			case gray:
				return fmt.Errorf("circular outbound dependency: %s -> %s",
					strings.Join(path, " -> "), next)
			case white:
				if err := visit(next); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		color[tag] = black
		return nil
	}
	for _, o := range outbounds {
		if color[o.Tag] == white {
			if err := visit(o.Tag); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *RouterConfig) DeleteCompositeOutbound(tag string, force bool) error {
	refs := c.outboundReferences(tag)
	if len(refs) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by %s", ErrOutboundReferenced, tag, strings.Join(refs, ", "))
	}
	filtered := make([]Outbound, 0, len(c.Outbounds))
	for _, o := range c.Outbounds {
		if o.Tag != tag {
			filtered = append(filtered, o)
		}
	}
	c.Outbounds = filtered
	if force {
		c.removeOutboundReferences(tag)
	}
	return nil
}

func (c *RouterConfig) rulesReferencingOutbound(tag string) []int {
	var refs []int
	for i, r := range c.Route.Rules {
		if ruleReferencesOutbound(r, tag) {
			refs = append(refs, i)
		}
	}
	return refs
}

func (c *RouterConfig) renameOutboundReferences(oldTag, newTag string) {
	for i := range c.Route.Rules {
		renameOutboundRefsInRule(&c.Route.Rules[i], oldTag, newTag)
	}
	if c.Route.Final == oldTag {
		c.Route.Final = newTag
	}
	for i := range c.Outbounds {
		c.Outbounds[i].Outbounds = rewriteTagSlice(c.Outbounds[i].Outbounds, oldTag, newTag)
		if c.Outbounds[i].Default == oldTag {
			c.Outbounds[i].Default = newTag
		}
	}
	for i := range c.DNS.Servers {
		if c.DNS.Servers[i].Detour == oldTag {
			c.DNS.Servers[i].Detour = newTag
		}
	}
	for i := range c.Route.RuleSet {
		if c.Route.RuleSet[i].DownloadDetour == oldTag {
			c.Route.RuleSet[i].DownloadDetour = newTag
		}
		if hc := c.Route.RuleSet[i].HTTPClient; hc != nil && hc.Detour == oldTag {
			hc.Detour = newTag
		}
	}
	for i := range c.HTTPClients {
		if c.HTTPClients[i].Detour == oldTag {
			c.HTTPClients[i].Detour = newTag
		}
	}
}

func (c *RouterConfig) removeOutboundReferences(tag string) {
	rules := make([]Rule, 0, len(c.Route.Rules))
	for _, r := range c.Route.Rules {
		if r.Outbound == tag {
			continue
		}
		removeOutboundRefsInNestedRules(&r, tag)
		rules = append(rules, r)
	}
	c.Route.Rules = rules
	if c.Route.Final == tag {
		c.Route.Final = "direct"
	}
	for i := range c.Outbounds {
		c.Outbounds[i].Outbounds = removeTagRefs(c.Outbounds[i].Outbounds, tag)
		if c.Outbounds[i].Default == tag {
			c.Outbounds[i].Default = ""
		}
	}
	for i := range c.DNS.Servers {
		if c.DNS.Servers[i].Detour == tag {
			c.DNS.Servers[i].Detour = ""
		}
	}
	for i := range c.Route.RuleSet {
		if c.Route.RuleSet[i].DownloadDetour == tag {
			c.Route.RuleSet[i].DownloadDetour = ""
		}
		if hc := c.Route.RuleSet[i].HTTPClient; hc != nil && hc.Detour == tag {
			hc.Detour = ""
		}
	}
	for i := range c.HTTPClients {
		if c.HTTPClients[i].Detour == tag {
			c.HTTPClients[i].Detour = ""
		}
	}
}

func (c *RouterConfig) outboundReferences(tag string) []string {
	var refs []string
	for i, r := range c.Route.Rules {
		if ruleReferencesOutbound(r, tag) {
			refs = append(refs, fmt.Sprintf("route.rules[%d]", i))
		}
	}
	if c.Route.Final == tag {
		refs = append(refs, "route.final")
	}
	for i, o := range c.Outbounds {
		for j, member := range o.Outbounds {
			if member == tag {
				refs = append(refs, fmt.Sprintf("outbounds[%d=%q].outbounds[%d]", i, o.Tag, j))
			}
		}
		if o.Default == tag {
			refs = append(refs, fmt.Sprintf("outbounds[%d=%q].default", i, o.Tag))
		}
	}
	for i, s := range c.DNS.Servers {
		if s.Detour == tag {
			refs = append(refs, fmt.Sprintf("dns.servers[%d=%q].detour", i, s.Tag))
		}
	}
	for i, rs := range c.Route.RuleSet {
		if rs.DownloadDetour == tag {
			refs = append(refs, fmt.Sprintf("route.rule_set[%d=%q].download_detour", i, rs.Tag))
		}
		if rs.HTTPClient != nil && rs.HTTPClient.Detour == tag {
			refs = append(refs, fmt.Sprintf("route.rule_set[%d=%q].http_client.detour", i, rs.Tag))
		}
	}
	for i, hc := range c.HTTPClients {
		if hc.Detour == tag {
			refs = append(refs, fmt.Sprintf("http_clients[%d=%q].detour", i, hc.Tag))
		}
	}
	return refs
}

// outboundReferencesExcludingRules returns all references to tag EXCEPT
// route.rules[...] entries — those are reported separately as rule
// indices by rulesReferencingOutbound (for UI deeplinking). Covers
// route.final, composite members, composite default, dns.servers detour,
// rule_set download_detour / http_client.detour, and top-level
// http_clients[].detour — all the locations validateLocked flags as
// unknown-outbound but that rulesReferencingOutbound does not see.
func (c *RouterConfig) outboundReferencesExcludingRules(tag string) []string {
	all := c.outboundReferences(tag)
	out := make([]string, 0, len(all))
	for _, ref := range all {
		if strings.HasPrefix(ref, "route.rules[") {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func ruleReferencesOutbound(r Rule, tag string) bool {
	if r.Outbound == tag {
		return true
	}
	for _, nested := range r.Rules {
		if ruleReferencesOutbound(nested, tag) {
			return true
		}
	}
	return false
}

func renameOutboundRefsInRule(r *Rule, oldTag, newTag string) {
	if r.Outbound == oldTag {
		r.Outbound = newTag
	}
	for i := range r.Rules {
		renameOutboundRefsInRule(&r.Rules[i], oldTag, newTag)
	}
}

func removeOutboundRefsInNestedRules(r *Rule, tag string) {
	for i := range r.Rules {
		if r.Rules[i].Outbound == tag {
			r.Rules[i].Outbound = ""
		}
		removeOutboundRefsInNestedRules(&r.Rules[i], tag)
	}
}

func rewriteTagSlice(tags []string, from, to string) []string {
	if from == "" || to == "" || from == to {
		return tags
	}
	return mapTagSlice(tags, func(tag string) (string, bool) {
		return to, tag == from
	})
}

func removeTagRefs(tags []string, tag string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := tags[:0]
	for _, existing := range tags {
		if existing != tag {
			out = append(out, existing)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (c *RouterConfig) CompositeOutbounds() []Outbound {
	// All non-system outbounds in 20-router.json are composite (urltest,
	// selector, loadbalance, ...). AWG-direct outbounds live in
	// 15-awg.json (owned by awgoutbounds) and are not present here.
	out := make([]Outbound, 0, len(c.Outbounds))
	for _, o := range c.Outbounds {
		out = append(out, o)
	}
	return out
}

// IsAutoManagedIface reports whether a kernel interface name is one that
// awgoutbounds auto-generates a direct outbound for (managed AWG tunnels,
// NativeWG, third-party WireGuard, our own tunnels, Keenetic sing-box
// proxies). Direct outbounds bound to these live in 15-awg.json and must
// not be duplicated in the user composite store. User VPNs (ipsec/ike/
// sstp/openvpn/l2tp/pptp/ppp/eth/...) are NOT auto-managed.
func IsAutoManagedIface(name string) bool {
	n := strings.ToLower(name)
	for _, p := range []string{"opkgtun", "awgm", "awg", "wg", "wireguard", "nwg", "t2s", "proxy"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// stripAutoManagedDirect filters out direct outbounds whose bind_interface
// belongs to the awgoutbounds auto-managed set (these live in 15-awg.json,
// owned by awgoutbounds). User-created direct outbounds bound to other VPN
// interfaces (IPSec/IKEv2/etc.) are kept — they live here in 20-router.json.
// Composite outbounds and bind_interface-less direct are always kept.
//
// Proxy kernel ifaces (t2sN / proxyN) are NEVER stripped (#323): awgoutbounds
// does not generate proxy outbounds, and the bindable picker only ever offers
// KeenOS-native (non-ours) proxies, so any direct→t2s here is a deliberate
// user bind. Keeping them unconditionally avoids a runtime NDMS lookup in the
// Enable path — a transient lookup error must never silently delete a user's
// persisted outbound.
func stripAutoManagedDirect(in []Outbound) []Outbound {
	out := make([]Outbound, 0, len(in))
	for _, o := range in {
		if o.Type == "direct" && o.BindInterface != "" &&
			IsStrippedDirectBind(o.BindInterface) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// IsStrippedDirectBind reports whether a direct outbound bound to iface is
// removed from the effective config by stripAutoManagedDirect (auto-managed
// AWG/WG/NWG ifaces, excluding proxy t2sN/proxyN which are kept). Exported so
// the device-proxy outbound catalog can hide non-selectable router directs.
func IsStrippedDirectBind(iface string) bool {
	return IsAutoManagedIface(iface) && !isProxyIface(iface)
}

// isProxyIface reports a Keenetic proxy kernel interface name (tun2socks
// "t2sN" or "proxyN"). These front a SOCKS proxy and are valid bind targets.
func isProxyIface(name string) bool {
	n := strings.ToLower(name)
	return strings.HasPrefix(n, "t2s") || strings.HasPrefix(n, "proxy")
}

// hasAnyMatcher reports whether the rule constrains anything at all. A rule
// with no matcher is not inert — sing-box matches EVERY connection with it
// (abstractDefaultRule.Match returns true on an empty item list) — so callers
// use this to refuse creating one and to drop one left behind.
//
// `ip_is_private: false` deliberately does NOT count: that is sing-box's way
// of writing "no such condition", and treating it as a matcher would let a
// rule survive force-delete as an unconditional catch-all.
func (r Rule) hasAnyMatcher() bool {
	return len(r.Domain) > 0 || len(r.DomainSuffix) > 0 || len(r.IPCIDR) > 0 ||
		len(r.SourceIPCIDR) > 0 || len(r.SourceMACAddress) > 0 ||
		len(r.Port) > 0 || len(r.RuleSet) > 0 || r.Protocol != "" || len(r.Rules) > 0 ||
		(r.IPIsPrivate != nil && *r.IPIsPrivate) || len(r.Inbound) > 0 || r.Network != ""
}

// isSystemUDPTimeoutRule reports whether r is the system route-options rule that
// raises the UDP idle timeout (Action "route-options" scoped to Network "udp").
func isSystemUDPTimeoutRule(r Rule) bool {
	return r.Action == "route-options" && r.Network == "udp"
}

// The three predicates below are the single definition of what counts as a
// leading "system rule". EnsureSystemRules (detection) and systemPrefixLen
// (insertion boundary for the route-options rule) both use them so the two can
// never drift apart.

func isSystemSniffRule(r Rule) bool {
	return r.Action == "sniff" && !r.hasAnyMatcher()
}

// isSystemHijackRule matches both the legacy (`protocol:dns`) and current
// (`logical(or){protocol:dns, port:53}`) system hijack-dns forms.
func isSystemHijackRule(r Rule) bool {
	return r.Action == "hijack-dns" &&
		(r.Protocol == "dns" || (r.Type == "logical" && r.Mode == "or"))
}

// isSystemPrivateRule matches any ip_is_private bypass. Outbound is intentionally
// NOT checked (mirrors EnsureSystemRules): a user may point private destinations
// at a specific direct-LAN outbound and that rule is still part of the prefix.
func isSystemPrivateRule(r Rule) bool {
	return r.IPIsPrivate != nil && *r.IPIsPrivate
}

// isSystemRule reports whether r is one of the leading system rules that
// EnsureSystemRules manages — bulk outbound changes must never touch them.
func isSystemRule(r Rule) bool {
	return isSystemSniffRule(r) || isSystemHijackRule(r) || isSystemUDPTimeoutRule(r) || isSystemPrivateRule(r)
}

// systemPrefixLen counts the leading system rules (sniff / hijack-dns /
// ip_is_private bypass) that EnsureSystemRules keeps ahead of any user routing
// rule. It marks the boundary where a non-final system rule (the udp_timeout
// route-options rule) can be inserted so it still runs before a user's final
// `route` action stops evaluation.
func (c *RouterConfig) systemPrefixLen() int {
	n := 0
	for _, r := range c.Route.Rules {
		if isSystemSniffRule(r) || isSystemHijackRule(r) || isSystemPrivateRule(r) {
			n++
			continue
		}
		break
	}
	return n
}

// EnsureUDPTimeoutRule keeps exactly one system route-options rule that raises
// the UDP idle timeout to `effective`, inserted within the system prefix (before
// any user routing rule). sing-box applies short per-protocol UDP idle timeouts
// on sniff/port inference — STUN/DNS 10s, QUIC/DTLS 30s — that ignore the inbound
// udp_timeout, so games/VoIP sessions drop early; a route-options rule raising
// the timeout to the inbound value neutralizes them. Idempotent: any prior copy
// is stripped first so a changed timeout takes effect and re-runs never stack.
func (c *RouterConfig) EnsureUDPTimeoutRule(effective string) {
	filtered := c.Route.Rules[:0]
	for _, r := range c.Route.Rules {
		if isSystemUDPTimeoutRule(r) {
			continue
		}
		filtered = append(filtered, r)
	}
	c.Route.Rules = filtered
	if effective == "" {
		return
	}
	pos := c.systemPrefixLen()
	rule := Rule{Action: "route-options", Network: "udp", UDPTimeout: effective}
	newRules := make([]Rule, 0, len(c.Route.Rules)+1)
	newRules = append(newRules, c.Route.Rules[:pos]...)
	newRules = append(newRules, rule)
	newRules = append(newRules, c.Route.Rules[pos:]...)
	c.Route.Rules = newRules
}

// validateCIDROrAddr accepts a value that is either a CIDR prefix or a bare IP
// address; it returns an error labeled with the caller-supplied field name when
// the value is neither. Shared by validateRule and validateDNSRule so the
// CIDR-or-address parse logic lives in one place; the label carries the
// per-caller error prefix (e.g. "ip_cidr %q", "dns rule: invalid source_ip_cidr %q").
func validateCIDROrAddr(label, v string) error {
	if _, err := netip.ParsePrefix(v); err != nil {
		if _, err := netip.ParseAddr(v); err != nil {
			return fmt.Errorf(label+" %q: %w", v, err)
		}
	}
	return nil
}

func validateRule(r Rule) error {
	for _, cidr := range r.IPCIDR {
		if err := validateCIDROrAddr("ip_cidr", cidr); err != nil {
			return err
		}
	}
	for _, cidr := range r.SourceIPCIDR {
		if err := validateCIDROrAddr("source_ip_cidr", cidr); err != nil {
			return err
		}
	}
	for _, mac := range r.SourceMACAddress {
		if _, err := net.ParseMAC(mac); err != nil {
			return fmt.Errorf("source_mac_address %q: %w", mac, err)
		}
	}
	for _, p := range r.Port {
		if p < 1 || p > 65535 {
			return fmt.Errorf("port %d out of range [1,65535]", p)
		}
	}
	for _, tag := range r.Inbound {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("inbound tag must be a non-empty string")
		}
		if isQoSInboundTag(strings.TrimSpace(tag)) {
			return fmt.Errorf("%w (получен %q)", ErrReservedInboundTag, tag)
		}
	}
	// Nested logical sub-rules carry matchers too — a reserved tag must not
	// slip in through `rules: [...]`.
	for _, nested := range r.Rules {
		if err := validateRule(nested); err != nil {
			return err
		}
	}
	return nil
}

func validateRuleSet(rs RuleSet) error {
	if rs.Tag == "" {
		return fmt.Errorf("rule_set tag is required")
	}
	if strings.HasSuffix(rs.Tag, inlineSRSSuffix) {
		return fmt.Errorf("rule_set %q: tag suffix %q is reserved for compiled inline rulesets", rs.Tag, inlineSRSSuffix)
	}
	switch rs.Type {
	case "inline":
		if len(rs.Rules) == 0 {
			return fmt.Errorf("rule_set %q: rules required for type=inline", rs.Tag)
		}
		for i, rule := range rs.Rules {
			if len(rule) == 0 {
				return fmt.Errorf("rule_set %q: inline rule at index %d is empty", rs.Tag, i)
			}
			if !inlineRuleHasKnownField(rule) {
				return fmt.Errorf("rule_set %q: inline rule at index %d has no known matcher/action fields", rs.Tag, i)
			}
		}
	case "remote":
		if rs.URL == "" {
			return fmt.Errorf("rule_set %q: url required for type=remote", rs.Tag)
		}
		u, err := url.Parse(rs.URL)
		if err != nil || u == nil || u.Host == "" {
			return fmt.Errorf("rule_set %q: invalid url %q", rs.Tag, rs.URL)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("rule_set %q: url scheme must be http or https, got %q", rs.Tag, u.Scheme)
		}
	case "local":
		if rs.Path == "" {
			return fmt.Errorf("rule_set %q: path required for type=local", rs.Tag)
		}
		if !filepath.IsAbs(rs.Path) {
			return fmt.Errorf("rule_set %q: path must be absolute", rs.Tag)
		}
	default:
		return fmt.Errorf("rule_set %q: unknown type %q", rs.Tag, rs.Type)
	}
	return nil
}

// inlineRuleHasKnownField reports whether an inline rule_set rule has at
// least one recognised matcher/action key with a non-empty value. Mirrors
// sing-box's headline-rule schema (subset; extend if sing-box adds more).
func inlineRuleHasKnownField(rule map[string]any) bool {
	known := []string{
		"domain", "domain_suffix", "domain_keyword", "domain_regex",
		"ip_cidr", "source_ip_cidr", "port", "source_port",
		"process_name", "process_path", "package_name",
		"protocol", "network", "rule_set",
	}
	for _, k := range known {
		v, ok := rule[k]
		if !ok {
			continue
		}
		if inlineRuleValueNonEmpty(v) {
			return true
		}
	}
	return false
}

func inlineRuleValueNonEmpty(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case []string:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

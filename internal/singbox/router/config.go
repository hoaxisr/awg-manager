package router

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
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
	return cfg, nil
}

func SaveConfig(path string, cfg *RouterConfig) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func (c *RouterConfig) DeleteRuleSet(tag string, force bool) error {
	refs := c.rulesReferencingRuleSet(tag)
	if len(refs) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by rules %v", ErrRuleSetReferenced, tag, refs)
	}
	filtered := make([]RuleSet, 0, len(c.Route.RuleSet))
	for _, rs := range c.Route.RuleSet {
		if rs.Tag != tag {
			filtered = append(filtered, rs)
		}
	}
	c.Route.RuleSet = filtered
	return nil
}

func (c *RouterConfig) rulesReferencingRuleSet(tag string) []int {
	var refs []int
	for i, r := range c.Route.Rules {
		for _, rsTag := range r.RuleSet {
			if rsTag == tag {
				refs = append(refs, i)
				break
			}
		}
	}
	return refs
}

func (c *RouterConfig) AddRule(r Rule) error {
	if !r.hasAnyMatcher() && r.Action != "sniff" && r.Action != "hijack-dns" {
		return ErrInvalidMatchers
	}
	if err := validateRule(r); err != nil {
		return err
	}
	c.Route.Rules = append(c.Route.Rules, r)
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
	c.Route.Rules[index] = r
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

func (c *RouterConfig) EnsureSystemRules() {
	hasSniff := false
	hasHijack := false
	for _, r := range c.Route.Rules {
		if r.Action == "sniff" && !r.hasAnyMatcher() {
			hasSniff = true
		}
		if r.Action == "hijack-dns" && r.Protocol == "dns" {
			hasHijack = true
		}
	}
	prepend := make([]Rule, 0, 2)
	if !hasSniff {
		prepend = append(prepend, Rule{Action: "sniff"})
	}
	if !hasHijack {
		prepend = append(prepend, Rule{Protocol: "dns", Action: "hijack-dns"})
	}
	if len(prepend) > 0 {
		c.Route.Rules = append(prepend, c.Route.Rules...)
	}
}

func (c *RouterConfig) AddCompositeOutbound(o Outbound) error {
	for _, existing := range c.Outbounds {
		if existing.Tag == o.Tag {
			return fmt.Errorf("%w: %q", ErrOutboundTagConflict, o.Tag)
		}
	}
	c.Outbounds = append(c.Outbounds, o)
	return nil
}

func (c *RouterConfig) UpdateCompositeOutbound(tag string, o Outbound) error {
	for i, existing := range c.Outbounds {
		if existing.Tag == tag {
			c.Outbounds[i] = o
			return nil
		}
	}
	return fmt.Errorf("%w: %q not found", ErrOutboundTagConflict, tag)
}

func (c *RouterConfig) DeleteCompositeOutbound(tag string, force bool) error {
	refs := c.rulesReferencingOutbound(tag)
	if len(refs) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by rules %v", ErrOutboundReferenced, tag, refs)
	}
	filtered := make([]Outbound, 0, len(c.Outbounds))
	for _, o := range c.Outbounds {
		if o.Tag != tag {
			filtered = append(filtered, o)
		}
	}
	c.Outbounds = filtered
	return nil
}

func (c *RouterConfig) rulesReferencingOutbound(tag string) []int {
	var refs []int
	for i, r := range c.Route.Rules {
		if r.Outbound == tag {
			refs = append(refs, i)
		}
	}
	return refs
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

// stripLegacyAWGDirect filters out direct outbounds with bind_interface
// set — these used to be written here pre-refactor; now they live in
// 15-awg.json owned by awgoutbounds. Composite outbounds are kept.
func stripLegacyAWGDirect(in []Outbound) []Outbound {
	out := make([]Outbound, 0, len(in))
	for _, o := range in {
		if o.Type == "direct" && o.BindInterface != "" {
			continue
		}
		out = append(out, o)
	}
	return out
}

func (r Rule) hasAnyMatcher() bool {
	return len(r.DomainSuffix) > 0 || len(r.IPCIDR) > 0 || len(r.SourceIPCIDR) > 0 ||
		len(r.Port) > 0 || len(r.RuleSet) > 0 || r.Protocol != ""
}

func validateRule(r Rule) error {
	for _, cidr := range r.IPCIDR {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			if _, err := netip.ParseAddr(cidr); err != nil {
				return fmt.Errorf("ip_cidr %q: %w", cidr, err)
			}
		}
	}
	for _, cidr := range r.SourceIPCIDR {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			if _, err := netip.ParseAddr(cidr); err != nil {
				return fmt.Errorf("source_ip_cidr %q: %w", cidr, err)
			}
		}
	}
	for _, p := range r.Port {
		if p < 1 || p > 65535 {
			return fmt.Errorf("port %d out of range [1,65535]", p)
		}
	}
	return nil
}

func validateRuleSet(rs RuleSet) error {
	if rs.Tag == "" {
		return fmt.Errorf("rule_set tag is required")
	}
	switch rs.Type {
	case "remote":
		if rs.URL == "" {
			return fmt.Errorf("rule_set %q: url required for type=remote", rs.Tag)
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

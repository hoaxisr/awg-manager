package router

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

var validDNSTypes = map[string]bool{
	"udp":    true,
	"tls":    true,
	"https":  true,
	"quic":   true,
	"h3":     true,
	"local":  true,
	"fakeip": true,
}

var validDNSStrategies = map[string]bool{
	"":            true,
	"prefer_ipv4": true,
	"prefer_ipv6": true,
	"ipv4_only":   true,
	"ipv6_only":   true,
}

var validDNSRuleActions = map[string]bool{
	"":           true,
	"route":      true,
	"reject":     true,
	"predefined": true,
}

var validDNSRcodes = map[string]bool{
	"NOERROR": true, "FORMERR": true, "SERVFAIL": true,
	"NXDOMAIN": true, "NOTIMP": true, "REFUSED": true,
}

var validRejectMethods = map[string]bool{
	"": true, "default": true, "drop": true,
}

const managedDNSDirectTag = "dns-direct"

// scrubDNSServerDetourStored normalizes only values that must never be kept in
// the editable router config (explicit direct → empty). Legacy detour on
// dns-direct is preserved so the UI can warn until the user saves.
func scrubDNSServerDetourStored(s *DNSServer) {
	if strings.TrimSpace(s.Detour) == "direct" {
		s.Detour = ""
	}
}

// scrubDNSServerDetourForSingbox clears detour values that must not reach
// sing-box: empty, explicit "direct", and any detour on dns-direct.
func scrubDNSServerDetourForSingbox(s *DNSServer) {
	d := strings.TrimSpace(s.Detour)
	if d == "" || d == "direct" || s.Tag == managedDNSDirectTag {
		s.Detour = ""
	}
}

// SanitizeDNSConfigForSingbox prepares DNS servers for 20-router.json / sing-box.
func SanitizeDNSConfigForSingbox(cfg *RouterConfig) {
	if cfg == nil {
		return
	}
	for i := range cfg.DNS.Servers {
		scrubDNSServerDetourForSingbox(&cfg.DNS.Servers[i])
	}
}

// SanitizeDNSConfig normalizes stored router config after load (direct only).
func SanitizeDNSConfig(cfg *RouterConfig) {
	if cfg == nil {
		return
	}
	for i := range cfg.DNS.Servers {
		scrubDNSServerDetourStored(&cfg.DNS.Servers[i])
	}
}

func validateDNSServer(s DNSServer) error {
	if strings.TrimSpace(s.Tag) == "" {
		return fmt.Errorf("dns server tag is required")
	}
	if !validDNSTypes[s.Type] {
		return fmt.Errorf("dns server %q: unknown type %q", s.Tag, s.Type)
	}
	if s.Type == "fakeip" {
		if strings.TrimSpace(s.Inet4Range) == "" && strings.TrimSpace(s.Inet6Range) == "" {
			return fmt.Errorf("dns server %q: fakeip requires inet4_range or inet6_range", s.Tag)
		}
		if r := strings.TrimSpace(s.Inet4Range); r != "" {
			p, err := netip.ParsePrefix(r)
			if err != nil {
				return fmt.Errorf("dns server %q: invalid inet4_range %q: %w", s.Tag, r, err)
			}
			if !p.Addr().Is4() {
				return fmt.Errorf("dns server %q: inet4_range %q is not IPv4", s.Tag, r)
			}
		}
		if r := strings.TrimSpace(s.Inet6Range); r != "" {
			p, err := netip.ParsePrefix(r)
			if err != nil {
				return fmt.Errorf("dns server %q: invalid inet6_range %q: %w", s.Tag, r, err)
			}
			if p.Addr().Is4() {
				return fmt.Errorf("dns server %q: inet6_range %q is not IPv6", s.Tag, r)
			}
		}
		return nil
	}
	if s.Type != "local" && strings.TrimSpace(s.Server) == "" {
		return fmt.Errorf("dns server %q: server is required", s.Tag)
	}
	if s.ServerPort < 0 || s.ServerPort > 65535 {
		return fmt.Errorf("dns server %q: server_port %d out of range", s.Tag, s.ServerPort)
	}
	if !validDNSStrategies[s.Strategy] {
		return fmt.Errorf("dns server %q: unknown strategy %q", s.Tag, s.Strategy)
	}
	if s.DomainResolver != nil {
		if strings.TrimSpace(s.DomainResolver.Server) == "" {
			return fmt.Errorf("dns server %q: domain_resolver.server is required", s.Tag)
		}
		if !validDNSStrategies[s.DomainResolver.Strategy] {
			return fmt.Errorf("dns server %q: domain_resolver.strategy %q unknown", s.Tag, s.DomainResolver.Strategy)
		}
	}
	return nil
}

func validateDNSRule(r DNSRule, serverTags map[string]bool) error {
	if !dnsRuleHasMatcher(r) {
		return ErrInvalidMatchers
	}
	for _, c := range r.SourceIPCIDR {
		if err := validateCIDROrAddr("dns rule: invalid source_ip_cidr", c); err != nil {
			return err
		}
	}
	for _, rx := range r.DomainRegex {
		if _, err := regexp.Compile(rx); err != nil {
			return fmt.Errorf("dns rule: invalid domain_regex %q: %w", rx, err)
		}
	}
	if !validDNSRuleActions[r.Action] {
		return fmt.Errorf("dns rule: unknown action %q", r.Action)
	}
	switch r.Action {
	case "reject":
		if !validRejectMethods[r.RejectMethod] {
			return fmt.Errorf("dns rule: unknown reject method %q", r.RejectMethod)
		}
		return nil
	case "predefined":
		if r.Rcode != "" && !validDNSRcodes[r.Rcode] {
			return fmt.Errorf("dns rule: unknown rcode %q", r.Rcode)
		}
		return nil
	}
	// route (или пустое действие)
	if strings.TrimSpace(r.Server) == "" {
		return fmt.Errorf("dns rule: server is required when action is route")
	}
	if !serverTags[r.Server] {
		return fmt.Errorf("%w: %q", ErrDNSInvalidServer, r.Server)
	}
	return nil
}

func dnsRuleHasMatcher(r DNSRule) bool {
	return len(r.RuleSet) > 0 ||
		len(r.SourceIPCIDR) > 0 ||
		len(r.DomainSuffix) > 0 ||
		len(r.Domain) > 0 ||
		len(r.DomainKeyword) > 0 ||
		len(r.DomainRegex) > 0 ||
		len(r.QueryType) > 0
}

func (c *RouterConfig) dnsServerTags() map[string]bool {
	tags := make(map[string]bool, len(c.DNS.Servers))
	for _, s := range c.DNS.Servers {
		tags[s.Tag] = true
	}
	return tags
}

func (c *RouterConfig) AddDNSServer(s DNSServer) error {
	scrubDNSServerDetourForSingbox(&s)
	if err := validateDNSServer(s); err != nil {
		return err
	}
	for _, existing := range c.DNS.Servers {
		if existing.Tag == s.Tag {
			return fmt.Errorf("%w: %q", ErrDNSServerTagConflict, s.Tag)
		}
	}
	if s.DomainResolver != nil && !c.dnsServerTags()[s.DomainResolver.Server] && s.DomainResolver.Server != s.Tag {
		return fmt.Errorf("%w: domain_resolver.server %q not found", ErrDNSServerNotFound, s.DomainResolver.Server)
	}
	c.DNS.Servers = append(c.DNS.Servers, s)
	return nil
}

func (c *RouterConfig) UpdateDNSServer(tag string, s DNSServer) error {
	scrubDNSServerDetourForSingbox(&s)
	if err := validateDNSServer(s); err != nil {
		return err
	}
	idx := -1
	for i, existing := range c.DNS.Servers {
		if existing.Tag == tag {
			idx = i
			continue
		}
		if existing.Tag == s.Tag && tag != s.Tag {
			return fmt.Errorf("%w: %q", ErrDNSServerTagConflict, s.Tag)
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrDNSServerNotFound, tag)
	}
	if s.DomainResolver != nil {
		tags := c.dnsServerTags()
		delete(tags, tag)
		tags[s.Tag] = true
		if !tags[s.DomainResolver.Server] {
			return fmt.Errorf("%w: domain_resolver.server %q not found", ErrDNSServerNotFound, s.DomainResolver.Server)
		}
	}
	c.DNS.Servers[idx] = s
	if tag != s.Tag {
		c.renameDNSServerReferences(tag, s.Tag)
	}
	return nil
}

func (c *RouterConfig) renameDNSServerReferences(oldTag, newTag string) {
	for i := range c.DNS.Rules {
		if c.DNS.Rules[i].Server == oldTag {
			c.DNS.Rules[i].Server = newTag
		}
	}
	for i := range c.DNS.Servers {
		if c.DNS.Servers[i].DomainResolver != nil && c.DNS.Servers[i].DomainResolver.Server == oldTag {
			c.DNS.Servers[i].DomainResolver.Server = newTag
		}
	}
	if c.DNS.Final == oldTag {
		c.DNS.Final = newTag
	}
}

func (c *RouterConfig) DeleteDNSServer(tag string, force bool) error {
	refs := c.dnsServerReferences(tag)
	if len(refs) > 0 && !force {
		return fmt.Errorf("%w: %q referenced by %s", ErrDNSServerReferenced, tag, strings.Join(refs, ", "))
	}
	filtered := make([]DNSServer, 0, len(c.DNS.Servers))
	for _, s := range c.DNS.Servers {
		if s.Tag != tag {
			filtered = append(filtered, s)
		}
	}
	c.DNS.Servers = filtered
	if force {
		rules := make([]DNSRule, 0, len(c.DNS.Rules))
		for _, r := range c.DNS.Rules {
			if r.Server == tag {
				continue
			}
			rules = append(rules, r)
		}
		c.DNS.Rules = rules
		for i := range c.DNS.Servers {
			if c.DNS.Servers[i].DomainResolver != nil && c.DNS.Servers[i].DomainResolver.Server == tag {
				c.DNS.Servers[i].DomainResolver = nil
			}
		}
		if c.DNS.Final == tag {
			c.DNS.Final = ""
		}
	}
	return nil
}

func (c *RouterConfig) dnsServerReferences(tag string) []string {
	var refs []string
	for i, r := range c.DNS.Rules {
		if r.Server == tag {
			refs = append(refs, fmt.Sprintf("rule[%d]", i))
		}
	}
	for _, s := range c.DNS.Servers {
		if s.Tag == tag {
			continue
		}
		if s.DomainResolver != nil && s.DomainResolver.Server == tag {
			refs = append(refs, fmt.Sprintf("server[%s].domain_resolver", s.Tag))
		}
	}
	if c.DNS.Final == tag {
		refs = append(refs, "final")
	}
	return refs
}

func (c *RouterConfig) AddDNSRule(r DNSRule) error {
	if err := validateDNSRule(r, c.dnsServerTags()); err != nil {
		return err
	}
	c.DNS.Rules = append(c.DNS.Rules, r)
	return nil
}

func (c *RouterConfig) UpdateDNSRule(index int, r DNSRule) error {
	if index < 0 || index >= len(c.DNS.Rules) {
		return ErrDNSRuleIndexOutOfRange
	}
	if err := validateDNSRule(r, c.dnsServerTags()); err != nil {
		return err
	}
	c.DNS.Rules[index] = r
	return nil
}

func (c *RouterConfig) DeleteDNSRule(index int) error {
	if index < 0 || index >= len(c.DNS.Rules) {
		return ErrDNSRuleIndexOutOfRange
	}
	c.DNS.Rules = append(c.DNS.Rules[:index], c.DNS.Rules[index+1:]...)
	return nil
}

func (c *RouterConfig) MoveDNSRule(from, to int) error {
	n := len(c.DNS.Rules)
	if from < 0 || from >= n || to < 0 || to >= n {
		return ErrDNSRuleIndexOutOfRange
	}
	if from == to {
		return nil
	}
	r := c.DNS.Rules[from]
	without := append(c.DNS.Rules[:from:from], c.DNS.Rules[from+1:]...)
	rules := make([]DNSRule, 0, n)
	rules = append(rules, without[:to]...)
	rules = append(rules, r)
	rules = append(rules, without[to:]...)
	c.DNS.Rules = rules
	return nil
}

// MoveDNSServer reorders the DNS server at index `from` to index `to`.
// ponytail: server order is cosmetic — sing-box references servers by tag;
// endpoint exists only for UX-consistent reordering.
func (c *RouterConfig) MoveDNSServer(from, to int) error {
	n := len(c.DNS.Servers)
	if from < 0 || from >= n || to < 0 || to >= n {
		return ErrDNSServerIndexOutOfRange
	}
	if from == to {
		return nil
	}
	s := c.DNS.Servers[from]
	without := append(c.DNS.Servers[:from:from], c.DNS.Servers[from+1:]...)
	servers := make([]DNSServer, 0, n)
	servers = append(servers, without[:to]...)
	servers = append(servers, s)
	servers = append(servers, without[to:]...)
	c.DNS.Servers = servers
	return nil
}

func (c *RouterConfig) SetDNSGlobals(final, strategy string) error {
	if final != "" && !c.dnsServerTags()[final] {
		return fmt.Errorf("%w: final %q", ErrDNSServerNotFound, final)
	}
	if !validDNSStrategies[strategy] {
		return fmt.Errorf("dns: unknown strategy %q", strategy)
	}
	c.DNS.Final = final
	c.DNS.Strategy = strategy
	return nil
}

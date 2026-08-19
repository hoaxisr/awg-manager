package router

import (
	"context"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// FakeIPConfigService — isolated fakeip-tun CRUD surface (SlotFakeIP)
//
// All 26 methods mirror the tproxy router CRUD (service.go / service_dns.go)
// but route exclusively through fakeipWithConfig / loadFakeIPConfig instead of
// withConfig / loadRouterConfig. The pure RouterConfig mutation methods are
// shared and unchanged.
//
// SSE event labels match the router versions so the frontend can reuse the
// same event names on both config paths.
// ---------------------------------------------------------------------------

// --- DNS servers ---

func (s *ServiceImpl) FakeIPListDNSServers(ctx context.Context) ([]DNSServer, error) {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return nil, err
	}
	return cfg.DNS.Servers, nil
}

func (s *ServiceImpl) FakeIPAddDNSServer(ctx context.Context, srv DNSServer) error {
	return s.fakeipWithConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.AddDNSServer(srv) })
}

func (s *ServiceImpl) FakeIPUpdateDNSServer(ctx context.Context, tag string, srv DNSServer) error {
	return s.fakeipWithConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.UpdateDNSServer(tag, srv) })
}

func (s *ServiceImpl) FakeIPDeleteDNSServer(ctx context.Context, tag string, force bool) error {
	return s.fakeipWithConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.DeleteDNSServer(tag, force) })
}

func (s *ServiceImpl) FakeIPMoveDNSServer(ctx context.Context, from, to int) error {
	return s.fakeipWithConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.MoveDNSServer(from, to) })
}

// --- DNS rules ---

func (s *ServiceImpl) FakeIPListDNSRules(ctx context.Context) ([]DNSRule, error) {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return nil, err
	}
	return s.ruleSetMaterializer().restoreConfig(cfg).DNS.Rules, nil
}

func (s *ServiceImpl) FakeIPAddDNSRule(ctx context.Context, r DNSRule) error {
	return s.fakeipWithConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.AddDNSRule(r) })
}

func (s *ServiceImpl) FakeIPUpdateDNSRule(ctx context.Context, index int, r DNSRule) error {
	return s.fakeipWithConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.UpdateDNSRule(index, r) })
}

func (s *ServiceImpl) FakeIPDeleteDNSRule(ctx context.Context, index int) error {
	return s.fakeipWithConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.DeleteDNSRule(index) })
}

func (s *ServiceImpl) FakeIPMoveDNSRule(ctx context.Context, from, to int) error {
	return s.fakeipWithConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.MoveDNSRule(from, to) })
}

// --- DNS globals ---

func (s *ServiceImpl) FakeIPGetDNSGlobals(ctx context.Context) (string, string, error) {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return "", "", err
	}
	return cfg.DNS.Final, cfg.DNS.Strategy, nil
}

func (s *ServiceImpl) FakeIPSetDNSGlobals(ctx context.Context, final, strategy string) error {
	return s.fakeipWithConfig(ctx, "dns-globals", func(c *RouterConfig) error { return c.SetDNSGlobals(final, strategy) })
}

// --- Route rules ---

func (s *ServiceImpl) FakeIPListRules(ctx context.Context) ([]Rule, error) {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return nil, err
	}
	return s.ruleSetMaterializer().restoreConfig(cfg).Route.Rules, nil
}

func (s *ServiceImpl) FakeIPAddRule(ctx context.Context, r Rule) error {
	return s.fakeipWithConfig(ctx, "rules", func(c *RouterConfig) error { return c.AddRule(r) })
}

func (s *ServiceImpl) FakeIPUpdateRule(ctx context.Context, index int, r Rule) error {
	return s.fakeipWithConfig(ctx, "rules", func(c *RouterConfig) error { return c.UpdateRule(index, r) })
}

func (s *ServiceImpl) FakeIPDeleteRule(ctx context.Context, index int) error {
	return s.fakeipWithConfig(ctx, "rules", func(c *RouterConfig) error { return c.DeleteRule(index) })
}

func (s *ServiceImpl) FakeIPMoveRule(ctx context.Context, from, to int) error {
	return s.fakeipWithConfig(ctx, "rules", func(c *RouterConfig) error { return c.MoveRule(from, to) })
}

// FakeIPBulkSetRuleOutbound is the fakeip-tun (SlotFakeIP) counterpart of
// BulkSetRuleOutbound — same validation, routed through fakeipWithConfig.
func (s *ServiceImpl) FakeIPBulkSetRuleOutbound(ctx context.Context, indices []int, outbound string) error {
	return s.fakeipWithConfig(ctx, "rules", func(c *RouterConfig) error {
		return bulkSetRuleOutbound(c, indices, outbound, func(t string) bool { return s.isKnownOutboundTag(ctx, t, c) })
	})
}

// --- Route final ---

// FakeIPSetRouteFinal sets route.final on the fakeip config slot.
// Validates that tag is a known outbound tag (mirrors SetRouteFinal exactly).
func (s *ServiceImpl) FakeIPSetRouteFinal(ctx context.Context, tag string) error {
	return s.fakeipWithConfig(ctx, "route", func(c *RouterConfig) error {
		if !s.isKnownOutboundTag(ctx, tag, c) {
			return fmt.Errorf("unknown outbound tag %q for route.final", tag)
		}
		return c.SetRouteFinal(tag)
	})
}

// --- Rule sets ---

func (s *ServiceImpl) FakeIPListRuleSets(ctx context.Context) ([]RuleSet, error) {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return nil, err
	}
	restored := s.ruleSetMaterializer().restoreConfig(cfg)
	return restored.Route.RuleSet, nil
}

// FakeIPAddRuleSet adds a rule set to the fakeip config slot.
// Mirrors AddRuleSet defaulting of Type/Format/UpdateInterval.
func (s *ServiceImpl) FakeIPAddRuleSet(ctx context.Context, rs RuleSet) error {
	if rs.Type == "" {
		rs.Type = "remote"
	}
	if rs.Format == "" && rs.Type != "inline" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" && rs.Type == "remote" {
		rs.UpdateInterval = "24h"
	}
	return s.fakeipWithConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.AddRuleSet(rs) })
}

// FakeIPUpdateRuleSet updates a rule set in the fakeip config slot.
// Mirrors UpdateRuleSet defaulting of Type/Format/UpdateInterval.
func (s *ServiceImpl) FakeIPUpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error {
	if rs.Type == "" {
		rs.Type = "remote"
	}
	if rs.Format == "" && rs.Type != "inline" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" && rs.Type == "remote" {
		rs.UpdateInterval = "24h"
	}
	return s.fakeipWithConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.UpdateRuleSet(tag, rs) })
}

// FakeIPBulkSetRuleSetDetour is the fakeip-tun (SlotFakeIP) counterpart of
// BulkSetRuleSetDetour — same validation, routed through fakeipWithConfig.
func (s *ServiceImpl) FakeIPBulkSetRuleSetDetour(ctx context.Context, tags []string, detour string) error {
	return s.fakeipWithConfig(ctx, "rulesets", func(c *RouterConfig) error {
		return bulkSetRuleSetDetour(c, tags, detour, func(t string) bool { return s.isKnownOutboundTag(ctx, t, c) })
	})
}

func (s *ServiceImpl) FakeIPDeleteRuleSet(ctx context.Context, tag string, force bool) error {
	inlineTag := tag
	if base, ok := inlineTagFromSRSTag(tag); ok {
		inlineTag = base
	}
	return s.fakeipWithConfig(ctx, "rulesets", func(c *RouterConfig) error {
		if err := c.DeleteRuleSet(inlineTag, force); err != nil {
			return err
		}
		if s.deps.Orch == nil {
			s.ruleSetMaterializer().removeInlineArtifacts(inlineTag)
		}
		return nil
	})
}

// --- Composite outbounds ---

func (s *ServiceImpl) FakeIPListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error) {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return nil, err
	}
	own := cfg.CompositeOutbounds()
	out := make([]CompositeOutboundView, 0, len(own))
	for _, o := range own {
		out = append(out, CompositeOutboundView{Outbound: o, Source: "router"})
	}
	if s.deps.SubscriptionComposites != nil {
		for _, o := range s.deps.SubscriptionComposites.ListSubscriptionComposites() {
			out = append(out, CompositeOutboundView{Outbound: o, Source: "subscription"})
		}
	}
	return out, nil
}

func (s *ServiceImpl) FakeIPAddCompositeOutbound(ctx context.Context, o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		if err := s.validateBindInterface(ctx, o.BindInterface); err != nil {
			return err
		}
	}
	return s.fakeipWithConfig(ctx, "outbounds", func(c *RouterConfig) error {
		if err := s.validateCompositeMembers(ctx, o, c); err != nil {
			return err
		}
		return c.AddCompositeOutbound(o)
	})
}

func (s *ServiceImpl) FakeIPUpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		if err := s.validateBindInterface(ctx, o.BindInterface); err != nil {
			return err
		}
	}
	return s.fakeipWithConfig(ctx, "outbounds", func(c *RouterConfig) error {
		if err := s.validateCompositeMembers(ctx, o, c); err != nil {
			return err
		}
		return c.UpdateCompositeOutbound(tag, o)
	})
}

// validateCompositeMembers отклоняет selector/urltest с member-тегами,
// которых нет ни в одном каталоге (слотовые выходы, subscription-композиты,
// AWG-теги, sing-box туннели, builtins). Молча сохранённый мёртвый член
// валит enable fakeip-tun кросс-слот валидацией с откатом в «Выключен»,
// не объясняя пользователю, что чинить (#567).
func (s *ServiceImpl) validateCompositeMembers(ctx context.Context, o Outbound, c *RouterConfig) error {
	switch strings.ToLower(o.Type) {
	case "selector", "urltest":
	default:
		return nil
	}
	var unknown []string
	for _, m := range o.Outbounds {
		if m == o.Tag || s.isKnownOutboundTag(ctx, m, c) {
			continue
		}
		unknown = append(unknown, m)
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s — такие выходы больше не существуют (туннель пересоздан или переименован), выберите членов заново", ErrCompositeMemberUnknown, strings.Join(unknown, ", "))
}

func (s *ServiceImpl) FakeIPDeleteCompositeOutbound(ctx context.Context, tag string, force bool) error {
	return s.fakeipWithConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.DeleteCompositeOutbound(tag, force) })
}

// ---------------------------------------------------------------------------
// FakeIPConfigService interface
// ---------------------------------------------------------------------------

// FakeIPConfigService is the isolated fakeip-tun config CRUD surface
// (SlotFakeIP), parallel to Service's tproxy CRUD (SlotRouter).
// Implemented by *ServiceImpl.
type FakeIPConfigService interface {
	// DNS servers
	FakeIPListDNSServers(ctx context.Context) ([]DNSServer, error)
	FakeIPAddDNSServer(ctx context.Context, srv DNSServer) error
	FakeIPUpdateDNSServer(ctx context.Context, tag string, srv DNSServer) error
	FakeIPDeleteDNSServer(ctx context.Context, tag string, force bool) error
	FakeIPMoveDNSServer(ctx context.Context, from, to int) error

	// DNS rules
	FakeIPListDNSRules(ctx context.Context) ([]DNSRule, error)
	FakeIPAddDNSRule(ctx context.Context, r DNSRule) error
	FakeIPUpdateDNSRule(ctx context.Context, index int, r DNSRule) error
	FakeIPDeleteDNSRule(ctx context.Context, index int) error
	FakeIPMoveDNSRule(ctx context.Context, from, to int) error

	// DNS globals
	FakeIPGetDNSGlobals(ctx context.Context) (final, strategy string, err error)
	FakeIPSetDNSGlobals(ctx context.Context, final, strategy string) error

	// Route rules
	FakeIPListRules(ctx context.Context) ([]Rule, error)
	FakeIPAddRule(ctx context.Context, r Rule) error
	FakeIPUpdateRule(ctx context.Context, index int, r Rule) error
	FakeIPDeleteRule(ctx context.Context, index int) error
	FakeIPMoveRule(ctx context.Context, from, to int) error
	FakeIPBulkSetRuleOutbound(ctx context.Context, indices []int, outbound string) error

	// Route final
	FakeIPSetRouteFinal(ctx context.Context, tag string) error

	// Rule sets
	FakeIPListRuleSets(ctx context.Context) ([]RuleSet, error)
	FakeIPAddRuleSet(ctx context.Context, rs RuleSet) error
	FakeIPUpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error
	FakeIPDeleteRuleSet(ctx context.Context, tag string, force bool) error
	FakeIPBulkSetRuleSetDetour(ctx context.Context, tags []string, detour string) error

	// Composite outbounds
	FakeIPListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error)
	FakeIPAddCompositeOutbound(ctx context.Context, o Outbound) error
	FakeIPUpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error
	FakeIPDeleteCompositeOutbound(ctx context.Context, tag string, force bool) error
}

// Compile-time assertion: *ServiceImpl must satisfy FakeIPConfigService.
var _ FakeIPConfigService = (*ServiceImpl)(nil)

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

// --- Общее содержимое: правила, наборы, композиты, route.final ---
//
// Всё это переехало в ОБЩИЙ слот 21-routing.json (подэтап 5D0): режимный слот
// несёт только захват трафика и DNS-механизм fakeip. Методы ниже оставлены
// тонкими делегатами к общему CRUD, чтобы ручки и фронт fakeip продолжали
// работать до их удаления; поведение (валидация, staging, события) —
// в точности общее.
//
// ГОЧА: общий CRUD пишет в pending/ (staging), а не напрямую, как писал
// fakeip-слот. Правка видна в списках сразу (LoadEffective читает черновик),
// но в живой конфиг попадает только после «Применить».

func (s *ServiceImpl) FakeIPListRules(ctx context.Context) ([]Rule, error) {
	return s.ListRules(ctx)
}

func (s *ServiceImpl) FakeIPAddRule(ctx context.Context, r Rule) error {
	return s.AddRule(ctx, r)
}

func (s *ServiceImpl) FakeIPUpdateRule(ctx context.Context, index int, r Rule) error {
	return s.UpdateRule(ctx, index, r)
}

func (s *ServiceImpl) FakeIPDeleteRule(ctx context.Context, index int) error {
	return s.DeleteRule(ctx, index)
}

func (s *ServiceImpl) FakeIPMoveRule(ctx context.Context, from, to int) error {
	return s.MoveRule(ctx, from, to)
}

func (s *ServiceImpl) FakeIPBulkSetRuleOutbound(ctx context.Context, indices []int, outbound string) error {
	return s.BulkSetRuleOutbound(ctx, indices, outbound)
}

func (s *ServiceImpl) FakeIPSetRouteFinal(ctx context.Context, tag string) error {
	return s.SetRouteFinal(ctx, tag)
}

func (s *ServiceImpl) FakeIPListRuleSets(ctx context.Context) ([]RuleSet, error) {
	return s.ListRuleSets(ctx)
}

func (s *ServiceImpl) FakeIPAddRuleSet(ctx context.Context, rs RuleSet) error {
	return s.AddRuleSet(ctx, rs)
}

func (s *ServiceImpl) FakeIPUpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error {
	return s.UpdateRuleSet(ctx, tag, rs)
}

func (s *ServiceImpl) FakeIPBulkSetRuleSetDetour(ctx context.Context, tags []string, detour string) error {
	return s.BulkSetRuleSetDetour(ctx, tags, detour)
}

func (s *ServiceImpl) FakeIPDeleteRuleSet(ctx context.Context, tag string, force bool) error {
	return s.DeleteRuleSet(ctx, tag, force)
}

func (s *ServiceImpl) FakeIPListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error) {
	return s.ListCompositeOutbounds(ctx)
}

func (s *ServiceImpl) FakeIPAddCompositeOutbound(ctx context.Context, o Outbound) error {
	return s.AddCompositeOutbound(ctx, o)
}

func (s *ServiceImpl) FakeIPUpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error {
	return s.UpdateCompositeOutbound(ctx, tag, o)
}

func (s *ServiceImpl) FakeIPDeleteCompositeOutbound(ctx context.Context, tag string, force bool) error {
	return s.DeleteCompositeOutbound(ctx, tag, force)
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

// ---------------------------------------------------------------------------
// FakeIPConfigService interface
// ---------------------------------------------------------------------------

// FakeIPConfigService is the isolated fakeip-tun config CRUD surface
// (SlotFakeIP), parallel to Service's tproxy CRUD (SlotRouting).
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

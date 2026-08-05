package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// ---------------------------------------------------------------------------
// FakeIPConfigService — DNS-механизм режимного слота fakeip-tun (SlotFakeIP)
//
// Правила, наборы и композиты живут в общем слоте 21-routing.json и правятся
// общим CRUD (service.go): режимный слот несёт только захват трафика и DNS.
// Методы ниже зеркалят DNS-CRUD роутера (service_dns.go), но ходят через
// fakeipWithConfig / loadFakeIPConfig вместо withConfig / loadRouterConfig.
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
	return s.fakeipWithConfig(ctx, "dns-servers", func(c *RouterConfig) error {
		return c.addDNSServer(srv, s.externalDNSServerTags(orchestrator.SlotFakeIP))
	})
}

func (s *ServiceImpl) FakeIPUpdateDNSServer(ctx context.Context, tag string, srv DNSServer) error {
	return s.fakeipWithConfig(ctx, "dns-servers", func(c *RouterConfig) error {
		return c.updateDNSServer(tag, srv, s.externalDNSServerTags(orchestrator.SlotFakeIP))
	})
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
}

// Compile-time assertion: *ServiceImpl must satisfy FakeIPConfigService.
var _ FakeIPConfigService = (*ServiceImpl)(nil)

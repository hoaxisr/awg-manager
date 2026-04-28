package router

import "context"

func (s *ServiceImpl) ListDNSServers(ctx context.Context) ([]DNSServer, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return cfg.DNS.Servers, nil
}

func (s *ServiceImpl) AddDNSServer(ctx context.Context, srv DNSServer) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.AddDNSServer(srv) })
}

func (s *ServiceImpl) UpdateDNSServer(ctx context.Context, tag string, srv DNSServer) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.UpdateDNSServer(tag, srv) })
}

func (s *ServiceImpl) DeleteDNSServer(ctx context.Context, tag string, force bool) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.DeleteDNSServer(tag, force) })
}

func (s *ServiceImpl) ListDNSRules(ctx context.Context) ([]DNSRule, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return cfg.DNS.Rules, nil
}

func (s *ServiceImpl) AddDNSRule(ctx context.Context, r DNSRule) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.AddDNSRule(r) })
}

func (s *ServiceImpl) UpdateDNSRule(ctx context.Context, index int, r DNSRule) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.UpdateDNSRule(index, r) })
}

func (s *ServiceImpl) DeleteDNSRule(ctx context.Context, index int) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.DeleteDNSRule(index) })
}

func (s *ServiceImpl) MoveDNSRule(ctx context.Context, from, to int) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error { return c.MoveDNSRule(from, to) })
}

func (s *ServiceImpl) GetDNSGlobals(ctx context.Context) (string, string, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return "", "", err
	}
	return cfg.DNS.Final, cfg.DNS.Strategy, nil
}

func (s *ServiceImpl) SetDNSGlobals(ctx context.Context, final, strategy string) error {
	return s.withConfig(ctx, "dns-globals", func(c *RouterConfig) error { return c.SetDNSGlobals(final, strategy) })
}

package router

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

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
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error {
		if err := c.UpdateDNSServer(tag, srv); err != nil {
			return err
		}
		return s.captureDNSChainServerRename(tag, srv.Tag)
	})
}

func (s *ServiceImpl) DeleteDNSServer(ctx context.Context, tag string, force bool) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.DeleteDNSServer(tag, force) })
}

func (s *ServiceImpl) MoveDNSServer(ctx context.Context, from, to int) error {
	return s.withConfig(ctx, "dns-servers", func(c *RouterConfig) error { return c.MoveDNSServer(from, to) })
}

func (s *ServiceImpl) ListDNSRules(ctx context.Context) ([]DNSRule, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return s.ruleSetMaterializer().restoreConfig(cfg).DNS.Rules, nil
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

func (s *ServiceImpl) GetDNSGlobals(ctx context.Context) (string, string, string, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return "", "", "", err
	}
	return cfg.DNS.Final, cfg.DNS.Strategy, cfg.DNS.Timeout, nil
}

func (s *ServiceImpl) SetDNSGlobals(ctx context.Context, final, strategy, timeout string) error {
	return s.withConfig(ctx, "dns-globals", func(c *RouterConfig) error { return c.SetDNSGlobals(final, strategy, timeout) })
}

// GetDNSChainPreset возвращает состояние DNS-пресета (Mode "" = выключен).
func (s *ServiceImpl) GetDNSChainPreset(_ context.Context) (storage.DNSChainPresetState, error) {
	st, err := s.dnsChainPresetState()
	if err != nil || st == nil {
		return storage.DNSChainPresetState{}, err
	}
	return *st, nil
}

// SetDNSChainPreset включает/переключает/выключает DNS-пресет: валидирует st в
// контексте текущего конфига, сохраняет состояние и переассертит цепочку.
//
// Состояние пишется ДО persist'а конфига (осознанный trade-off staging-пути):
// пресет применяется сразу, а цепочка появится в active после Apply. Если
// пользователь сделает Discard, состояние останется — и цепочка вернётся при
// следующей мутации через ensure-хук withConfig.
func (s *ServiceImpl) SetDNSChainPreset(ctx context.Context, st storage.DNSChainPresetState) error {
	return s.withConfig(ctx, "dns-rules", func(c *RouterConfig) error {
		var stored *storage.DNSChainPresetState
		if st.Mode != "" {
			if _, err := dnsChainRules(c, &st); err != nil {
				return err
			}
			stored = &st
		}
		if err := s.deps.Settings.SetDNSChainPresetState(stored); err != nil {
			return fmt.Errorf("dns-пресет: save settings: %w", err)
		}
		// Выключение снимает цепочку именно здесь: ensure-хук withConfig при
		// пустом Mode — no-op. Для активного пресета хук повторит ensure
		// идемпотентно.
		return ensureDNSChainOverlay(c, stored)
	})
}

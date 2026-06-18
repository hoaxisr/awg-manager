package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// loadFakeIPConfig returns the fakeip-tun RouterConfig the user is currently
// editing. When the orchestrator is wired, it delegates to LoadEffective which
// prefers pending/ over active/ so UI callers always see the latest draft.
// Fakeip is orch-only in practice; when Orch is nil we return an empty config.
// ponytail: no legacy path for fakeip — it is orch-only from day one.
func (s *ServiceImpl) loadFakeIPConfig() (*RouterConfig, error) {
	if s.deps.Orch != nil {
		data, err := s.deps.Orch.LoadEffective(orchestrator.SlotFakeIP)
		if err != nil {
			return nil, fmt.Errorf("load fakeip config: %w", err)
		}
		if data == nil {
			return NewEmptyConfig(), nil
		}
		cfg := NewEmptyConfig()
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse fakeip config: %w", err)
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
	// Orch-nil: fakeip is not available without the orchestrator.
	return NewEmptyConfig(), nil
}

// persistFakeIPConfig materializes, validates and saves a fakeip RouterConfig
// directly to the active path (21-fakeip.json) via Orch.Save. It mirrors
// persistConfigDirect but targets SlotFakeIP instead of SlotRouter.
// Byte-equal short-circuit: if the serialized bytes match what is already on
// disk we skip the write (and the debounced reload it would trigger).
func (s *ServiceImpl) persistFakeIPConfig(ctx context.Context, cfg *RouterConfig) error {
	if s.deps.Orch == nil {
		// Orch-nil: test-only, nothing to persist.
		return nil
	}
	materialized, err := s.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		return err
	}
	if err := validateNoCompositeCycles(materialized.Outbounds); err != nil {
		return err
	}
	data, err := json.MarshalIndent(materialized, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal fakeip config: %w", err)
	}
	activePath := filepath.Join(s.deps.Orch.ConfigDir(), "21-fakeip.json")
	if existing, err := os.ReadFile(activePath); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := s.deps.Orch.Save(orchestrator.SlotFakeIP, data); err != nil {
		return err
	}
	return nil
}

// ensureFakeIPOverlayFromState loads settings and re-asserts all engine-locked
// bits into cfg via ensureFakeIPOverlay. Called on every persist so the overlay
// always wins over a user edit that touched a locked field.
func (s *ServiceImpl) ensureFakeIPOverlayFromState(cfg *RouterConfig) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return fmt.Errorf("fakeip overlay: load settings: %w", err)
	}
	if settings == nil || settings.FakeIP == nil {
		return fmt.Errorf("fakeip overlay: FakeIPState not provisioned (nil)")
	}
	p := resolveFakeIPParams(s.deps.FakeIPTun, settings.SingboxRouter)
	spec := FakeIPTunSpec{
		Iface:      fakeIPIfaceName(settings.FakeIP.Index),
		TunAddr4:   p.TunAddr4,
		TunAddr6:   p.TunAddr6,
		MTU:        p.MTU,
		Inet4Range: p.Inet4Range,
		Inet6Range: p.Inet6Range,
		CachePath:  p.CachePath,
		RealServer: p.RealServer,
		Stack:      settings.SingboxRouter.FakeIPStack,
	}
	ensureFakeIPOverlay(cfg, spec)
	return nil
}

// fakeipWithConfig is the isolated load→restore→mutate→overlay→persist→emit
// skeleton for the fakeip-tun config slot. It mirrors withConfig but:
//   - loads/persists SlotFakeIP (not SlotRouter),
//   - inserts ensureFakeIPOverlayFromState after the user mutation so
//     engine-locked bits always win on every write.
func (s *ServiceImpl) fakeipWithConfig(ctx context.Context, event string, fn func(*RouterConfig) error) error {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return err
	}
	cfg = s.ruleSetMaterializer().restoreConfig(cfg)
	if err := fn(cfg); err != nil {
		return err
	}
	if err := s.ensureFakeIPOverlayFromState(cfg); err != nil {
		return err
	}
	if err := s.persistFakeIPConfig(ctx, cfg); err != nil {
		return err
	}
	s.emitCfgEvent(event, cfg)
	return nil
}

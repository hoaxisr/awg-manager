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

// cloneRouterConfig returns a deep copy of cfg via a JSON round-trip.
// ponytail: json round-trip deep copy — config is already JSON-serializable, no hand-written Clone.
func cloneRouterConfig(cfg *RouterConfig) (*RouterConfig, error) {
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("cloneRouterConfig: marshal: %w", err)
	}
	clone := NewEmptyConfig()
	if err := json.Unmarshal(data, clone); err != nil {
		return nil, fmt.Errorf("cloneRouterConfig: unmarshal: %w", err)
	}
	return clone, nil
}

// guardFakeIPLocked checks whether the user's mutation (from before to after)
// clobbered any engine-locked fakeip-tun field. It is diff-based: a fresh slot
// where before has no locked bits yet (first provision) passes through so the
// overlay can create them. Only an established config that LOSES or CHANGES a
// locked bit is rejected.
//
// All errors wrap ErrFakeIPLockedField so errors.Is(err, ErrFakeIPLockedField)
// holds upstream and the HTTP layer can map them to 4xx.
func guardFakeIPLocked(before, after *RouterConfig) error {
	// 1. real DNS server deleted/renamed.
	if hasDNSServerByTag(before, "real") && !hasDNSServerByTag(after, "real") {
		return fmt.Errorf("%w: dns server \"real\" is engine-locked (managed by fakeip-tun, cannot be deleted or renamed)", ErrFakeIPLockedField)
	}

	// 2. fakeip DNS server deleted.
	if hasDNSServerByType(before, "fakeip") && !hasDNSServerByType(after, "fakeip") {
		return fmt.Errorf("%w: dns server with type \"fakeip\" is engine-locked (managed by fakeip-tun, cannot be deleted)", ErrFakeIPLockedField)
	}

	// 3. hijack-dns rule removed.
	if hasHijackDNSRule(before) && !hasHijackDNSRule(after) {
		return fmt.Errorf("%w: route rule \"hijack-dns\" is engine-locked (managed by fakeip-tun, cannot be removed)", ErrFakeIPLockedField)
	}

	// 4. dns.final changed.
	if before.DNS.Final != "" && after.DNS.Final != before.DNS.Final {
		return fmt.Errorf("%w: dns.final is engine-locked to %q (managed by fakeip-tun, cannot be changed)", ErrFakeIPLockedField, before.DNS.Final)
	}

	// 5. default_domain_resolver changed.
	if before.Route.DefaultDomainResolver != nil {
		if after.Route.DefaultDomainResolver == nil || after.Route.DefaultDomainResolver.Server != before.Route.DefaultDomainResolver.Server {
			return fmt.Errorf("%w: route.default_domain_resolver is engine-locked (managed by fakeip-tun, cannot be changed or removed)", ErrFakeIPLockedField)
		}
	}

	return nil
}

// hasDNSServerByTag reports whether cfg has at least one DNSServer with the
// given Tag.
func hasDNSServerByTag(cfg *RouterConfig, tag string) bool {
	for _, sv := range cfg.DNS.Servers {
		if sv.Tag == tag {
			return true
		}
	}
	return false
}

// hasDNSServerByType reports whether cfg has at least one DNSServer with the
// given Type.
func hasDNSServerByType(cfg *RouterConfig, typ string) bool {
	for _, sv := range cfg.DNS.Servers {
		if sv.Type == typ {
			return true
		}
	}
	return false
}

// hasHijackDNSRule reports whether cfg has at least one Route Rule with
// Action=="hijack-dns".
func hasHijackDNSRule(cfg *RouterConfig) bool {
	for _, r := range cfg.Route.Rules {
		if r.Action == "hijack-dns" {
			return true
		}
	}
	return false
}

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

// fakeIPConfigEmpty reports whether cfg carries no user routing intent —
// i.e. neither DNS nor route rules have been authored and route.final is
// still the system default ("direct" or unset). Used by enableFakeIPTun to
// decide whether to seed a starter DNS rule on first enable.
func fakeIPConfigEmpty(cfg *RouterConfig) bool {
	return len(cfg.Route.Rules) == 0 && len(cfg.DNS.Rules) == 0 &&
		(cfg.Route.Final == "" || cfg.Route.Final == "direct")
}

// fakeipWithConfig is the isolated load→restore→clone→mutate→guard→overlay→persist→emit
// skeleton for the fakeip-tun config slot. It mirrors withConfig but:
//   - loads/persists SlotFakeIP (not SlotRouter),
//   - snapshots `before` (deep copy) after restore so guardFakeIPLocked can
//     diff the pre-mutation state against the user's edit,
//   - rejects edits that clobber engine-locked bits via guardFakeIPLocked,
//   - inserts ensureFakeIPOverlayFromState after the guard so locked bits
//     always win on every write.
func (s *ServiceImpl) fakeipWithConfig(ctx context.Context, event string, fn func(*RouterConfig) error) error {
	cfg, err := s.loadFakeIPConfig()
	if err != nil {
		return err
	}
	cfg = s.ruleSetMaterializer().restoreConfig(cfg)
	before, err := cloneRouterConfig(cfg) // deep copy via json round-trip
	if err != nil {
		return err
	}
	if err := fn(cfg); err != nil {
		return err
	}
	if err := guardFakeIPLocked(before, cfg); err != nil {
		return err
	}
	if err := s.ensureFakeIPOverlayFromState(cfg); err != nil {
		return err
	}
	if err := s.persistFakeIPConfig(ctx, cfg); err != nil {
		return err
	}
	// Sync specific CIDR routes to the tun for proxy-routed dst CIDRs.
	// Best-effort; never fails the CRUD. fakeipWithConfig runs only when
	// provisioned (ensureFakeIPOverlayFromState above errors on nil FakeIP).
	if settings, serr := s.deps.Settings.Load(); serr == nil && settings != nil && settings.FakeIP != nil {
		s.syncTunCIDRRoutes(ctx, fakeIPNDMSName(settings.FakeIP.Index), before, cfg)
	}
	s.emitCfgEvent(event, cfg)
	return nil
}

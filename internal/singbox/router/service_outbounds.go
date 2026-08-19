package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// IsOutboundTagInUse reports whether tag is already occupied by any outbound
// catalog visible to singbox-router.
func (s *ServiceImpl) IsOutboundTagInUse(ctx context.Context, tag string) bool {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		cfg = NewEmptyConfig()
	}
	return s.isKnownOutboundTag(ctx, tag, cfg)
}

// RenameExternalOutboundTag rewrites references to a base outbound owned by
// another producer (for example a single sing-box tunnel in 10-tunnels.json).
// It updates both live/disabled router config and the pending draft, if any.
func (s *ServiceImpl) RenameExternalOutboundTag(ctx context.Context, oldTag, newTag string) error {
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if oldTag == "" || newTag == "" || oldTag == newTag {
		return nil
	}
	// Settings-side references: QoS classes route to outbound tags too and
	// must follow the rename (config-side rewrites below don't see them).
	if err := s.renameQoSClassOutbound(oldTag, newTag); err != nil {
		return err
	}
	if s.deps.Orch == nil {
		return s.withConfig(ctx, "all", func(c *RouterConfig) error {
			c.renameOutboundReferences(oldTag, newTag)
			return nil
		})
	}

	configDir := s.deps.Orch.ConfigDir()
	activePath := filepath.Join(configDir, "20-router.json")
	disabledPath := filepath.Join(configDir, "disabled", "20-router.json")
	pendingPath := filepath.Join(configDir, "pending", "20-router.json")
	changed := false

	if data, ok, err := rewriteRouterConfigOutboundRefs(activePath, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := s.deps.Orch.Save(orchestrator.SlotRouter, data); err != nil {
			return err
		}
		changed = true
	}
	if data, ok, err := rewriteRouterConfigOutboundRefs(disabledPath, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := storage.AtomicWrite(disabledPath, data); err != nil {
			return err
		}
		changed = true
	}
	if data, ok, err := rewriteRouterConfigOutboundRefs(pendingPath, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := s.deps.Orch.SaveDraft(orchestrator.SlotRouter, data); err != nil {
			return err
		}
		s.emitStagingEvent("staged")
		changed = true
	}
	// 21-fakeip.json: члены композитов и правила fakeip-слота ссылаются на
	// те же внешние теги — без переписи ссылка повисает и валит enable
	// fakeip-tun кросс-слот валидацией (#567). Та же тройка локаций.
	fakeipActive := filepath.Join(configDir, "21-fakeip.json")
	fakeipDisabled := filepath.Join(configDir, "disabled", "21-fakeip.json")
	fakeipPending := filepath.Join(configDir, "pending", "21-fakeip.json")
	if data, ok, err := rewriteRouterConfigOutboundRefs(fakeipActive, oldTag, newTag); err != nil {
		return err
	} else if ok {
		// Через Orch.Save: активный слот мерджится в живой конфиг — запись
		// должна взвести debounced reload (как router-ветка выше).
		if err := s.deps.Orch.Save(orchestrator.SlotFakeIP, data); err != nil {
			return err
		}
		changed = true
	}
	if data, ok, err := rewriteRouterConfigOutboundRefs(fakeipDisabled, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := storage.AtomicWrite(fakeipDisabled, data); err != nil {
			return err
		}
		changed = true
	}
	if data, ok, err := rewriteRouterConfigOutboundRefs(fakeipPending, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := storage.AtomicWrite(fakeipPending, data); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if cfg, err := s.loadRouterConfig(); err == nil {
			s.emitCfgEvent("all", s.ruleSetMaterializer().restoreConfig(cfg))
		}
	}
	return nil
}

func rewriteRouterConfigOutboundRefs(path, oldTag, newTag string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	cfg := NewEmptyConfig()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cfg.outboundReferences(oldTag)) == 0 {
		return nil, false, nil
	}
	cfg.renameOutboundReferences(oldTag, newTag)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (s *ServiceImpl) ListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error) {
	cfg, err := s.loadRouterConfig()
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

func (s *ServiceImpl) AddCompositeOutbound(ctx context.Context, o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		if err := s.validateBindInterface(ctx, o.BindInterface); err != nil {
			return err
		}
	}
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error {
		if err := s.validateCompositeMembers(ctx, o, c); err != nil {
			return err
		}
		return c.AddCompositeOutbound(o)
	})
}

func (s *ServiceImpl) UpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		if err := s.validateBindInterface(ctx, o.BindInterface); err != nil {
			return err
		}
	}
	if err := s.withConfig(ctx, "outbounds", func(c *RouterConfig) error {
		if err := s.validateCompositeMembers(ctx, o, c); err != nil {
			return err
		}
		return c.UpdateCompositeOutbound(tag, o)
	}); err != nil {
		return err
	}
	// A rename rewrites config references (renameOutboundReferences inside
	// UpdateCompositeOutbound); mirror it for the settings-side QoS classes.
	if newTag := strings.TrimSpace(o.Tag); newTag != "" && newTag != tag {
		if err := s.renameQoSClassOutbound(tag, newTag); err != nil {
			s.appLog.Warn("qos-classes", tag, "rename outbound in QoS classes: "+err.Error())
		}
	}
	return nil
}

func (s *ServiceImpl) DeleteCompositeOutbound(ctx context.Context, tag string, force bool) error {
	// QoS classes reference outbounds from settings, invisible to the config's
	// own reference scan — guard them the same way route rules are guarded.
	if !force {
		if refs := s.qosClassesReferencing(tag); len(refs) > 0 {
			return fmt.Errorf("%w: %q referenced by %s", ErrOutboundReferenced, tag, strings.Join(refs, ", "))
		}
	}
	if err := s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.DeleteCompositeOutbound(tag, force) }); err != nil {
		return err
	}
	if force {
		// Force-delete DISABLES (not deletes) referencing classes so the UI
		// shows them off instead of silently losing user configuration.
		if err := s.disableQoSClassesForOutbound(tag); err != nil {
			s.appLog.Warn("qos-classes", tag, "disable QoS classes after outbound delete: "+err.Error())
		}
	}
	return nil
}

// RulesReferencing returns the indices of route rules whose outbound
// equals tag. Used by tunnel.Service.Delete to refuse deletions that
// would orphan references in router rules.
func (s *ServiceImpl) RulesReferencing(tag string) []int {
	cfg, err := s.loadRouterConfig()
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.rulesReferencingOutbound(tag)
}

// OutboundReferenceLocations returns human-readable locations in the
// router config that reference tag, EXCLUDING route.rules[...] (covered
// by RulesReferencing). Used by the tunnel-delete guard to refuse
// deletion of a tunnel still referenced via composite member, route
// final, dns detour, or rule_set download_detour.
func (s *ServiceImpl) OutboundReferenceLocations(tag string) []string {
	var out []string
	if cfg, err := s.loadRouterConfig(); err == nil && cfg != nil {
		out = append(out, cfg.outboundReferencesExcludingRules(tag)...)
	}
	// fakeip-слот ссылается на те же внешние теги (члены композитов, правила,
	// route.final) — без учёта guard позволял удалить туннель и оставить
	// висячего члена, валящего enable fakeip-tun (#567). Правила здесь идут
	// строками, а не индексами: RulesReferencing — контракт про router-слот.
	if fcfg, err := s.loadFakeIPConfig(); err == nil && fcfg != nil {
		for _, i := range fcfg.rulesReferencingOutbound(tag) {
			out = append(out, fmt.Sprintf("[fakeip] route.rules[%d]", i))
		}
		for _, loc := range fcfg.outboundReferencesExcludingRules(tag) {
			out = append(out, "[fakeip] "+loc)
		}
	}
	return out
}

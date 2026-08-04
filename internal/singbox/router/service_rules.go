package router

import (
	"context"
	"fmt"
)

func (s *ServiceImpl) ListRules(ctx context.Context) ([]Rule, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	rules := s.ruleSetMaterializer().restoreConfig(cfg).Route.Rules
	filtered, _ := stripSelectiveManagedRules(rules)
	return filtered, nil
}

func (s *ServiceImpl) AddRule(ctx context.Context, r Rule) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.AddRule(r) })
}

func (s *ServiceImpl) UpdateRule(ctx context.Context, index int, r Rule) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.UpdateRule(index, r) })
}

func (s *ServiceImpl) DeleteRule(ctx context.Context, index int) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.DeleteRule(index) })
}

// BulkSetRuleOutbound sets Outbound on every rule at the given indices in a
// single config write. Stricter than UpdateRule: rejects an empty/duplicate
// index list, an out-of-range index, a non-route rule (!ActionIsRoute), or
// an unknown outbound tag — validating the whole batch before mutating
// anything, so a single invalid element leaves the config untouched.
func (s *ServiceImpl) BulkSetRuleOutbound(ctx context.Context, indices []int, outbound string) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error {
		return bulkSetRuleOutbound(c, indices, outbound, func(t string) bool { return s.isKnownOutboundTag(ctx, t, c) })
	})
}

func (s *ServiceImpl) MoveRule(ctx context.Context, from, to int) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.MoveRule(from, to) })
}

func (s *ServiceImpl) SetRouteFinal(ctx context.Context, tag string) error {
	if err := s.withConfig(ctx, "route", func(c *RouterConfig) error {
		if !s.isKnownOutboundTag(ctx, tag, c) {
			return fmt.Errorf("unknown outbound tag %q for route.final", tag)
		}
		return c.SetRouteFinal(tag)
	}); err != nil {
		return err
	}
	if tag != "direct" {
		return s.disableSelectiveBypassIfEnabled(ctx)
	}
	return nil
}

// isKnownOutboundTag returns true if tag is a sing-box built-in or matches
// an outbound from any known catalog (router composites, subscription
// composites, AWG, sing-box tunnels).
func (s *ServiceImpl) isKnownOutboundTag(ctx context.Context, tag string, cfg *RouterConfig) bool {
	if tag == "direct" || tag == "block" || tag == "dns" {
		return true
	}
	// Router-managed composites
	for _, o := range cfg.Outbounds {
		if o.Tag == tag {
			return true
		}
	}
	// Subscription composites (40-subscriptions.json)
	if s.deps.SubscriptionComposites != nil {
		for _, o := range s.deps.SubscriptionComposites.ListSubscriptionComposites() {
			if o.Tag == tag {
				return true
			}
		}
	}
	// AWG-direct outbounds (managed + system)
	if s.deps.AWGTags != nil {
		if tags, err := s.deps.AWGTags.ListTags(ctx); err == nil {
			for _, t := range tags {
				if t.Tag == tag {
					return true
				}
			}
		}
	}
	// Sing-box tunnels (10-tunnels.json)
	if s.deps.SingboxTunnels != nil {
		if tags, err := s.deps.SingboxTunnels.ListTunnelTags(ctx); err == nil {
			for _, t := range tags {
				if t == tag {
					return true
				}
			}
		}
	}
	return false
}

// bulkSetRuleOutbound validates the whole (indices, outbound) batch before
// mutating c — see BulkSetRuleOutbound. known reports whether outbound is a
// recognized tag ("direct"/"block"/"dns" or a catalog outbound).
//
// Системный префикс режима (sniff / hijack-dns / ip_is_private / route-options)
// живёт в режимном слоте, а c — общий слот: сюда он не попадает, поэтому
// отдельной проверки на него нет. Правило вида «ip_is_private → свой LAN-выход»
// в общем слоте пользователь завёл сам (splitLegacyRouteRules намеренно
// оставляет такие при миграции) — оно правится наравне с остальными.
func bulkSetRuleOutbound(c *RouterConfig, indices []int, outbound string, known func(string) bool) error {
	if len(indices) == 0 {
		return ErrBulkEmptyIndices
	}
	if !known(outbound) {
		return fmt.Errorf("%w: unknown outbound tag %q", ErrBulkInvalidSelection, outbound)
	}
	seen := make(map[int]bool, len(indices))
	for _, i := range indices {
		if seen[i] {
			return fmt.Errorf("%w: duplicate rule index %d", ErrBulkInvalidSelection, i)
		}
		seen[i] = true
		if i < 0 || i >= len(c.Route.Rules) {
			return fmt.Errorf("%w: index %d", ErrRuleIndexOutOfRange, i)
		}
		if !c.Route.Rules[i].ActionIsRoute() {
			return fmt.Errorf("%w: rule %d is not a route rule (action %q)", ErrBulkInvalidSelection, i, c.Route.Rules[i].Action)
		}
	}
	for _, i := range indices {
		c.Route.Rules[i].Outbound = outbound
	}
	return nil
}

// bulkSetRuleSetDetour validates the whole (tags, detour) batch before
// mutating c — see BulkSetRuleSetDetour. known reports whether detour is a
// recognized outbound tag; it is not consulted when detour is "" (clearing
// the field is always allowed).
func bulkSetRuleSetDetour(c *RouterConfig, tags []string, detour string, known func(string) bool) error {
	if len(tags) == 0 {
		return ErrBulkEmptyTags
	}
	if detour != "" && !known(detour) {
		return fmt.Errorf("%w: unknown outbound tag %q", ErrBulkInvalidSelection, detour)
	}
	byTag := make(map[string]int, len(c.Route.RuleSet))
	for i, rs := range c.Route.RuleSet {
		byTag[rs.Tag] = i
	}
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if seen[tag] {
			return fmt.Errorf("%w: duplicate rule set tag %q", ErrBulkInvalidSelection, tag)
		}
		seen[tag] = true
		i, ok := byTag[tag]
		if !ok {
			return fmt.Errorf("%w: %q", ErrRuleSetNotFound, tag)
		}
		if c.Route.RuleSet[i].Type != "remote" {
			return fmt.Errorf("%w: rule set %q is not type=remote (got %q)", ErrBulkInvalidSelection, tag, c.Route.RuleSet[i].Type)
		}
	}
	for _, tag := range tags {
		c.Route.RuleSet[byTag[tag]].DownloadDetour = detour
	}
	return nil
}

func (s *ServiceImpl) ListRuleSets(ctx context.Context) ([]RuleSet, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	restored := s.ruleSetMaterializer().restoreConfig(cfg)
	return restored.Route.RuleSet, nil
}

func (s *ServiceImpl) AddRuleSet(ctx context.Context, rs RuleSet) error {
	if rs.Type == "" {
		rs.Type = "remote"
	}
	if rs.Format == "" && rs.Type != "inline" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" && rs.Type == "remote" {
		rs.UpdateInterval = "24h"
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.AddRuleSet(rs) })
}

func (s *ServiceImpl) UpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error {
	if rs.Type == "" {
		rs.Type = "remote"
	}
	if rs.Format == "" && rs.Type != "inline" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" && rs.Type == "remote" {
		rs.UpdateInterval = "24h"
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.UpdateRuleSet(tag, rs) })
}

// BulkSetRuleSetDetour sets DownloadDetour on every rule set with a tag in
// the given list, in a single config write. Stricter than UpdateRuleSet:
// rejects an empty/duplicate tag list, an unknown tag, a rule set whose
// Type isn't "remote", or an unknown outbound tag (detour "" is allowed —
// it clears the field and skips the known-tag check) — validating the
// whole batch before mutating anything.
func (s *ServiceImpl) BulkSetRuleSetDetour(ctx context.Context, tags []string, detour string) error {
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error {
		return bulkSetRuleSetDetour(c, tags, detour, func(t string) bool { return s.isKnownOutboundTag(ctx, t, c) })
	})
}

func (s *ServiceImpl) DeleteRuleSet(ctx context.Context, tag string, force bool) error {
	inlineTag := tag
	if base, ok := inlineTagFromSRSTag(tag); ok {
		inlineTag = base
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error {
		if err := c.DeleteRuleSet(inlineTag, force); err != nil {
			return err
		}
		if s.deps.Orch == nil {
			s.ruleSetMaterializer().removeInlineArtifacts(inlineTag)
		}
		return nil
	})
}

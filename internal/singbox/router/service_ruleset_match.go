package router

import "context"

// MatchRuleSets matches a domain or IP against selected rule-sets from the
// active routing slot. It only reads configuration and uses the inspector's
// transient remote-rule-set cache; it never applies or discards router state.
func (s *ServiceImpl) MatchRuleSets(ctx context.Context, input string, tags []string) ([]RuleSetMatchResult, error) {
	cfg, slot, err := s.inspectSlotConfig()
	if err != nil {
		return nil, err
	}
	m := s.ruleSetMaterializer()
	ruleSets := withRuleSetFetchVia(slot, m.inspectRuleSetsWithInlineAliases(cfg))
	binary := ""
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	s.inspectCacheOnce.Do(func() {
		s.inspectCache = newRuleSetCache("")
		s.inspectCache.onPublish = func(context.Context) { s.InvalidateCoverageIndex() }
	})
	return MatchRuleSets(input, tags, ruleSets, binary, s.inspectCache), nil
}

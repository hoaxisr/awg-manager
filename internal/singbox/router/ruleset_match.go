package router

// RuleSetMatchResult is the outcome of matching one input against one
// requested rule-set. Supported is false when the rule-set cannot be
// evaluated at all (for example, it is undefined, its file is unavailable, or
// sing-box is not installed). Error describes either an unsupported check or
// a diagnostic returned by sing-box; it does not fail the other requested
// rule-sets.
type RuleSetMatchResult struct {
	Tag       string `json:"tag"`
	Matched   bool   `json:"matched"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// MatchRuleSets matches input against the selected rule-sets in request order.
// It deliberately uses the inspector's matchRuleSet path so local/remote
// formats, remote caching, download detours and sing-box marker semantics stay
// identical to route inspection. A bad or unsupported tag produces a per-tag
// result rather than aborting the batch.
func MatchRuleSets(input string, tags []string, ruleSets []RuleSet, singboxBinary string, cache *ruleSetCache) []RuleSetMatchResult {
	byTag := make(map[string]RuleSet, len(ruleSets))
	for _, rs := range ruleSets {
		byTag[rs.Tag] = rs
	}

	results := make([]RuleSetMatchResult, 0, len(tags))
	for _, tag := range tags {
		result := RuleSetMatchResult{Tag: tag}
		rs, ok := byTag[tag]
		if !ok {
			result.Error = "rule-set is not defined"
			results = append(results, result)
			continue
		}

		matched, supported, err := matchRuleSet(input, rs, singboxBinary, cache, nil)
		result.Matched = matched
		result.Supported = supported
		if err != nil {
			result.Error = err.Error()
		} else if !result.Supported {
			result.Error = "rule-set cannot be evaluated: sing-box binary or rule-set file is unavailable, or the type is unsupported"
		}
		results = append(results, result)
	}
	return results
}

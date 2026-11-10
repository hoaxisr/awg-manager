package autolist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// RuleSetMatchResult is one item returned by awg-manager's read-only
// POST /api/singbox/router/rulesets/match endpoint. An unsupported or unknown
// rule-set is an item-level outcome, not a request error.
type RuleSetMatchResult struct {
	Tag       string `json:"tag"`
	Matched   bool   `json:"matched"`
	Supported bool   `json:"supported"`
	Error     string `json:"error,omitempty"`
}

// MatchRuleSets checks a domain or IP against selected existing sing-box
// rule-sets. Results follow tags order. The endpoint applies no router changes.
func (r *RuleClient) MatchRuleSets(ctx context.Context, input string, tags []string) ([]RuleSetMatchResult, error) {
	body, err := r.post(ctx, "/api/singbox/router/rulesets/match", map[string]any{
		"input": input,
		"tags":  tags,
	})
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool                 `json:"success"`
		Data    []RuleSetMatchResult `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("sing-box rule-set match: %w", err)
	}
	if !env.Success {
		return nil, errors.New("sing-box rule-set match: API reported failure")
	}
	return env.Data, nil
}

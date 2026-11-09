// Package exclusions decides whether an autolist candidate is excluded by
// manual domain configuration or by selected sing-box rule-sets.
package exclusions

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrNoRuleSetMatcher reports selected rule-sets without a way to evaluate
// them. Callers should reject the candidate rather than silently ignore its
// configured exclusions.
var ErrNoRuleSetMatcher = errors.New("exclusions: rule-set matcher is not configured")

// RuleSetMatcher evaluates input against all selected rule-set tags. A true
// result means that at least one tag matches. Implementations must return an
// error when a selected tag cannot be evaluated; treating that case as a
// non-match would silently disable an exclusion.
type RuleSetMatcher interface {
	MatchRuleSets(ctx context.Context, input string, tags []string) (bool, error)
}

// RuleSetMatcherFunc adapts a callback to RuleSetMatcher.
type RuleSetMatcherFunc func(context.Context, string, []string) (bool, error)

// MatchRuleSets calls f.
func (f RuleSetMatcherFunc) MatchRuleSets(ctx context.Context, input string, tags []string) (bool, error) {
	return f(ctx, input, tags)
}

// Config selects exclusions. New copies both slices. Domain entries are
// case-insensitive and may have a trailing dot; empty entries are ignored.
type Config struct {
	Domains     []string
	RuleSetTags []string
}

// Matcher is an immutable exclusion matcher. Its zero value is a no-op.
type Matcher struct {
	domains  []string
	tags     []string
	ruleSets RuleSetMatcher
}

// New constructs a Matcher. A nil RuleSetMatcher is valid when no rule-set
// tags are selected.
func New(cfg Config, ruleSets RuleSetMatcher) *Matcher {
	m := &Matcher{
		tags:     append([]string(nil), cfg.RuleSetTags...),
		ruleSets: ruleSets,
	}
	for _, domain := range cfg.Domains {
		if domain = normalize(domain); domain != "" {
			m.domains = append(m.domains, domain)
		}
	}
	return m
}

// Excluded reports whether host or its candidate registeredDomain must be
// rejected. In addition to ordinary exact/subdomain exclusions, a registered
// domain is rejected when a domain_suffix rule for it would cover any manually
// excluded subdomain (the autolist exclusion-hole rule).
//
// Manual checks run first. Otherwise rule-sets are checked for host and then
// registeredDomain, omitting a duplicate or empty input. Matcher errors are
// returned so the caller can fail closed. A nil or empty Matcher is a no-op.
func (m *Matcher) Excluded(ctx context.Context, host, registeredDomain string) (bool, error) {
	if m == nil {
		return false, nil
	}
	host = normalize(host)
	registeredDomain = normalize(registeredDomain)

	for _, excluded := range m.domains {
		if within(host, excluded) || within(registeredDomain, excluded) || within(excluded, registeredDomain) {
			return true, nil
		}
	}

	if len(m.tags) == 0 {
		return false, nil
	}
	inputs := []string{host}
	if registeredDomain != "" && registeredDomain != host {
		inputs = append(inputs, registeredDomain)
	}
	for _, input := range inputs {
		if input == "" {
			continue
		}
		if m.ruleSets == nil {
			return false, ErrNoRuleSetMatcher
		}
		matched, err := m.ruleSets.MatchRuleSets(ctx, input, m.tags)
		if err != nil {
			return false, fmt.Errorf("exclusions: match rule-sets for %q: %w", input, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

func normalize(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// within reports whether name is parent itself or one of its subdomains.
// The label boundary prevents example.com from matching badexample.com.
func within(name, parent string) bool {
	if name == "" || parent == "" || len(parent) > len(name) {
		return false
	}
	if name == parent {
		return true
	}
	return strings.HasSuffix(name, parent) && name[len(name)-len(parent)-1] == '.'
}

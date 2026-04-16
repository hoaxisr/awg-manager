package hydraroute

import (
	"fmt"
	"sort"
	"strings"
)

// HRRule is the HR-file-backed rule model. The rule name is its identity;
// renaming = delete + create at the service layer.
type HRRule struct {
	Name    string
	Domains []string
	Subnets []string
	Target  string
}

// ListRules returns all rules currently present in domain.conf and ip.list,
// merged by name, plus the oversized geoip tags found in HR Neo's service
// block (##impossible to use). Files are the source of truth — each call
// re-parses, so manual edits between calls are immediately visible.
func (s *Service) ListRules() ([]HRRule, []string, error) {
	entries, oversized, err := s.loadEntries()
	if err != nil {
		return nil, nil, err
	}
	result := make([]HRRule, 0, len(entries))
	for _, e := range entries {
		result = append(result, entryToRule(e))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, oversized, nil
}

// CreateRule persists a new rule. Name must be unique — duplicate is rejected.
func (s *Service) CreateRule(rule HRRule) (*HRRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateRule(rule); err != nil {
		return nil, err
	}

	entries, _, err := s.loadEntries()
	if err != nil {
		return nil, err
	}
	if _, exists := entries[rule.Name]; exists {
		return nil, fmt.Errorf("rule %q already exists", rule.Name)
	}
	entries[rule.Name] = ruleToEntry(rule)
	if err := s.saveEntries(entries); err != nil {
		return nil, err
	}
	if s.log != nil {
		s.log.Infof("hydraroute: created rule %q (target=%q, domains=%d, subnets=%d)",
			rule.Name, rule.Target, len(rule.Domains), len(rule.Subnets))
	}
	return &rule, nil
}

// UpdateRule replaces the rule identified by originalName with the supplied
// content. Allows rename: if rule.Name differs from originalName, the old
// entry is dropped and a new one (under the new name) is written. The new
// name must not collide with another existing rule.
func (s *Service) UpdateRule(originalName string, rule HRRule) (*HRRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateRule(rule); err != nil {
		return nil, err
	}

	entries, _, err := s.loadEntries()
	if err != nil {
		return nil, err
	}
	if _, exists := entries[originalName]; !exists {
		return nil, fmt.Errorf("rule %q not found", originalName)
	}
	if rule.Name != originalName {
		if _, collision := entries[rule.Name]; collision {
			return nil, fmt.Errorf("rule %q already exists", rule.Name)
		}
		delete(entries, originalName)
	}
	entries[rule.Name] = ruleToEntry(rule)
	if err := s.saveEntries(entries); err != nil {
		return nil, err
	}
	if s.log != nil {
		s.log.Infof("hydraroute: updated rule %q (target=%q, domains=%d, subnets=%d)",
			rule.Name, rule.Target, len(rule.Domains), len(rule.Subnets))
	}
	return &rule, nil
}

// DeleteRule removes a rule by name. Missing name is a no-op — idempotent.
func (s *Service) DeleteRule(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, _, err := s.loadEntries()
	if err != nil {
		return err
	}
	if _, exists := entries[name]; !exists {
		return nil
	}
	delete(entries, name)
	if err := s.saveEntries(entries); err != nil {
		return err
	}
	if s.log != nil {
		s.log.Infof("hydraroute: deleted rule %q", name)
	}
	return nil
}

// loadEntries reads both HR files and merges into a single map keyed by name.
// Also returns the oversized geoip tags that HR Neo has quarantined in its
// ##impossible to use service block.
func (s *Service) loadEntries() (map[string]ManagedEntry, []string, error) {
	domainData, err := readOrEmpty(domainConfPath)
	if err != nil {
		return nil, nil, err
	}
	ipData, err := readOrEmpty(ipListPath)
	if err != nil {
		return nil, nil, err
	}

	domains := parseDomainConf(domainData)
	subnets, oversized := parseIPList(ipData)

	merged := make(map[string]ManagedEntry, len(domains)+len(subnets))
	for _, e := range domains {
		merged[e.ListName] = e
	}
	for _, e := range subnets {
		if existing, ok := merged[e.ListName]; ok {
			existing.Subnets = e.Subnets
			if existing.Iface == "" {
				existing.Iface = e.Iface
			}
			merged[e.ListName] = existing
		} else {
			merged[e.ListName] = e
		}
	}
	return merged, oversized, nil
}

// saveEntries writes the full content of both files and schedules a daemon
// restart so HR Neo picks up the change.
func (s *Service) saveEntries(entries map[string]ManagedEntry) error {
	if !s.status.Installed {
		return fmt.Errorf("HydraRoute Neo is not installed")
	}

	ordered := make([]ManagedEntry, 0, len(entries))
	for _, e := range entries {
		ordered = append(ordered, e)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ListName < ordered[j].ListName })

	if err := WriteWholeFile(domainConfPath, GenerateDomainConf(ordered)); err != nil {
		return err
	}
	if err := WriteWholeFile(ipListPath, GenerateIPList(ordered)); err != nil {
		return err
	}
	s.scheduleRestart()
	return nil
}

func ruleToEntry(r HRRule) ManagedEntry {
	return ManagedEntry{
		ListName: r.Name,
		Domains:  r.Domains,
		Subnets:  r.Subnets,
		Iface:    r.Target,
	}
}

func entryToRule(e ManagedEntry) HRRule {
	return HRRule{
		Name:    e.ListName,
		Domains: e.Domains,
		Subnets: e.Subnets,
		Target:  e.Iface,
	}
}

func validateRule(r HRRule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("rule name must not be empty")
	}
	if r.Target == "" {
		return fmt.Errorf("rule target (interface or policy) must not be empty")
	}
	if len(r.Domains) == 0 && len(r.Subnets) == 0 {
		return fmt.Errorf("rule must have at least one domain or subnet")
	}
	return nil
}

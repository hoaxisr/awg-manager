package hydraroute

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ListPolicyNames returns the names of all Keenetic ip-policies configured
// on the router, read from the NDMS Policies Query Store.
//
// Returns an empty slice when no Queries registry is wired up (e.g. during
// standalone tests); callers treat that as "no policies known" and fall
// back to interface-mode classification.
func (s *Service) ListPolicyNames(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	queries := s.queries
	s.mu.Unlock()

	if queries == nil || queries.Policies == nil {
		return nil, nil
	}

	list, err := queries.Policies.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list ip policies: %w", err)
	}

	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Name)
	}
	return names, nil
}

var ghostPolicyPrefixRE = regexp.MustCompile(`(?i)^(awgm|awg|opkgtun|nwg|wireguard|ppp|pppoe|eth|usb|wlan|wifi|br|tun|tap)\d.*$`)

func isGhostPolicyName(name string) bool {
	// Never treat Keenetic system policies (Policy0, Policy1, etc.) as ghost policies.
	if isSystemPolicy(name) {
		return false
	}
	// Matches known tunnel / interface naming patterns
	if ghostPolicyPrefixRE.MatchString(name) {
		return true
	}
	// Target with prefix
	if strings.HasPrefix(name, "wan:") || strings.HasPrefix(name, "system:") {
		return true
	}
	return false
}

func isSystemPolicy(name string) bool {
	if strings.HasPrefix(name, "Policy") {
		numPart := strings.TrimPrefix(name, "Policy")
		if _, err := strconv.Atoi(numPart); err == nil {
			return true
		}
	}
	return false
}

// CleanGhostPolicies removes empty IP policies that were erroneously created by
// HR Neo on KeeneticOS when interfaces were down or not yet present in /sys/class/net
// during boot (issue #967).
//
// A policy is considered a ghost policy if:
// 1. It is NOT a Keenetic system policy (Policy0, Policy1, etc.).
// 2. It has an empty description ("").
// 3. It has no permitted interfaces (len(p.Interfaces) == 0).
// 4. Its name matches known interface/tunnel patterns (e.g. awgm0, nwg0, opkgtun10, Wireguard0)
//    OR matches an interface target from domain.conf.
func (s *Service) CleanGhostPolicies(ctx context.Context) (int, error) {
	s.mu.Lock()
	queries := s.queries
	policies := s.policies
	s.mu.Unlock()

	if queries == nil || queries.Policies == nil || policies == nil {
		return 0, nil
	}

	list, err := queries.Policies.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list policies for ghost cleanup: %w", err)
	}

	// Collect targets from existing HR rules in domain.conf
	hrTargets := make(map[string]bool)
	if rules, _, err := s.ListRules(); err == nil {
		for _, r := range rules {
			if r.Target != "" {
				hrTargets[r.Target] = true
			}
		}
	}

	deleted := 0
	for _, p := range list {
		// System policies (Policy0, Policy1, etc.) must NEVER be deleted.
		if isSystemPolicy(p.Name) {
			continue
		}
		// Ghost policies created by hrneo always have empty description and no interfaces.
		if p.Description != "" || len(p.Interfaces) > 0 {
			continue
		}

		// A policy is a ghost if it matches interface naming patterns or is an interface target in domain.conf.
		isCandidate := isGhostPolicyName(p.Name) || hrTargets[p.Name]
		if !isCandidate {
			continue
		}

		if s.appLog != nil {
			s.appLog.Warn("ghost-policy-cleanup", p.Name, fmt.Sprintf("deleting ghost policy %q created by hrneo", p.Name))
		}
		if err := policies.DeletePolicy(ctx, p.Name); err != nil {
			if s.appLog != nil {
				s.appLog.Warn("ghost-policy-cleanup", p.Name, fmt.Sprintf("failed to delete ghost policy %q: %v", p.Name, err))
			}
		} else {
			deleted++
		}
	}

	if deleted > 0 && s.appLog != nil {
		s.appLog.Info("ghost-policy-cleanup", "", fmt.Sprintf("cleaned up %d ghost policy(ies)", deleted))
	}
	return deleted, nil
}

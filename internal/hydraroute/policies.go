package hydraroute

import (
	"context"
	"fmt"
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

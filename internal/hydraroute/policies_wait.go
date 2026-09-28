package hydraroute

import (
	"context"
	"fmt"
	"time"
)

// WaitForPolicy polls the NDMS policy store until the given policy name
// appears or the timeout expires. Intended for the new-policy flow: after
// a rule is written into HR Neo's config and the daemon restarts, HR Neo
// uses RCI to create the policy on the router; we can only permit
// interfaces in the policy once it exists.
//
// The Policies Query Store has a 60 min TTL, which is far too long for a
// poll of an external event. We force a fresh read each iteration by
// invalidating the cache before every List call.
//
// Returns nil on success, an error on timeout or ctx cancellation. If no
// Queries registry is wired (e.g. tests), returns nil immediately.
func (s *Service) WaitForPolicy(ctx context.Context, policyName string, timeout time.Duration) error {
	s.mu.Lock()
	queries := s.queries
	s.mu.Unlock()
	if queries == nil || queries.Policies == nil {
		return nil
	}
	s.appLog.Info("wait-policy", policyName, fmt.Sprintf("waiting (timeout %s)", timeout))

	deadline := time.Now().Add(timeout)
	interval := 300 * time.Millisecond
	start := time.Now()

	for {
		list, err := queries.Policies.Fetch(ctx)
		if err == nil {
			for _, p := range list {
				if p.Name == policyName {
					s.appLog.Info("wait-policy", policyName, fmt.Sprintf("appeared after %s", time.Since(start).Round(100*time.Millisecond)))
					return nil
				}
			}
		}

		if time.Now().After(deadline) {
			s.appLog.Warn("wait-policy", policyName, fmt.Sprintf("did not appear within %s", timeout))
			return fmt.Errorf("policy %q did not appear within %s", policyName, timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

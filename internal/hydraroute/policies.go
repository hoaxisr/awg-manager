package hydraroute

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListPolicyNames returns the names of all Keenetic ip-policies configured
// on the router, parsed from `show rc ip policy`.
//
// Returns an empty slice when no NDMS client is wired up (e.g. during
// standalone tests); callers treat that as "no policies known" and fall
// back to interface-mode classification.
func (s *Service) ListPolicyNames(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	client := s.ndms
	s.mu.Unlock()

	if client == nil {
		return nil, nil
	}

	raw, err := client.RCIGet(ctx, "/show/rc/ip/policy")
	if err != nil {
		return nil, fmt.Errorf("show rc ip policy: %w", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse ip policy response: %w", err)
	}

	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names, nil
}

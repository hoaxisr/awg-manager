package managed

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// SetPolicy applies an ip hotspot policy to the managed server's
// interface and persists the choice to storage. Accepted values:
//   - "none"  → clears the policy via RCI (no policy <iface>)
//   - "permit" / "deny" → literal RCI policy values
//   - "<PolicyName>" → must match an existing IP Policy profile name
//     from the router (queries.Policies.List)
//
// When the new value equals the current one the call is a no-op
// (no RCI traffic, no save). Validation runs before any RCI command.
func (s *Service) SetPolicy(ctx context.Context, id, policy string) error {
	if policy == "" {
		return fmt.Errorf("policy must not be empty")
	}
	server, ok := s.settings.GetManagedServerByID(id)
	if !ok {
		return fmt.Errorf("managed server not found: %s", id)
	}

	// No-op shortcut runs before profile-list validation so a repeat
	// click on the same named policy doesn't waste an RCI round-trip.
	if policy == server.Policy {
		return nil
	}

	if policy != "none" && policy != "permit" && policy != "deny" {
		opts, err := s.ListPolicies(ctx)
		if err != nil {
			return fmt.Errorf("list policies: %w", err)
		}
		known := false
		for _, o := range opts {
			if o.ID == policy {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown policy: %s", policy)
		}
	}

	if policy == "none" {
		if err := s.rciClearHotspotPolicy(ctx, server.InterfaceName); err != nil {
			return fmt.Errorf("clear policy: %w", err)
		}
	} else {
		if err := s.rciSetHotspotPolicy(ctx, server.InterfaceName, policy); err != nil {
			return fmt.Errorf("set policy: %w", err)
		}
	}

	if err := s.settings.UpdateManagedServer(id, func(sv *storage.ManagedServer) error {
		sv.Policy = policy
		return nil
	}); err != nil {
		s.log.Warn("policy applied via RCI but failed to persist", "error", err, "policy", policy)
		return fmt.Errorf("save policy: %w", err)
	}

	s.log.Info("managed server policy changed", "interface", server.InterfaceName, "policy", policy)
	s.appLog.Info("policy", server.InterfaceName, fmt.Sprintf("Policy set to %s", policy))
	return nil
}

// ListPolicies returns every IP Policy profile configured on the
// router, suitable for surfacing in the "choose policy" dropdown.
// Description may be empty for profiles without a user-facing label.
func (s *Service) ListPolicies(ctx context.Context) ([]PolicyOption, error) {
	if s.queries == nil || s.queries.Policies == nil {
		return nil, fmt.Errorf("policy store not wired")
	}
	policies, err := s.queries.Policies.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	out := make([]PolicyOption, 0, len(policies))
	for _, p := range policies {
		out = append(out, PolicyOption{
			ID:          p.Name,
			Description: p.Description,
		})
	}
	return out, nil
}

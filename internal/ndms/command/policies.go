package command

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type PolicyCommands struct {
	poster       Poster
	save         *SaveCoordinator
	queries      *query.Queries
	hookNotifier HookNotifier
}

func NewPolicyCommands(p Poster, s *SaveCoordinator, q *query.Queries, hn HookNotifier) *PolicyCommands {
	return &PolicyCommands{poster: p, save: s, queries: q, hookNotifier: hn}
}

// SetHookNotifier replaces the HookNotifier after construction. See
// InterfaceCommands.SetHookNotifier for the rationale.
func (c *PolicyCommands) SetHookNotifier(hn HookNotifier) { c.hookNotifier = hn }

func (c *PolicyCommands) CreatePolicy(ctx context.Context, name, description string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{"description": description},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "create policy "+name,
		c.queries.Policies.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) DeletePolicy(ctx context.Context, name string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{"no": true},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "delete policy "+name,
		c.queries.Policies.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) SetDescription(ctx context.Context, name, description string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{"description": description},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "set policy description "+name,
		c.queries.Policies.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) SetStandalone(ctx context.Context, name string, enabled bool) error {
	var standaloneVal any
	if enabled {
		standaloneVal = true
	} else {
		standaloneVal = map[string]any{"no": true}
	}
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{"standalone": standaloneVal},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "set standalone "+name,
		c.queries.Policies.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) PermitInterface(ctx context.Context, name, iface string, order int) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{
					"permit": map[string]any{
						"global":    true,
						"interface": iface,
						"order":     order,
					},
				},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "permit "+iface+" on "+name,
		c.queries.Policies.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) DenyInterface(ctx context.Context, name, iface string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{
					"permit": map[string]any{
						"global":    true,
						"interface": iface,
						"no":        true,
					},
				},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "deny "+iface+" on "+name,
		c.queries.Policies.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) AssignDevice(ctx context.Context, mac, policyName string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"hotspot": map[string]any{
				"host": map[string]any{
					"mac":    mac,
					"policy": policyName,
				},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "assign device "+mac+" to "+policyName,
		c.queries.Hotspot.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *PolicyCommands) UnassignDevice(ctx context.Context, mac string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"hotspot": map[string]any{
				"host": map[string]any{
					"mac":    mac,
					"policy": map[string]any{"no": true},
				},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "unassign device "+mac,
		c.queries.Hotspot.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

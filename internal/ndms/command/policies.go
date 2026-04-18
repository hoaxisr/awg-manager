package command

import (
	"context"
	"fmt"

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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("create policy %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Policies.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

func (c *PolicyCommands) DeletePolicy(ctx context.Context, name string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{"no": true},
			},
		},
	}
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("delete policy %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Policies.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

func (c *PolicyCommands) SetDescription(ctx context.Context, name, description string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"policy": map[string]any{
				name: map[string]any{"description": description},
			},
		},
	}
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("set policy description %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Policies.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("set standalone %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Policies.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("permit %s on %s: %w", iface, name, err)
	}
	c.save.Request()
	c.queries.Policies.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("deny %s on %s: %w", iface, name, err)
	}
	c.save.Request()
	c.queries.Policies.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("assign device %s to %s: %w", mac, policyName, err)
	}
	c.save.Request()
	c.queries.Hotspot.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("unassign device %s: %w", mac, err)
	}
	c.save.Request()
	c.queries.Hotspot.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}


package command

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type PingCheckCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewPingCheckCommands(p Poster, s *SaveCoordinator, q *query.Queries) *PingCheckCommands {
	return &PingCheckCommands{poster: p, save: s, queries: q}
}

// ConfigureProfile idempotently configures a ping-check profile and
// binds it to the given interface. Sequence: best-effort teardown,
// create profile, bind. Uses ndms.PingCheckConfig — the single domain
// type shared with tunnel operators and API handlers.
func (c *PingCheckCommands) ConfigureProfile(ctx context.Context, profile, ifaceName string, cfg ndms.PingCheckConfig) error {
	c.bestEffortRemove(ctx, profile, ifaceName)

	profileInner := map[string]any{
		"host":            cfg.Host,
		"mode":            cfg.Mode,
		"update-interval": map[string]any{"seconds": cfg.UpdateInterval},
		"timeout":         cfg.Timeout,
	}
	if cfg.MaxFails > 0 {
		profileInner["max-fails"] = map[string]any{"count": cfg.MaxFails}
	}
	if cfg.MinSuccess > 0 {
		profileInner["min-success"] = map[string]any{"count": cfg.MinSuccess}
	}
	if cfg.Port > 0 && (cfg.Mode == "connect" || cfg.Mode == "tls") {
		profileInner["port"] = cfg.Port
	}
	createPayload := map[string]any{
		"ping-check": map[string]any{
			"profile": map[string]any{profile: profileInner},
		},
	}
	if _, err := c.poster.Post(ctx, createPayload); err != nil {
		return fmt.Errorf("create ping-check profile %s: %w", profile, err)
	}

	bindPayload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"ping-check": map[string]any{
					"profile": profile,
					"restart": cfg.Restart,
				},
			},
		},
	}
	if _, err := c.poster.Post(ctx, bindPayload); err != nil {
		return fmt.Errorf("bind ping-check profile %s to %s: %w", profile, ifaceName, err)
	}

	c.save.Request()
	c.queries.PingCheckProfile.InvalidateAll()
	c.queries.PingCheckStatus.InvalidateAll()
	c.queries.Interfaces.Invalidate(ifaceName)
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// RemoveProfile tears down a ping-check profile. Best-effort — partial
// state is tolerated.
func (c *PingCheckCommands) RemoveProfile(ctx context.Context, profile, ifaceName string) error {
	c.bestEffortRemove(ctx, profile, ifaceName)
	c.save.Request()
	c.queries.PingCheckProfile.InvalidateAll()
	c.queries.PingCheckStatus.InvalidateAll()
	c.queries.Interfaces.Invalidate(ifaceName)
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// bestEffortRemove runs the 3-step teardown, ignoring per-step errors.
func (c *PingCheckCommands) bestEffortRemove(ctx context.Context, profile, ifaceName string) {
	_, _ = c.poster.Post(ctx, map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"ping-check": map[string]any{"restart": map[string]any{"no": true}},
			},
		},
	})
	_, _ = c.poster.Post(ctx, map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"ping-check": map[string]any{
					"profile": map[string]any{"no": true, "profile": profile},
				},
			},
		},
	})
	_, _ = c.poster.Post(ctx, map[string]any{
		"ping-check": map[string]any{
			"profile": map[string]any{profile: map[string]any{"no": true}},
		},
	})
}

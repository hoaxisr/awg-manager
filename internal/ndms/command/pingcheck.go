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
//
// Снос прежней привязки — только если она есть (bestEffortRemove), F546.
func (c *PingCheckCommands) ConfigureProfile(ctx context.Context, profile string, iface query.Confirmed, cfg ndms.PingCheckConfig) error {
	c.bestEffortRemove(ctx, profile, iface)
	return c.configure(ctx, profile, iface.Name(), cfg)
}

// ConfigureProfileLegacy — временно, до Task 19 (F546).
func (c *PingCheckCommands) ConfigureProfileLegacy(ctx context.Context, profile, ifaceName string, cfg ndms.PingCheckConfig) error {
	c.bestEffortRemoveLegacy(ctx, profile, ifaceName)
	return c.configure(ctx, profile, ifaceName, cfg)
}

// configure — создание профиля и привязка после сноса прежнего.
func (c *PingCheckCommands) configure(ctx context.Context, profile, ifaceName string, cfg ndms.PingCheckConfig) error {
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
func (c *PingCheckCommands) RemoveProfile(ctx context.Context, profile string, iface query.Confirmed) error {
	c.bestEffortRemove(ctx, profile, iface)
	c.save.Request()
	c.queries.PingCheckProfile.InvalidateAll()
	c.queries.PingCheckStatus.InvalidateAll()
	c.queries.Interfaces.Invalidate(iface.Name())
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// RemoveOrphanProfile снимает профиль, когда интерфейса уже нет: только
// `no ping-check profile <p>`, ни одной команды `interface X …` (F546).
func (c *PingCheckCommands) RemoveOrphanProfile(ctx context.Context, profile string) error {
	c.deleteProfile(ctx, profile)
	c.save.Request()
	c.queries.PingCheckProfile.InvalidateAll()
	c.queries.PingCheckStatus.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// RemoveProfileLegacy — временно, до Task 19 (F546).
func (c *PingCheckCommands) RemoveProfileLegacy(ctx context.Context, profile, ifaceName string) error {
	c.bestEffortRemoveLegacy(ctx, profile, ifaceName)
	c.save.Request()
	c.queries.PingCheckProfile.InvalidateAll()
	c.queries.PingCheckStatus.InvalidateAll()
	c.queries.Interfaces.Invalidate(ifaceName)
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

// bestEffortRemove — снос привязки и профиля, ошибки шагов игнорируются.
// Команды `interface X ping-check …` уходят, только если у X привязан профиль
// по свежему статусу: без привязки NDMS пишет E «interface "X" has no assigned
// profile». Статус не прочитался — шлём всё, как раньше: E в журнале дешевле
// оставленной привязки.
func (c *PingCheckCommands) bestEffortRemove(ctx context.Context, profile string, iface query.Confirmed) {
	name := iface.Name()
	bound := true
	if rows, err := c.queries.PingCheckStatus.Fetch(ctx); err == nil {
		bound = false
		for _, r := range rows {
			if r.Interface == name {
				bound = true
				break
			}
		}
	}
	if bound {
		c.unbind(ctx, profile, name)
	}
	c.deleteProfile(ctx, profile)
}

// bestEffortRemoveLegacy — временно, до Task 19 (F546).
//
// bestEffortRemoveLegacy runs the 3-step teardown, ignoring per-step errors.
func (c *PingCheckCommands) bestEffortRemoveLegacy(ctx context.Context, profile, ifaceName string) {
	c.unbind(ctx, profile, ifaceName)
	c.deleteProfile(ctx, profile)
}

func (c *PingCheckCommands) unbind(ctx context.Context, profile, ifaceName string) {
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
}

func (c *PingCheckCommands) deleteProfile(ctx context.Context, profile string) {
	_, _ = c.poster.Post(ctx, map[string]any{
		"ping-check": map[string]any{
			"profile": map[string]any{profile: map[string]any{"no": true}},
		},
	})
}

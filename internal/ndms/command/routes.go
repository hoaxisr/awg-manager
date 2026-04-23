package command

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type RouteCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewRouteCommands(p Poster, s *SaveCoordinator, q *query.Queries) *RouteCommands {
	return &RouteCommands{poster: p, save: s, queries: q}
}

// StaticRouteSpec describes a static route mutation. Exactly one of
// Host (/32) or Network+Mask must be set.
type StaticRouteSpec struct {
	Interface string
	Host      string
	Network   string
	Mask      string
	Reject    bool
	Comment   string
}

func (c *RouteCommands) SetDefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"default": true, "interface": name},
		},
	}
	return c.mutate(ctx, payload, "set default route "+name)
}

func (c *RouteCommands) RemoveDefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"default": true, "interface": name, "no": true},
		},
	}
	return c.mutate(ctx, payload, "remove default route "+name)
}

func (c *RouteCommands) SetIPv6DefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ipv6": map[string]any{
			"route": map[string]any{"default": true, "interface": name},
		},
	}
	return c.mutate(ctx, payload, "set ipv6 default route "+name)
}

func (c *RouteCommands) RemoveIPv6DefaultRoute(ctx context.Context, name string) error {
	payload := map[string]any{
		"ipv6": map[string]any{
			"route": map[string]any{"default": true, "interface": name, "no": true},
		},
	}
	return c.mutate(ctx, payload, "remove ipv6 default route "+name)
}

// RemoveHostRoute removes an IPv4 host route (best-effort).
func (c *RouteCommands) RemoveHostRoute(ctx context.Context, host string) error {
	payload := map[string]any{
		"ip": map[string]any{
			"route": map[string]any{"no": true, "host": host},
		},
	}
	return c.mutate(ctx, payload, "remove host route "+host)
}

// AddStaticRoute adds a network or host route to the given interface.
func (c *RouteCommands) AddStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	inner := map[string]any{
		"interface": route.Interface,
		"auto":      true,
	}
	if route.Host != "" {
		inner["host"] = route.Host
	} else {
		inner["network"] = route.Network
		inner["mask"] = route.Mask
	}
	if route.Reject {
		inner["reject"] = true
	}
	if route.Comment != "" {
		inner["comment"] = route.Comment
	}
	payload := map[string]any{
		"ip": map[string]any{"route": inner},
	}
	return c.mutate(ctx, payload, "add static route")
}

// RemoveStaticRoute removes a previously-added static route.
func (c *RouteCommands) RemoveStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	inner := map[string]any{
		"interface": route.Interface,
		"no":        true,
	}
	if route.Host != "" {
		inner["host"] = route.Host
	} else {
		inner["network"] = route.Network
		inner["mask"] = route.Mask
	}
	payload := map[string]any{
		"ip": map[string]any{"route": inner},
	}
	return c.mutate(ctx, payload, "remove static route")
}

// mutate is a thin wrapper over postMutation with RouteCommands' fixed
// invalidation set (Routes + RunningConfig). Every route mutation touches
// both caches identically, so we pin them in one place.
func (c *RouteCommands) mutate(ctx context.Context, payload any, op string) error {
	return postMutation(ctx, c.poster, c.save, payload, op,
		c.queries.Routes.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

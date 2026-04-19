package command

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type DNSRouteCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
	isOS5   func() bool
}

func NewDNSRouteCommands(p Poster, s *SaveCoordinator, q *query.Queries, isOS5 func() bool) *DNSRouteCommands {
	if isOS5 == nil {
		isOS5 = func() bool { return false }
	}
	return &DNSRouteCommands{poster: p, save: s, queries: q, isOS5: isOS5}
}

// DNSRouteSpec describes a dns-proxy route entry.
type DNSRouteSpec struct {
	Group     string
	Interface string
	Reject    bool
}

// DeleteRoutes removes dns-proxy route entries in a single batch.
func (c *DNSRouteCommands) DeleteRoutes(ctx context.Context, specs []DNSRouteSpec) error {
	if !c.isOS5() {
		return query.ErrNotSupportedOnOS4
	}
	if len(specs) == 0 {
		return nil
	}
	routes := make([]any, 0, len(specs))
	for _, s := range specs {
		routes = append(routes, map[string]any{
			"group":     s.Group,
			"interface": s.Interface,
			"no":        true,
		})
	}
	payload := map[string]any{
		"dns-proxy": map[string]any{"route": routes},
	}
	return postMutation(ctx, c.poster, c.save, payload, "delete dns-proxy routes",
		c.queries.DNSProxy.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// UpsertRoutes adds or updates dns-proxy route entries in a single batch.
func (c *DNSRouteCommands) UpsertRoutes(ctx context.Context, specs []DNSRouteSpec) error {
	if !c.isOS5() {
		return query.ErrNotSupportedOnOS4
	}
	if len(specs) == 0 {
		return nil
	}
	routes := make([]any, 0, len(specs))
	for _, s := range specs {
		route := map[string]any{
			"group":     s.Group,
			"interface": s.Interface,
			"auto":      true,
		}
		if s.Reject {
			route["reject"] = true
		}
		routes = append(routes, route)
	}
	payload := map[string]any{
		"dns-proxy": map[string]any{"route": routes},
	}
	return postMutation(ctx, c.poster, c.save, payload, "upsert dns-proxy routes",
		c.queries.DNSProxy.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

package command

import (
	"context"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type ProxyCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewProxyCommands(p Poster, s *SaveCoordinator, q *query.Queries) *ProxyCommands {
	return &ProxyCommands{poster: p, save: s, queries: q}
}

func (c *ProxyCommands) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	proxy := map[string]any{
		"protocol": map[string]any{"proto": "socks5"},
		"upstream": map[string]any{
			"host": upstreamHost,
			"port": strconv.Itoa(upstreamPort),
		},
	}
	if socks5UDP {
		proxy["socks5-udp"] = true
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"description": description,
				"proxy":       proxy,
				"ip":          map[string]any{"global": map[string]any{"auto": true}},
				"up":          true,
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "create proxy "+name,
		c.queries.Interfaces.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *ProxyCommands) DeleteProxy(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"no": true},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "delete proxy "+name,
		c.queries.Interfaces.InvalidateAll,
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

func (c *ProxyCommands) ProxyUp(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": true},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "interface up "+name,
		func() { c.queries.Interfaces.Invalidate(name) })
}

func (c *ProxyCommands) ProxyDown(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": false},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "interface down "+name,
		func() { c.queries.Interfaces.Invalidate(name) })
}

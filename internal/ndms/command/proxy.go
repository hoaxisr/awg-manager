package command

import (
	"context"
	"fmt"
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
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("create proxy %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Interfaces.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

func (c *ProxyCommands) DeleteProxy(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"no": true},
		},
	}
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("delete proxy %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Interfaces.InvalidateAll()
	c.queries.Interfaces.Invalidate(name)
	c.queries.RunningConfig.InvalidateAll()
	return nil
}

func (c *ProxyCommands) ProxyUp(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": true},
		},
	}
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("proxy up %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Interfaces.Invalidate(name)
	return nil
}

func (c *ProxyCommands) ProxyDown(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"down": true},
		},
	}
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("proxy down %s: %w", name, err)
	}
	c.save.Request()
	c.queries.Interfaces.Invalidate(name)
	return nil
}

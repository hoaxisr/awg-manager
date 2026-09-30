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

// CreateProxy создаёт ProxyN и подтверждает его свежим списком (Confirm):
// дальнейшие команды по интерфейсу идут с доказательством (F546).
func (c *ProxyCommands) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) (query.Confirmed, error) {
	if err := postMutationChecked(ctx, c.poster, c.save, proxyPayload(name, description, upstreamHost, upstreamPort, socks5UDP), "create proxy "+name,
		c.queries.RunningConfig.InvalidateAll); err != nil {
		return query.Confirmed{}, err
	}
	conf, _, ok, err := c.queries.Interfaces.Confirm(ctx, name)
	if err != nil {
		return query.Confirmed{}, fmt.Errorf("create proxy: %w", err) // имя уже в ошибке Confirm
	}
	if !ok {
		return query.Confirmed{}, fmt.Errorf("create proxy %s: NDMS принял команду, но записи в списке нет", name)
	}
	return conf, nil
}

// CreateProxyLegacy — временно, до Task 19 (F546).
func (c *ProxyCommands) CreateProxyLegacy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	return postMutationChecked(ctx, c.poster, c.save, proxyPayload(name, description, upstreamHost, upstreamPort, socks5UDP), "create proxy "+name,
		c.queries.Interfaces.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func proxyPayload(name, description, upstreamHost string, upstreamPort int, socks5UDP bool) map[string]any {
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
	return map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"description": description,
				"proxy":       proxy,
				"ip":          map[string]any{"global": map[string]any{"auto": true}},
				"up":          true,
			},
		},
	}
}

// DeleteProxy снимает ProxyN. Снято (или его уже не было) — запись
// забывается в кэше сразу, не дожидаясь хука ifdestroyed (F546).
func (c *ProxyCommands) DeleteProxy(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"no": true},
		},
	}
	err := postMutationCheckedTolerant(ctx, c.poster, c.save, payload, "delete proxy "+name,
		isMissingInterface,
		c.queries.RunningConfig.InvalidateAll)
	if err == nil {
		c.queries.Interfaces.Forget(name)
	}
	return err
}

// DeleteProxyLegacy — временно, до Task 19 (F546).
func (c *ProxyCommands) DeleteProxyLegacy(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"no": true},
		},
	}
	// InvalidateAll уже убирает удалённый интерфейс из перестроенной карты;
	// per-name Invalidate делал бы `show interface ProxyN` по только что
	// снятому имени, и NDMS писал бы в свой лог E «unable to find» (F409).
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, "delete proxy "+name,
		isMissingInterface,
		c.queries.Interfaces.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

func (c *ProxyCommands) ProxyUp(ctx context.Context, iface query.Confirmed) error {
	return c.ProxyUpLegacy(ctx, iface.Name())
}

// ProxyUpLegacy — временно, до Task 19 (F546).
func (c *ProxyCommands) ProxyUpLegacy(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": true},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "proxy up "+name,
		func() { c.queries.Interfaces.Invalidate(name) })
}

func (c *ProxyCommands) ProxyDown(ctx context.Context, iface query.Confirmed) error {
	return c.ProxyDownLegacy(ctx, iface.Name())
}

// ProxyDownLegacy — временно, до Task 19 (F546).
func (c *ProxyCommands) ProxyDownLegacy(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"down": true},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "proxy down "+name,
		func() { c.queries.Interfaces.Invalidate(name) })
}

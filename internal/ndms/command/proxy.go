package command

import (
	"context"
	"errors"
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

// CreateProxy создаёт ProxyN по имени, выбранному свободным, и подтверждает
// его свежим списком (F546). Создание — голое, настройки — отдельным POST по
// Confirmed (F577, как у OpkgTun — F569): иначе отказ настройки в том же
// ответе прятал фразу «created», и созданное оставалось сиротой. NDMS не
// создал запись (на ≥5.01) — ErrNotCreated без настроек и сноса (F574).
// Отказ настроек — снос созданного этой командой. reply — вердикт ответа и
// на ошибке: откат вызывающего сносит только reply.Ours().
func (c *ProxyCommands) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) (query.Confirmed, CreateReply, error) {
	create := map[string]any{"interface": map[string]any{name: map[string]any{}}}
	conf, reply, err := CreateInterface(ctx, c.poster, c.save, c.queries, create, name, false,
		c.save.Request, c.queries.RunningConfig.InvalidateAll)
	if err != nil {
		return query.Confirmed{}, reply, fmt.Errorf("create proxy: %w", err) // имя уже в ошибке
	}
	if err := c.ConfigureProxy(ctx, conf, description, upstreamHost, upstreamPort, socks5UDP); err != nil {
		// Запись без нашего description и upstream — сирота, которую ни
		// владение по description, ни слот не узнают. Создана этой командой
		// (CreateInterface иначе уже вернул бы ErrNotCreated) — сносим.
		return query.Confirmed{}, reply, errors.Join(err, c.DeleteProxy(ctx, conf))
	}
	return conf, reply, nil
}

// ConfigureProxy пишет настройки подтверждённого ProxyN: description,
// upstream socks5, ip global, up. Владение записью проверяет вызывающий.
func (c *ProxyCommands) ConfigureProxy(ctx context.Context, iface query.Confirmed, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	name := iface.Name()
	return postMutationChecked(ctx, c.poster, c.save, proxyPayload(name, description, upstreamHost, upstreamPort, socks5UDP), "configure proxy "+name,
		func() { c.queries.Interfaces.Invalidate(name) },
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

func (c *ProxyCommands) ProxyUp(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": true},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "proxy up "+name,
		func() { c.queries.Interfaces.Invalidate(name) })
}

func (c *ProxyCommands) ProxyDown(ctx context.Context, iface query.Confirmed) error {
	name := iface.Name()
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"down": true},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "proxy down "+name,
		func() { c.queries.Interfaces.Invalidate(name) })
}

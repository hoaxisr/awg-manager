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
// дальнейшие команды по интерфейсу идут с доказательством (F546). reply —
// вердикт ответа и на ошибке: откат вызывающего сносит только reply.Ours().
func (c *ProxyCommands) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) (query.Confirmed, CreateReply, error) {
	// EnsureProxy шлёт это и по своей существующей записи (обновление),
	// поэтому «не создано» здесь не ошибка; сносить по имени можно только
	// созданное этой командой (F574).
	reply, err := PostCreate(ctx, c.poster, proxyPayload(name, description, upstreamHost, upstreamPort, socks5UDP), "create proxy "+name, name,
		c.save.Request, c.queries.RunningConfig.InvalidateAll)
	if err != nil {
		return query.Confirmed{}, reply, err
	}
	conf, err := confirmCreated(ctx, c.poster, c.save, c.queries, nil, name, reply.Ours())
	if err != nil {
		return query.Confirmed{}, reply, fmt.Errorf("create proxy: %w", err) // имя уже в ошибке подтверждения
	}
	return conf, reply, nil
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

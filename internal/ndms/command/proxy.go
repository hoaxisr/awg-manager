package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type ProxyCommands struct {
	ndmsMutator
}

// NewProxyCommands паникует на nil s (newMutator).
func NewProxyCommands(p Poster, s *SaveCoordinator, q *query.Queries) *ProxyCommands {
	return &ProxyCommands{ndmsMutator: newMutator(p, s, q)}
}

// CreateProxy создаёт ProxyN по имени, выбранному свободным, и подтверждает
// его свежим списком (F546). Создание — голое, настройки — отдельным POST по
// Confirmed (F577, как у OpkgTun — F569): иначе отказ настройки в том же
// ответе прятал фразу «created», и созданное оставалось сиротой. NDMS не
// создал запись — ErrNotCreated без настроек и сноса (F574).
// Отказ настроек — снос созданного этой командой. reply — вердикт ответа и
// на ошибке: откат вызывающего сносит только reply.Proven().
//
// Созданное этой командой осталось на роутере (подтверждения нет при
// доказанном «created», снос неподтверждённого или ненастроенного отказал) —
// ошибка *LeftCreatedError с именем и description записи на роутере:
// вызывающий отдаёт её отложенному сносу (F562), иначе голая сирота
// навсегда чужая для владения по description.
func (c *ProxyCommands) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) (query.Confirmed, CreateReply, error) {
	create := map[string]any{"interface": map[string]any{name: map[string]any{}}}
	conf, reply, err := c.createInterface(ctx, create, name, false,
		c.save.Request, c.queries.RunningConfig.InvalidateAll)
	if err != nil {
		err = fmt.Errorf("create proxy: %w", err) // имя уже в ошибке
		// Голая (настроек не было): «created» доказан, записи в списке нет,
		// а снос отказал.
		if errors.Is(err, ErrLeftOnRouter) {
			err = &LeftCreatedError{Name: name, Err: err}
		}
		return query.Confirmed{}, reply, err
	}
	if err := c.ConfigureProxy(ctx, conf, description, upstreamHost, upstreamPort, socks5UDP); err != nil {
		// Запись без нашего description и upstream — сирота, которую ни
		// владение по description, ни слот не узнают. Создана этой командой
		// (CreateInterface иначе уже вернул бы ErrNotCreated) — сносим.
		derr := c.DeleteProxy(ctx, conf)
		if derr == nil {
			return query.Confirmed{}, reply, err
		}
		return query.Confirmed{}, reply, c.leftConfigured(ctx, name, errors.Join(err, derr))
	}
	return conf, reply, nil
}

// LeftCreatedError — ProxyN Name, созданный этой командой, остался на
// роутере; Desc — его description там ("" у голой записи).
type LeftCreatedError struct {
	Name, Desc string
	Err        error
}

func (e *LeftCreatedError) Error() string { return e.Err.Error() }
func (e *LeftCreatedError) Unwrap() error { return e.Err }

// leftConfigured — настройки отвергнуты, снос тоже: description на роутере
// читается свежим списком (элементы настроек NDMS мог применить частично).
// Записи нет — оставлять нечего. Список не прочитан — "": отказ настроек и
// сноса обычно один сбой RCI; метка с не тем description снимется тиком без
// сноса (MarkedForeign) — сирота, но не снос чужого.
func (c *ProxyCommands) leftConfigured(ctx context.Context, name string, err error) error {
	_, rec, ok, lerr := c.queries.Interfaces.Confirm(ctx, name)
	switch {
	case lerr != nil:
		return &LeftCreatedError{Name: name, Err: err}
	case !ok:
		return err
	case rec == nil:
		return &LeftCreatedError{Name: name, Err: err}
	}
	return &LeftCreatedError{Name: name, Desc: rec.Description, Err: err}
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

// DeleteProxy снимает ProxyN через deleteInterface.
func (c *ProxyCommands) DeleteProxy(ctx context.Context, iface query.Confirmed) error {
	return c.deleteInterface(ctx, iface, "delete proxy "+iface.Name(),
		c.queries.RunningConfig.InvalidateAll)
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

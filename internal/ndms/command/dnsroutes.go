package command

import (
	"context"
	"fmt"

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

// DNSRouteSpec — постановка строки dns-proxy route: интерфейс подтверждён
// свежим списком (F546).
type DNSRouteSpec struct {
	Group     string
	Interface query.Confirmed
	Reject    bool
}

// DNSRouteRef — снос строки, взятой из выдачи роутера: интерфейс в ней —
// строка как есть (строка существует, пока её не снесли).
type DNSRouteRef struct {
	Group, Interface string
}

// DeleteRoutes removes dns-proxy route entries in a single batch.
func (c *DNSRouteCommands) DeleteRoutes(ctx context.Context, refs []DNSRouteRef) error {
	if !c.isOS5() {
		return query.ErrNotSupportedOnOS4
	}
	if len(refs) == 0 {
		return nil
	}
	routes := make([]any, 0, len(refs))
	for _, s := range refs {
		routes = append(routes, map[string]any{
			"group":     s.Group,
			"interface": s.Interface,
			"no":        true,
		})
	}
	payload := map[string]any{
		"dns-proxy": map[string]any{"route": routes},
	}
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, "delete dns-proxy routes",
		toleratesMissingDNSRoute,
		c.queries.DNSProxy.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// SetDisabled toggles a dns-proxy route's disable flag without deleting
// the route, using Keenetic's native `dns-proxy.route.disable` command.
// `index` is the stable hash returned by /show/sc/dns-proxy/route; we
// look it up on the caller side.
//
// Wire payload note: NDMS uses a double-negative — `no:false` applies
// the disable (rule becomes inactive), `no:true` negates the disable
// (rule becomes active). `no` is therefore the LOGICAL inverse of the
// desired "disabled" state.
func (c *DNSRouteCommands) SetDisabled(ctx context.Context, index string, disabled bool) error {
	if !c.isOS5() {
		return query.ErrNotSupportedOnOS4
	}
	if index == "" {
		return nil
	}
	payload := map[string]any{
		"dns-proxy": map[string]any{
			"route": map[string]any{
				"disable": map[string]any{
					"index": index,
					"no":    !disabled,
				},
			},
		},
	}
	if _, err := c.poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("toggle dns-proxy route disable: %w", err)
	}
	c.queries.DNSProxy.InvalidateAll()
	c.queries.RunningConfig.InvalidateAll()
	// Flush save synchronously, not via the debounced coordinator. NDMS
	// applies dns-proxy.route.disable to running state on POST, but the
	// flag only surfaces in /show/sc/… (which Keenetic's web UI reads)
	// after system-configuration-save. Matching the native UI's batched
	// disable+save POST sequence makes toggles show up immediately on
	// both sides rather than after the 500ms debounce window.
	if err := c.save.Flush(ctx); err != nil {
		return fmt.Errorf("save after dns-proxy disable toggle: %w", err)
	}
	return nil
}

// ReplaceRoutes переписывает набор dns-proxy маршрутов одним батчем: сначала
// снос перечисленных строк, затем запись новых в порядке приоритета.
//
// Обе половины обязаны ехать в ОДНОМ payload. Порядок строк `dns-proxy route`
// в NDMS и есть приоритет выбора туннеля, а переставить строку нечем: upsert
// существующей её не двигает, новая всегда дописывается в хвост (проверено на
// KeeneticOS 5.01). Значит смена приоритета — это снос блока и запись заново.
// Разбиение на два POST оставило бы группу без правил на всё время между ними,
// а на больших списках между сносом и записью успевает пройти обновление
// object-group — сотни миллисекунд без DNS-маршрутизации. NDMS применяет
// элементы payload по порядку, поэтому один POST закрывает окно.
func (c *DNSRouteCommands) ReplaceRoutes(ctx context.Context, deletes []DNSRouteRef, upserts []DNSRouteSpec) error {
	if !c.isOS5() {
		return query.ErrNotSupportedOnOS4
	}
	if len(deletes)+len(upserts) == 0 {
		return nil
	}
	// Нулевой Confirmed (поле пропущено в литерале) — отказ всего батча без
	// POST: постановка по имени "" мимо подтверждения (F546).
	for _, s := range upserts {
		if s.Interface.Name() == "" {
			return fmt.Errorf("replace dns-proxy routes: группа %s без интерфейса", s.Group)
		}
	}
	routes := make([]any, 0, len(deletes)+len(upserts))
	for _, s := range deletes {
		routes = append(routes, map[string]any{
			"group":     s.Group,
			"interface": s.Interface,
			"no":        true,
		})
	}
	upsertIfaces := make(map[string]bool, len(upserts))
	for _, s := range upserts {
		route := map[string]any{
			"group":     s.Group,
			"interface": s.Interface.Name(),
			"auto":      true,
		}
		if s.Reject {
			route["reject"] = true
		}
		routes = append(routes, route)
		upsertIfaces[s.Interface.Name()] = true
	}
	payload := map[string]any{
		"dns-proxy": map[string]any{"route": routes},
	}
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, "replace dns-proxy routes",
		func(msg string) bool { return toleratesReplaceRoutes(msg, upsertIfaces) },
		c.queries.DNSProxy.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

// toleratesReplaceRoutes — поблажки смешанного батча. Ответ NDMS плоский:
// отличить отказ сноса от отказа постановки можно только по смыслу сообщения.
//   - «правила нет» приходит только со сноса и всегда безобиден;
//   - «нет интерфейса» безобиден, когда мы этот интерфейс сносим и заново не
//     ставим (штатный дрейф: интерфейс удалили раньше маршрута). Если он есть
//     среди записываемых — это настоящий отказ постановки, и он обязан всплыть.
func toleratesReplaceRoutes(msg string, upsertIfaces map[string]bool) bool {
	if isMissingDNSRouteRule(msg) {
		return true
	}
	if name := missingInterfaceName(msg); name != "" {
		return !upsertIfaces[name]
	}
	return false
}

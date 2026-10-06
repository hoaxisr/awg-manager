package command

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type DNSRouteCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
	isOS5   func() bool
	// dirtyAt — эпоха Requested координатора после последней нашей правки
	// dns-proxy route: sc-вид покажет её только после сохранения, начатого
	// позже (F568, FlushPendingSave).
	dirtyAt atomic.Uint64
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

// FlushPendingSave синхронно сохраняет конфигурацию, если sc-вид dns-proxy
// route грязен с последнего завершённого сохранения (F568).
// `/show/sc/dns-proxy/route` показывает снесённую через RCI строку до
// `system configuration save` (добавленную — сразу; стенд): без сохранения
// сверка видит уже снесённый маршрут и сносит его повторно — E «unable to
// find the DNS route» в журнале ndm. Гейт — эпохи координатора, а не
// PendingCount: ожидающее сохранение после чужих правок (снос туннелей
// пачкой) sc-вид не портит, и Flush на нём сериализовал бы пачку на событиях
// сохранения (Н10b). Ошибка сохранения — ошибка вызывающему: по устаревшему
// виду сверка посчитала бы неверные сносы.
func (c *DNSRouteCommands) FlushPendingSave(ctx context.Context) error {
	if !c.isOS5() {
		return nil
	}
	if _, saved := c.save.Epoch(); saved >= c.dirtyAt.Load() {
		return nil
	}
	return c.save.Flush(ctx)
}

// markDirty — после правки dns-proxy route: sc-вид устарел до сохранения,
// заказанного этой правкой.
func (c *DNSRouteCommands) markDirty() {
	req, _ := c.save.Epoch()
	for {
		cur := c.dirtyAt.Load()
		if req <= cur || c.dirtyAt.CompareAndSwap(cur, req) {
			return
		}
	}
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
		c.queries.RunningConfig.InvalidateAll,
		c.markDirty)
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
	c.markDirty()
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
		c.queries.RunningConfig.InvalidateAll,
		c.markDirty)
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

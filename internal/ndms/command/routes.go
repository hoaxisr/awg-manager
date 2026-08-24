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
	// V6 selects the IPv6 route form: the payload uses the "ipv6" outer key and
	// emits ONLY {network, interface, auto/no} — no mask/host/reject/comment
	// (the v6 pool route is a plain auto network route). v4 (V6 false) keeps the
	// full form (auto/reject/comment/mask/host).
	V6 bool
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
	return c.mutateTolerant(ctx, payload, "remove default route "+name, isNetlinkFileExists)
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
	return c.mutateTolerant(ctx, payload, "remove ipv6 default route "+name, isNetlinkFileExists)
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

// AddStaticRoute adds a network or host route to the given interface. For v6
// (route.V6) it emits the bare {prefix, interface, auto} form under the "ipv6"
// key (NDMS reasserts on iface up); for v4 it keeps the full
// auto/reject/comment/mask/host form under "ip".
//
// Ключ подсети у v6 — ИМЕННО prefix, не network (как у v4). NDMS молча
// отбрасывает неизвестное поле, и запрос вырождается: add остаётся без
// обязательной подсети и получает ложный «no input», а remove без неё целится
// в ::/0 — то есть в ДЕФОЛТНЫЙ маршрут интерфейса. Форма стенд-проверена
// 2026-08-24: сам роутер хранит запись как {prefix, interface, auto, comment}.
func (c *RouteCommands) AddStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	if route.V6 {
		payload := map[string]any{
			"ipv6": map[string]any{
				"route": map[string]any{
					"prefix":    route.Network,
					"interface": route.Interface,
					"auto":      true,
				},
			},
		}
		return c.mutate(ctx, payload, "add ipv6 static route")
	}
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

// RemoveStaticRoute removes a previously-added static route. For v6 (route.V6)
// it emits the bare {network, interface, no} form under "ipv6"; for v4 it emits
// {interface, no, host|network+mask} under "ip".
func (c *RouteCommands) RemoveStaticRoute(ctx context.Context, route StaticRouteSpec) error {
	if route.V6 {
		payload := map[string]any{
			"ipv6": map[string]any{
				"route": map[string]any{
					"prefix":    route.Network,
					"interface": route.Interface,
					"no":        true,
				},
			},
		}
		return c.mutateTolerant(ctx, payload, "remove ipv6 static route", isNoSuchInterface)
	}
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
	return c.mutateTolerant(ctx, payload, "remove static route", isNoSuchInterface)
}

// mutate is a thin wrapper over postMutation with RouteCommands' fixed
// invalidation set (Routes + RunningConfig). Every route mutation touches
// both caches identically, so we pin them in one place.
func (c *RouteCommands) mutate(ctx context.Context, payload any, op string) error {
	return c.mutateTolerant(ctx, payload, op, nil)
}

// mutateTolerant — mutate, признающий часть отказов безобидными. Нужен снятию
// маршрутов: отложенный drain fakeip снимает их уже ПОСЛЕ удаления интерфейса,
// и NDMS отвечает «no such interface» на живую, ожидаемую ситуацию.
func (c *RouteCommands) mutateTolerant(ctx context.Context, payload any, op string, tolerate func(string) bool) error {
	return postMutationCheckedTolerant(ctx, c.poster, c.save, payload, op, tolerate,
		c.queries.Routes.InvalidateAll,
		c.queries.RunningConfig.InvalidateAll)
}

package command

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// freshStaticRoutes — /show/rc/ip/route, прочитанный сейчас. Снимок кэша
// внешние правки (веб-морда роутера) не сбрасывают, а List при отказе RCI
// отдаёт устаревшее значение без ошибки; Fetch ходит мимо кэша и
// singleflight, и отказ всплывает — решения о владении по нему fail-closed
// (11.B/11.6).
func (c *RouteCommands) freshStaticRoutes(ctx context.Context) ([]query.StaticRouteEntry, error) {
	if c.queries == nil || c.queries.StaticRoutes == nil {
		return nil, fmt.Errorf("static route store not wired")
	}
	entries, err := c.queries.StaticRoutes.Fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("read static routes: %w", err)
	}
	return entries, nil
}

// NetworkRouteOwner — есть ли статическая запись маршрута на пару (сеть,
// интерфейс) и наша ли она. Читается /show/rc/ip/route (11.A/11.2): там есть
// комментарий и записи опущенных интерфейсов, чего у /show/ip/route нет.
// own — комментарий равен метке ЦЕЛИКОМ. /32 роутер может хранить host-формой
// (адаптер и ставит её так) — такая запись сопоставляется по host.
// Читается всегда свежо (freshStaticRoutes): по устаревшему снимку мы бы
// сняли чужую запись.
func (c *RouteCommands) NetworkRouteOwner(ctx context.Context, network, mask, iface, comment string) (exists, own bool, err error) {
	entries, err := c.freshStaticRoutes(ctx)
	if err != nil {
		return false, false, err
	}
	for _, e := range entries {
		if e.Interface != iface {
			continue
		}
		// Пустая сеть у host-записи: без гарда вызов с пустой сетью принял бы
		// первую host-запись интерфейса за искомую.
		byNet := e.Network != "" && e.Network == network && e.Mask == mask
		byHost := e.Host != "" && e.Host == network && mask == "255.255.255.255"
		if byNet || byHost {
			return true, e.Comment == comment, nil
		}
	}
	return false, false, nil
}

// RemoveOwnNetworkRoute снимает запись только со своей меткой (spec.Comment);
// чужую или отсутствующую — успех без мутации. В отличие от RemoveOwnHostRoute
// слепой формы нет намеренно: там снималось наследство прежних версий, здесь
// чужая запись на той же паре — пользовательская.
func (c *RouteCommands) RemoveOwnNetworkRoute(ctx context.Context, spec StaticRouteSpec) (bool, error) {
	network, mask := spec.Network, spec.Mask
	if spec.Host != "" {
		network, mask = spec.Host, "255.255.255.255"
	}
	_, own, err := c.NetworkRouteOwner(ctx, network, mask, spec.Interface, spec.Comment)
	if err != nil || !own {
		return false, err
	}
	return true, c.RemoveStaticRoute(ctx, spec)
}

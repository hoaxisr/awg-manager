package command

import (
	"context"
	"fmt"
)

// NetworkRouteOwner — есть ли статическая запись маршрута на пару (сеть,
// интерфейс) и наша ли она. Читается /show/rc/ip/route (11.A/11.2): там есть
// комментарий и записи опущенных интерфейсов, чего у /show/ip/route нет.
// own — комментарий равен метке ЦЕЛИКОМ. /32 роутер может хранить host-формой
// (адаптер и ставит её так) — такая запись сопоставляется по host.
func (c *RouteCommands) NetworkRouteOwner(ctx context.Context, network, mask, iface, comment string) (exists, own bool, err error) {
	entries, err := c.queries.StaticRoutes.List(ctx)
	if err != nil {
		return false, false, fmt.Errorf("read static routes: %w", err)
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

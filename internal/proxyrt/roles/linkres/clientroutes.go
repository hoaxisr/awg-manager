package linkres

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// RouteHooks — сигнатуры wdtt.ClientRouteHooks (client_access.go:8-11);
// прод — clientroute-сервис.
type RouteHooks interface {
	OnTunnelStart(ctx context.Context, tunnelID, kernelIface string) error
	OnTunnelStop(ctx context.Context, tunnelID string) error
}

// ClientRoutes — ресурс client_routes («VPN для устройств»). Честно про его
// природу: это УВЕДОМЛЕНИЕ подсистемы clientroute, наблюдаемого состояния у
// него нет — фактические ip rules применяет и чинит сама clientroute.
// Наблюдение — защёлка «о каком интерфейсе уведомили»; переживает прогоны,
// сбрасывается сменой интерфейса и выключением.
type ClientRoutes struct {
	id       proxyrt.ResourceID
	hooks    RouteHooks
	tunnelID string
	iface    string
	active   bool
	notified string // интерфейс последнего OnTunnelStart; "" — уведомлён stop
}

func NewClientRoutes(id proxyrt.ResourceID, hooks RouteHooks) *ClientRoutes {
	return &ClientRoutes{id: id, hooks: hooks}
}

func (c *ClientRoutes) SetDesired(tunnelID, kernelIface string, active bool) {
	c.tunnelID, c.iface, c.active = tunnelID, kernelIface, active
}

func (c *ClientRoutes) ID() proxyrt.ResourceID { return c.id }

func (c *ClientRoutes) Observe(context.Context) (proxyrt.Observation, error) {
	want := ""
	if c.active {
		want = c.iface
	}
	return proxyrt.Observation{Known: true, Exists: c.notified == want,
		Attrs: map[string]string{"notified": c.notified}}, nil
}

func (c *ClientRoutes) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists {
		return nil
	}
	if c.active {
		return []proxyrt.Step{{Resource: c.id, Op: "notify-start",
			Args: map[string]string{"iface": c.iface}, Reason: "clientroute не уведомлён о старте"}}
	}
	return []proxyrt.Step{{Resource: c.id, Op: "notify-stop", Reason: "clientroute не уведомлён об остановке"}}
}

func (c *ClientRoutes) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "notify-start":
		if err := c.hooks.OnTunnelStart(ctx, c.tunnelID, c.iface); err != nil {
			return err
		}
		c.notified = c.iface
		return nil
	case "notify-stop":
		if err := c.hooks.OnTunnelStop(ctx, c.tunnelID); err != nil {
			return err
		}
		c.notified = ""
		return nil
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (c *ClientRoutes) RecheckAfter() time.Duration { return 0 }

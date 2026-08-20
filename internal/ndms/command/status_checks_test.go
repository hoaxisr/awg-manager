package command

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Ответы сняты с живого роутера (KeeneticOS 5.01, 2026-08-20): NDMS отвечает
// HTTP 200 и прячет отказ во вложенном status.
const (
	// Отказы, которые обязаны всплывать.
	respPermitNoIface = `{"ip":{"policy":{"P":{"permit":{"status":[{"status":"error","code":"25362475",` +
		`"ident":"Network::PolicyTable","message":"no such interface."}]}}}}}`
	respHostUnregistered = `{"ip":{"hotspot":{"host":{"policy":{"status":[{"status":"error","code":"19007841",` +
		`"ident":"Hotspot::Manager","message":"host \"00:00:00:00:00:01\" is unregistered."}]}}}}}`
	respRouteNoIface = `{"ip":{"route":{"status":[{"status":"error","code":"5046299",` +
		`"ident":"Network::RoutingTable","message":"no such interface: OpkgTun42."}]}}}`
	respNATNoIface = `{"ip":{"nat":{"status":[{"status":"error","code":"101122063",` +
		`"ident":"Network::Nat","message":"no \"AwgmNoSeg\" IP interface found."}]}}}`
	respStaticNATUnknown = `{"ip":{"static":[{"status":[{"status":"error","code":"101187607",` +
		`"ident":"Network::StaticNat","message":"unknown interface \"AwgmNoSeg\"."}]}]}}`
	respDNSRouteNoIface = `{"dns-proxy":{"route":[{"status":[{"status":"error","code":"4456948",` +
		`"ident":"Dns::Route::Manager","message":"no such interface: OpkgTun42."}]}]}}`
	respASCRejected = `{"interface":{"Wireguard42":{"wireguard":{"asc":{"status":[{"status":"error","code":"1",` +
		`"ident":"Network::Interface::Wireguard","message":"asc rejected."}]}}}}}`
	respProxyMissing = `{"interface":{"Proxy9":{"status":[{"status":"error","code":"6553611",` +
		`"ident":"Network::Interface::Repository","message":"unable to find interface \"Proxy9\"."}]}}}`

	// Безобидные отказы идемпотентных сносов.
	respNoInput = `{"ip":{"policy":{"P":{"status":[{"status":"error","code":"7471107",` +
		`"ident":"Command::Root","message":"no input [http/rci 127.0.0.1]."}]}}}}`
	respGroupNoInput = `{"object-group":{"fqdn":{"g":{"status":[{"status":"error","code":"7471107",` +
		`"ident":"Command::Root","message":"no input [http/rci 127.0.0.1]."}]}}}}`
	respDNSRuleMissing = `{"dns-proxy":{"route":[{"status":[{"status":"error","code":"4457148",` +
		`"ident":"Dns::Route::Manager","message":"unable to find the DNS route rule."}]}]}}`
)

// Отказ роутера обязан стать ошибкой вызова, а не тишиной.
func TestMutators_SurfaceNestedErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		resp string
		call func(*fakePoster) error
	}{
		{"CreatePolicy", respNoInput, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.CreatePolicy(ctx, "P", "d")
		}},
		{"PermitInterface", respPermitNoIface, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.PermitInterface(ctx, "P", "OpkgTun42", 0)
		}},
		{"DenyInterface", respPermitNoIface, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.DenyInterface(ctx, "P", "OpkgTun42")
		}},
		{"SetStandalone", respPermitNoIface, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.SetStandalone(ctx, "P", true)
		}},
		{"SetPolicyDescription", respPermitNoIface, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.SetDescription(ctx, "P", "d")
		}},
		{"AssignDevice", respHostUnregistered, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.AssignDevice(ctx, "00:00:00:00:00:01", "P")
		}},
		{"UnassignDevice", respHostUnregistered, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.UnassignDevice(ctx, "00:00:00:00:00:01")
		}},
		{"AddStaticRoute", respRouteNoIface, func(p *fakePoster) error {
			c := newRouteCommandsWith(p)
			return c.AddStaticRoute(ctx, StaticRouteSpec{Network: "203.0.113.0", Mask: "255.255.255.0", Interface: "OpkgTun42"})
		}},
		{"SetSegmentNAT", respNATNoIface, func(p *fakePoster) error {
			return newNATCommandsWith(p).SetSegmentNAT(ctx, "AwgmNoSeg")
		}},
		{"SetStaticNAT", respStaticNATUnknown, func(p *fakePoster) error {
			return newNATCommandsWith(p).SetStaticNAT(ctx, "AwgmNoSeg", "ISP")
		}},
		{"UpsertDNSRoutes", respDNSRouteNoIface, func(p *fakePoster) error {
			return newDNSRouteCommandsWith(p).UpsertRoutes(ctx, []DNSRouteSpec{{Group: "g", Interface: "OpkgTun42"}})
		}},
		{"UpsertFQDNGroup", respGroupNoInput, func(p *fakePoster) error {
			return newObjectGroupCommandsWith(p).UpsertGroup(ctx, FQDNGroupMutation{Name: "g", AddIncludes: []string{"example.com"}})
		}},
		{"CreateProxy", respProxyMissing, func(p *fakePoster) error {
			return newProxyCommandsWith(p).CreateProxy(ctx, "Proxy9", "d", "127.0.0.1", 1080, false)
		}},
		{"ProxyUp", respProxyMissing, func(p *fakePoster) error {
			return newProxyCommandsWith(p).ProxyUp(ctx, "Proxy9")
		}},
		{"ProxyDown", respProxyMissing, func(p *fakePoster) error {
			return newProxyCommandsWith(p).ProxyDown(ctx, "Proxy9")
		}},
		{"SetASCParams", respASCRejected, func(p *fakePoster) error {
			return NewWireguardCommands(p, newSaveFor(p), testQueries()).
				SetASCParams(ctx, "Wireguard42", json.RawMessage(`{"jc":"5"}`))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			poster := &fakePoster{}
			poster.SetResponse(c.resp)
			if err := c.call(poster); err == nil {
				t.Error("отказ роутера проглочен")
			}
		})
	}
}

// Идемпотентные сносы: «этого уже нет» — не ошибка.
func TestRemovals_TolerateMissingTarget(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		resp string
		call func(*fakePoster) error
	}{
		{"DeletePolicy", respNoInput, func(p *fakePoster) error {
			c, _, _, _ := newTestPolicyCommandsWith(p)
			return c.DeletePolicy(ctx, "P")
		}},
		{"DeleteProxy", respProxyMissing, func(p *fakePoster) error {
			return newProxyCommandsWith(p).DeleteProxy(ctx, "Proxy9")
		}},
		{"RemoveStaticRoute", respRouteNoIface, func(p *fakePoster) error {
			return newRouteCommandsWith(p).RemoveStaticRoute(ctx, StaticRouteSpec{
				Network: "203.0.113.0", Mask: "255.255.255.0", Interface: "OpkgTun42"})
		}},
		{"RemoveStaticNAT", respStaticNATUnknown, func(p *fakePoster) error {
			return newNATCommandsWith(p).RemoveStaticNAT(ctx, "AwgmNoSeg", "ISP")
		}},
		{"DeleteDNSRoutes", respDNSRuleMissing, func(p *fakePoster) error {
			return newDNSRouteCommandsWith(p).DeleteRoutes(ctx, []DNSRouteSpec{{Group: "g", Interface: "OpkgTun10"}})
		}},
		{"DeleteFQDNGroups", respGroupNoInput, func(p *fakePoster) error {
			return newObjectGroupCommandsWith(p).DeleteGroups(ctx, []string{"g"})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			poster := &fakePoster{}
			poster.SetResponse(c.resp)
			if err := c.call(poster); err != nil {
				t.Errorf("повторный снос считается ошибкой: %v", err)
			}
		})
	}
}

// Толерантность узкая: настоящий отказ сноса всплывает.
func TestRemovals_SurfaceRealErrors(t *testing.T) {
	ctx := context.Background()
	busy := `{"ip":{"policy":{"P":{"status":[{"status":"error","code":"1",` +
		`"ident":"Network::PolicyTable","message":"policy is in use."}]}}}}`
	poster := &fakePoster{}
	poster.SetResponse(busy)
	c, _, _, _ := newTestPolicyCommandsWith(poster)
	err := c.DeletePolicy(ctx, "P")
	if err == nil {
		t.Fatal("реальный отказ сноса проглочен")
	}
	if !strings.Contains(err.Error(), "policy is in use") {
		t.Errorf("ошибка не доносит ответ роутера: %v", err)
	}
}

// ── конструкторы поверх заданного постера ───────────────────────────────────

func newSaveFor(p Poster) *SaveCoordinator {
	return NewSaveCoordinator(p, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
}

func newTestPolicyCommandsWith(p Poster) (*PolicyCommands, *SaveCoordinator, *query.Queries, *spyHookNotifier) {
	sc, q, hn := newSaveFor(p), testQueries(), &spyHookNotifier{}
	return NewPolicyCommands(p, sc, q, hn), sc, q, hn
}

func newRouteCommandsWith(p Poster) *RouteCommands {
	return NewRouteCommands(p, newSaveFor(p), testQueries())
}

func newNATCommandsWith(p Poster) *NATCommands {
	return NewNATCommands(p, newSaveFor(p), testQueries())
}

func newDNSRouteCommandsWith(p Poster) *DNSRouteCommands {
	return NewDNSRouteCommands(p, newSaveFor(p), testQueries(), func() bool { return true })
}

func newObjectGroupCommandsWith(p Poster) *ObjectGroupCommands {
	return NewObjectGroupCommands(p, newSaveFor(p), testQueries())
}

func newProxyCommandsWith(p Poster) *ProxyCommands {
	return NewProxyCommands(p, newSaveFor(p), testQueries())
}

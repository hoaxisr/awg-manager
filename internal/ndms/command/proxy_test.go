package command

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func newTestProxyCommands(_ *testing.T) (*ProxyCommands, *fakePoster, *SaveCoordinator) {
	cmds, poster, sc, _ := newTestProxyCommandsWithGetter()
	return cmds, poster, sc
}

func newTestProxyCommandsWithGetter() (*ProxyCommands, *fakePoster, *SaveCoordinator, *query.FakeGetter) {
	poster := &fakePoster{}
	pub := &fakePublisher{}
	sc := NewSaveCoordinator(poster, pub, 500*time.Millisecond, 5*time.Second, 0, nil)
	g := query.NewFakeGetter()
	q := query.NewQueries(query.Deps{Getter: g, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewProxyCommands(poster, sc, q), poster, sc, g
}

func TestProxyCommands_CreateProxy_SOCKS5(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	if err := cmds.CreateProxy(context.Background(), "Proxy0", "sing-box", "127.0.0.1", 1080, true); err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	proxy := iface["proxy"].(map[string]any)
	if proxy["protocol"].(map[string]any)["proto"] != "socks5" {
		t.Errorf("proto: %v", proxy["protocol"])
	}
	if proxy["upstream"].(map[string]any)["port"] != "1080" {
		t.Errorf("port: %v", proxy["upstream"])
	}
	if proxy["socks5-udp"] != true {
		t.Errorf("socks5-udp: %v", proxy["socks5-udp"])
	}
	if iface["up"] != true {
		t.Errorf("up: %v", iface["up"])
	}
}

func TestProxyCommands_CreateProxy_NoUDP(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.CreateProxy(context.Background(), "Proxy1", "", "127.0.0.1", 1081, false)
	p := poster.Payloads()[0].(map[string]any)
	proxy := p["interface"].(map[string]any)["Proxy1"].(map[string]any)["proxy"].(map[string]any)
	if _, ok := proxy["socks5-udp"]; ok {
		t.Errorf("socks5-udp must be absent when UDP=false")
	}
}

func TestProxyCommands_DeleteProxy(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.DeleteProxy(context.Background(), "Proxy0")
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if iface["no"] != true {
		t.Errorf("no: %v", iface["no"])
	}
}

// F409: после `no interface ProxyN` нельзя спрашивать NDMS про это имя —
// `show interface` по снятому интерфейсу даёт E «unable to find» в ndm-логе.
// Список целиком перечитать можно, по имени — нет.
func TestProxyCommands_DeleteProxy_NoShowByNameAfterDelete(t *testing.T) {
	cmds, _, _, g := newTestProxyCommandsWithGetter()
	// Список интерфейсов есть — иначе bootstrap кэша падает раньше, чем
	// доходит до запроса по имени, и тест зелен при любом коде.
	g.SetJSON("/show/interface/", `{"Proxy0":{"id":"Proxy0","type":"Proxy","state":"up"}}`)
	if err := cmds.DeleteProxy(context.Background(), "Proxy0"); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	if n := g.PostInterfaceCalls("Proxy0"); n != 0 {
		t.Fatalf("после удаления ушло %d запросов show interface Proxy0 — NDMS ответит «unable to find»", n)
	}
}

func TestProxyCommands_ProxyUp(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.ProxyUp(context.Background(), "Proxy0")
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if iface["up"] != true {
		t.Errorf("up: %v", iface["up"])
	}
}

func TestProxyCommands_ProxyDown_UsesDownKey(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.ProxyDown(context.Background(), "Proxy0")
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if iface["down"] != true {
		t.Errorf("down: %v", iface["down"])
	}
	if _, ok := iface["up"]; ok {
		t.Errorf("up must be absent")
	}
}

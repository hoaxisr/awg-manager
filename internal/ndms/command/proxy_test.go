package command

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
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
	// Созданное подтверждается списком: имена, которые тесты создают, в нём уже есть.
	g.SetJSON("/show/interface/", `{"Proxy0":{"id":"Proxy0","type":"Proxy"},"Proxy1":{"id":"Proxy1","type":"Proxy"}}`)
	q := query.NewQueries(query.Deps{Getter: g, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewProxyCommands(poster, sc, q), poster, sc, g
}

func TestProxyCommands_CreateProxy_SOCKS5(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	if _, err := cmds.CreateProxy(context.Background(), "Proxy0", "sing-box", "127.0.0.1", 1080, true); err != nil {
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
	_, _ = cmds.CreateProxy(context.Background(), "Proxy1", "", "127.0.0.1", 1081, false)
	p := poster.Payloads()[0].(map[string]any)
	proxy := p["interface"].(map[string]any)["Proxy1"].(map[string]any)["proxy"].(map[string]any)
	if _, ok := proxy["socks5-udp"]; ok {
		t.Errorf("socks5-udp must be absent when UDP=false")
	}
}

func TestProxyCommands_DeleteProxy(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.DeleteProxy(context.Background(), confirmed(t, "Proxy0"))
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
	// Любой POST слоя запросов, кроме резолвера имён, — сюда (точечных
	// хелперов у FakeGetter нет, F546).
	var posts atomic.Int32
	g.SetPostHandler(func(any) (json.RawMessage, error) {
		posts.Add(1)
		return nil, errors.New("unexpected query POST")
	})
	if err := cmds.DeleteProxy(context.Background(), confirmed(t, "Proxy0")); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	if n := posts.Load(); n != 0 {
		t.Fatalf("после удаления ушло %d запросов show interface Proxy0 — NDMS ответит «unable to find»", n)
	}
}

func TestProxyCommands_ProxyUp(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.ProxyUp(context.Background(), confirmed(t, "Proxy0"))
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if iface["up"] != true {
		t.Errorf("up: %v", iface["up"])
	}
}

func TestProxyCommands_ProxyDown_UsesDownKey(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.ProxyDown(context.Background(), confirmed(t, "Proxy0"))
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if iface["down"] != true {
		t.Errorf("down: %v", iface["down"])
	}
	if _, ok := iface["up"]; ok {
		t.Errorf("up must be absent")
	}
}

func TestCreateProxy_ReturnsConfirmed(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	f.ExpectCreate("Proxy0")
	c, err := cmds.Proxies.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, false)
	if err != nil || c.Name() != "Proxy0" {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "Proxy0"); rec == nil {
		t.Fatal("Confirm после создания обязан положить запись в кэш")
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

// Снятый ProxyN забывается в кэше сразу, не дожидаясь ifdestroyed.
func TestDeleteProxy_Forgets(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil, ndms.Interface{ID: "Proxy0"})
	c, _, ok, err := q.Interfaces.Confirm(context.Background(), "Proxy0")
	if err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	if err := cmds.Proxies.DeleteProxy(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if rec, _ := q.Interfaces.Get(context.Background(), "Proxy0"); rec != nil || f.Has("Proxy0") {
		t.Fatalf("запись осталась: cache=%v ndms=%v", rec != nil, f.Has("Proxy0"))
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

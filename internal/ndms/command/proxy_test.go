package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	sc.SetSaveTimings(SaveEventCap, 0, SaveAfterRemoval)
	g := query.NewFakeGetter()
	// Созданное подтверждается списком: имена, которые тесты создают, в нём уже есть.
	g.SetJSON("/show/interface/", `{"Proxy0":{"id":"Proxy0","type":"Proxy"},"Proxy1":{"id":"Proxy1","type":"Proxy"}}`)
	q := query.NewQueries(query.Deps{Getter: g, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	return NewProxyCommands(poster, sc, q), poster, sc, g
}

func TestProxyCommands_CreateProxy_SOCKS5(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	poster.SetResponse(`{"status":[{"status":"message","code":"6553601","message":"\"Proxy0\" interface created."}]}`)
	if _, _, err := cmds.CreateProxy(context.Background(), "Proxy0", "sing-box", "127.0.0.1", 1080, true); err != nil {
		t.Fatalf("CreateProxy: %v", err)
	}
	// F577: создание голое, настройки — отдельным POST.
	create := p0(t, poster.Payloads()[0])
	if len(create) != 0 {
		t.Fatalf("создание не голое: %v", create)
	}
	iface := p0(t, poster.Payloads()[1])
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
	if _, ok := iface["up"]; ok {
		t.Errorf("up в настройках: %v (только единым путём setUp, R68-1)", iface["up"])
	}
	up := p0(t, poster.Payloads()[2])
	if len(up) != 1 || up["up"] != true {
		t.Errorf("up отдельной командой: %v", up)
	}
}

func TestProxyCommands_CreateProxy_NoUDP(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	poster.SetResponse(`{"status":[{"status":"message","code":"6553601","message":"\"Proxy1\" interface created."}]}`)
	_, _, _ = cmds.CreateProxy(context.Background(), "Proxy1", "", "127.0.0.1", 1081, false)
	p := poster.Payloads()[1].(map[string]any)
	proxy := p["interface"].(map[string]any)["Proxy1"].(map[string]any)["proxy"].(map[string]any)
	if _, ok := proxy["socks5-udp"]; ok {
		t.Errorf("socks5-udp must be absent when UDP=false")
	}
}

// p0 — тело единственного интерфейса Proxy0 в payload.
func p0(t *testing.T, payload any) map[string]any {
	t.Helper()
	iface, ok := payload.(map[string]any)["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if !ok {
		t.Fatalf("payload без Proxy0: %v", payload)
	}
	return iface
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

// R68-1: ProxyDown — `up:false` единым путём setUp (форма `down:true`
// снята; равнозначность для Proxy — проверка стенда Task 70).
func TestProxyCommands_ProxyDown_UsesUpFalse(t *testing.T) {
	cmds, poster, _ := newTestProxyCommands(t)
	_ = cmds.ProxyDown(context.Background(), confirmed(t, "Proxy0"))
	p := poster.Payloads()[0].(map[string]any)
	iface := p["interface"].(map[string]any)["Proxy0"].(map[string]any)
	if iface["up"] != false {
		t.Errorf("up: %v", iface["up"])
	}
	if _, ok := iface["down"]; ok {
		t.Errorf("down must be absent")
	}
}

func TestCreateProxy_ReturnsConfirmed(t *testing.T) {
	cmds, f, q := newOracleCommands(t, nil)
	f.ExpectCreate("Proxy0")
	c, _, err := cmds.Proxies.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, false)
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

// rejectProxySettings — NDMS, применяющий payload поэлементно (оракул), но
// отвергающий настройку `proxy` в том же ответе: на 5.01+ запись при этом
// создана, а ответ несёт и «created», и status:error (ревью F574 N1).
// refuseDrop — и снос отвергается (до оракула: запись остаётся).
type rejectProxySettings struct {
	*query.FakeNDMS
	refuseDrop bool
}

func (p rejectProxySettings) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	b, _ := json.Marshal(payload)
	if p.refuseDrop && strings.Contains(string(b), `"no":true`) {
		return nil, errors.New("NDMS refused")
	}
	resp, err := p.FakeNDMS.Post(ctx, payload)
	if err != nil || !strings.Contains(string(b), `"proxy":{`) {
		return resp, err
	}
	return json.RawMessage(`[` + string(resp) + `,{"status":[{"status":"error","code":"1","message":"upstream rejected"}]}]`), nil
}

// F577 (а): настройка отвергнута — созданная этой командой запись снесена,
// сироты нет, E == 0, фантомов нет.
func TestCreateProxy_SettingsRejected_Dropped(t *testing.T) {
	_, f, q := newOracleCommands(t, nil)
	f.ExpectCreate("Proxy0")
	p := rejectProxySettings{FakeNDMS: f}
	cmds := NewProxyCommands(p, noBusSave(NewSaveCoordinator(f, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)), q)
	_, _, err := cmds.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, true)
	if err == nil || f.Has("Proxy0") || !hasDrop(f.Posts, "Proxy0") {
		t.Fatalf("err=%v has=%v posts=%v", err, f.Has("Proxy0"), f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// F577 (б): индекс занят чужим ProxyN, которого не было в списке, — ответ
// без «created»: ErrNotCreated, единственная команда — голое создание, без
// настроек и сноса; чужой цел со своим description.
func TestCreateProxy_HiddenForeign_NoSettingsNoDrop(t *testing.T) {
	cmds, f, _ := newOracleCommands(t, nil)
	f.HideCreated(1)
	f.Add(ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "Work"})
	f.HideCreated(0)
	posts := len(f.Posts)
	_, reply, err := cmds.Proxies.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, true)
	if !errors.Is(err, ErrNotCreated) || reply.Proven() {
		t.Fatalf("reply=%v err=%v", reply, err)
	}
	if got := f.Posts[posts:]; len(got) != 1 || got[0] != `{"interface":{"Proxy0":{}}}` {
		t.Fatalf("команды: %v", got)
	}
	if !f.Has("Proxy0") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("has=%v E=%d phantoms=%d", f.Has("Proxy0"), f.E, f.Phantoms)
	}
}

// F577 fix 1 N1: созданное осталось на роутере — *LeftCreatedError с
// description записи на роутере: голая при «created», не показанная списком,
// со снесом, который отказал, — ""; настройки и снос отвергнуты — description
// из свежего списка (элементы настроек применены частично).
// R54: «created» доказан, записи в списке нет — снос, не LeftCreatedError.
// Мутация: вернуть «ErrNotSeen — оставить» → красный not listed.
func TestCreateProxy_LeftOnRouter(t *testing.T) {
	t.Run("not listed, dropped", func(t *testing.T) {
		cmds, f, q := newOracleCommands(t, nil)
		f.ExpectCreate("Proxy0")
		q.Interfaces.SetCreatedBackoff(time.Millisecond)
		f.HideCreated(100)
		_, _, err := cmds.Proxies.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, true)
		var left *LeftCreatedError
		if errors.As(err, &left) || !errors.Is(err, query.ErrNotListed) {
			t.Fatalf("err=%v left=%+v", err, left)
		}
		if !hasDrop(f.Posts, "Proxy0") || f.Has("Proxy0") || f.E != 0 || f.Phantoms != 0 {
			t.Fatalf("has=%v E=%d phantoms=%d posts=%v", f.Has("Proxy0"), f.E, f.Phantoms, f.Posts)
		}
	})
	t.Run("unlisted, drop refused", func(t *testing.T) {
		_, f, q := newOracleCommands(t, nil)
		f.ExpectCreate("Proxy0")
		q.Interfaces.SetCreatedBackoff(time.Millisecond)
		f.HideCreated(100)
		p := rejectProxySettings{FakeNDMS: f, refuseDrop: true}
		cmds := NewProxyCommands(p, noBusSave(NewSaveCoordinator(f, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)), q)
		_, _, err := cmds.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, true)
		var left *LeftCreatedError
		if !errors.As(err, &left) || left.Desc != "" || !errors.Is(err, ErrLeftOnRouter) {
			t.Fatalf("err=%v left=%+v", err, left)
		}
	})
	t.Run("settings and drop refused", func(t *testing.T) {
		_, f, q := newOracleCommands(t, nil)
		f.ExpectCreate("Proxy0")
		p := rejectProxySettings{FakeNDMS: f, refuseDrop: true}
		cmds := NewProxyCommands(p, noBusSave(NewSaveCoordinator(f, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)), q)
		_, _, err := cmds.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, true)
		var left *LeftCreatedError
		if !errors.As(err, &left) || left.Name != "Proxy0" || left.Desc != "d" || !f.Has("Proxy0") {
			t.Fatalf("err=%v left=%+v has=%v", err, left, f.Has("Proxy0"))
		}
	})
	t.Run("settings refused, dropped", func(t *testing.T) {
		_, f, q := newOracleCommands(t, nil)
		f.ExpectCreate("Proxy0")
		cmds := NewProxyCommands(rejectProxySettings{FakeNDMS: f}, noBusSave(NewSaveCoordinator(f, &fakePublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)), q)
		_, _, err := cmds.CreateProxy(context.Background(), "Proxy0", "d", "127.0.0.1", 1080, true)
		var left *LeftCreatedError
		if err == nil || errors.As(err, &left) {
			t.Fatalf("снесено — оставленного нет: err=%v", err)
		}
	})

}

// R68-1: ProxyUp/ProxyDown — единым путём setUp: no-op кредита не даёт,
// действующая команда — ровно один. Мутации: «ProxyDown сырым POST» →
// кредита disabled нет, красный; «кредит всегда» → no-op up даёт кредит,
// красный.
func TestProxyUpDown_ConfCreditOnlyWhenEffective(t *testing.T) {
	ctx := context.Background()
	cmds, _, q := newOracleCommands(t, nil, ndms.Interface{ID: "Proxy0", Type: "Proxy", ConfLayer: "running"})
	c, _, _, err := q.Interfaces.Confirm(ctx, "Proxy0")
	if err != nil {
		t.Fatal(err)
	}
	claim := func(level string) bool { return q.Interfaces.ClaimOwnConf("Proxy0", level) }
	if err := cmds.Proxies.ProxyUp(ctx, c); err != nil || claim("running") {
		t.Fatalf("no-op up: err=%v или выдан кредит", err)
	}
	if err := cmds.Proxies.ProxyDown(ctx, c); err != nil {
		t.Fatal(err)
	}
	if !claim("disabled") || claim("disabled") {
		t.Fatal("действующий down: ждали ровно один кредит disabled")
	}
}

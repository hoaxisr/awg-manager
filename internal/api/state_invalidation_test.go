package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/cdndispatcher"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/serveringress"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/vlessserver"
)

// expectInvalidation ждёт ровно ту подсказку, что заменила таймер опроса на
// фронте; без неё страница молча показывает устаревшее.
func expectInvalidation(t *testing.T, ch <-chan events.Event, res events.Resource, reason string) {
	t.Helper()
	select {
	case ev := <-ch:
		data, _ := ev.Data.(events.ResourceInvalidatedEvent)
		if ev.Type != events.EventResourceInvalidated || data.Resource != res || data.Reason != reason {
			t.Fatalf("event = %+v, want %s/%s", ev, res, reason)
		}
	case <-time.After(time.Second):
		t.Fatalf("no %s/%s event", res, reason)
	}
}

func expectNoEvent(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event on failed mutation: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestVlessServerHandler_MutationPublishesIngress(t *testing.T) {
	h := NewVlessServerHandler(vlessserver.New(t.TempDir(), nil), nil)
	bus := events.NewBus()
	h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	rec := httptest.NewRecorder()
	h.ResolveRecovery(rec, httptest.NewRequest(http.MethodGet, "/api/servers/vless/recovery/resolve", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	expectNoEvent(t, ch)

	rec = httptest.NewRecorder()
	h.ResolveRecovery(rec, httptest.NewRequest(http.MethodPost, "/api/servers/vless/recovery/resolve", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}
	expectInvalidation(t, ch, events.ResourceServersIngress, "vless-recovery-resolved")
}

func TestIngressDispatcherHandler_UpdatePublishesIngress(t *testing.T) {
	disp := cdndispatcher.New(cdndispatcher.Config{ListenAddr: "127.0.0.1:0", DefaultAction: "404"})
	h := NewIngressDispatcherHandler(serveringress.New(t.TempDir(), nil, disp, nil), disp, nil)
	bus := events.NewBus()
	h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	rec := httptest.NewRecorder()
	h.UpdateDispatcherConfig(rec, httptest.NewRequest(http.MethodPut, "/api/servers/ingress/dispatcher", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	expectNoEvent(t, ch)

	rec = httptest.NewRecorder()
	h.UpdateDispatcherConfig(rec, httptest.NewRequest(http.MethodPut, "/api/servers/ingress/dispatcher",
		strings.NewReader(`{"enabled":false,"listen_addr":"127.0.0.1:0","default_action":"404"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	expectInvalidation(t, ch, events.ResourceServersIngress, "dispatcher-config-updated")
}

func TestUplinkHandler_MutationPublishesUplink(t *testing.T) {
	store, err := uplink.OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(store, dnsTestProviders{}, nil)
	if c.WANDNS, err = uplink.OpenWANDNS(filepath.Join(t.TempDir(), "dns.json")); err != nil {
		t.Fatal(err)
	}
	h := &UplinkHandler{Controller: c}
	bus := events.NewBus()
	h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	put := func(body string) int {
		rec := httptest.NewRecorder()
		h.DNS(rec, httptest.NewRequest(http.MethodPut, "/api/uplink/dns", strings.NewReader(body)))
		return rec.Code
	}
	if code := put(`{"provider":"OTHER","revision":0,"servers":["1.1.1.1"]}`); code != http.StatusBadRequest {
		t.Fatalf("invalid provider: %d", code)
	}
	expectNoEvent(t, ch)
	if code := put(`{"provider":"ISP","revision":0,"servers":["1.1.1.1"]}`); code != http.StatusOK {
		t.Fatalf("valid: %d", code)
	}
	expectInvalidation(t, ch, events.ResourceUplink, "wan-dns-updated")
}

func TestFircHandler_ExtUpdatePublishesFirc(t *testing.T) {
	tmp := t.TempDir()
	op := firc.NewOperator(firc.Options{ConfigDir: filepath.Join(tmp, "firc")})
	store := storage.NewSettingsStore(tmp)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box" // офлайн-путь: демон не трогаем
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	bus := events.NewBus()
	h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/firc/ext", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	expectNoEvent(t, ch)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/firc/ext", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT ext: %d %s", rec.Code, rec.Body.String())
	}
	expectInvalidation(t, ch, events.ResourceFirc, "ext-updated")
}

// Правки групп/туннелей через /v1 — тоже мутации FIRC; GET и чтения-POST
// (preview) подсказок не шлют.
func TestFircHandler_V1MutationPublishesFirc(t *testing.T) {
	tmp := t.TempDir()
	op := firc.NewOperator(firc.Options{ConfigDir: filepath.Join(tmp, "firc")})
	store := storage.NewSettingsStore(tmp)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box" // офлайн-стейджинг, демон не нужен
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	bus := events.NewBus()
	h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	do := func(method, path, body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code
	}

	if code := do(http.MethodGet, "/api/firc/v1/groups", ""); code != http.StatusOK {
		t.Fatalf("GET groups: %d", code)
	}
	if code := do(http.MethodPut, "/api/firc/v1/groups", "{"); code != http.StatusBadRequest {
		t.Fatalf("bad PUT: %d", code)
	}
	expectNoEvent(t, ch)
	if code := do(http.MethodPut, "/api/firc/v1/groups", `{"groups":[]}`); code != http.StatusOK {
		t.Fatalf("PUT groups: %d", code)
	}
	expectInvalidation(t, ch, events.ResourceFirc, "v1-groups")
}

// Страница серверов читает ingressInterfaces из настроек роутера.
func TestRouterPutSettings_IngressChangePublishesServersIngress(t *testing.T) {
	svc := &mockRouterSvc{}
	h := newMockRouterHandler(svc)
	bus := events.NewBus()
	h.SetEventBus(bus)
	_, ch, unsub := bus.Subscribe()
	defer unsub()
	put := func(body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.PutSettings(rec, httptest.NewRequest(http.MethodPut, "/api/singbox/router/settings", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}
	put(`{"enabled":true,"ingressInterfaces":["managed:Wireguard3"]}`)
	expectInvalidation(t, ch, events.ResourceServersIngress, "ingress-interfaces-changed")
	put(`{"enabled":true,"ingressInterfaces":["managed:Wireguard3"]}`)
	expectNoEvent(t, ch)
}

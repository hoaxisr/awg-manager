package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/events"
)

type spyDispatcher struct {
	mu     sync.Mutex
	events []events.Event
}

func (s *spyDispatcher) Enqueue(e events.Event) {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
}

func (s *spyDispatcher) Events() []events.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]events.Event, len(s.events))
	copy(out, s.events)
	return out
}

func newTestHookHandler(disp HookDispatcher) *HookHandler {
	return &HookHandler{
		dispatcher: disp,
		log:        logging.NewScopedLogger(nil, logging.GroupSystem, logging.SubBoot),
	}
}

func TestHookHandler_HandleNDMS_LayerChanged(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	body := strings.NewReader("type=iflayerchanged&id=Wireguard0&layer=conf&level=running")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d — body: %s", w.Code, readBody(w))
	}
	got := disp.Events()
	if len(got) != 1 {
		t.Fatalf("events: want 1, got %d", len(got))
	}
	if got[0].Type != events.EventIfLayerChanged || got[0].ID != "Wireguard0" ||
		got[0].Layer != "conf" || got[0].Level != "running" {
		t.Errorf("event: %#v", got[0])
	}
}

func TestHookHandler_HandleNDMS_UnknownType(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	body := strings.NewReader("type=bogus&id=x")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown type: want 400, got %d", w.Code)
	}
	if len(disp.Events()) != 0 {
		t.Errorf("events on bad request: want 0, got %d", len(disp.Events()))
	}
}

func TestHookHandler_HandleNDMS_IfCreated(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	body := strings.NewReader("type=ifcreated&id=Wireguard1&system_name=nwg1")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body: %s", w.Code, readBody(w))
	}
	got := disp.Events()
	if len(got) != 1 || got[0].Type != events.EventIfCreated {
		t.Errorf("event: %#v", got)
	}
}

func TestHookHandler_TunnelRefresher_FiresOnCreateAndDestroy(t *testing.T) {
	// Regression: user reported a system-tunnel card staying visible
	// after NDMS fired ifdestroyed. The hook handler must call the
	// tunnel-snapshot refresher so the UI re-renders.
	var calls int32
	refreshed := make(chan struct{}, 4)

	h := newTestHookHandler(&spyDispatcher{})
	h.SetTunnelRefresher(func(ctx context.Context) {
		atomic.AddInt32(&calls, 1)
		refreshed <- struct{}{}
	})

	for _, typ := range []string{"ifcreated", "ifdestroyed"} {
		body := strings.NewReader("type=" + typ + "&id=Wireguard1")
		req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.HandleNDMS(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", typ, w.Code)
		}
	}

	// Refresher runs async in a goroutine; wait for both firings.
	for i := 0; i < 2; i++ {
		select {
		case <-refreshed:
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("refresher call %d did not fire within 500ms", i+1)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("refresher calls: want 2, got %d", got)
	}
}

func TestHookHandler_TunnelRefresher_NotFiredOnLayerChange(t *testing.T) {
	var calls int32
	h := newTestHookHandler(&spyDispatcher{})
	h.SetTunnelRefresher(func(ctx context.Context) {
		atomic.AddInt32(&calls, 1)
	})

	body := strings.NewReader("type=iflayerchanged&id=Wireguard0&layer=conf&level=running")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.HandleNDMS(w, req)

	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("refresher must only fire on create/destroy, fired %d times on layerchanged", got)
	}
}

func TestHookHandler_HandleNDMS_MethodNotAllowed(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)

	req := httptest.NewRequest(http.MethodGet, "/api/hook/ndms", nil)
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: want 405, got %d", w.Code)
	}
}

func TestHookHandler_HandleNDMS_NilDispatcher_NoPanic(t *testing.T) {
	h := newTestHookHandler(nil) // nil dispatcher

	body := strings.NewReader("type=ifcreated&id=X")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	// Should not panic even if dispatcher is nil.
	h.HandleNDMS(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("nil dispatcher: want 200 (silent drop), got %d", w.Code)
	}
}

func readBody(w *httptest.ResponseRecorder) string {
	b, _ := io.ReadAll(w.Result().Body)
	return string(b)
}

// fakeWANModel records SetUp calls for the ipv4-layer hook path.
type fakeWANModel struct {
	mu    sync.Mutex
	calls []fakeWANSetUp
}

type fakeWANSetUp struct {
	Name string
	Up   bool
}

func (f *fakeWANModel) SetUp(name string, up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeWANSetUp{Name: name, Up: up})
}

func (f *fakeWANModel) Calls() []fakeWANSetUp {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeWANSetUp, len(f.calls))
	copy(out, f.calls)
	return out
}

func TestHookHandler_HandleNDMS_IPv4Up_UpdatesWANModel(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	body := strings.NewReader("type=iflayerchanged&id=PPPoE0&system_name=ppp0&layer=ipv4&level=running")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d — body: %s", w.Code, readBody(w))
	}

	// Dispatcher still sees the event (cache invalidation is independent).
	if got := disp.Events(); len(got) != 1 || got[0].Type != events.EventIfLayerChanged {
		t.Errorf("dispatcher: %#v", got)
	}

	calls := wm.Calls()
	if len(calls) != 1 {
		t.Fatalf("wan SetUp calls: want 1, got %d (%#v)", len(calls), calls)
	}
	if calls[0].Name != "ppp0" || !calls[0].Up {
		t.Errorf("wan SetUp: want (ppp0, true), got %#v", calls[0])
	}
}

func TestHookHandler_HandleNDMS_IPv4Down_EmitsWANDown(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	body := strings.NewReader("type=iflayerchanged&id=PPPoE0&system_name=ppp0&layer=ipv4&level=disabled")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d — body: %s", w.Code, readBody(w))
	}

	calls := wm.Calls()
	if len(calls) != 1 {
		t.Fatalf("wan SetUp calls: want 1, got %d", len(calls))
	}
	if calls[0].Name != "ppp0" || calls[0].Up {
		t.Errorf("wan SetUp: want (ppp0, false), got %#v", calls[0])
	}
}

func TestHookHandler_HandleNDMS_IPv4_VPNInterface_Skipped(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	// nwg0 matches the IsNonISPInterface filter — must NOT touch WAN model.
	body := strings.NewReader("type=iflayerchanged&id=Wireguard0&system_name=nwg0&layer=ipv4&level=running")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}

	if calls := wm.Calls(); len(calls) != 0 {
		t.Errorf("wan SetUp: want 0 (VPN filtered), got %#v", calls)
	}
	// Dispatcher still sees the event — cache invalidation is independent of filter.
	if got := disp.Events(); len(got) != 1 {
		t.Errorf("dispatcher: want 1, got %d", len(got))
	}
}

func TestHookHandler_HandleNDMS_IPv4_EmptySystemName_Skipped(t *testing.T) {
	disp := &spyDispatcher{}
	h := newTestHookHandler(disp)
	wm := &fakeWANModel{}
	h.SetWANModel(wm)

	// No system_name → no kernel name → cannot update WAN model.
	body := strings.NewReader("type=iflayerchanged&id=PPPoE0&layer=ipv4&level=running")
	req := httptest.NewRequest(http.MethodPost, "/api/hook/ndms", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.HandleNDMS(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	if calls := wm.Calls(); len(calls) != 0 {
		t.Errorf("wan SetUp: want 0 (empty system_name), got %#v", calls)
	}
}

package api

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
)

func newToggleHarness(t *testing.T, cur *service.TunnelWithStatus) (*ControlHandler, *stubTunnelSvc, *busProbe) {
	t.Helper()
	stub := &stubTunnelSvc{getFn: func(_ context.Context, id string) (*service.TunnelWithStatus, error) {
		if cur == nil || cur.ID != id {
			return nil, errors.New("not found")
		}
		return cur, nil
	}}
	p := newBusProbe(t)
	th, store := newTunnelsUpdateHarness(t, stub)
	th.SetEventBus(p.bus())
	h := NewControlHandler(stub, store, nil)
	h.SetTunnelsHandler(th)
	h.SetEventBus(p.bus())
	return h, stub, p
}

// ToggleEnabled: новое значение = инверсия текущего, уходит в SetEnabled с тем же id;
// ответ несёт его же; успех публикует список туннелей и routing-tunnels.
func TestControlHandler_ToggleEnabled_InvertsAndPublishes(t *testing.T) {
	for _, cur := range []bool{true, false} {
		h, stub, p := newToggleHarness(t, &service.TunnelWithStatus{ID: "awg7", Name: "NL", Enabled: cur})
		rr := perform(h.ToggleEnabled, http.MethodPost, "/tunnels/toggle-enabled?id=awg7", "")
		if rr.Code != 200 {
			t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
		}
		if want := []toggleCall{{"awg7", !cur}}; !reflect.DeepEqual(stub.setEnabledCalls, want) {
			t.Fatalf("SetEnabled calls = %v, want %v", stub.setEnabledCalls, want)
		}
		data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
		if data["id"] != "awg7" || data["enabled"] != !cur {
			t.Fatalf("тело = %v, want id=awg7 enabled=%v", data, !cur)
		}
		want := []string{"routing.tunnels/state-changed", "tunnels/list-changed"}
		if got := p.invalidated(); !reflect.DeepEqual(got, want) {
			t.Fatalf("публикации = %v, want %v", got, want)
		}
	}
}

func TestControlHandler_ToggleEnabled_ServiceFailureIsNot200AndSilent(t *testing.T) {
	h, stub, p := newToggleHarness(t, &service.TunnelWithStatus{ID: "awg7", Enabled: false})
	stub.setEnabledErr = errors.New("ndms down")
	rr := perform(h.ToggleEnabled, http.MethodPost, "/tunnels/toggle-enabled?id=awg7", "")
	if rr.Code == 200 || decodeJSONBody(t, rr)["code"] != "TOGGLE_FAILED" {
		t.Fatalf("ожидался TOGGLE_FAILED, got %d %s", rr.Code, rr.Body.String())
	}
	if got := p.invalidated(); len(got) != 0 {
		t.Fatalf("на отказе публикаций быть не должно: %v", got)
	}
	// Неизвестный id — до SetEnabled не доходим.
	h2, stub2, p2 := newToggleHarness(t, nil)
	if rr := perform(h2.ToggleEnabled, http.MethodPost, "/tunnels/toggle-enabled?id=awg9", ""); decodeJSONBody(t, rr)["code"] != "NOT_FOUND" {
		t.Fatalf("ожидался NOT_FOUND: %s", rr.Body.String())
	}
	if len(stub2.setEnabledCalls) != 0 {
		t.Fatalf("SetEnabled не должен вызываться: %v", stub2.setEnabledCalls)
	}
	if len(p2.invalidated()) != 0 {
		t.Fatalf("на NOT_FOUND публикаций быть не должно: %v", p2.invalidated())
	}
}

func TestControlHandler_ToggleDefaultRoute_InvertsAndPublishes(t *testing.T) {
	for _, cur := range []bool{true, false} {
		h, stub, p := newToggleHarness(t, &service.TunnelWithStatus{ID: "awg7", Name: "NL", DefaultRoute: cur})
		rr := perform(h.ToggleDefaultRoute, http.MethodPost, "/tunnels/toggle-default-route?id=awg7", "")
		if rr.Code != 200 {
			t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
		}
		if want := []toggleCall{{"awg7", !cur}}; !reflect.DeepEqual(stub.setDefaultRouteCalls, want) {
			t.Fatalf("SetDefaultRoute calls = %v, want %v", stub.setDefaultRouteCalls, want)
		}
		data, _ := decodeJSONBody(t, rr)["data"].(map[string]any)
		if data["id"] != "awg7" || data["defaultRoute"] != !cur {
			t.Fatalf("тело = %v", data)
		}
		want := []string{"routing.tunnels/state-changed", "tunnels/list-changed"}
		if got := p.invalidated(); !reflect.DeepEqual(got, want) {
			t.Fatalf("публикации = %v, want %v", got, want)
		}
		if len(stub.setEnabledCalls) != 0 {
			t.Fatalf("ToggleDefaultRoute не должен трогать Enabled: %v", stub.setEnabledCalls)
		}
	}
}

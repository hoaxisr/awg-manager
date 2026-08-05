package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newTestFakeIPConfigHandler wires a real *router.ServiceImpl over a real
// *orchestrator.Orchestrator (both SlotRouting and SlotFakeIP registered),
// with a SettingsStore that has FakeIPState provisioned so writes succeed.
func newTestFakeIPConfigHandler(t *testing.T) *SingboxFakeIPConfigHandler {
	t.Helper()
	dir := t.TempDir()

	orch := orchestrator.New(dir, nil)
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouting, Filename: "21-routing.json"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotFakeIP, Filename: "20-fakeip.json"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	if err := orch.SetEnabled(orchestrator.SlotFakeIP, true); err != nil {
		t.Fatal(err)
	}

	settingsStore := storage.NewSettingsStore(dir)
	if _, err := settingsStore.Load(); err != nil {
		t.Fatal(err)
	}
	if err := settingsStore.SetFakeIPState(&storage.FakeIPState{Provisioned: true, Index: 0}); err != nil {
		t.Fatal(err)
	}

	params := router.DefaultFakeIPTunParams()
	params.CachePath = dir + "/fakeip-test.db"

	svc := router.NewService(router.Deps{
		Settings:       settingsStore,
		Orch:           orch,
		WANIPCollector: &noopWANIPCollector{},
		FakeIPTun:      params,
	})
	return NewSingboxFakeIPConfigHandler(svc, nil)
}

// TestFakeIPConfigHandler_ListDNSServers_Returns200Array verifies that
// GET .../dns/servers/list returns 200 and a JSON array (never null).
func TestFakeIPConfigHandler_ListDNSServers_Returns200Array(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/singbox/fakeip/config/dns/servers/list", nil)
	rr := httptest.NewRecorder()
	fh.ListDNSServers(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("ListDNSServers: want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	var env struct {
		Success bool              `json:"success"`
		Data    []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rr.Body.String())
	}
	if !env.Success {
		t.Errorf("expected success=true")
	}
	// data must be a JSON array (never null / absent)
	if env.Data == nil {
		t.Errorf("data is null, expected []")
	}
}

// seedFakeIPConfigOverlay триггерит fakeipWithConfig один раз, чтобы в
// режимном слоте появились движковые биты (серверы fakeip/real, hijack-dns и
// т.д.) до пользовательских правок, которые на них ссылаются.
//
// Правки правил и наборов сюда больше не годятся: они уехали в общий слот и
// оверлея режимного слота не касаются. Берём DNS-глобалы — они остались
// режимными.
func seedFakeIPConfigOverlay(t *testing.T, fh *SingboxFakeIPConfigHandler) {
	t.Helper()
	body := `{"strategy":"prefer_ipv4"}`
	req := httptest.NewRequest(http.MethodPut, "/api/singbox/fakeip/config/dns/globals",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.PutDNSGlobals(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("seedFakeIPConfigOverlay PutDNSGlobals: want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestFakeIPConfigHandler_AddDNSRule_ThenList verifies that
// POST .../dns/rules/add adds a rule visible via a subsequent list call.
// It seeds the overlay first so the "fakeip" DNS server exists.
func TestFakeIPConfigHandler_AddDNSRule_ThenList(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)
	seedFakeIPConfigOverlay(t, fh)

	body := `{"action":"route","server":"fakeip","query_type":["A"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/dns/rules/add",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.AddDNSRule(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("AddDNSRule: want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	// List and verify the rule is present.
	req2 := httptest.NewRequest(http.MethodGet, "/api/singbox/fakeip/config/dns/rules/list", nil)
	rr2 := httptest.NewRecorder()
	fh.ListDNSRules(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("ListDNSRules: want 200, got %d (body: %s)", rr2.Code, rr2.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Data    []struct {
			Action    string   `json:"action"`
			Server    string   `json:"server"`
			QueryType []string `json:"query_type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rr2.Body.String())
	}
	found := false
	for _, r := range env.Data {
		if r.Action == "route" && r.Server == "fakeip" && len(r.QueryType) == 1 && r.QueryType[0] == "A" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("AddDNSRule: added rule not found in ListDNSRules; rules: %+v", env.Data)
	}
}

// TestFakeIPConfigHandler_LockedFieldDelete_Returns4xx verifies that
// attempting to delete the engine-locked "real" DNS server maps to 4xx (not 500).
func TestFakeIPConfigHandler_LockedFieldDelete_Returns4xx(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)
	seedFakeIPConfigOverlay(t, fh)

	// Now try to delete "real" with force=true — overlay is established, guard fires.
	delBody := `{"tag":"real","force":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/dns/servers/delete",
		strings.NewReader(delBody))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.DeleteDNSServer(rr, req)

	if rr.Code == http.StatusInternalServerError {
		t.Errorf("DeleteDNSServer locked field: got 500 (want 4xx); body: %s", rr.Body.String())
	}
	if rr.Code < 400 || rr.Code >= 500 {
		t.Errorf("DeleteDNSServer locked field: want 4xx, got %d; body: %s", rr.Code, rr.Body.String())
	}
}

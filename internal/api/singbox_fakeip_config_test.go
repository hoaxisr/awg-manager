package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newTestFakeIPConfigHandler wires a real *router.ServiceImpl over a real
// *orchestrator.Orchestrator (both SlotRouter and SlotFakeIP registered),
// with a SettingsStore that has the OpkgTun ownership record provisioned so writes succeed.
func newTestFakeIPConfigHandler(t *testing.T) *SingboxFakeIPConfigHandler {
	t.Helper()
	dir := t.TempDir()

	orch := orchestrator.New(dir, nil)
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotRouter, Filename: "20-router.json"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Register(orchestrator.SlotMeta{Slot: orchestrator.SlotFakeIP, Filename: "21-fakeip.json"}); err != nil {
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
	if err := settingsStore.SetOpkgTunState(&storage.OpkgTunState{Mode: storage.OpkgTunModeFakeIP, Provisioned: true, Index: 0}); err != nil {
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

// seedFakeIPConfigOverlay does a no-op route-rule add+delete to trigger
// fakeipWithConfig once so the engine-locked overlay bits (fakeip/real DNS
// servers, hijack-dns route rule, etc.) are established in the slot before
// any user mutations reference them.
func seedFakeIPConfigOverlay(t *testing.T, fh *SingboxFakeIPConfigHandler) {
	t.Helper()
	// Add a route rule (does not reference DNS servers, so no chicken-and-egg
	// with the fakeip server that only exists after the overlay runs).
	body := `{"action":"route","outbound":"direct","domain_suffix":[".test.invalid"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rules/add",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.AddRule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("seedFakeIPConfigOverlay AddRule: want 200, got %d (body: %s)", rr.Code, rr.Body.String())
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

// TestFakeIPConfigHandler_BulkSetRuleOutbound_200 exercises the endpoint
// against a real ServiceImpl: seeds one route rule, bulk-sets its outbound,
// and verifies both the {"updated":1} response and the actual mutation.
func TestFakeIPConfigHandler_BulkSetRuleOutbound_200(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)

	// The fakeip-tun slot seeds an engine-managed hijack-dns rule at index 0
	// (not a route rule — bulk-outbound rejects it), so the newly added rule
	// lands at index 1.
	addBody := `{"action":"route","outbound":"old","domain_suffix":["example.com"]}`
	addReq := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rules/add", strings.NewReader(addBody))
	addReq.Header.Set("Content-Type", "application/json")
	addRR := httptest.NewRecorder()
	fh.AddRule(addRR, addReq)
	if addRR.Code != http.StatusOK {
		t.Fatalf("seed AddRule: want 200, got %d (body: %s)", addRR.Code, addRR.Body.String())
	}
	rules, err := fh.svc.FakeIPListRules(context.Background())
	if err != nil {
		t.Fatalf("FakeIPListRules: %v", err)
	}
	seededIndex := len(rules) - 1

	body := fmt.Sprintf(`{"indices":[%d],"outbound":"direct"}`, seededIndex)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rules/bulk-outbound", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.BulkSetRuleOutbound(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Updated int `json:"updated"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rr.Body.String())
	}
	if !env.Success || env.Data.Updated != 1 {
		t.Fatalf("want success=true updated=1, got %+v", env)
	}
}

// TestFakeIPConfigHandler_BulkSetRuleOutbound_EmptyIndices_Returns400 verifies
// the service's empty-selection guard (ErrBulkEmptyIndices) maps to 400.
func TestFakeIPConfigHandler_BulkSetRuleOutbound_EmptyIndices_Returns400(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)

	body := `{"indices":[],"outbound":"direct"}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rules/bulk-outbound", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.BulkSetRuleOutbound(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestFakeIPConfigHandler_BulkSetRuleOutbound_DuplicateIndex_Returns400
// verifies the service's non-empty-but-invalid selection guard
// (ErrBulkInvalidSelection) maps to 400, not 500.
func TestFakeIPConfigHandler_BulkSetRuleOutbound_DuplicateIndex_Returns400(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)

	addBody := `{"action":"route","outbound":"old","domain_suffix":["example.com"]}`
	addReq := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rules/add", strings.NewReader(addBody))
	addReq.Header.Set("Content-Type", "application/json")
	addRR := httptest.NewRecorder()
	fh.AddRule(addRR, addReq)
	if addRR.Code != http.StatusOK {
		t.Fatalf("seed AddRule: want 200, got %d (body: %s)", addRR.Code, addRR.Body.String())
	}
	rules, err := fh.svc.FakeIPListRules(context.Background())
	if err != nil {
		t.Fatalf("FakeIPListRules: %v", err)
	}
	seededIndex := len(rules) - 1

	body := fmt.Sprintf(`{"indices":[%d,%d],"outbound":"direct"}`, seededIndex, seededIndex)
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rules/bulk-outbound", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.BulkSetRuleOutbound(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	var env struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rr.Body.String())
	}
	if env.Code != "BULK_INVALID_SELECTION" {
		t.Fatalf("want code BULK_INVALID_SELECTION, got %q", env.Code)
	}
}

// TestFakeIPConfigHandler_BulkSetRuleSetDetour_200 exercises the endpoint
// against a real ServiceImpl: seeds one remote ruleset, bulk-sets its detour,
// and verifies both the {"updated":1} response and the actual mutation.
func TestFakeIPConfigHandler_BulkSetRuleSetDetour_200(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)

	addBody := `{"tag":"geosite-test","type":"remote","url":"https://cdn.example.com/geosite-test.srs","download_detour":"old"}`
	addReq := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rulesets/add", strings.NewReader(addBody))
	addReq.Header.Set("Content-Type", "application/json")
	addRR := httptest.NewRecorder()
	fh.AddRuleSet(addRR, addReq)
	if addRR.Code != http.StatusOK {
		t.Fatalf("seed AddRuleSet: want 200, got %d (body: %s)", addRR.Code, addRR.Body.String())
	}

	body := `{"tags":["geosite-test"],"downloadDetour":"direct"}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rulesets/bulk-detour", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.BulkSetRuleSetDetour(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Updated int `json:"updated"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rr.Body.String())
	}
	if !env.Success || env.Data.Updated != 1 {
		t.Fatalf("want success=true updated=1, got %+v", env)
	}
}

// TestFakeIPConfigHandler_BulkSetRuleSetDetour_EmptyTags_Returns400 verifies
// the service's empty-selection guard (ErrBulkEmptyTags) maps to 400.
func TestFakeIPConfigHandler_BulkSetRuleSetDetour_EmptyTags_Returns400(t *testing.T) {
	fh := newTestFakeIPConfigHandler(t)

	body := `{"tags":[],"downloadDetour":"direct"}`
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/fakeip/config/rulesets/bulk-detour", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	fh.BulkSetRuleSetDetour(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

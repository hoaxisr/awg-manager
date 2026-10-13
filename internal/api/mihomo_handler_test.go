package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomo/installer"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeMihomoEngine struct {
	running     bool
	pid         int
	reloadCalls int
	reloadErr   error
	lastError   string
	configDir   string
}

func (f *fakeMihomoEngine) Reload() error                           { f.reloadCalls++; return f.reloadErr }
func (f *fakeMihomoEngine) IsRunning() (bool, int)                  { return f.running, f.pid }
func (f *fakeMihomoEngine) Start() error                            { return nil }
func (f *fakeMihomoEngine) Stop() error                             { return nil }
func (f *fakeMihomoEngine) ClearManualStop() error                  { return nil }
func (f *fakeMihomoEngine) ValidateConfigDir(context.Context) error { return nil }
func (f *fakeMihomoEngine) ConfigDir() string {
	if f.configDir != "" {
		return f.configDir
	}
	return "/tmp/mihomo"
}
func (f *fakeMihomoEngine) Binary() string                       { return "/usr/bin/mihomo" }
func (f *fakeMihomoEngine) LastError() string                    { return f.lastError }
func (f *fakeMihomoEngine) CrashStats() (int, string, time.Time) { return 0, "", time.Time{} }

func TestMihomoStatusReportsSelectedAndActive(t *testing.T) {
	store := newMihomoHandlerSettings(t, "mihomo", true)
	op := &fakeMihomoEngine{running: true, pid: 4321}
	h := NewMihomoHandler(op)
	h.SetSettingsStore(store)

	rec := httptest.NewRecorder()
	h.HandleStatus(rec, httptest.NewRequest(http.MethodGet, "/api/mihomo/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	var envelope struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !envelope.Success {
		t.Fatalf("success = false; response=%s", rec.Body.String())
	}
	got := envelope.Data
	for _, key := range []string{"running", "selected", "enabled", "active"} {
		if got[key] != true {
			t.Fatalf("status[%q] = %v, want true; status=%v", key, got[key], got)
		}
	}
	if got["pid"] != float64(4321) {
		t.Fatalf("status pid = %v, want 4321", got["pid"])
	}
}

func TestMihomoReloadUsesPreparedReload(t *testing.T) {
	store := newMihomoHandlerSettings(t, "mihomo", true)
	op := &fakeMihomoEngine{}
	h := NewMihomoHandler(op)
	h.SetSettingsStore(store)
	preparedCalls := 0
	h.SetReloadFunc(func() error {
		preparedCalls++
		return nil
	})

	rec := httptest.NewRecorder()
	h.handleReload(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/reload", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if preparedCalls != 1 || op.reloadCalls != 0 {
		t.Fatalf("reload calls: prepared=%d direct=%d, want 1/0", preparedCalls, op.reloadCalls)
	}
}

func TestMihomoReloadRejectsInactiveEngine(t *testing.T) {
	store := newMihomoHandlerSettings(t, "sing-box", true)
	op := &fakeMihomoEngine{}
	h := NewMihomoHandler(op)
	h.SetSettingsStore(store)
	called := false
	h.SetReloadFunc(func() error { called = true; return errors.New("must not run") })

	rec := httptest.NewRecorder()
	h.handleReload(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/reload", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status code = %d, want 409", rec.Code)
	}
	if called {
		t.Fatal("prepared reload called for inactive Mihomo")
	}
}

func TestMihomoRoutesAreGuarded(t *testing.T) {
	h := NewMihomoHandler(&fakeMihomoEngine{})
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "guarded", http.StatusUnauthorized)
		}
	})

	for _, path := range []string{"/api/mihomo/status", "/api/mihomo/install", "/api/mihomo/update", "/api/mihomo/uninstall", "/api/mihomo/config", "/api/mihomo/reload", "/api/mihomo/clash", "/api/mihomo/clash/proxies"} {
		rec := httptest.NewRecorder()
		method := http.MethodGet
		if path == "/api/mihomo/reload" || path == "/api/mihomo/install" || path == "/api/mihomo/update" || path == "/api/mihomo/uninstall" {
			method = http.MethodPost
		}
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d, want 401", path, rec.Code)
		}
	}
}

func TestMihomoConfigReturnsGeneratedYAML(t *testing.T) {
	dir := t.TempDir()
	want := []byte("mode: rule\nrules:\n  - MATCH,DIRECT\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), want, 0600); err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(&fakeMihomoEngine{configDir: dir})
	rec := httptest.NewRecorder()
	h.handleConfig(rec, httptest.NewRequest(http.MethodGet, "/api/mihomo/config", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "MATCH,DIRECT") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func newMihomoHandlerSettings(t *testing.T, engine string, enabled bool) *storage.SettingsStore {
	t.Helper()
	store := storage.NewSettingsStore(t.TempDir())
	err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = engine
		s.SingboxRouter.Enabled = enabled
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	return store
}

func TestMihomoInstallWithoutInstallerReturnsNotConfigured(t *testing.T) {
	h := NewMihomoHandler(&fakeMihomoEngine{})
	rec := httptest.NewRecorder()
	h.handleInstall(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/install", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestMihomoUninstallStopsProcessAndRemovesBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "mihomo")
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	inst := installer.New(binPath, "test-arch", installer.BinarySpec{}, nil)
	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetInstaller(inst)

	rec := httptest.NewRecorder()
	h.handleUninstall(rec, httptest.NewRequest(http.MethodPost, "/api/mihomo/uninstall", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(binPath); !os.IsNotExist(err) {
		t.Fatalf("binary still exists after uninstall")
	}
}

func TestMihomoUnsupportedRulesEndpoints(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "native.json")
	initialJSON := `{
		"version": 4,
		"rules": [
			{"id": "r-valid", "type": "DOMAIN", "payload": "valid.com", "outbound": "DIRECT", "enabled": true},
			{"id": "r-sub1", "type": "SUB-RULE", "payload": "(DOMAIN,test1.com)", "outbound": "DIRECT", "enabled": true},
			{"id": "r-sub2", "type": "SUB-RULE", "payload": "(DOMAIN,test2.com)", "outbound": "DIRECT", "enabled": false}
		]
	}`
	if err := os.WriteFile(storePath, []byte(initialJSON), 0600); err != nil {
		t.Fatal(err)
	}

	nativeStore, err := mihomonative.NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(nativeStore)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	// 1. GET /api/mihomo/native/rules/unsupported
	reqGet := httptest.NewRequest(http.MethodGet, "/api/mihomo/native/rules/unsupported", nil)
	recGet := httptest.NewRecorder()
	mux.ServeHTTP(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Fatalf("GET /api/mihomo/native/rules/unsupported status = %d: %s", recGet.Code, recGet.Body.String())
	}
	var getResp struct {
		Success bool `json:"success"`
		Data    struct {
			Items    []mihomonative.Rule `json:"items"`
			Revision string              `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recGet.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal get response: %v", err)
	}
	if len(getResp.Data.Items) != 2 {
		t.Fatalf("expected 2 unsupported rules, got %#v", getResp.Data.Items)
	}
	if !strings.HasPrefix(getResp.Data.Revision, "v1:") {
		t.Fatalf("expected revision starting with v1:, got %s", getResp.Data.Revision)
	}
	validRevision := getResp.Data.Revision

	// 2. Reject empty IDs array -> 400 INVALID_REQUEST
	emptyIDsBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{},
		"revision": validRevision,
	})
	reqEmpty := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(emptyIDsBody))
	recEmpty := httptest.NewRecorder()
	mux.ServeHTTP(recEmpty, reqEmpty)
	if recEmpty.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty IDs, got %d: %s", recEmpty.Code, recEmpty.Body.String())
	}
	if !strings.Contains(recEmpty.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST, got %s", recEmpty.Body.String())
	}

	// 3. Reject missing IDs field / null -> 400 INVALID_REQUEST
	nullIDsBody, _ := json.Marshal(map[string]interface{}{
		"revision": validRevision,
	})
	reqNull := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(nullIDsBody))
	recNull := httptest.NewRecorder()
	mux.ServeHTTP(recNull, reqNull)
	if recNull.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing IDs, got %d: %s", recNull.Code, recNull.Body.String())
	}
	if !strings.Contains(recNull.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST, got %s", recNull.Body.String())
	}

	// 4. Reject empty string in IDs array -> 400 INVALID_REQUEST
	emptyStringIDBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{"r-sub1", ""},
		"revision": validRevision,
	})
	reqEmptyString := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(emptyStringIDBody))
	recEmptyString := httptest.NewRecorder()
	mux.ServeHTTP(recEmptyString, reqEmptyString)
	if recEmptyString.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty string ID, got %d: %s", recEmptyString.Code, recEmptyString.Body.String())
	}
	if !strings.Contains(recEmptyString.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("expected INVALID_REQUEST, got %s", recEmptyString.Body.String())
	}

	// 5. Reject duplicate IDs in selection -> 400 SELECTION_MISMATCH
	dupIDsBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{"r-sub1", "r-sub1"},
		"revision": validRevision,
	})
	reqDup := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(dupIDsBody))
	recDup := httptest.NewRecorder()
	mux.ServeHTTP(recDup, reqDup)
	if recDup.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate IDs, got %d: %s", recDup.Code, recDup.Body.String())
	}
	if !strings.Contains(recDup.Body.String(), "SELECTION_MISMATCH") {
		t.Fatalf("expected SELECTION_MISMATCH, got %s", recDup.Body.String())
	}

	// 6. Reject stale revision with 409 MIHOMO_RULES_STALE
	staleBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{"r-sub1", "r-sub2"},
		"revision": "v1:stale-rev",
	})
	reqStale := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(staleBody))
	recStale := httptest.NewRecorder()
	mux.ServeHTTP(recStale, reqStale)
	if recStale.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for stale revision, got %d: %s", recStale.Code, recStale.Body.String())
	}
	if !strings.Contains(recStale.Body.String(), "MIHOMO_RULES_STALE") {
		t.Fatalf("expected error code MIHOMO_RULES_STALE, got %s", recStale.Body.String())
	}

	// 7. Reject partial IDs (subset) with 400 SELECTION_MISMATCH
	subsetBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{"r-sub1"},
		"revision": validRevision,
	})
	reqSubset := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(subsetBody))
	recSubset := httptest.NewRecorder()
	mux.ServeHTTP(recSubset, reqSubset)
	if recSubset.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for subset mismatch, got %d: %s", recSubset.Code, recSubset.Body.String())
	}
	if !strings.Contains(recSubset.Body.String(), "SELECTION_MISMATCH") {
		t.Fatalf("expected error code SELECTION_MISMATCH, got %s", recSubset.Body.String())
	}

	// 8. Reject superset / unknown IDs with 400 SELECTION_MISMATCH
	supersetBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{"r-sub1", "r-sub2", "unknown-id"},
		"revision": validRevision,
	})
	reqSuperset := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(supersetBody))
	recSuperset := httptest.NewRecorder()
	mux.ServeHTTP(recSuperset, reqSuperset)
	if recSuperset.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for superset mismatch, got %d: %s", recSuperset.Code, recSuperset.Body.String())
	}
	if !strings.Contains(recSuperset.Body.String(), "SELECTION_MISMATCH") {
		t.Fatalf("expected error code SELECTION_MISMATCH, got %s", recSuperset.Body.String())
	}

	// 9. Successful POST /api/mihomo/native/rules/unsupported/delete
	validBody, _ := json.Marshal(map[string]interface{}{
		"ids":      []string{"r-sub1", "r-sub2"},
		"revision": validRevision,
	})
	reqDel := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules/unsupported/delete?apply=false", bytes.NewReader(validBody))
	recDel := httptest.NewRecorder()
	mux.ServeHTTP(recDel, reqDel)

	if recDel.Code != http.StatusOK {
		t.Fatalf("POST /api/mihomo/native/rules/unsupported/delete status = %d: %s", recDel.Code, recDel.Body.String())
	}
	var delResp struct {
		Success bool `json:"success"`
		Data    struct {
			Deleted      bool `json:"deleted"`
			DeletedCount int  `json:"deletedCount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recDel.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("unmarshal del response: %v", err)
	}
	if !delResp.Data.Deleted || delResp.Data.DeletedCount != 2 {
		t.Fatalf("expected deleted=true, deletedCount=2, got %#v", delResp.Data)
	}

	// 10. Verify GET returns 0 items now
	recGet2 := httptest.NewRecorder()
	mux.ServeHTTP(recGet2, reqGet)
	if err := json.Unmarshal(recGet2.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("unmarshal get2 response: %v", err)
	}
	if len(getResp.Data.Items) != 0 {
		t.Fatalf("expected 0 items after deletion, got %d", len(getResp.Data.Items))
	}

	// 11. Verify disk persistence: re-instantiate store from disk
	reloadedStore, err := mihomonative.NewStore(storePath)
	if err != nil {
		t.Fatalf("failed to reload store from disk: %v", err)
	}
	persistedRules := reloadedStore.ListRules()
	if len(persistedRules) != 1 || persistedRules[0].ID != "r-valid" {
		t.Fatalf("expected 1 remaining valid rule 'r-valid' on disk, got %#v", persistedRules)
	}
	if len(reloadedStore.UnsupportedRules()) != 0 {
		t.Fatalf("expected 0 unsupported rules in reloaded store, got %d", len(reloadedStore.UnsupportedRules()))
	}
}

func TestMihomoRulesCreateUpdateEndpoints(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "native.json")
	nativeStore, err := mihomonative.NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	h := NewMihomoHandler(&fakeMihomoEngine{})
	h.SetNativeStore(nativeStore)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	// 1. POST /api/mihomo/native/rules with body ID -> 400 ID_NOT_ALLOWED
	withIDBody, _ := json.Marshal(map[string]interface{}{
		"id":       "custom-id",
		"type":     "DOMAIN",
		"payload":  "example.com",
		"outbound": "DIRECT",
	})
	reqPostID := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules?apply=false", bytes.NewReader(withIDBody))
	recPostID := httptest.NewRecorder()
	mux.ServeHTTP(recPostID, reqPostID)
	if recPostID.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when creating rule with ID, got %d: %s", recPostID.Code, recPostID.Body.String())
	}
	if !strings.Contains(recPostID.Body.String(), "ID_NOT_ALLOWED") {
		t.Fatalf("expected error code ID_NOT_ALLOWED, got %s", recPostID.Body.String())
	}

	// 2. POST /api/mihomo/native/rules without ID -> 200 OK, default enabled=true
	validCreateBody, _ := json.Marshal(map[string]interface{}{
		"type":     "DOMAIN",
		"payload":  "example.com",
		"outbound": "DIRECT",
	})
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules?apply=false", bytes.NewReader(validCreateBody))
	recCreate := httptest.NewRecorder()
	mux.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusOK {
		t.Fatalf("POST /api/mihomo/native/rules status = %d: %s", recCreate.Code, recCreate.Body.String())
	}
	var createResp struct {
		Success bool                       `json:"success"`
		Data    NativeRuleMutationResponse `json:"data"`
	}
	if err := json.Unmarshal(recCreate.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if createResp.Data.Item == nil || createResp.Data.Item.ID == "" || !createResp.Data.Item.Enabled {
		t.Fatalf("expected non-empty ID and Enabled=true, got %#v", createResp.Data)
	}
	createdID := createResp.Data.Item.ID

	// 3. POST /api/mihomo/native/rules with enabled=false -> 200 OK
	disabledCreateBody, _ := json.Marshal(map[string]interface{}{
		"type":     "DOMAIN",
		"payload":  "disabled.com",
		"outbound": "DIRECT",
		"enabled":  false,
	})
	reqCreateDisabled := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/rules?apply=false", bytes.NewReader(disabledCreateBody))
	recCreateDisabled := httptest.NewRecorder()
	mux.ServeHTTP(recCreateDisabled, reqCreateDisabled)
	if recCreateDisabled.Code != http.StatusOK {
		t.Fatalf("POST /api/mihomo/native/rules (disabled) status = %d: %s", recCreateDisabled.Code, recCreateDisabled.Body.String())
	}
	var disabledResp struct {
		Success bool                       `json:"success"`
		Data    NativeRuleMutationResponse `json:"data"`
	}
	if err := json.Unmarshal(recCreateDisabled.Body.Bytes(), &disabledResp); err != nil {
		t.Fatalf("unmarshal disabled create response: %v", err)
	}
	if disabledResp.Data.Item == nil || disabledResp.Data.Item.Enabled {
		t.Fatalf("expected Enabled=false, got true")
	}
	disabledID := disabledResp.Data.Item.ID

	// 4. PUT /api/mihomo/native/rules/nonexistent -> 404 NOT_FOUND
	updateBody, _ := json.Marshal(map[string]interface{}{
		"type":     "DOMAIN",
		"payload":  "foo.com",
		"outbound": "DIRECT",
	})
	reqPutMissing := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/nonexistent?apply=false", bytes.NewReader(updateBody))
	recPutMissing := httptest.NewRecorder()
	mux.ServeHTTP(recPutMissing, reqPutMissing)
	if recPutMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing rule update, got %d: %s", recPutMissing.Code, recPutMissing.Body.String())
	}

	// 5. PUT /api/mihomo/native/rules/{id} with mismatched body ID -> 400 ID_MISMATCH
	mismatchedIDBody, _ := json.Marshal(map[string]interface{}{
		"id":       "different-id",
		"type":     "DOMAIN",
		"payload":  "foo.com",
		"outbound": "DIRECT",
	})
	reqPutMismatch := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/"+createdID+"?apply=false", bytes.NewReader(mismatchedIDBody))
	recPutMismatch := httptest.NewRecorder()
	mux.ServeHTTP(recPutMismatch, reqPutMismatch)
	if recPutMismatch.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ID mismatch on update, got %d: %s", recPutMismatch.Code, recPutMismatch.Body.String())
	}
	if !strings.Contains(recPutMismatch.Body.String(), "ID_MISMATCH") {
		t.Fatalf("expected error code ID_MISMATCH, got %s", recPutMismatch.Body.String())
	}

	// 6. PUT /api/mihomo/native/rules/{disabledID} preserving enabled=false when omitted
	updateOmittedEnabled, _ := json.Marshal(map[string]interface{}{
		"type":     "DOMAIN-SUFFIX",
		"payload":  "disabled.com",
		"outbound": "REJECT",
	})
	reqPutPreserve := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/"+disabledID+"?apply=false", bytes.NewReader(updateOmittedEnabled))
	recPutPreserve := httptest.NewRecorder()
	mux.ServeHTTP(recPutPreserve, reqPutPreserve)
	if recPutPreserve.Code != http.StatusOK {
		t.Fatalf("PUT /api/mihomo/native/rules/{id} status = %d: %s", recPutPreserve.Code, recPutPreserve.Body.String())
	}
	var preserveResp struct {
		Success bool                       `json:"success"`
		Data    NativeRuleMutationResponse `json:"data"`
	}
	if err := json.Unmarshal(recPutPreserve.Body.Bytes(), &preserveResp); err != nil {
		t.Fatalf("unmarshal preserve response: %v", err)
	}
	if preserveResp.Data.Item == nil || preserveResp.Data.Item.Enabled {
		t.Fatalf("expected Enabled=false to be preserved, got true")
	}

	// 7. PUT /api/mihomo/native/rules/{disabledID} explicitly setting enabled=true
	updateEnable, _ := json.Marshal(map[string]interface{}{
		"type":     "DOMAIN-SUFFIX",
		"payload":  "disabled.com",
		"outbound": "REJECT",
		"enabled":  true,
	})
	reqPutEnable := httptest.NewRequest(http.MethodPut, "/api/mihomo/native/rules/"+disabledID+"?apply=false", bytes.NewReader(updateEnable))
	recPutEnable := httptest.NewRecorder()
	mux.ServeHTTP(recPutEnable, reqPutEnable)
	if recPutEnable.Code != http.StatusOK {
		t.Fatalf("PUT /api/mihomo/native/rules/{id} enable status = %d: %s", recPutEnable.Code, recPutEnable.Body.String())
	}
	var enableResp struct {
		Success bool                       `json:"success"`
		Data    NativeRuleMutationResponse `json:"data"`
	}
	if err := json.Unmarshal(recPutEnable.Body.Bytes(), &enableResp); err != nil {
		t.Fatalf("unmarshal enable response: %v", err)
	}
	if enableResp.Data.Item == nil || !enableResp.Data.Item.Enabled {
		t.Fatalf("expected Enabled=true after update, got false")
	}
}

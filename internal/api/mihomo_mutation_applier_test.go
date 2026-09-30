package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
)

type fakeMutationApplier struct {
	degraded       bool
	applyCount     int
	draftCount     int
	pendingCount   int
	reconcileCalls []string
}

func (f *fakeMutationApplier) ApplyNativeMutation(ctx context.Context, mutateFn func() error) error {
	f.applyCount++
	return mutateFn()
}

func (f *fakeMutationApplier) ApplyNativeMutationWithOutcome(ctx context.Context, mutateFn func() error) (*mihomo.MutationOutcome, error) {
	f.applyCount++
	err := mutateFn()
	return &mihomo.MutationOutcome{
		TxID:         "fake-tx-123",
		ApplyPath:    mihomo.ApplyPathHotReload,
		Generation:   1,
		ServerTiming: "hot_reload;dur=5",
	}, err
}

func (f *fakeMutationApplier) ApplyDraftOnly(ctx context.Context, mutateFn func() error) error {
	f.draftCount++
	return mutateFn()
}

func (f *fakeMutationApplier) ApplyPendingDraft(ctx context.Context) error {
	f.pendingCount++
	return nil
}

func (f *fakeMutationApplier) IsDegraded() bool {
	return f.degraded
}

func (f *fakeMutationApplier) CheckMutationAllowed() error {
	if f.degraded {
		return ErrRecoveryRequired
	}
	return nil
}

func (f *fakeMutationApplier) ExportEvidence(ctx context.Context) (*mihomo.RecoveryEvidenceDTO, error) {
	return &mihomo.RecoveryEvidenceDTO{
		State: mihomo.StateRecoveryRequired,
	}, nil
}

func (f *fakeMutationApplier) Reconcile(ctx context.Context, action string, force bool) error {
	f.reconcileCalls = append(f.reconcileCalls, action)
	return nil
}

func TestMihomoHandler_DegradedReturns503(t *testing.T) {
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(nil)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	// Attempt a mutation while degraded
	body := []byte(`{"type":"select","name":"TestGroup","proxies":["DIRECT"]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/groups", bytes.NewReader(body))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected HTTP 503, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["code"] != "RECOVERY_REQUIRED" {
		t.Fatalf("expected code RECOVERY_REQUIRED, got: %#v", resp)
	}
}

func TestMihomoHandler_ApplyVsDraftRouting(t *testing.T) {
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(nil)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: false}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	// 1. apply=false -> ApplyDraftOnly
	body := []byte(`{"type":"select","name":"DraftGroup","proxies":["DIRECT"]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/groups?apply=false", bytes.NewReader(body))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if applier.draftCount != 1 || applier.applyCount != 0 {
		t.Fatalf("expected draftCount=1, applyCount=0; got draft=%d apply=%d", applier.draftCount, applier.applyCount)
	}

	// 2. apply=true (default) -> ApplyNativeMutation
	body2 := []byte(`{"type":"select","name":"AppliedGroup","proxies":["DIRECT"]}`)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/groups", bytes.NewReader(body2))
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if applier.applyCount != 1 {
		t.Fatalf("expected applyCount=1, got: %d", applier.applyCount)
	}
}

func TestMihomoHandler_RecoveryReconcileEndpoint(t *testing.T) {
	h := NewMihomoHandler(nil)
	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	body := []byte(`{"action":"rollback_to_lkg","force":false}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mihomo/recovery/reconcile", bytes.NewReader(body))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if len(applier.reconcileCalls) != 1 || applier.reconcileCalls[0] != "rollback_to_lkg" {
		t.Fatalf("expected reconcile call rollback_to_lkg, got: %#v", applier.reconcileCalls)
	}
}

func TestMihomoHandler_MutationInvokesBridgePrepare(t *testing.T) {
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := NewMihomoHandler(nil)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: false}
	h.SetMutationApplier(applier)

	prepareCalled := 0
	h.SetNativeBridgeLifecycle(func(ctx context.Context, previous []mihomonative.BridgeRef) error {
		prepareCalled++
		return nil
	}, nil, nil)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	body := []byte(`{"type":"select","name":"TestGroup","proxies":["DIRECT"]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/mihomo/native/groups", bytes.NewReader(body))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if applier.applyCount != 1 {
		t.Fatalf("expected applyCount=1, got: %d", applier.applyCount)
	}
	if prepareCalled != 1 {
		t.Fatalf("expected prepareCalled=1, got: %d", prepareCalled)
	}
}

type sentinelReader struct {
	readCalled bool
	data       []byte
	offset     int
}

func newSentinelReader(body string) *sentinelReader {
	return &sentinelReader{data: []byte(body)}
}

func (s *sentinelReader) Read(p []byte) (n int, err error) {
	s.readCalled = true
	if s.offset >= len(s.data) {
		return 0, io.EOF
	}
	n = copy(p, s.data[s.offset:])
	s.offset += n
	return n, nil
}

type trackingRefresherEngine struct {
	fakeMihomoEngine
	refreshCalls int
}

func (t *trackingRefresherEngine) RefreshProvider(ctx context.Context, providerName string) error {
	t.refreshCalls++
	return nil
}

func (t *trackingRefresherEngine) ControllerTarget() (string, string) {
	return "127.0.0.1:9090", ""
}

func TestMihomoHandler_DegradedMatrixFailClosed(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "native.json")
	initialBytes := []byte("{\n  \"version\": 4,\n  \"proxies\": [],\n  \"subscriptions\": [],\n  \"groups\": [],\n  \"rules\": [],\n  \"rule_providers\": []\n}\n")
	if err := os.WriteFile(storePath, initialBytes, 0644); err != nil {
		t.Fatal(err)
	}
	native, err := mihomonative.NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	initialHash := sha256.Sum256(initialBytes)

	engine := &trackingRefresherEngine{}
	h := NewMihomoHandler(engine)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	var bridgePrepareCalled, bridgeReadyCalled, bridgeDownCalled int
	h.SetNativeBridgeLifecycle(
		func(ctx context.Context, refs []mihomonative.BridgeRef) error {
			bridgePrepareCalled++
			return nil
		},
		func(ctx context.Context) error {
			bridgeReadyCalled++
			return nil
		},
		func(ctx context.Context) error {
			bridgeDownCalled++
			return nil
		},
	)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	routes := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "Install", method: http.MethodPost, path: "/api/mihomo/install", body: ""},
		{name: "Update", method: http.MethodPost, path: "/api/mihomo/update", body: ""},
		{name: "Uninstall", method: http.MethodPost, path: "/api/mihomo/uninstall", body: ""},
		{name: "Reload", method: http.MethodPost, path: "/api/mihomo/reload", body: ""},
		{name: "ProxyCreate", method: http.MethodPost, path: "/api/mihomo/native/proxies", body: `{"uri":"ss://test"}`},
		{name: "ProxyUpdate", method: http.MethodPut, path: "/api/mihomo/native/proxies/p1", body: `{"uri":"ss://test2"}`},
		{name: "ProxyDelete", method: http.MethodDelete, path: "/api/mihomo/native/proxies/p1", body: ""},
		{name: "SubscriptionCreate", method: http.MethodPost, path: "/api/mihomo/native/subscriptions", body: `{"name":"sub1","url":"http://example.com"}`},
		{name: "SubscriptionUpdate", method: http.MethodPut, path: "/api/mihomo/native/subscriptions/s1", body: `{"name":"sub1","url":"http://example.com"}`},
		{name: "SubscriptionDelete", method: http.MethodDelete, path: "/api/mihomo/native/subscriptions/s1", body: ""},
		{name: "SubscriptionRefresh", method: http.MethodPost, path: "/api/mihomo/native/subscriptions/s1/refresh", body: ""},
		{name: "GroupSavePOST", method: http.MethodPost, path: "/api/mihomo/native/groups", body: `{"name":"g1","type":"select"}`},
		{name: "GroupSavePUT", method: http.MethodPut, path: "/api/mihomo/native/groups/g1", body: `{"name":"g1","type":"select"}`},
		{name: "GroupDelete", method: http.MethodDelete, path: "/api/mihomo/native/groups/g1", body: ""},
		{name: "RuleCreate", method: http.MethodPost, path: "/api/mihomo/native/rules", body: `{"type":"DOMAIN","payload":"example.com","target":"DIRECT"}`},
		{name: "RuleUpdate", method: http.MethodPut, path: "/api/mihomo/native/rules/r1", body: `{"type":"DOMAIN","payload":"example.com","target":"DIRECT"}`},
		{name: "RuleDelete", method: http.MethodDelete, path: "/api/mihomo/native/rules/r1", body: ""},
		{name: "RuleOrder", method: http.MethodPut, path: "/api/mihomo/native/rules/order", body: `{"ids":["r1","r2"]}`},
		{name: "UnsupportedRulesDelete1", method: http.MethodPost, path: "/api/mihomo/native/rules/unsupported/delete", body: `{"ids":["r1"],"revision":"rev1"}`},
		{name: "UnsupportedRulesDelete2Alias", method: http.MethodPost, path: "/api/router/mihomo/rules/unsupported/delete", body: `{"ids":["r1"],"revision":"rev1"}`},
		{name: "RuleProviderSavePOST", method: http.MethodPost, path: "/api/mihomo/native/rule-providers", body: `{"name":"rp1","type":"http"}`},
		{name: "RuleProviderSavePUT", method: http.MethodPut, path: "/api/mihomo/native/rule-providers/rp1", body: `{"name":"rp1","type":"http"}`},
		{name: "RuleProviderDelete", method: http.MethodDelete, path: "/api/mihomo/native/rule-providers/rp1", body: ""},
		{name: "NativeReset", method: http.MethodPost, path: "/api/mihomo/native/reset", body: ""},
	}

	if len(routes) != 24 {
		t.Fatalf("expected 24 ordinary mutation routes, got: %d", len(routes))
	}

	for _, tc := range routes {
		t.Run(tc.name, func(t *testing.T) {
			sentinel := newSentinelReader(tc.body)
			req := httptest.NewRequest(tc.method, tc.path, sentinel)
			rec := httptest.NewRecorder()

			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("[%s] expected status 503, got %d: %s", tc.name, rec.Code, rec.Body.String())
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("[%s] unmarshal response error: %v", tc.name, err)
			}
			if resp["code"] != "RECOVERY_REQUIRED" {
				t.Fatalf("[%s] expected code RECOVERY_REQUIRED, got: %#v", tc.name, resp)
			}

			// Sentinel reader MUST NOT be read
			if sentinel.readCalled {
				t.Fatalf("[%s] request body reader was read! Expected zero reads before degradation rejection", tc.name)
			}
		})
	}

	// Assert zero side-effects across all calls:
	if applier.applyCount != 0 || applier.draftCount != 0 {
		t.Fatalf("expected applyCount=0 and draftCount=0, got apply=%d draft=%d", applier.applyCount, applier.draftCount)
	}
	if bridgePrepareCalled != 0 || bridgeReadyCalled != 0 || bridgeDownCalled != 0 {
		t.Fatalf("expected zero bridge callbacks, got prepare=%d ready=%d down=%d", bridgePrepareCalled, bridgeReadyCalled, bridgeDownCalled)
	}
	if engine.refreshCalls != 0 {
		t.Fatalf("expected zero provider refresh calls, got %d", engine.refreshCalls)
	}
	if engine.reloadCalls != 0 {
		t.Fatalf("expected zero engine reload calls, got %d", engine.reloadCalls)
	}

	// Verify store bytes and SHA-256 digest are strictly unchanged
	currentBytes, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	currentHash := sha256.Sum256(currentBytes)
	if !bytes.Equal(initialBytes, currentBytes) || currentHash != initialHash {
		t.Fatalf("store file was modified on disk! initial SHA256=%x current SHA256=%x", initialHash, currentHash)
	}
}

func TestMihomoHandler_DegradedDirectMethodsFailClosed(t *testing.T) {
	native, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	engine := &trackingRefresherEngine{}
	h := NewMihomoHandler(engine)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	ctx := context.Background()

	if err := h.Restart(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Restart() err = %v, want ErrRecoveryRequired", err)
	}
	if err := h.Reload(); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Reload() err = %v, want ErrRecoveryRequired", err)
	}
	if err := h.RefreshNativeSubscription(ctx, "sub-1"); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("RefreshNativeSubscription() err = %v, want ErrRecoveryRequired", err)
	}
	if err := h.BatchSaveRules(ctx, []mihomonative.Rule{}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("BatchSaveRules() err = %v, want ErrRecoveryRequired", err)
	}

	if engine.refreshCalls != 0 {
		t.Fatalf("expected 0 refresh calls, got %d", engine.refreshCalls)
	}
	if engine.reloadCalls != 0 {
		t.Fatalf("expected 0 reload calls, got %d", engine.reloadCalls)
	}
}

func TestMihomoHandler_InspectPostIsReadOnly(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "native.json")
	initialBytes := []byte("{\n  \"version\": 4,\n  \"proxies\": [],\n  \"subscriptions\": [],\n  \"groups\": [],\n  \"rules\": [],\n  \"rule_providers\": []\n}\n")
	if err := os.WriteFile(storePath, initialBytes, 0644); err != nil {
		t.Fatal(err)
	}
	native, err := mihomonative.NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}

	engine := &trackingRefresherEngine{
		fakeMihomoEngine: fakeMihomoEngine{running: true, pid: 1234},
	}
	h := NewMihomoHandler(engine)
	h.SetNativeStore(native)

	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/mihomo/router/inspect", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// Inspect is read-only: it must NOT be rejected by the degraded mutation gate
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("Inspect endpoint was rejected by degraded gate: %d %s", rec.Code, rec.Body.String())
	}

	// Verify store is untouched
	currentBytes, _ := os.ReadFile(storePath)
	if !bytes.Equal(initialBytes, currentBytes) {
		t.Fatal("store was modified during read-only inspection!")
	}
	if applier.applyCount != 0 || applier.draftCount != 0 {
		t.Fatalf("inspect triggered mutation callbacks! apply=%d draft=%d", applier.applyCount, applier.draftCount)
	}
}

func TestMihomoHandler_RecoveryReconcileAllowedWhenDegraded(t *testing.T) {
	h := NewMihomoHandler(nil)
	applier := &fakeMutationApplier{degraded: true}
	h.SetMutationApplier(applier)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, nil)

	// 1. Valid action rollback_to_lkg succeeds
	body1 := []byte(`{"action":"rollback_to_lkg","force":false}`)
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/api/mihomo/recovery/reconcile", bytes.NewReader(body1))
	mux.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid recovery reconcile, got %d: %s", rec1.Code, rec1.Body.String())
	}

	// 2. Invalid action rejected
	body2 := []byte(`{"action":"unsupported_action","force":false}`)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/mihomo/recovery/reconcile", bytes.NewReader(body2))
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid recovery reconcile, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

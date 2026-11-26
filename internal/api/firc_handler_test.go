package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestFircHandler_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	sbDir := filepath.Join(tmpDir, "sing-box", "config.d")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sing-box config.d: %v", err)
	}

	// Write a sample 10-tunnels.json and 20-router.json for migration test
	tunnelsJSON := `{
  "outbounds": [
    {
      "type": "vless",
      "tag": "vless-test",
      "server": "example.com",
      "server_port": 443,
      "uuid": "11111111-2222-3333-4444-555555555555",
      "tls": {"enabled": true, "server_name": "example.com"}
    }
  ]
}`
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	settingsStore := storage.NewSettingsStore(tmpDir)
	_, _ = settingsStore.Load()

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})
	h := NewFircHandler(op)
	h.SetSettingsStore(settingsStore)
	h.SetSingboxConfigDir(sbDir)

	var selectedState bool
	h.SetOnSelectEngine(func(selected bool) {
		selectedState = selected
	})

	var startedOther []string
	var stoppedOther []string
	h.SetOtherEngineLifecycle(
		func(_ context.Context, engine string) error {
			startedOther = append(startedOther, engine)
			return nil
		},
		func(_ context.Context, engine string) error {
			stoppedOther = append(stoppedOther, engine)
			return nil
		},
	)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	// 1. GET /api/firc/status
	req := httptest.NewRequest(http.MethodGet, "/api/firc/status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/firc/status status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. POST /api/firc/tunnels/preview
	previewBody, _ := json.Marshal(map[string]string{
		"uri": "tt://user:pass@1.2.3.4:443?sni=example.com#MyTT",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/firc/tunnels/preview", bytes.NewReader(previewBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/tunnels/preview status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 3a. POST /api/firc/migrate?dry_run=1 (must not write tunnels.yaml)
	req = httptest.NewRequest(http.MethodPost, "/api/firc/migrate?dry_run=1", bytes.NewReader([]byte(`{}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/migrate?dry_run=1 status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(op.TunnelsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected dry_run migration not to write tunnels.yaml, stat err=%v", err)
	}

	// 3b. POST /api/firc/migrate (live migration)
	req = httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(`{}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/migrate status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 4. GET /api/firc/tunnels (verify migrated tunnel)
	req = httptest.NewRequest(http.MethodGet, "/api/firc/tunnels", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/firc/tunnels status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("vless-test")) {
		t.Fatalf("expected migrated tunnel vless-test in response, got: %s", rec.Body.String())
	}

	// 5. PUT /api/firc/groups (create routing group)
	groupBody, _ := json.Marshal(map[string]interface{}{
		"groups": []firc.Group{
			{
				ID:        "youtube",
				Name:      "YouTube",
				Interface: firc.GroupInterface{"vless-test"},
				Enable:    true,
			},
		},
	})
	req = httptest.NewRequest(http.MethodPut, "/api/firc/groups", bytes.NewReader(groupBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/firc/groups status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 6a. Fault injection: selecting firc when fircd start fails MUST fail and leave RoutingEngine untouched
	op.SetLifecycleHooks(
		func(_ context.Context) error { return errors.New("fircd binary failed to start") },
		func(_ context.Context) error { return nil },
	)
	settingsBody, _ := json.Marshal(map[string]interface{}{
		"selected": true,
	})
	req = httptest.NewRequest(http.MethodPut, "/api/firc/settings", bytes.NewReader(settingsBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected PUT /api/firc/settings to fail when fircd start fails, got 200")
	}
	cur, _ := settingsStore.Get()
	if cur.SingboxRouter.RoutingEngine == "firc" {
		t.Fatalf("expected RoutingEngine not to switch to firc when fircd start fails")
	}

	// 6b. Healthy fircd daemon: selecting firc succeeds and commits RoutingEngine = "firc"
	mockFircMux := http.NewServeMux()
	mockFircMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(firc.NetfilterHealth{Ok: true})
	})
	mockFircMux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(firc.PutTunnelsResponse{})
	})
	mockFircMux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"groups": []firc.Group{}})
	})
	mockFircMux.HandleFunc("/api/v1/system/settings", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"inboundPort": 1099})
	})
	mockSrv := httptest.NewServer(mockFircMux)
	defer mockSrv.Close()
	op.SetClient(firc.NewHTTPClient(mockSrv.URL, mockSrv.Client()))
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return nil },
	)

	settingsBody2, _ := json.Marshal(map[string]interface{}{
		"inboundPort": 1099,
		"selected":    true,
	})
	req = httptest.NewRequest(http.MethodPut, "/api/firc/settings", bytes.NewReader(settingsBody2))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/firc/settings status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !selectedState {
		t.Fatalf("expected SetOnSelectEngine callback to receive true")
	}
	cur, err := settingsStore.Get()
	if err != nil || cur.SingboxRouter.RoutingEngine != "firc" {
		t.Fatalf("expected RoutingEngine == firc, got %q (err=%v)", cur.SingboxRouter.RoutingEngine, err)
	}
	if len(stoppedOther) != 1 || stoppedOther[0] != "sing-box" {
		t.Fatalf("expected unified coordinator to stop previous engine sing-box, got %v", stoppedOther)
	}

	// 7a. Target "mihomo" MUST be rejected (Mihomo runtime removed)
	mihomoBody, _ := json.Marshal(map[string]string{"engine": "mihomo"})
	req = httptest.NewRequest(http.MethodPost, "/api/firc/engine/switch", bytes.NewReader(mihomoBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("expected POST /api/firc/engine/switch (to mihomo) to fail, got 200 body=%s", rec.Body.String())
	}

	// 7b. Unified switch via POST /api/firc/engine/switch (firc -> sing-box)
	reBody, _ := json.Marshal(map[string]string{"engine": "sing-box"})
	req = httptest.NewRequest(http.MethodPost, "/api/firc/engine/switch", bytes.NewReader(reBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/engine/switch (to sing-box) status=%d body=%s", rec.Code, rec.Body.String())
	}
	cur, _ = settingsStore.Get()
	if cur.SingboxRouter.RoutingEngine != "sing-box" {
		t.Fatalf("expected RoutingEngine == sing-box after /api/firc/engine/switch, got %q", cur.SingboxRouter.RoutingEngine)
	}
	if len(startedOther) != 1 || startedOther[0] != "sing-box" {
		t.Fatalf("expected unified coordinator to start sing-box, got %v", startedOther)
	}
	if selectedState {
		t.Fatalf("expected SetOnSelectEngine callback to receive false when switching away from firc")
	}

	// 8. Legacy migration switch: settings.json with legacy "mihomo" -> POST /api/firc/engine/switch (to firc) succeeds
	_ = settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "mihomo"
		return nil
	})
	swBody, _ := json.Marshal(map[string]string{"engine": "firc"})
	req = httptest.NewRequest(http.MethodPost, "/api/firc/engine/switch", bytes.NewReader(swBody))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/engine/switch (legacy mihomo -> firc) status=%d body=%s", rec.Code, rec.Body.String())
	}
	cur, _ = settingsStore.Get()
	if cur.SingboxRouter.RoutingEngine != "firc" {
		t.Fatalf("expected RoutingEngine == firc after legacy migration switch, got %q", cur.SingboxRouter.RoutingEngine)
	}

	// 9. P0.3 & P1.2: Verified recovery reconciliation and hardened break-glass forceAdminClear
	if err := op.SetRecoveryRequiredWithMeta(firc.RecoveryMarker{
		Reason:         "simulated rollback failure",
		TransitionID:   "tx-breakglass-1",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
	}); err != nil {
		t.Fatalf("SetRecoveryRequiredWithMeta: %v", err)
	}
	if !op.IsRecoveryRequired() {
		t.Fatal("expected IsRecoveryRequired() == true")
	}

	// 9a. P1.2: Break-glass forceAdminClear from non-loopback remote peer MUST be rejected with 403 Forbidden
	req = httptest.NewRequest(http.MethodPost, "/api/firc/recovery/clear", bytes.NewReader([]byte(`{"forceAdminClear":true,"confirm":"FORCE_CLEAR_UNVERIFIED","actor":"remote-attacker","transitionId":"tx-breakglass-1"}`)))
	req.RemoteAddr = "192.168.90.55:54321"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !op.IsRecoveryRequired() {
		t.Fatalf("P1.2 regression: expected 403 Forbidden on non-loopback forceAdminClear, got code=%d recovery=%v", rec.Code, op.IsRecoveryRequired())
	}

	// 9a-2. P1.2: Loopback forceAdminClear with X-Forwarded-For header MUST be rejected with 403 Forbidden
	req = httptest.NewRequest(http.MethodPost, "/api/firc/recovery/clear", bytes.NewReader([]byte(`{"forceAdminClear":true,"confirm":"FORCE_CLEAR_UNVERIFIED","actor":"proxied-caller","transitionId":"tx-breakglass-1"}`)))
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !op.IsRecoveryRequired() {
		t.Fatalf("P1.2 regression: expected 403 Forbidden on X-Forwarded-For forceAdminClear, got code=%d recovery=%v", rec.Code, op.IsRecoveryRequired())
	}

	// 9a-3. P1.2: Loopback forceAdminClear with wrong confirm token or mismatched transitionId MUST fail (400)
	req = httptest.NewRequest(http.MethodPost, "/api/firc/recovery/clear", bytes.NewReader([]byte(`{"forceAdminClear":true,"confirm":"FORCE_CLEAR_UNVERIFIED","actor":"local-admin","transitionId":"wrong-tx"}`)))
	req.RemoteAddr = "127.0.0.1:54321"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !op.IsRecoveryRequired() {
		t.Fatalf("P1.2 regression: expected 400 and recovery_required=true on mismatched transitionId, got code=%d recovery=%v", rec.Code, op.IsRecoveryRequired())
	}

	// 9b. Unverified clear when target engine start fails MUST fail (500) and keep recovery_required=true
	op.SetLifecycleHooks(
		func(_ context.Context) error { return errors.New("fircd still unhealthy") },
		func(_ context.Context) error { return nil },
	)
	req = httptest.NewRequest(http.MethodPost, "/api/firc/recovery/clear", bytes.NewReader([]byte(`{"targetEngine":"firc"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || !op.IsRecoveryRequired() {
		t.Fatalf("P0.3 regression: expected 500 and recovery_required=true when reconciliation fails, got code=%d recovery=%v", rec.Code, op.IsRecoveryRequired())
	}

	// 9c. Healthy target engine -> /api/firc/recovery/reconcile verifies state and clears recovery_required
	op.SetLifecycleHooks(
		func(_ context.Context) error { return nil },
		func(_ context.Context) error { return nil },
	)
	req = httptest.NewRequest(http.MethodPost, "/api/firc/recovery/reconcile", bytes.NewReader([]byte(`{"targetEngine":"firc"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/recovery/reconcile status=%d body=%s", rec.Code, rec.Body.String())
	}
	if op.IsRecoveryRequired() {
		t.Fatal("expected IsRecoveryRequired() == false after successful /api/firc/recovery/reconcile")
	}

	// 9d. P1.2: Valid loopback break-glass clear with matching runtime transitionId and actor succeeds
	if err := op.SetRecoveryRequiredWithMeta(firc.RecoveryMarker{
		Reason:         "second simulated failure",
		TransitionID:   "tx-breakglass-2",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
	}); err != nil {
		t.Fatalf("SetRecoveryRequiredWithMeta: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/firc/recovery/clear", bytes.NewReader([]byte(`{"forceAdminClear":true,"confirm":"FORCE_CLEAR_UNVERIFIED","actor":"cli-root","transitionId":"tx-breakglass-2"}`)))
	req.RemoteAddr = "127.0.0.1:54321"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || op.IsRecoveryRequired() {
		t.Fatalf("P1.2: expected valid loopback break-glass clear to succeed, got code=%d body=%s recovery=%v", rec.Code, rec.Body.String(), op.IsRecoveryRequired())
	}

	// 9e. Reload control action when recovery_required is true automatically reconciles and clears recovery
	if err := op.SetRecoveryRequiredWithMeta(firc.RecoveryMarker{
		Reason:         "third simulated failure",
		TransitionID:   "tx-reload-reconcile",
		TargetEngine:   "firc",
		PreviousEngine: "sing-box",
	}); err != nil {
		t.Fatalf("SetRecoveryRequiredWithMeta: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/firc/control", bytes.NewReader([]byte(`{"action":"reload"}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || op.IsRecoveryRequired() {
		t.Fatalf("expected control reload to automatically reconcile recovery, got code=%d body=%s recovery=%v", rec.Code, rec.Body.String(), op.IsRecoveryRequired())
	}
}

func TestFircHandler_AutolistCandidateConfirmationProxyAndConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})

	var (
		rtMu                sync.Mutex
		rtGroups            []firc.Group
		failGetGroups       bool
		failPutGroupsOnce   bool
		onGetGroups         func()
		directFircdAddCalls int32
		dismissCalls        int32
	)

	cloneGrps := func(in []firc.Group) []firc.Group {
		raw, _ := json.Marshal(in)
		var out []firc.Group
		_ = json.Unmarshal(raw, &out)
		return out
	}

	mockMux := http.NewServeMux()
	mockMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(firc.NetfilterHealth{Ok: true})
	})
	mockMux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			rtMu.Lock()
			hook := onGetGroups
			rtMu.Unlock()
			if hook != nil {
				hook()
			}
		}
		rtMu.Lock()
		defer rtMu.Unlock()
		switch r.Method {
		case http.MethodGet:
			if failGetGroups {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected runtime GetGroups error"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": cloneGrps(rtGroups)})
		case http.MethodPut:
			var req struct {
				Groups []firc.Group `json:"groups"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if failPutGroupsOnce {
				failPutGroupsOnce = false
				rtGroups = cloneGrps(req.Groups)
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected runtime PutGroups error"})
				return
			}
			rtGroups = cloneGrps(req.Groups)
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": cloneGrps(rtGroups)})
		}
	})
	mockMux.HandleFunc("/api/v1/system/autolist/candidates/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/add") {
			atomic.AddInt32(&directFircdAddCalls, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/dismiss") {
			atomic.AddInt32(&dismissCalls, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	mockSrv := httptest.NewServer(mockMux)
	defer mockSrv.Close()
	op.SetClient(firc.NewHTTPClient(mockSrv.URL, mockSrv.Client()))

	var (
		nfMu       sync.Mutex
		nfGroups   []firc.Group
		failNfOnce bool
	)
	op.SetNetfilterStrictHook(func(_ context.Context, _ firc.ExtConfig, groups []firc.Group) error {
		nfMu.Lock()
		defer nfMu.Unlock()
		if failNfOnce {
			failNfOnce = false
			return errors.New("injected netfilter reconcile failure")
		}
		nfGroups = cloneGrps(groups)
		return nil
	})

	seedGroups := []firc.Group{
		{
			ID:        "g1",
			Name:      "MainProxy",
			Interface: firc.GroupInterface{"tunvless0"},
			Enable:    true,
			Rules: []firc.Rule{
				{Type: "domain", Rule: "seed.example.com", Enable: true},
			},
		},
	}
	if _, err := op.ApplyGroups(context.Background(), seedGroups); err != nil {
		t.Fatalf("seed ApplyGroups: %v", err)
	}

	h := NewFircHandler(op)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	assertAllStatesRules := func(step string, wantRules []string) {
		t.Helper()
		diskGrps, err := op.LoadGroupsFile()
		if err != nil {
			t.Fatalf("%s: LoadGroupsFile: %v", step, err)
		}
		rtMu.Lock()
		curRT := cloneGrps(rtGroups)
		rtMu.Unlock()
		nfMu.Lock()
		curNF := cloneGrps(nfGroups)
		nfMu.Unlock()

		extract := func(grps []firc.Group) []string {
			if len(grps) != 1 {
				t.Fatalf("%s: expected 1 group, got %d", step, len(grps))
			}
			out := make([]string, 0, len(grps[0].Rules))
			for _, r := range grps[0].Rules {
				out = append(out, r.Rule)
			}
			return out
		}
		if got := extract(diskGrps); !reflect.DeepEqual(got, wantRules) {
			t.Fatalf("%s: disk rules=%v, want %v", step, got, wantRules)
		}
		if got := extract(curRT); !reflect.DeepEqual(got, wantRules) {
			t.Fatalf("%s: runtime rules=%v, want %v", step, got, wantRules)
		}
		if got := extract(curNF); !reflect.DeepEqual(got, wantRules) {
			t.Fatalf("%s: netfilter rules=%v, want %v", step, got, wantRules)
		}
	}

	postJSON := func(path string, body string) *httptest.ResponseRecorder {
		var rdr *bytes.Reader
		if body == "" {
			rdr = bytes.NewReader(nil)
		} else {
			rdr = bytes.NewReader([]byte(body))
		}
		req := httptest.NewRequest(http.MethodPost, path, rdr)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. /api/firc/autolist/candidate without confirmed, without groupId, with unknown groupId, and under recovery_required
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"unconfirmed.example.com","action":"add","groupId":"g1","confirmed":false}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without confirmed=true, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"nogroup.example.com","action":"add","confirmed":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without groupId (U1), got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"badgroup.example.com","action":"add","groupId":"missing","confirmed":true}`); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown groupId, got %d (%s)", rec.Code, rec.Body.String())
	}
	_ = op.SetRecoveryRequired("simulated recovery state")
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"recovery.example.com","action":"add","groupId":"g1","confirmed":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 under recovery_required, got %d (%s)", rec.Code, rec.Body.String())
	}
	if err := op.ClearRecovery(); err != nil {
		t.Fatalf("ClearRecovery: %v", err)
	}
	if got := atomic.LoadInt32(&directFircdAddCalls); got != 0 {
		t.Fatalf("expected 0 direct fircd /add calls, got %d", got)
	}
	assertAllStatesRules("after_u1_guards", []string{"seed.example.com"})

	// 2. U2: Direct POST /api/firc/v1/system/autolist/candidates/{domain}/add cannot bypass confirmation or transaction
	if rec := postJSON("/api/firc/v1/system/autolist/candidates/bypass1.example.com/add", ``); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty-body v1 proxy /add, got %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := postJSON("/api/firc/v1/system/autolist/candidates/bypass2.example.com/add", `{"confirmed":true}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on v1 proxy /add without groupId, got %d (%s)", rec.Code, rec.Body.String())
	}
	_ = op.SetRecoveryRequired("simulated recovery state for v1 proxy")
	if rec := postJSON("/api/firc/v1/system/autolist/candidates/bypass3.example.com/add", `{"confirmed":true,"groupId":"g1"}`); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on v1 proxy /add under recovery_required, got %d (%s)", rec.Code, rec.Body.String())
	}
	if err := op.ClearRecovery(); err != nil {
		t.Fatalf("ClearRecovery: %v", err)
	}
	if got := atomic.LoadInt32(&directFircdAddCalls); got != 0 {
		t.Fatalf("expected 0 direct fircd /add calls after v1 proxy rejections, got %d", got)
	}
	assertAllStatesRules("after_u2_proxy_guards", []string{"seed.example.com"})

	// 3. Runtime GetGroups read failure, disk SaveGroupsFile failure, runtime PutGroups failure, and netfilter failure
	// must all return HTTP 500 (never 200 OK) and roll back disk, runtime, and netfilter state.
	rtMu.Lock()
	failGetGroups = true
	rtMu.Unlock()
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"getfail.example.com","action":"add","groupId":"g1","confirmed":true}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on GetGroups failure, got %d (%s)", rec.Code, rec.Body.String())
	}
	rtMu.Lock()
	failGetGroups = false
	rtMu.Unlock()
	assertAllStatesRules("after_http_get_groups_failure", []string{"seed.example.com"})

	op.SetGroupsSaveFaultHook(func(_ []firc.Group) error {
		return errors.New("injected disk save failure")
	})
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"savefail.example.com","action":"add","groupId":"g1","confirmed":true}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on SaveGroupsFile failure, got %d (%s)", rec.Code, rec.Body.String())
	}
	op.SetGroupsSaveFaultHook(nil)
	assertAllStatesRules("after_http_save_groups_failure", []string{"seed.example.com"})

	rtMu.Lock()
	failPutGroupsOnce = true
	rtMu.Unlock()
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"putfail.example.com","action":"add","groupId":"g1","confirmed":true}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on PutGroups failure, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertAllStatesRules("after_http_put_groups_failure_rollback", []string{"seed.example.com"})

	nfMu.Lock()
	failNfOnce = true
	nfMu.Unlock()
	if rec := postJSON("/api/firc/autolist/candidate", `{"domain":"nffail.example.com","action":"add","groupId":"g1","confirmed":true}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on netfilter failure, got %d (%s)", rec.Code, rec.Body.String())
	}
	assertAllStatesRules("after_http_netfilter_failure_rollback", []string{"seed.example.com"})

	// 4. U3: Deterministic barrier test for two concurrent HTTP add requests (one via /api/firc/autolist/candidate
	// and one via /api/firc/v1/system/autolist/candidates/{domain}/add) targeting the same group.
	firstInGet := make(chan struct{})
	releaseFirstGet := make(chan struct{})
	var once sync.Once
	var inFlight int32
	var maxInFlight int32

	rtMu.Lock()
	onGetGroups = func() {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			prev := atomic.LoadInt32(&maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, cur) {
				break
			}
		}
		once.Do(func() {
			close(firstInGet)
			<-releaseFirstGet
		})
		atomic.AddInt32(&inFlight, -1)
	}
	rtMu.Unlock()

	recCh := make(chan *httptest.ResponseRecorder, 2)
	secondDispatched := make(chan struct{})
	go func() {
		recCh <- postJSON("/api/firc/autolist/candidate", `{"domain":"concurrent-a.example.com","action":"add","groupId":"g1","confirmed":true}`)
	}()
	<-firstInGet
	go func() {
		close(secondDispatched)
		recCh <- postJSON("/api/firc/v1/system/autolist/candidates/concurrent-b.example.com/add", `{"confirmed":true,"groupId":"g1"}`)
	}()
	<-secondDispatched
	time.Sleep(25 * time.Millisecond)
	close(releaseFirstGet)

	for i := 0; i < 2; i++ {
		rec := <-recCh
		if rec.Code != http.StatusOK {
			t.Fatalf("concurrent HTTP add #%d failed with status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	rtMu.Lock()
	onGetGroups = nil
	rtMu.Unlock()

	if gotMax := atomic.LoadInt32(&maxInFlight); gotMax != 1 {
		t.Fatalf("expected serialized GetGroups across concurrent HTTP handlers (maxInFlight=1), got %d", gotMax)
	}
	if got := atomic.LoadInt32(&directFircdAddCalls); got != 0 {
		t.Fatalf("expected 0 direct fircd /add calls after concurrent adds, got %d", got)
	}
	if got := atomic.LoadInt32(&dismissCalls); got != 2 {
		t.Fatalf("expected 2 candidate dismiss calls after 2 confirmed adds, got %d", got)
	}
	assertAllStatesRules("after_concurrent_http_adds", []string{
		"seed.example.com",
		"concurrent-a.example.com",
		"concurrent-b.example.com",
	})
}

func TestFircHandler_InstallUninstallHTTPLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	op := firc.NewOperator(firc.Options{ConfigDir: filepath.Join(tmpDir, "firc")})
	h := NewFircHandler(op)
	store := storage.NewSettingsStore(tmpDir)
	h.SetSettingsStore(store)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	// 1. Without installer wired -> 503 Unavailable
	req := httptest.NewRequest(http.MethodGet, "/api/firc/install-status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when installer is nil, got %d", rec.Code)
	}

	// 2. Wire installer with empty spec -> status returns state=unavailable, POST install returns 503
	instUnavail := firc.NewOnDemandInstaller("aarch64-3.10", firc.FircBinarySpec{Version: "0.4.0"})
	instUnavail.SetPackageLayout(firc.DefaultPackageLayout(tmpDir))
	h.SetInstaller(instUnavail)

	req = httptest.NewRequest(http.MethodGet, "/api/firc/install-status", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on install-status, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"state":"unavailable"`) {
		t.Fatalf("expected state=unavailable in response, got %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/firc/install", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on POST install when release unavailable, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 3. When FIRC is active routing engine -> POST uninstall returns 409 Conflict
	_ = store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	})
	req = httptest.NewRequest(http.MethodPost, "/api/firc/uninstall", bytes.NewReader([]byte(`{"purgeConfigs":false}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on uninstall when FIRC is active engine, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 4. When dependent services exist -> POST uninstall returns 409 Conflict
	_ = store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	})
	instUnavail.SetDependencyChecker(func(context.Context) ([]string, error) {
		return []string{"VLESS XHTTP Server"}, nil
	})
	req = httptest.NewRequest(http.MethodPost, "/api/firc/uninstall", bytes.NewReader([]byte(`{"purgeConfigs":false}`)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_DEPENDENCIES") {
		t.Fatalf("expected 409 FIRC_DEPENDENCIES on uninstall with dependencies, got %d (%s)", rec.Code, rec.Body.String())
	}
	for _, body := range []string{`{"purgeConfigs":true}`, `{"unknown":true}`, `{} {}`, `{`} {
		req = httptest.NewRequest(http.MethodPost, "/api/firc/uninstall", strings.NewReader(body))
		req.ContentLength = -1 // chunked/unknown length must still be validated
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid/unsupported removal body %q: %d", body, rec.Code)
		}
	}
}

type apiMockDownloader struct {
	url  string
	body []byte
}

func (d apiMockDownloader) DownloadFile(_ context.Context, r firc.DownloadFileRequest) (firc.DownloadFileResult, error) {
	if r.URL != d.url {
		return firc.DownloadFileResult{}, errors.New("unexpected url")
	}
	if err := os.WriteFile(r.DestPath, d.body, r.Mode); err != nil {
		return firc.DownloadFileResult{}, err
	}
	return firc.DownloadFileResult{Path: r.DestPath, Size: int64(len(d.body))}, nil
}

type apiMockPkgMgr struct {
	mu         sync.Mutex
	root       string
	runtimeVer string
	targetVer  string
}

func (m *apiMockPkgMgr) Version(_ context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if name == firc.RuntimePackageName {
		return m.runtimeVer, nil
	}
	return "", nil
}

func (m *apiMockPkgMgr) Install(_ context.Context, ipkPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := os.ReadFile(ipkPath)
	if err != nil {
		return err
	}
	outerGz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer outerGz.Close()
	outerTar := tar.NewReader(outerGz)
	var dataTarGz, ctrlTarGz []byte
	for {
		hdr, err := outerTar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch strings.TrimPrefix(hdr.Name, "./") {
		case "data.tar.gz":
			dataTarGz, err = io.ReadAll(outerTar)
			if err != nil {
				return err
			}
		case "control.tar.gz":
			ctrlTarGz, err = io.ReadAll(outerTar)
			if err != nil {
				return err
			}
		}
	}
	ctrlVer := ""
	if len(ctrlTarGz) > 0 {
		if cg, cErr := gzip.NewReader(bytes.NewReader(ctrlTarGz)); cErr == nil {
			ct := tar.NewReader(cg)
			for {
				ch, nErr := ct.Next()
				if nErr != nil {
					break
				}
				if strings.TrimPrefix(ch.Name, "./") == "control" {
					if cb, rErr := io.ReadAll(ct); rErr == nil {
						for _, line := range strings.Split(string(cb), "\n") {
							if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Version" {
								ctrlVer = strings.TrimSpace(v)
							}
						}
					}
					break
				}
			}
			_ = cg.Close()
		}
	}
	innerGz, err := gzip.NewReader(bytes.NewReader(dataTarGz))
	if err != nil {
		return err
	}
	defer innerGz.Close()
	innerTar := tar.NewReader(innerGz)
	for {
		hdr, err := innerTar.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(hdr.Name, "./"), "/")
		if rel == "" || rel == "." || hdr.Typeflag == tar.TypeDir {
			continue
		}
		if rel == "opt/etc/firc/firc.conf" || rel == "opt/etc/firc/groups.yaml" {
			if _, statErr := os.Lstat(filepath.Join(m.root, filepath.FromSlash(rel))); statErr == nil {
				continue
			}
		}
		dst := filepath.Join(m.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		if hdr.Typeflag == tar.TypeSymlink {
			if err := os.Symlink(hdr.Linkname, dst); err != nil {
				return err
			}
			continue
		}
		data, err := io.ReadAll(innerTar)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, os.FileMode(hdr.Mode)&0777); err != nil {
			return err
		}
	}
	for _, name := range []string{"firc.conf", "groups.yaml"} {
		dstCfg := filepath.Join(m.root, "opt", "etc", "firc", name)
		if _, err := os.Lstat(dstCfg); os.IsNotExist(err) {
			defPath := filepath.Join(m.root, "opt", "share", "awg-firc-runtime", "defaults", name)
			if defData, readErr := os.ReadFile(defPath); readErr == nil {
				if mkErr := os.MkdirAll(filepath.Dir(dstCfg), 0700); mkErr != nil {
					return mkErr
				}
				if wErr := os.WriteFile(dstCfg, defData, 0600); wErr != nil {
					return wErr
				}
			}
		}
	}
	ownList := filepath.Join(m.root, "opt", "lib", "opkg", "info", firc.RuntimePackageName+".list")
	if err := os.MkdirAll(filepath.Dir(ownList), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(ownList, []byte("/opt/sbin/fircd\n/opt/bin/fircd\n"), 0644); err != nil {
		return err
	}
	if ctrlVer != "" {
		m.runtimeVer = ctrlVer
	} else {
		m.runtimeVer = m.targetVer
	}
	return nil
}

func (m *apiMockPkgMgr) Remove(_ context.Context, _ string) error { return nil }

func apiMinimalMIPS32ELF(tag string) []byte {
	buf := make([]byte, 52+len(tag))
	copy(buf[0:4], []byte{0x7f, 'E', 'L', 'F'})
	buf[4] = byte(elf.ELFCLASS32)
	buf[5] = byte(elf.ELFDATA2LSB)
	buf[6] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(buf[16:18], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(buf[18:20], uint16(elf.EM_MIPS))
	binary.LittleEndian.PutUint32(buf[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(buf[40:42], 52)
	copy(buf[52:], []byte(tag))
	return buf
}

func TestFircHandler_UnselectedAndStoppedNeverStartsDaemonOrMutatesNetfilter(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	sbDir := filepath.Join(tmpDir, "singbox")
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}

	sockDir, err := os.MkdirTemp("", "firc-u-")
	if err != nil {
		t.Fatalf("mkdtemp sockDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load settings store: %v", err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	}); err != nil {
		t.Fatalf("set routing engine sing-box: %v", err)
	}

	var ipcHits atomic.Int32
	var putSettingsHits atomic.Int32
	fakeFircMux := http.NewServeMux()
	fakeFircMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, r *http.Request) {
		ipcHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"chains":["FIRC_DNS"]}`))
	})
	fakeFircMux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		ipcHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"groups":[]}`))
	})
	fakeFircMux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, r *http.Request) {
		ipcHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tunnels":[]}`))
	})
	fakeFircMux.HandleFunc("/api/v1/system/autolist", func(w http.ResponseWriter, r *http.Request) {
		ipcHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"candidates":[]}`))
	})
	fakeFircMux.HandleFunc("/api/v1/system/settings", func(w http.ResponseWriter, r *http.Request) {
		ipcHits.Add(1)
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			putSettingsHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"settings":{"app.autolist.failThreshold":5,"app.autolist.failTime":30,"app.autolist.retransThreshold":5,"app.dnsProxy.unmatchedTtl":0}}`))
	})
	fakeFircMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		ipcHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	var sockMu sync.Mutex
	var activeStopSock func()
	stopActiveSock := func() {
		sockMu.Lock()
		fn := activeStopSock
		activeStopSock = nil
		sockMu.Unlock()
		if fn != nil {
			fn()
		}
	}

	startUnixServer := func() func() {
		stopActiveSock()
		_ = os.Remove(sockPath)
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen unix %s: %v", sockPath, err)
		}
		srv := &http.Server{Handler: fakeFircMux}
		go func() { _ = srv.Serve(ln) }()
		var once sync.Once
		closeFn := func() {
			once.Do(func() {
				_ = srv.Close()
				_ = ln.Close()
				_ = os.Remove(sockPath)
			})
		}
		sockMu.Lock()
		activeStopSock = closeFn
		sockMu.Unlock()
		return closeFn
	}

	stopSock := startUnixServer()
	t.Cleanup(stopSock)

	var iptablesCalls atomic.Int32
	var poolRejectChecks atomic.Int32
	var poolRejectDeletes atomic.Int32
	firc.SetExtIptablesRunner(func(ctx context.Context, args ...string) ([]byte, error) {
		iptablesCalls.Add(1)
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "FIRC_POOLREJECT") {
			if strings.Contains(joined, "-S") || strings.Contains(joined, "-L") {
				poolRejectChecks.Add(1)
			}
			if strings.Contains(joined, "-D") {
				poolRejectDeletes.Add(1)
			}
		}
		return []byte{}, nil
	})
	t.Cleanup(func() { firc.SetExtIptablesRunner(nil) })

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		SocketPath:  sockPath,
		InboundPort: 1099,
	})
	var startCalls atomic.Int32
	var stopCalls atomic.Int32
	op.SetLifecycleHooks(
		func(ctx context.Context) error {
			startCalls.Add(1)
			return nil
		},
		func(ctx context.Context) error {
			stopCalls.Add(1)
			stopActiveSock()
			return nil
		},
	)

	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	h.SetSingboxConfigDir(sbDir)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	doReq := func(method, path, body string) *httptest.ResponseRecorder {
		var bodyReader *strings.Reader
		if body != "" {
			bodyReader = strings.NewReader(body)
		} else {
			bodyReader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, bodyReader)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	assertCountersZero := func(stage string) {
		t.Helper()
		if got := startCalls.Load(); got != 0 {
			t.Fatalf("%s: expected startCalls == 0, got %d", stage, got)
		}
		if got := ipcHits.Load(); got != 0 {
			t.Fatalf("%s: expected ipcHits == 0, got %d", stage, got)
		}
		if got := iptablesCalls.Load(); got != 0 {
			t.Fatalf("%s: expected iptablesCalls == 0, got %d", stage, got)
		}
	}

	// Case 1: RoutingEngine == "sing-box" with fake Unix socket OPEN.
	// All GET reads must succeed offline with zero daemon starts, zero IPC hits, zero iptables calls.
	unselectedReads := []string{
		"/api/firc/status",
		"/api/firc/tunnels",
		"/api/firc/tunnels/state",
		"/api/firc/groups",
		"/api/firc/events",
		"/api/firc/settings",
		"/api/firc/ext",
		"/api/firc/monitor",
		"/api/firc/autolist",
		"/api/firc/migrate/state",
		"/api/firc/v1/system/netfilter",
		"/api/firc/v1/tunnels",
		"/api/firc/v1/groups",
		"/api/firc/v1/system/autolist",
		"/api/firc/v1/system/settings",
	}
	for _, path := range unselectedReads {
		rec := doReq(http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("Case 1 GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
		assertCountersZero("Case 1 GET " + path)
	}

	// Offline disk stagings while unselected must return 200 and persist to disk without daemon/IPC/iptables.
	tunnelsPayload := `{"tunnels":[{"name":"tun0","type":"awg","interface":"awg0","enabled":true}]}`
	groupsPayload := `{"groups":[{"id":"default","name":"Default","tunnels":["tun0"]}]}`
	extCfg := firc.ExtConfig{
		ProxyGroups: []firc.ProxyGroup{
			{ID: "ext1", Name: "Ext1", Interfaces: []string{"awg0"}},
		},
		KeenDNSCloud: firc.KeenDNSCloudConfig{
			Enabled:      false,
			ProxyGroupID: "ext1",
		},
		ServerRouting: firc.ServerRoutingConfig{
			EnabledServers: []string{},
		},
	}
	extBytes, err := json.Marshal(extCfg)
	if err != nil {
		t.Fatalf("marshal extCfg: %v", err)
	}
	previewPayload := `{"type":"awg","config":"[Interface]\nPrivateKey = AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n"}`

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/firc/tunnels", tunnelsPayload},
		{http.MethodPut, "/api/firc/groups", groupsPayload},
		{http.MethodPut, "/api/firc/ext", string(extBytes)},
		{http.MethodPost, "/api/firc/tunnels/preview", previewPayload},
		{http.MethodPut, "/api/firc/v1/tunnels", tunnelsPayload},
		{http.MethodPut, "/api/firc/v1/groups", groupsPayload},
		{http.MethodPost, "/api/firc/v1/tunnels/preview", previewPayload},
		{http.MethodPost, "/api/firc/migrate/reverse?dry_run=1", `{}`},
	} {
		rec := doReq(tc.method, tc.path, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("Case 1 offline stage %s %s expected 200, got %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		assertCountersZero("Case 1 offline stage " + tc.method + " " + tc.path)
	}

	for _, fname := range []string{"tunnels.yaml", "groups.yaml", "awgm-ext.json"} {
		data, err := os.ReadFile(filepath.Join(fircDir, fname))
		if err != nil || len(bytes.TrimSpace(data)) == 0 {
			t.Fatalf("expected persisted %s on offline stage, err=%v len=%d", fname, err, len(data))
		}
	}

	// All live unselected mutations must return 409 FIRC_NOT_SELECTED with counters == 0.
	unselectedMutations := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/firc/control", `{"action":"start"}`},
		{http.MethodPost, "/api/firc/control", `{"action":"reload"}`},
		{http.MethodPost, "/api/firc/tunnels/probe", `{"name":"tun0"}`},
		{http.MethodPut, "/api/firc/settings", `{"failThreshold":5,"failTime":30,"retransThreshold":5}`},
		{http.MethodPost, "/api/firc/monitor/check", `{"domain":"example.com"}`},
		{http.MethodPut, "/api/firc/autolist/config", `{"enabled":true,"groupId":"default"}`},
		{http.MethodPost, "/api/firc/autolist/check", `{}`},
		{http.MethodPost, "/api/firc/autolist/probe", `{"host":"example.com"}`},
		{http.MethodPost, "/api/firc/autolist/candidate", `{"domain":"example.com","action":"dismiss"}`},
		{http.MethodPost, "/api/firc/autolist/candidate", `{"domain":"example.com","action":"add","groupId":"default","confirmed":true}`},
		{http.MethodPost, "/api/firc/autolist/address/dismiss", `{"address":"1.2.3.4","proto":"tcp","port":443}`},
		{http.MethodPost, "/api/firc/v1/system/restart", `{}`},
		{http.MethodPut, "/api/firc/v1/system/settings", `{"app.autolist.enabled":true}`},
		{http.MethodPost, "/api/firc/v1/tunnels/probe", `{"name":"tun0"}`},
	}
	for _, tc := range unselectedMutations {
		rec := doReq(tc.method, tc.path, tc.body)
		if rec.Code != http.StatusConflict {
			t.Fatalf("Case 1 live mutation %s %s expected 409, got %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "FIRC_NOT_SELECTED") {
			t.Fatalf("Case 1 live mutation %s %s expected FIRC_NOT_SELECTED, got: %s", tc.method, tc.path, rec.Body.String())
		}
		assertCountersZero("Case 1 live mutation " + tc.method + " " + tc.path)
	}

	// POST /api/firc/control {"action":"stop"} while unselected must succeed (200),
	// not start daemon, not hit IPC, and clean up FIRC_POOLREJECT.
	rec := doReq(http.MethodPost, "/api/firc/control", `{"action":"stop"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("Case 1 stop expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if startCalls.Load() != 0 || ipcHits.Load() != 0 {
		t.Fatalf("Case 1 stop mutated startCalls=%d ipcHits=%d", startCalls.Load(), ipcHits.Load())
	}
	if poolRejectChecks.Load()+poolRejectDeletes.Load() == 0 {
		t.Fatalf("Case 1 stop did not check/clean FIRC_POOLREJECT")
	}

	// Case 2: RoutingEngine == "firc", Unix socket CLOSED.
	stopSock()
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatalf("set routing engine firc: %v", err)
	}

	for _, path := range []string{
		"/api/firc/v1/system/netfilter",
		"/api/firc/v1/tunnels",
		"/api/firc/v1/groups",
	} {
		rec := doReq(http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("Case 2 offline GET %s expected 200, got %d: %s", path, rec.Code, rec.Body.String())
		}
		if startCalls.Load() != 0 {
			t.Fatalf("Case 2 offline GET %s triggered startCalls=%d", path, startCalls.Load())
		}
	}

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/firc/tunnels/probe", `{"name":"tun0"}`},
		{http.MethodPost, "/api/firc/monitor/check", `{"domain":"example.com"}`},
		{http.MethodPost, "/api/firc/v1/system/restart", `{}`},
		{http.MethodPost, "/api/firc/v1/tunnels/probe", `{"name":"tun0"}`},
	} {
		rec := doReq(tc.method, tc.path, tc.body)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("Case 2 live mutation %s %s expected 503, got %d: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "NOT_RUNNING") {
			t.Fatalf("Case 2 live mutation %s %s expected NOT_RUNNING, got: %s", tc.method, tc.path, rec.Body.String())
		}
		if startCalls.Load() != 0 {
			t.Fatalf("Case 2 live mutation %s %s triggered startCalls=%d", tc.method, tc.path, startCalls.Load())
		}
	}

	// Case 3: RoutingEngine == "firc", new fake Unix socket OPEN.
	// GET /api/firc/autolist must return 200 with zero hidden PUT /api/v1/system/settings writes.
	stopSock2 := startUnixServer()
	t.Cleanup(stopSock2)
	putSettingsHits.Store(0)

	rec = doReq(http.MethodGet, "/api/firc/autolist", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("Case 3 GET /api/firc/autolist expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := putSettingsHits.Load(); got != 0 {
		t.Fatalf("Case 3 GET /api/firc/autolist caused hidden PUT settings writes: %d", got)
	}
}

func TestFircTunnelProbeKeysAndProgressRoutes(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	sbDir := filepath.Join(tmpDir, "sing-box")
	if err := os.MkdirAll(fircDir, 0o755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}
	if err := os.MkdirAll(sbDir, 0o755); err != nil {
		t.Fatalf("mkdir sbDir: %v", err)
	}
	sockDir, err := os.MkdirTemp("", "awgm-firc-probe-")
	if err != nil {
		t.Fatalf("mkdtemp sockDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatalf("set engine firc: %v", err)
	}

	var probePostBody atomic.Value
	fakeMux := http.NewServeMux()
	fakeMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"running":true}`))
	})
	fakeMux.HandleFunc("/api/v1/tunnels/probe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"total":3,"nodes":[{"key":"src:N1","ok":true,"handshakeMs":11,"firstByteMs":22,"why":""}]}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		probePostBody.Store(string(b))
		_, _ = w.Write([]byte(`{"nodes":[{"key":"src:N1","ok":true,"handshakeMs":11,"firstByteMs":22,"why":""}]}`))
	})

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: fakeMux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		SocketPath:  sockPath,
		InboundPort: 1099,
	})
	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	h.SetSingboxConfigDir(sbDir)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	doReq := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// V1 reverse proxy must preserve keys alongside tunnel source normalization.
	v1Post := doReq(http.MethodPost, "/api/firc/v1/tunnels/probe", `{"tunnel":{"id":"t1","device":"tunvless0","sources":[{"id":"src1","kind":"link","link":"vless://11111111-1111-1111-1111-111111111111@1.1.1.1:443#N1"}]},"keys":["src:N1"]}`)
	if v1Post.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/v1/tunnels/probe expected 200, got %d: %s", v1Post.Code, v1Post.Body.String())
	}
	forwarded, _ := probePostBody.Load().(string)
	if !strings.Contains(forwarded, `"keys":["src:N1"]`) {
		t.Fatalf("v1 proxy dropped keys during normalization: %s", forwarded)
	}

	// Online GET /api/firc/v1/tunnels/probe and /api/firc/tunnels/probe return live progress.
	for _, p := range []string{"/api/firc/v1/tunnels/probe", "/api/firc/tunnels/probe"} {
		rec := doReq(http.MethodGet, p, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":3`) {
			t.Fatalf("online GET %s expected total:3, got %d: %s", p, rec.Code, rec.Body.String())
		}
	}

	// Wrapper {id, tunnel, keys} on POST /api/firc/tunnels/probe also forwards keys.
	probePostBody.Store("")
	wrapPost := doReq(http.MethodPost, "/api/firc/tunnels/probe", `{"id":"t1","keys":["src:N1"]}`)
	if wrapPost.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/tunnels/probe expected 200, got %d: %s", wrapPost.Code, wrapPost.Body.String())
	}
	forwarded2, _ := probePostBody.Load().(string)
	if !strings.Contains(forwarded2, `"keys":["src:N1"]`) || !strings.Contains(forwarded2, `"id":"t1"`) {
		t.Fatalf("handleProbeTunnel did not forward id/keys: %s", forwarded2)
	}

	// Offline fallback (socket closed) must return total:0, nodes:[] on both routes.
	_ = srv.Close()
	_ = ln.Close()
	_ = os.Remove(sockPath)
	for _, p := range []string{"/api/firc/v1/tunnels/probe", "/api/firc/tunnels/probe"} {
		rec := doReq(http.MethodGet, p, "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) || !strings.Contains(rec.Body.String(), `"nodes":[]`) {
			t.Fatalf("offline GET %s expected total:0 nodes:[], got %d: %s", p, rec.Code, rec.Body.String())
		}
	}
}

func TestFircHandler_FilterValidation(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(fircDir, 0755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}

	sockDir, err := os.MkdirTemp("", "firc-fv-")
	if err != nil {
		t.Fatalf("mkdtemp sockDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "sing-box"
		return nil
	}); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		SocketPath:  sockPath,
		InboundPort: 1099,
	})
	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	doReq := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// 1. Offline PUT /api/firc/tunnels and /api/firc/v1/tunnels reject lookaround / bad exclude regex
	badLookaround := `{"tunnels":[{"id":"t1","device":"tunvless0","enable":true,"filter":"^(?!RU).*$","sources":[{"id":"s1","kind":"subscription","url":"https://example.com/sub"}]}]}`
	rec := doReq(http.MethodPut, "/api/firc/tunnels", badLookaround)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lookahead/lookbehind") {
		t.Fatalf("PUT /api/firc/tunnels lookaround: expected 400 with hint, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doReq(http.MethodPut, "/api/firc/v1/tunnels", badLookaround)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lookahead/lookbehind") {
		t.Fatalf("PUT /api/firc/v1/tunnels lookaround: expected 400 with hint, got %d: %s", rec.Code, rec.Body.String())
	}

	badExclude := `{"tunnels":[{"id":"t1","device":"tunvless0","enable":true,"filterExclude":"[unclosed","sources":[{"id":"s1","kind":"subscription","url":"https://example.com/sub"}]}]}`
	rec = doReq(http.MethodPut, "/api/firc/tunnels", badExclude)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_FILTER") {
		t.Fatalf("PUT /api/firc/tunnels bad exclude: expected 400 INVALID_FILTER, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Valid filters with surrounding whitespace are trimmed and persisted offline
	validPayload := `{"tunnels":[{"id":"t1","device":"tunvless0","enable":true,"filter":"  (?i)(DE|NL)  ","filterExclude":"  (?i)RU  ","sources":[{"id":"s1","kind":"subscription","url":"https://example.com/sub"}]}]}`
	rec = doReq(http.MethodPut, "/api/firc/tunnels", validPayload)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/firc/tunnels valid: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	saved, err := op.LoadTunnelsFile()
	if err != nil || len(saved) != 1 || saved[0].Filter != "(?i)(DE|NL)" || saved[0].FilterExclude != "(?i)RU" {
		t.Fatalf("expected trimmed filters saved, got err=%v saved=%+v", err, saved)
	}

	// 3. Preview endpoints reject invalid regex even while offline
	rec = doReq(http.MethodPost, "/api/firc/tunnels/preview", `{"id":"t1","filter":"(?!RU)"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lookahead/lookbehind") {
		t.Fatalf("POST /api/firc/tunnels/preview lookaround: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doReq(http.MethodPost, "/api/firc/v1/tunnels/preview", `{"id":"t1","filterExclude":"`+strings.Repeat("x", firc.MaxTunnelFilterBytes+1)+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/firc/v1/tunnels/preview overlong: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doReq(http.MethodPost, "/api/firc/v1/tunnels/preview", `{"id":"t1","filter":"  (?i)DE  ","filterExclude":" (?i)RU "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/v1/tunnels/preview valid offline: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// 4. Live fircd returning unknown key "filterExclude" yields 400 instead of 500
	if err := op.SaveTunnelsFile([]firc.Tunnel{}); err != nil {
		t.Fatalf("reset tunnels.yaml: %v", err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatalf("switch engine firc: %v", err)
	}
	fakeMux := http.NewServeMux()
	fakeMux.HandleFunc("/api/v1/system/netfilter", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"active":true,"chains":["FIRC_DNS"]}`))
	})
	fakeMux.HandleFunc("/api/v1/tunnels", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			if strings.Contains(string(raw), "filterExclude") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"unknown key \"filterExclude\""}`))
				return
			}
			_, _ = w.Write([]byte(`{"tunnels":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"tunnels":[]}`))
	})
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: fakeMux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})

	rec = doReq(http.MethodPut, "/api/firc/tunnels", validPayload)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "filterExclude") {
		t.Fatalf("PUT /api/firc/tunnels stale fircd: expected 400 explaining filterExclude, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFircHandler_InstallAllowedWhileActiveWhenMissingOrBroken(t *testing.T) {
	source := t.TempDir()
	t.Setenv("FIRC_SOURCE_ROOT", source)
	for rel, content := range map[string]string{
		"LICENSE":                             "GPL-3.0-or-later\n",
		"files/entware_kn/etc/init.d/S99firc": "#!/bin/sh\n[ -e /opt/etc/firc/.awgm-disabled ] && exit 8\nexit 0\n",
		"files/entware_kn/etc/ndm/netfilter.d/100-firc": "#!/bin/sh\nexit 0\n",
		"files/common/etc/firc/firc.conf":               "configVersion: 0.7.0\n",
		"files/common/etc/firc/groups.yaml":             "groups: []\n",
	} {
		p := filepath.Join(source, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	fircdBin := filepath.Join(source, "fircd")
	tunBin := filepath.Join(source, "tunvless")
	if err := os.WriteFile(fircdBin, apiMinimalMIPS32ELF("fircd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tunBin, apiMinimalMIPS32ELF("tunvless"), 0755); err != nil {
		t.Fatal(err)
	}
	ipkPath := filepath.Join(source, "runtime.ipk")
	if err := firc.BuildRuntimeIPK(source, fircdBin, tunBin, "mipsel-3.4", "0.8.0-awgm.4", ipkPath); err != nil {
		t.Fatal(err)
	}
	ipkBytes, err := os.ReadFile(ipkPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(ipkBytes)
	ipkSHA := hex.EncodeToString(sum[:])

	root := t.TempDir()
	pm := &apiMockPkgMgr{root: root, targetVer: "0.8.0-awgm.4"}
	inst := firc.NewOnDemandInstaller("mipsel-3.4", firc.FircBinarySpec{
		Version:       "0.8.0-awgm.4",
		PackageURL:    "https://example.invalid/runtime.ipk",
		PackageSHA256: ipkSHA,
		PackageSize:   int64(len(ipkBytes)),
	})
	inst.SetPackageLayout(firc.DefaultPackageLayout(root))
	inst.SetPackageManager(pm)
	inst.SetDownloader(apiMockDownloader{url: "https://example.invalid/runtime.ipk", body: ipkBytes})
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })

	var running atomic.Bool
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })

	store := storage.NewSettingsStore(t.TempDir())
	_, _ = store.Load()
	_ = store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	})
	inst.SetEngineActiveChecker(func() bool {
		cfg, _ := store.Get()
		return cfg.SingboxRouter.RoutingEngine == "firc"
	})

	op := firc.NewOperator(firc.Options{ConfigDir: filepath.Join(root, "opt", "etc", "firc")})
	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	h.SetInstaller(inst)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	waitNotBusy := func(t *testing.T) firc.InstallStatus {
		t.Helper()
		for range 100 {
			recSt := httptest.NewRecorder()
			mux.ServeHTTP(recSt, httptest.NewRequest(http.MethodGet, "/api/firc/install-status", nil))
			var env struct {
				Data firc.InstallStatus `json:"data"`
			}
			if json.Unmarshal(recSt.Body.Bytes(), &env) == nil && !env.Data.Busy {
				return env.Data
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatal("installer remained busy")
		return firc.InstallStatus{}
	}

	// 1. Active engine + stopped + not_installed -> 202 install_started.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/install", nil))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "install_started") {
		t.Fatalf("expected 202 install_started when active+not_installed, got %d (%s)", rec.Code, rec.Body.String())
	}
	st := waitNotBusy(t)
	if st.State != firc.InstallStateInstalled || st.InstalledVersion != "0.8.0-awgm.4" {
		t.Fatalf("expected installed state after install, got %+v", st)
	}

	// 2. Active engine + cleanly installed (stopped) -> 409 FIRC_ACTIVE.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/install", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_ACTIVE") {
		t.Fatalf("expected 409 FIRC_ACTIVE when active+cleanly installed, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 3. Active engine + running -> 409 FIRC_RUNNING.
	running.Store(true)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/install", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_RUNNING") {
		t.Fatalf("expected 409 FIRC_RUNNING when active+running, got %d (%s)", rec.Code, rec.Body.String())
	}
	running.Store(false)

	// 4. Active engine + stopped + recovery_required (missing fircd) -> 202 install_started repairs cleanly.
	if err := os.Remove(filepath.Join(root, "opt", "sbin", "fircd")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/install", nil))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "install_started") {
		t.Fatalf("expected 202 install_started when active+recovery_required, got %d (%s)", rec.Code, rec.Body.String())
	}
	st = waitNotBusy(t)
	if st.State != firc.InstallStateInstalled {
		t.Fatalf("expected installed state after repair, got %+v", st)
	}
}

func TestFircV1Proxy_RouteAllowlistAndCanonicalPath(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	if err := os.MkdirAll(fircDir, 0o755); err != nil {
		t.Fatalf("mkdir fircDir: %v", err)
	}
	sockDir, err := os.MkdirTemp("", "awgm-firc-v1allow-")
	if err != nil {
		t.Fatalf("mkdtemp sockDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")

	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatalf("set engine firc: %v", err)
	}

	var mu sync.Mutex
	var upstreamPaths []string
	var healthHits atomic.Int32

	fakeMux := http.NewServeMux()
	fakeMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/netfilter" && r.Method == http.MethodGet && r.Header.Get("X-Test-Client") == "" {
			healthHits.Add(1)
		} else {
			mu.Lock()
			upstreamPaths = append(upstreamPaths, r.Method+" "+r.URL.Path)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"running":true}`))
	})

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: fakeMux}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		_ = ln.Close()
		_ = os.Remove(sockPath)
	}()

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		SocketPath:  sockPath,
		InboundPort: 1099,
	})
	h := NewFircHandler(op)
	h.SetSettingsStore(store)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	resetHits := func() {
		mu.Lock()
		upstreamPaths = nil
		mu.Unlock()
	}
	snapshotHits := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(upstreamPaths))
		copy(out, upstreamPaths)
		return out
	}

	// 1. Disallowed internal routes and disallowed methods must be rejected with 403 and 0 upstream calls.
	blockedCases := []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/firc/v1/system/hooks/netfilterd"},
		{http.MethodPost, "/api/firc/v1/system/hooks/iflayer"},
		{http.MethodPost, "/api/firc/v1/system/update/install"},
		{http.MethodGet, "/api/firc/v1/system/update"},
		{http.MethodPost, "/api/firc/v1/groups"},
		{http.MethodDelete, "/api/firc/v1/groups/g1"},
		{http.MethodPost, "/api/firc/v1/system/config/save"},
		{http.MethodPost, "/api/firc/v1/system/autolist/candidates/a/b/add"},
		{http.MethodDelete, "/api/firc/v1/tunnels"},
		{http.MethodGet, "/api/firc/v1/tunnels/t1/restart"},
	}
	for _, tc := range blockedCases {
		resetHits()
		req := httptest.NewRequest(tc.method, tc.target, nil)
		req.Header.Set("X-Test-Client", "1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s: expected 403, got %d (%s)", tc.method, tc.target, rec.Code, rec.Body.String())
		}
		if hits := snapshotHits(); len(hits) != 0 {
			t.Fatalf("%s %s: expected 0 upstream hits, got %v", tc.method, tc.target, hits)
		}
	}

	// 2. Traversal and encoded-slash paths must be rejected with 400 and 0 upstream calls.
	resetHits()
	reqTraversalDirect := httptest.NewRequest(http.MethodPost, "/api/firc/v1/tunnels/../system/hooks/netfilterd", nil)
	reqTraversalDirect.Header.Set("X-Test-Client", "1")
	recDirect := httptest.NewRecorder()
	h.handleV1Proxy(recDirect, reqTraversalDirect)
	if recDirect.Code != http.StatusBadRequest {
		t.Fatalf("direct traversal: expected 400, got %d (%s)", recDirect.Code, recDirect.Body.String())
	}
	if hits := snapshotHits(); len(hits) != 0 {
		t.Fatalf("direct traversal leaked upstream hits: %v", hits)
	}

	for _, viaMux := range []bool{false, true} {
		resetHits()
		reqEnc := httptest.NewRequest(http.MethodPost, "/api/firc/v1/tunnels%2f..%2fsystem/hooks/netfilterd", nil)
		reqEnc.Header.Set("X-Test-Client", "1")
		recEnc := httptest.NewRecorder()
		if viaMux {
			mux.ServeHTTP(recEnc, reqEnc)
		} else {
			h.handleV1Proxy(recEnc, reqEnc)
		}
		if recEnc.Code != http.StatusBadRequest {
			t.Fatalf("encoded slash traversal (viaMux=%v): expected 400, got %d (%s)", viaMux, recEnc.Code, recEnc.Body.String())
		}
		if hits := snapshotHits(); len(hits) != 0 {
			t.Fatalf("encoded slash traversal leaked upstream hits (viaMux=%v): %v", viaMux, hits)
		}
	}

	// 3. Allowed routes must forward with canonical clean path.
	allowedCases := []struct {
		method   string
		target   string
		wantRecv string
	}{
		{http.MethodGet, "/api/firc/v1/tunnels/", "GET /api/v1/tunnels"},
		{http.MethodPost, "/api/firc/v1/tunnels/t1/restart", "POST /api/v1/tunnels/t1/restart"},
		{http.MethodPost, "/api/firc/v1/groups/g1/move-up", "POST /api/v1/groups/g1/move-up"},
		{http.MethodGet, "/api/firc/v1/groups/g1/list/rules", "GET /api/v1/groups/g1/list/rules"},
		{http.MethodPatch, "/api/firc/v1/groups/g1/list/rules", "PATCH /api/v1/groups/g1/list/rules"},
		{http.MethodPost, "/api/firc/v1/system/capture", "POST /api/v1/system/capture"},
		{http.MethodDelete, "/api/firc/v1/system/capture", "DELETE /api/v1/system/capture"},
	}
	for _, tc := range allowedCases {
		resetHits()
		req := httptest.NewRequest(tc.method, tc.target, nil)
		req.Header.Set("X-Test-Client", "1")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: expected 200, got %d (%s)", tc.method, tc.target, rec.Code, rec.Body.String())
		}
		hits := snapshotHits()
		if len(hits) != 1 || hits[0] != tc.wantRecv {
			t.Fatalf("%s %s: expected upstream [%q], got %v", tc.method, tc.target, tc.wantRecv, hits)
		}
	}

	// /auth must return 200 locally without calling upstream.
	resetHits()
	reqAuth := httptest.NewRequest(http.MethodGet, "/api/firc/v1/auth", nil)
	reqAuth.Header.Set("X-Test-Client", "1")
	recAuth := httptest.NewRecorder()
	mux.ServeHTTP(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK || !strings.Contains(recAuth.Body.String(), `"authenticated":true`) {
		t.Fatalf("GET /api/firc/v1/auth expected 200 authenticated, got %d (%s)", recAuth.Code, recAuth.Body.String())
	}
	if hits := snapshotHits(); len(hits) != 0 {
		t.Fatalf("GET /api/firc/v1/auth should not hit upstream, got %v", hits)
	}
}

func TestFircHandler_UpgradeRuntime_ActiveAndPreconditions(t *testing.T) {
	buildIPK := func(ver string) (firc.FircBinarySpec, []byte) {
		source := t.TempDir()
		t.Setenv("FIRC_SOURCE_ROOT", source)
		for rel, content := range map[string]string{
			"LICENSE":                             "GPL-3.0-or-later\n",
			"files/entware_kn/etc/init.d/S99firc": "#!/bin/sh\n[ -e /opt/etc/firc/.awgm-disabled ] && exit 8\nexit 0\n",
			"files/entware_kn/etc/ndm/netfilter.d/100-firc": "#!/bin/sh\nexit 0\n",
			"files/common/etc/firc/firc.conf":               "configVersion: 0.7.0\n",
			"files/common/etc/firc/groups.yaml":             "groups: []\n",
		} {
			p := filepath.Join(source, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0755); err != nil {
				t.Fatal(err)
			}
		}
		fircdBin := filepath.Join(source, "fircd")
		tunBin := filepath.Join(source, "tunvless")
		if err := os.WriteFile(fircdBin, apiMinimalMIPS32ELF("fircd-"+ver), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tunBin, apiMinimalMIPS32ELF("tunvless-"+ver), 0755); err != nil {
			t.Fatal(err)
		}
		ipkPath := filepath.Join(source, "runtime-"+ver+".ipk")
		if err := firc.BuildRuntimeIPK(source, fircdBin, tunBin, "mipsel-3.4", ver, ipkPath); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(ipkPath)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		return firc.FircBinarySpec{
			Version:       ver,
			PackageURL:    "https://example.invalid/runtime-" + ver + ".ipk",
			PackageSHA256: hex.EncodeToString(sum[:]),
			PackageSize:   int64(len(b)),
		}, b
	}

	specV1, bytesV1 := buildIPK("0.8.0-awgm.4")
	specV2, bytesV2 := buildIPK("0.8.0-awgm.5")

	root := t.TempDir()
	pm := &apiMockPkgMgr{root: root, targetVer: "0.8.0-awgm.4"}
	store := storage.NewSettingsStore(t.TempDir())
	_, _ = store.Load()
	_ = store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	})

	op := firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(root, "opt", "etc", "firc"),
		FircdBinary: filepath.Join(root, "opt", "sbin", "fircd"),
		TunvlessBin: filepath.Join(root, "opt", "sbin", "tunvless"),
		InitScript:  filepath.Join(root, "opt", "etc", "init.d", "S99firc"),
	})

	var running atomic.Bool
	op.SetLifecycleHooks(
		func(context.Context) error { running.Store(true); return nil },
		func(context.Context) error { running.Store(false); return nil },
	)

	makeHandler := func(spec firc.FircBinarySpec, ipkBytes []byte) (*http.ServeMux, *firc.OnDemandInstaller) {
		inst := firc.NewOnDemandInstaller("mipsel-3.4", spec)
		inst.SetPackageLayout(firc.DefaultPackageLayout(root))
		inst.SetPackageManager(pm)
		inst.SetDownloader(apiMockDownloader{url: spec.PackageURL, body: ipkBytes})
		inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
		inst.SetEngineActiveChecker(func() bool {
			cfg, _ := store.Get()
			return cfg.SingboxRouter.RoutingEngine == "firc"
		})
		inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
		inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

		h := NewFircHandler(op)
		h.SetSettingsStore(store)
		h.SetInstaller(inst)
		mux := http.NewServeMux()
		h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
		return mux, inst
	}

	waitDone := func(t *testing.T, mux *http.ServeMux) firc.InstallStatus {
		t.Helper()
		for range 120 {
			recSt := httptest.NewRecorder()
			mux.ServeHTTP(recSt, httptest.NewRequest(http.MethodGet, "/api/firc/install-status", nil))
			var env struct {
				Data firc.InstallStatus `json:"data"`
			}
			if json.Unmarshal(recSt.Body.Bytes(), &env) == nil && !env.Data.Busy {
				return env.Data
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatal("installer remained busy")
		return firc.InstallStatus{}
	}

	// 1. Not installed -> POST /api/firc/upgrade returns 409 RUNTIME_NOT_INSTALLED.
	muxV1, instV1 := makeHandler(specV1, bytesV1)
	rec := httptest.NewRecorder()
	muxV1.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/upgrade", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "RUNTIME_NOT_INSTALLED") {
		t.Fatalf("expected 409 RUNTIME_NOT_INSTALLED, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Seed v1 while active+stopped, then mark it running.
	if err := instV1.Install(context.Background()); err != nil {
		t.Fatalf("seed v1 install: %v", err)
	}
	running.Store(true)

	// 2. Same version -> 409 ALREADY_UP_TO_DATE.
	rec = httptest.NewRecorder()
	muxV1.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/upgrade", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "ALREADY_UP_TO_DATE") {
		t.Fatalf("expected 409 ALREADY_UP_TO_DATE, got %d (%s)", rec.Code, rec.Body.String())
	}

	// 3. Recovery required (missing binary) -> 409 RECOVERY_REQUIRED.
	muxV2, _ := makeHandler(specV2, bytesV2)
	if err := os.Remove(filepath.Join(root, "opt", "sbin", "fircd")); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	muxV2.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/upgrade", nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "RECOVERY_REQUIRED") {
		t.Fatalf("expected 409 RECOVERY_REQUIRED, got %d (%s)", rec.Code, rec.Body.String())
	}
	// Repair back to v1 cleanly.
	running.Store(false)
	if err := instV1.Install(context.Background()); err != nil {
		t.Fatalf("repair v1: %v", err)
	}
	running.Store(true)

	// 4. install-status exposes both versions + updateAvailable=true, and POST /api/firc/upgrade upgrades active running runtime cleanly.
	stRec := httptest.NewRecorder()
	muxV2.ServeHTTP(stRec, httptest.NewRequest(http.MethodGet, "/api/firc/install-status", nil))
	if stRec.Code != http.StatusOK || !strings.Contains(stRec.Body.String(), `"updateAvailable":true`) {
		t.Fatalf("expected updateAvailable=true in status, got %d (%s)", stRec.Code, stRec.Body.String())
	}

	rec = httptest.NewRecorder()
	muxV2.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/upgrade", nil))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "upgrade_started") {
		t.Fatalf("expected 202 upgrade_started, got %d (%s)", rec.Code, rec.Body.String())
	}
	stAfter := waitDone(t, muxV2)
	if stAfter.State != firc.InstallStateRunning || stAfter.InstalledVersion != "0.8.0-awgm.5" || stAfter.UpdateAvailable {
		t.Fatalf("expected running 0.8.0-awgm.5 with updateAvailable=false, got %+v", stAfter)
	}

	// 5. Rollback over HTTP when upgraded daemon crashes on boot.
	specV3, bytesV3 := buildIPK("0.8.0-awgm.6")
	muxV3, _ := makeHandler(specV3, bytesV3)
	attempts := 0
	op.SetLifecycleHooks(
		func(context.Context) error {
			attempts++
			if attempts == 1 {
				return errors.New("v3 boot failure")
			}
			running.Store(true)
			return nil
		},
		func(context.Context) error { running.Store(false); return nil },
	)
	rec = httptest.NewRecorder()
	muxV3.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/upgrade", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 upgrade_started for v3, got %d (%s)", rec.Code, rec.Body.String())
	}
	stRollback := waitDone(t, muxV3)
	if stRollback.State != firc.InstallStateRunning || stRollback.InstalledVersion != "0.8.0-awgm.5" || !stRollback.UpdateAvailable || !strings.Contains(stRollback.Error, "v3 boot failure") {
		t.Fatalf("expected rolled back running 0.8.0-awgm.5 with error surfaced, got %+v", stRollback)
	}
}

// Списки клиентов монитора уходят только при ?clients=1; счётчики — всегда.
func TestFircHandler_MonitorClientsOnDemand(t *testing.T) {
	dir := t.TempDir()
	// Метку группы демон узнаёт из mangle-цепочки.
	iptables := "#!/bin/sh\ncase \"$*\" in *\"-L FIRC_tg \"*) echo '1 1 MARK all -- * * 0.0.0.0/0 198.18.5.0/24 MARK xset 0x40010000/0x40ff0000';; esac\n"
	if err := os.WriteFile(filepath.Join(dir, "iptables"), []byte(iptables), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	conntrack := filepath.Join(dir, "conntrack")
	line := "ipv4 2 tcp 6 300 ESTABLISHED src=192.168.1.50 dst=1.1.1.1 sport=1 dport=443 packets=1 bytes=10 src=1.1.1.1 dst=192.168.1.50 sport=443 dport=1 packets=1 bytes=20 [ASSURED] mark=1073807360 use=1\n"
	if err := os.WriteFile(conntrack, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "firc.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/groups" {
			_, _ = w.Write([]byte(`{"groups":[{"id":"tg","name":"Telegram","enable":true}]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	h := NewFircHandler(firc.NewOperator(firc.Options{ConfigDir: dir, SocketPath: sock, ConntrackPath: conntrack}))
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	get := func(path string) firc.MonitorSnapshot {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var out struct {
			Data firc.MonitorSnapshot `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("GET %s: code %d err %v: %s", path, rec.Code, err, rec.Body.String())
		}
		if out.Data.TotalClients != 1 || len(out.Data.Groups) != 1 || out.Data.Groups[0].ActiveClients != 1 {
			t.Fatalf("GET %s counters: %+v", path, out.Data)
		}
		return out.Data
	}

	if snap := get("/api/firc/monitor"); snap.Clients != nil || snap.Groups[0].Clients != nil || snap.Servers[0].Clients != nil {
		t.Fatalf("lists must be absent without ?clients=1: %+v", snap)
	}
	snap := get("/api/firc/monitor?clients=1")
	if len(snap.Clients) != 1 || snap.Clients[0].IP != "192.168.1.50" || len(snap.Groups[0].Clients) != 1 || len(snap.Servers[0].Clients) != 1 {
		t.Fatalf("lists missing with ?clients=1: %+v", snap)
	}
}

// Связи групп приходят вместе с группами по обоим путям и ложатся в ext-конфиг.
func TestHandlePutGroups_StoresGroupLinks(t *testing.T) {
	for _, path := range []string{"/api/firc/groups", "/api/firc/v1/groups?save=true"} {
		t.Run(path, func(t *testing.T) {
			store := storage.NewSettingsStore(t.TempDir())
			if err := store.Update(func(s *storage.Settings) error {
				s.SingboxRouter.RoutingEngine = "sing-box"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			op := firc.NewOperator(firc.Options{ConfigDir: t.TempDir()})
			if err := op.SaveExtConfig(firc.ExtConfig{ProxyGroups: []firc.ProxyGroup{
				{ID: "pg", Name: "PG", Interfaces: []string{"eth3", "tunvless0"}},
			}}); err != nil {
				t.Fatal(err)
			}
			h := NewFircHandler(op)
			h.SetSettingsStore(store)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, path, strings.NewReader(
				`{"groups":[{"id":"aa11bb22","name":"G","interface":["eth3","tunvless0"],"enable":true}],"groupLinks":{"aa11bb22":"pg"}}`)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if got := op.LoadExtConfig().GroupLinks; got["aa11bb22"] != "pg" {
				t.Fatalf("links = %v", got)
			}
		})
	}
}

func TestFircV1SystemInterfaces_NormalizesTunnelRoleAndFlagsAwgmProxies(t *testing.T) {
	runReq := func(t *testing.T, ownedFn func(context.Context) ([]string, error), mockJSON string) []map[string]any {
		t.Helper()
		dir := t.TempDir()
		sock := filepath.Join(dir, "firc.sock")
		l, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/v1/system/interfaces":
				_, _ = w.Write([]byte(mockJSON))
			case "/api/v1/tunnels/state":
				_, _ = w.Write([]byte(`{"tunnels":[]}`))
			default:
				_, _ = w.Write([]byte(`{}`))
			}
		})}
		go func() { _ = srv.Serve(l) }()
		t.Cleanup(func() { _ = srv.Close() })

		h := NewFircHandler(firc.NewOperator(firc.Options{ConfigDir: dir, SocketPath: sock}))
		if ownedFn != nil {
			h.SetOtherEngineIntegrationHooks(OtherEngineIntegrationHooks{
				ListOwnedProxies: ownedFn,
			})
		}
		mux := http.NewServeMux()
		h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/firc/v1/system/interfaces", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/firc/v1/system/interfaces: code=%d body=%s", rec.Code, rec.Body.String())
		}
		var parsed struct {
			Interfaces []map[string]any `json:"interfaces"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("unmarshal response: %v (body=%s)", err, rec.Body.String())
		}
		return parsed.Interfaces
	}

	payload := `{
		"interfaces": [
			{"id":"t2s0","name":"!Xray_Nuxt_t2s0","ndmsName":"Proxy0","role":"wan","up":true},
			{"id":"t2s1","name":"NL-Singbox","ndmsName":"Proxy1","role":"tunnel","up":false},
			{"id":"t2s2","name":"VOX [awg-manager:singbox:subscription:5090]","ndmsName":"Proxy2","role":"tunnel","up":true},
			{"id":"t2s4","name":"!Xray_RawTun","role":"tunnel","up":true},
			{"id":"nwg0","name":"HomeWG","role":"wan","up":true},
			{"id":"eth3","name":"Broadband","role":"wan","up":true}
		]
	}`

	findItem := func(items []map[string]any, id string) map[string]any {
		for _, it := range items {
			if it["id"] == id {
				return it
			}
		}
		t.Fatalf("interface %q not found in %+v", id, items)
		return nil
	}

	// Case 1: ListOwnedProxies positively identifies t2s1; t2s2 carries the singbox marker;
	// t2s0 (!Xray_Nuxt_t2s0) and raw kernel TUN t2s4 (!Xray_RawTun) stay selectable with role=tunnel.
	items := runReq(t, func(context.Context) ([]string, error) {
		return []string{"t2s1"}, nil
	}, payload)
	if it := findItem(items, "t2s0"); it["role"] != "tunnel" || it["awgmSingboxProxy"] == true {
		t.Fatalf("t2s0 expected role=tunnel without awgmSingboxProxy, got %+v", it)
	}
	if it := findItem(items, "t2s4"); it["role"] != "tunnel" || it["awgmSingboxProxy"] == true {
		t.Fatalf("t2s4 expected role=tunnel without awgmSingboxProxy, got %+v", it)
	}
	if it := findItem(items, "t2s1"); it["awgmSingboxProxy"] != true {
		t.Fatalf("t2s1 expected awgmSingboxProxy=true, got %+v", it)
	}
	if it := findItem(items, "t2s2"); it["awgmSingboxProxy"] != true {
		t.Fatalf("t2s2 expected awgmSingboxProxy=true, got %+v", it)
	}
	if it := findItem(items, "nwg0"); it["role"] != "tunnel" {
		t.Fatalf("nwg0 expected role=tunnel, got %+v", it)
	}
	if it := findItem(items, "eth3"); it["role"] != "wan" {
		t.Fatalf("eth3 expected role=wan, got %+v", it)
	}

	// Case 2: ListOwnedProxies unavailable/error -> orphaned [awg-manager:singbox: still flagged,
	// while !Xray t2s0 and raw t2s4 stay unflagged without false positives.
	fallbackItems := runReq(t, func(context.Context) ([]string, error) {
		return nil, errors.New("ndms unavailable")
	}, payload)
	if it := findItem(fallbackItems, "t2s0"); it["role"] != "tunnel" || it["awgmSingboxProxy"] == true {
		t.Fatalf("fallback t2s0 expected role=tunnel without awgmSingboxProxy, got %+v", it)
	}
	if it := findItem(fallbackItems, "t2s4"); it["role"] != "tunnel" || it["awgmSingboxProxy"] == true {
		t.Fatalf("fallback t2s4 expected role=tunnel without awgmSingboxProxy, got %+v", it)
	}
	if it := findItem(fallbackItems, "t2s2"); it["awgmSingboxProxy"] != true {
		t.Fatalf("fallback t2s2 expected awgmSingboxProxy=true, got %+v", it)
	}
}

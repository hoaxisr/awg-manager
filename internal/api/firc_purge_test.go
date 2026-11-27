package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func createExecutableDummy(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write dummy %s: %v", p, err)
	}
	return p
}

func setupPurgeTestHarness(t *testing.T, installFircBins bool) (
	h *FircHandler,
	mux *http.ServeMux,
	op *firc.Operator,
	settingsStore *storage.SettingsStore,
	sbDir string,
	fircAlive *atomic.Bool,
	sbRunning *atomic.Bool,
	purgeCalls *atomic.Int32,
	sbInstalled *atomic.Bool,
	eventsLog *[]string,
) {
	t.Helper()
	tmpDir := t.TempDir()
	awgmDir := filepath.Join(tmpDir, "awg-manager")
	sbDir = filepath.Join(awgmDir, "singbox", "config.d")
	fircDir := filepath.Join(tmpDir, "firc")
	varLibDir := filepath.Join(tmpDir, "var-lib-firc")
	for _, d := range []string{sbDir, fircDir, varLibDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

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
	if err := os.WriteFile(filepath.Join(sbDir, "00-base.json"), []byte(`{"log":{"level":"info"}}`), 0644); err != nil {
		t.Fatalf("write 00-base.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "subscriptions.json"), []byte(`[]`), 0644); err != nil {
		t.Fatalf("write subscriptions.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "subscription-groups.json"), []byte(`[]`), 0644); err != nil {
		t.Fatalf("write subscription-groups.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "awg3.json"), []byte(`[]`), 0644); err != nil {
		t.Fatalf("write awg3.json: %v", err)
	}

	settingsStore = storage.NewSettingsStore(awgmDir)
	if _, err := settingsStore.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}

	fircAlive = new(atomic.Bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fircAlive.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		switch {
		case r.URL.Path == "/api/v1/system/netfilter":
			_ = json.NewEncoder(w).Encode(firc.NetfilterHealth{Ok: true})
		case r.URL.Path == "/api/v1/tunnels" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"tunnels": []firc.Tunnel{}})
		case r.URL.Path == "/api/v1/tunnels" && r.Method == http.MethodPut:
			_ = json.NewEncoder(w).Encode(firc.PutTunnelsResponse{})
		case r.URL.Path == "/api/v1/groups" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []firc.Group{}})
		case r.URL.Path == "/api/v1/groups" && r.Method == http.MethodPut:
			_ = json.NewEncoder(w).Encode(map[string]any{"groups": []firc.Group{}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)

	fircBin := filepath.Join(tmpDir, "missing-bin", "fircd")
	tunBin := filepath.Join(tmpDir, "missing-bin", "tunvless")
	if installFircBins {
		binDir := filepath.Join(tmpDir, "bin")
		fircBin = createExecutableDummy(t, binDir, "fircd")
		tunBin = createExecutableDummy(t, binDir, "tunvless")
	}

	op = firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		FircdBinary: fircBin,
		TunvlessBin: tunBin,
		InboundPort: 1099,
	})
	op.SetVarLibDir(varLibDir)
	op.SetClient(firc.NewHTTPClient(srv.URL, srv.Client()))

	events := make([]string, 0, 8)
	eventsLog = &events

	op.SetLifecycleHooks(
		func(context.Context) error {
			fircAlive.Store(true)
			*eventsLog = append(*eventsLog, "startFirc")
			return nil
		},
		func(context.Context) error {
			fircAlive.Store(false)
			*eventsLog = append(*eventsLog, "stopFirc")
			return nil
		},
	)
	if err := op.StopByUser(context.Background()); err != nil {
		t.Fatalf("StopByUser: %v", err)
	}

	sbRunning = new(atomic.Bool)
	sbRunning.Store(true)
	sbInstalled = new(atomic.Bool)
	sbInstalled.Store(true)
	purgeCalls = new(atomic.Int32)

	h = NewFircHandler(op)
	h.SetSettingsStore(settingsStore)
	h.SetSingboxConfigDir(sbDir)
	h.SetOtherEngineLifecycle(
		func(_ context.Context, engine string) error {
			sbRunning.Store(true)
			*eventsLog = append(*eventsLog, "startOther:"+engine)
			return nil
		},
		func(_ context.Context, engine string) error {
			sbRunning.Store(false)
			*eventsLog = append(*eventsLog, "stopOther:"+engine)
			return nil
		},
	)
	h.SetOtherEngineIntegrationHooks(OtherEngineIntegrationHooks{
		IsSingboxRunning: func() bool {
			return sbRunning.Load()
		},
		IsSingboxInstalled: func() bool {
			return sbInstalled.Load()
		},
		GetSingboxCounts: func() (int, int) {
			return 1, 0
		},
		PurgeSingbox: func(context.Context) (int, int, error) {
			purgeCalls.Add(1)
			sbRunning.Store(false)
			sbInstalled.Store(false)
			*eventsLog = append(*eventsLog, "purgeSingbox")
			return 1, 1, nil
		},
		PostRestoreSync: func(_ context.Context, restoredSingbox, restoredSettings bool) error {
			*eventsLog = append(*eventsLog, "postRestoreSync")
			return nil
		},
		OnBeforePurgeFirc: func(context.Context) {
			*eventsLog = append(*eventsLog, "onBeforePurgeFirc")
		},
	})

	mux = http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return
}

func TestHandleMigrate_SwitchAndUninstall(t *testing.T) {
	// 1. Missing FIRC runtime aborts switch_and_uninstall with 409 FIRC_RUNTIME_NOT_INSTALLED
	// BEFORE writing tunnels.yaml or calling PurgeSingbox.
	tmpMissing := t.TempDir()
	sbMissing := filepath.Join(tmpMissing, "awg-manager", "singbox", "config.d")
	_ = os.MkdirAll(sbMissing, 0755)
	_ = os.WriteFile(filepath.Join(sbMissing, "10-tunnels.json"), []byte(`{"outbounds":[{"type":"vless","tag":"vless-test","server":"example.com","server_port":443,"uuid":"11111111-2222-3333-4444-555555555555"}]}`), 0644)
	opMissing := firc.NewOperator(firc.Options{
		ConfigDir:   filepath.Join(tmpMissing, "firc"),
		FircdBinary: filepath.Join(tmpMissing, "missing", "fircd"),
		TunvlessBin: filepath.Join(tmpMissing, "missing", "tunvless"),
		InboundPort: 1099,
	})
	var purgeMissing atomic.Int32
	hMissing := NewFircHandler(opMissing)
	hMissing.SetSingboxConfigDir(sbMissing)
	hMissing.SetOtherEngineIntegrationHooks(OtherEngineIntegrationHooks{
		PurgeSingbox: func(context.Context) (int, int, error) {
			purgeMissing.Add(1)
			return 0, 0, nil
		},
	})
	muxMissing := http.NewServeMux()
	hMissing.RegisterRoutes(muxMissing, func(next http.HandlerFunc) http.HandlerFunc { return next })

	reqBody := `{"postSingboxAction":"switch_and_uninstall","includeTunnels":true,"selectedTunnelIds":["vless-test"]}`
	rec := httptest.NewRecorder()
	muxMissing.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(reqBody))))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_RUNTIME_NOT_INSTALLED") {
		t.Fatalf("expected 409 FIRC_RUNTIME_NOT_INSTALLED, got %d body=%s", rec.Code, rec.Body.String())
	}
	if purgeMissing.Load() != 0 {
		t.Fatalf("PurgeSingbox called despite missing runtime")
	}
	if _, err := os.Stat(opMissing.TunnelsPath()); !os.IsNotExist(err) {
		t.Fatalf("tunnels.yaml written despite missing runtime")
	}

	// 2. With FIRC binaries installed, switch_and_uninstall takes the mandatory snapshot,
	// switches to firc, and purges sing-box + tunnels while preserving snapshots.
	_, mux, op, settingsStore, _, _, sbRunning, purgeCalls, _, _ := setupPurgeTestHarness(t, true)
	op.SetSingboxValidator(func(context.Context, string) error { return nil })
	reqBody = `{"postSingboxAction":"switch_and_uninstall","includeTunnels":true,"selectedTunnelIds":["vless-test"]}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(reqBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/firc/migrate switch_and_uninstall failed: %d %s", rec.Code, rec.Body.String())
	}
	res := decodeMigrateResp(t, rec.Body.Bytes())
	if !res.ConfigMigrated || !res.FircStarted || !res.EngineSwitched || res.PostActionApplied != "switch_and_uninstall" {
		t.Fatalf("unexpected migrate switch_and_uninstall result: %+v", res)
	}
	if res.SnapshotCreated == nil {
		t.Fatal("expected mandatory pre-purge snapshot")
	}
	if res.RemovedEngine == nil || res.RemovedEngine.Engine != "sing-box" || res.RemovedEngine.TunnelsRemoved != 1 || res.RemovedEngine.SubscriptionsRemoved != 1 {
		t.Fatalf("unexpected RemovedEngine metadata: %+v", res.RemovedEngine)
	}
	if purgeCalls.Load() != 1 || sbRunning.Load() {
		t.Fatalf("expected purgeCalls=1 and sing-box stopped, got purge=%d sbRunning=%v", purgeCalls.Load(), sbRunning.Load())
	}
	if st, _ := settingsStore.Get(); st.SingboxRouter.RoutingEngine != "firc" {
		t.Fatalf("expected active engine firc, got %s", st.SingboxRouter.RoutingEngine)
	}
	if snaps, err := op.ListSystemSnapshots(); err != nil || len(snaps) == 0 {
		t.Fatalf("expected preserved snapshot on disk, err=%v snaps=%+v", err, snaps)
	}
}

func TestHandlePurgeEngine_EndpointAndRestoreGuards(t *testing.T) {
	_, mux, op, settingsStore, _, fircAlive, sbRunning, purgeCalls, sbInstalled, events := setupPurgeTestHarness(t, true)
	op.SetSingboxValidator(func(context.Context, string) error { return nil })
	_ = os.WriteFile(op.TunnelsPath(), []byte("tunnels:\n  - id: ft1\n    device: tunvless0\n    enable: true\n"), 0644)
	_ = os.WriteFile(op.GroupsPath(), []byte("groups: []\n"), 0644)
	_ = os.WriteFile(filepath.Join(op.ConfigDir(), "firc.conf"), []byte("app:\n  autolist:\n    enabled: true\n"), 0644)

	// 1. Cannot purge currently active sing-box.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/engine/purge", bytes.NewReader([]byte(`{"engine":"sing-box"}`))))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CANNOT_PURGE_ACTIVE_ENGINE") {
		t.Fatalf("expected 409 CANNOT_PURGE_ACTIVE_ENGINE, got %d %s", rec.Code, rec.Body.String())
	}

	// 2. Purge inactive FIRC via /api/firc/engine/purge.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/engine/purge", bytes.NewReader([]byte(`{"engine":"firc"}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("purge inactive firc failed: %d %s", rec.Code, rec.Body.String())
	}
	var purgeEnv struct {
		Data firc.EnginePurgeResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &purgeEnv); err != nil {
		t.Fatalf("decode purge resp: %v", err)
	}
	if purgeEnv.Data.Engine != "firc" || purgeEnv.Data.TunnelsRemoved != 1 || purgeEnv.Data.SnapshotCreated == nil {
		t.Fatalf("unexpected purge result: %+v", purgeEnv.Data)
	}
	if purgeEnv.Data.SnapshotCreated.Trigger != "auto-pre-purge" {
		t.Fatalf("expected trigger auto-pre-purge, got %s", purgeEnv.Data.SnapshotCreated.Trigger)
	}

	// 3. Cannot purge active firc; CAN purge inactive sing-box when firc is active.
	sbSnap, err := op.CreateSystemSnapshot(filepath.Join(filepath.Dir(op.ConfigDir()), "awg-manager", "singbox", "config.d"), "sb-snap", "manual", "sing-box")
	if err != nil {
		t.Fatalf("CreateSystemSnapshot sing-box: %v", err)
	}
	_ = settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	})
	fircAlive.Store(true)
	sbRunning.Store(false)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/engine/purge", bytes.NewReader([]byte(`{"engine":"firc"}`))))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CANNOT_PURGE_ACTIVE_ENGINE") {
		t.Fatalf("expected 409 CANNOT_PURGE_ACTIVE_ENGINE for active firc, got %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/engine/purge", bytes.NewReader([]byte(`{"engine":"sing-box"}`))))
	if rec.Code != http.StatusOK || purgeCalls.Load() != 1 {
		t.Fatalf("expected 200 & purgeCalls=1 for inactive sing-box, got %d (%s) purgeCalls=%d", rec.Code, rec.Body.String(), purgeCalls.Load())
	}

	// 4. Snapshot restore preflight checks missing sing-box binary when switchEngine=true -> 409 SINGBOX_NOT_INSTALLED.
	restoreBody, _ := json.Marshal(map[string]any{"id": sbSnap.ID, "switchEngine": true})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader(restoreBody)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SINGBOX_NOT_INSTALLED") {
		t.Fatalf("expected 409 SINGBOX_NOT_INSTALLED on snapshot restore when sing-box absent, got %d %s", rec.Code, rec.Body.String())
	}

	// 5. After reinstalling sing-box, restoring sbSnap while current engine is "firc" stops "firc" (via preRestoreEngine),
	// calls PostRestoreSync, and starts "sing-box".
	sbInstalled.Store(true)
	*events = (*events)[:0]
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader(restoreBody)))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore sbSnap failed: %d %s", rec.Code, rec.Body.String())
	}
	var snapResp struct {
		Data firc.SnapshotMeta `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &snapResp); err != nil {
		t.Fatalf("decode restore meta: %v", err)
	}
	if snapResp.Data.PostActionApplied != "switch_to_sing-box" || snapResp.Data.PostActionError != "" {
		t.Fatalf("unexpected restore postAction: %+v", snapResp.Data)
	}
	if !sbRunning.Load() || fircAlive.Load() {
		t.Fatalf("expected sing-box running and firc stopped after restore; sb=%v firc=%v", sbRunning.Load(), fircAlive.Load())
	}
	sawStopFirc, sawPostSync, sawStartSb := false, false, false
	for _, ev := range *events {
		switch ev {
		case "stopFirc":
			sawStopFirc = true
		case "postRestoreSync":
			sawPostSync = true
		case "startOther:sing-box":
			sawStartSb = true
		}
	}
	if !sawStopFirc || !sawPostSync || !sawStartSb {
		t.Fatalf("expected stopFirc, postRestoreSync, and startOther:sing-box in events, got %v", *events)
	}

	// 6. Snapshot restore preflight checks missing FIRC runtime when activeEngine=="firc" -> 409 FIRC_RUNTIME_NOT_INSTALLED.
	fircSnap, err := op.CreateSystemSnapshot(filepath.Join(filepath.Dir(op.ConfigDir()), "awg-manager", "singbox", "config.d"), "firc-snap", "manual", "firc")
	if err != nil {
		t.Fatalf("CreateSystemSnapshot firc: %v", err)
	}
	opNoRuntime := firc.NewOperator(firc.Options{
		ConfigDir:   op.ConfigDir(),
		FircdBinary: filepath.Join(t.TempDir(), "missing", "fircd"),
		TunvlessBin: filepath.Join(t.TempDir(), "missing", "tunvless"),
		InboundPort: 1099,
	})
	hNoRuntime := NewFircHandler(opNoRuntime)
	muxNoRuntime := http.NewServeMux()
	hNoRuntime.RegisterRoutes(muxNoRuntime, func(next http.HandlerFunc) http.HandlerFunc { return next })
	fircRestoreBody, _ := json.Marshal(map[string]any{"id": fircSnap.ID, "switchEngine": true})
	rec = httptest.NewRecorder()
	muxNoRuntime.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader(fircRestoreBody)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "FIRC_RUNTIME_NOT_INSTALLED") {
		t.Fatalf("expected 409 FIRC_RUNTIME_NOT_INSTALLED on firc snapshot restore, got %d %s", rec.Code, rec.Body.String())
	}
}

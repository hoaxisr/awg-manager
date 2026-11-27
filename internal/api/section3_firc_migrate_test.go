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
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func setupSection3MigrateHandler(t *testing.T, fircFailStart error) (*FircHandler, *http.ServeMux, *firc.Operator, *storage.SettingsStore, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	sbDir := filepath.Join(tmpDir, "sing-box", "config.d")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sing-box config.d: %v", err)
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
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	settingsStore := storage.NewSettingsStore(tmpDir)
	if _, err := settingsStore.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}

	var fircAlive atomic.Bool
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

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		InboundPort: 1099,
	})
	op.SetClient(firc.NewHTTPClient(srv.URL, srv.Client()))
	op.SetLifecycleHooks(
		func(context.Context) error {
			if fircFailStart != nil {
				return fircFailStart
			}
			fircAlive.Store(true)
			return nil
		},
		func(context.Context) error {
			fircAlive.Store(false)
			return nil
		},
	)
	if err := op.StopByUser(context.Background()); err != nil {
		t.Fatalf("StopByUser: %v", err)
	}

	sbRunning := new(atomic.Bool)
	sbRunning.Store(true)

	h := NewFircHandler(op)
	h.SetSettingsStore(settingsStore)
	h.SetSingboxConfigDir(sbDir)
	h.SetOtherEngineLifecycle(
		func(context.Context, string) error {
			sbRunning.Store(true)
			return nil
		},
		func(context.Context, string) error {
			sbRunning.Store(false)
			return nil
		},
	)

	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })
	return h, mux, op, settingsStore, &fircAlive, sbRunning
}

func decodeMigrateResp(t *testing.T, body []byte) firc.MigrationResult {
	t.Helper()
	var env struct {
		Data firc.MigrationResult `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal migrate response (%s): %v", string(body), err)
	}
	return env.Data
}

func TestHandleMigrateFromDisabledSemantics(t *testing.T) {
	ctx := context.Background()

	// 1. keep_current must NOT clear .awgm-disabled or start fircd.
	_, mux, op, settingsStore, _, sbRunning := setupSection3MigrateHandler(t, nil)
	body := `{"postSingboxAction":"keep_current","includeTunnels":true,"selectedTunnelIds":["vless-test"]}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("keep_current status=%d body=%s", rec.Code, rec.Body.String())
	}
	resKeep := decodeMigrateResp(t, rec.Body.Bytes())
	if !resKeep.ConfigMigrated || resKeep.FircStarted || resKeep.EngineSwitched || resKeep.PostActionApplied != "keep_current" {
		t.Fatalf("unexpected keep_current result: %+v", resKeep)
	}
	if !op.ManuallyDisabled() {
		t.Fatal("keep_current unexpectedly cleared .awgm-disabled")
	}
	if op.IsRunning(ctx) {
		t.Fatal("keep_current unexpectedly started fircd")
	}
	if s, _ := settingsStore.Get(); s.SingboxRouter.RoutingEngine == "firc" || !sbRunning.Load() {
		t.Fatalf("keep_current changed active engine or stopped sing-box")
	}

	// 2. First migration with switch_and_stop from .awgm-disabled succeeds without prior manual Start.
	body = `{"postSingboxAction":"switch_and_stop","includeTunnels":true,"selectedTunnelIds":["vless-test"]}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("switch_and_stop status=%d body=%s", rec.Code, rec.Body.String())
	}
	resSw := decodeMigrateResp(t, rec.Body.Bytes())
	if !resSw.ConfigMigrated || !resSw.FircStarted || !resSw.EngineSwitched || resSw.PostActionApplied != "switch_and_stop" || resSw.PostActionError != "" {
		t.Fatalf("unexpected switch_and_stop result: %+v", resSw)
	}
	if op.ManuallyDisabled() {
		t.Fatal("expected .awgm-disabled cleared after switch_and_stop migration")
	}
	if !op.IsRunning(ctx) {
		t.Fatal("expected fircd running after switch_and_stop migration")
	}
	if s, _ := settingsStore.Get(); s.SingboxRouter.RoutingEngine != "firc" || sbRunning.Load() {
		t.Fatalf("expected RoutingEngine=firc and sing-box stopped; engine=%s sbRunning=%v", s.SingboxRouter.RoutingEngine, sbRunning.Load())
	}

	// 3. Failed start preserves .awgm-disabled and returns separate configMigrated/fircStarted/engineSwitched fields.
	_, muxFail, opFail, settingsFail, _, sbFailRunning := setupSection3MigrateHandler(t, errors.New("fircd failed to start"))
	rec = httptest.NewRecorder()
	muxFail.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("failed switch status=%d body=%s", rec.Code, rec.Body.String())
	}
	resFail := decodeMigrateResp(t, rec.Body.Bytes())
	if !resFail.ConfigMigrated || resFail.FircStarted || resFail.EngineSwitched || resFail.StartError == "" || resFail.SwitchError == "" || resFail.PostActionError == "" {
		t.Fatalf("expected separate start/switch error outcome, got: %+v", resFail)
	}
	if !opFail.ManuallyDisabled() {
		t.Fatal("expected .awgm-disabled preserved on failed migration switch")
	}
	if s, _ := settingsFail.Get(); s.SingboxRouter.RoutingEngine == "firc" || !sbFailRunning.Load() {
		t.Fatalf("expected previous sing-box engine kept on failed switch")
	}
}

func TestHandleMigrateMissingRuntimeFailsBeforeMutation(t *testing.T) {
	tmpDir := t.TempDir()
	fircDir := filepath.Join(tmpDir, "firc")
	sbDir := filepath.Join(tmpDir, "sing-box", "config.d")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatalf("mkdir sing-box config.d: %v", err)
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
	if err := os.WriteFile(filepath.Join(sbDir, "10-tunnels.json"), []byte(tunnelsJSON), 0644); err != nil {
		t.Fatalf("write 10-tunnels.json: %v", err)
	}

	settingsStore := storage.NewSettingsStore(tmpDir)
	if _, err := settingsStore.Load(); err != nil {
		t.Fatalf("load settings: %v", err)
	}

	op := firc.NewOperator(firc.Options{
		ConfigDir:   fircDir,
		FircdBinary: filepath.Join(tmpDir, "missing", "fircd"),
		TunvlessBin: filepath.Join(tmpDir, "missing", "tunvless"),
		InboundPort: 1099,
	})
	h := NewFircHandler(op)
	h.SetSettingsStore(settingsStore)
	h.SetSingboxConfigDir(sbDir)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux, func(next http.HandlerFunc) http.HandlerFunc { return next })

	// 1. switch_standby / switch_and_stop must abort BEFORE creating tunnels.yaml, groups.yaml, or snapshots.
	for _, action := range []string{"switch_standby", "switch_and_stop"} {
		body := `{"postSingboxAction":"` + action + `","includeTunnels":true,"selectedTunnelIds":["vless-test"]}`
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(body))))
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s expected 409 Conflict when firc runtime missing, got %d body=%s", action, rec.Code, rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("FIRC_RUNTIME_NOT_INSTALLED")) {
			t.Fatalf("%s expected FIRC_RUNTIME_NOT_INSTALLED code, got %s", action, rec.Body.String())
		}
		if _, err := os.Stat(op.TunnelsPath()); !os.IsNotExist(err) {
			t.Fatalf("%s mutated %s despite missing runtime", action, op.TunnelsPath())
		}
		if _, err := os.Stat(op.GroupsPath()); !os.IsNotExist(err) {
			t.Fatalf("%s mutated %s despite missing runtime", action, op.GroupsPath())
		}
		if snaps, err := op.ListSystemSnapshots(); err == nil && len(snaps) > 0 {
			t.Fatalf("%s created unexpected snapshots despite missing runtime: %+v", action, snaps)
		}
	}

	// 2. keep_current remains allowed to stage config offline without starting/switching FIRC.
	keepBody := `{"postSingboxAction":"keep_current","includeTunnels":true,"selectedTunnelIds":["vless-test"]}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/firc/migrate", bytes.NewReader([]byte(keepBody))))
	if rec.Code != http.StatusOK {
		t.Fatalf("keep_current expected 200 OK even without runtime, got %d body=%s", rec.Code, rec.Body.String())
	}
	res := decodeMigrateResp(t, rec.Body.Bytes())
	if !res.ConfigMigrated || res.FircStarted || res.EngineSwitched {
		t.Fatalf("unexpected keep_current result: %+v", res)
	}
}

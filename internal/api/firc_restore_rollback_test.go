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
	"testing"

	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestHandleRestoreSnapshot_RollbackUsesPreRestoreEngineFromContext(t *testing.T) {
	h, mux, op, settingsStore, _, sbRunning := setupSection3MigrateHandler(t, nil)
	op.SetSingboxValidator(func(context.Context, string) error { return nil })

	sbCfgDir := h.singboxConfigDir
	if err := os.MkdirAll(sbCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbCfgDir, "00-base.json"), []byte(`{"log":{"level":"info"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture snapshot while activeEngine is "sing-box".
	meta, err := op.CreateSystemSnapshot(sbCfgDir, "sb-snap", "manual", "sing-box")
	if err != nil {
		t.Fatalf("CreateSystemSnapshot: %v", err)
	}

	// Simulate active engine now being "firc" with user-disabled marker cleared.
	_ = os.Remove(filepath.Join(op.ConfigDir(), ".awgm-disabled"))
	if err := settingsStore.Update(func(s *storage.Settings) error {
		s.SingboxRouter.RoutingEngine = "firc"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sbRunning.Store(false)

	var captureSawEngine string
	var rollbackRestoredFirc bool
	h.SetOtherEngineRoutingHooks(
		func(ctx context.Context) (func(context.Context) error, error) {
			prevEngine := ""
			if loaded, err := settingsStore.Load(); err == nil && loaded != nil {
				prevEngine = loaded.SingboxRouter.RoutingEngine
			}
			if override, ok := firc.SwitchPreviousEngineFromContext(ctx); ok {
				prevEngine = override
			}
			captureSawEngine = prevEngine
			return func(context.Context) error {
				if prevEngine == "sing-box" {
					return errors.New("unexpected ResumeFromExternalEngine while firc active")
				}
				rollbackRestoredFirc = true
				return nil
			}, nil
		},
		func(context.Context, string) error { return nil },
		func(_ context.Context, target string) error {
			if target == "sing-box" {
				return errors.New("injected activate_target_sing-box failure")
			}
			return nil
		},
	)

	req := httptest.NewRequest(http.MethodPost, "/api/firc/snapshots/restore", bytes.NewReader([]byte(`{"id":"`+meta.ID+`","switchEngine":true}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			PostActionError  string `json:"postActionError"`
			RecoveryRequired bool   `json:"recoveryRequired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if env.Data.PostActionError == "" {
		t.Fatal("expected postActionError from injected activate failure")
	}
	if env.Data.RecoveryRequired {
		t.Fatalf("expected clean rollback without recoveryRequired, postActionError=%q", env.Data.PostActionError)
	}
	if captureSawEngine != "firc" {
		t.Fatalf("expected CaptureState to receive pre-restore engine firc via context, got %q", captureSawEngine)
	}
	if !rollbackRestoredFirc {
		t.Fatal("expected rollback closure to run the prevEngine=firc branch")
	}
	if loaded, err := settingsStore.Load(); err != nil || loaded.SingboxRouter.RoutingEngine != "firc" {
		t.Fatalf("expected settings rolled back to firc, got %+v err=%v", loaded, err)
	}
}

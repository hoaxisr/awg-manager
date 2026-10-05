package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestSyncMihomoAfterSingboxReload_IgnoresSingboxEngine(t *testing.T) {
	store := newDynamicEngineTestStore(t, "sing-box")
	mh := &fakeProxyEngine{}
	generateCalls := 0

	err := syncMihomoAfterSingboxReload(store, func() error {
		generateCalls++
		return nil
	}, mh)
	if err != nil {
		t.Fatalf("syncMihomoAfterSingboxReload() error = %v", err)
	}
	if generateCalls != 0 || mh.reloadCalls != 0 || mh.stopCalls != 0 {
		t.Fatalf("inactive Mihomo touched: generate=%d reload=%d stop=%d", generateCalls, mh.reloadCalls, mh.stopCalls)
	}
}

func TestSyncMihomoAfterSingboxReload_GeneratesThenReloadsOnce(t *testing.T) {
	store := newDynamicEngineTestStore(t, "mihomo")
	setRouterEnabled(t, store, true)
	mh := &fakeProxyEngine{}
	generateCalls := 0

	err := syncMihomoAfterSingboxReload(store, func() error {
		generateCalls++
		return nil
	}, mh)
	if err != nil {
		t.Fatalf("syncMihomoAfterSingboxReload() error = %v", err)
	}
	if generateCalls != 1 {
		t.Fatalf("generate calls = %d, want 1", generateCalls)
	}
	if mh.reloadCalls != 1 {
		t.Fatalf("reload calls = %d, want 1", mh.reloadCalls)
	}
	if mh.stopCalls != 0 {
		t.Fatalf("stop calls = %d, want 0", mh.stopCalls)
	}
}

func TestSyncMihomoAfterSingboxReload_GenerateFailureSkipsReload(t *testing.T) {
	store := newDynamicEngineTestStore(t, "mihomo")
	setRouterEnabled(t, store, true)
	mh := &fakeProxyEngine{}

	err := syncMihomoAfterSingboxReload(store, func() error {
		return errors.New("generate failed")
	}, mh)
	if err == nil || !strings.Contains(err.Error(), "generate failed") {
		t.Fatalf("error = %v, want generation failure", err)
	}
	if mh.reloadCalls != 0 {
		t.Fatalf("reload calls = %d, want 0", mh.reloadCalls)
	}
}

func TestSyncMihomoAfterSingboxReload_DisabledStopsWithoutGenerate(t *testing.T) {
	store := newDynamicEngineTestStore(t, "mihomo")
	setRouterEnabled(t, store, false)
	mh := &fakeProxyEngine{}
	generateCalls := 0

	err := syncMihomoAfterSingboxReload(store, func() error {
		generateCalls++
		return nil
	}, mh)
	if err != nil {
		t.Fatalf("syncMihomoAfterSingboxReload() error = %v", err)
	}
	if generateCalls != 0 || mh.reloadCalls != 0 || mh.stopCalls != 1 {
		t.Fatalf("disabled Mihomo lifecycle: generate=%d reload=%d stop=%d", generateCalls, mh.reloadCalls, mh.stopCalls)
	}
}

func setRouterEnabled(t *testing.T, store *storage.SettingsStore, enabled bool) {
	t.Helper()
	err := store.Update(func(s *storage.Settings) error {
		s.SingboxRouter.Enabled = enabled
		return nil
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
}

func TestSetupMihomo_CorruptNativeStoreQuarantined(t *testing.T) {
	dataDir := t.TempDir()
	mihomoDir := filepath.Join(dataDir, "mihomo")
	if err := os.MkdirAll(mihomoDir, 0755); err != nil {
		t.Fatal(err)
	}
	nativePath := filepath.Join(mihomoDir, "native.json")
	corruptContent := []byte("{ this is corrupt json !!!")
	if err := os.WriteFile(nativePath, corruptContent, 0644); err != nil {
		t.Fatal(err)
	}

	settingsStore := storage.NewSettingsStore(dataDir)
	loggingService := logging.NewService(settingsStore)
	t.Cleanup(loggingService.Stop)

	a := &app{
		dataDir:        dataDir,
		settingsStore:  settingsStore,
		settings:       &storage.Settings{},
		loggingService: loggingService,
		bootLog:        logging.NewScopedLogger(loggingService, logging.GroupSystem, logging.SubCleanup),
		eventBus:       events.NewBus(),
	}
	t.Cleanup(a.runOnExit)

	// Should not panic on corrupt native.json
	a.setupMihomo()

	if a.mihomoNativeStore == nil {
		t.Fatal("mihomoNativeStore should be initialized with clean store")
	}

	// Verify quarantine file was created
	files, err := os.ReadDir(mihomoDir)
	if err != nil {
		t.Fatal(err)
	}
	foundCorrupt := false
	for _, f := range files {
		if strings.Contains(f.Name(), "native.json.corrupt") {
			foundCorrupt = true
			data, err := os.ReadFile(filepath.Join(mihomoDir, f.Name()))
			if err != nil {
				t.Fatalf("failed to read corrupt file: %v", err)
			}
			if string(data) != string(corruptContent) {
				t.Fatalf("corrupt file content mismatch: got %q, want %q", string(data), string(corruptContent))
			}
			break
		}
	}
	if !foundCorrupt {
		t.Fatal("expected native.json.corrupt.<timestamp> to be created")
	}
}

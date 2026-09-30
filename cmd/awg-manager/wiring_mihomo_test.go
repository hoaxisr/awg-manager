package main

import (
	"errors"
	"strings"
	"testing"

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

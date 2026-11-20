package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestUplinkProductionEngineGuardBeforeControllerInitialization(t *testing.T) {
	a := &app{dataDir: t.TempDir()}
	path := filepath.Join(a.dataDir, "uplink-policies.json")
	store, err := uplink.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(uplink.Policy{ID: "vpn", Target: uplink.Target{Engine: "sing-box", Ref: "out"}, Providers: []string{"a", "b"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	hooks := a.buildProductionEngineSwitchHooks()
	guard, err := hooks.AcquireTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.CheckTarget("firc"); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
	guard.Release()
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.AcquireTransition(context.Background()); err == nil {
		t.Fatal("corrupt durable policy silently ignored")
	}
	// The live controller is authoritative after initialization; use its lock
	// and snapshot rather than constructing a second controller from disk.
	a.uplinkController.Store(uplink.New(store, nil, nil))
	guard, err = hooks.AcquireTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	if err := guard.CheckTarget("firc"); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
}

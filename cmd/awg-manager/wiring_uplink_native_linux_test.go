//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestUplinkNativeProductionGuardsStorageAndEngineIndependence(t *testing.T) {
	dir := t.TempDir()
	awg := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "locks"))
	if err := awg.Create(&storage.AWGTunnel{ID: "awg50", Backend: "nativewg", Name: "before", ISPInterface: "ISP1"}); err != nil {
		t.Fatal(err)
	}
	policies, err := uplink.OpenStore(filepath.Join(dir, "uplink-policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := policies.Put(uplink.Policy{ID: "native", Target: uplink.Target{Engine: "wireguard", Ref: "awg50"}, Providers: []string{"ISP1", "ISP2"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a := &app{dataDir: dir, awgStore: awg, tunnelService: &service.ServiceImpl{}}
	manager, err := a.setupNativeUplink(policies)
	if err != nil {
		t.Fatal(err)
	}
	a.uplinkController.Store(uplink.New(policies, nil, map[string]uplink.Adapter{"wireguard": manager}))
	if err := awg.Update("awg50", func(t *storage.AWGTunnel) error { t.ISPInterface = ""; return nil }); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("storage bypassed native policy", err)
	}
	if err := a.tunnelService.Delete(context.Background(), "awg50"); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("delete guard ran after side effects", err)
	}
	if err := awg.Update("awg50", func(t *storage.AWGTunnel) error { t.Name = "after"; return nil }); err != nil {
		t.Fatal(err)
	}
	a.uplinkController.Store(nil)
	guard, err := a.buildProductionEngineSwitchHooks().AcquireTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.CheckTarget("firc"); err != nil {
		t.Fatal("preinit native policy blocked unrelated engine", err)
	}
	guard.Release()
	p.Enabled = false
	if _, err = policies.Put(p); err != nil {
		t.Fatal(err)
	}
	if err := awg.Update("awg50", func(t *storage.AWGTunnel) error { t.ISPInterface = "ISP2"; return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestUplinkNativeBackupPauseStopsPrestartWithoutReverseLock(t *testing.T) {
	dir := t.TempDir()
	policies, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	op := nwg.NewOperator(nil, nil, nil, nil)
	defer op.Close()
	a := &app{dataDir: dir, nwgOp: op}
	manager, err := a.setupNativeUplink(policies)
	if err != nil {
		t.Fatal(err)
	}
	c := uplink.New(policies, nil, map[string]uplink.Adapter{"wireguard": manager})
	a.uplinkController.Store(c)
	if err := c.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := op.Start(context.Background(), &storage.AWGTunnel{ID: "awg50"}); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("prestart bypassed backup pause", err)
	}
}

func TestUplinkNativeCorruptRegistryBlocksNativeMutation(t *testing.T) {
	dir := t.TempDir()
	awg := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "locks"))
	for _, record := range []*storage.AWGTunnel{{ID: "awg50", Backend: "nativewg", ISPInterface: "ISP1"}, {ID: "awg1", Backend: "kernel"}} {
		if err := awg.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "uplink-wireguard.json"), []byte(`{"version":999,"profiles":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	policies, err := uplink.OpenStore(filepath.Join(dir, "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{dataDir: dir, awgStore: awg, tunnelService: &service.ServiceImpl{}}
	if _, err = a.setupNativeUplink(policies); err == nil {
		t.Fatal("corrupt registry accepted")
	}
	c := uplink.New(policies, nil, map[string]uplink.Adapter{"wireguard": a.unavailableNativeAdapter(err)})
	a.uplinkController.Store(c)
	release, guardErr := c.GuardNativeMutation(context.Background(), "awg1", "transport")
	if guardErr != nil {
		t.Fatal("native registry failure blocked kernel transport", guardErr)
	}
	release()
	if err := awg.Update("awg50", func(t *storage.AWGTunnel) error { t.ISPInterface = ""; return nil }); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
	if err := a.tunnelService.Start(context.Background(), "awg50"); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal(err)
	}
	if err := awg.Update("awg1", func(t *storage.AWGTunnel) error { t.ISPInterface = "ISP2"; return nil }); err != nil {
		t.Fatal("native failure blocked unrelated kernel record", err)
	}
}

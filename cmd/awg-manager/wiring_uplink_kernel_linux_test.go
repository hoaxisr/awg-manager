//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/service"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

func TestUplinkKernelProductionGuardsAndCatalog(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "locks"))
	record := &storage.AWGTunnel{ID: "awg10", Name: "Sel", Backend: "kernel", Enabled: true}
	record.Interface.PrivateKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	record.Interface.DNS = "1.1.1.1"
	record.Peer.PublicKey = base64.StdEncoding.EncodeToString(make([]byte, 32))
	record.Peer.Endpoint = "192.0.2.9:1234"
	record.Peer.AllowedIPs = []string{"0.0.0.0/0"}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	policies, err := uplink.OpenStore(filepath.Join(dir, "uplink-policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{dataDir: dir, awgStore: store, tunnelService: &service.ServiceImpl{}, operator: ops.NewOperatorOS5(nil, nil, nil, nil, nil)}
	adapter := a.setupKernelUplink(policies)
	if _, err = a.setupNativeUplink(policies); err != nil {
		t.Fatal(err)
	}
	c := uplink.New(policies, nil, map[string]uplink.Adapter{"kernel-amneziawg": adapter})
	a.uplinkController.Store(c)
	list, err := c.Targets(context.Background())
	if err != nil || len(list) != 1 || list[0].Target.Engine != "kernel-amneziawg" || list[0].Target.Ref != "awg10" {
		t.Fatal(list, err)
	}
	if _, err = policies.Put(uplink.Policy{ID: "kernel", Enabled: true, Target: list[0].Target, Providers: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err = store.Update("awg10", func(t *storage.AWGTunnel) error { t.Peer.Endpoint = "192.0.2.10:1234"; return nil }); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("storage bypassed policy", err)
	}
	if err = a.tunnelService.Delete(context.Background(), "awg10"); !errors.Is(err, uplink.ErrConflict) {
		t.Fatal("delete reached side effects", err)
	}
	if err = store.Update("awg10", func(t *storage.AWGTunnel) error { t.Name = "renamed"; return nil }); err != nil {
		t.Fatal("metadata blocked", err)
	}
	g, err := c.AcquireEngineTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = g.CheckTarget("firc"); err != nil {
		t.Fatal("kernel policy coupled to proxy engine", err)
	}
	g.Release()
	a.uplinkController.Store(nil)
	preGuard, err := a.buildProductionEngineSwitchHooks().AcquireTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = preGuard.CheckTarget("firc"); err != nil {
		t.Fatal("pre-init guard coupled kernel policy to proxy engine", err)
	}
	preGuard.Release()
}

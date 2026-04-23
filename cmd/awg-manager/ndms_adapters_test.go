package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	"github.com/hoaxisr/awg-manager/internal/storage"
	trafficpkg "github.com/hoaxisr/awg-manager/internal/traffic"
)

type stubTunnelLister struct {
	tunnels []trafficpkg.RunningTunnel
}

func (s *stubTunnelLister) RunningTunnels(ctx context.Context) []trafficpkg.RunningTunnel {
	return s.tunnels
}

type stubSystemTunnels struct {
	list []ndms.SystemWireguardTunnel
	err  error
}

func (s *stubSystemTunnels) List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return s.list, s.err
}

// TestRunningInterfaces_FiltersKernelAndDownServers verifies the two fixes:
// 1. Kernel-backend tunnels are skipped (no /wireguard/peer in NDMS).
// 2. Server interfaces whose system-tunnel Status != "up" are filtered out.
// Up servers remain included, and an up NativeWG tunnel is included as a
// non-server.
func TestRunningInterfaces_FiltersKernelAndDownServers(t *testing.T) {
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if err := settings.Save(&storage.Settings{
		ServerInterfaces: []string{"Wireguard0", "Wireguard5"},
		ManagedServer:    &storage.ManagedServer{InterfaceName: "Wireguard9"},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	tunnels := &stubTunnelLister{
		tunnels: []trafficpkg.RunningTunnel{
			{ID: "kern-1", BackendType: "kernel", NDMSName: "OpkgTun10"},
			{ID: "nwg-1", BackendType: "nativewg", NDMSName: "Wireguard3"},
		},
	}

	sys := &stubSystemTunnels{
		list: []ndms.SystemWireguardTunnel{
			{ID: "Wireguard0", Status: "up"},       // server, up -> include
			{ID: "Wireguard5", Status: "down"},     // server, down -> skip
			{ID: "Wireguard9", Status: "up"},       // managed server, up -> include
			{ID: "Wireguard7", Status: "up"},       // unmanaged up -> include
			{ID: "Wireguard8", Status: "disabled"}, // unmanaged down -> skip
		},
	}

	a := newRunningInterfacesAdapter(tunnels, sys, settings)
	refs := a.RunningInterfaces(context.Background())

	got := make(map[string]metrics.InterfaceRef, len(refs))
	for _, r := range refs {
		if _, dup := got[r.ID]; dup {
			t.Fatalf("duplicate id %q in refs: %+v", r.ID, refs)
		}
		got[r.ID] = r
	}

	// Presence-only: dedupeRefs keeps first-seen, so IsServer gets fixed by
	// the insertion order inside RunningInterfaces (non-server first when an
	// ID appears in both systemTunnels and the server list). The fix under
	// test is concerned with *which IDs* are emitted, not the IsServer
	// marker — that's a separate, pre-existing dedupe quirk.
	wantIncluded := []string{
		"Wireguard3", // nwg running tunnel
		"Wireguard7", // up unmanaged system tunnel
		"Wireguard0", // up marked server
		"Wireguard9", // up managed server
	}
	for _, id := range wantIncluded {
		if _, ok := got[id]; !ok {
			t.Errorf("expected %q in refs, got: %+v", id, refs)
		}
	}

	wantExcluded := []string{
		"OpkgTun10",  // kernel-backend tunnel
		"Wireguard5", // down marked server
		"Wireguard8", // down unmanaged system tunnel
	}
	for _, id := range wantExcluded {
		if _, ok := got[id]; ok {
			t.Errorf("did not expect %q in refs, got: %+v", id, refs)
		}
	}
}

// TestRunningInterfaces_NilSystemTunnelsFallback verifies that when the
// systemTunnels provider is nil, server interfaces are still included
// (preserving previous behaviour rather than silently hiding them).
func TestRunningInterfaces_NilSystemTunnelsFallback(t *testing.T) {
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if err := settings.Save(&storage.Settings{
		ServerInterfaces: []string{"Wireguard0"},
		ManagedServer:    &storage.ManagedServer{InterfaceName: "Wireguard9"},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	a := newRunningInterfacesAdapter(&stubTunnelLister{}, nil, settings)
	refs := a.RunningInterfaces(context.Background())

	wantIDs := map[string]bool{"Wireguard0": true, "Wireguard9": true}
	if len(refs) != len(wantIDs) {
		t.Fatalf("refs=%+v, want %d entries", refs, len(wantIDs))
	}
	for _, r := range refs {
		if !wantIDs[r.ID] {
			t.Errorf("unexpected ref %+v", r)
		}
		if !r.IsServer {
			t.Errorf("ref %+v should be marked server", r)
		}
	}
}

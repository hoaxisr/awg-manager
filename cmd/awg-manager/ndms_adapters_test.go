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

	// dedupeRefs now merges IsServer=true over IsServer=false, so IDs that
	// appear in both systemTunnels (non-server) and the server list (or
	// managed-server) end up with IsServer=true. That's the point of the
	// merge: the poller must route peer changes on those IDs to the
	// server-snapshot path, not the tunnel-traffic path.
	wantIncluded := map[string]bool{
		"Wireguard3": false, // nwg running tunnel (managed, not a server)
		"Wireguard7": false, // up unmanaged system tunnel (not a server)
		"Wireguard0": true,  // up marked server
		"Wireguard9": true,  // up managed server (also in systemTunnels list)
	}
	for id, wantServer := range wantIncluded {
		r, ok := got[id]
		if !ok {
			t.Errorf("expected %q in refs, got: %+v", id, refs)
			continue
		}
		if r.IsServer != wantServer {
			t.Errorf("ref %q: IsServer = %v, want %v", id, r.IsServer, wantServer)
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

// TestDedupeRefs_ServerFlagWins verifies that when the same ID appears
// both as a non-server (IsServer=false) and a server (IsServer=true),
// the merged entry has IsServer=true regardless of insertion order.
func TestDedupeRefs_ServerFlagWins(t *testing.T) {
	cases := []struct {
		name  string
		input []metrics.InterfaceRef
	}{
		{
			name: "non-server then server",
			input: []metrics.InterfaceRef{
				{ID: "Wireguard0", IsServer: false},
				{ID: "Wireguard0", IsServer: true},
			},
		},
		{
			name: "server then non-server",
			input: []metrics.InterfaceRef{
				{ID: "Wireguard0", IsServer: true},
				{ID: "Wireguard0", IsServer: false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := dedupeRefs(tc.input)
			if len(out) != 1 {
				t.Fatalf("expected 1 entry, got %d: %+v", len(out), out)
			}
			if !out[0].IsServer {
				t.Errorf("IsServer = false, want true (server flag must win): %+v", out[0])
			}
		})
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

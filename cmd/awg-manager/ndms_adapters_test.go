package main

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/metrics"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type stubSystemTunnels struct {
	list []ndms.SystemWireguardTunnel
	err  error
}

func (s *stubSystemTunnels) List(ctx context.Context) ([]ndms.SystemWireguardTunnel, error) {
	return s.list, s.err
}

// stubAWGStore implements awgStoreLister for tests that need to configure
// which managed tunnels exist (and at which NWGIndex) without touching disk.
type stubAWGStore struct {
	list []storage.AWGTunnel
	err  error
}

func (s *stubAWGStore) List() ([]storage.AWGTunnel, error) {
	return s.list, s.err
}

// TestRunningInterfaces_FiltersDownServersAndIncludesSystemTunnels verifies:
//  1. Server interfaces whose system-tunnel Status != "up" are filtered out.
//  2. Up unmanaged system tunnels are included as non-servers.
//  3. Managed server (InterfaceName) is included as a server when up.
//
// Managed AWGM tunnels do not pass through this adapter — they are driven
// by traffic.SysfsPoller (wired in main.go) — so this adapter is only
// tested for its NDMS-side responsibilities.
func TestRunningInterfaces_FiltersDownServersAndIncludesSystemTunnels(t *testing.T) {
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if err := settings.Save(&storage.Settings{
		ServerInterfaces: []string{"Wireguard0", "Wireguard5"},
		ManagedServer:    &storage.ManagedServer{InterfaceName: "Wireguard9"},
	}); err != nil {
		t.Fatalf("save settings: %v", err)
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

	a := newRunningInterfacesAdapter(sys, nil, settings)
	refs := a.RunningInterfaces(context.Background())

	got := make(map[string]metrics.InterfaceRef, len(refs))
	for _, r := range refs {
		if _, dup := got[r.ID]; dup {
			t.Fatalf("duplicate id %q in refs: %+v", r.ID, refs)
		}
		got[r.ID] = r
	}

	// dedupeRefs merges IsServer=true over IsServer=false, so IDs that
	// appear in both systemTunnels (non-server) and the server list (or
	// managed-server) end up with IsServer=true. That's the point of the
	// merge: the poller must route peer changes on those IDs to the
	// server-snapshot path, not the tunnel-traffic path.
	wantIncluded := map[string]bool{
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

	a := newRunningInterfacesAdapter(nil, nil, settings)
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

// TestRunningInterfaces_SkipsManagedNWGNames verifies that NDMS names of
// AWG Manager-managed NativeWG tunnels are filtered out of the NDMS
// poller's interface list. These tunnels are already polled via
// traffic.SysfsPoller against the kernel iface (e.g. nwg3); emitting a
// second tunnel:traffic event keyed by the NDMS name (e.g. Wireguard3)
// wastes an RCI call per cycle and creates a stale traffic.History
// entry the UI never reads.
func TestRunningInterfaces_SkipsManagedNWGNames(t *testing.T) {
	dir := t.TempDir()
	settings := storage.NewSettingsStore(dir)
	if err := settings.Save(&storage.Settings{}); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	awg := &stubAWGStore{
		list: []storage.AWGTunnel{
			{ID: "t-managed", Backend: "nativewg", NWGIndex: 3},
			{ID: "t-kernel", Backend: "kernel"}, // not a NativeWG-managed iface
		},
	}

	sys := &stubSystemTunnels{
		list: []ndms.SystemWireguardTunnel{
			{ID: "Wireguard0", Status: "up"}, // unmanaged -> include
			{ID: "Wireguard3", Status: "up"}, // managed NWG (matches NWGIndex=3) -> skip
		},
	}

	a := newRunningInterfacesAdapter(sys, awg, settings)
	refs := a.RunningInterfaces(context.Background())

	got := make(map[string]bool, len(refs))
	for _, r := range refs {
		got[r.ID] = true
	}

	if !got["Wireguard0"] {
		t.Errorf("expected Wireguard0 in refs (unmanaged up tunnel), got: %+v", refs)
	}
	if got["Wireguard3"] {
		t.Errorf("did not expect Wireguard3 in refs (managed NativeWG, polled via sysfs), got: %+v", refs)
	}
}

package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func seedAllowIPsStore(t *testing.T, peers []storage.ManagedPeer, migrated bool) (*storage.SettingsStore, *fakePoster) {
	t.Helper()
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := store.SetManagedPeerAllowIPsMigrated(migrated); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0", Address: "10.0.0.1", Mask: "255.255.255.0",
		ListenPort: 51820, Peers: peers,
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	return store, &fakePoster{}
}

func TestMigratePeerAllowIPs_StripsDefaultRoute(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{
		{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"},
		{PublicKey: "PEER_B", TunnelIP: "10.0.0.3/32"},
	}, false)
	s := &Service{settings: store, transport: poster}

	s.MigratePeerAllowIPs(context.Background())

	if len(poster.posts) != 2 {
		t.Fatalf("expected 2 RCI removes, got %d", len(poster.posts))
	}
	raw, _ := json.Marshal(poster.posts[0])
	got := string(raw)
	for _, want := range []string{`"no":true`, `"address":"0.0.0.0"`, `"mask":"0.0.0.0"`, `"key":"PEER_A"`} {
		if !strings.Contains(got, want) {
			t.Errorf("remove payload missing %q; got %s", want, got)
		}
	}
	if !store.IsManagedPeerAllowIPsMigrated() {
		t.Error("flag not set after successful sweep")
	}
}

func TestMigratePeerAllowIPs_SkipsWhenMigrated(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{
		{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"},
	}, true)
	s := &Service{settings: store, transport: poster}

	s.MigratePeerAllowIPs(context.Background())

	if len(poster.posts) != 0 {
		t.Errorf("expected no RCI calls when already migrated, got %d", len(poster.posts))
	}
}

func TestMigratePeerAllowIPs_RetriesWhenAllFail(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{
		{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"},
	}, false)
	poster.err = errors.New("ndms unreachable")
	s := &Service{settings: store, transport: poster}

	s.MigratePeerAllowIPs(context.Background())

	if store.IsManagedPeerAllowIPsMigrated() {
		t.Error("flag must stay false when every removal fails (retry next boot)")
	}
}

package managed

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestExportAll_CopiesStorageWithPrivateKey(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(filepath.Join(dir))
	_, _ = store.Load()
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0", Address: "10.0.0.1", Mask: "255.255.255.0",
		ListenPort: 51820, PrivateKey: "PRIVKEY=",
		Peers: []storage.ManagedPeer{{PublicKey: "P", I1: "a", I2: "b", I3: "c", I4: "d", I5: "e", SignatureProfile: "dns"}},
	})

	s := &Service{settings: store}
	exported, err := s.ExportAll(context.Background())
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if len(exported) != 1 {
		t.Fatalf("len: got %d, want 1", len(exported))
	}
	if exported[0].PrivateKey != "PRIVKEY=" {
		t.Errorf("PrivateKey not exported: %q", exported[0].PrivateKey)
	}
	if exported[0].InterfaceName != "Wireguard0" {
		t.Errorf("InterfaceName: %q", exported[0].InterfaceName)
	}
	p := exported[0].Peers[0]
	if p.I3 != "c" || p.I4 != "d" || p.I5 != "e" || p.SignatureProfile != "dns" {
		t.Errorf("peer signature lost in export: %+v", p)
	}
}

func TestDrift_ReturnsEntriesMissingFromNDMS(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewSettingsStore(filepath.Join(dir))
	_, _ = store.Load()
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard0", Address: "10.0.0.1", Mask: "255.255.255.0",
		ListenPort: 51820, PrivateKey: "k0", Peers: []storage.ManagedPeer{},
	})
	_ = store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard1", Address: "10.1.0.1", Mask: "255.255.255.0",
		ListenPort: 51821, PrivateKey: "k1", Peers: []storage.ManagedPeer{},
	})

	s := &Service{settings: store}
	presence := func(ndms string) bool {
		return ndms == "Wireguard0" // only first is present in NDMS
	}
	drift, err := s.driftWith(context.Background(), presence)
	if err != nil {
		t.Fatalf("driftWith: %v", err)
	}
	if len(drift) != 1 {
		t.Fatalf("len: got %d, want 1", len(drift))
	}
	if drift[0].InterfaceName != "Wireguard1" {
		t.Errorf("got drift for %q, want Wireguard1", drift[0].InterfaceName)
	}
	if drift[0].PrivateKey != "k1" {
		t.Errorf("drift entry missing PrivateKey: got %q", drift[0].PrivateKey)
	}
}

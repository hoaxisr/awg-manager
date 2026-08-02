package storage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// #670: удалённый managed-сервер оставлял свой ref в
// singboxRouter.ingressInterfaces. Router-reconcile (тик 30s) пытался
// резолвить "managed:Wireguard1" через RCI, а NDMS на каждый запрос писал
// в журнал 'unable to find Wireguard1 in "Network::Interface::Base"'.

func TestLoad_PrunesOrphanIngressRefs(t *testing.T) {
	raw := `{"schemaVersion":32,
		"managedServers":[{"interfaceName":"Wireguard0"}],
		"singboxRouter":{"ingressInterfaces":["managed:Wireguard0","managed:Wireguard1","iface:nwg5"]}}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	store := NewSettingsStore(dir)
	s, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"managed:Wireguard0", "iface:nwg5"}
	if !slices.Equal(s.SingboxRouter.IngressInterfaces, want) {
		t.Fatalf("IngressInterfaces = %v, want %v", s.SingboxRouter.IngressInterfaces, want)
	}
	// Самолечение должно быть записано на диск, а не жить только в памяти.
	reloaded, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Equal(reloaded.SingboxRouter.IngressInterfaces, want) {
		t.Fatalf("после перезагрузки = %v, want %v", reloaded.SingboxRouter.IngressInterfaces, want)
	}
}

func TestDeleteManagedServer_PrunesIngressRef(t *testing.T) {
	raw := `{"schemaVersion":32,
		"managedServers":[{"interfaceName":"Wireguard0"},{"interfaceName":"Wireguard1"}],
		"singboxRouter":{"ingressInterfaces":["managed:Wireguard0","managed:Wireguard1","iface:nwg5"]}}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(raw), 0644); err != nil {
		t.Fatal(err)
	}
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.DeleteManagedServer("Wireguard1"); err != nil {
		t.Fatalf("DeleteManagedServer: %v", err)
	}
	want := []string{"managed:Wireguard0", "iface:nwg5"}
	reloaded, err := NewSettingsStore(dir).Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !slices.Equal(reloaded.SingboxRouter.IngressInterfaces, want) {
		t.Fatalf("IngressInterfaces = %v, want %v", reloaded.SingboxRouter.IngressInterfaces, want)
	}
}

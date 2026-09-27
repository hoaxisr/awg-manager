package storage

import (
	"reflect"
	"testing"
)

// Оба поля переживают запись и перечитывание с диска у обеих записей.
func TestPeerSubnets_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewSettingsStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	sec := ServerPeerSecret{PrivateKey: "P", TunnelIP: "10.9.0.2/32", ClientAllowedIPs: "10.9.0.0/24, 192.168.1.0/24", RemoteSubnets: []string{"192.168.77.0/24"}}
	if err := store.SetServerPeerSecret("Wireguard0", "K=", sec); err != nil {
		t.Fatal(err)
	}
	if err := store.AddManagedServer(ManagedServer{InterfaceName: "Wireguard1", Peers: []ManagedPeer{
		{PublicKey: "M=", ClientAllowedIPs: "0.0.0.0/1", RemoteSubnets: []string{"192.168.78.0/24"}}}}); err != nil {
		t.Fatal(err)
	}
	fresh := NewSettingsStore(dir)
	if _, err := fresh.Load(); err != nil {
		t.Fatal(err)
	}
	got, ok := fresh.GetServerPeerSecret("Wireguard0", "K=")
	if !ok || !reflect.DeepEqual(got, sec) {
		t.Fatalf("secret = %+v", got)
	}
	sv, ok := fresh.GetManagedServerByID("Wireguard1")
	if !ok || sv.Peers[0].ClientAllowedIPs != "0.0.0.0/1" || !reflect.DeepEqual(sv.Peers[0].RemoteSubnets, []string{"192.168.78.0/24"}) {
		t.Fatalf("peer = %+v", sv.Peers)
	}
}

func TestGetServerPeerSecrets_SnapshotDoesNotLeak(t *testing.T) {
	store := NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	_ = store.SetServerPeerSecret("Wireguard0", "A=", ServerPeerSecret{PrivateKey: "a"})
	_ = store.SetServerPeerSecret("Wireguard3", "B=", ServerPeerSecret{PrivateKey: "b"})
	all := store.GetServerPeerSecrets()
	if len(all) != 2 || all["Wireguard3"]["B="].PrivateKey != "b" {
		t.Fatalf("snapshot = %v", all)
	}
	delete(all["Wireguard0"], "A=")
	if _, ok := store.GetServerPeerSecret("Wireguard0", "A="); !ok {
		t.Fatal("правка снимка утекла в стор")
	}
	// GetServerPeerSecrets() лениво грузит настройки через Get() → Load(),
	// который на отсутствующем файле создаёт и сохраняет пустой Settings
	// (see Load(), settings.go): ошибки нет, поэтому карта пустая, не nil.
	if got := NewSettingsStore(t.TempDir()).GetServerPeerSecrets(); got == nil || len(got) != 0 {
		t.Fatalf("незагруженный стор: %v", got)
	}
}

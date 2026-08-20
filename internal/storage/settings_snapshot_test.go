package storage

import (
	"fmt"
	"testing"
)

func newLoadedStore(t *testing.T) *SettingsStore {
	t.Helper()
	s := NewSettingsStore(t.TempDir())
	if _, err := s.Load(); err != nil {
		t.Fatalf("seed Load: %v", err)
	}
	return s
}

// Красный до фикса под -race: GetServerPeerSecret читает живую map без
// лока, SetServerPeerSecret пишет в неё же под локом — data race, а без
// -race это тот самый fatal concurrent map read and map write.
func TestGetServerPeerSecret_NoRaceWithWrites(t *testing.T) {
	s := newLoadedStore(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			_ = s.SetServerPeerSecret("Wireguard0", fmt.Sprintf("pk%d", i), ServerPeerSecret{PrivateKey: "x"})
		}
	}()
	for i := 0; i < 200000; i++ {
		s.GetServerPeerSecret("Wireguard0", "pk0")
		s.GetServerInterfaceMeta("Wireguard0")
	}
	<-done
}

// Snapshot — независимая копия: правка снапшота не видна кэшу.
// (Красный компиляционно: метода ещё нет.)
func TestSnapshot_IndependentCopy(t *testing.T) {
	s := newLoadedStore(t)
	if err := s.SetServerPeerSecret("Wireguard0", "pk", ServerPeerSecret{PrivateKey: "x"}); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap.ApiKey = "mutated"
	delete(snap.ServerPeerSecrets, "Wireguard0")
	live, _ := s.Get()
	if live.ApiKey == "mutated" {
		t.Fatal("снапшот разделяет скаляры с кэшем")
	}
	if _, ok := s.GetServerPeerSecret("Wireguard0", "pk"); !ok {
		t.Fatal("снапшот разделяет map'ы с кэшем")
	}
}

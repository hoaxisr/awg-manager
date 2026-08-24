package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestSyncLinkedAwgTunnelEndpoints(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	tun := &storage.AWGTunnel{
		ID:               "awgm1",
		Name:             "FT",
		FreeTurnClientID: "client-a",
		Peer:             storage.AWGPeer{Endpoint: "127.0.0.1:9000"},
	}
	if err := store.Save(tun); err != nil {
		t.Fatal(err)
	}

	updated, errs := syncLinkedAwgTunnelEndpoints(context.Background(), store, nil, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "127.0.0.1:9001")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(updated) != 1 || updated[0] != "awgm1" {
		t.Fatalf("updated = %v", updated)
	}
	got, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "127.0.0.1:9001" {
		t.Fatalf("endpoint = %q", got.Peer.Endpoint)
	}

	updated, errs = syncLinkedAwgTunnelEndpoints(context.Background(), store, nil, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "127.0.0.1:9001")
	if len(errs) != 0 || len(updated) != 0 {
		t.Fatalf("idempotent sync: updated=%v errs=%v", updated, errs)
	}
}

func TestSyncLinkedAwgTunnelNames(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	tun := &storage.AWGTunnel{
		ID:               "awgm1",
		Name:             "Old FT",
		FreeTurnClientID: "client-a",
	}
	if err := store.Save(tun); err != nil {
		t.Fatal(err)
	}

	renamed, errs := syncLinkedAwgTunnelNames(context.Background(), store, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "New FT")
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(renamed) != 1 || renamed[0] != "awgm1" {
		t.Fatalf("renamed = %v", renamed)
	}
	got, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New FT" {
		t.Fatalf("name = %q", got.Name)
	}

	renamed, errs = syncLinkedAwgTunnelNames(context.Background(), store, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "New FT")
	if len(errs) != 0 || len(renamed) != 0 {
		t.Fatalf("idempotent rename: renamed=%v errs=%v", renamed, errs)
	}
}

// SyncLinkedProxyEndpoints — экспорт для прокси-рантайма: поле связи выбирает
// туннели, чужие не трогаются.
func TestSyncLinkedProxyEndpointsByField(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awgm1", Name: "FT", FreeTurnClientID: "client-a", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
		{ID: "awgm2", Name: "WD", WdttClientID: "client-a", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
	} {
		if err := store.Save(tun); err != nil {
			t.Fatal(err)
		}
	}

	updated, failed := SyncLinkedProxyEndpoints(context.Background(), store, nil,
		LinkedWdtt, "client-a", "127.0.0.1:9001")
	if len(failed) != 0 || len(updated) != 1 || updated[0] != "awgm2" {
		t.Fatalf("wdtt: updated=%v failed=%v", updated, failed)
	}
	ft, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Peer.Endpoint != "127.0.0.1:9000" {
		t.Fatalf("чужой туннель тронут: %q", ft.Peer.Endpoint)
	}

	updated, failed = SyncLinkedProxyEndpoints(context.Background(), store, nil,
		LinkedFreeTurn, "client-a", "127.0.0.1:9002")
	if len(failed) != 0 || len(updated) != 1 || updated[0] != "awgm1" {
		t.Fatalf("freeturn: updated=%v failed=%v", updated, failed)
	}
	ft, err = store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Peer.Endpoint != "127.0.0.1:9002" {
		t.Fatalf("endpoint = %q", ft.Peer.Endpoint)
	}
}

// Разбор listen без умирающих пакетов (Н1). Хост обязан быть 127.0.0.1 либо
// пустым: приём любого хоста делал бы endpoint туннеля локальным для чужого
// адреса прослушивания.
func TestLocalEndpointFromListen(t *testing.T) {
	cases := []struct {
		listen string
		want   string
		ok     bool
	}{
		{"127.0.0.1:9001", "127.0.0.1:9001", true},
		{":9001", "127.0.0.1:9001", true},
		{"0.0.0.0:9001", "", false},
		{"8.8.8.8:9001", "", false},
		{"localhost:9001", "", false},
		{"", "", false},
		{"9001", "", false},
		{"127.0.0.1:abc", "", false},
		{"127.0.0.1:0", "", false},
		{"127.0.0.1:70000", "", false},
	}
	for _, c := range cases {
		got, ok := localEndpointFromListen(c.listen)
		if ok != c.ok || got != c.want {
			t.Fatalf("%q: got (%q,%v), want (%q,%v)", c.listen, got, ok, c.want, c.ok)
		}
	}
}

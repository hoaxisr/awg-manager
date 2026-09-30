package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// routerWithPeers — Queries над FakeGetter: на Wireguard0 роутера стоят пиры
// keys (миграция шлёт allow-ips только им — свежее чтение rc).
func routerWithPeers(t *testing.T, keys ...string) *query.Queries {
	t.Helper()
	return query.NewQueries(query.Deps{Getter: fakeWithPeers(keys...), Logger: query.NopLogger()})
}

// fakeWithPeers — роутер с интерфейсом Wireguard0 и пирами keys на нём.
func fakeWithPeers(keys ...string) *query.FakeGetter {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"}}`)
	var peers []string
	for _, k := range keys {
		peers = append(peers, `{"key":"`+k+`"}`)
	}
	fg.SetRC("Wireguard0", `{"wireguard":{"peer":[`+strings.Join(peers, ",")+`]}}`)
	return fg
}

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
	s := &Service{settings: store, transport: poster, queries: routerWithPeers(t, "PEER_A", "PEER_B")}

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
	s := &Service{settings: store, transport: poster, queries: routerWithPeers(t, "PEER_A", "PEER_B")}

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
	s := &Service{settings: store, transport: poster, queries: routerWithPeers(t, "PEER_A", "PEER_B")}

	s.MigratePeerAllowIPs(context.Background())

	if store.IsManagedPeerAllowIPsMigrated() {
		t.Error("flag must stay false when every removal fails (retry next boot)")
	}
}

// Fix round 3: пира из записи на роутере нет (снят в веб-морде) — снятие 0/0
// ему не шлётся: любая операция allow-ips на неизвестном ключе создаёт пира.
// Остальным — шлётся; флаг встаёт.
func TestMigratePeerAllowIPs_AbsentPeerSkipped(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{
		{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"},
		{PublicKey: "GONE", TunnelIP: "10.0.0.3/32"},
	}, false)
	s := &Service{settings: store, transport: poster, queries: routerWithPeers(t, "PEER_A")}
	s.MigratePeerAllowIPs(context.Background())
	if len(poster.posts) != 1 {
		t.Fatalf("posts = %v", poster.posts)
	}
	if raw, _ := json.Marshal(poster.posts[0]); !strings.Contains(string(raw), `"key":"PEER_A"`) {
		t.Fatalf("post = %s", raw)
	}
	if !store.IsManagedPeerAllowIPsMigrated() {
		t.Error("флаг не встал")
	}
}

// Fix round 3: список пиров не прочитался — ничего не шлём, флаг не ставим
// (повтор на следующей загрузке).
func TestMigratePeerAllowIPs_PeerReadFailure_NoPostsRetry(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"}}, false)
	fg := fakeWithPeers("PEER_A")
	fg.SetError("/show/rc/interface/", errors.New("rci down"))
	s := &Service{settings: store, transport: poster, queries: query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})}
	s.MigratePeerAllowIPs(context.Background())
	if len(poster.posts) != 0 || store.IsManagedPeerAllowIPsMigrated() {
		t.Fatalf("posts=%v migrated=%v", poster.posts, store.IsManagedPeerAllowIPsMigrated())
	}
}

func addSecondServer(t *testing.T, store *storage.SettingsStore) {
	t.Helper()
	if err := store.AddManagedServer(storage.ManagedServer{
		InterfaceName: "Wireguard1", Address: "10.0.1.1", Mask: "255.255.255.0",
		ListenPort: 51821, Peers: []storage.ManagedPeer{{PublicKey: "PEER_B", TunnelIP: "10.0.1.2/32"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// Fix round 4 (N1): rc серверов не прочитался — флаг не встаёт: пиры
// непрочитанного сервера иначе не мигрируют никогда. Дерево rc одно на все
// серверы (F546): отказ чтения — ни одного снятия.
func TestMigratePeerAllowIPs_OneServerReadFails_NoFlag(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"}}, false)
	addSecondServer(t, store)
	fg := fakeWithPeers("PEER_A")
	fg.SetJSON("/show/interface/", `{"Wireguard0":{"id":"Wireguard0","type":"Wireguard"},"Wireguard1":{"id":"Wireguard1","type":"Wireguard"}}`)
	fg.SetError("/show/rc/interface/", errors.New("rci down"))
	s := &Service{settings: store, transport: poster, queries: query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})}
	s.MigratePeerAllowIPs(context.Background())
	if len(poster.posts) != 0 {
		t.Fatalf("posts = %v", poster.posts)
	}
	if store.IsManagedPeerAllowIPsMigrated() {
		t.Fatal("флаг встал при непрочитанном сервере")
	}
}

// Fix round 4 (N1): интерфейса managed-сервера нет в прочитанном списке
// (удалён вне панели) — сервер обработан, флаг встаёт, ему ничего не шлётся.
func TestMigratePeerAllowIPs_InterfaceGone_Processed(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"}}, false)
	addSecondServer(t, store) // Wireguard1 на роутере нет
	s := &Service{settings: store, transport: poster, queries: routerWithPeers(t, "PEER_A")}
	s.MigratePeerAllowIPs(context.Background())
	if len(poster.posts) != 1 {
		t.Fatalf("posts = %v", poster.posts)
	}
	if !store.IsManagedPeerAllowIPsMigrated() {
		t.Fatal("флаг не встал: удалённый интерфейс держит миграцию вечно")
	}
}

// Fix round 4 (N1): список интерфейсов не прочитан — ничего не шлём, флаг не ставим.
func TestMigratePeerAllowIPs_InterfaceListFails_NoFlag(t *testing.T) {
	store, poster := seedAllowIPsStore(t, []storage.ManagedPeer{{PublicKey: "PEER_A", TunnelIP: "10.0.0.2/32"}}, false)
	fg := fakeWithPeers("PEER_A")
	fg.SetError("/show/interface/", errors.New("rci down"))
	s := &Service{settings: store, transport: poster, queries: query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})}
	s.MigratePeerAllowIPs(context.Background())
	if len(poster.posts) != 0 || store.IsManagedPeerAllowIPsMigrated() {
		t.Fatalf("posts=%v migrated=%v", poster.posts, store.IsManagedPeerAllowIPsMigrated())
	}
}

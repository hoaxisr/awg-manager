// internal/singbox/awgoutbounds/service_test.go
package awgoutbounds

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

type fakeSingbox struct {
	dir         string
	reloadCalls int
	reloadErr   error
}

func (f *fakeSingbox) ConfigDir() string { return f.dir }
func (f *fakeSingbox) Reload() error {
	f.reloadCalls++
	return f.reloadErr
}

func newSvcWithIface(t *testing.T, awgStore AWGTunnelStore, sysStore SystemTunnelQuery, ifaces ...string) (*ServiceImpl, *fakeSingbox) {
	t.Helper()
	cfgDir := t.TempDir()
	netRoot := t.TempDir()
	for _, n := range ifaces {
		if err := os.MkdirAll(filepath.Join(netRoot, n), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", n, err)
		}
	}
	sb := &fakeSingbox{dir: cfgDir}
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels:    awgStore,
			SystemTunnels: sysStore,
			Singbox:       sb,
		},
		sysClassNet: netRoot,
	}
	return s, sb
}

func TestSync_WritesFileAndReloads(t *testing.T) {
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		nil,
		"t2s0",
	)
	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("SyncAWGOutbounds: %v", err)
	}
	if sb.reloadCalls != 1 {
		t.Errorf("want 1 reload call, got %d", sb.reloadCalls)
	}
	raw, err := os.ReadFile(filepath.Join(sb.dir, "15-awg.json"))
	if err != nil {
		t.Fatalf("read 15-awg.json: %v", err)
	}
	var got fileShape
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Outbounds) != 1 {
		t.Errorf("want 1 outbound, got %d", len(got.Outbounds))
	}
}

func TestReconcile_NoReload(t *testing.T) {
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		nil,
		"t2s0",
	)
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sb.reloadCalls != 0 {
		t.Errorf("want 0 reload calls (Reconcile is reload-free), got %d", sb.reloadCalls)
	}
	if _, err := os.Stat(filepath.Join(sb.dir, "15-awg.json")); err != nil {
		t.Errorf("file should exist: %v", err)
	}
}

func TestSync_Idempotent(t *testing.T) {
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		nil,
		"t2s0",
	)
	for i := 0; i < 3; i++ {
		if err := s.SyncAWGOutbounds(context.Background()); err != nil {
			t.Fatalf("Sync iteration %d: %v", i, err)
		}
	}
	// First Sync writes + reloads; iterations 2 and 3 see identical
	// marshalled payload and skip both write and reload.
	if sb.reloadCalls != 1 {
		t.Errorf("want 1 reload call (first Sync only; identical content skipped), got %d", sb.reloadCalls)
	}
}

// TestSync_RewritesOnChange covers the inverse of TestSync_Idempotent:
// when the catalog changes between Syncs, writeFile must re-emit and reload.
// Guards against an over-aggressive skip that would freeze the file.
func TestSync_RewritesOnChange(t *testing.T) {
	store := &fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}}
	s, sb := newSvcWithIface(t, store, nil, "t2s0", "t2s1")

	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("Sync #1: %v", err)
	}
	// Add a second tunnel and Sync again — payload differs, must reload.
	store.tunnels = append(store.tunnels, AWGTunnelInfo{ID: "b", Name: "B", BackendIface: "t2s1"})
	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("Sync #2: %v", err)
	}
	if sb.reloadCalls != 2 {
		t.Errorf("want 2 reload calls (initial + post-change), got %d", sb.reloadCalls)
	}
}

func TestListTags_MatchesEnumerate(t *testing.T) {
	s, _ := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		&fakeSystemStore{tunnels: []SystemTunnelInfo{{ID: "Wireguard0", InterfaceName: "nwg0", Description: "W0"}}},
		"t2s0", "nwg0",
	)
	tags, err := s.ListTags(context.Background())
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("want 2 tags, got %d", len(tags))
	}
	if tags[0].Tag != "awg-a" || tags[0].Kind != "managed" {
		t.Errorf("tag 0 wrong: %+v", tags[0])
	}
	if tags[1].Tag != "awg-sys-Wireguard0" || tags[1].Kind != "system" {
		t.Errorf("tag 1 wrong: %+v", tags[1])
	}
}

func TestSync_NilSingbox_DoesNotReload(t *testing.T) {
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels: &fakeAWGStore{tunnels: nil},
		},
	}
	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("Sync with nil Singbox should be safe: %v", err)
	}
}

// countingAWGStore считает обращения подписчика: enumerate дёргает List на
// каждом SyncAWGOutbounds, поэтому ненулевой счётчик = «проснулся».
// Атомарный, потому что пишет горутина подписчика, а читает тест.
type countingAWGStore struct {
	calls atomic.Int32
}

func (c *countingAWGStore) List(ctx context.Context) ([]AWGTunnelInfo, error) {
	c.calls.Add(1)
	return nil, nil
}

// F81: фильтр подписчика ждал ключ "system-tunnels", которого не публиковал
// НИКТО — константы с таким значением нет в events.AllResources, во фронтовом
// union нет, публикатора в прод-коде нет. Точный близнец F66. Ниша, ради
// которой ключ заводился (NDMS-хук на out-of-band смену системного WG), уже
// обслужена живым ключом: хук публикует ResourceTunnels с reason "ndms-hook"
// (internal/server/server_routes.go).
func TestSubscribeBus_WakesOnTunnelsNotOnDeadKey(t *testing.T) {
	store := &countingAWGStore{}
	bus := events.NewBus()
	s := &ServiceImpl{
		deps: Deps{
			AWGTunnels:    store,
			SystemTunnels: &fakeSystemStore{},
			Bus:           bus,
		},
		sysClassNet: t.TempDir(),
	}

	unsub := s.SubscribeBus(context.Background())
	defer unsub()

	// Снесённый ключ будить не должен. Settle-окно обязательно — без него
	// ассерт прошёл бы зелёным и на старом коде, просто не дождавшись.
	bus.PublishInvalidated(events.Resource("system-tunnels"), "test")
	time.Sleep(500 * time.Millisecond)
	if got := store.calls.Load(); got != 0 {
		t.Fatalf("мёртвый ключ system-tunnels разбудил синк (обращений: %d)", got)
	}

	// Живой ключ будить обязан.
	bus.PublishInvalidated(events.ResourceTunnels, "test")
	deadline := time.Now().Add(3 * time.Second)
	for store.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if store.calls.Load() == 0 {
		t.Error("живой ключ tunnels не разбудил синк")
	}
}

// F83: отказ NDMS при перечислении системных туннелей глотался, и enumerate
// возвращала managed-only с nil-ошибкой — 15-awg.json переписывался БЕЗ всех
// awg-sys-*, а «изменение» ещё и триггерило reload. Транзиентный сбой молча
// выносил системные туннели из конфига движка.
func TestSync_SystemStoreFailureKeepsFile(t *testing.T) {
	sysStore := &fakeSystemStore{
		tunnels: []SystemTunnelInfo{{ID: "sys1", InterfaceName: "nwg0", Description: "WG"}},
	}
	s, sb := newSvcWithIface(t,
		&fakeAWGStore{tunnels: []AWGTunnelInfo{{ID: "a", Name: "A", BackendIface: "t2s0"}}},
		sysStore,
		"t2s0", "nwg0",
	)

	if err := s.SyncAWGOutbounds(context.Background()); err != nil {
		t.Fatalf("первый синк: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(sb.dir, "15-awg.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var seed fileShape
	if err := json.Unmarshal(before, &seed); err != nil {
		t.Fatal(err)
	}
	if len(seed.Outbounds) != 2 {
		t.Fatalf("подготовка: ждём managed+system, получено %d", len(seed.Outbounds))
	}
	reloadsBefore := sb.reloadCalls

	// NDMS отвалился.
	sysStore.err = errors.New("ndms unavailable")
	if err := s.SyncAWGOutbounds(context.Background()); err == nil {
		t.Error("отказ NDMS проглочен: SyncAWGOutbounds вернул nil")
	}

	after, err := os.ReadFile(filepath.Join(sb.dir, "15-awg.json"))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("15-awg.json переписан без системных записей:\nбыло: %s\nстало: %s", before, after)
	}
	if sb.reloadCalls != reloadsBefore {
		t.Errorf("движок перезагружен на неполном конфиге: reload'ов %d → %d", reloadsBefore, sb.reloadCalls)
	}
}

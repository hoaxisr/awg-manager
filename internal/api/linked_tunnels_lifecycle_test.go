package api

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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
	if err := store.Create(tun); err != nil {
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
	if err := store.Create(tun); err != nil {
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
		if err := store.Create(tun); err != nil {
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

// stateSvc — TunnelService с моделью состояния: ЖУРНАЛ вызовов Start/Stop, а
// не счётчик. Факт вызова тут ничего не доказывает — доказывает состав: какие
// именно записи подняты и опущены.
type stateSvc struct {
	TunnelService
	running map[string]bool
	// state — точное состояние поверх running: нужно, чтобы отличить
	// «поднимается» от «поднят». Оба считаются поднятыми.
	state   map[string]tunnel.State
	started []string
	stopped []string
}

func (s *stateSvc) GetState(_ context.Context, id string) tunnel.StateInfo {
	if st, ok := s.state[id]; ok {
		return tunnel.StateInfo{State: st}
	}
	if s.running[id] {
		return tunnel.StateInfo{State: tunnel.StateRunning}
	}
	return tunnel.StateInfo{State: tunnel.StateStopped}
}

func (s *stateSvc) Start(_ context.Context, id string) error {
	s.started = append(s.started, id)
	s.running[id] = true
	return nil
}

func (s *stateSvc) Stop(_ context.Context, id string) error {
	s.stopped = append(s.stopped, id)
	s.running[id] = false
	return nil
}

func proxyStateStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awgm-wd", Name: "WD", WdttClientID: "client-a", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}},
		{ID: "wdttraw-client-a", Name: "RAW", WdttClientID: "client-a", Backend: backendWdttRaw},
		{ID: "awgm-ft", Name: "FT", FreeTurnClientID: "client-a", Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9001"}},
	} {
		if err := store.Create(tun); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// Подъём и остановка по полю связи: raw-зеркало WDTT исключено (паритет
// tunnelLinkedAwgOnly), чужое поле связи не трогается.
func TestSetLinkedProxyTunnelsState(t *testing.T) {
	store := proxyStateStore(t)
	svc := &stateSvc{running: map[string]bool{}}

	changed, failed := SetLinkedProxyTunnelsState(context.Background(), store, svc,
		LinkedWdtt, "client-a", true)
	if len(failed) != 0 || len(changed) != 1 || changed[0] != "awgm-wd" {
		t.Fatalf("подъём wdtt: changed=%v failed=%v", changed, failed)
	}
	if len(svc.started) != 1 || svc.started[0] != "awgm-wd" {
		t.Fatalf("подняты: %v (зеркало и чужой туннель обязаны остаться)", svc.started)
	}

	changed, failed = SetLinkedProxyTunnelsState(context.Background(), store, svc,
		LinkedWdtt, "client-a", false)
	if len(failed) != 0 || len(changed) != 1 || changed[0] != "awgm-wd" {
		t.Fatalf("остановка wdtt: changed=%v failed=%v", changed, failed)
	}
	if len(svc.stopped) != 1 || svc.stopped[0] != "awgm-wd" {
		t.Fatalf("опущены: %v", svc.stopped)
	}

	changed, _ = SetLinkedProxyTunnelsState(context.Background(), store, svc,
		LinkedFreeTurn, "client-a", true)
	if len(changed) != 1 || changed[0] != "awgm-ft" {
		t.Fatalf("подъём freeturn: %v", changed)
	}
}

// Список для прокси-рантайма несёт признаки, по которым ресурс считает
// расхождение: состояние и участие в жизненном цикле.
func TestListLinkedProxyTunnels(t *testing.T) {
	store := proxyStateStore(t)
	svc := &stateSvc{running: map[string]bool{"awgm-wd": true}}

	list, err := ListLinkedProxyTunnels(context.Background(), store, svc, LinkedWdtt, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("список: %+v (связь включает зеркало)", list)
	}
	byID := map[string]LinkedProxyTunnel{}
	for _, it := range list {
		byID[it.ID] = it
	}
	wd, raw := byID["awgm-wd"], byID["wdttraw-client-a"]
	if !wd.Lifecycle || !wd.Running || wd.Endpoint != "127.0.0.1:9000" {
		t.Fatalf("wg-туннель: %+v", wd)
	}
	if raw.Lifecycle || raw.Running {
		t.Fatalf("raw-зеркало вне жизненного цикла: %+v", raw)
	}

	// Starting — уже поднят: старый мир пропускал такой туннель и на старте,
	// и на остановке. Иначе рантайм звал бы Start по второму разу.
	starting := &stateSvc{running: map[string]bool{},
		state: map[string]tunnel.State{"awgm-wd": tunnel.StateStarting}}
	list, err = ListLinkedProxyTunnels(context.Background(), store, starting, LinkedWdtt, "client-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list {
		if it.ID == "awgm-wd" && !it.Running {
			t.Fatalf("starting обязан читаться как поднятый: %+v", it)
		}
	}
}

// Неизвестное поле связи — дефект проводки, а не пустой список: молчаливое
// «ничего не нашли» навсегда спрятало бы неподнятый туннель.
func TestSetLinkedProxyTunnelsStateUnknownField(t *testing.T) {
	store := proxyStateStore(t)
	_, failed := SetLinkedProxyTunnelsState(context.Background(), store,
		&stateSvc{running: map[string]bool{}}, LinkedField(42), "client-a", true)
	if len(failed) == 0 {
		t.Fatal("неизвестное поле связи проглочено")
	}
	if _, err := ListLinkedProxyTunnels(context.Background(), store, nil, LinkedField(42), "client-a"); err == nil {
		t.Fatal("неизвестное поле связи проглочено списком")
	}
}

// Д1: доводка адреса прокси-рантайма обходит зеркало стороной. Адрес реле в
// записи зеркала — не дрейф: его пишет зеркало прокси-рантайма, и
// переписывать его на локальный порт нельзя (парный комментарий —
// linked_tunnels_lifecycle.go:253-254).
func TestSyncLinkedProxyEndpointsSkipsMirror(t *testing.T) {
	store := proxyStateStore(t)
	// Зеркало уже посеяно фикстурой — здесь ему дописывается адрес реле.
	if err := store.Update("wdttraw-client-a", func(mirror *storage.AWGTunnel) error {
		mirror.Peer.Endpoint = "vps.example:56003"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	updated, failed := SyncLinkedProxyEndpoints(context.Background(), store, nil,
		LinkedWdtt, "client-a", "127.0.0.1:9007")
	if len(failed) != 0 {
		t.Fatalf("failed = %v", failed)
	}
	if len(updated) != 1 || updated[0] != "awgm-wd" {
		t.Fatalf("updated = %v (зеркало трогать нельзя)", updated)
	}
	got, err := store.Get("wdttraw-client-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "vps.example:56003" {
		t.Fatalf("адрес зеркала переписан на %q", got.Peer.Endpoint)
	}
}

// TestTunnelLinkedToFreeTurnClient переехал из freeturn_linked_test.go вместе
// с самим предикатом.
func TestTunnelLinkedToFreeTurnClient(t *testing.T) {
	tun := storage.AWGTunnel{ID: "awgm1", FreeTurnClientID: "client-a"}
	if !tunnelLinkedToFreeTurnClient(tun, "client-a") {
		t.Fatal("tagged tunnel must match its client id")
	}
	if tunnelLinkedToFreeTurnClient(tun, "client-b") {
		t.Fatal("must not match another client id")
	}
	if tunnelLinkedToFreeTurnClient(storage.AWGTunnel{Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"}}, "client-a") {
		t.Fatal("manual tunnel without tag must not match")
	}
}

// F8: персист endpoint'а писал снимок, снятый ДО svc.Update, записью целиком —
// runtime-поля, которые оркестратор успел записать за время RCI-обмена,
// откатывались обратно.
func TestSyncLinkedAwgTunnelEndpoints_KeepsConcurrentRuntimeWrites(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
	if err := store.Create(&storage.AWGTunnel{
		ID: "awgm1", Name: "FT", FreeTurnClientID: "client-a",
		Enabled: true, ActiveWAN: "ISP", StartedAt: "2026-08-29T10:00:00Z",
		Peer: storage.AWGPeer{Endpoint: "127.0.0.1:9000"},
	}); err != nil {
		t.Fatal(err)
	}
	// Пока svc.Update «ходит в RCI», запись переписывает оркестратор.
	svc := &stubTunnelSvc{updateFn: func(context.Context, *storage.AWGTunnel, *storage.AWGTunnel) error {
		return store.Update("awgm1", func(fresh *storage.AWGTunnel) error {
			fresh.ActiveWAN = "Wireguard2"           // WAN-failover в окне
			fresh.StartedAt = "2026-08-29T11:00:00Z" // рестарт
			fresh.Enabled = false                    // suspend оркестратором
			return nil
		})
	}}

	updated, errs := syncLinkedAwgTunnelEndpoints(context.Background(), store, svc, nil, func(t storage.AWGTunnel) bool {
		return tunnelLinkedToFreeTurnClient(t, "client-a")
	}, "127.0.0.1:9001")
	if len(errs) != 0 || len(updated) != 1 {
		t.Fatalf("updated=%v errs=%v", updated, errs)
	}

	got, err := store.Get("awgm1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Peer.Endpoint != "127.0.0.1:9001" {
		t.Fatalf("endpoint не записан: %q", got.Peer.Endpoint)
	}
	if got.ActiveWAN != "Wireguard2" || got.StartedAt != "2026-08-29T11:00:00Z" || got.Enabled {
		t.Fatalf("runtime-поля затёрты снимком, снятым до svc.Update: ActiveWAN=%q StartedAt=%q Enabled=%v",
			got.ActiveWAN, got.StartedAt, got.Enabled)
	}
}

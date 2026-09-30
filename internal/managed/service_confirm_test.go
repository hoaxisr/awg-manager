package managed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// oracleGetter — FakeNDMS плюс ответы по путям вне его модели (rc маршрутов,
// rc пиров с содержимым).
type oracleGetter struct {
	*query.FakeNDMS
	raw map[string]string
}

func (g oracleGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if r, ok := g.raw[path]; ok {
		return []byte(r), nil
	}
	return g.FakeNDMS.GetRaw(ctx, path)
}

// Post: ключ raw "POST <payload JSON>" — ответ на чтение POST-ом (show
// interface записи); остальное — FakeNDMS.
func (g oracleGetter) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if r, ok := g.raw["POST "+string(b)]; ok {
		return json.RawMessage(r), nil
	}
	return g.FakeNDMS.Post(ctx, payload)
}

func (g oracleGetter) Get(ctx context.Context, path string, dst any) error {
	raw, err := g.GetRaw(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// newServiceWithOracle — Service над оракулом: FakeNDMS — и getter, и poster
// (rci и command.Commands), servers — в хранилище. raw — пути вне модели
// FakeNDMS (nil — нет).
func newServiceWithOracle(t *testing.T, f *query.FakeNDMS, raw map[string]string, servers ...storage.ManagedServer) *Service {
	t.Helper()
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	for _, sv := range servers {
		if err := store.AddManagedServer(sv); err != nil {
			t.Fatalf("seed %s: %v", sv.InterfaceName, err)
		}
	}
	g := oracleGetter{FakeNDMS: f, raw: raw}
	ifaces := query.NewInterfaceStore(g, query.NopLogger())
	queries := &query.Queries{
		Interfaces:    ifaces,
		WGServers:     query.NewWGServerStore(g, query.NopLogger(), ifaces),
		RunningConfig: query.NewRunningConfigStore(g, query.NopLogger()),
		StaticRoutes:  query.NewStaticRouteStore(g, query.NopLogger()),
		Routes:        query.NewRouteStore(g, query.NopLogger()),
	}
	sc := command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil)
	cmds := command.NewCommands(command.Deps{Poster: f, Save: sc, Queries: queries})
	svc := New(f, sc, queries, cmds, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	svc.wgRun = func(context.Context, string, ...string) (string, error) {
		return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\n", nil
	}
	svc.keyGen = &fakeKeyGen{}
	return svc
}

// confirmed — доказательство для вызовов rci* в тестах: берётся у отдельного
// FakeNDMS, где name есть.
func confirmed(t *testing.T, name string) query.Confirmed {
	t.Helper()
	s := query.NewInterfaceStore(query.NewFakeNDMS(ndms.Interface{ID: name}), query.NopLogger())
	c, _, ok, err := s.Confirm(context.Background(), name)
	if err != nil || !ok {
		t.Fatalf("confirm %s: ok=%v err=%v", name, ok, err)
	}
	return c
}

// listsDuring — сколько полных списков прочитано за call; карта интерфейсов
// загружена заранее (в проде она тёплая с загрузки демона).
func listsDuring(t *testing.T, s *Service, f *query.FakeNDMS, call func()) int {
	t.Helper()
	if _, err := s.queries.Interfaces.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := f.ListCalls()
	call()
	return f.ListCalls() - before
}

func TestDelete_InterfaceGoneOutside_NoCommandsRecordRemoved(t *testing.T) { // F547
	f := query.NewFakeNDMS() // Wireguard3 снят мимо панели
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3", NATMode: "full", LANSegments: []string{"Bridge0"}})
	if err := s.Delete(context.Background(), "Wireguard3"); err != nil {
		t.Fatal(err)
	}
	if f.Phantoms != 0 || f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("phantoms=%d E=%d posts=%v", f.Phantoms, f.E, f.Posts)
	}
	if _, ok := s.settings.GetManagedServerByID("Wireguard3"); ok {
		t.Fatal("storage record must be removed")
	}
	if s.saveCoord.Status().State == command.SaveStatePending {
		t.Fatal("no save without commands")
	}
}

func TestDelete_ListError_FailsClosed(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3"})
	f.FailList(errors.New("rci down"))
	if err := s.Delete(context.Background(), "Wireguard3"); err == nil {
		t.Fatal("must fail closed")
	}
	if _, ok := s.settings.GetManagedServerByID("Wireguard3"); !ok {
		t.Fatal("storage must stay for a retry")
	}
	if len(f.Posts) != 0 {
		t.Fatalf("no commands on unknown state: %v", f.Posts)
	}
}

// R16: NAT снимается по доказательству ДО `no interface`, `down` не шлётся.
func TestDelete_Present_NATThenNoInterface_NoDown(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard", State: "up"})
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3", NATMode: "full"})
	if err := s.Delete(context.Background(), "Wireguard3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"ip":{"nat":[{"interface":"Wireguard3","no":true}]}}`,
		`{"interface":{"Wireguard3":{"no":true}}}`,
	}
	if strings.Join(f.Posts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("posts:\n%s\nwant:\n%s", strings.Join(f.Posts, "\n"), strings.Join(want, "\n"))
	}
	if f.Phantoms != 0 || f.E != 0 || f.Has("Wireguard3") {
		t.Fatalf("phantoms=%d E=%d has=%v", f.Phantoms, f.E, f.Has("Wireguard3"))
	}
	if _, ok, _ := s.queries.Interfaces.Lookup(context.Background(), "Wireguard3"); ok {
		t.Fatal("снятый интерфейс остался в кэше")
	}
	if _, ok := s.settings.GetManagedServerByID("Wireguard3"); ok {
		t.Fatal("storage record must be removed")
	}
}

// Create: список FindFreeIndex и один Confirm после создания; команды
// списка не перечитывают (прежде — InvalidateAll на каждый пост).
func TestCreate_OneListThenNoListsPerPost(t *testing.T) {
	f := query.NewFakeNDMS()
	f.ExpectCreate("Wireguard0")
	s := newServiceWithOracle(t, f, map[string]string{
		`POST {"show":{"interface":{"system-name":{"name":"Wireguard0"}}}}`: `{"show":{"interface":{"system-name":"nwg0"}}}`,
	})
	no := false
	var sv *storage.ManagedServer
	lists := listsDuring(t, s, f, func() {
		var err error
		sv, err = s.Create(context.Background(), CreateServerRequest{Address: "10.77.0.1", Mask: "24", ListenPort: 51820, GenerateASC: &no})
		if err != nil {
			t.Fatal(err)
		}
	})
	if sv.InterfaceName != "Wireguard0" {
		t.Fatalf("iface %s", sv.InterfaceName)
	}
	if lists != 2 { // FindFreeIndex + Confirm
		t.Fatalf("list reads = %d, want 2 (FindFreeIndex + Confirm)", lists)
	}
	if f.Phantoms != 0 || f.E != 0 || len(f.Created) != 1 {
		t.Fatalf("phantoms=%d E=%d created=%v", f.Phantoms, f.E, f.Created)
	}
}

func TestSetEnabled_InterfaceGone_ErrorNoCommands(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3"})
	for _, call := range []func() error{
		func() error { return s.SetEnabled(context.Background(), "Wireguard3", true) },
		func() error { return s.RestartOrStart(context.Background(), "Wireguard3") },
		func() error { return s.SetNATMode(context.Background(), "Wireguard3", "none") },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "интерфейс Wireguard3 снят в NDMS") {
			t.Fatalf("err = %v", err)
		}
	}
	if f.Phantoms != 0 || f.E != 0 || len(f.Posts) != 0 {
		t.Fatalf("phantoms=%d E=%d posts=%v", f.Phantoms, f.E, f.Posts)
	}
}

// F552: снимок занятых — одним списком, сколько бы серверов ни было.
func TestOccupiedSubnets_OneListRead(t *testing.T) {
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard", Address: "10.1.0.1", Mask: "255.255.255.0"},
		ndms.Interface{ID: "Wireguard2", Type: "Wireguard", Address: "10.2.0.1", Mask: "255.255.255.0"},
		ndms.Interface{ID: "Wireguard3", Type: "Wireguard", Address: "10.3.0.1", Mask: "255.255.255.0"},
	)
	s := newServiceWithOracle(t, f, map[string]string{"/show/rc/ip/route": `[]`},
		storage.ManagedServer{InterfaceName: "Wireguard1"},
		storage.ManagedServer{InterfaceName: "Wireguard2"},
		storage.ManagedServer{InterfaceName: "Wireguard3"},
	)
	lists := listsDuring(t, s, f, func() {
		occ, err := s.OccupiedSubnets(context.Background(), PeerRef{})
		if err != nil {
			t.Fatal(err)
		}
		if len(occ) != 3 {
			t.Fatalf("occupied = %v", occ)
		}
	})
	if lists != 1 {
		t.Fatalf("list reads = %d, want 1", lists)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// Сервер, которого карта не знала (потерян ifcreated), попадает в проверку:
// тот же список показывает его, второй подтверждает.
func TestOccupiedSubnets_BuiltInMissedByCache_StillRead(t *testing.T) {
	f := query.NewFakeNDMS()
	f.SetRC("Wireguard0", json.RawMessage(`{"wireguard":{"peer":[{"key":"K","allow-ips":[{"address":"192.168.50.0","mask":"255.255.255.0"}]}]}}`))
	s := newServiceWithOracle(t, f, map[string]string{
		"/show/rc/ip/route": `[]`,
	})
	if _, err := s.queries.Interfaces.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: ndms.BuiltInVPNServerDescription})
	f.DrainHooks() // хук потерян
	occ, err := s.OccupiedSubnets(context.Background(), PeerRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(occ) != 1 || occ[0].Net.String() != "192.168.50.0/24" {
		t.Fatalf("occupied = %v", occ)
	}
}

func restoreServerWithPeers(n int) ManagedServerExport {
	sv := ManagedServerExport{
		InterfaceName: "Wireguard1", Address: "10.99.0.1", Mask: "255.255.255.0",
		ListenPort: 51900, PrivateKey: validPrivateKey(1), NATMode: "none",
	}
	for i := 0; i < n; i++ {
		sv.Peers = append(sv.Peers, storage.ManagedPeer{
			PublicKey:     validPeerKey(byte(10 + i)),
			TunnelIP:      "10.99.0." + string(rune('2'+i)) + "/32",
			Enabled:       true,
			RemoteSubnets: []string{"192.168.10" + string(rune('0'+i)) + ".0/24"},
		})
	}
	return sv
}

func restoreRCPeers(sv ManagedServerExport) string {
	var peers []map[string]any
	for _, p := range sv.Peers {
		peers = append(peers, map[string]any{"key": p.PublicKey})
	}
	b, _ := json.Marshal(map[string]any{"wireguard": map[string]any{"peer": peers}})
	return string(b)
}

// Восстановление сервера читает список не на каждого пира: подтверждение
// созданного плюс один снимок занятых — при любом числе пиров с сетями.
func TestRestore_ListReadsDoNotGrowWithPeers(t *testing.T) {
	for _, n := range []int{1, 3} {
		sv := restoreServerWithPeers(n)
		f := query.NewFakeNDMS()
		f.ExpectCreate("Wireguard1")
		f.SetRC("Wireguard1", json.RawMessage(restoreRCPeers(sv)))
		s := newServiceWithOracle(t, f, map[string]string{
			"/show/rc/ip/route":    `[]`,
			"/show/running-config": `{"message":[]}`,
		})
		var out []RestoreOutcome
		lists := listsDuring(t, s, f, func() {
			out = s.Restore(context.Background(), []ManagedServerExport{sv}, RestoreOptions{})
		})
		if len(out) != 1 || out[0].Action != "created" {
			t.Fatalf("n=%d outcome %+v", n, out)
		}
		got, _ := s.settings.GetManagedServerByID("Wireguard1")
		for _, p := range got.Peers {
			if len(p.RemoteSubnets) != 1 {
				t.Fatalf("n=%d: сети пира не восстановлены: %+v", n, got.Peers)
			}
		}
		// Confirm создания + снимок занятых + одно чтение карты по метке
		// «грязно» после настройки сервера (F546: память после нашей записи
		// читает свежий список) — от числа пиров не зависит.
		if lists != 3 {
			t.Fatalf("n=%d: list reads = %d, want 3", n, lists)
		}
		if f.Phantoms != 0 || f.E != 0 {
			t.Fatalf("n=%d: phantoms=%d E=%d", n, f.Phantoms, f.E)
		}
	}
}

// Мерж в живой сервер — ОДИН список: снимок занятых подтверждает и сервер.
func TestRestoreMerge_OneListRead(t *testing.T) {
	sv := restoreServerWithPeers(3)
	existing := sv
	existing.Peers = nil
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard1", Type: "Wireguard", Address: "10.99.0.1", Mask: "255.255.255.0"})
	pub := mustDerivePublicKey(t, sv.PrivateKey)
	f.SetRC("Wireguard1", json.RawMessage(restoreRCPeers(sv)))
	s := newServiceWithOracle(t, f, map[string]string{
		"/show/rc/ip/route":    `[]`,
		"/show/running-config": `{"message":[]}`,
	}, existing)
	// WGServers.Get: ключ живого сервера — тот же, что в бэкапе; runtime — из
	// снимка списка (F546).
	f.SetDetail("Wireguard1", json.RawMessage(`{"wireguard":{"public-key":"`+pub+`"}}`))
	if live, err := s.queries.WGServers.Get(context.Background(), "Wireguard1"); err != nil || live.PublicKey != pub {
		t.Fatalf("живой ключ не читается фикстурой: %+v %v", live, err)
	}
	var out []RestoreOutcome
	lists := listsDuring(t, s, f, func() {
		out = s.Restore(context.Background(), []ManagedServerExport{sv}, RestoreOptions{})
	})
	if len(out) != 1 || out[0].Action != "merged" || out[0].AddedPeers != 3 {
		t.Fatalf("outcome %+v", out)
	}
	if lists != 1 {
		t.Fatalf("list reads = %d, want 1", lists)
	}
	if f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("phantoms=%d E=%d", f.Phantoms, f.E)
	}
}

// Миграция allow-ips: один список на все серверы, не по списку на сервер.
func TestMigratePeerAllowIPs_OneListRead(t *testing.T) {
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard2", Type: "Wireguard"},
	)
	peer := func(k string) []storage.ManagedPeer { return []storage.ManagedPeer{{PublicKey: k}} }
	f.SetRC("Wireguard1", json.RawMessage(`{"wireguard":{"peer":[{"key":"A"}]}}`))
	f.SetRC("Wireguard2", json.RawMessage(`{"wireguard":{"peer":[{"key":"B"}]}}`))
	s := newServiceWithOracle(t, f, map[string]string{},
		storage.ManagedServer{InterfaceName: "Wireguard1", Peers: peer("A")},
		storage.ManagedServer{InterfaceName: "Wireguard2", Peers: peer("B")},
		storage.ManagedServer{InterfaceName: "Wireguard3", Peers: peer("C")}, // снят мимо панели
	)
	if err := s.settings.SetManagedPeerAllowIPsMigrated(false); err != nil {
		t.Fatal(err)
	}
	lists := listsDuring(t, s, f, func() { s.MigratePeerAllowIPs(context.Background()) })
	if lists != 1 {
		t.Fatalf("list reads = %d, want 1", lists)
	}
	if len(f.Posts) != 2 || f.Phantoms != 0 || f.E != 0 {
		t.Fatalf("posts=%v phantoms=%d E=%d", f.Posts, f.Phantoms, f.E)
	}
}

// internet-only: выход, которого нет в NDMS, не называется ни в постановке,
// ни в снятии хвоста — ссылка на него была бы E в журнале ndm.
func TestSetNATMode_AbsentExitsSkipped(t *testing.T) {
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Wireguard3", Type: "Wireguard"},
		ndms.Interface{ID: "PPPoE0", Type: "PPPoE"},
	)
	s := newServiceWithOracle(t, f, map[string]string{
		// Wireguard2 — выход в running-config, но интерфейса уже нет.
		"/show/running-config": `{"message":["interface PPPoE0","    ip global 32767","!","interface Wireguard2","    ip global 100","!"]}`,
	}, storage.ManagedServer{InterfaceName: "Wireguard3", NATMode: "internet-only", NATStaticWANs: []string{"ISP"}})
	if err := s.SetNATMode(context.Background(), "Wireguard3", "internet-only"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"ip":{"static":{"interface":"Wireguard3","to-interface":"PPPoE0"}}}`,
		`{"ip":{"nat":[{"interface":"Wireguard3","no":true}]}}`,
	}
	if strings.Join(f.Posts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("posts:\n%s", strings.Join(f.Posts, "\n"))
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
	sv, _ := s.settings.GetManagedServerByID("Wireguard3")
	if len(sv.NATStaticWANs) != 1 || sv.NATStaticWANs[0] != "PPPoE0" {
		t.Fatalf("NATStaticWANs = %v", sv.NATStaticWANs)
	}
}

// Снятие у интерфейса, которого нет, — снимать нечего; постановка — отказ.
// Ни в одном случае команд нет.
func TestApplyToInterface_Absent(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newServiceWithOracle(t, f, nil)
	ctx := context.Background()
	if err := s.ApplyLANSegmentsToInterface(ctx, "OpkgTun7", "", "", nil); err != nil {
		t.Fatalf("teardown LAN: %v", err)
	}
	if _, err := s.ApplyNATModeToInterface(ctx, "OpkgTun7", "none", []string{"PPPoE0"}); err != nil {
		t.Fatalf("teardown NAT: %v", err)
	}
	if err := s.ApplyPolicyToInterface(ctx, "OpkgTun7", "none"); err != nil {
		t.Fatalf("teardown policy: %v", err)
	}
	if _, err := s.ApplyNATModeToInterface(ctx, "OpkgTun7", "full", nil); err == nil || !strings.Contains(err.Error(), "OpkgTun7") {
		t.Fatalf("постановка NAT на отсутствующий: %v", err)
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d phantoms=%d", f.Posts, f.E, f.Phantoms)
	}
}

// Пир сервера, чей интерфейс снят мимо панели: снимать с роутера нечего,
// запись пира удаляется без команд.
func TestDeletePeer_InterfaceGone_RecordOnly(t *testing.T) {
	// Бридж есть: правила ACL сетей пира собрать можно — не шлём их всё равно.
	f := query.NewFakeNDMS(ndms.Interface{ID: "Bridge0", Type: "Bridge", Address: "192.168.1.1", Mask: "255.255.255.0"})
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{
		InterfaceName: "Wireguard3", LANSegments: []string{"Bridge0"},
		Peers: []storage.ManagedPeer{{PublicKey: "K", RemoteSubnets: []string{"192.168.5.0/24"}}},
	})
	if err := s.DeletePeer(context.Background(), "Wireguard3", "K"); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d phantoms=%d", f.Posts, f.E, f.Phantoms)
	}
	if sv, _ := s.settings.GetManagedServerByID("Wireguard3"); len(sv.Peers) != 0 {
		t.Fatalf("peers = %+v", sv.Peers)
	}
}

// Правка одних полей записи (endpoint, DNS) у сервера со снятым интерфейсом
// проходит без подтверждения и без команд.
func TestUpdate_RecordOnlyFields_InterfaceGone(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{
		InterfaceName: "Wireguard3", Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820,
	})
	ep := "vpn.example.org"
	if err := s.Update(context.Background(), "Wireguard3", UpdateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51820, Endpoint: &ep,
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d phantoms=%d", f.Posts, f.E, f.Phantoms)
	}
	if sv, _ := s.settings.GetManagedServerByID("Wireguard3"); sv.Endpoint != ep {
		t.Fatalf("endpoint = %q", sv.Endpoint)
	}
	// Правка, которую надо слать в NDMS, — отказ без команд.
	if err := s.Update(context.Background(), "Wireguard3", UpdateServerRequest{
		Address: "10.66.66.1", Mask: "255.255.255.0", ListenPort: 51821,
	}); err == nil || !strings.Contains(err.Error(), "интерфейс Wireguard3 снят в NDMS") {
		t.Fatalf("err = %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("posts=%v", f.Posts)
	}
}

// failListAfterPost — poster, после первого поста которого список интерфейсов
// перестаёт читаться: сбой приходится на подтверждение посреди потока.
type failListAfterPost struct{ f *query.FakeNDMS }

func (p failListAfterPost) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	r, err := p.f.Post(ctx, payload)
	p.f.FailList(errors.New("rci down"))
	return r, err
}

// internet-only → full: выходы не подтвердились — static NAT не снят, и
// запись не смеет сказать «снят» (иначе правила-сироты на роутере).
func TestSetNATMode_FullAfterInternetOnly_WANListError_KeepsStored(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"}, ndms.Interface{ID: "PPPoE0"})
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3", NATMode: "internet-only", NATStaticWANs: []string{"PPPoE0"}})
	s.transport = failListAfterPost{f}
	if err := s.SetNATMode(context.Background(), "Wireguard3", "full"); err == nil {
		t.Fatal("сбой чтения выходов принят за успех")
	}
	sv, _ := s.settings.GetManagedServerByID("Wireguard3")
	if sv.NATMode != "internet-only" || len(sv.NATStaticWANs) != 1 || sv.NATStaticWANs[0] != "PPPoE0" {
		t.Fatalf("запись изменена: mode=%s wans=%v", sv.NATMode, sv.NATStaticWANs)
	}
	for _, p := range f.Posts {
		if strings.Contains(p, `"static"`) {
			t.Fatalf("static по неподтверждённому выходу: %s", p)
		}
	}
}

func TestDelete_PresentInternetOnly_StaticThenNoInterface(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"}, ndms.Interface{ID: "PPPoE0"})
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3", NATMode: "internet-only", NATStaticWANs: []string{"PPPoE0"}})
	if err := s.Delete(context.Background(), "Wireguard3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"ip":{"static":[{"interface":"Wireguard3","no":true,"to-interface":"PPPoE0"}]}}`,
		`{"interface":{"Wireguard3":{"no":true}}}`,
	}
	if strings.Join(f.Posts, "\n") != strings.Join(want, "\n") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts:\n%s\nE=%d phantoms=%d", strings.Join(f.Posts, "\n"), f.E, f.Phantoms)
	}
}

func TestDelete_PresentLANSegments_ACLThenNoInterface(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	s := newServiceWithOracle(t, f, map[string]string{
		"/show/running-config": `{"message":["access-list AWGM_Wireguard3","    permit ip 10.66.66.0 255.255.255.0 192.168.1.0 255.255.255.0","!","interface Wireguard3","    ip access-group AWGM_Wireguard3 in","!"]}`,
	}, storage.ManagedServer{InterfaceName: "Wireguard3", NATMode: "none", LANSegments: []string{"Bridge0"}})
	if err := s.Delete(context.Background(), "Wireguard3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"parse":"no interface Wireguard3 ip access-group AWGM_Wireguard3 in"}`,
		`{"parse":"no access-list AWGM_Wireguard3"}`,
		`{"interface":{"Wireguard3":{"no":true}}}`,
	}
	// Чтения POST-ом (show interface после правки) — не команды.
	var cmds []string
	for _, p := range f.Posts {
		if !strings.HasPrefix(p, `{"show"`) {
			cmds = append(cmds, p)
		}
	}
	if strings.Join(cmds, "\n") != strings.Join(want, "\n") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("commands:\n%s\nE=%d phantoms=%d", strings.Join(cmds, "\n"), f.E, f.Phantoms)
	}
}

// Решение 4: список не прочитан — отказ без команд.
func TestListError_SetEnabledAndDeletePeer_NoCommands(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	s := newServiceWithOracle(t, f, nil, storage.ManagedServer{InterfaceName: "Wireguard3",
		Peers: []storage.ManagedPeer{{PublicKey: "K"}}})
	f.FailList(errors.New("rci down"))
	if err := s.SetEnabled(context.Background(), "Wireguard3", false); err == nil {
		t.Fatal("SetEnabled: must fail closed")
	}
	if err := s.DeletePeer(context.Background(), "Wireguard3", "K"); err == nil {
		t.Fatal("DeletePeer: must fail closed")
	}
	if len(f.Posts) != 0 {
		t.Fatalf("posts=%v", f.Posts)
	}
	if sv, _ := s.settings.GetManagedServerByID("Wireguard3"); len(sv.Peers) != 1 {
		t.Fatal("пир обязан остаться в записи для повтора")
	}
}

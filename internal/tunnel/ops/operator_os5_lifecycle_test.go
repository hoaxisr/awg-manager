package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// newOS5Lifecycle — OS5-оператор над записывающим RCI-постером и записывающим
// ip: ни один вызов не уходит на хост. queries=nil: opkgTunExists отвечает
// «нет», и ColdStart идёт по ветке CreateOpkgTun (это тоже RCI в poster).
func newOS5Lifecycle(t *testing.T) (*OperatorOS5Impl, *recordingPoster, *ipRunRecorder) {
	t.Helper()
	poster := &recordingPoster{}
	o, rec := newOS5LifecycleOn(t, poster, ndmsquery.NewFakeGetter(), &MockBackend{}, false)
	return o, poster, rec
}

// newOS5LifecycleOn — та же сборка с подставными постером, снимком NDMS и
// бэкендом; withQueries=true отдаёт оператору queries, и opkgTunExists
// отвечает по снимку getter'а (`/show/interface/`).
func newOS5LifecycleOn(t *testing.T, poster ndmscommand.Poster, getter *ndmsquery.FakeGetter,
	backend *MockBackend, withQueries bool) (*OperatorOS5Impl, *ipRunRecorder) {
	t.Helper()
	queries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: getter,
		Logger: ndmsquery.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, queries.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	var q *ndmsquery.Queries
	if withQueries {
		q = queries
	}
	o := NewOperatorOS5(q, cmds, &MockWGClient{}, backend, &MockFirewall{})
	rec := &ipRunRecorder{}
	o.ipRun = rec.run
	return o, rec
}

// deviceGatedPoster — RCI-постер с поведением роутера 5.01 (стенд, 2026-09-05):
// `ip address` на записи OpkgTun, за которой нет kernel-устройства, отвергается
// `system failed [0xcffd0217]`; устройство появляется только от backend.Start.
type deviceGatedPoster struct {
	recordingPoster
	backend *MockBackend
}

func (p *deviceGatedPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p.payloads = append(p.payloads, payload)
	js, _ := json.Marshal(payload)
	if strings.Contains(string(js), `"ip":{"address":{"address":`) && !p.backend.running {
		return json.RawMessage(`[{"interface":{"OpkgTun10":{"ip":{"address":{"status":[{"status":"error",` +
			`"code":"268239383","ident":"Network::Interface::Ip","critical":"yes",` +
			`"message":"\"OpkgTun10\": system failed [0xcffd0217]."}]}}}}}]`), nil
	}
	return json.RawMessage(`[{"status":[{"status":"ok"}]}]`), nil
}

// Запись OpkgTun есть, kernel-устройства нет (NDMS `state: error` — так
// остаётся после `ip link del`, rmmod или отката неудачного старта). Устройство
// создаёт backend.Start, поэтому адрес ставится ПОСЛЕ него: с адресом до
// бэкенда старт падал на `[0xcffd0217]`, а откат сносил устройство снова —
// туннель не стартовал никогда (трекер F97).
func TestColdStart_ExistingRecordWithoutDevice_BackendBeforeAddress(t *testing.T) {
	backend := &MockBackend{}
	poster := &deviceGatedPoster{backend: backend}
	getter := ndmsquery.NewFakeGetter()
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"error","link":"down"}}`)
	o, _ := newOS5LifecycleOn(t, poster, getter, backend, true)

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatalf("ColdStart на записи без устройства: %v", err)
	}
	if len(backend.StartCalls) != 1 || backend.StartCalls[0] != "opkgtun10" {
		t.Fatalf("backend.Start: %v", backend.StartCalls)
	}
	if !hasPayload(poster.payloads, `{"interface":{"OpkgTun10":{"ip":{"address":{"address":"10.9.7.2","mask":"255.255.255.192"}}}}}`) {
		t.Fatalf("адрес не поставлен:\n%v", poster.payloads)
	}
}

// hasPayload — RCI-payload сравнивается СЕРИАЛИЗОВАННЫМ литералом: ключи map
// json.Marshal выдаёт отсортированными, поэтому ожидание пишется в том же
// порядке ключей.
func hasPayload(payloads []any, want string) bool {
	for _, p := range payloads {
		b, err := json.Marshal(p)
		if err == nil && string(b) == want {
			return true
		}
	}
	return false
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

func lifecycleCfg(t *testing.T) tunnel.Config {
	t.Helper()
	conf := filepath.Join(t.TempDir(), "awg10.conf")
	if err := os.WriteFile(conf, []byte("[Interface]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return tunnel.Config{ID: "awg10", Name: "Germany", Address: "10.9.7.2", AddressPrefix: 26,
		MTU: 1342, ConfPath: conf}
}

// Дефолт-маршрут NDMS ставится ТОЛЬКО когда пользователь его включил; иначе
// весь трафик роутера уходит в туннель, не помеченный дефолтом, — или
// дефолтный туннель остаётся без маршрута. Инверсия условия проходила зелёной.
func TestColdStart_DefaultRouteFollowsFlag(t *testing.T) {
	const want = `{"ip":{"route":{"default":true,"interface":"OpkgTun10"}}}`
	t.Run("флаг снят — маршрута нет", func(t *testing.T) {
		o, poster, _ := newOS5Lifecycle(t)
		cfg := lifecycleCfg(t)
		if err := o.ColdStart(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if hasPayload(poster.payloads, want) {
			t.Fatalf("дефолт-маршрут поставлен без флага:\n%v", poster.payloads)
		}
	})
	t.Run("флаг стоит — маршрут есть", func(t *testing.T) {
		o, poster, _ := newOS5Lifecycle(t)
		cfg := lifecycleCfg(t)
		cfg.DefaultRoute = true
		if err := o.ColdStart(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
		if !hasPayload(poster.payloads, want) {
			t.Fatalf("дефолт-маршрут не поставлен:\n%v", poster.payloads)
		}
	})
}

// Маска из конфига доезжает до `ip address replace`: регресс фикса 0128ebb3e
// («маска не доезжала до интерфейса») ловился только на NDMS-половине.
// Ожидание — литерал /26, не addressWithPrefix.
func TestColdStart_KernelAddressCarriesUserPrefix(t *testing.T) {
	o, _, rec := newOS5Lifecycle(t)
	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatal(err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip address replace dev opkgtun10 10.9.7.2/26") {
		t.Fatalf("адрес с маской пользователя не выставлен:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set dev opkgtun10 txqueuelen 1000 mtu 1342") {
		t.Fatalf("MTU из конфига не выставлен:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

func TestReconcile_KernelAddressCarriesUserPrefix(t *testing.T) {
	o, _, rec := newOS5Lifecycle(t)
	err := o.Reconcile(context.Background(), lifecycleCfg(t))
	if !hasCall(rec.Calls, "/opt/sbin/ip address replace dev opkgtun10 10.9.7.2/26") {
		t.Fatalf("Reconcile: адрес с маской не выставлен (err=%v):\n%s", err, strings.Join(rec.Calls, "\n"))
	}
}

// Stop без `conf: disabled` в NDMS: роутер сам поднимет интерфейс после
// ребута, а веб-морда покажет «включён» на остановленном.
//
// Poster отвечает `ok`, поэтому ветка ретраев с time.Sleep(1s) в
// interfaceDownBestEffort не исполняется (шва у сна нет — реальный тест
// его не пинует).
func TestStop_DownsKernelAndNDMS(t *testing.T) {
	o, poster, rec := newOS5Lifecycle(t)
	if err := o.Stop(context.Background(), "awg10"); err != nil {
		t.Fatal(err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set down dev opkgtun10") {
		t.Fatalf("ядро не опущено:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if !hasPayload(poster.payloads, `{"interface":{"OpkgTun10":{"up":false}}}`) {
		t.Fatalf("NDMS не получил up:false:\n%v", poster.payloads)
	}
}

// Пять команд ip rule/route policy-routing — литералами; при готовом
// ipRunRecorder у них не было ни одного прогона.
func TestClientRouteOps_Argv(t *testing.T) {
	rec := &ipRunRecorder{}
	c := newClientRouteOps(rec.run, func(string, string, string) {})
	ctx := context.Background()
	if err := c.SetupClientRouteTable(ctx, "opkgtun10", 110); err != nil {
		t.Fatal(err)
	}
	if err := c.AddClientRule(ctx, "192.168.1.77", 110); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveClientRule(ctx, "192.168.1.77", 110); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/opt/sbin/ip route replace default dev opkgtun10 table 110",
		"/opt/sbin/ip rule add from 192.168.1.77 lookup 110 priority 110",
		"/opt/sbin/ip rule del from 192.168.1.77 lookup 110",
	} {
		if !hasCall(rec.Calls, want) {
			t.Errorf("нет команды %q:\n%s", want, strings.Join(rec.Calls, "\n"))
		}
	}
	rec.Calls = nil
	if err := c.CleanupClientRouteTable(ctx, 110); err != nil {
		t.Fatal(err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip route flush table 110") {
		t.Errorf("таблица не сброшена:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

// scriptedIPRun — ipRunRecorder, отвечающий на `ip route get` заданной
// строкой, а на `ip link show` — заданным отказом (интерфейса нет);
// остальные команды — пустой успех.
type scriptedIPRun struct {
	ipRunRecorder
	routeGet    string
	linkShowErr error
}

func (s *scriptedIPRun) run(ctx context.Context, name string, args ...string) (*exec.Result, error) {
	res, err := s.ipRunRecorder.run(ctx, name, args...)
	if len(args) >= 2 && args[0] == "route" && args[1] == "get" ||
		len(args) >= 3 && args[0] == "-6" && args[1] == "route" && args[2] == "get" {
		return &exec.Result{Stdout: s.routeGet}, nil
	}
	if s.linkShowErr != nil && len(args) >= 2 && args[0] == "link" && args[1] == "show" {
		return &exec.Result{ExitCode: 1}, s.linkShowErr
	}
	return res, err
}

// Endpoint, который ядро уже маршрутизирует через наш же tun, — петля:
// host-route ставить нельзя, иначе туннель «поднялся и молчит».
func TestSetupEndpointRoute_RefusesLoopThroughTunnelDevice(t *testing.T) {
	o, poster, _ := newOS5Lifecycle(t)
	s := &scriptedIPRun{routeGet: "203.0.113.7 dev opkgtun3 src 10.9.0.2 uid 0"}
	o.ipRun = s.run
	_, err := o.SetupEndpointRoute(context.Background(), "awg10", "203.0.113.7:51820", "", "")
	if err == nil || !strings.Contains(err.Error(), "routing loop") {
		t.Fatalf("петля не распознана: err=%v", err)
	}
	for _, c := range s.Calls {
		if strings.Contains(c, "route replace 203.0.113.7/32") {
			t.Fatalf("host-route поставлен несмотря на петлю: %s", c)
		}
	}
	if len(poster.payloads) != 0 {
		t.Fatalf("в NDMS что-то ушло при петле: %v", poster.payloads)
	}
}

// Два туннеля к одному серверу делят host-route: остановка первого не имеет
// права срубить маршрут второму. Снимается только когда IP ничей.
func TestCleanupEndpointRoute_KeepsSharedIPUntilLastOwner(t *testing.T) {
	o, poster, _ := newOS5Lifecycle(t)
	s := &scriptedIPRun{routeGet: "203.0.113.7 via 192.0.2.1 dev eth3 src 192.0.2.10 uid 0"}
	o.ipRun = s.run
	ctx := context.Background()
	for _, id := range []string{"awg10", "awg11"} {
		if _, err := o.SetupEndpointRoute(ctx, id, "203.0.113.7:51820", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	s.Calls, poster.payloads = nil, nil

	if err := o.CleanupEndpointRoute(ctx, "awg10"); err != nil {
		t.Fatal(err)
	}
	if hasCall(s.Calls, "/opt/sbin/ip route del 203.0.113.7/32") {
		t.Fatalf("маршрут снят, пока им пользуется awg11:\n%s", strings.Join(s.Calls, "\n"))
	}
	if hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.7","no":true}}}`) {
		t.Fatalf("NDMS host-route снят при живом втором владельце: %v", poster.payloads)
	}

	if err := o.CleanupEndpointRoute(ctx, "awg11"); err != nil {
		t.Fatal(err)
	}
	if !hasCall(s.Calls, "/opt/sbin/ip route del 203.0.113.7/32") {
		t.Fatalf("маршрут не снят у последнего владельца:\n%s", strings.Join(s.Calls, "\n"))
	}
	if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.7","no":true}}}`) {
		t.Fatalf("NDMS host-route не снят у последнего владельца: %v", poster.payloads)
	}
}

// Удаление обязано снять host-route к серверу и в ядре, и в NDMS: иначе
// после удаления туннеля остаётся маршрут на WAN, переживающий пересоздание
// туннеля с другим endpoint. IP берётся из записи, а у старых записей без
// него — из endpoint (здесь литерал, DNS не трогаем).
func TestDelete_RemovesEndpointHostRoute(t *testing.T) {
	t.Run("IP сохранён в записи", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		stored := &storage.AWGTunnel{ID: "awg10", ResolvedEndpointIP: "203.0.113.5"}
		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
		if !hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.5/32") {
			t.Fatalf("host-route не снят в ядре:\n%s", strings.Join(rec.Calls, "\n"))
		}
		if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
			t.Fatalf("host-route не снят в NDMS:\n%v", poster.payloads)
		}
	})
	t.Run("IP только в endpoint", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		stored := &storage.AWGTunnel{ID: "awg10"}
		stored.Peer.Endpoint = "203.0.113.6:51820"
		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
		if !hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.6/32") {
			t.Fatalf("host-route не снят в ядре:\n%s", strings.Join(rec.Calls, "\n"))
		}
		if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.6","no":true}}}`) {
			t.Fatalf("host-route не снят в NDMS:\n%v", poster.payloads)
		}
	})
	t.Run("отказ ip route del не роняет Delete, но виден в журнале", func(t *testing.T) {
		o, _, rec := newOS5Lifecycle(t)
		rec.failOn = "route del 203.0.113.5/32"
		spy := &recAppLog{}
		o.SetAppLogger(spy)
		stored := &storage.AWGTunnel{ID: "awg10", ResolvedEndpointIP: "203.0.113.5"}
		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatalf("Delete обязан оставаться best-effort: %v", err)
		}
		want := "warn|delete|awg10|ip route del 203.0.113.5: ip: RTNETLINK answers: No such process"
		if !slices.Contains(spy.entries, want) {
			t.Fatalf("журнал = %v, ждали %q", spy.entries, want)
		}
	})

	// F228: три туннеля к одному серверу делят один host-route (F130/#867).
	// Удаление одного из них не должно срывать маршрут у оставшихся.
	t.Run("маршрут держит сосед", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		spy := &recAppLog{}
		o.SetAppLogger(spy)
		if _, err := o.RestoreEndpointTracking(context.Background(), "awg11", "203.0.113.5:51820"); err != nil {
			t.Fatalf("RestoreEndpointTracking: %v", err)
		}

		stored := &storage.AWGTunnel{ID: "awg10", ResolvedEndpointIP: "203.0.113.5"}
		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatal(err)
		}

		if hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.5/32") {
			t.Errorf("маршрут снят из ядра, хотя им пользуется awg11:\n%s", strings.Join(rec.Calls, "\n"))
		}
		if hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
			t.Errorf("маршрут снят в NDMS, хотя им пользуется awg11:\n%v", poster.payloads)
		}
		if got := o.GetTrackedEndpointIP("awg11"); got != "203.0.113.5" {
			t.Errorf("сосед потерял свою запись в карте: %q", got)
		}
		if got := o.GetTrackedEndpointIP("awg10"); got != "" {
			t.Errorf("удалённый туннель остался в карте: %q", got)
		}
		want := "info|delete|awg10|IP 203.0.113.5 still in use by another tunnel"
		if !slices.Contains(spy.entries, want) {
			t.Errorf("журнал = %v, ждали %q", spy.entries, want)
		}
	})

	// F117: наследство прежних версий. Тогда host-route до петли реально
	// ставился, и удаление туннеля — путь, которым он уходит с роутера.
	t.Run("петля от прежней версии", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		stored := &storage.AWGTunnel{ID: "awg10", ResolvedEndpointIP: "127.0.0.1"}
		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatal(err)
		}
		if !hasCall(rec.Calls, "/opt/sbin/ip route del 127.0.0.1/32") {
			t.Errorf("маршрут не снят из ядра: %v", rec.Calls)
		}
		if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"127.0.0.1","no":true}}}`) {
			t.Error("host-route не снят из конфига NDMS — переживёт перезагрузку роутера")
		}
	})
}

// F129 (#867): рестарт демона приходил в Reconcile на РАБОТАЮЩИЙ kernel-туннель
// и безусловно делал ip link del + add + setconf — сессия рвалась на каждом
// рестарте awg-manager. Живое amneziawg-устройство надо оставить и лишь
// досинхронизировать конфиг (syncconf сохраняет сессию при неизменном конфиге).
func TestReconcile_KeepsRunningKernelInterface(t *testing.T) {
	backend := &MockBackend{running: true, pid: 1}
	getter := ndmsquery.NewFakeGetter()
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"up"}}`)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{}, getter, backend, true)
	if err := o.Reconcile(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatal(err)
	}
	if hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10") {
		t.Fatalf("живое устройство удалено:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if len(backend.StartCalls) != 0 {
		t.Fatalf("устройство пересоздано: %v", backend.StartCalls)
	}
	wgc := o.wg.(*MockWGClient)
	if len(wgc.SetConfCalls) != 0 || len(wgc.SyncConfCalls) != 1 {
		t.Fatalf("ожидался один syncconf без setconf: set=%v sync=%v", wgc.SetConfCalls, wgc.SyncConfCalls)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set up dev opkgtun10") {
		t.Fatalf("интерфейс не поднят:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

// Устройство пересоздаётся, если его нет (rmmod, ручной ip link del) — и если
// записи OpkgTun в NDMS не было: на живом kernel-устройстве NDMS отвергает
// ip address (exit 122), запись надо ставить на свежее.
func TestReconcile_RecreatesKernelInterface(t *testing.T) {
	cases := []struct {
		name    string
		backend *MockBackend
	}{
		{"устройства нет", &MockBackend{}},
		{"устройство живо, записи OpkgTun нет", &MockBackend{running: true, pid: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, rec := newOS5LifecycleOn(t, &recordingPoster{}, ndmsquery.NewFakeGetter(), tc.backend, false)
			if err := o.Reconcile(context.Background(), lifecycleCfg(t)); err != nil {
				t.Fatal(err)
			}
			if !hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10") || !slices.Equal(tc.backend.StartCalls, []string{"opkgtun10"}) {
				t.Fatalf("устройство не пересоздано: start=%v\n%s", tc.backend.StartCalls, strings.Join(rec.Calls, "\n"))
			}
		})
	}
}

// F130 (#867): три туннеля к одному серверу делят host-route; старт каждого
// следующего делал `route del` + `route add` — окно, в котором пакеты уже
// работающих туннелей уходили по default route (на роутере с политиками —
// в чужой туннель). Замена атомарна.
func TestSetupEndpointRoute_ReplacesSharedHostRouteAtomically(t *testing.T) {
	o, _, _ := newOS5Lifecycle(t)
	s := &scriptedIPRun{routeGet: "203.0.113.7 via 192.0.2.1 dev eth3 src 192.0.2.10 uid 0"}
	o.ipRun = s.run
	for _, id := range []string{"awg10", "awg11"} {
		if _, err := o.SetupEndpointRoute(context.Background(), id, "203.0.113.7:51820", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if hasCall(s.Calls, "/opt/sbin/ip route del 203.0.113.7/32") {
		t.Fatalf("общий host-route снимался при старте соседа:\n%s", strings.Join(s.Calls, "\n"))
	}
	if !hasCall(s.Calls, "/opt/sbin/ip route replace 203.0.113.7/32 via 192.0.2.1") {
		t.Fatalf("маршрут не поставлен через replace:\n%s", strings.Join(s.Calls, "\n"))
	}
}

// F117: до петли хост-маршрут не нужен. Связанные туннели wdtt/freeturn в
// WG-режиме несут endpoint 127.0.0.1:<порт> — релей слушает на самом роутере.
// Роутер такую команду ПРИНИМАЕТ (стенд 5.01): в ядре оседает
// `127.0.0.1 dev ppp0`, в конфиге NDMS — host-route `127.0.0.1/32`.
//
// Адрес наружу не возвращается: иначе вызывающие запишут петлю в
// ResolvedEndpointIP, где у loopback-пира лежит адрес target'а релея.
func TestSetupEndpointRoute_SkipsUnroutableEndpoint(t *testing.T) {
	for _, tc := range []struct{ endpoint, del string }{
		{"127.0.0.1:51820", "/opt/sbin/ip route del 127.0.0.1/32"},     // связанный wdtt/freeturn
		{"127.0.0.5:1234", "/opt/sbin/ip route del 127.0.0.5/32"},      // вся /8, а не один адрес
		{"[::1]:51820", "/opt/sbin/ip -6 route del ::1/128"},           // v6-петля
		{"0.0.0.0:51820", "/opt/sbin/ip route del 0.0.0.0/32"},         // «неуказанный»
		{"169.254.10.1:500", "/opt/sbin/ip route del 169.254.10.1/32"}, // link-local unicast
		{"[ff02::1]:51820", "/opt/sbin/ip -6 route del ff02::1/128"},   // link-local multicast
	} {
		endpoint := tc.endpoint
		t.Run(endpoint, func(t *testing.T) {
			o, _, rec := newOS5Lifecycle(t)
			spy := &recAppLog{}
			o.SetAppLogger(spy)

			ip, err := o.SetupEndpointRoute(context.Background(), "awg1", endpoint, "eth3", "eth3")
			if err != nil {
				t.Fatalf("немаршрутизируемый endpoint — не ошибка: %v", err)
			}
			if ip != "" {
				t.Errorf("адрес не должен уходить вызывающему (попадёт в ResolvedEndpointIP), got %q", ip)
			}
			// Маршрут не ставится, но наследство прежней версии снимается (F227).
			if slices.ContainsFunc(rec.Calls, func(c string) bool { return strings.Contains(c, "route replace") }) {
				t.Errorf("маршрут до немаршрутизируемого адреса поставлен: %v", rec.Calls)
			}
			if !hasCall(rec.Calls, tc.del) {
				t.Errorf("наследство не снято, ждали %q: %v", tc.del, rec.Calls)
			}
			if got := o.GetTrackedEndpointIP("awg1"); got != "" {
				t.Errorf("в карту маршрутов попал %q", got)
			}
			// Молчаливый пропуск маршрута — ровно то, чего гард не должен делать.
			if !slices.ContainsFunc(spy.entries, func(e string) bool {
				return strings.HasPrefix(e, "info|setup_route|awg1|endpoint не маршрутизируется")
			}) {
				t.Errorf("гард сработал молча, журнал = %v", spy.entries)
			}
		})
	}
}

// M3/M4: обратная сторона гарда — приватный адрес маршрут ПОЛУЧАЕТ. Цепочка
// туннелей (endpoint = адрес другого туннеля) и сервер в LAN иначе молча
// остались бы без host-route.
func TestSetupEndpointRoute_KeepsPrivateEndpoint(t *testing.T) {
	for _, tc := range []struct{ endpoint, want string }{
		{"10.8.0.1:51820", "/opt/sbin/ip route replace 10.8.0.1/32 via 192.0.2.1"},
		{"[fd00::1]:51820", "/opt/sbin/ip -6 route replace fd00::1/128 via 192.0.2.1"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			o, _, _ := newOS5Lifecycle(t)
			s := &scriptedIPRun{routeGet: "x via 192.0.2.1 dev eth3 src 192.0.2.10 uid 0"}
			o.ipRun = s.run

			ip, err := o.SetupEndpointRoute(context.Background(), "awg1", tc.endpoint, "", "")
			if err != nil {
				t.Fatalf("SetupEndpointRoute: %v", err)
			}
			if ip == "" {
				t.Fatalf("гард проглотил маршрутизируемый адрес %q", tc.endpoint)
			}
			if !hasCall(s.Calls, tc.want) {
				t.Errorf("маршрут не поставлен:\n%s", strings.Join(s.Calls, "\n"))
			}
		})
	}
}

// M1: у туннеля, которого нет в карте (не поднимался с рестарта демона),
// снимать нечего. Без раннего возврата в роутер уехали бы `ip route del /32` и
// host-route с пустым адресом.
func TestCleanupEndpointRoute_UntrackedTunnelIsNoop(t *testing.T) {
	o, poster, rec := newOS5Lifecycle(t)

	if err := o.CleanupEndpointRoute(context.Background(), "awg1"); err != nil {
		t.Fatalf("CleanupEndpointRoute: %v", err)
	}

	if len(rec.Calls) != 0 {
		t.Errorf("в ядро ушли команды: %v", rec.Calls)
	}
	if len(poster.payloads) != 0 {
		t.Errorf("в NDMS ушли запросы: %v", poster.payloads)
	}
}

// Снятие обязано работать и для петли. На роутере, поработавшем под прежней
// версией, мусорный маршрут уже лежит, а NDMS переигрывает его в ядро на
// каждой загрузке — это единственный путь, которым он уходит с роутера.
func TestCleanupEndpointRoute_RemovesLoopbackLeftover(t *testing.T) {
	o, poster, rec := newOS5Lifecycle(t)
	spy := &recAppLog{}
	o.SetAppLogger(spy)

	// Так карта наполняется на рестарте демона для уже поднятого туннеля.
	if _, err := o.RestoreEndpointTracking(context.Background(), "awg1", "127.0.0.1:51820"); err != nil {
		t.Fatalf("RestoreEndpointTracking: %v", err)
	}
	if got := o.GetTrackedEndpointIP("awg1"); got != "127.0.0.1" {
		t.Fatalf("наследство не попало в карту: %q", got)
	}

	if err := o.CleanupEndpointRoute(context.Background(), "awg1"); err != nil {
		t.Fatalf("CleanupEndpointRoute: %v", err)
	}

	if !hasCall(rec.Calls, "/opt/sbin/ip route del 127.0.0.1/32") {
		t.Errorf("маршрут из ядра не снят: %v", rec.Calls)
	}
	if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"127.0.0.1","no":true}}}`) {
		t.Error("запись host-route не снята из конфига NDMS — она переживёт перезагрузку роутера")
	}
	// Метка действия в журнале — своя у каждого пути снятия, иначе запись врёт
	// о том, что случилось с туннелем.
	want := "info|cleanup_route|awg1|Маршрут до endpoint 127.0.0.1 удалён"
	if !slices.Contains(spy.entries, want) {
		t.Errorf("журнал = %v, ждали %q", spy.entries, want)
	}
}

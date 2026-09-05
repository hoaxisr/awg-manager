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
	queries := ndmsquery.NewQueries(ndmsquery.Deps{
		Getter: ndmsquery.NewFakeGetter(),
		Logger: ndmsquery.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	cmds := ndmscommand.NewCommands(ndmscommand.Deps{
		Poster:  poster,
		Queries: queries,
		Save:    ndmscommand.NewSaveCoordinator(poster, nil, time.Hour, time.Hour, 0, queries.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	o := NewOperatorOS5(nil, cmds, &MockWGClient{}, &MockBackend{}, &MockFirewall{})
	rec := &ipRunRecorder{}
	o.ipRun = rec.run
	return o, poster, rec
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

// Маска из конфига доезжает до `ip address add`: регресс фикса 0128ebb3e
// («маска не доезжала до интерфейса») ловился только на NDMS-половине.
// Ожидание — литерал /26, не addressWithPrefix.
func TestColdStart_KernelAddressCarriesUserPrefix(t *testing.T) {
	o, _, rec := newOS5Lifecycle(t)
	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatal(err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip address add dev opkgtun10 10.9.7.2/26") {
		t.Fatalf("адрес с маской пользователя не выставлен:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set dev opkgtun10 txqueuelen 1000 mtu 1342") {
		t.Fatalf("MTU из конфига не выставлен:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

func TestReconcile_KernelAddressCarriesUserPrefix(t *testing.T) {
	o, _, rec := newOS5Lifecycle(t)
	err := o.Reconcile(context.Background(), lifecycleCfg(t))
	if !hasCall(rec.Calls, "/opt/sbin/ip address add dev opkgtun10 10.9.7.2/26") {
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
		if strings.Contains(c, "route add 203.0.113.7/32") {
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
}

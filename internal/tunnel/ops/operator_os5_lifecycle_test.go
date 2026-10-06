package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/hoaxisr/awg-manager/internal/tunnel/backend"
)

// newOS5Lifecycle — OS5-оператор над записывающим RCI-постером и записывающим
// ip: ни один вызов не уходит на хост. queries=nil: opkgTunRecord отвечает
// «нет», и ColdStart идёт по ветке CreateOpkgTun (это тоже RCI в poster).
func newOS5Lifecycle(t *testing.T) (*OperatorOS5Impl, *recordingPoster, *ipRunRecorder) {
	t.Helper()
	poster := &recordingPoster{}
	o, rec := newOS5LifecycleOn(t, poster, ndmsquery.NewFakeGetter(), &MockBackend{}, false)
	return o, poster, rec
}

// newOS5LifecycleOn — та же сборка с подставными постером, снимком NDMS и
// бэкендом; withQueries=true отдаёт оператору queries, и opkgTunRecord
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
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"error","link":"down","description":"Germany"}}`)
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

// F517: запись OpkgTun10 в NDMS есть. Наша — описание равно имени туннеля
// (так её создаёт CreateOpkgTun и так же NDMS восстанавливает сохранённую
// запись после ребута) — старт идёт дальше. Чужое описание без живого
// amneziawg — отказ ДО backend.Start и до любой RCI-записи, без отката. Чужое
// описание под живым amneziawg — запись наша (описание разошлось): старт
// переписывает описание на имя туннеля и идёт дальше.
func TestColdStart_ExistingRecordOwnership(t *testing.T) {
	cases := []struct {
		name        string
		description string
		running     bool
		wantForeign bool
	}{
		{"описание = имя туннеля", "Germany", false, false},
		{"чужое описание, устройства нет", "csqtt", false, true},
		{"пустое описание, устройства нет", "", false, true},
		{"чужое описание, живое amneziawg", "csqtt", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be := &MockBackend{running: tc.running}
			poster := &recordingPoster{}
			getter := ndmsquery.NewFakeGetter()
			getter.SetJSON("/show/interface/", fmt.Sprintf(
				`{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":%q}}`, tc.description))
			o, rec := newOS5LifecycleOn(t, poster, getter, be, true)

			err := o.ColdStart(context.Background(), lifecycleCfg(t))

			var foreign *ForeignRecordError
			if got := errors.As(err, &foreign); got != tc.wantForeign {
				t.Fatalf("err = %v, want ForeignRecordError = %v", err, tc.wantForeign)
			}
			if !tc.wantForeign {
				if err != nil {
					t.Fatalf("ColdStart: %v", err)
				}
				// Запись наша. Описание разошлось (живое amneziawg) — старт
				// лечит его на имя туннеля; совпало — не пишет ничего.
				var descr []string
				for _, p := range poster.payloads {
					if js, _ := json.Marshal(p); strings.Contains(string(js), `"description"`) {
						descr = append(descr, string(js))
					}
				}
				want := 0
				if tc.description != "Germany" {
					want = 1
				}
				if len(descr) != want || (want == 1 && descr[0] != `{"interface":{"OpkgTun10":{"description":"Germany"}}}`) {
					t.Fatalf("описание на старте = %v, want %d запись с «Germany»", descr, want)
				}
				return
			}
			if foreign.NDMSName != "OpkgTun10" || foreign.Description != tc.description || foreign.Want != "Germany" {
				t.Fatalf("ForeignRecordError = %+v", foreign)
			}
			if len(poster.payloads) != 0 {
				t.Fatalf("RCI тронут при чужой записи: %v", poster.payloads)
			}
			if len(be.StartCalls) != 0 || len(be.StopCalls) != 0 {
				t.Fatalf("бэкенд тронут при чужой записи (откат?): start=%v stop=%v", be.StartCalls, be.StopCalls)
			}
			if len(rec.Calls) != 0 {
				t.Fatalf("ip вызван при чужой записи:\n%s", strings.Join(rec.Calls, "\n"))
			}
		})
	}
}

// F532: смена описания записи снаружи (`interface OpkgTunN description …`)
// не даёт NDMS-хука, и кэш InterfaceStore может годами помнить прежнее
// значение. Гейт Фазы 1 обязан решать по свежему ответу NDMS, а не по
// кэшу: устарел кэш — «чужая» → старт идёт.
func TestColdStart_GateUsesFreshReadNotStaleCache(t *testing.T) {
	getter := ndmsquery.NewFakeGetter()
	// Кэш (bootstrap-снимок) считает запись чужой.
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"csqtt"}}`)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{}, getter, &MockBackend{}, true)

	// Поднимаем кэш на устаревшем значении — как будто он уже был прочитан
	// до внешней правки.
	if _, err := o.queries.Interfaces.Get(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("precondition Get: %v", err)
	}

	// NDMS сейчас (снаружи awg-manager описание переписали на имя туннеля)
	// отвечает по-другому — хука на это не было, кэш остался «csqtt».
	getter.SetPostInterface("OpkgTun10", `{"show":{"interface":{
		"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"Germany"
	}}}`)

	if err := o.ColdStart(context.Background(), lifecycleCfg(t)); err != nil {
		t.Fatalf("ColdStart должен был пройти по свежему ответу (наша запись): %v", err)
	}
	if len(rec.Calls) == 0 {
		t.Fatalf("старт не тронул ip — гейт отказал по устаревшему кэшу")
	}
}

// Обратный случай F532: кэш ещё помнит нашу запись, а NDMS сейчас отвечает
// про чужую (например номер вернулся другой программе) — гейт обязан
// отказать по свежему ответу, а не пропустить по кэшу.
func TestColdStart_GateRefusesOnFreshForeignRecord(t *testing.T) {
	getter := ndmsquery.NewFakeGetter()
	// Кэш (bootstrap-снимок) считает запись нашей.
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"Germany"}}`)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{}, getter, &MockBackend{}, true)

	if _, err := o.queries.Interfaces.Get(context.Background(), "OpkgTun10"); err != nil {
		t.Fatalf("precondition Get: %v", err)
	}

	// NDMS сейчас отвечает про чужую запись — кэш этого не видел.
	getter.SetPostInterface("OpkgTun10", `{"show":{"interface":{
		"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"csqtt"
	}}}`)

	err := o.ColdStart(context.Background(), lifecycleCfg(t))
	var foreign *ForeignRecordError
	if !errors.As(err, &foreign) {
		t.Fatalf("err = %v, want *ForeignRecordError (свежая запись чужая)", err)
	}
	if len(rec.Calls) != 0 {
		t.Fatalf("ip вызван при свежей чужой записи:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

// Тот же гейт в Reconcile (рестарт демона, устройство исчезло, на номере чужая
// запись): ни своего ip link del, ни backend.Start. Текст доходит до человека.
func TestReconcile_ForeignRecord_Refused(t *testing.T) {
	be := &MockBackend{}
	getter := ndmsquery.NewFakeGetter()
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"csqtt"}}`)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{}, getter, be, true)

	err := o.Reconcile(context.Background(), lifecycleCfg(t))

	var foreign *ForeignRecordError
	if !errors.As(err, &foreign) {
		t.Fatalf("err = %v, want *ForeignRecordError", err)
	}
	if !strings.Contains(err.Error(), "запись OpkgTun10 в NDMS не принадлежит туннелю «Germany»: её описание — «csqtt»") {
		t.Fatalf("текст отказа не для человека: %q", err.Error())
	}
	if len(be.StartCalls) != 0 || hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10") {
		t.Fatalf("чужая запись тронута: start=%v\n%s", be.StartCalls, strings.Join(rec.Calls, "\n"))
	}
}

// Рестарт демона (opkg upgrade → Reconcile) лечит разошедшееся описание
// работающего туннеля; провал записи описания старт не валит — запись наша.
func TestReconcile_HealsDriftedDescription(t *testing.T) {
	const want = `{"interface":{"OpkgTun10":{"description":"Germany"}}}`
	for name, fail := range map[string]bool{"запись прошла": false, "запись отвергнута": true} {
		t.Run(name, func(t *testing.T) {
			poster := &descriptionPoster{fail: fail}
			getter := ndmsquery.NewFakeGetter()
			getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"up","description":"old"}}`)
			o, _ := newOS5LifecycleOn(t, poster, getter, &MockBackend{running: true}, true)
			if err := o.Reconcile(context.Background(), lifecycleCfg(t)); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if !hasPayload(poster.payloads, want) {
				t.Fatalf("описание не вылечено: %v", poster.payloads)
			}
		})
	}
}

// descriptionPoster — recordingPoster, который при fail отвергает запись описания.
type descriptionPoster struct {
	recordingPoster
	fail bool
}

func (p *descriptionPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	p.payloads = append(p.payloads, payload)
	if js, _ := json.Marshal(payload); p.fail && strings.Contains(string(js), `"description"`) {
		return nil, errors.New("injected: description")
	}
	return json.RawMessage(`[{"status":[{"status":"ok"}]}]`), nil
}

// Кэш записей NDMS не поднялся — «не знаем», а не «записи нет»: Create поверх
// существующей записи переписал бы её описание. Отказ без единой RCI-записи;
// оркестратор повторит старт следующим decide.
func TestColdStart_RecordReadError_RefusesWithoutCreate(t *testing.T) {
	poster := &recordingPoster{}
	getter := ndmsquery.NewFakeGetter()
	getter.SetError("/show/interface/", errors.New("injected: ndms"))
	o, _ := newOS5LifecycleOn(t, poster, getter, &MockBackend{}, true)

	err := o.ColdStart(context.Background(), lifecycleCfg(t))
	if err == nil || !strings.Contains(err.Error(), "read OpkgTun record") {
		t.Fatalf("err = %v, want отказ чтения записи", err)
	}
	if len(poster.payloads) != 0 {
		t.Fatalf("RCI тронут при недоступном кэше NDMS: %v", poster.payloads)
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
	poster := &recordingPoster{}
	o, rec := newOS5LifecycleOn(t, poster, ndmsquery.NewFakeGetter(), &MockBackend{running: true}, false)
	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatal(err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip link set down dev opkgtun10") {
		t.Fatalf("ядро не опущено:\n%s", strings.Join(rec.Calls, "\n"))
	}
	if !hasPayload(poster.payloads, `{"interface":{"OpkgTun10":{"up":false}}}`) {
		t.Fatalf("NDMS не получил up:false:\n%v", poster.payloads)
	}
}

// F500/F517: на номере чужой plain tun (устройство не amneziawg) под чужой
// записью — Stop не опускает чужое устройство и не ставит чужой записи
// `conf: disabled`. Наша запись без нашего устройства (plain tun после
// ребута) по-прежнему получает `conf: disabled`, но устройство не трогаем.
func TestStop_NotOurDevice(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		stopName          string
		wantDisabled      bool
	}{
		{"чужая запись", "csqtt", "Germany", false},
		{"наша запись", "Germany", "Germany", true},
		{"карточки нет, запись без описания", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poster := &recordingPoster{}
			getter := ndmsquery.NewFakeGetter()
			getter.SetJSON("/show/interface/", fmt.Sprintf(
				`{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","description":%q}}`, tc.description))
			o, rec := newOS5LifecycleOn(t, poster, getter, &MockBackend{}, true)

			if err := o.Stop(context.Background(), "awg10", tc.stopName); err != nil {
				t.Fatal(err)
			}
			if hasCall(rec.Calls, "/opt/sbin/ip link set down dev opkgtun10") {
				t.Fatalf("опущено не наше устройство:\n%s", strings.Join(rec.Calls, "\n"))
			}
			if got := hasPayload(poster.payloads, `{"interface":{"OpkgTun10":{"up":false}}}`); got != tc.wantDisabled {
				t.Fatalf("conf: disabled = %v, want %v: %v", got, tc.wantDisabled, poster.payloads)
			}
		})
	}
}

// F500: Delete сносит устройство через backend.Stop с гейтом держателя.
// Устройство держит чужая программа — не сносим, удаление не падает;
// наше amneziawg — сносится.
func TestDelete_DeviceGoesThroughHolderGate(t *testing.T) {
	t.Run("чужой держатель", func(t *testing.T) {
		be := &MockBackend{stopError: &backend.HeldError{Iface: "opkgtun10", PID: 42, Comm: "csqtt"}}
		o, rec := newOS5LifecycleOn(t, &recordingPoster{}, ndmsquery.NewFakeGetter(), be, false)
		if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
			t.Fatalf("HeldError не должен валить удаление: %v", err)
		}
		if hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10") {
			t.Fatalf("ip link del мимо гейта:\n%s", strings.Join(rec.Calls, "\n"))
		}
		if len(be.StopCalls) != 1 || be.StopCalls[0] != "opkgtun10" {
			t.Fatalf("backend.Stop: %v", be.StopCalls)
		}
	})
	t.Run("наше amneziawg", func(t *testing.T) {
		be := &MockBackend{running: true}
		o, _ := newOS5LifecycleOn(t, &recordingPoster{}, ndmsquery.NewFakeGetter(), be, false)
		if err := o.Delete(context.Background(), &storage.AWGTunnel{ID: "awg10", Name: "Germany"}); err != nil {
			t.Fatal(err)
		}
		if len(be.StopCalls) != 1 || be.StopCalls[0] != "opkgtun10" || be.running {
			t.Fatalf("наше устройство не снесено: stop=%v running=%v", be.StopCalls, be.running)
		}
	})
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
		// F412: страховка kill switch — переживает уход `default dev` при down/del.
		"/opt/sbin/ip route replace unreachable default metric 4294967295 table 110",
		// F411: приоритет ниже fwmark-правил NDMS (100+), иначе DNS-маршрут бьёт устройство.
		"/opt/sbin/ip rule add from 192.168.1.77 lookup 110 priority 50",
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
	t.Run("нечего снимать — молча, и NDMS не тревожим", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		rec.failOn = "route del 203.0.113.5/32" // ip(8): No such process
		spy := &recAppLog{}
		o.SetAppLogger(spy)
		stored := &storage.AWGTunnel{ID: "awg10", ResolvedEndpointIP: "203.0.113.5"}

		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatalf("Delete обязан оставаться best-effort: %v", err)
		}

		if hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
			t.Errorf("в NDMS ушёл запрос на снятие несуществующего маршрута: %v", poster.payloads)
		}
		for _, e := range spy.entries {
			if strings.Contains(e, "route del") || strings.Contains(e, "Маршрут до endpoint") {
				t.Errorf("шум в журнале там, где снимать было нечего: %q", e)
			}
		}
	})
	t.Run("настоящий отказ ip route del виден в журнале", func(t *testing.T) {
		o, _, rec := newOS5Lifecycle(t)
		rec.failOn = "route del 203.0.113.5/32"
		rec.failErr = "ip: Operation not permitted"
		spy := &recAppLog{}
		o.SetAppLogger(spy)
		stored := &storage.AWGTunnel{ID: "awg10", ResolvedEndpointIP: "203.0.113.5"}

		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatalf("Delete обязан оставаться best-effort: %v", err)
		}

		want := "warn|delete|awg10|ip route del 203.0.113.5: ip: Operation not permitted"
		if !slices.Contains(spy.entries, want) {
			t.Fatalf("журнал = %v, ждали %q", spy.entries, want)
		}
		// «Удалён» после «не смог» — ложь, которую пользователь видит в /logs.
		if slices.ContainsFunc(spy.entries, func(e string) bool {
			return strings.Contains(e, "Маршрут до endpoint")
		}) {
			t.Errorf("журнал обещает снятие маршрута, которого не было: %v", spy.entries)
		}
	})

	// F229: запись могла остаться без ResolvedEndpointIP — правка карточки
	// связанного туннеля обнуляет поле, потому что маршрута до петли нет.
	// Зацепка при этом не теряется: адрес берётся из endpoint'а. Здесь это
	// литерал, который netutil разбирает без обращения к DNS, — ветка
	// настоящего резолва в Delete остаётся непокрытой (в ней блокирующий
	// сетевой вызов без таймаута, отдельная находка).
	t.Run("петля без записанного адреса", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		stored := &storage.AWGTunnel{ID: "awg10"}
		stored.Peer.Endpoint = "127.0.0.1:51820"

		if err := o.Delete(context.Background(), stored); err != nil {
			t.Fatal(err)
		}

		if !hasCall(rec.Calls, "/opt/sbin/ip route del 127.0.0.1/32") {
			t.Errorf("наследство не снято из ядра: %v", rec.Calls)
		}
		if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"127.0.0.1","no":true}}}`) {
			t.Errorf("наследство не снято в NDMS: %v", poster.payloads)
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
// ip address (exit 122), запись надо ставить на свежее. `ip link del` из
// Reconcile — только для живого amneziawg под свежей записью; отсутствующее
// или не-amneziawg устройство сносит сам backend.Start (там гейт F500).
func TestReconcile_RecreatesKernelInterface(t *testing.T) {
	cases := []struct {
		name    string
		backend *MockBackend
		wantDel bool
	}{
		{"устройства нет", &MockBackend{}, false},
		{"устройство живо, записи OpkgTun нет", &MockBackend{running: true, pid: 1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, rec := newOS5LifecycleOn(t, &recordingPoster{}, ndmsquery.NewFakeGetter(), tc.backend, false)
			if err := o.Reconcile(context.Background(), lifecycleCfg(t)); err != nil {
				t.Fatal(err)
			}
			if got := hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10"); got != tc.wantDel {
				t.Fatalf("ip link del = %v, want %v:\n%s", got, tc.wantDel, strings.Join(rec.Calls, "\n"))
			}
			if !slices.Equal(tc.backend.StartCalls, []string{"opkgtun10"}) {
				t.Fatalf("устройство не пересоздано: start=%v", tc.backend.StartCalls)
			}
		})
	}
}

// F500: на номере туннеля plain tun, который держит чужая программа (запись
// OpkgTun10 в NDMS есть — стенд 27.09: запись раньше процесса). Reconcile не
// сносит его ни своим `ip link del`, ни через backend.Start — наружу
// HeldError с pid и именем программы.
func TestReconcile_ForeignHeldDevice_RefusedWithoutDelete(t *testing.T) {
	held := &backend.HeldError{Iface: "opkgtun10", PID: 4242, Comm: "csqtt"}
	be := &MockBackend{startError: held}
	getter := ndmsquery.NewFakeGetter()
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"Germany"}}`)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{}, getter, be, true)

	err := o.Reconcile(context.Background(), lifecycleCfg(t))

	var got *backend.HeldError
	if !errors.As(err, &got) || got.PID != 4242 {
		t.Fatalf("err = %v, want *backend.HeldError{PID:4242}", err)
	}
	if hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10") {
		t.Fatalf("чужое устройство снесено оператором:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

// Тот же сценарий на ColdStart (boot / ручной старт). Откат идёт через
// backend.Stop — реальный бэкенд отказывает и там (backend.TestStop_Held…),
// здесь проверяется, что оператор сам `ip link del` не зовёт и ошибку не
// прячет за откатом.
func TestColdStart_ForeignHeldDevice_RefusedWithoutDelete(t *testing.T) {
	held := &backend.HeldError{Iface: "opkgtun10", PID: 4242, Comm: "csqtt"}
	be := &MockBackend{startError: held}
	getter := ndmsquery.NewFakeGetter()
	getter.SetJSON("/show/interface/", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","state":"up","link":"down","description":"Germany"}}`)
	o, rec := newOS5LifecycleOn(t, &recordingPoster{}, getter, be, true)

	err := o.ColdStart(context.Background(), lifecycleCfg(t))

	var got *backend.HeldError
	if !errors.As(err, &got) || got.Comm != "csqtt" {
		t.Fatalf("err = %v, want *backend.HeldError{Comm:csqtt}", err)
	}
	if !strings.Contains(err.Error(), "занят сторонней программой csqtt (pid 4242)") {
		t.Fatalf("текст отказа не дошёл до пользователя: %q", err.Error())
	}
	if hasCall(rec.Calls, "/opt/sbin/ip link del dev opkgtun10") {
		t.Fatalf("чужое устройство снесено оператором:\n%s", strings.Join(rec.Calls, "\n"))
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

// F612/#1004: шлюз не-предпочтительного WAN лежит не в main, а в таблице
// интерфейса за правилом `from <адрес> lookup N` (роутер репортёра:
// `1176: from 192.168.8.100 lookup 16397` → `default via 192.168.8.1 dev
// cdc_br1`). Без `from` ядро с заданным oif и без маршрута в main считает
// адрес on-link — вывод без `via`, и маршрут вставал `dev cdc_br1` (ARP в
// сеть модема). Подмена отвечает так же, как ядро.
func TestSetupEndpointRoute_NonPreferredWANUsesInterfaceTable(t *testing.T) {
	o, _, _ := newOS5Lifecycle(t)
	rec := &ipRunRecorder{}
	o.ipRun = func(ctx context.Context, name string, args ...string) (*exec.Result, error) {
		rec.run(ctx, name, args...)
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "-o -4 addr show dev cdc_br1"):
			return &exec.Result{Stdout: "7: cdc_br1    inet 192.168.8.100/24 brd 192.168.8.255 scope global cdc_br1\\       valid_lft forever preferred_lft forever\n"}, nil
		case strings.HasPrefix(line, "route get 198.51.100.20 from 192.168.8.100 oif cdc_br1"):
			return &exec.Result{Stdout: "198.51.100.20 from 192.168.8.100 via 192.168.8.1 dev cdc_br1 table 16397 uid 0\n"}, nil
		case strings.HasPrefix(line, "route get"):
			return &exec.Result{Stdout: "198.51.100.20 dev cdc_br1 src 192.168.8.100 uid 0\n"}, nil
		}
		return &exec.Result{}, nil
	}
	if _, err := o.SetupEndpointRoute(context.Background(), "awg10", "198.51.100.20:51820", "cdc_br1", "CdcEthernet1"); err != nil {
		t.Fatal(err)
	}
	if !hasCall(rec.Calls, "/opt/sbin/ip route replace 198.51.100.20/32 via 192.168.8.1") {
		t.Fatalf("маршрут не через шлюз интерфейса:\n%s", strings.Join(rec.Calls, "\n"))
	}
}

// F117: до петли хост-маршрут не нужен. Связанные туннели wdtt/freeturn в
// WG-режиме несут endpoint 127.0.0.1:<порт> — релей слушает на самом роутере.
// Роутер такую команду ПРИНИМАЕТ (стенд 5.01): в ядре оседает
// `127.0.0.1 dev ppp0`, в конфиге NDMS — host-route `127.0.0.1/32`.
//
// Адрес наружу не возвращается: пустая строка говорит вызывающему «маршрута
// нет». (Соглашение «в ResolvedEndpointIP лежит target релея» — про
// обфусцированные туннели, а они всегда nativewg и сюда не приходят.)
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
			o, _, _ := newOS5Lifecycle(t)
			// scriptedIPRun, а не ipRunRecorder: с пустым выводом `ip route get`
			// постановка маршрута падает раньше гарда, и проверять было бы нечего.
			rec := &scriptedIPRun{routeGet: "x via 192.0.2.1 dev eth3 src 192.0.2.10 uid 0"}
			o.ipRun = rec.run
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

// F231: остановленный туннель не держит host-route. Карта маршрутов должна
// означать «маршрут стоит», иначе остановленный сосед вечно запрещает снятие.
func TestStop_RemovesEndpointRoute(t *testing.T) {
	t.Run("маршрут снят", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		spy := &recAppLog{}
		o.SetAppLogger(spy)
		if _, err := o.RestoreEndpointTracking(context.Background(), "awg10", "203.0.113.5:51820"); err != nil {
			t.Fatalf("RestoreEndpointTracking: %v", err)
		}

		if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		if !hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.5/32") {
			t.Errorf("маршрут из ядра не снят: %v", rec.Calls)
		}
		if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
			t.Errorf("запись host-route не снята в NDMS: %v", poster.payloads)
		}
		if got := o.GetTrackedEndpointIP("awg10"); got != "" {
			t.Errorf("остановленный туннель остался в карте: %q", got)
		}
		want := "info|stop|awg10|Маршрут до endpoint 203.0.113.5 удалён"
		if !slices.Contains(spy.entries, want) {
			t.Errorf("журнал = %v, ждали %q", spy.entries, want)
		}
	})

	t.Run("маршрут держит работающий сосед", func(t *testing.T) {
		o, poster, rec := newOS5Lifecycle(t)
		for _, id := range []string{"awg10", "awg11"} {
			if _, err := o.RestoreEndpointTracking(context.Background(), id, "203.0.113.5:51820"); err != nil {
				t.Fatalf("RestoreEndpointTracking %s: %v", id, err)
			}
		}

		if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		if hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.5/32") {
			t.Errorf("маршрут снят, хотя им пользуется awg11: %v", rec.Calls)
		}
		if hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
			t.Errorf("маршрут снят в NDMS, хотя им пользуется awg11: %v", poster.payloads)
		}
		if got := o.GetTrackedEndpointIP("awg11"); got != "203.0.113.5" {
			t.Errorf("сосед потерял свою запись: %q", got)
		}
	})
}

// Ref-count считает владельцев ОДНОГО адреса. Сосед с другим endpoint снятию
// не мешает — иначе любой второй туннель запирал бы уборку навсегда.
func TestStop_NeighbourWithOtherAddressDoesNotHold(t *testing.T) {
	o, poster, rec := newOS5Lifecycle(t)
	if _, err := o.RestoreEndpointTracking(context.Background(), "awg10", "203.0.113.5:51820"); err != nil {
		t.Fatalf("RestoreEndpointTracking awg10: %v", err)
	}
	if _, err := o.RestoreEndpointTracking(context.Background(), "awg11", "198.51.100.9:51820"); err != nil {
		t.Fatalf("RestoreEndpointTracking awg11: %v", err)
	}

	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if !hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.5/32") {
		t.Errorf("маршрут не снят, хотя сосед смотрит на другой адрес: %v", rec.Calls)
	}
	if !hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
		t.Errorf("маршрут не снят в NDMS: %v", poster.payloads)
	}
	if got := o.GetTrackedEndpointIP("awg11"); got != "198.51.100.9" {
		t.Errorf("сосед потерял свою запись: %q", got)
	}
}

// Тот же host-route ставит обфусцированный nativewg-туннель (до target'а
// релея, тем же объектом NDMS). Карта про чужой бэкенд не знает, поэтому
// снятие спрашивает ещё и общий предикат по стору.
func TestStop_KeepsRouteHeldByOtherBackend(t *testing.T) {
	o, poster, rec := newOS5Lifecycle(t)
	o.SetEndpointRouteSharing(func(excludeID, ip string) bool {
		return excludeID == "awg10" && ip == "203.0.113.5" // маршрут держит обфусцированный сосед
	})
	if _, err := o.RestoreEndpointTracking(context.Background(), "awg10", "203.0.113.5:51820"); err != nil {
		t.Fatalf("RestoreEndpointTracking: %v", err)
	}

	if err := o.Stop(context.Background(), "awg10", "Germany"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if hasCall(rec.Calls, "/opt/sbin/ip route del 203.0.113.5/32") {
		t.Errorf("снят маршрут, который держит туннель другого бэкенда: %v", rec.Calls)
	}
	if hasPayload(poster.payloads, `{"ip":{"route":{"host":"203.0.113.5","no":true}}}`) {
		t.Errorf("запись снята в NDMS, хотя маршрут держит другой бэкенд: %v", poster.payloads)
	}
	if got := o.GetTrackedEndpointIP("awg10"); got != "" {
		t.Errorf("остановленный туннель остался в карте: %q", got)
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

// Петлю в карту не кладём: маршрута под ней не бывает, а «владельцем» она
// получилась бы настоящим — два связанных туннеля держали бы друг другу
// уборку под ключом 127.0.0.1 (F230). Наследство прежних версий снимается не
// отсюда, а из гарда SetupEndpointRoute и из Delete по записи туннеля.
func TestRestoreEndpointTracking_SkipsUnroutableEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"127.0.0.1:51820",  // связанный wdtt/freeturn
		"127.0.0.5:1234",   // вся /8
		"[::1]:51820",      // v6-петля
		"0.0.0.0:51820",    // «неуказанный»: фильтрующий DNS на заблокированный домен
		"169.254.10.1:500", // link-local unicast
		"[ff02::1]:51820",  // link-local multicast
	} {
		t.Run(endpoint, func(t *testing.T) {
			o, poster, rec := newOS5Lifecycle(t)
			spy := &recAppLog{}
			o.SetAppLogger(spy)

			ip, err := o.RestoreEndpointTracking(context.Background(), "awg1", endpoint)
			if err != nil {
				t.Fatalf("RestoreEndpointTracking: %v", err)
			}
			if ip != "" {
				t.Errorf("адрес ушёл вызывающему и окажется в ResolvedEndpointIP: %q", ip)
			}
			if got := o.GetTrackedEndpointIP("awg1"); got != "" {
				t.Errorf("непригодный адрес попал в карту: %q — два туннеля запрут друг другу уборку", got)
			}
			if len(rec.Calls) != 0 || len(poster.payloads) != 0 {
				t.Errorf("трекинг не должен ничего слать: %v %v", rec.Calls, poster.payloads)
			}
			if !slices.ContainsFunc(spy.entries, func(e string) bool {
				return strings.HasPrefix(e, "info|restore_tracking|awg1|endpoint не маршрутизируется")
			}) {
				t.Errorf("пропуск не виден в журнале: %v", spy.entries)
			}
		})
	}
}

// Переименование переписывает описание только НАШЕЙ записи — по тому же
// правилу, что и гейт старта (F517): описание = прежнему имени туннеля или под
// записью живое amneziawg. Иначе туннель, которому старт отказал на чужой
// записи, после переименования перезаписал бы её описание — и следующий старт
// взял бы чужую запись как свою. Отсутствующую запись не создаём: RCI-форма
// описания на ней создала бы запись без security-level; её заведёт Фаза 1.
func TestUpdateDescription_OnlyOurRecord(t *testing.T) {
	const want = `{"interface":{"OpkgTun10":{"description":"Norway"}}}`
	rec := func(descr string) string {
		return fmt.Sprintf(`{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","description":%q}}`, descr)
	}
	for _, tc := range []struct {
		name, snapshot string
		prevName       string
		running        bool
		wantPost       bool
		wantForeign    bool
	}{
		{"наша: описание = прежнему имени", rec("Germany"), "Germany", false, true, false},
		{"наша: чужое описание, живое amneziawg", rec("csqtt"), "Germany", true, true, false},
		{"чужая: описание не наше, устройства нет", rec("csqtt"), "Germany", false, false, true},
		{"записи нет", `{}`, "Germany", false, false, false},
		{"пустое прежнее имя, запись без описания", rec(""), "", false, false, true},
		{"пустое прежнее имя, живое amneziawg", rec(""), "", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poster := &recordingPoster{}
			getter := ndmsquery.NewFakeGetter()
			getter.SetJSON("/show/interface/", tc.snapshot)
			o, _ := newOS5LifecycleOn(t, poster, getter, &MockBackend{running: tc.running}, true)
			err := o.UpdateDescription(context.Background(), "awg10", tc.prevName, "Norway")
			var foreign *ForeignRecordError
			if errors.As(err, &foreign) != tc.wantForeign || (!tc.wantForeign && err != nil) {
				t.Fatalf("err = %v, want ForeignRecordError = %v", err, tc.wantForeign)
			}
			if got := hasPayload(poster.payloads, want); got != tc.wantPost || (!tc.wantPost && len(poster.payloads) != 0) {
				t.Fatalf("описание отправлено = %v, want %v: %v", got, tc.wantPost, poster.payloads)
			}
		})
	}
}

// Взятие стороннего туннеля (Adopt) забирает запись ОСОЗНАННО: описание
// переписывается без проверки владения. Отсутствующую запись не создаём.
func TestCaptureDescription_TakesForeignRecord(t *testing.T) {
	const want = `{"interface":{"OpkgTun10":{"description":"Norway"}}}`
	for _, tc := range []struct {
		name, snapshot string
		wantPost       bool
	}{
		{"чужая запись", `{"OpkgTun10":{"id":"OpkgTun10","type":"OpkgTun","description":"csqtt"}}`, true},
		{"записи нет", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			poster := &recordingPoster{}
			getter := ndmsquery.NewFakeGetter()
			getter.SetJSON("/show/interface/", tc.snapshot)
			o, _ := newOS5LifecycleOn(t, poster, getter, &MockBackend{}, true)
			if err := o.CaptureDescription(context.Background(), "awg10", "Norway"); err != nil {
				t.Fatalf("CaptureDescription: %v", err)
			}
			if got := hasPayload(poster.payloads, want); got != tc.wantPost || (!tc.wantPost && len(poster.payloads) != 0) {
				t.Fatalf("описание отправлено = %v, want %v: %v", got, tc.wantPost, poster.payloads)
			}
		})
	}
}

// Кэш записей не поднялся — «не знаем»: отказ, а не молчаливый пропуск
// (вызывающий пишет Warn — после ребута F517 откажет) и не слепая запись.
func TestUpdateDescription_RecordReadError(t *testing.T) {
	for name, call := range map[string]func(o *OperatorOS5Impl) error{
		"update": func(o *OperatorOS5Impl) error {
			return o.UpdateDescription(context.Background(), "awg10", "Germany", "Norway")
		},
		"capture": func(o *OperatorOS5Impl) error { return o.CaptureDescription(context.Background(), "awg10", "Norway") },
	} {
		t.Run(name, func(t *testing.T) {
			poster := &recordingPoster{}
			getter := ndmsquery.NewFakeGetter()
			getter.SetError("/show/interface/", errors.New("injected: ndms"))
			o, _ := newOS5LifecycleOn(t, poster, getter, &MockBackend{}, true)
			if err := call(o); err == nil {
				t.Fatal("ошибка чтения записи проглочена")
			}
			if len(poster.payloads) != 0 {
				t.Fatalf("RCI тронут при недоступном кэше: %v", poster.payloads)
			}
		})
	}
}

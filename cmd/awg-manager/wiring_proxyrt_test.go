package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmproto"
	"github.com/hoaxisr/awg-manager/internal/listenfirewall"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/captcha"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/install"
	"github.com/hoaxisr/awg-manager/internal/proxyapp/wdttusers"
	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instance"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/manager"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ── занятость номеров OpkgTun ────────────────────────────────────

// mipsRange — общий пул mips: 0..15. Берётся явно, а не от runtime.GOARCH:
// ноль там законный номер, и именно на нём ловится сентинел «пина нет».
func mipsRange(t *testing.T) (int, int) {
	t.Helper()
	min, max, shared := roles.OpkgIndexRange("mipsle")
	if !shared || min != 0 {
		t.Fatalf("диапазон mips изменился: %d..%d shared=%v", min, max, shared)
	}
	return min, max
}

type fakeLiveIfaces struct {
	live map[int]bool
	err  error
}

func (f fakeLiveIfaces) LiveOpkgTunIndices(context.Context) (map[int]bool, error) {
	return f.live, f.err
}

// occEnv — окружение формулы taken: хранилище прокси-инстансов, хранилище
// AWG-туннелей и настройки; живая половина подставляется фейком.
type occEnv struct {
	dir      string
	store    *instancestore.Store
	awg      *storage.AWGTunnelStore
	settings *storage.SettingsStore
}

func newOccEnv(t *testing.T) *occEnv {
	t.Helper()
	dir := t.TempDir()
	awgDir := filepath.Join(dir, "tunnels")
	settings := storage.NewSettingsStore(dir)
	if _, err := settings.Load(); err != nil {
		t.Fatalf("settings.Load: %v", err)
	}
	return &occEnv{
		dir:      dir,
		store:    instancestore.New(dir),
		awg:      storage.NewAWGTunnelStoreWithLockDir(awgDir, filepath.Join(dir, "lock")),
		settings: settings,
	}
}

// alloc собирает боевую формулу: занятость из четырёх поставщиков минус
// собственные пины владельца.
func (e *occEnv) alloc(t *testing.T, live map[int]bool, extra ...storage.OpkgTunPins) func(string, int, bool) (int, error) {
	t.Helper()
	min, max := mipsRange(t)
	pins := append([]storage.OpkgTunPins{
		e.awg.OpkgTunPinsOf,
		e.settings.OpkgTunPinsOf,
		proxyRecordPins(e.store),
	}, extra...)
	occ := storage.OpkgTunOccupancy(fakeLiveIfaces{live: live}, pins...)
	return proxyAllocIndex(context.Background(),
		proxyrt.NewAllocator(proxyrt.IndexRange{Min: min, Max: max}), min, occ, e.store)
}

func (e *occEnv) putRecord(t *testing.T, rec instancestore.Record) {
	t.Helper()
	if _, err := e.store.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, rec)
		return nil
	}); err != nil {
		t.Fatalf("store.Replace: %v", err)
	}
}

func rawClientRecord(id, ndms, kernel string) instancestore.Record {
	return instancestore.Record{ID: id, Kind: instancestore.KindWdttClient,
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Peer: "1.2.3.4:5",
			NdmsIface: ndms, RawIface: kernel}}
}

func serverRecord(id, wgNDMS, wgKernel, rawNDMS, rawKernel string) instancestore.Record {
	return instancestore.Record{ID: id, Kind: instancestore.KindWdttServer,
		WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", Password: "p",
			NatMode: "full", RelayMode: "wg",
			NdmsIface: wgNDMS, WgIface: wgKernel,
			RawNdmsIface: rawNDMS, RawIface: rawKernel}}
}

// B2: живой интерфейс владельца не отбирается у него самого. Без вычитания
// заявленного пина усыновление превратилось бы в перепин, и permit'ы
// пользователя, выписанные на OpkgTun18, повисли бы.
func TestAllocIndexAdoptsOwnLiveInterface(t *testing.T) {
	e := newOccEnv(t)
	// Номер берётся внутри mips-диапазона: 18 туда не попадает.
	alloc := e.alloc(t, map[int]bool{7: true})
	got, err := alloc("wdtt-client:de", 7, true)
	if err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if got != 7 {
		t.Fatalf("усыновление живого интерфейса: got %d, want 7", got)
	}
}

// Пин ЧУЖОЙ записи прокси занят: без поставщика записей инстансов второй
// владелец получил бы имя первого.
func TestAllocIndexRespectsOtherProxyRecordPin(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, rawClientRecord("de", "OpkgTun3", "opkgtun3"))
	alloc := e.alloc(t, nil)

	got, err := alloc("wdtt-client:nl", 0, false)
	if err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if got == 3 {
		t.Fatal("выдан номер, который держит запись другого инстанса")
	}
}

// Сентинел «пина нет» обязан быть вне диапазона: ноль на mips — законный
// номер, и переданный как pinned он выдавался бы вопреки занятости.
func TestAllocIndexWithoutPinDoesNotClaimZero(t *testing.T) {
	e := newOccEnv(t)
	alloc := e.alloc(t, map[int]bool{0: true})

	got, err := alloc("wdtt-client:de", 0, false)
	if err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if got == 0 {
		t.Fatal("занятый нулевой номер выдан как свободный: сентинел спутан с пином")
	}
}

// Ф1: запись AWG-туннеля держит номер с момента СОЗДАНИЯ, интерфейс появится
// только при первом включении. Без этого поставщика прокси занял бы номер
// выключенного туннеля, а первое же его включение усыновило бы чужой
// интерфейс по номеру.
func TestAllocIndexRespectsAwgRecordWithoutLiveInterface(t *testing.T) {
	e := newOccEnv(t)
	if err := e.awg.Save(&storage.AWGTunnel{ID: "awg12", Name: "vpn", Backend: "kernel"}); err != nil {
		t.Fatalf("awg.Save: %v", err)
	}
	alloc := e.alloc(t, nil)

	for i := 0; i <= 15; i++ {
		got, err := alloc("owner-"+string(rune('a'+i)), 0, false)
		if err != nil {
			break
		}
		if got == 12 {
			t.Fatal("выдан номер записи AWG-туннеля без живого интерфейса")
		}
	}
}

// nativewg живёт как Wireguard<N> и OpkgTun не создаёт: его идентификатор
// пула номеров не отнимает.
func TestAllocIndexIgnoresNativeWGRecord(t *testing.T) {
	e := newOccEnv(t)
	if err := e.awg.Save(&storage.AWGTunnel{ID: "awg5", Name: "nwg", Backend: "nativewg"}); err != nil {
		t.Fatalf("awg.Save: %v", err)
	}
	alloc := e.alloc(t, nil)

	got, err := alloc("wdtt-client:de", 5, true)
	if err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if got != 5 {
		t.Fatalf("nativewg-запись отняла номер: got %d, want 5", got)
	}
}

// Удерживающая запись настроек занимает номер, даже когда интерфейса нет и
// Provisioned=false. Ноль — законный номер режимов роутера.
func TestAllocIndexRespectsSettingsHoldAtZero(t *testing.T) {
	e := newOccEnv(t)
	if err := e.settings.SetPolicyTunState(&storage.PolicyTunState{Index: 0}); err != nil {
		t.Fatalf("SetPolicyTunState: %v", err)
	}
	alloc := e.alloc(t, nil)

	got, err := alloc("wdtt-client:de", 0, false)
	if err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if got == 0 {
		t.Fatal("выдан номер, удержанный записью настроек")
	}
}

// Fail-closed: «не смогли посмотреть» обязано быть отказом. Неполная картина
// читается как «номер свободен» — единственное направление ошибки, дающее
// коллизию.
func TestAllocIndexFailsClosedOnOccupancyError(t *testing.T) {
	e := newOccEnv(t)
	broken := storage.OpkgTunPins(func(context.Context) (map[int]bool, error) {
		return nil, errors.New("хранилище недоступно")
	})
	alloc := e.alloc(t, nil, broken)

	if _, err := alloc("wdtt-client:de", 0, false); err == nil {
		t.Fatal("отказ поставщика занятости обязан отказывать аллокацию")
	}
}

// Второй пин СОБСТВЕННОЙ записи чужой не становится: это другой интерфейс
// того же инстанса, и коллизия с ним запрещена.
func TestAllocIndexKeepsSiblingPinOfOwnRecord(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, serverRecord("default", "OpkgTun4", "opkgtun4", "OpkgTun5", "opkgtun5"))
	alloc := e.alloc(t, nil)

	// Запрос WG-половины с пином raw-половины: raw остаётся занятым.
	got, err := alloc("wdtt-server:default/wg", 5, true)
	if err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if got == 5 {
		t.Fatal("выдан номер второй половины собственной записи")
	}
}

// ── listen-порты ─────────────────────────────────────────────────

func newListenAlloc(t *testing.T, e *occEnv) func(string) (string, error) {
	t.Helper()
	return proxyAllocListen(context.Background(),
		proxyrt.NewAllocator(proxyrt.IndexRange{Min: roles.ListenPortMin, Max: roles.ListenPortMax}),
		e.store, e.awg)
}

// Резерв, а не скан: два параллельных Create получают РАЗНЫЕ порты, хотя ни
// один из них ещё не лёг на диск. Стейтлес-скан отдал бы обоим один.
func TestAllocListenReservesAcrossOwners(t *testing.T) {
	e := newOccEnv(t)
	alloc := newListenAlloc(t, e)

	first, err := alloc("wdtt-client:a/listen")
	if err != nil {
		t.Fatalf("AllocListen: %v", err)
	}
	second, err := alloc("wdtt-client:b/listen")
	if err != nil {
		t.Fatalf("AllocListen: %v", err)
	}
	if first == second {
		t.Fatalf("два владельца получили один порт: %s", first)
	}
}

func TestAllocListenSkipsPortsOfExistingRecords(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, instancestore.Record{ID: "a", Kind: instancestore.KindFreeTurnClient,
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}})
	e.putRecord(t, instancestore.Record{ID: "b", Kind: instancestore.KindFreeTurnClient,
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}})
	alloc := newListenAlloc(t, e)

	got, err := alloc("wdtt-client:c/listen")
	if err != nil {
		t.Fatalf("AllocListen: %v", err)
	}
	if got != "127.0.0.1:9002" {
		t.Fatalf("AllocListen = %s, want 127.0.0.1:9002", got)
	}
}

func TestAllocListenExhaustedIsError(t *testing.T) {
	e := newOccEnv(t)
	alloc := newListenAlloc(t, e)
	for i := roles.ListenPortMin; i <= roles.ListenPortMax; i++ {
		if _, err := alloc("owner:" + string(rune(i))); err != nil {
			t.Fatalf("порт %d: %v", i, err)
		}
	}
	if _, err := alloc("owner:last"); err == nil {
		t.Fatal("исчерпанный пул обязан отказывать")
	}
}

// ── возврат пинов ────────────────────────────────────────────────

// recordingBook — ведомость с перехваченными швами listenfirewall.
func recordingBook(keys []string) (*proxyFWBook, *[]int) {
	applied := &[]int{}
	b := newProxyFWBook(keys)
	b.list = func(context.Context) ([]listenfirewall.PortSpec, error) { return nil, nil }
	b.apply = func(_ context.Context, desired []listenfirewall.PortSpec) {
		*applied = append(*applied, len(desired))
	}
	return b, applied
}

func TestReleasePinsWithoutOwnersIsNoop(t *testing.T) {
	opkg := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 0, Max: 3})
	port := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 9000, Max: 9001})
	book, applied := recordingBook([]string{"wdtt-server:default"})

	proxyReleasePins(context.Background(), opkg, port, book, nil)()

	if len(*applied) != 0 {
		t.Fatalf("пустой вызов тронул ведомость портов: %v", *applied)
	}
}

func TestReleasePinsToleratesUnknownOwners(t *testing.T) {
	opkg := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 0, Max: 3})
	port := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 9000, Max: 9001})
	book, _ := recordingBook(nil)

	// Delete зовёт четыре ключа вслепую — ни один из них ведомости не знаком.
	proxyReleasePins(context.Background(), opkg, port, book, nil)(
		"wdtt-client:x", "wdtt-client:x/wg", "wdtt-client:x/raw", "wdtt-client:x/listen")
}

// Освобождение владельца возвращает и номер, и порт: без этого held течёт до
// перезапуска демона, а номера «заняты» несуществующим инстансом.
func TestReleasePinsReturnsIndexAndPort(t *testing.T) {
	opkg := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 0, Max: 0})
	port := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 9000, Max: 9000})
	book, _ := recordingBook(nil)
	if _, err := opkg.AllocIndex("k", -1, nil); err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}
	if _, err := port.AllocIndex("k/listen", 8999, nil); err != nil {
		t.Fatalf("AllocIndex: %v", err)
	}

	proxyReleasePins(context.Background(), opkg, port, book, nil)("k", "k/listen")

	if _, err := opkg.AllocIndex("other", -1, nil); err != nil {
		t.Fatalf("номер не вернулся: %v", err)
	}
	if _, err := port.AllocIndex("other/listen", 8999, nil); err != nil {
		t.Fatalf("порт не вернулся: %v", err)
	}
}

// Снятие вклада из ведомости идёт по ПЕРВОМУ ключу — голому ключу записи,
// под которым инстанс получал хендл.
func TestReleasePinsForgetsFirstKeyInBook(t *testing.T) {
	opkg := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 0, Max: 3})
	port := proxyrt.NewAllocator(proxyrt.IndexRange{Min: 9000, Max: 9001})
	book, applied := recordingBook([]string{"wdtt-server:default"})

	proxyReleasePins(context.Background(), opkg, port, book, nil)(
		"wdtt-server:default", "wdtt-server:default/wg")

	if len(*applied) == 0 {
		t.Fatal("вклад известного ключа не снят: ведомость не приводилась")
	}
}

// ── фабрика инстансов ────────────────────────────────────────────

type stubRegistry struct{}

func (stubRegistry) SetDeclared([]exitreg.ExitDecl) error { return nil }
func (stubRegistry) MarkSeeded(int) error                 { return nil }

type stubSweeper struct{}

func (stubSweeper) Sweep(context.Context, map[string]bool) ([]string, error) { return nil, nil }

type stubJournal struct{}

func (stubJournal) Info(string, string, string) {}
func (stubJournal) Warn(string, string, string) {}

// factoryEnv — минимальное окружение фабрики: конкретные клиенты NDMS фабрика
// только КЛАДЁТ в зависимости ролей, поэтому пустые структуры достаточны —
// прогонов реконсиляции здесь нет.
func newFactoryApp(t *testing.T, book *proxyFWBook) (*app, manager.Factory, *proxyManagerRef) {
	t.Helper()
	dir := t.TempDir()
	a := &app{
		dataDir:       dir,
		shutdownCtx:   context.Background(),
		awgStore:      storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "lock")),
		settingsStore: storage.NewSettingsStore(dir),
		ndmsQueries:   &ndmsquery.Queries{},
		ndmsCommands:  &ndmscommand.Commands{},
	}
	store := instancestore.New(dir)
	ref := &proxyManagerRef{}
	users := wdttusers.New(wdttusers.Deps{
		Records: proxyRecords{ref: ref}, Mutator: proxyMutator{ref: ref},
	})
	factory := a.proxyFactory(ref, nil, newProxyLinkBook(), book,
		proxyrt.NewStateStore(nil, nil), install.New(install.Deps{DataDir: dir}), users, store)
	ref.mgr = manager.New(manager.Deps{
		Store: store, Registry: stubRegistry{}, Sweeper: stubSweeper{},
		Factory: factory, Journal: stubJournal{},
		Seed: func(context.Context) (instancestore.SeedResult, error) {
			return instancestore.SeedResult{}, nil
		},
		PostSeed: func(context.Context, instancestore.SeedResult, map[string]bool) error { return nil },
		AllocIndex: func(_ string, pinned int, havePin bool) (int, error) {
			if havePin {
				return pinned, nil
			}
			return 0, errors.New("в тесте фабрики выделение не нужно")
		},
		AllocListen:  func(string) (string, error) { return "127.0.0.1:9000", nil },
		ReleasePins:  func(...string) {},
		WaitDisabled: func(string, time.Duration) bool { return true },
	})
	return a, factory, ref
}

func freeturnServerRecord(id string) instancestore.Record {
	return instancestore.Record{ID: id, Kind: instancestore.KindFreeTurnServer,
		FreeTurnServer: &roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", OpenFirewall: true}}
}

// Фабрика собирает все четыре роли. Для wdtt-сервера это ещё и утверждение о
// НЕПУСТЫХ Deps.Access/.Ingress: их гард в wdttserver.New — паника, и тест
// фиксирует, что фабрика её не ловит молча.
//
// Возвращаемый тип проверяется отдельно: сброс паузы перезапуска обязан
// доходить до роли, а обёртка, потерявшая ResetStartBackoff, собралась бы
// молча — RunningInstance ловит только отсутствие метода, не подмену.
func TestProxyFactoryBuildsEveryKind(t *testing.T) {
	book, _ := recordingBook(nil)
	_, factory, _ := newFactoryApp(t, book)

	recs := []instancestore.Record{
		rawClientRecord("de", "OpkgTun3", "opkgtun3"),
		serverRecord("default", "OpkgTun4", "opkgtun4", "OpkgTun5", "opkgtun5"),
		{ID: "ft", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}},
		freeturnServerRecord("fts"),
	}
	for _, rec := range recs {
		t.Run(string(rec.Kind), func(t *testing.T) {
			inst, err := factory(rec, &manager.Live{})
			if err != nil {
				t.Fatalf("фабрика: %v", err)
			}
			if _, ok := inst.(*instance.Instance); !ok {
				t.Fatalf("фабрика обязана вернуть instance.Instance без обёрток, got %T", inst)
			}
			inst.Stop()
		})
	}
}

// Ведомость INPUT-портов — ОДНА на процесс: оба серверных инстанса обязаны
// смотреть в неё, иначе они закрывают порты друг друга каждые 15 секунд.
// Наблюдение — через ресурс input_port обеих ролей: его Observe идёт в шов
// list ведомости, и второй экземпляр book сюда бы не дошёл.
func TestProxyFactoryServerRolesShareOneFWBook(t *testing.T) {
	book, _ := recordingBook(nil)
	listed := 0
	book.list = func(context.Context) ([]listenfirewall.PortSpec, error) {
		listed++
		return nil, nil
	}
	_, factory, _ := newFactoryApp(t, book)

	cases := []struct {
		rec instancestore.Record
		cfg any
	}{
		{serverRecord("default", "OpkgTun4", "opkgtun4", "OpkgTun5", "opkgtun5"),
			roles.WdttServerConfig{Listen: "0.0.0.0:56000", Password: "p",
				NatMode: "full", RelayMode: "wg",
				NdmsIface: "OpkgTun4", WgIface: "opkgtun4",
				RawNdmsIface: "OpkgTun5", RawIface: "opkgtun5", OpenFirewall: true}},
		{freeturnServerRecord("fts"),
			roles.FreeTurnServerConfig{Listen: "0.0.0.0:3478", OpenFirewall: true}},
	}
	for _, c := range cases {
		inst, err := factory(c.rec, &manager.Live{})
		if err != nil {
			t.Fatalf("фабрика %s: %v", c.rec.Kind, err)
		}
		defer inst.Stop()
		role := roleOf(t, inst)
		input := findInputPort(t, role.Resources(proxyrt.IntentEnabled, c.cfg, proxyrt.Observations{}))
		if _, err := input.Observe(context.Background()); err != nil {
			t.Fatalf("Observe input_port %s: %v", c.rec.Kind, err)
		}
	}
	if listed != 2 {
		t.Fatalf("ведомость опрошена %d раз(а), а серверных инстансов два: хендлы взяты не у неё", listed)
	}
}

// roleOf — роль собранного инстанса. Инстанс её не отдаёт, поэтому берётся
// через сброс паузы: единственный метод, который у него ЕСТЬ и который идёт
// в роль. Здесь нужен сам объект — читаем поле через известный тип.
func roleOf(t *testing.T, inst manager.RunningInstance) proxyrt.Role {
	t.Helper()
	i, ok := inst.(*instance.Instance)
	if !ok {
		t.Fatalf("не instance.Instance: %T", inst)
	}
	return i.Role()
}

func findInputPort(t *testing.T, res []proxyrt.Resource) *netres.InputPort {
	t.Helper()
	for _, r := range res {
		if r.ID() == roles.RInputPort {
			p, ok := r.(*netres.InputPort)
			if !ok {
				t.Fatalf("input_port не netres.InputPort: %T", r)
			}
			return p
		}
	}
	t.Fatal("роль сервера не объявила input_port")
	return nil
}

// ── маршрутизация поверхности ────────────────────────────────────

// Подпути инстанса обязаны разбираться ДО хендлера инстансов: тот терминален
// и отвечает 404 на неизвестном хвосте, то есть проглотил бы users, link,
// captcha и allowlist целиком.
func TestProxyInstancesDispatcherRoutesSubpaths(t *testing.T) {
	var seen []string
	mark := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, name)
			w.WriteHeader(http.StatusOK)
		}
	}
	dispatch := proxyrtDispatch{
		instances: mark("instances"),
		users: func(w http.ResponseWriter, r *http.Request, key string, sub []string) {
			seen = append(seen, "users:"+key+":"+strings.Join(sub, "/"))
		},
		captcha: func(w http.ResponseWriter, r *http.Request, key string, sub []string) {
			seen = append(seen, "captcha:"+key+":"+strings.Join(sub, "/"))
		},
		allowlist: func(w http.ResponseWriter, r *http.Request, key string, sub []string) {
			seen = append(seen, "allowlist:"+key)
		},
		link:     func(w http.ResponseWriter, r *http.Request, key string) { seen = append(seen, "link:"+key) },
		ensureWG: func(w http.ResponseWriter, r *http.Request, key string) { seen = append(seen, "ensure:"+key) },
		clear:    func(w http.ResponseWriter, r *http.Request, key string) { seen = append(seen, "clear:"+key) },
		refresh:  func(w http.ResponseWriter, r *http.Request, key string) { seen = append(seen, "refresh:"+key) },
	}.handler()

	tests := []struct{ path, want string }{
		{"/api/proxyrt/instances", "instances"},
		{"/api/proxyrt/instances/wdtt-client:de", "instances"},
		{"/api/proxyrt/instances/wdtt-client:de/apply", "instances"},
		{"/api/proxyrt/instances/wdtt-server:d/users", "users:wdtt-server:d:"},
		{"/api/proxyrt/instances/wdtt-server:d/users/secret", "users:wdtt-server:d:secret"},
		{"/api/proxyrt/instances/freeturn-client:d/captcha/status", "captcha:freeturn-client:d:status"},
		{"/api/proxyrt/instances/freeturn-client:d/captcha/generic_proxy", "captcha:freeturn-client:d:generic_proxy"},
		{"/api/proxyrt/instances/freeturn-server:d/allowlist", "allowlist:freeturn-server:d"},
		{"/api/proxyrt/instances/freeturn-server:d/allowlist/abc", "allowlist:freeturn-server:d"},
		{"/api/proxyrt/instances/wdtt-server:d/link", "link:wdtt-server:d"},
		{"/api/proxyrt/instances/wdtt-client:de/ensure-wg-tunnel", "ensure:wdtt-client:de"},
		{"/api/proxyrt/instances/wdtt-client:de/linked-tunnels/clear", "clear:wdtt-client:de"},
		{"/api/proxyrt/instances/wdtt-client:de/subscription/refresh", "refresh:wdtt-client:de"},
		{"/api/proxyrt/instances/wdtt-client:de/неведомое", "instances"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			seen = nil
			dispatch(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, tt.path, nil))
			if len(seen) != 1 || seen[0] != tt.want {
				t.Fatalf("%s → %v, want [%s]", tt.path, seen, tt.want)
			}
		})
	}
}

// fakeRecordSource — источник записей для интеграции решателя капчи.
type fakeRecordSource map[string]instancestore.Record

func (f fakeRecordSource) Get(key string) (instancestore.Record, bool) {
	rec, ok := f[key]
	return rec, ok
}

func (f fakeRecordSource) Records() []instancestore.Record {
	out := make([]instancestore.Record, 0, len(f))
	for _, rec := range f {
		out = append(out, rec)
	}
	return out
}

// Ручки капчи на РЕАЛЬНОМ входе: и статус инстанса, и страница живут под тем
// же подпутём, что ручки самого инстанса, и разъехаться им нельзя. Роль
// решает путь: локальный сервер капчи поднимает только freeturn-клиент.
func TestProxyrtCaptchaRoutingThroughDispatcher(t *testing.T) {
	recs := fakeRecordSource{
		"freeturn-client:ft": {ID: "ft", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}},
		"wdtt-client:de": rawClientRecord("de", "OpkgTun3", "opkgtun3"),
	}
	closedPort := captcha.Listener(func([]int) (int, bool) { return 0, false })
	svc := captcha.New(captcha.Deps{
		Records: recs, Instances: recs,
		Snapshots: func(string) (awgmproto.State, bool) { return awgmproto.State{}, false },
		Log:       func(string) string { return "" },
		Listener:  closedPort,
	})
	instancesHits := 0
	dispatch := proxyrtDispatch{
		instances: func(w http.ResponseWriter, r *http.Request) { instancesHits++ },
		captcha:   svc.Serve,
	}.handler()

	tests := []struct {
		name, path string
		wantCode   int
	}{
		{"статус инстанса", "/api/proxyrt/instances/freeturn-client:ft/captcha/status", http.StatusOK},
		{"страница при закрытом порте", "/api/proxyrt/instances/freeturn-client:ft/captcha", http.StatusServiceUnavailable},
		{"делегирование общему прокси при закрытом порте", "/api/proxyrt/instances/freeturn-client:ft/captcha/generic_proxy?proxy_url=https://oauth.vk.com/", http.StatusServiceUnavailable},
		{"чужая роль", "/api/proxyrt/instances/wdtt-client:de/captcha/status", http.StatusBadRequest},
		{"неизвестный инстанс", "/api/proxyrt/instances/freeturn-client:zz/captcha/status", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			dispatch(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Fatalf("%s → %d, want %d (тело: %s)", tt.path, rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
	if instancesHits != 0 {
		t.Fatalf("хендлер инстансов проглотил подпуть капчи %d раз(а)", instancesHits)
	}
}

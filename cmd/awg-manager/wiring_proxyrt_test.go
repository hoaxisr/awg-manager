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
	"github.com/hoaxisr/awg-manager/internal/api"
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
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/freeturn"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/netres"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttclient"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttserver"
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

// alloc собирает БОЕВУЮ формулу: тот же состав поставщиков, что у проводки,
// минус собственные пины владельца. Живая половина и записи NDMS —
// подставляемые снаружи параметры прод-функции, остальные три поставщика
// настоящие.
func (e *occEnv) alloc(t *testing.T, live map[int]bool) func(string, int, bool) (int, error) {
	t.Helper()
	return e.allocWithNDMS(t, live, func(context.Context) (map[int]bool, error) { return nil, nil })
}

func (e *occEnv) allocWithNDMS(t *testing.T, live map[int]bool, ndmsPins storage.OpkgTunPins) func(string, int, bool) (int, error) {
	t.Helper()
	min, max := mipsRange(t)
	occ := proxyOpkgOccupancy(fakeLiveIfaces{live: live}, ndmsPins, e.awg, e.settings, e.store)
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

	// Пул перебирается целиком: с одной выдачей аллокатор отдал бы младший
	// свободный номер и разошёлся бы с занятостью незаметно.
	for i := 0; i <= 15; i++ {
		got, err := alloc("wdtt-client:nl-"+string(rune('a'+i)), 0, false)
		if err != nil {
			break
		}
		if got == 3 {
			t.Fatal("выдан номер, который держит запись другого инстанса")
		}
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

// Запись NDMS без устройства в /sys держит номер: `ip link del opkgtunN`
// оставляет её живой со state error, и выданный по ней номер отдал бы
// интерфейс с чужой записью.
func TestAllocIndexRespectsNdmsRecordPin(t *testing.T) {
	e := newOccEnv(t)
	alloc := e.allocWithNDMS(t, nil, func(context.Context) (map[int]bool, error) {
		return map[int]bool{9: true}, nil
	})

	for i := 0; i <= 15; i++ {
		got, err := alloc("owner-"+string(rune('a'+i)), 0, false)
		if err != nil {
			break
		}
		if got == 9 {
			t.Fatal("выдан номер, который держит запись NDMS")
		}
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
	alloc := e.allocWithNDMS(t, nil, func(context.Context) (map[int]bool, error) {
		return nil, errors.New("NDMS недоступен")
	})

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

	cases := []struct {
		rec  instancestore.Record
		role func(proxyrt.Role) bool
	}{
		{rawClientRecord("de", "OpkgTun3", "opkgtun3"), func(r proxyrt.Role) bool {
			v, ok := r.(*wdttclient.Role)
			return ok && v != nil
		}},
		{serverRecord("default", "OpkgTun4", "opkgtun4", "OpkgTun5", "opkgtun5"), func(r proxyrt.Role) bool {
			// Серверная роль приходит за гейтом усыновления абонентов
			// (рулинг Н3); проверяется и обёртка, и то, что внутри неё роль.
			w, ok := r.(*proxyAdoptedRole)
			if !ok || w == nil {
				return false
			}
			v, ok := w.inner.(*wdttserver.Role)
			return ok && v != nil
		}},
		{instancestore.Record{ID: "ft", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}},
			func(r proxyrt.Role) bool {
				v, ok := r.(*freeturn.ClientRole)
				return ok && v != nil
			}},
		{freeturnServerRecord("fts"), func(r proxyrt.Role) bool {
			v, ok := r.(*freeturn.ServerRole)
			return ok && v != nil
		}},
	}
	for _, c := range cases {
		t.Run(string(c.rec.Kind), func(t *testing.T) {
			inst, err := factory(c.rec, &manager.Live{})
			if err != nil {
				t.Fatalf("фабрика: %v", err)
			}
			if _, ok := inst.(*instance.Instance); !ok {
				t.Fatalf("фабрика обязана вернуть instance.Instance без обёрток, got %T", inst)
			}
			// Роль не просто «какая-то»: ПУСТОЙ указатель своего типа
			// проходит и типизацию, и nil-гарды instance.New. Так выглядит
			// проглоченная паника гарда wdttserver.New, ловящего непроведённые
			// Access/Ingress.
			if role := roleOf(t, inst); !c.role(role) {
				t.Fatalf("роль собрана не полностью: %T (%v)", role, role)
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
		// Гейт усыновления абонентов снимается: его вердикт к ведомости
		// портов отношения не имеет, а без записи в store он не проходит.
		role := innerRole(roleOf(t, inst))
		input := findInputPort(t, role.Resources(proxyrt.IntentEnabled, c.cfg, proxyrt.Observations{}))
		// Исход наблюдения не важен: сверяется, ЧЬЯ ведомость его обслужила.
		// Чужая ответила бы настоящим listenfirewall — то есть попыткой
		// запустить iptables, которого в тесте нет.
		_, _ = input.Observe(context.Background())
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

// innerRole снимает обёртки проводки: тесты, которым нужна сама декларация,
// не должны знать, сколько гейтов роль пережила.
func innerRole(r proxyrt.Role) proxyrt.Role {
	if w, ok := r.(*proxyAdoptedRole); ok {
		return w.inner
	}
	return r
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

// Амендмент B: поле связи строго по роли. Перепутанное не даёт ни ошибки, ни
// отказа — только пустой список связанных туннелей и вечное молчание.
func TestProxyLinkedFieldFollowsRole(t *testing.T) {
	if got := proxyLinkedField(instancestore.KindWdttClient); got != api.LinkedWdtt {
		t.Errorf("wdtt-клиент → %v, want LinkedWdtt", got)
	}
	if got := proxyLinkedField(instancestore.KindFreeTurnClient); got != api.LinkedFreeTurn {
		t.Errorf("freeturn-клиент → %v, want LinkedFreeTurn", got)
	}
}

// ── аллокация и транзакция хранилища ─────────────────────────────

type fakeRunning struct{}

func (fakeRunning) Start(context.Context)       {}
func (fakeRunning) Post(proxyrt.EventKind) bool { return true }
func (fakeRunning) ResetStartBackoff()          {}
func (fakeRunning) Stop()                       {}

// newProdAllocManager — менеджер с БОЕВЫМИ аллокаторами поверх настоящего
// store: только на них воспроизводится дедлок «выделение под замком
// транзакции», потому что читают они тот же store.
func newProdAllocManager(t *testing.T, e *occEnv) *manager.Manager {
	t.Helper()
	min, max := mipsRange(t)
	occ := proxyOpkgOccupancy(fakeLiveIfaces{}, func(context.Context) (map[int]bool, error) {
		return nil, nil
	}, e.awg, e.settings, e.store)
	ctx := context.Background()
	return manager.New(manager.Deps{
		Store: e.store, Registry: stubRegistry{}, Sweeper: stubSweeper{},
		Factory: func(instancestore.Record, *manager.Live) (manager.RunningInstance, error) {
			return fakeRunning{}, nil
		},
		Journal: stubJournal{},
		Seed: func(context.Context) (instancestore.SeedResult, error) {
			st, err := e.store.Load()
			return instancestore.SeedResult{State: st}, err
		},
		PostSeed: func(context.Context, instancestore.SeedResult, map[string]bool) error { return nil },
		AllocIndex: proxyAllocIndex(ctx,
			proxyrt.NewAllocator(proxyrt.IndexRange{Min: min, Max: max}), min, occ, e.store),
		AllocListen: proxyAllocListen(ctx,
			proxyrt.NewAllocator(proxyrt.IndexRange{Min: roles.ListenPortMin, Max: roles.ListenPortMax}),
			e.store, e.awg),
		ReleasePins:  func(...string) {},
		WaitDisabled: func(string, time.Duration) bool { return true },
	})
}

// Х1: правка, которой нужны новые пины (клиент wg → raw), обязана дойти до
// конца. Аллокаторы читают тот же store, чей нереентрантный замок держит
// транзакция, поэтому выделение ВНУТРИ неё вешало Update навсегда — а с ним
// m.mu, то есть всю поверхность /api/proxyrt и Shutdown, до перезапуска демона.
func TestUpdateAllocatesOutsideStoreTransaction(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, instancestore.Record{ID: "de", Kind: instancestore.KindWdttClient,
		Enabled:    true,
		WdttClient: &roles.WdttClientConfig{Mode: "wg", Peer: "1.2.3.4:5", Password: "p"}})
	mgr := newProdAllocManager(t, e)
	if err := mgr.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- mgr.Update(context.Background(), "wdtt-client:de",
			func(r *instancestore.Record) error {
				r.WdttClient.Mode = "raw"
				return nil
			})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Update завис: выделение пинов идёт под замком транзакции хранилища")
	}

	st, err := e.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	c := st.Records[0].WdttClient
	if c.NdmsIface == "" || c.RawIface == "" || c.Listen == "" {
		t.Fatalf("пины не выделены: %+v", c)
	}
}

// ── гейт усыновления абонентов (рулинг Н3) ───────────────────────

// countingRole — внутренняя роль гейта: считает обращения и умеет сбрасывать
// паузу перезапуска.
type countingRole struct {
	calls  int
	resets int
}

func (r *countingRole) Resources(proxyrt.Intent, any, proxyrt.Observations) []proxyrt.Resource {
	r.calls++
	return []proxyrt.Resource{proxyBlocked{id: "inner", reason: errors.New("внутренняя ведомость")}}
}

func (r *countingRole) ResetStartBackoff() { r.resets++ }

// Fail-closed: пока абоненты не усыновлены, ведомость роли не объявляется
// вовсе — процесс не стартует, а прогон уходит в failed с причиной. Иначе
// материализация passwords.json НЕОБРАТИМО отберёт доступ у абонентов,
// заведённых телеграм-ботом или admin-API форка.
func TestUsersGateBlocksEnabledServerUntilAdopted(t *testing.T) {
	inner := &countingRole{}
	fails, syncs := 2, 0
	role := &proxyAdoptedRole{inner: inner, gate: &proxyUsersGate{sync: func() error {
		syncs++
		if fails > 0 {
			fails--
			return errors.New("configDir не задан")
		}
		return nil
	}}}

	res := role.Resources(proxyrt.IntentEnabled, nil, proxyrt.Observations{})
	if len(res) != 1 || res[0].ID() != proxyUsersResource {
		t.Fatalf("ведомость при неусыновлённых абонентах: %v", res)
	}
	if inner.calls != 0 {
		t.Fatal("ведомость роли объявлена до усыновления — процесс мог стартовать")
	}
	// Приговор виден и валит прогон: шаг с ошибкой применения = фаза failed.
	steps := res[0].Plan(proxyrt.Observation{})
	if len(steps) != 1 || steps[0].Op != "fail" {
		t.Fatalf("гейт обязан планировать приговор: %v", steps)
	}
	if err := res[0].Apply(context.Background(), steps[0]); err == nil ||
		!strings.Contains(err.Error(), "configDir") {
		t.Fatalf("причина обязана доходить до пользователя: %v", err)
	}
	if res[0].RecheckAfter() == 0 {
		t.Fatal("без подстраховочной сверки гейт не повторится: внешнего события у этой беды нет")
	}

	// Выключенный инстанс гейта не знает: снятие ресурсов надо доводить в
	// любом состоянии абонентов.
	if role.Resources(proxyrt.IntentDisabled, nil, proxyrt.Observations{})[0].ID() == proxyUsersResource {
		t.Fatal("гейт запер teardown выключенного инстанса")
	}
	if inner.calls != 1 {
		t.Fatalf("ведомость выключенного инстанса не объявлена: calls=%d", inner.calls)
	}

	// Повтор: усыновление прошло — роль работает; дальше гейт молчит.
	role.Resources(proxyrt.IntentEnabled, nil, proxyrt.Observations{})
	if fails != 0 {
		t.Fatalf("гейт не повторил усыновление: осталось %d отказов", fails)
	}
	role.Resources(proxyrt.IntentEnabled, nil, proxyrt.Observations{})
	role.Resources(proxyrt.IntentEnabled, nil, proxyrt.Observations{})
	if inner.calls != 3 {
		t.Fatalf("после успеха ведомость роли обязана объявляться: calls=%d", inner.calls)
	}
	// Цикл абонентов — путь СТАРТА, а не каждого прогона: он переписывает
	// passwords.json, и повтор на каждом тике точил бы флеш роутера.
	if syncs != 3 {
		t.Fatalf("усыновление звалось %d раз(а): после успеха гейт обязан молчать", syncs)
	}

	// Амендмент C: сброс паузы обязан ДОХОДИТЬ до внутренней роли.
	role.ResetStartBackoff()
	if inner.resets != 1 {
		t.Fatalf("сброс паузы не дошёл до роли: resets=%d", inner.resets)
	}
}

// ── связанные туннели по роли (амендмент B, место вызова) ────────

func findLinkedEndpoint(t *testing.T, res []proxyrt.Resource) proxyrt.Resource {
	t.Helper()
	for _, r := range res {
		if r.ID() == roles.RLinkedEndpoint {
			return r
		}
	}
	t.Fatal("роль клиента не объявила linked_endpoint")
	return nil
}

// Наблюдается РЕЗУЛЬТАТ, а не хелпер: у wdtt-клиента связанным считается
// туннель со своим полем связи, у freeturn-клиента — со своим. Перепутанное
// поле не даёт ни ошибки, ни отказа — только пустой список и вечное молчание.
func TestProxyFactoryLinksTunnelsByRoleField(t *testing.T) {
	book, _ := recordingBook(nil)
	a, factory, _ := newFactoryApp(t, book)
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awg10", Name: "wdtt", WdttClientID: "de"},
		{ID: "awg11", Name: "ft", FreeTurnClientID: "ft"},
	} {
		if err := a.awgStore.Save(tun); err != nil {
			t.Fatalf("awg.Save: %v", err)
		}
	}

	cases := []struct {
		rec instancestore.Record
		cfg any
	}{
		{instancestore.Record{ID: "de", Kind: instancestore.KindWdttClient,
			WdttClient: &roles.WdttClientConfig{Mode: "wg", Peer: "1.2.3.4:5", Listen: "127.0.0.1:9000"}},
			roles.WdttClientConfig{Mode: "wg", Peer: "1.2.3.4:5", Listen: "127.0.0.1:9000"}},
		{instancestore.Record{ID: "ft", Kind: instancestore.KindFreeTurnClient,
			FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}},
			roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}},
	}
	for _, c := range cases {
		t.Run(string(c.rec.Kind), func(t *testing.T) {
			inst, err := factory(c.rec, &manager.Live{})
			if err != nil {
				t.Fatalf("фабрика: %v", err)
			}
			defer inst.Stop()
			role := innerRole(roleOf(t, inst))
			linked := findLinkedEndpoint(t,
				role.Resources(proxyrt.IntentDisabled, c.cfg, proxyrt.Observations{}))
			obs, err := linked.Observe(context.Background())
			if err != nil {
				t.Fatalf("Observe linked_endpoint: %v", err)
			}
			if obs.Attrs["total"] != "1" {
				t.Fatalf("связанных туннелей %q, want 1: поле связи не следует роли",
					obs.Attrs["total"])
			}
		})
	}
}

// ── ведомость INPUT-портов: только серверные ключи ───────────────

// Амендмент A: клиентских ключей в списке ожидания быть не должно — у
// клиентских ролей ресурса input_port нет, и лишний ключ никогда не отчитается,
// продержав окно щажения чужих портов все две минуты.
func TestProxyServerKeysAreServersOnly(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, rawClientRecord("de", "OpkgTun3", "opkgtun3"))
	e.putRecord(t, serverRecord("default", "OpkgTun4", "opkgtun4", "OpkgTun5", "opkgtun5"))
	e.putRecord(t, instancestore.Record{ID: "ftc", Kind: instancestore.KindFreeTurnClient,
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9000"}})
	e.putRecord(t, freeturnServerRecord("fts"))

	got := proxyServerKeys(e.store)

	want := map[string]bool{"wdtt-server:default": true, "freeturn-server:fts": true}
	if len(got) != len(want) {
		t.Fatalf("proxyServerKeys() = %v, want ключи %v", got, want)
	}
	for _, k := range got {
		if !want[k] {
			t.Errorf("лишний ключ %q: ресурса input_port у этой роли нет", k)
		}
	}
}

// ── уборка связанных туннелей по роли (амендмент B, F1) ──────────

// stubTunnelSvc — служба туннелей с одним рабочим методом: уборщику нужен
// только Delete, остальное встроенным интерфейсом не реализовано намеренно —
// вызов чего-то ещё уронит тест, а не пройдёт молча.
type stubTunnelSvc struct {
	api.TunnelService
	deleted []string
}

func (s *stubTunnelSvc) Delete(_ context.Context, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

// Наблюдается РЕЗУЛЬТАТ уборки: клиент каждой подсистемы сносит туннели своего
// поля связи и только их. Идентификатор у обоих клиентов один — поле остаётся
// единственным различителем, поэтому перепутанные поля краснеют.
func TestProxyLinkedCleanersDeleteOwnFieldOnly(t *testing.T) {
	dir := t.TempDir()
	store := storage.NewAWGTunnelStoreWithLockDir(filepath.Join(dir, "tunnels"), filepath.Join(dir, "lock"))
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awg10", Name: "wdtt", WdttClientID: "same"},
		{ID: "awg11", Name: "ft", FreeTurnClientID: "same"},
	} {
		if err := store.Save(tun); err != nil {
			t.Fatalf("awg.Save: %v", err)
		}
	}

	tests := []struct {
		kind instancestore.Kind
		want string
	}{
		{instancestore.KindWdttClient, "awg10"},
		{instancestore.KindFreeTurnClient, "awg11"},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			svc := &stubTunnelSvc{}
			cleaners := proxyLinkedCleaners(store, svc, nil, nil)
			cleaner, ok := cleaners[tt.kind]
			if !ok {
				t.Fatalf("уборщика для роли %s нет", tt.kind)
			}
			deleted, errs := cleaner.DeleteLinked(context.Background(), "same")
			if len(errs) != 0 {
				t.Fatalf("уборка: %v", errs)
			}
			if len(deleted) != 1 || deleted[0] != tt.want {
				t.Fatalf("снесено %v, want [%s]: поле связи не следует роли", deleted, tt.want)
			}
			if len(svc.deleted) != 1 || svc.deleted[0] != tt.want {
				t.Fatalf("служба туннелей получила %v, want [%s]", svc.deleted, tt.want)
			}
		})
	}
}

// Заряд гейта, а не его тип (F2): фабрика обязана подключить в обёртку
// НАСТОЯЩИЙ цикл абонентов. Здесь записи инстанса в менеджере нет, поэтому
// усыновление отказывает — и включённый сервер обязан остаться без ведомости
// роли (стартовать нечему) и получить фазу failed с причиной.
func TestProxyFactoryWdttServerBlockedUntilUsersAdopted(t *testing.T) {
	book, _ := recordingBook(nil)
	_, factory, _ := newFactoryApp(t, book)
	rec := serverRecord("default", "OpkgTun4", "opkgtun4", "OpkgTun5", "opkgtun5")
	cfg := roles.WdttServerConfig{Listen: "0.0.0.0:56000", Password: "p",
		NatMode: "full", RelayMode: "wg",
		NdmsIface: "OpkgTun4", WgIface: "opkgtun4",
		RawNdmsIface: "OpkgTun5", RawIface: "opkgtun5", OpenFirewall: true}

	inst, err := factory(rec, &manager.Live{})
	if err != nil {
		t.Fatalf("фабрика: %v", err)
	}
	defer inst.Stop()
	role := roleOf(t, inst)

	// Сперва состав: под гейтом объявлен РОВНО приговор и ничего больше.
	// Проверка идёт первой намеренно — при выключенном гейте ведомость роли
	// ушла бы наблюдать NDMS, которого в тесте нет.
	res := role.Resources(proxyrt.IntentEnabled, cfg, proxyrt.Observations{})
	if len(res) != 1 || res[0].ID() != proxyUsersResource {
		ids := make([]proxyrt.ResourceID, 0, len(res))
		for _, r := range res {
			ids = append(ids, r.ID())
		}
		t.Fatalf("под неусыновлёнными абонентами объявлено %v: цикл абонентов не подключён", ids)
	}

	// Теперь исход прогона: фаза failed с причиной, а не тихое settled.
	result, phase := proxyrt.NewReconciler(role, cfg, proxyrt.ReconcileOpts{MaxPasses: 3}).
		Run(context.Background(), proxyrt.IntentEnabled)
	if phase != proxyrt.PhaseFailed {
		t.Fatalf("фаза %q, want failed", phase)
	}
	if len(result.States) != 1 || result.States[0].ID != proxyUsersResource {
		t.Fatalf("состояние прогона: %+v", result.States)
	}
	if !strings.Contains(result.States[0].Error, "абоненты сервера не усыновлены") {
		t.Fatalf("причина не доехала до пользователя: %+v", result.States[0])
	}
}

// Мутатор — ЧУЖОЕ замыкание, и исполняться оно обязано ровно один раз:
// холостой прогон по копии ради «какие пины понадобятся» был бы вторым
// исполнением, а среди мутаторов есть считающие новый состав от актуальной
// записи (абоненты сервера). Правка берётся с аллокацией — на ней соблазн
// прогнать мутатора дважды и возникает.
func TestUpdateRunsMutatorExactlyOnce(t *testing.T) {
	e := newOccEnv(t)
	e.putRecord(t, instancestore.Record{ID: "de", Kind: instancestore.KindWdttClient,
		Enabled:    true,
		WdttClient: &roles.WdttClientConfig{Mode: "wg", Peer: "1.2.3.4:5", Password: "p"}})
	mgr := newProdAllocManager(t, e)
	if err := mgr.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}

	calls := 0
	if err := mgr.Update(context.Background(), "wdtt-client:de",
		func(r *instancestore.Record) error {
			calls++
			r.WdttClient.Mode = "raw"
			return nil
		}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if calls != 1 {
		t.Fatalf("мутатор исполнен %d раз(а), want 1", calls)
	}
}

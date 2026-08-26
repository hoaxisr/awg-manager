package manager

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/exitreg"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

type fakeRegistry struct {
	mu       sync.Mutex
	calls    []string // "seeded:N" | "declared:N"
	declared [][]exitreg.ExitDecl
	failSet  error
	failMark error
	// dropped — id, снятые адресно (DropMirror). Список, а не счётчик: не тот
	// id снёс бы карточку чужого туннеля.
	dropped  []string
	failDrop error
}

func (f *fakeRegistry) DropMirror(id, ownerInstanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, id)
	return f.failDrop
}

func (f *fakeRegistry) MarkSeeded(n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "seeded:"+itoa(n))
	return f.failMark
}

func (f *fakeRegistry) SetDeclared(list []exitreg.ExitDecl) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "declared:"+itoa(len(list)))
	f.declared = append(f.declared, append([]exitreg.ExitDecl(nil), list...))
	return f.failSet
}

func itoa(n int) string { return string(rune('0' + n)) } // n < 10 в тестах

type fakeSweeper struct {
	mu    sync.Mutex
	calls []map[string]bool
	// owned — что сканер видит на роутере. Отдельно от calls: ведомость
	// удаления при незаверенном посеве строится ИЗ НЕГО, а не из записей.
	owned    []string
	ownedErr error
}

func (f *fakeSweeper) OwnedNames(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owned, f.ownedErr
}

func (f *fakeSweeper) Sweep(_ context.Context, declared map[string]bool) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, declared)
	return nil, nil
}

func (f *fakeSweeper) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

type fakeInstance struct {
	mu               sync.Mutex
	started, stopped bool
	posts            []proxyrt.EventKind
	// calls — ПОРЯДОК обращений менеджера к инстансу. Сброс паузы обязан
	// ложиться ДО побудки, и факта вызова тут мало: сброс после побудки
	// пропадает впустую (см. TestUpdateResetsStartBackoffBeforeWakeup).
	calls  []string
	onStop func() // для TestStopRunsOutsideManagerLock
}

func (f *fakeInstance) Start(context.Context) { f.mu.Lock(); f.started = true; f.mu.Unlock() }
func (f *fakeInstance) Stop() {
	if f.onStop != nil {
		f.onStop()
	}
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}
func (f *fakeInstance) Post(k proxyrt.EventKind) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, k)
	f.calls = append(f.calls, "post:"+string(k))
	return true
}

func (f *fakeInstance) ResetStartBackoff() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "reset")
}

// callTail — последние n обращений: хвост, а не весь список, потому что boot
// кладёт свои будильники раньше проверяемой правки.
func (f *fakeInstance) callTail(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) < n {
		n = len(f.calls)
	}
	return append([]string(nil), f.calls[len(f.calls)-n:]...)
}

func (f *fakeInstance) resetCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == "reset" {
			n++
		}
	}
	return n
}

type recJournal struct {
	mu   sync.Mutex
	rows []string
	// msgs — ТЕКСТЫ строк. Отдельно от rows: там только действие, и на нём
	// «строку написали» не отличить от «написали не о том».
	msgs []string
}

func (j *recJournal) Info(a, _, m string) {
	j.mu.Lock()
	j.rows = append(j.rows, "info:"+a)
	j.msgs = append(j.msgs, m)
	j.mu.Unlock()
}
func (j *recJournal) Warn(a, _, m string) {
	j.mu.Lock()
	j.rows = append(j.rows, "warn:"+a)
	j.msgs = append(j.msgs, m)
	j.mu.Unlock()
}

// journalMsgs — снимок текстов под тем же замком, что и запись.
func (j *recJournal) journalMsgs() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.msgs...)
}

type env struct {
	m          *Manager
	st         *instancestore.Store
	dir        string
	reg        *fakeRegistry
	sw         *fakeSweeper
	j          *recJournal
	instances  map[string]*fakeInstance
	factoryN   map[string]int
	seedErr    error
	seedRes    instancestore.SeedResult
	seedResSet bool // ИСПРАВЛЕНИЕ ошибки фикстуры ред. 1: явный флаг вместо
	// ветвления по Records == nil (оно делало seedRes недостижимым)
	postSeed [][2]bool // {SeededNow, вызван}
	// postSeedNDMS — ведомость NDMS-имён, ДОШЕДШАЯ до уборочных шагов (I-1
	// ревью: факта вызова мало, пустая или nil-карта = чистка правил с живых
	// объявленных интерфейсов).
	postSeedNDMS []map[string]bool
	factoryErr   error // отказ сборки инстанса (I-2 ревью)
	waited       []string
	waitHook     func() // срабатывает внутри WaitDisabled (нужен тесту воскрешения)
	seedHook     func() // срабатывает ВНУТРИ посева (нужен тесту сериализации Boot)
	released     [][]string
	allocN       int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{st: instancestore.New(dir), dir: dir, reg: &fakeRegistry{},
		sw: &fakeSweeper{}, j: &recJournal{},
		instances: map[string]*fakeInstance{}, factoryN: map[string]int{}}
	e.m = New(Deps{
		Store:    e.st,
		Registry: e.reg,
		Sweeper:  e.sw,
		Journal:  e.j,
		Factory: func(rec instancestore.Record, live *Live) (RunningInstance, error) {
			if e.factoryErr != nil {
				return nil, e.factoryErr
			}
			fi := &fakeInstance{}
			e.instances[rec.Key()] = fi
			e.factoryN[rec.Key()]++
			return fi, nil
		},
		Seed: func(ctx context.Context) (instancestore.SeedResult, error) {
			if e.seedHook != nil {
				e.seedHook()
			}
			if e.seedErr != nil {
				return instancestore.SeedResult{}, e.seedErr
			}
			if e.seedResSet {
				return e.seedRes, nil
			}
			st, err := e.st.Load()
			if err != nil {
				return instancestore.SeedResult{}, err
			}
			return instancestore.SeedResult{State: st, SeededNow: !st.Seeded}, nil
		},
		PostSeed: func(_ context.Context, res instancestore.SeedResult, declaredNDMS map[string]bool) error {
			e.postSeed = append(e.postSeed, [2]bool{res.SeededNow, true})
			e.postSeedNDMS = append(e.postSeedNDMS, declaredNDMS)
			return nil
		},
		AllocIndex: func(_ string, pinned int, havePin bool) (int, error) {
			if havePin {
				return pinned, nil
			}
			e.allocN++
			return 30, nil
		},
		AllocListen: func(string) (string, error) { return "127.0.0.1:9007", nil },
		ReleasePins: func(keys ...string) { e.released = append(e.released, keys) },
		WaitDisabled: func(key string, _ time.Duration) bool {
			e.waited = append(e.waited, key)
			if e.waitHook != nil {
				e.waitHook()
			}
			return true
		},
	})
	return e
}

func rawRec(id, ndms, kernel string) instancestore.Record {
	return instancestore.Record{ID: id, Kind: instancestore.KindWdttClient,
		Name: "Имя", Enabled: true,
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Listen: "127.0.0.1:9000",
			Peer: "1.2.3.4:5", Password: "pw", VKHashes: "h",
			NdmsIface: ndms, RawIface: kernel}}
}

func ftRec(id string) instancestore.Record {
	return instancestore.Record{ID: id, Kind: instancestore.KindFreeTurnClient,
		Name: "FT", Enabled: true,
		FreeTurnClient: &roles.FreeTurnClientConfig{Listen: "127.0.0.1:9001"}}
}

func seedState(t *testing.T, e *env, recs ...instancestore.Record) {
	t.Helper()
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.Records = append(st.Records, recs...)
		st.SeededFrom = []string{"test"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func boot(t *testing.T, e *env) {
	t.Helper()
	if err := e.m.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBootCertifiesTheSameListItDeclares(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), ftRec("ft"))
	boot(t, e)
	// Требование 2: n = число ИНСТАНСОВ того же списка; порядок seeded → declared.
	if len(e.reg.calls) < 2 || e.reg.calls[0] != "seeded:2" || e.reg.calls[1] != "declared:1" {
		t.Fatalf("порядок писателя: %v (ждали [seeded:2 declared:1 …]: 2 инстанса, 1 выход)", e.reg.calls)
	}
	if e.reg.declared[0][0].ID != "wdttraw-de" {
		t.Fatalf("ExitID: %+v", e.reg.declared[0][0])
	}
	if len(e.instances) != 2 {
		t.Fatalf("инстансов %d (disabled — тоже живая декларация)", len(e.instances))
	}
	for k, fi := range e.instances {
		if !fi.started {
			t.Fatalf("%s не стартовал", k)
		}
	}
	if info := e.m.SeedInfo(); !info.Booted || !info.Certified {
		t.Fatalf("SeedInfo после чистого боота: %+v", info)
	}
}

func TestBootSeedFailureStartsNothingAndCertifiesNothing(t *testing.T) {
	e := newEnv(t)
	e.seedErr = errors.New("rci down")
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("ошибка посева обязана быть ошибкой боота")
	}
	if len(e.reg.calls) != 0 {
		t.Fatalf("писатель реестра не имел права работать: %v", e.reg.calls)
	}
	if len(e.instances) != 0 {
		t.Fatal("инстансы не должны стартовать без посева")
	}
	if info := e.m.SeedInfo(); info.Booted || info.Err == "" {
		t.Fatalf("причина отказа посева обязана быть видна (Р9): %+v", info)
	}
}

// Амендмент D: отказ посева обязан снимать признак боота, а не только писать
// ошибку. Иначе повторный Boot после успешного отдаёт Booted=true со списком
// записей ПРОШЛОГО боота и текстом новой ошибки.
func TestBootSeedFailureAfterSuccessDropsBooted(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if info := e.m.SeedInfo(); !info.Booted {
		t.Fatalf("первый боот обязан пройти: %+v", info)
	}

	e.seedErr = errors.New("rci down")
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("ошибка посева обязана быть ошибкой боота")
	}
	if info := e.m.SeedInfo(); info.Booted {
		t.Fatalf("после отказа посева Booted обязан быть снят: %+v", info)
	}
}

// Тот же класс, что и у отказа посева, на двух других ветках боота: и отказ
// объявления выходов, и отказ сборки инстанса оставляли Booted=true со списком
// записей ПРОШЛОГО боота.
func TestBootDeclareFailureDropsBooted(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)

	e.reg.failSet = errors.New("реестр недоступен")
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("отказ объявления обязан быть ошибкой боота")
	}
	if info := e.m.SeedInfo(); info.Booted {
		t.Fatalf("после отказа объявления Booted обязан быть снят: %+v", info)
	}
}

func TestBootFactoryFailureDropsBooted(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)

	// Новая запись: живые инстансы повторный боот не пересоздаёт, и без неё
	// фабрику никто не позовёт.
	seedState(t, e, ftRec("ft"))
	e.factoryErr = errors.New("бинарь не поставлен")
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("отказ фабрики обязан быть ошибкой боота")
	}
	if info := e.m.SeedInfo(); info.Booted {
		t.Fatalf("после отказа фабрики Booted обязан быть снят: %+v", info)
	}
}

func TestBootMarkSeededFailureIsLoggedNotFatal(t *testing.T) {
	e := newEnv(t)
	e.reg.failMark = errors.New("призраки в каталоге")
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e) // не фатально: заперта только уборка
	if len(e.reg.calls) < 2 || e.reg.calls[1] != "declared:1" {
		t.Fatalf("объявление обязано пройти: %v", e.reg.calls)
	}
}

func TestSeedInfoReportsCertification(t *testing.T) {
	// Щ8: запертый гейт отличим от несостоявшегося посева.
	e := newEnv(t)
	e.reg.failMark = errors.New("призраки в каталоге")
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	info := e.m.SeedInfo()
	if !info.Booted || info.Certified || info.Err == "" {
		t.Fatalf("SeedInfo: %+v (ждали Booted=true, Certified=false, Err непуст)", info)
	}
}

func TestBootLeavesGateLockedWhenOldConfigWasSkipped(t *testing.T) {
	// Пропущенный источник занижает число инстансов: сертифицируй мы такой
	// посев — уборка снесла бы зеркальные записи непереехавших инстансов
	// НЕОБРАТИМО. Поэтому MarkSeeded не зовётся вовсе, а рантайм при этом
	// поднимается: битый файл никто не починит, и отказ боота запер бы
	// управление прокси навсегда (амендмент D).
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	st.SkippedSources = []instancestore.SkippedSource{{File: "wdtt.json", Reason: "поле не того типа"}}
	e.seedRes = instancestore.SeedResult{State: st}
	e.seedResSet = true
	boot(t, e)
	for _, c := range e.reg.calls {
		if strings.HasPrefix(c, "seeded:") {
			t.Fatalf("сертификация обязана быть пропущена: %v", e.reg.calls)
		}
	}
	if len(e.reg.calls) != 1 || e.reg.calls[0] != "declared:1" {
		t.Fatalf("объявление выходов обязано пройти: %v", e.reg.calls)
	}
	if len(e.instances) != 1 {
		t.Fatalf("инстансы обязаны стартовать: %d", len(e.instances))
	}
	info := e.m.SeedInfo()
	if !info.Booted || info.Certified || info.Err == "" {
		t.Fatalf("SeedInfo: %+v (ждали Booted=true, Certified=false, Err непуст)", info)
	}
	if len(info.Skipped) != 1 || info.Skipped[0].File != "wdtt.json" {
		t.Fatalf("пропущенный источник обязан быть виден наружу: %+v", info.Skipped)
	}
}

func TestBootSetDeclaredFailureIsFatal(t *testing.T) {
	e := newEnv(t)
	e.reg.failSet = errors.New("дубликат id")
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("отказ объявления — отказ боота: инстансы с невидимыми выходами не стартуют")
	}
	if len(e.instances) != 0 {
		t.Fatal("инстансы не должны стартовать")
	}
}

func TestBootFirstSeedRunsCleanupSteps(t *testing.T) {
	e := newEnv(t)
	e.seedRes = instancestore.SeedResult{State: instancestore.State{Seeded: true}, SeededNow: true}
	e.seedResSet = true
	boot(t, e)
	if len(e.postSeed) != 1 || !e.postSeed[0][0] {
		t.Fatalf("PostSeed при первом посеве: %v (ждали один вызов с SeededNow=true)", e.postSeed)
	}
}

func TestBootCallsPostSeedAlways(t *testing.T) {
	// Замечание 2 ревью А: обнуление адресов — на каждом бооте, не только при
	// первом посеве; различает SeededNow сам PostSeed (задача 6).
	e := newEnv(t)
	e.seedRes = instancestore.SeedResult{State: instancestore.State{Seeded: true}}
	e.seedResSet = true
	boot(t, e)
	if len(e.postSeed) != 1 || e.postSeed[0][0] {
		t.Fatalf("PostSeed на повторном бооте: %v (ждали один вызов с SeededNow=false)", e.postSeed)
	}
}

func TestCreateDeclaresBeforePersistAndRefusesOnRegistryError(t *testing.T) {
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	e.reg.failSet = errors.New("невалидное объявление")
	if err := e.m.Create(context.Background(), rawRec("de", "OpkgTun18", "opkgtun18")); err == nil {
		t.Fatal("отказ реестра — отказ операции (требование 15)")
	}
	st, _ := e.st.Load()
	if len(st.Records) != 0 {
		t.Fatal("конфиг не имел права лечь на диск после отказа реестра")
	}
}

func TestCreatePersistsAndStarts(t *testing.T) {
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	if err := e.m.Create(context.Background(), rawRec("de", "OpkgTun18", "opkgtun18")); err != nil {
		t.Fatal(err)
	}
	st, _ := e.st.Load()
	if len(st.Records) != 1 {
		t.Fatal("запись не легла")
	}
	if fi := e.instances["wdtt-client:de"]; fi == nil || !fi.started {
		t.Fatal("инстанс не стартовал")
	}
}

func TestCreateAllocatesMissingPinsAndListen(t *testing.T) {
	// Щ1 (блокирующая): без этого создание raw-клиента через API невозможно —
	// validateState отвергает запись без пинов.
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	rec := instancestore.Record{ID: "np", Kind: instancestore.KindWdttClient,
		Name: "N", Enabled: true,
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Peer: "1.1.1.1:1",
			Password: "pw", VKHashes: "h"}} // без пинов и listen
	if err := e.m.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	st, _ := e.st.Load()
	c, _ := st.Records[0].WdttClientConfig()
	if c.NdmsIface != "OpkgTun30" || c.RawIface != "opkgtun30" {
		t.Fatalf("пины не выделены: %+v", c)
	}
	if c.Listen != "127.0.0.1:9007" {
		t.Fatalf("listen не выделен: %+v", c)
	}
}

func TestUpdateModeSwitchAllocatesPins(t *testing.T) {
	// Щ1: PATCH wg → raw обязан получить пины тем же путём.
	e := newEnv(t)
	wg := instancestore.Record{ID: "w", Kind: instancestore.KindWdttClient,
		Name: "W", Enabled: true,
		WdttClient: &roles.WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9000",
			Peer: "p:1", Password: "pw", VKHashes: "h"}}
	seedState(t, e, wg)
	boot(t, e)
	if err := e.m.Update(context.Background(), "wdtt-client:w", func(r *instancestore.Record) error {
		r.WdttClient.Mode = "raw"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := e.st.Load()
	c, _ := st.Records[0].WdttClientConfig()
	if c.NdmsIface == "" || c.RawIface == "" {
		t.Fatalf("смена режима не выделила пины: %+v", c)
	}
}

func TestSetEnabledPostsIntentChangedAndKeepsDeclared(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if err := e.m.SetEnabled(context.Background(), "wdtt-client:de", false); err != nil {
		t.Fatal(err)
	}
	fi := e.instances["wdtt-client:de"]
	found := false
	for _, k := range fi.posts {
		if k == proxyrt.EventIntentChanged {
			found = true
		}
	}
	if !found {
		t.Fatal("смена намерения обязана будить воркер")
	}
	last := e.reg.declared[len(e.reg.declared)-1]
	if len(last) != 1 || last[0].Enabled {
		t.Fatalf("выключенный НЕ исчезает из ведомости (disabled — живая декларация): %+v", last)
	}
	// Live-снимок обновлён: воркер на следующем прогоне видит disabled.
	if on, ok := e.m.Enabled("wdtt-client:de"); !ok || on {
		t.Fatalf("Enabled() = %v %v", on, ok)
	}
}

func TestDeleteRunsTeardownBeforeStop(t *testing.T) {
	// Щ5: Worker.Stop терминален — прогона с disabled после него не будет
	// никогда; правила сервера снял бы только teardown-прогон ДО Stop.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	if len(e.waited) != 1 || e.waited[0] != "wdtt-client:de" {
		t.Fatalf("WaitDisabled не зван: %v", e.waited)
	}
	// Порядок: объявление с Enabled=false (teardown) случилось ДО пустого.
	n := len(e.reg.declared)
	if n < 2 {
		t.Fatalf("объявлений %d, ждали ≥2 (teardown + удаление)", n)
	}
	teardown := e.reg.declared[n-2]
	if len(teardown) != 1 || teardown[0].Enabled {
		t.Fatalf("teardown-ведомость: %+v (ждали выключенный выход)", teardown)
	}
	if last := e.reg.declared[n-1]; len(last) != 0 {
		t.Fatalf("после удаления ведомость обязана быть пустой: %+v", last)
	}
	if !e.instances["wdtt-client:de"].stopped {
		t.Fatal("инстанс не остановлен")
	}
}

func TestDeleteRemovesSweepsReleases(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	before := e.sw.count()
	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	st, _ := e.st.Load()
	if len(st.Records) != 0 {
		t.Fatal("запись осталась")
	}
	if e.sw.count() <= before {
		t.Fatal("уборка NDMS-имен после удаления обязана позваться")
	}
	if len(e.released) != 1 {
		t.Fatalf("ReleasePins: %v", e.released)
	}
}

func TestDeleteRevivesInstanceOnPersistFailure(t *testing.T) {
	// Замечание 6 ревью А: отказ записи после Stop оставлял бы запись без
	// инстанса до рестарта. Диск ломается ПОСЛЕ teardown-шага (иначе упал бы
	// уже SetEnabled) — хук waitHook срабатывает между teardown и удалением.
	if os.Geteuid() == 0 {
		t.Skip("под root каталог остаётся записываемым")
	}
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	e.waitHook = func() {
		if err := os.Chmod(e.dir, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	defer os.Chmod(e.dir, 0o755)
	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err == nil {
		t.Fatal("отказ записи обязан быть ошибкой Delete")
	}
	if e.factoryN["wdtt-client:de"] < 2 {
		t.Fatal("инстанс обязан быть воскрешён фабрикой")
	}
}

func TestDeclarationsSeeNormalizedRecords(t *testing.T) {
	// З1: объявление идёт после нормализации store — зеркальная запись не
	// должна получить необрезанный peer.
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	rec := rawRec("nz", "OpkgTun18", "opkgtun18")
	rec.WdttClient.Peer = "  5.5.5.5:6  "
	if err := e.m.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	last := e.reg.declared[len(e.reg.declared)-1]
	if len(last) != 1 || last[0].Peer != "5.5.5.5:6" {
		t.Fatalf("объявление до нормализации: %+v", last)
	}
}

func TestDeclarationsSeeNormalizedModeAndPins(t *testing.T) {
	// Достройка стража ред. 1 (мутант 18 выживал): свидетель предыдущего теста —
	// Peer, а его RawExit тримит САМ (roles/config.go:168), поэтому объявление
	// до нормализации по нему неотличимо. Свидетели, которых RawExit не чинит:
	// пробельный Mode (до нормализации это не «raw» — выход выпадает из
	// ведомости целиком) и пробельный пин (уезжает в зеркальную запись как
	// есть).
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	rec := rawRec("nz", "  OpkgTun18  ", "opkgtun18")
	rec.WdttClient.Mode = "  raw  "
	if err := e.m.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	last := e.reg.declared[len(e.reg.declared)-1]
	if len(last) != 1 || last[0].NDMSName != "OpkgTun18" {
		t.Fatalf("объявление до нормализации: %+v", last)
	}
}

func TestCreateReleasesPinsOnRefusal(t *testing.T) {
	// Н6: отказ реестра после выделения пинов не должен оставлять их в held.
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	e.reg.failSet = errors.New("отказ")
	rec := instancestore.Record{ID: "np", Kind: instancestore.KindWdttClient,
		Name: "N", Enabled: true,
		WdttClient: &roles.WdttClientConfig{Mode: "raw", Peer: "1.1.1.1:1",
			Password: "pw", VKHashes: "h"}}
	if err := e.m.Create(context.Background(), rec); err == nil {
		t.Fatal("ждали отказ")
	}
	if len(e.released) != 1 || e.released[0][0] != "wdtt-client:np" {
		t.Fatalf("свежие пины обязаны вернуться: %v", e.released)
	}
}

func TestMutationsRefusedBeforeBoot(t *testing.T) {
	e := newEnv(t)
	if err := e.m.Create(context.Background(), rawRec("de", "OpkgTun18", "opkgtun18")); err == nil {
		t.Fatal("мутации до боота отклоняются: ведомость до посева неполна по построению")
	}
}

func TestPostAllReachesEveryInstance(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), ftRec("ft"))
	boot(t, e)
	e.m.PostAll(proxyrt.EventWANUp)
	for k, fi := range e.instances {
		if len(fi.posts) == 0 {
			t.Fatalf("%s не получил будильник", k)
		}
	}
}

func TestDeclarationsUseValueConfigs(t *testing.T) {
	// Требования 16/19: ведомость из типизированных ЗНАЧЕНИЙ store.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	d := e.reg.declared[0][0]
	if d.InstanceID != "de" || d.Name != "Имя" || d.NDMSName != "OpkgTun18" || d.Peer != "1.2.3.4:5" {
		t.Fatalf("объявление собрано не из конфига store: %+v", d)
	}
}

func TestStopRunsOutsideManagerLock(t *testing.T) {
	// Щ6: фейковый Stop зовёт метод менеджера, берущий m.mu. Если Delete
	// держит лок на время Stop — взаимоблокировка, тест умирает таймаутом.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	e.instances["wdtt-client:de"].onStop = func() { _ = e.m.Records() }
	done := make(chan struct{})
	go func() {
		_ = e.m.Delete(context.Background(), "wdtt-client:de")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Delete держит m.mu на время Stop — дедлок Щ6")
	}
}

// --- Фикс-раунд 1 по ревью: I-1 (состав ведомостей), I-2 (ретрай запуска),
// I-3 (контракт AllocListen), I-4 (дисциплина флагов), F2 (перенос PostSeed).

func sameNames(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, n := range want {
		if !got[n] {
			return false
		}
	}
	return true
}

func TestPostSeedRunsEvenWhenDeclarationFails(t *testing.T) {
	// F2: посев уже лёг на диск, поэтому следующий боот увидит SeededNow=false.
	// Если уборочные шаги ждут конца боота, отказ объявления отменяет их
	// НАВСЕГДА — на железе это два живых поколения процессов.
	e := newEnv(t)
	e.reg.failSet = errors.New("дубликат id")
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("отказ объявления — отказ боота")
	}
	if len(e.postSeed) != 1 {
		t.Fatalf("уборочные шаги посева обязаны пройти до объявления: %v", e.postSeed)
	}
}

func TestBootSweepsExactlyDeclaredNDMSNames(t *testing.T) {
	// I-1: пустая ведомость — это снос ВСЕХ интерфейсов; факта вызова мало.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), ftRec("ft"))
	boot(t, e)
	if len(e.sw.calls) != 1 || !sameNames(e.sw.calls[0], "OpkgTun18") {
		t.Fatalf("ведомость уборщика на бооте: %v (ждали ровно {OpkgTun18})", e.sw.calls)
	}
}

// Амендмент F1: ведомость NDMS-имён собрана из того же списка, который
// сертификация признаёт неполным, — значит уборщик заперт тем же гейтом, что и
// уборка зеркальных записей. Обе половины гейта в одном тесте намеренно: ноль
// вызовов уборщика — это ещё и значение по умолчанию у свежего окружения, и
// отличить запертый гейт от неподключённого уборщика позволяет только
// соседний прогон, где уборка идёт.
func TestBootGatesNDMSSweepOnCertification(t *testing.T) {
	t.Run("пропущенный старый конфиг запирает уборку", func(t *testing.T) {
		e := newEnv(t)
		seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
		st, err := e.st.Load()
		if err != nil {
			t.Fatal(err)
		}
		st.SkippedSources = []instancestore.SkippedSource{{File: "wdtt.json", Reason: "поле не того типа"}}
		e.seedRes = instancestore.SeedResult{State: st}
		e.seedResSet = true
		boot(t, e)
		if len(e.sw.calls) != 0 {
			t.Fatalf("уборщик зван при незаверенном посеве: %v (интерфейсы непереехавших "+
				"инстансов ушли бы вместе с permit'ами политик)", e.sw.calls)
		}
		// Боот дошёл до конца: инстансы стартовали, признак боота стоит —
		// значит уборку пропустил гейт, а не обрыв где-то раньше.
		if len(e.instances) != 1 {
			t.Fatalf("инстансы обязаны стартовать: %d", len(e.instances))
		}
		if info := e.m.SeedInfo(); !info.Booted || info.Certified {
			t.Fatalf("SeedInfo: %+v (ждали Booted=true, Certified=false)", info)
		}
	})

	t.Run("отказ MarkSeeded запирает уборку", func(t *testing.T) {
		e := newEnv(t)
		e.reg.failMark = errors.New("призраки в каталоге")
		seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
		boot(t, e)
		if len(e.sw.calls) != 0 {
			t.Fatalf("уборщик зван при отказе сертификации: %v", e.sw.calls)
		}
	})

	t.Run("сертифицированный посев убирает", func(t *testing.T) {
		e := newEnv(t)
		seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
		boot(t, e)
		if info := e.m.SeedInfo(); !info.Certified {
			t.Fatalf("чистый боот обязан сертифицироваться: %+v", info)
		}
		if len(e.sw.calls) != 1 || !sameNames(e.sw.calls[0], "OpkgTun18") {
			t.Fatalf("ведомость уборщика: %v (ждали ровно {OpkgTun18}): гейт нельзя «починить» "+
				"запретом навсегда", e.sw.calls)
		}
	})
}

func TestPostSeedReceivesDeclaredNDMSNames(t *testing.T) {
	// I-1: nil в уборке наследия (задача 6) = чистка правил с ЖИВЫХ интерфейсов.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if len(e.postSeedNDMS) != 1 || !sameNames(e.postSeedNDMS[0], "OpkgTun18") {
		t.Fatalf("ведомость уборочных шагов: %v (ждали ровно {OpkgTun18})", e.postSeedNDMS)
	}
}

func TestDeleteSweepsSurvivorsNDMSNames(t *testing.T) {
	// I-1: после удаления одного инстанса ведомость обязана нести имена
	// ОСТАВШИХСЯ — пустая карта снесла бы интерфейс живого соседа.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), rawRec("dv", "OpkgTun19", "opkgtun19"))
	boot(t, e)
	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	last := e.sw.calls[len(e.sw.calls)-1]
	if !sameNames(last, "OpkgTun19") {
		t.Fatalf("ведомость уборщика после удаления: %v (ждали ровно {OpkgTun19})", last)
	}
}

// Амендмент F, вторая половина: на пути УДАЛЕНИЯ гейт был бы неверным
// лечением — сертификация монотонна, и запертая уборка оставила бы интерфейс
// только что удалённого инстанса сиротой навсегда. Поэтому при незаверенном
// посеве ведомость строится из скана минус имена удаляемого.
//
// OpkgTun20 в скане — интерфейс НЕПЕРЕЕХАВШЕГО инстанса: в записях store его
// нет и быть не может, и он же различает обе половины теста.
func TestDeleteSweepLedgerFollowsCertification(t *testing.T) {
	setup := func(t *testing.T, certified bool) *env {
		t.Helper()
		e := newEnv(t)
		e.sw.owned = []string{"OpkgTun18", "OpkgTun19", "OpkgTun20"}
		seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), rawRec("dv", "OpkgTun19", "opkgtun19"))
		if !certified {
			st, err := e.st.Load()
			if err != nil {
				t.Fatal(err)
			}
			st.SkippedSources = []instancestore.SkippedSource{{File: "wdtt.json", Reason: "поле не того типа"}}
			e.seedRes = instancestore.SeedResult{State: st}
			e.seedResSet = true
		}
		boot(t, e)
		if got := e.m.SeedInfo().Certified; got != certified {
			t.Fatalf("Certified = %v, ждали %v", got, certified)
		}
		return e
	}

	t.Run("незаверенный посев щадит непереехавших", func(t *testing.T) {
		e := setup(t, false)
		if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
			t.Fatal(err)
		}
		if len(e.sw.calls) != 1 {
			t.Fatalf("вызовов уборщика %d, ждали ровно один (на бооте уборка заперта)", len(e.sw.calls))
		}
		if !sameNames(e.sw.calls[0], "OpkgTun19", "OpkgTun20") {
			t.Fatalf("ведомость удаления: %v (ждали ровно {OpkgTun19 OpkgTun20}: приговорён только "+
				"интерфейс удаляемого, непереехавший OpkgTun20 неприкосновенен)", e.sw.calls[0])
		}
	})

	t.Run("отказ скана отменяет уборку", func(t *testing.T) {
		e := setup(t, false)
		e.sw.ownedErr = errors.New("rci down")
		if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
			t.Fatal(err)
		}
		if len(e.sw.calls) != 0 {
			t.Fatalf("уборка при несобранной ведомости: %v («не знаем» не равно «наш и лишний»)", e.sw.calls)
		}
	})

	t.Run("заверенный посев убирает сирот", func(t *testing.T) {
		e := setup(t, true)
		if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
			t.Fatal(err)
		}
		last := e.sw.calls[len(e.sw.calls)-1]
		if !sameNames(last, "OpkgTun19") {
			t.Fatalf("ведомость удаления: %v (ждали ровно {OpkgTun19}: список записей полон, и "+
				"сирота OpkgTun20 обязан быть подобран)", last)
		}
	})
}

func TestRepeatBootKeepsRunningInstances(t *testing.T) {
	// I-2: повторный Boot (ретрай посева зовут задачи 7 и 14) обязан оставить
	// живых в покое — иначе вторые воркеры на тех же ресурсах.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	first := e.instances["wdtt-client:de"]
	boot(t, e)
	if e.factoryN["wdtt-client:de"] != 1 {
		t.Fatalf("инстанс пересобран на повторном бооте: %d", e.factoryN["wdtt-client:de"])
	}
	if e.instances["wdtt-client:de"] != first {
		t.Fatal("живой инстанс подменён на повторном бооте")
	}
}

func TestBootFactoryFailureIsFatalAndVisible(t *testing.T) {
	// I-2 + I-4 (и F1): отказ сборки инстанса — отказ боота, причина видна
	// наружу, мутации остаются заперты.
	e := newEnv(t)
	e.factoryErr = errors.New("нет бинаря")
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("отказ фабрики — отказ боота")
	}
	info := e.m.SeedInfo()
	if info.Booted || info.Err == "" {
		t.Fatalf("SeedInfo после отказа фабрики: %+v (ждали Booted=false, Err непуст)", info)
	}
	if err := e.m.Create(context.Background(), ftRec("ft")); err == nil {
		t.Fatal("мутации после оборванного боота обязаны отклоняться")
	}
}

func TestBootDeclarationFailureKeepsSubsystemUnbooted(t *testing.T) {
	// I-4: booted=true в фатальной ветке пустил бы мутации на неполную ведомость.
	e := newEnv(t)
	e.reg.failSet = errors.New("дубликат id")
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	if err := e.m.Boot(context.Background()); err == nil {
		t.Fatal("отказ объявления — отказ боота")
	}
	if info := e.m.SeedInfo(); info.Booted {
		t.Fatalf("SeedInfo: %+v (ждали Booted=false)", info)
	}
	e.reg.failSet = nil
	if err := e.m.Create(context.Background(), ftRec("ft")); err == nil {
		t.Fatal("мутации после оборванного боота обязаны отклоняться")
	}
}

func TestListenAllocationUsesItsOwnOwnerKey(t *testing.T) {
	// I-3 (круг 2): свежая listen-аллокация обязана вернуться при отказе — но
	// под СВОИМ владельцем key+"/listen". Голый key освободил бы ВСЕ номера
	// владельца (alloc.go:78-86), то есть отобрал бы у живой записи её индекс
	// OpkgTun; отказ от учёта вовсе (прежняя редакция) требовал бы аллокатора
	// без резерва и терял уникальность порта между параллельными Create.
	e := newEnv(t)
	noListen := rawRec("de", "OpkgTun18", "opkgtun18")
	noListen.WdttClient.Listen = ""
	seedState(t, e, noListen)
	boot(t, e)
	e.reg.failSet = errors.New("отказ")
	if err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.Name = "Другое"
		return nil
	}); err == nil {
		t.Fatal("ждали отказ реестра")
	}
	if len(e.released) != 1 || len(e.released[0]) != 1 || e.released[0][0] != "wdtt-client:de/listen" {
		t.Fatalf("возврат listen-аллокации: %v (ждали ровно [wdtt-client:de/listen] — голый ключ отобрал бы живой OpkgTun)", e.released)
	}
}

func TestDeleteReleasesEveryOwnerKey(t *testing.T) {
	// Круг 2: у Delete записи под рукой уже нет, поэтому он возвращает всех
	// владельцев вслепую. Забытый key+"/listen" тёк бы до перезапуска — а
	// свидетеля на состав списка не было (прежний тест считал только вызовы).
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	want := []string{"wdtt-client:de", "wdtt-client:de/wg", "wdtt-client:de/raw", "wdtt-client:de/listen"}
	if len(e.released) != 1 || len(e.released[0]) != len(want) {
		t.Fatalf("владельцы на возврате: %v (ждали %v)", e.released, want)
	}
	for i, k := range want {
		if e.released[0][i] != k {
			t.Fatalf("владельцы на возврате: %v (ждали %v)", e.released, want)
		}
	}
}

// Правка записи — единственная точка, через которую проходят ВСЕ ручные
// правки конфига (PATCH любой из четырёх ролей, импорт ссылки, обновление
// подписки). Каждая из них могла устранить причину, по которой процесс не
// поднимался, и держать клиента в паузе повторного старта до пяти минут
// значит держать его ровно тогда, когда человек его чинит.
//
// Порядок обязателен: сброс ДО побудки. Период перепроверки считается ПОСЛЕ
// цикла реконсиляции (reconcile.go), и сброс, легший между отказом применения
// и этим подсчётом, даёт Recheck=0 — воркер получает пустой таймер, выбор по
// нему не срабатывает никогда, и инстанс стоит до следующего внешнего
// события, которого в отказавшем инстансе не будет.
func TestUpdateResetsStartBackoffBeforeWakeup(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	fi := e.instances["wdtt-client:de"]

	if err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.WdttClient.Password = "починил"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got := fi.callTail(2)
	want := []string{"reset", "post:intent-changed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("хвост обращений %v, ожидали %v", got, want)
	}
}

// Сброс висит на СОСТОЯВШЕЙСЯ записи: отказ правки — ровно тот случай, ради
// которого пауза существует, и снимать её там нечем и незачем.
func TestUpdateFailureDoesNotResetStartBackoff(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	fi := e.instances["wdtt-client:de"]

	err := e.m.Update(context.Background(), "wdtt-client:de", func(*instancestore.Record) error {
		return errors.New("правка отвергнута")
	})
	if err == nil {
		t.Fatal("отказ мутатора обязан доехать до вызывающего")
	}
	if n := fi.resetCount(); n != 0 {
		t.Fatalf("сбросов при непринятой правке %d, ожидали 0", n)
	}
}

// Сброс АДРЕСОВАН одному инстансу. Без этой пробы мутация «пройтись сбросом по
// всей карте» проходит зелёной по всему дереву: соседям пауза снимется молча, и
// клиент, падающий сам по себе, перестанет замедляться от чужой правки.
func TestUpdateResetsOnlyAddressedInstance(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), rawRec("fi", "OpkgTun19", "opkgtun19"))
	boot(t, e)

	if err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.WdttClient.Password = "починил"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := e.instances["wdtt-client:de"].resetCount(); n != 1 {
		t.Fatalf("адресат получил сбросов %d, ожидали 1", n)
	}
	if n := e.instances["wdtt-client:fi"].resetCount(); n != 0 {
		t.Fatalf("сосед получил сбросов %d, ожидали 0", n)
	}
}

// F5 ревью задачи 14: ретрай посева (задача 16) приводит второго вызывающего —
// хуки NDMS и wan-up могут выстрелить одновременно, а гейт !Booted живёт у
// вызывающего и от гонки не спасает. Шаги боота — посев, PostSeed,
// сертификация, объявление — на параллельный прогон не рассчитаны, поэтому
// Boot обязан сериализоваться сам с собой.
func TestBootIsSerializedWithItself(t *testing.T) {
	e := newEnv(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	e.seedHook = func() {
		entered <- struct{}{}
		<-release
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = e.m.Boot(context.Background()) }()
	<-entered // первый вошёл в посев и держит его

	wg.Add(1)
	go func() { defer wg.Done(); _ = e.m.Boot(context.Background()) }()

	select {
	case <-entered:
		t.Fatal("второй Boot вошёл в посев, пока первый его не закончил")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("второй Boot не дождался очереди")
	}
	// Дождаться обоих обязательно: горутина, пережившая тест, писала бы в уже
	// снесённый t.TempDir().
	wg.Wait()
}

func TestBootAnnouncesListenMove(t *testing.T) {
	// Амендмент G3: молча сменить порт нельзя — снаружи мог быть настроен
	// клиент на прежний. Оба адреса обязаны быть и в журнале, и в поверхности
	// статуса; строка пишется на КАЖДОМ бооте, потому что список приходит с
	// диска, а читают журнал и после перезапуска.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	st.MovedListen = []instancestore.ListenMove{{Instance: "freeturn-client:default",
		Name: "Клиент", From: "127.0.0.1:9000", To: "127.0.0.1:9002"}}
	e.seedRes = instancestore.SeedResult{State: st}
	e.seedResSet = true
	boot(t, e)

	var said string
	for _, m := range e.j.journalMsgs() {
		if strings.Contains(m, "freeturn-client:default") {
			said = m
		}
	}
	if said == "" {
		t.Fatalf("переезд обязан попасть в журнал: %v", e.j.journalMsgs())
	}
	for _, want := range []string{"127.0.0.1:9000", "127.0.0.1:9002"} {
		if !strings.Contains(said, want) {
			t.Fatalf("в строке обязаны быть ОБА адреса, нет %s: %q", want, said)
		}
	}
	if got := e.m.SeedInfo().MovedListen; !reflect.DeepEqual(got, st.MovedListen) {
		t.Fatalf("признак переезда обязан быть виден в поверхности статуса: %+v", got)
	}
}

func TestDeleteDropsMirrorOfExactlyTheRemovedInstance(t *testing.T) {
	// Хвост 1: id зеркальной записи менеджер считает сам, и ошибиться в нём
	// значит снести карточку соседа. У инстанса без выхода (freeturn) снимать
	// нечего — адресный снос не имеет права звучать вовсе.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), ftRec("ft"))
	boot(t, e)

	if err := e.m.Delete(context.Background(), "freeturn-client:ft"); err != nil {
		t.Fatal(err)
	}
	if len(e.reg.dropped) != 0 {
		t.Fatalf("у инстанса без выхода зеркальной записи нет: %v", e.reg.dropped)
	}

	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.reg.dropped, []string{"wdttraw-de"}) {
		t.Fatalf("адресно снято: %v (ждали ровно [wdttraw-de])", e.reg.dropped)
	}
}

func TestDeleteSurvivesMirrorDropFailure(t *testing.T) {
	// Требование 3 брифа: инстанс уже удалён, откатывать нечего — отказ сноса
	// это предупреждение, а не отказ Delete. Молчать при этом нельзя: без
	// строки карточку-призрак не найти.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	e.reg.failDrop = errors.New("диск")

	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatalf("отказ сноса зеркальной записи не имеет права отменять удаление: %v", err)
	}
	st, _ := e.st.Load()
	if len(st.Records) != 0 {
		t.Fatal("запись инстанса обязана быть удалена")
	}
	found := false
	for _, msg := range e.j.journalMsgs() {
		if strings.Contains(msg, "зеркальная запись не убрана") && strings.Contains(msg, "диск") {
			found = true
		}
	}
	if !found {
		t.Fatalf("причина обязана быть в журнале: %v", e.j.journalMsgs())
	}
}

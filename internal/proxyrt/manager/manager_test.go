package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
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
	postRefuses      bool // воркер остановлен: Post отдаёт false
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
	// Вердикт настраиваемый: у прод-воркера он false, когда воркер уже
	// остановлен, и менеджер обязан этот отказ ПРОНЕСТИ, а не подменить
	// своим «нашёл — значит ок» (RT50).
	return !f.postRefuses
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
	// listenTaken — подмена занятости: адрес → чем его заменит аллокатор.
	listenTaken map[string]string
	// factoryRecs — записи, С КОТОРЫМИ собирались воркеры: переезд порта на
	// бооте обязан доехать до сборки, иначе процесс сядет на занятый порт.
	factoryRecs map[string]instancestore.Record
	released    [][]string
	allocN      int
	allocKeys   []string

	ensureErr   error                  // отказ ворот бута (F98)
	ensureRecs  []instancestore.Record // список, дошедший до ворот
	ensureCalls int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{st: instancestore.New(dir), dir: dir, reg: &fakeRegistry{},
		sw: &fakeSweeper{}, j: &recJournal{},
		instances: map[string]*fakeInstance{}, factoryN: map[string]int{},
		factoryRecs: map[string]instancestore.Record{}}
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
			e.factoryRecs[rec.Key()] = rec
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
		EnsureBinaries: func(_ context.Context, recs []instancestore.Record, progress func(string)) error {
			e.ensureCalls++
			e.ensureRecs = recs
			if e.ensureErr != nil {
				progress("идёт загрузка")
				return e.ensureErr
			}
			return nil
		},
		AllocIndex: func(key string, pinned int, havePin bool) (int, error) {
			if havePin {
				return pinned, nil
			}
			e.allocN++
			// Ключ запоминаем, а индексы выдаём РАЗНЫЕ: у сервера две
			// половины, и общий индекс на обе — тот самый дефект, который
			// константа 30 скрывала бы от любого теста.
			e.allocKeys = append(e.allocKeys, key)
			return 30 + e.allocN - 1, nil
		},
		AllocListen: func(_ string, _ instancestore.Kind, _, current string) (string, error) {
			if next, taken := e.listenTaken[current]; taken {
				return next, nil
			}
			if current != "" {
				return current, nil
			}
			return "127.0.0.1:9007", nil
		},
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

// Занятый порт на бооте инстанс не убивает: посев разводит конфликты только
// между записями, а занятость шире (localhost-endpoint AWG-туннеля). Прежде
// ensurePins стоял лишь на путях Create/Update, и такой инстанс уходил в
// blocked до ручного стоп/старта.
func TestBootMovesInstanceOffTakenListen(t *testing.T) {
	e := newEnv(t)
	// Аллокатор говорит, что 9000 занят кем-то снаружи записей.
	e.listenTaken = map[string]string{"127.0.0.1:9000": "127.0.0.1:9042"}
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), ftRec("ft"))
	boot(t, e)

	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Records {
		p := instancestore.ClientListen(&r)
		if r.ID == "de" && *p != "127.0.0.1:9042" {
			t.Fatalf("переезд не записан на диск: %s", *p)
		}
		if r.ID == "ft" && *p != "127.0.0.1:9001" {
			t.Fatalf("годный порт тронут: %s", *p)
		}
	}
	// Молча сменить порт нельзя: переезд обязан быть виден человеку.
	if len(st.MovedListen) != 1 || st.MovedListen[0].From != "127.0.0.1:9000" ||
		st.MovedListen[0].To != "127.0.0.1:9042" {
		t.Fatalf("переезд не показан: %+v", st.MovedListen)
	}
	// Воркер обязан подняться уже с новым портом, иначе процесс сядет на занятый.
	built, ok := e.factoryRecs["wdtt-client:de"]
	if !ok {
		t.Fatal("инстанс не создан")
	}
	if got := *instancestore.ClientListen(&built); got != "127.0.0.1:9042" {
		t.Fatalf("воркер собран со старым портом: %s", got)
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
	// Именно WANUp, а не «хоть что-нибудь»: boot кладёт свой будильник
	// РАНЬШЕ, поэтому ассерт `len(posts) != 0` был истинным и с пустым телом
	// PostAll — мутация «ничего не рассылать» проходила зелёной.
	for k, fi := range e.instances {
		fi.mu.Lock()
		got := slices.Contains(fi.posts, proxyrt.EventWANUp)
		posts := slices.Clone(fi.posts)
		fi.mu.Unlock()
		if !got {
			t.Fatalf("%s не получил WANUp: %v", k, posts)
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

func TestUpdateDropsMirrorOnlyWhenExitDisappears(t *testing.T) {
	// Хвост 4: raw-клиент объявляет выход, wg-клиент — нет. После смены режима
	// выход пропадает из ведомости, а снять зеркальную запись могла бы только
	// массовая уборка — она заперта гейтом посева, и у ЖИВОГО инстанса карточка
	// туннеля висит до перезапуска процесса.
	//
	// Три случая в одном тесте: снос обязан идти ровно на исчезнувшем
	// объявлении. Сносить на каждой правке значит убивать карточку от
	// переименования, а на появлении выхода — сразу после его создания.
	wgRec := func(id string) instancestore.Record {
		r := rawRec(id, "OpkgTun18", "opkgtun18")
		r.WdttClient.Mode = "wg"
		return r
	}
	for _, tc := range []struct {
		name    string
		start   instancestore.Record
		mutate  func(*instancestore.Record) error
		dropped []string
	}{
		{
			name:    "raw→wg: выход исчез",
			start:   rawRec("de", "OpkgTun18", "opkgtun18"),
			mutate:  func(r *instancestore.Record) error { r.WdttClient.Mode = "wg"; return nil },
			dropped: []string{"wdttraw-de"},
		},
		{
			name:   "wg→raw: выход появился",
			start:  wgRec("de"),
			mutate: func(r *instancestore.Record) error { r.WdttClient.Mode = "raw"; return nil },
		},
		{
			name:   "правка не режима: выход на месте",
			start:  rawRec("de", "OpkgTun18", "opkgtun18"),
			mutate: func(r *instancestore.Record) error { r.Name = "Другое имя"; return nil },
		},
		{
			// PATCH кладёт connMode из тела запроса КАК ЕСТЬ
			// (api/proxy_instances.go:735), а к "raw" его приводит store на
			// записи — выход остаётся объявленным, и снимать нечего.
			name:   "ненормализованный режим: выход объявлен",
			start:  rawRec("de", "OpkgTun18", "opkgtun18"),
			mutate: func(r *instancestore.Record) error { r.WdttClient.Mode = "RAW"; return nil },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			seedState(t, e, tc.start)
			boot(t, e)

			if err := e.m.Update(context.Background(), "wdtt-client:de", tc.mutate); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e.reg.dropped, tc.dropped) {
				t.Fatalf("адресно снято: %v, ждали %v", e.reg.dropped, tc.dropped)
			}
			// Требование 2: снимается ЗЕРКАЛЬНАЯ ЗАПИСЬ, а не инстанс — он
			// продолжает работать в новом режиме.
			fi := e.instances["wdtt-client:de"]
			if fi == nil || fi.stopped {
				t.Fatalf("инстанс обязан продолжать работу: %+v", fi)
			}
			st, err := e.st.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Records) != 1 {
				t.Fatalf("запись инстанса обязана остаться: %+v", st.Records)
			}
		})
	}
}

func TestUpdateSurvivesMirrorDropFailure(t *testing.T) {
	// Требование 3 брифа: конфиг уже записан, откатывать нечего — отказ сноса
	// это предупреждение с причиной, а не отказ правки. Молчать нельзя: без
	// строки карточку-призрак не найти.
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	e.reg.failDrop = errors.New("диск")

	err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.WdttClient.Mode = "wg"
		return nil
	})
	if err != nil {
		t.Fatalf("отказ сноса зеркальной записи не имеет права отменять правку: %v", err)
	}
	st, lerr := e.st.Load()
	if lerr != nil {
		t.Fatal(lerr)
	}
	if st.Records[0].WdttClient.Mode != "wg" {
		t.Fatalf("правка обязана быть записана: %+v", st.Records[0].WdttClient)
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

// Уведомление о переезде listen-порта не должно переживать своего инстанса:
// на стенде плашка рассказывала про адрес freeturn-client:default, которого в
// файле уже не было, и снять её было нечем.
func TestDeleteDropsListenMoveOfRemovedInstance(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.MovedListen = []instancestore.ListenMove{
			{Instance: "wdtt-client:de", Name: "Удаляемый", From: "127.0.0.1:9000", To: "127.0.0.1:9001"},
			{Instance: "freeturn-client:default", Name: "Соседний", From: "127.0.0.1:9002", To: "127.0.0.1:9003"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	boot(t, e)

	if err := e.m.Delete(context.Background(), "wdtt-client:de"); err != nil {
		t.Fatal(err)
	}

	st, _ := e.st.Load()
	if len(st.MovedListen) != 1 {
		t.Fatalf("на диске остались переезды: %+v", st.MovedListen)
	}
	// Чужой переезд не трогаем: он про живой инстанс, и его владелец сообщение
	// ещё не читал.
	if st.MovedListen[0].Instance != "freeturn-client:default" {
		t.Errorf("снят переезд не того инстанса: %+v", st.MovedListen[0])
	}
	for _, mv := range e.m.SeedInfo().MovedListen {
		if mv.Instance == "wdtt-client:de" {
			t.Error("удалённый инстанс всё ещё в выдаче переездов")
		}
	}
}

// Признание снимает переезды и с диска, и из кэша менеджера: иначе плашка
// вернулась бы на следующем опросе, до перезапуска демона.
func TestAckListenMovesClearsDiskAndCache(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	if _, err := e.st.Replace(func(st *instancestore.State) error {
		st.MovedListen = []instancestore.ListenMove{
			{Instance: "wdtt-client:de", Name: "Клиент", From: "127.0.0.1:9000", To: "127.0.0.1:9001"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	boot(t, e)
	if len(e.m.SeedInfo().MovedListen) != 1 {
		t.Fatal("переезд не доехал до SeedInfo — тест не о том")
	}

	if err := e.m.AckListenMoves(); err != nil {
		t.Fatal(err)
	}

	if st, _ := e.st.Load(); len(st.MovedListen) != 0 {
		t.Errorf("на диске остались переезды: %+v", st.MovedListen)
	}
	if got := e.m.SeedInfo().MovedListen; len(got) != 0 {
		t.Errorf("в кэше менеджера остались переезды: %+v", got)
	}
}

// PF16: переезд listen-порта на пути Update виден тем же каналом, что и на
// бооте. Прежде боот писал MovedListen, а правка меняла порт молча — при том
// что снаружи так же мог быть настроен клиент на прежний адрес.
func TestUpdateRecordsListenMove(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18")) // listen 127.0.0.1:9000
	boot(t, e)
	// Занятость появляется ПОСЛЕ боота: иначе порт переехал бы уже там.
	e.listenTaken = map[string]string{"127.0.0.1:9000": "127.0.0.1:9042"}

	if err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.Name = "Другое"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MovedListen) != 1 {
		t.Fatalf("переезд правки не записан: %+v", st.MovedListen)
	}
	mv := st.MovedListen[0]
	if mv.Instance != "wdtt-client:de" || mv.From != "127.0.0.1:9000" || mv.To != "127.0.0.1:9042" {
		t.Fatalf("переезд назван неверно: %+v", mv)
	}
	if mv.Name != "Другое" {
		t.Fatalf("имя инстанса взято не из правки: %+v", mv)
	}
	// Плашка читает снимок в памяти, а не диск: без синхронизации переезд
	// появился бы только после перезапуска демона.
	if got := e.m.SeedInfo().MovedListen; len(got) != 1 || got[0].To != "127.0.0.1:9042" {
		t.Fatalf("снимок в памяти не обновлён: %+v", got)
	}
}

// Правка, не сдвинувшая порт, молчит: иначе плашка всплывала бы на каждом
// сохранении карточки.
func TestUpdateWithoutListenMoveIsQuiet(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.Name = "Другое"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MovedListen) != 0 {
		t.Fatalf("переезда не было, а запись есть: %+v", st.MovedListen)
	}
}

// Ревью ветки: осознанная смена порта через API — НЕ переезд. Переездом
// считается только отказ аллокатора дать желаемое; иначе плашка «порт был
// занят другой записью» врала бы тому, кто порт сам и поменял.
func TestUpdateDeliberateListenChangeIsNotAMove(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18")) // listen 127.0.0.1:9000
	boot(t, e)

	if err := e.m.Update(context.Background(), "wdtt-client:de", func(r *instancestore.Record) error {
		r.WdttClient.Listen = "127.0.0.1:9500" // свободный порт, аллокатор отдаст как есть
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MovedListen) != 0 {
		t.Fatalf("осознанная смена порта объявлена переездом: %+v", st.MovedListen)
	}
	for _, r := range st.Records {
		if p := instancestore.ClientListen(&r); p != nil && *p != "127.0.0.1:9500" {
			t.Fatalf("порт не сохранён: %s", *p)
		}
	}
}

// Ревью 2: канал уведомления о переезде обязан быть один на ВСЕ пути, включая
// создание. listen на создании принимает и прямой API-запрос, и подмена
// заданного порта молчала бы, хотя снаружи клиент мог быть настроен на него.
func TestCreateRecordsListenMove(t *testing.T) {
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)
	e.listenTaken = map[string]string{"127.0.0.1:9000": "127.0.0.1:9042"}

	rec := rawRec("de", "OpkgTun18", "opkgtun18") // listen 127.0.0.1:9000
	if err := e.m.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}

	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MovedListen) != 1 {
		t.Fatalf("подмена заданного порта на создании молчит: %+v", st.MovedListen)
	}
	mv := st.MovedListen[0]
	if mv.From != "127.0.0.1:9000" || mv.To != "127.0.0.1:9042" {
		t.Fatalf("переезд назван неверно: %+v", mv)
	}
	if got := e.m.SeedInfo().MovedListen; len(got) != 1 {
		t.Fatalf("снимок в памяти не обновлён: %+v", got)
	}
}

// Создание БЕЗ заданного listen переездом не считается: желаемого порта не
// было, значит аллокатор ничего не отвергал.
func TestCreateWithoutListenIsQuiet(t *testing.T) {
	e := newEnv(t)
	seedState(t, e)
	boot(t, e)

	rec := rawRec("de", "OpkgTun18", "opkgtun18")
	rec.WdttClient.Listen = ""
	if err := e.m.Create(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.MovedListen) != 0 {
		t.Fatalf("выдача порта на пустом месте объявлена переездом: %+v", st.MovedListen)
	}
}

// Ревью 4: на бооте ЗАПИСЬ выделенного порта и УВЕДОМЛЕНИЕ о переезде —
// разные решения. Запись с пустым listen (посев копирует его из старого
// конфига вербатим) обязана получить порт и сохранить его: без этого ресурс
// listen валит инстанс на каждом бооте. Уведомлять при этом не о чем —
// выдача порта на пустом месте переездом не является.
func TestBootFillsEmptyListenWithoutAnnouncingMove(t *testing.T) {
	e := newEnv(t)
	rec := rawRec("de", "OpkgTun18", "opkgtun18")
	rec.WdttClient.Listen = ""
	seedState(t, e, rec)
	boot(t, e)

	st, err := e.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Records {
		p := instancestore.ClientListen(&r)
		if p == nil || *p == "" {
			t.Fatalf("пустой listen не вылечен на бооте: %+v", r)
		}
	}
	if len(st.MovedListen) != 0 {
		t.Fatalf("выдача порта на пустом месте объявлена переездом: %+v", st.MovedListen)
	}
	// Воркер обязан подняться уже с выданным портом, а не с пустым.
	built, ok := e.factoryRecs["wdtt-client:de"]
	if !ok {
		t.Fatal("инстанс не создан")
	}
	if got := *instancestore.ClientListen(&built); got == "" {
		t.Fatal("воркер собран с пустым listen")
	}
}

// RT4: намерение воркера читается из ЖИВОЙ записи, и Enabled отображается в
// него прямо, а не наоборот.
//
// `Live.Intent` — единственный источник намерения для всего рантайма проксей:
// по нему роль решает, поднимать процесс или снимать. Инверсия отображения
// («выключенные бегут, включённые гаснут») проходила по всему дереву тестов
// незамеченной — проверено мутацией.
//
// Вторая половина пина — про СВЕЖЕСТЬ: замыкание обязано читать запись в
// момент вопроса, а не снимок времени сборки инстанса (докстрока Factory).
func TestLiveIntentFollowsEnabledAndStaysFresh(t *testing.T) {
	rec := rawRec("de", "OpkgTun18", "opkgtun18")
	rec.Enabled = true
	live := newLive(rec)

	if got := live.Intent(); got != proxyrt.IntentEnabled {
		t.Fatalf("включённая запись даёт намерение %v, ждали IntentEnabled", got)
	}

	off := rec
	off.Enabled = false
	live.rec.Store(&off)
	if got := live.Intent(); got != proxyrt.IntentDisabled {
		t.Fatalf("после выключения намерение %v, ждали IntentDisabled", got)
	}
	// И обратно: снимок времени сборки дал бы здесь застрявшее значение.
	live.rec.Store(&rec)
	if got := live.Intent(); got != proxyrt.IntentEnabled {
		t.Fatalf("намерение не следует за записью: %v", got)
	}
}

// RT5: адресное событие доходит ДО инстанса, а не теряется в менеджере.
//
// Через `Manager.Post` идут, в частности, события смерти процесса от связи:
// потерянные, они означают, что упавший инстанс никто не поднимет. Мутация
// «всегда возвращать false, никому не доставляя» была зелёной.
func TestManagerPostReachesInstance(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)

	fi := e.instances["wdtt-client:de"]
	fi.mu.Lock()
	before := len(fi.posts)
	fi.mu.Unlock()

	if ok := e.m.Post("wdtt-client:de", proxyrt.EventProcessDied); !ok {
		t.Fatal("доставка живому инстансу обязана подтверждаться")
	}
	fi.mu.Lock()
	got := slices.Clone(fi.posts[before:])
	fi.mu.Unlock()
	if len(got) != 1 || got[0] != proxyrt.EventProcessDied {
		t.Fatalf("инстанс получил %v, ждали ровно [EventProcessDied]", got)
	}

	// Неизвестный ключ — честное «не доставлено», а не молчаливое «ок».
	if ok := e.m.Post("wdtt-client:нет-такого", proxyrt.EventProcessDied); ok {
		t.Fatal("доставка несуществующему инстансу не может быть успешной")
	}
}

// RT22: Records — единственный источник списка инстансов для API (карточки
// прокси, деталь связи, статус captcha). Мутация «всегда пустой срез»
// проходила зелёной: список никто не проверял, а его пропажа в UI выглядит
// как «инстансы удалились сами».
//
// Ключи сверяем составом, а не длиной: срез правильной длины из одного и того
// же инстанса — ровно тот дефект, который длина не различает.
func TestRecordsListsEveryLiveInstance(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"), ftRec("ft"))
	boot(t, e)

	got := map[string]instancestore.Kind{}
	for _, r := range e.m.Records() {
		got[r.ID] = r.Kind
	}
	if len(got) != 2 || got["de"] != instancestore.KindWdttClient || got["ft"] != instancestore.KindFreeTurnClient {
		t.Fatalf("ведомость собрана не из живых инстансов: %+v", got)
	}
}

// RT23: сервер, созданный БЕЗ пинов, обязан получить обе половины.
//
// В тестах пакета серверы приходили только с готовыми именами интерфейсов,
// поэтому выпил raw-ветки `ensurePins` проходил зелёным — а без него
// создание сервера с пустыми полями упирается в невнятный отказ
// `validateState` вместо выделения интерфейса.
func TestEnsurePins_ServerAllocatesBothHalves(t *testing.T) {
	e := newEnv(t)
	boot(t, e)
	if err := e.m.Create(context.Background(), instancestore.Record{
		ID: "srv", Kind: instancestore.KindWdttServer, Name: "S", Enabled: true,
		WdttServer: &roles.WdttServerConfig{Listen: "0.0.0.0:56000", ConfigDir: t.TempDir()},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var saved *roles.WdttServerConfig
	for _, r := range e.m.Records() {
		if r.ID == "srv" {
			saved = r.WdttServer
		}
	}
	if saved == nil {
		t.Fatal("сервер не заведён")
	}
	if saved.NdmsIface == "" || saved.WgIface == "" {
		t.Fatalf("wg-половина без интерфейса: %+v", saved)
	}
	if saved.RawNdmsIface == "" || saved.RawIface == "" {
		t.Fatalf("raw-половина без интерфейса: %+v", saved)
	}
	if saved.NdmsIface == saved.RawNdmsIface {
		t.Fatalf("половины делят один интерфейс %q", saved.NdmsIface)
	}
	if !slices.Contains(e.allocKeys, "wdtt-server:srv/wg") || !slices.Contains(e.allocKeys, "wdtt-server:srv/raw") {
		t.Fatalf("пины выделены не на обе половины: %v", e.allocKeys)
	}
}

// RT50: Manager.Post обязан вернуть вердикт ИНСТАНСА, а не факт «нашёл ключ».
// По этому bool ручка apply (api/proxy_instances.go) отличает живой инстанс от
// мёртвого и отвечает 404 вместо молчаливого «ок». У остановленного воркера
// прод-Post отдаёт false — значит менеджеру нельзя подменять его на true.
// RT5 пинует доставку и отказ на неизвестном ключе, но не вердикт: мутант
// «доставить и вернуть true безусловно» его переживал.
func TestManagerPostCarriesInstanceVerdict(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)

	fi := e.instances["wdtt-client:de"]
	fi.mu.Lock()
	fi.postRefuses = true
	before := len(fi.posts)
	fi.mu.Unlock()

	if ok := e.m.Post("wdtt-client:de", proxyrt.EventIntentChanged); ok {
		t.Error("отказ инстанса подменён успехом: ручка ответит «ок» мёртвому инстансу")
	}
	// И доставка при этом всё равно состоялась — отказ приходит ОТ инстанса,
	// а не от того, что менеджер решил не отправлять.
	fi.mu.Lock()
	got := slices.Clone(fi.posts[before:])
	fi.mu.Unlock()
	if len(got) != 1 || got[0] != proxyrt.EventIntentChanged {
		t.Fatalf("инстанс получил %v, ждали ровно [EventIntentChanged]", got)
	}
}

// F98: пока бинари не совпали с пином, старое поколение живёт — PostSeed
// (добивание, legacyCleanup) и сборка воркеров не выполняются, а причина
// видна в SeedInfo. Следующий Boot после появления бинарей проходит целиком.
func TestBootBinariesPendingKeepsOldGenerationAlive(t *testing.T) {
	e := newEnv(t)
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	e.ensureErr = fmt.Errorf("%w: wdtt: зеркало недоступно", ErrBinariesPending)
	// Ворота стоят ДО reconcileBootListen: занятый порт не должен двигать
	// запись, пока бут отложен (ревью В5).
	e.listenTaken = map[string]string{"127.0.0.1:9000": "127.0.0.1:9007"}

	err := e.m.Boot(context.Background())
	if !errors.Is(err, ErrBinariesPending) {
		t.Fatalf("бут обязан вернуть ErrBinariesPending: %v", err)
	}
	if len(e.postSeed) != 0 {
		t.Fatal("PostSeed вызван без бинарей — старое поколение добито")
	}
	if len(e.instances) != 0 || len(e.reg.calls) != 0 {
		t.Fatalf("воркеры/реестр тронуты без бинарей: %d/%v", len(e.instances), e.reg.calls)
	}
	if info := e.m.SeedInfo(); info.Booted || !strings.Contains(info.Err, "зеркало недоступно") {
		t.Fatalf("ожидание бинарей обязано быть видно: %+v", info)
	}
	if len(e.ensureRecs) != 1 || e.ensureRecs[0].ID != "de" {
		t.Fatalf("ворота получили не тот список: %+v", e.ensureRecs)
	}
	if info := e.m.SeedInfo(); len(info.MovedListen) != 0 {
		t.Fatalf("listen переехал до появления бинарей: %+v", info.MovedListen)
	}
	if st, err := e.st.Load(); err != nil || st.Records[0].WdttClient.Listen != "127.0.0.1:9000" {
		t.Fatalf("запись на диске тронута в ожидании бинарей: %v %+v", err, st.Records)
	}

	e.ensureErr = nil
	boot(t, e)
	if len(e.postSeed) != 1 || len(e.instances) != 1 {
		t.Fatalf("после появления бинарей бут обязан пройти целиком: postSeed=%d inst=%d", len(e.postSeed), len(e.instances))
	}
	if info := e.m.SeedInfo(); !info.Booted || info.Err != "" {
		t.Fatalf("после успешного бута ошибка обязана быть снята: %+v", info)
	}
	if e.ensureCalls != 2 {
		t.Fatalf("ворота зовутся на каждом бооте: %d", e.ensureCalls)
	}
}

// nil-хук — прежнее поведение (тесты без проводки).
func TestBootWithoutBinariesHookBootsAsBefore(t *testing.T) {
	e := newEnv(t)
	e.m.deps.EnsureBinaries = nil
	seedState(t, e, rawRec("de", "OpkgTun18", "opkgtun18"))
	boot(t, e)
	if len(e.instances) != 1 {
		t.Fatal("без хука бут обязан идти как раньше")
	}
}

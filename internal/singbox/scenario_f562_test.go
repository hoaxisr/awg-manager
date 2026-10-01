package singbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// refusingPoster — NDMS, отказывающий в `no interface X` (снос), пока refuse;
// остальное — оракул.
// lostReplyOn — payload с этой подстрокой применяется оракулом, а ответ
// теряется (транспортная ошибка): частичное применение (F577 R2).
type refusingPoster struct {
	*query.FakeNDMS
	mu          sync.Mutex
	refuse      bool
	refused     int
	lostReplyOn string
}

func (p *refusingPoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	b, _ := json.Marshal(payload)
	if p.lostReplyOn != "" && strings.Contains(string(b), p.lostReplyOn) {
		_, _ = p.FakeNDMS.Post(ctx, payload)
		return nil, errors.New("reply lost")
	}
	p.mu.Lock()
	if p.refuse && strings.Contains(string(b), `"no":true`) {
		p.refused++
		p.mu.Unlock()
		return nil, errors.New("NDMS refused")
	}
	p.mu.Unlock()
	return p.FakeNDMS.Post(ctx, payload)
}

func (p *refusingPoster) attempts() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refused
}

// f562Stand — оператор с туннелями A (Proxy3) и B (Proxy4) в конфиге и
// оракулом NDMS: Proxy3/Proxy4 наши, Proxy5 и Proxy6 — чужие (description
// пользователя и пустой), Proxy0 — пользовательский с description "A", как
// тег туннеля A. Конфиг пишется через фейк, оракул ставится после — в
// счётчики оракула попадает только проверяемый путь. sing-box не запущен, и
// Reconcile в тике выходит сразу (ручной стоп): уборка от него не зависит.
func f562Stand(t *testing.T) (*Operator, *Watchdog, *query.FakeNDMS, *refusingPoster) {
	t.Helper()
	op, _ := newOrchedOperator(t)
	op.proxyMgr = &fakeProxies{}
	ctx := context.Background()
	cfg := NewConfig()
	if err := cfg.AddTunnelWithListenPort("A", "vless", "a.example", 443, 1083, json.RawMessage(`{"type":"vless","tag":"A"}`)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddTunnelWithListenPort("B", "vless", "b.example", 443, 1084, json.RawMessage(`{"type":"vless","tag":"B"}`)); err != nil {
		t.Fatal(err)
	}
	if err := op.ApplyConfig(ctx, cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	f := query.NewFakeNDMS(
		ndms.Interface{ID: "Proxy0", Type: "Proxy", Description: "A", State: "up"},
		ndms.Interface{ID: "Proxy3", Type: "Proxy", Description: "A", State: "up"},
		ndms.Interface{ID: "Proxy4", Type: "Proxy", Description: "B", State: "up"},
		ndms.Interface{ID: "Proxy5", Type: "Proxy", Description: "user", State: "up"},
		ndms.Interface{ID: "Proxy6", Type: "Proxy", State: "up"},
	)
	p := &refusingPoster{FakeNDMS: f}
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	c := command.NewCommands(command.Deps{
		Poster:  p,
		Queries: q,
		Save:    command.NewSaveCoordinator(p, nil, time.Hour, time.Hour, 0, q.RunningConfig),
		IsOS5:   func() bool { return true },
	})
	pm := NewProxyManager(q, c)
	pm.marks = op
	op.proxyMgr = pm
	op.manuallyStopped.Store(true)
	w := NewWatchdog(op, nil, nil)
	w.swept.Store(true)
	return op, w, f, p
}

func mustHave(t *testing.T, f *query.FakeNDMS, names ...string) {
	t.Helper()
	for _, n := range names {
		if !f.Has(n) {
			t.Fatalf("%s снят: posts=%v", n, f.Posts)
		}
	}
}

func clean(t *testing.T, f *query.FakeNDMS) {
	t.Helper()
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// F562: список при RemoveTunnel не прочитан — ни одной команды, метка
// (Proxy3, "A"); тик снимает ровно Proxy3. Пользовательский Proxy0 с тем же
// description "A" цел (ревью F1), живой Proxy4 и чужие тоже; следующий тик
// без меток ничего не читает.
func TestScenario_RemoveTunnel_ListErrorDeferredThenSwept(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	ctx := context.Background()

	f.FailList(errors.New("rci down"))
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	if len(f.Posts) != 0 || !f.Has("Proxy3") {
		t.Fatalf("при непрочитанном списке: posts=%v", f.Posts)
	}
	f.FailList(nil)

	w.tick(ctx)
	if f.Has("Proxy3") {
		t.Fatalf("Proxy3 не снят тиком: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy4", "Proxy5", "Proxy6")
	for _, p := range f.Posts {
		if !strings.Contains(p, "Proxy3") {
			t.Fatalf("команда не по Proxy3: %s", p)
		}
	}
	clean(t, f)

	lists := f.ListCalls()
	w.tick(ctx)
	if got := f.ListCalls() - lists; got != 0 {
		t.Fatalf("тик после уборки прочитал список %d раз, want 0", got)
	}
}

// Ревью F1: description записи под именем метки сменился — запись уже не
// наша; метка снимается без команд.
func TestScenario_DeferredMark_DescriptionChanged_Untouched(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.deferProxyRemoval("Proxy5", "old-tag")
	w.tick(context.Background())
	mustHave(t, f, "Proxy5")
	if len(f.Posts) != 0 || len(op.deferredProxies) != 0 {
		t.Fatalf("posts=%v метки=%v", f.Posts, op.deferredProxies)
	}
}

// Без меток и флага тик не читает список (R36, ревью F6).
func TestScenario_WatchdogTick_NoDeferredNoList(t *testing.T) {
	_, w, f, _ := f562Stand(t)
	w.tick(context.Background())
	if n := f.ListCalls(); n != 0 || len(f.Posts) != 0 {
		t.Fatalf("списков=%d posts=%v, want 0/пусто", n, f.Posts)
	}
}

// Имя метки нужно живому туннелю (B → Proxy4) — метка снимается без списка
// и команд (ревью F3: проверка нужности прямо перед сносом).
func TestScenario_DeferredNameInUse_NoCommands(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.deferProxyRemoval("Proxy4", "B")
	w.tick(context.Background())
	if n := f.ListCalls(); n != 0 || len(f.Posts) != 0 || !f.Has("Proxy4") {
		t.Fatalf("списков=%d posts=%v", n, f.Posts)
	}
	if len(op.deferredProxies) != 0 {
		t.Fatalf("метка осталась: %v", op.deferredProxies)
	}
}

// Ревью F3: тик ждёт migrationMu (его держат AddTunnels/RemoveTunnel/
// Migrate* через RCI) и до его освобождения ничего не читает и не сносит.
func TestScenario_DeferredRemoval_WaitsMigrationMu(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false } // Proxy3 не нужен туннелю
	op.deferProxyRemoval("Proxy3", "A")
	op.migrationMu.Lock()
	done := make(chan struct{})
	go func() { w.tick(context.Background()); close(done) }()
	time.Sleep(50 * time.Millisecond)
	if n := f.ListCalls(); n != 0 || !f.Has("Proxy3") {
		op.migrationMu.Unlock()
		<-done
		t.Fatalf("под чужим migrationMu: списков=%d posts=%v", n, f.Posts)
	}
	op.migrationMu.Unlock()
	<-done
	if f.Has("Proxy3") {
		t.Fatalf("после освобождения Proxy3 не снят: posts=%v", f.Posts)
	}
}

// Уборка не удалась (список снова не прочитан) — метка остаётся, повтор —
// по выдержке, на тике, где список читается.
func TestScenario_DeferredSweepListError_KeepsMark(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	ctx := context.Background()
	clock := time.Unix(0, 0)
	op.deferredNow = func() time.Time { return clock }
	f.FailList(errors.New("rci down"))
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	w.tick(ctx)
	if len(f.Posts) != 0 || op.deferredProxies["Proxy3"] == nil {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	f.FailList(nil)
	w.tick(ctx) // выдержка 30 с ещё не вышла — ни списка, ни команд
	if len(f.Posts) != 0 || op.deferredProxies["Proxy3"] == nil {
		t.Fatalf("до выдержки: posts=%v", f.Posts)
	}
	clock = clock.Add(30 * time.Second)
	w.tick(ctx)
	if f.Has("Proxy3") || len(op.deferredProxies) != 0 {
		t.Fatalf("Proxy3 не снят: posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	clean(t, f)
}

// Ревью F2: NDMS отказывает в сносе — `no interface` на ступенях выдержки
// 30 с, 1, 2, 4, 8 мин, дальше раз в 15 мин, а не на каждом 30-секундном
// тике.
func TestScenario_DeferredRemoval_RefusedDeleteBackoff(t *testing.T) {
	op, w, _, p := f562Stand(t)
	p.refuse = true
	clock := time.Unix(0, 0)
	op.deferredNow = func() time.Time { return clock }
	op.deferProxyRemoval("Proxy3", "A")
	if err := op.ApplyConfig(context.Background(), mustConfigWithout(t, op, "A")); err != nil {
		t.Fatal(err)
	}

	var at []int
	for sec := 30; sec <= 3600; sec += 30 {
		clock = time.Unix(int64(sec), 0)
		before := p.attempts()
		w.tick(context.Background())
		if p.attempts() != before {
			at = append(at, sec)
		}
	}
	want := []int{30, 60, 120, 240, 480, 960, 1860, 2760}
	if !slices.Equal(at, want) {
		t.Fatalf("сносы на секундах %v, want %v", at, want)
	}
}

// Ревью F2: MigrateOff не сбрасывает выдержку — по имени, уже ждущему
// снос, прямой попытки нет.
func TestScenario_MigrateOff_KeepsBackoffOfMarked(t *testing.T) {
	op, w, _, p := f562Stand(t)
	p.refuse = true
	op.ndmsProxyEnabledFn = func() bool { return false } // как после MigrateOff
	clock := time.Unix(0, 0)
	op.deferredNow = func() time.Time { return clock }
	op.deferProxyRemoval("Proxy3", "A")
	clock = clock.Add(30 * time.Second)
	w.tick(context.Background()) // попытка 1, выдержка 30 с
	if p.attempts() != 1 {
		t.Fatalf("попыток=%d, want 1", p.attempts())
	}
	m := NewMigrator(op, &fakeToggler{events: new([]string)}, nil)
	if err := m.MigrateOff(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Proxy4 (B) — свой прямой снос MigrateOff (отказ), Proxy3 — без попытки.
	if p.attempts() != 2 {
		t.Fatalf("попыток после MigrateOff=%d, want 2 (только Proxy4)", p.attempts())
	}
}

// Удаление подписки: снос ProxyN не прошёл (список не прочитан) — метка
// (Proxy7, Label); строка подписки ушла, тик снимает Proxy7, остальные целы.
func TestScenario_SubscriptionRemoveFailure_DeferredThenSwept(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	ctx := context.Background()
	f.Add(ndms.Interface{ID: "Proxy7", Type: "Proxy", Description: "sub1", State: "up"})
	_ = f.DrainHooks()
	subs := &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}}}
	op.subProxies = subs
	reg := op.SubscriptionProxyRegistrar(op.proxyMgr.(*ProxyManager))

	f.FailList(errors.New("rci down"))
	if err := reg.RemoveProxy(ctx, 7, "sub1"); err == nil {
		t.Fatal("RemoveProxy при непрочитанном списке без ошибки")
	}
	if len(f.Posts) != 0 || op.deferredProxies["Proxy7"] == nil || op.deferredProxies["Proxy7"].desc != "sub1" {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	subs.list = nil // сервис удалил строку подписки
	f.FailList(nil)

	w.tick(ctx)
	if f.Has("Proxy7") || len(op.deferredProxies) != 0 {
		t.Fatalf("Proxy7 не снят: posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	mustHave(t, f, "Proxy0", "Proxy3", "Proxy4", "Proxy5", "Proxy6")
	clean(t, f)
}

// Режим NDMS Proxy выключен: имя живого туннеля не защищает — наших ProxyN
// в этом режиме быть не должно.
func TestScenario_DeferredRemoval_ModeOffSweepsLiveName(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.deferProxyRemoval("Proxy3", "A")
	w.tick(context.Background())
	if f.Has("Proxy3") {
		t.Fatalf("posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy4", "Proxy5", "Proxy6")
	clean(t, f)
}

// MigrateOff при непрочитанном списке: снос ProxyN туннелей и подписки
// отложен, первый тик (режим выключен) снимает все три; чужие и
// пользовательский Proxy0 с description "A" целы.
func TestScenario_MigrateOffListError_DeferredThenSwept(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	ctx := context.Background()
	f.Add(ndms.Interface{ID: "Proxy7", Type: "Proxy", Description: "sub1", State: "up"})
	_ = f.DrainHooks()
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}}}
	m := NewMigrator(op, &fakeToggler{events: new([]string)}, nil)
	op.ndmsProxyEnabledFn = func() bool { return false }

	f.FailList(errors.New("rci down"))
	if err := m.MigrateOff(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 || len(op.deferredProxies) != 3 {
		t.Fatalf("posts=%v метки=%v", f.Posts, op.deferredProxies)
	}
	op.needsOrphanCleanup.Store(false) // флаг — свой тест
	f.FailList(nil)

	w.tick(ctx)
	for _, n := range []string{"Proxy3", "Proxy4", "Proxy7"} {
		if f.Has(n) {
			t.Fatalf("%s не снят: posts=%v", n, f.Posts)
		}
	}
	mustHave(t, f, "Proxy0", "Proxy5", "Proxy6")
	clean(t, f)
}

// Ревью F5: метка того же имени, поставленная, пока попытка сноса в полёте,
// не теряется её успехом.
func TestScenario_DeferredMark_ReMarkDuringRetryKept(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.deferProxyRemoval("Proxy3", "A")
	f.InList(func() { op.deferProxyRemoval("Proxy3", "A") })
	w.tick(context.Background())
	f.InList(nil)
	if f.Has("Proxy3") {
		t.Fatalf("Proxy3 не снят: posts=%v", f.Posts)
	}
	if op.deferredProxies["Proxy3"] == nil {
		t.Fatal("новая метка потеряна успехом устаревшей попытки")
	}
}

type staticSubProxies struct{ list []SubscriptionProxy }

func (s *staticSubProxies) SubscriptionProxies() []SubscriptionProxy { return s.list }

// mustConfigWithout — текущий конфиг без туннеля tag.
func mustConfigWithout(t *testing.T, op *Operator, tag string) *Config {
	t.Helper()
	cfg, err := op.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.RemoveTunnel(tag); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// R38: метки переживают рестарт — новый Operator на том же каталоге грузит
// файл, первый тик снимает сироту, опустевший набор убирает файл.
func TestScenario_DeferredMarks_SurviveRestart(t *testing.T) {
	op, _, f, _ := f562Stand(t)
	ctx := context.Background()
	f.FailList(errors.New("rci down"))
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(op.dir, deferredProxiesFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("файл меток не записан: %v", err)
	}
	f.FailList(nil)

	op2 := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: op.dir, Binary: op.binary})
	op2.proxyMgr = op.proxyMgr
	op2.manuallyStopped.Store(true)
	w2 := NewWatchdog(op2, nil, nil)
	w2.swept.Store(true)
	w2.tick(ctx)
	if f.Has("Proxy3") {
		t.Fatalf("после рестарта: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy4", "Proxy5", "Proxy6")
	clean(t, f)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("пустой набор, а файл есть: %v", err)
	}
}

// R38: битый файл — пустой набор и ровно один Warn.
func TestScenario_DeferredMarks_CorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, deferredProxiesFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	op := newOrchedOperatorWithDeps(t, OperatorDeps{Dir: dir, Log: slog.New(slog.NewTextHandler(&buf, nil))})
	if len(op.deferredProxies) != 0 {
		t.Fatalf("набор не пуст: %v", op.deferredProxies)
	}
	if n := strings.Count(buf.String(), "deferred proxy removals"); n != 1 || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("Warn о файле %d раз, лог:\n%s", n, buf.String())
	}
}

// R38: набор не менялся (повторная метка, отказ с ростом выдержки) — файл
// не переписан (тот же inode: AtomicWrite кладёт новый через rename).
func TestScenario_DeferredMarks_NoWriteWhenUnchanged(t *testing.T) {
	op, w, _, p := f562Stand(t)
	p.refuse = true
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.deferProxyRemoval("Proxy3", "A")
	path := filepath.Join(op.dir, deferredProxiesFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	op.deferProxyRemoval("Proxy3", "A")
	w.tick(context.Background()) // отказ сноса, выдержка 30 с
	if p.attempts() != 1 {
		t.Fatalf("попыток=%d, want 1", p.attempts())
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("файл меток переписан без смены набора")
	}
}

// R38 + ревью F4: флаг уборки режима off снимается только после
// прочитанного списка; тик делает её при мёртвом sing-box и раннем выходе
// Reconcile (ручной стоп). Найденное — наши Proxy3/Proxy4 — сносится тем
// же тиком через метки; чужие и Proxy0 целы.
func TestScenario_OrphanCleanupFlag_KeptOnFailure(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.MarkNeedsOrphanCleanup()
	ctx := context.Background()

	f.FailList(errors.New("rci down"))
	w.tick(ctx)
	if !op.needsOrphanCleanup.Load() || len(f.Posts) != 0 {
		t.Fatalf("после отказа: флаг=%v posts=%v", op.needsOrphanCleanup.Load(), f.Posts)
	}
	f.FailList(nil)
	w.tick(ctx)
	if op.needsOrphanCleanup.Load() {
		t.Fatal("флаг не снят после прочитанного списка")
	}
	if f.Has("Proxy3") || f.Has("Proxy4") {
		t.Fatalf("наши не сняты: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy5", "Proxy6")
	clean(t, f)
}

// Ревью F6: с меткой решение — по свежему списку, не по памяти. Память
// тёплая и не знает Proxy8 (хук создания потерян), NDMS его знает — снос
// идёт.
func TestScenario_DeferredMark_DecidesByFreshList(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	pm := op.proxyMgr.(*ProxyManager)
	if _, err := pm.queries.Interfaces.List(context.Background()); err != nil { // память тёплая
		t.Fatal(err)
	}
	f.Add(ndms.Interface{ID: "Proxy8", Type: "Proxy", Description: "gone", State: "up"})
	_ = f.DrainHooks()
	op.deferProxyRemoval("Proxy8", "gone")
	w.tick(context.Background())
	if f.Has("Proxy8") {
		t.Fatalf("Proxy8 не снят: posts=%v", f.Posts)
	}
	clean(t, f)
}

// Ре-ревью N1: индекс подписки переживает MigrateOff в store; рестарт в
// режиме off поднимает флаг. Пользовательский Proxy7 ("Work") на имени
// подписки sub1 цел — совпадение имени без Label не наше; Proxy9 подписки
// sub2 с её Label снимается.
func TestScenario_OrphanCleanupFlag_SubscriptionNeedsLabel(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	f.Add(ndms.Interface{ID: "Proxy7", Type: "Proxy", Description: "Work", State: "up"})
	f.Add(ndms.Interface{ID: "Proxy9", Type: "Proxy", Description: "sub2", State: "up"})
	_ = f.DrainHooks()
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{
		{Index: 7, Port: 1087, Label: "sub1"},
		{Index: 9, Port: 1089, Label: "sub2"},
	}}
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.MarkNeedsOrphanCleanup() // как singbox_core на старте в режиме off
	w.tick(context.Background())
	if f.Has("Proxy9") {
		t.Fatalf("Proxy9 подписки не снят: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy7", "Proxy0", "Proxy5", "Proxy6")
	if op.deferredProxies["Proxy7"] != nil {
		t.Fatalf("метка на чужом Proxy7: %v", op.deferredProxies)
	}
	clean(t, f)
}

// Ре-ревью N2: список не читается — на тик одна попытка списка, а не по
// одной на каждую созревшую метку (все под migrationMu).
func TestScenario_DeferredSweep_ListErrorOneReadPerTick(t *testing.T) {
	op, w, f, _ := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.deferProxyRemoval("Proxy3", "A")
	op.deferProxyRemoval("Proxy4", "B")
	op.deferProxyRemoval("Proxy5", "user")
	f.FailList(errors.New("rci down"))
	w.tick(context.Background())
	if n := f.ListCalls(); n != 1 || len(f.Posts) != 0 {
		t.Fatalf("списков=%d posts=%v, want 1/пусто", n, f.Posts)
	}
	if len(op.deferredProxies) != 3 {
		t.Fatalf("метки потеряны: %v", op.deferredProxies)
	}
}

// F577 (в): MigrateOn после выключения режима — индекс подписки (Proxy5) из
// store занял пользовательский прокси "user". По нему ни одной команды,
// description цел; ProxyN туннелей (свои, подняты) тоже без команд.
func TestScenario_MigrateOn_ForeignOnSubscriptionIndex_Untouched(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{{Index: 5, Port: 1085, Label: "sub1"}}}
	if err := NewMigrator(op, &fakeToggler{events: new([]string)}, nil).MigrateOn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды: %v", f.Posts)
	}
	_, rec, ok, err := op.proxyMgr.(*ProxyManager).queries.Interfaces.Confirm(context.Background(), "Proxy5")
	if err != nil || !ok || rec.Description != "user" {
		t.Fatalf("Proxy5: rec=%+v ok=%v err=%v", rec, ok, err)
	}
	clean(t, f)
}

// F577 (г): переименование туннеля A → A2 — его Proxy3 (description "A")
// перенастроен на "A2"; пользовательский Proxy0 с description "A" цел.
func TestScenario_RenameTunnel_ProxyOwnedByOldTag(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	ctx := context.Background()
	if err := op.RenameTunnel(ctx, "A", "A2"); err != nil {
		t.Fatal(err)
	}
	q := op.proxyMgr.(*ProxyManager).queries
	if _, rec, ok, err := q.Interfaces.Confirm(ctx, "Proxy3"); err != nil || !ok || rec.Description != "A2" {
		t.Fatalf("Proxy3: rec=%+v ok=%v err=%v posts=%v", rec, ok, err, f.Posts)
	}
	if _, rec, _, _ := q.Interfaces.Confirm(ctx, "Proxy0"); rec == nil || rec.Description != "A" {
		t.Fatalf("Proxy0: %+v", rec)
	}
	clean(t, f)
}

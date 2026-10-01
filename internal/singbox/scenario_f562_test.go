package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// f562Stand — оператор с туннелями A (Proxy3) и B (Proxy4) в конфиге и
// оракулом NDMS: Proxy3/Proxy4 наши, Proxy5 и Proxy6 — чужие (description
// пользователя и пустой). Конфиг пишется через фейк, оракул ставится после —
// в счётчики оракула попадает только проверяемый путь.
func f562Stand(t *testing.T) (*Operator, *Watchdog, *query.FakeNDMS) {
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
		ndms.Interface{ID: "Proxy3", Type: "Proxy", Description: "A", State: "up"},
		ndms.Interface{ID: "Proxy4", Type: "Proxy", Description: "B", State: "up"},
		ndms.Interface{ID: "Proxy5", Type: "Proxy", Description: "user", State: "up"},
		ndms.Interface{ID: "Proxy6", Type: "Proxy", State: "up"},
	)
	op.proxyMgr = oracleProxyManager(f)
	op.manuallyStopped.Store(true) // Reconcile в тике — не предмет теста
	w := NewWatchdog(op, nil, nil)
	w.swept.Store(true)
	return op, w, f
}

// F562: список при RemoveTunnel не прочитан — ни одной команды, метка; тик
// сторожа с читаемым списком снимает ровно наш Proxy3, чужие и живой Proxy4
// не тронуты; следующий тик без метки списка не читает.
func TestScenario_RemoveTunnel_ListErrorDeferredThenSwept(t *testing.T) {
	op, w, f := f562Stand(t)
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
	for _, n := range []string{"Proxy4", "Proxy5", "Proxy6"} {
		if !f.Has(n) {
			t.Fatalf("%s снят: posts=%v", n, f.Posts)
		}
	}
	for _, p := range f.Posts {
		if !strings.Contains(p, "Proxy3") {
			t.Fatalf("команда не по Proxy3: %s", p)
		}
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d posts=%v", f.E, f.Phantoms, f.Posts)
	}

	lists := f.ListCalls()
	w.tick(ctx)
	if got := f.ListCalls() - lists; got != 0 {
		t.Fatalf("тик после уборки прочитал список %d раз, want 0", got)
	}
}

// Без метки тик не читает список (R36).
func TestScenario_WatchdogTick_NoDeferredNoList(t *testing.T) {
	_, w, f := f562Stand(t)
	w.tick(context.Background())
	if n := f.ListCalls(); n != 0 || len(f.Posts) != 0 {
		t.Fatalf("списков=%d posts=%v, want 0/пусто", n, f.Posts)
	}
}

// Тег снова занят туннелем — метка снимается без списка и команд: запись с
// таким description не отличить от живой.
func TestScenario_DeferredTagReused_NoCommands(t *testing.T) {
	op, w, f := f562Stand(t)
	op.deferProxyRemoval("B")
	w.tick(context.Background())
	if n := f.ListCalls(); n != 0 || len(f.Posts) != 0 || !f.Has("Proxy4") {
		t.Fatalf("списков=%d posts=%v", n, f.Posts)
	}
	if len(op.deferredProxies) != 0 {
		t.Fatalf("метка осталась: %v", op.deferredProxies)
	}
}

// Уборка не удалась (список снова не прочитан) — метка остаётся, повтор —
// по выдержке, на тике, где список читается.
func TestScenario_DeferredSweepListError_KeepsMark(t *testing.T) {
	op, w, f := f562Stand(t)
	ctx := context.Background()
	clock := time.Unix(0, 0)
	op.deferredNow = func() time.Time { return clock }
	f.FailList(errors.New("rci down"))
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	w.tick(ctx)
	if len(f.Posts) != 0 || op.deferredProxies["A"] == nil {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	f.FailList(nil)
	w.tick(ctx) // выдержка 30 с ещё не вышла — ни списка, ни команд
	if len(f.Posts) != 0 || op.deferredProxies["A"] == nil {
		t.Fatalf("до выдержки: posts=%v", f.Posts)
	}
	clock = clock.Add(30 * time.Second)
	w.tick(ctx)
	if f.Has("Proxy3") || len(op.deferredProxies) != 0 {
		t.Fatalf("Proxy3 не снят: posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}
}

// failingOrphans — снос отказывает всегда не из-за списка; считает попытки.
type failingOrphans struct {
	fakeProxies
	attempts int
}

func (f *failingOrphans) RemoveOrphanSingboxProxies(context.Context, map[string]bool, map[int]bool, map[int]bool) error {
	f.attempts++
	return errors.New("delete refused")
}

// Отказ сноса не из-за списка: попытки на ступенях выдержки 30 с, 1, 2, 4,
// 8 мин, дальше раз в 15 мин, а не на каждом 30-секундном тике.
func TestScenario_DeferredRemoval_Backoff(t *testing.T) {
	op, w, _ := f562Stand(t)
	stub := &failingOrphans{}
	op.proxyMgr = stub
	clock := time.Unix(0, 0)
	op.deferredNow = func() time.Time { return clock }
	op.deferProxyRemoval("Z")

	var at []int
	for sec := 30; sec <= 3600; sec += 30 {
		clock = time.Unix(int64(sec), 0)
		before := stub.attempts
		w.tick(context.Background())
		if stub.attempts != before {
			at = append(at, sec)
		}
	}
	want := []int{30, 60, 120, 240, 480, 960, 1860, 2760}
	if !slices.Equal(at, want) {
		t.Fatalf("попытки на секундах %v, want %v", at, want)
	}
}

// Удаление подписки: снос ProxyN не прошёл (список не прочитан) — метка по
// Label; строка подписки ушла, тик с читаемым списком снимает Proxy7, живой
// Proxy4 и чужие не тронуты, E и фантомов нет.
func TestScenario_SubscriptionRemoveFailure_DeferredThenSwept(t *testing.T) {
	op, w, f := f562Stand(t)
	ctx := context.Background()
	f.Add(ndms.Interface{ID: "Proxy7", Type: "Proxy", Description: "sub1", State: "up"})
	_ = f.DrainHooks()
	subs := &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}}}
	op.subProxies = subs
	reg := op.SubscriptionProxyRegistrar(op.proxyMgr.(*ProxyManager))

	f.FailList(errors.New("rci down"))
	if err := reg.RemoveProxy(ctx, 7); err == nil {
		t.Fatal("RemoveProxy при непрочитанном списке без ошибки")
	}
	if len(f.Posts) != 0 || op.deferredProxies["sub1"] == nil {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	subs.list = nil // сервис удалил строку подписки
	f.FailList(nil)

	w.tick(ctx)
	if f.Has("Proxy7") || len(op.deferredProxies) != 0 {
		t.Fatalf("Proxy7 не снят: posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	for _, n := range []string{"Proxy3", "Proxy4", "Proxy5", "Proxy6"} {
		if !f.Has(n) {
			t.Fatalf("%s снят: posts=%v", n, f.Posts)
		}
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// Режим NDMS Proxy выключен (MigrateOff не снял Proxy3): живой тег не
// защищает — наших ProxyN в этом режиме быть не должно.
func TestScenario_DeferredRemoval_ModeOffSweepsLiveTag(t *testing.T) {
	op, w, f := f562Stand(t)
	op.ndmsProxyEnabledFn = func() bool { return false }
	op.deferProxyRemoval("A")
	w.tick(context.Background())
	if f.Has("Proxy3") || !f.Has("Proxy4") || !f.Has("Proxy5") || !f.Has("Proxy6") {
		t.Fatalf("posts=%v", f.Posts)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}
}

type staticSubProxies struct{ list []SubscriptionProxy }

func (s *staticSubProxies) SubscriptionProxies() []SubscriptionProxy { return s.list }

// MigrateOff при непрочитанном списке: снос ProxyN туннелей и подписки
// отложен, первый тик сторожа (режим выключен) снимает все три, чужие
// Proxy5/Proxy6 не тронуты.
func TestScenario_MigrateOffListError_DeferredThenSwept(t *testing.T) {
	op, w, f := f562Stand(t)
	ctx := context.Background()
	f.Add(ndms.Interface{ID: "Proxy7", Type: "Proxy", Description: "sub1", State: "up"})
	_ = f.DrainHooks()
	op.subProxies = &staticSubProxies{list: []SubscriptionProxy{{Index: 7, Port: 1087, Label: "sub1"}}}
	var events []string
	m := NewMigrator(op, &fakeToggler{events: &events}, nil)
	op.ndmsProxyEnabledFn = func() bool { return false }

	f.FailList(errors.New("rci down"))
	if err := m.MigrateOff(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 || len(op.deferredProxies) != 3 {
		t.Fatalf("posts=%v метки=%v", f.Posts, op.deferredProxies)
	}
	f.FailList(nil)

	w.tick(ctx)
	for _, n := range []string{"Proxy3", "Proxy4", "Proxy7"} {
		if f.Has(n) {
			t.Fatalf("%s не снят: posts=%v", n, f.Posts)
		}
	}
	if !f.Has("Proxy5") || !f.Has("Proxy6") || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v E=%d фантомов=%d", f.Posts, f.E, f.Phantoms)
	}
}

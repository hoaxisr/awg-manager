package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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
	if len(op.deferredProxyTags) != 0 {
		t.Fatalf("метка осталась: %v", op.deferredProxyTags)
	}
}

// Уборка не удалась (список снова не прочитан) — метка остаётся до тика,
// на котором список читается.
func TestScenario_DeferredSweepListError_KeepsMark(t *testing.T) {
	op, w, f := f562Stand(t)
	ctx := context.Background()
	f.FailList(errors.New("rci down"))
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	w.tick(ctx)
	if len(f.Posts) != 0 || !op.deferredProxyTags["A"] {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxyTags)
	}
	f.FailList(nil)
	w.tick(ctx)
	if f.Has("Proxy3") || len(op.deferredProxyTags) != 0 {
		t.Fatalf("Proxy3 не снят: posts=%v метка=%v", f.Posts, op.deferredProxyTags)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}
}

package singbox

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

const f577Link = "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@n.example:443?security=tls&sni=h#N"

func tunnelTags(t *testing.T, op *Operator) []string {
	t.Helper()
	cfg, err := op.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, tn := range cfg.Tunnels() {
		tags = append(tags, tn.Tag)
	}
	return tags
}

// F577 N2: свободный по списку Proxy1 занят чужим невидимым "Work" — NDMS не
// создал запись: туннель снят из конфига (индекс не удержан), команд по
// Proxy1, кроме голого создания, нет, метки нет. Следующая попытка — по
// свежему списку: Proxy2.
func TestScenario_AddTunnels_HiddenForeignSlot_Unslotted(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	ctx := context.Background()
	f.HideCreated(-1)
	f.Add(ndms.Interface{ID: "Proxy1", Type: "Proxy", Description: "Work", State: "up"})
	f.HideCreated(0)

	added, errs, err := op.AddTunnels(ctx, f577Link)
	if err != nil || len(added) != 0 || len(errs) != 1 || !errors.Is(errs[0].Err, ErrProxyForeign) {
		t.Fatalf("added=%+v errs=%+v err=%v", added, errs, err)
	}
	if tags := tunnelTags(t, op); !slices.Equal(tags, []string{"A", "B"}) {
		t.Fatalf("туннели: %v", tags)
	}
	if !slices.Equal(f.Posts, []string{`{"interface":{"Proxy1":{}}}`}) || op.proxyRemovalDeferred("Proxy1") {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}

	f.DrainHooks() // чужой Proxy1 виден в списке
	f.ExpectCreate("Proxy2")
	added, errs, err = op.AddTunnels(ctx, f577Link)
	if err != nil || len(errs) != 0 || len(added) != 1 || added[0].ProxyInterface != "Proxy2" {
		t.Fatalf("added=%+v errs=%+v err=%v", added, errs, err)
	}
	_, rec, _, _ := op.proxyMgr.(*ProxyManager).queries.Interfaces.Confirm(ctx, "Proxy1")
	if rec == nil || rec.Description != "Work" {
		t.Fatalf("Proxy1: %+v", rec)
	}
	clean(t, f)
}

// F577 N1: «created» доказан, а записи не видно (ErrNotSeen) — туннель снят
// из конфига, голый Proxy1 уходит в метку (Proxy1, ""); когда запись видна,
// тик её снимает.
func TestScenario_AddTunnels_CreatedLeft_MarkedThenSwept(t *testing.T) {
	withProxy501(t)
	op, w, f, _ := f562Stand(t)
	ctx := context.Background()
	op.proxyMgr.(*ProxyManager).queries.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.ExpectCreate("Proxy1")
	f.HideCreated(-1)

	added, errs, err := op.AddTunnels(ctx, f577Link)
	if err != nil || len(added) != 0 || len(errs) != 1 {
		t.Fatalf("added=%+v errs=%+v err=%v", added, errs, err)
	}
	if tags := tunnelTags(t, op); !slices.Equal(tags, []string{"A", "B"}) {
		t.Fatalf("туннели: %v", tags)
	}
	if d := op.deferredProxies["Proxy1"]; d == nil || d.desc != "" {
		t.Fatalf("метка: %+v", op.deferredProxies)
	}
	f.HideCreated(0)
	f.DrainHooks()
	w.tick(ctx)
	if f.Has("Proxy1") || op.proxyRemovalDeferred("Proxy1") {
		t.Fatalf("Proxy1 не снят: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy3", "Proxy4", "Proxy5", "Proxy6")
	clean(t, f)
}

// F577 (опасение 3): слот туннеля A занят пользовательским прокси —
// RemoveTunnel не шлёт по нему ни одной команды и не ставит метку.
func TestScenario_RemoveTunnel_ForeignSlot_Untouched(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	f.Remove("Proxy3")
	f.Add(ndms.Interface{ID: "Proxy3", Type: "Proxy", Description: "user", State: "up"})
	if err := op.RemoveTunnel(context.Background(), "A"); err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 0 || op.proxyRemovalDeferred("Proxy3") {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	mustHave(t, f, "Proxy3")
	clean(t, f)
}

// F577 (опасение 3): MigrateOff — чужой на слоте B не снимается и не метится,
// свой Proxy3 (A) снят.
func TestScenario_MigrateOff_ForeignSlot_Untouched(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	f.Remove("Proxy4")
	f.Add(ndms.Interface{ID: "Proxy4", Type: "Proxy", Description: "user", State: "up"})
	if err := NewMigrator(op, &fakeToggler{events: new([]string)}, nil).MigrateOff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.Has("Proxy3") || op.proxyRemovalDeferred("Proxy4") {
		t.Fatalf("posts=%v метка=%v", f.Posts, op.deferredProxies)
	}
	mustHave(t, f, "Proxy4")
	clean(t, f)
}

// F577 (опасение 1): роутер не принял новое description (список не
// прочитан) — переименование отменено: в конфиге прежний тег. Повтор
// проходит.
func TestScenario_RenameTunnel_RouterFailure_Reverted(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	ctx := context.Background()
	f.FailList(errors.New("rci down"))
	if err := op.RenameTunnel(ctx, "A", "A2"); err == nil {
		t.Fatal("переименование без роутера прошло")
	}
	if tags := tunnelTags(t, op); !slices.Equal(tags, []string{"A", "B"}) {
		t.Fatalf("туннели: %v", tags)
	}
	f.FailList(nil)
	if err := op.RenameTunnel(ctx, "A", "A2"); err != nil {
		t.Fatal(err)
	}
	if tags := tunnelTags(t, op); !slices.Equal(tags, []string{"A2", "B"}) {
		t.Fatalf("туннели: %v", tags)
	}
	clean(t, f)
}

// F577 N1: у подписок созданный и оставленный ProxyN метит регистратор
// (сервис подписок о метках не знает).
func TestScenario_SubscriptionCreateLeft_Marked(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	pm := op.proxyMgr.(*ProxyManager)
	pm.queries.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.ExpectCreate("Proxy7")
	f.HideCreated(-1)
	if _, err := op.SubscriptionProxyRegistrar(pm).CreateProxy(context.Background(), 7, 1087, "sub1"); err == nil {
		t.Fatal("ErrNotSeen без ошибки")
	}
	if d := op.deferredProxies["Proxy7"]; d == nil || d.desc != "" {
		t.Fatalf("метка: %+v", op.deferredProxies)
	}
	clean(t, f)
}

package singbox

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
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
	f.HideCreated(2)
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

// F577 N1, R54: «created» доказан, а записи в списках нет — запись снесена
// (NDMS ответил «created», снос без E), метки нет; туннель остаётся со своим
// слотом (на роутере его записи нет — следующий Sync создаст заново). Снос
// отказал — туннель снят из конфига, голый Proxy1 уходит в метку (Proxy1,
// ""); тик его снимает.
// Мутация: вернуть «ErrNotSeen — оставить» → снос не послан, метка есть.
func TestScenario_AddTunnels_CreatedNeverListed(t *testing.T) {
	t.Run("dropped", func(t *testing.T) {
		withProxy501(t)
		op, _, f, _ := f562Stand(t)
		op.proxyMgr.(*ProxyManager).queries.Interfaces.SetCreatedBackoff(time.Millisecond)
		f.ExpectCreate("Proxy1")
		f.HideCreated(100)
		added, errs, err := op.AddTunnels(context.Background(), f577Link)
		if err != nil || len(added) != 1 || len(errs) != 1 || !errors.Is(errs[0].Err, query.ErrNotListed) {
			t.Fatalf("added=%+v errs=%+v err=%v", added, errs, err)
		}
		if f.Has("Proxy1") || !slices.Contains(f.Posts, `{"interface":{"Proxy1":{"no":true}}}`) || op.proxyRemovalDeferred("Proxy1") {
			t.Fatalf("has=%v метка=%v posts=%v", f.Has("Proxy1"), op.deferredProxies, f.Posts)
		}
		cfg, err := op.loadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if tags := tunnelTags(t, op); !slices.Equal(tags, []string{"A", "B", "N"}) || cfg.Tunnels()[2].ProxyInterface != "Proxy1" {
			t.Fatalf("туннели: %v, слот N: %+v", tags, cfg.Tunnels())
		}
		// Следующий Sync создаёт запись на слоте туннеля заново.
		f.HideCreated(0)
		f.ExpectCreate("Proxy1")
		if err := op.proxyMgr.(*ProxyManager).SyncProxies(context.Background(), cfg.Tunnels()); err != nil || !f.Has("Proxy1") {
			t.Fatalf("Sync: err=%v has=%v posts=%v", err, f.Has("Proxy1"), f.Posts)
		}
		mustHave(t, f, "Proxy0", "Proxy3", "Proxy4", "Proxy5", "Proxy6")
		clean(t, f)
	})
	t.Run("drop refused, marked then swept", func(t *testing.T) {
		withProxy501(t)
		op, w, f, p := f562Stand(t)
		ctx := context.Background()
		op.proxyMgr.(*ProxyManager).queries.Interfaces.SetCreatedBackoff(time.Millisecond)
		f.ExpectCreate("Proxy1")
		f.HideCreated(100)
		p.refuse = true
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
		p.refuse = false
		f.HideCreated(0)
		f.ShowHidden()
		f.DrainHooks()
		w.tick(ctx)
		if f.Has("Proxy1") || op.proxyRemovalDeferred("Proxy1") {
			t.Fatalf("Proxy1 не снят: posts=%v", f.Posts)
		}
		mustHave(t, f, "Proxy0", "Proxy3", "Proxy4", "Proxy5", "Proxy6")
		clean(t, f)
	})
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
// прочитан) — переименование отказано: в конфиге прежний тег. Повтор
// проходит.
func TestScenario_RenameTunnel_RouterFailure_NoLocalChange(t *testing.T) {
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

// F577 N1, R54: у подписок созданное и не показанное списком снесено, метки
// нет; снос отказал — оставленный ProxyN метит регистратор (сервис подписок о
// метках не знает).
func TestScenario_SubscriptionCreateLeft_Marked(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		withProxy501(t)
		op, _, f, p := f562Stand(t)
		pm := op.proxyMgr.(*ProxyManager)
		pm.queries.Interfaces.SetCreatedBackoff(time.Millisecond)
		f.ExpectCreate("Proxy7")
		f.HideCreated(100)
		p.refuse = refuse
		if _, err := op.SubscriptionProxyRegistrar(pm).CreateProxy(context.Background(), 7, 1087, "sub1"); err == nil {
			t.Fatalf("refuse=%v: ErrNotListed без ошибки", refuse)
		}
		if d := op.deferredProxies["Proxy7"]; refuse && (d == nil || d.desc != "") || !refuse && (d != nil || f.Has("Proxy7")) {
			t.Fatalf("refuse=%v: has=%v метка=%+v", refuse, f.Has("Proxy7"), op.deferredProxies)
		}
		clean(t, f)
	}
}

// orphanOnLiveSlot — голая сирота на слоте СУЩЕСТВУЮЩЕГО туннеля A:
// пользователь снял Proxy3, Sync создал запись, NDMS её не показал, а снос
// отказал (ErrLeftOnRouter) — метка (Proxy3, ""); затем запись видна голой.
func orphanOnLiveSlot(t *testing.T) (*Operator, *Watchdog, *query.FakeNDMS, *ProxyManager, []TunnelInfo) {
	t.Helper()
	withProxy501(t)
	op, w, f, p := f562Stand(t)
	pm := op.proxyMgr.(*ProxyManager)
	pm.queries.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.Remove("Proxy3")
	f.DrainHooks()
	f.ExpectCreate("Proxy3")
	f.HideCreated(100)
	cfg, err := op.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	p.refuse = true
	if err := pm.SyncProxies(context.Background(), cfg.Tunnels()); err != nil {
		t.Fatalf("SyncProxies: %v", err)
	}
	p.refuse = false
	if d := op.deferredProxies["Proxy3"]; d == nil || d.desc != "" {
		t.Fatalf("метка: %+v", op.deferredProxies)
	}
	f.HideCreated(0)
	f.ShowHidden()
	f.DrainHooks() // голый Proxy3 виден
	return op, w, f, pm, cfg.Tunnels()
}

// F577 R1/N1: ближайший тик сторожа (sing-box работает, Reconcile не
// зовётся) сам усыновляет голую сироту на слоте живого туннеля: настройки
// туннеля A, метка снята; RemoveTunnel затем снимает Proxy3.
func TestScenario_BareOrphanOnLiveSlot_TickAdoptsThenRemoved(t *testing.T) {
	op, w, f, pm, _ := orphanOnLiveSlot(t)
	ctx := context.Background()
	op.manuallyStopped.Store(false) // sing-box «работает»: Reconcile тик не зовёт
	w.tick(ctx)
	_, rec, ok, _ := pm.queries.Interfaces.Confirm(ctx, "Proxy3")
	if !ok || rec.Description != "A" || op.proxyRemovalDeferred("Proxy3") {
		t.Fatalf("не усыновлён тиком: rec=%+v метка=%v posts=%v", rec, op.deferredProxies, f.Posts)
	}
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatal(err)
	}
	if f.Has("Proxy3") {
		t.Fatalf("Proxy3 не снят: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy4", "Proxy5", "Proxy6")
	clean(t, f)
}

// F577 R1: Sync раньше тика — усыновляет он, метка снята.
func TestScenario_BareOrphanOnLiveSlot_SyncAdopts(t *testing.T) {
	op, _, f, pm, tunnels := orphanOnLiveSlot(t)
	ctx := context.Background()
	if err := pm.SyncProxies(ctx, tunnels); err != nil {
		t.Fatal(err)
	}
	_, rec, ok, _ := pm.queries.Interfaces.Confirm(ctx, "Proxy3")
	if !ok || rec.Description != "A" || op.proxyRemovalDeferred("Proxy3") {
		t.Fatalf("rec=%+v метка=%v", rec, op.deferredProxies)
	}
	clean(t, f)
}

// F577 N2: метка (Proxy3, "") на слоте живого туннеля, а в списке записи нет
// или у неё непустой description (пользователь пересоздал) — метка
// снимается, команд нет.
func TestScenario_BareMarkLiveSlot_NotBare_Dropped(t *testing.T) {
	ctx := context.Background()
	t.Run("description не пуст", func(t *testing.T) {
		op, w, f, _, _ := orphanOnLiveSlot(t)
		f.Remove("Proxy3")
		f.Add(ndms.Interface{ID: "Proxy3", Type: "Proxy", Description: "user", State: "up"})
		f.DrainHooks()
		posts := len(f.Posts)
		w.tick(ctx)
		if len(f.Posts) != posts || op.proxyRemovalDeferred("Proxy3") {
			t.Fatalf("posts=%v метка=%v", f.Posts[posts:], op.deferredProxies)
		}
		clean(t, f)
	})
	t.Run("записи нет", func(t *testing.T) {
		op, w, f, _, _ := orphanOnLiveSlot(t)
		f.Remove("Proxy3")
		f.DrainHooks()
		posts := len(f.Posts)
		w.tick(ctx)
		if len(f.Posts) != posts || op.proxyRemovalDeferred("Proxy3") {
			t.Fatalf("posts=%v метка=%v", f.Posts[posts:], op.deferredProxies)
		}
		clean(t, f)
	})
}

// F577 R1/R5: голая запись без метки — чужая для EnsureProxy и OwnedProxies
// (на слоте туннеля тоже); с меткой — наша: EnsureProxy усыновляет и снимает
// метку.
func TestEnsureProxy_BareNeedsMark(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	ctx := context.Background()
	pm := op.proxyMgr.(*ProxyManager)
	f.Add(ndms.Interface{ID: "Proxy7", Type: "Proxy", State: "up"})
	if err := pm.EnsureProxy(ctx, 7, 1087, "sub1", "sub1"); !errors.Is(err, ErrProxyForeign) || len(f.Posts) != 0 {
		t.Fatalf("без метки: err=%v posts=%v", err, f.Posts)
	}
	marks, err := pm.OwnedProxies(ctx, map[string]string{"Proxy6": "X"}, nil)
	if err != nil || len(marks) != 0 {
		t.Fatalf("голый Proxy6 на слоте без метки: %v err=%v", marks, err)
	}
	op.deferProxyRemoval("Proxy6", "")
	if marks, _ := pm.OwnedProxies(ctx, map[string]string{"Proxy6": "X"}, nil); len(marks) != 1 {
		t.Fatalf("голый Proxy6 с меткой: %v", marks)
	}
	op.deferProxyRemoval("Proxy7", "")
	if err := pm.EnsureProxy(ctx, 7, 1087, "sub1", "sub1"); err != nil {
		t.Fatal(err)
	}
	_, rec, _, _ := pm.queries.Interfaces.Confirm(ctx, "Proxy7")
	if rec == nil || rec.Description != "sub1" || op.proxyRemovalDeferred("Proxy7") {
		t.Fatalf("rec=%+v метка=%v", rec, op.deferredProxies)
	}
	clean(t, f)
}

// F577 R3: оставленное при создании (не показано списком, снос отказал) не
// обрывает SyncProxies — следующий туннель тоже обслужен (обе записи
// получили метки).
func TestSyncProxies_LeftContinues(t *testing.T) {
	withProxy501(t)
	op, _, f, p := f562Stand(t)
	pm := op.proxyMgr.(*ProxyManager)
	pm.queries.Interfaces.SetCreatedBackoff(time.Millisecond)
	f.Remove("Proxy3")
	f.Remove("Proxy4")
	f.DrainHooks()
	f.ExpectCreate("Proxy3", "Proxy4")
	f.HideCreated(100)
	p.refuse = true
	cfg, _ := op.loadConfig()
	if err := pm.SyncProxies(context.Background(), cfg.Tunnels()); err != nil {
		t.Fatal(err)
	}
	if !op.proxyRemovalDeferred("Proxy3") || !op.proxyRemovalDeferred("Proxy4") {
		t.Fatalf("метки: %v posts=%v", op.deferredProxies, f.Posts)
	}
}

// F577 R2: переименование — роутер ДО конфига. Новое description применено,
// ответ потерян — переименование отказано без локальных изменений, description
// возвращён к прежнему одной попыткой.
func TestScenario_RenameTunnel_LostReply_DescriptionRestored(t *testing.T) {
	withProxy501(t)
	op, _, f, p := f562Stand(t)
	ctx := context.Background()
	p.lostReplyOn = `"description":"A2"`
	if err := op.RenameTunnel(ctx, "A", "A2"); err == nil {
		t.Fatal("переименование без ответа роутера прошло")
	}
	if tags := tunnelTags(t, op); !slices.Equal(tags, []string{"A", "B"}) {
		t.Fatalf("туннели: %v", tags)
	}
	_, rec, _, _ := op.proxyMgr.(*ProxyManager).queries.Interfaces.Confirm(ctx, "Proxy3")
	if rec == nil || rec.Description != "A" {
		t.Fatalf("Proxy3: %+v", rec)
	}
	clean(t, f)
}

// F577 R1: метка голой записи на живом слоте, а записи так и нет — Sync
// создаёт её заново; метка снимается, а не висит до удаления туннеля.
func TestScenario_BareMark_RecreatedClears(t *testing.T) {
	withProxy501(t)
	op, _, f, _ := f562Stand(t)
	pm := op.proxyMgr.(*ProxyManager)
	f.Remove("Proxy3")
	f.DrainHooks()
	op.deferProxyRemoval("Proxy3", "")
	f.ExpectCreate("Proxy3")
	cfg, _ := op.loadConfig()
	if err := pm.SyncProxies(context.Background(), cfg.Tunnels()); err != nil {
		t.Fatal(err)
	}
	if !f.Has("Proxy3") || op.proxyRemovalDeferred("Proxy3") {
		t.Fatalf("has=%v метка=%v", f.Has("Proxy3"), op.deferredProxies)
	}
	clean(t, f)
}

// L1: голая метка (Proxy3, "") + RemoveTunnel при непрочитанном списке —
// отложенный снос с тегом метку не затирает; тик сносит голую запись.
func TestScenario_BareMark_NotOverwrittenByTag(t *testing.T) {
	op, w, f, _, _ := orphanOnLiveSlot(t)
	ctx := context.Background()
	if d := op.deferredProxies["Proxy3"]; d == nil || d.desc != "" {
		t.Fatalf("исходная метка = %+v, want голая", d)
	}
	f.FailList(errors.New("rci down"))
	if err := op.RemoveTunnel(ctx, "A"); err != nil {
		t.Fatalf("RemoveTunnel: %v", err)
	}
	if d := op.deferredProxies["Proxy3"]; d == nil || d.desc != "" {
		t.Fatalf("метка после RemoveTunnel = %+v, want голая", d)
	}
	f.FailList(nil)
	w.tick(ctx)
	if f.Has("Proxy3") {
		t.Fatalf("голая сирота не снята тиком: posts=%v", f.Posts)
	}
	mustHave(t, f, "Proxy0", "Proxy4", "Proxy5", "Proxy6")
	clean(t, f)
}

// L2/R40: кандидаты bind (#323) — голая запись на слоте живого туннеля наша
// только с меткой (name, ""); без метки она чужая и предлагается.
func TestScenario_ListNativeProxies_BareNeedsMark(t *testing.T) {
	op, _, f, _ := f562Stand(t)
	ctx := context.Background()
	// Proxy3 — слот туннеля A, запись голая; имя ядра известно резолверу.
	f.Add(ndms.Interface{ID: "Proxy3", Type: "Proxy", State: "up", SystemName: "t2s3"})
	f.Add(ndms.Interface{ID: "Proxy5", Type: "Proxy", Description: "user", State: "up", SystemName: "t2s5"})

	op.deferProxyRemoval("Proxy3", "")
	got, err := op.ListNativeProxies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "t2s5") {
		t.Fatalf("чужой Proxy5 не предложен: %v", got)
	}
	if slices.Contains(got, "t2s3") {
		t.Fatalf("голая Proxy3 с меткой предложена как чужая: %v", got)
	}
	op.clearBareMark("Proxy3")
	if got, err = op.ListNativeProxies(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, "t2s3") {
		t.Fatalf("голая Proxy3 без метки на слоте туннеля A скрыта: %v", got)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды: %v", f.Posts)
	}
	clean(t, f)
}

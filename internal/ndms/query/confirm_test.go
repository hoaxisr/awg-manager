package query

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func TestConfirm_ReadsOneListAndUpdatesMap(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: "old"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	_, _ = s.Get(ctx, "Wireguard0")
	f.Add(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: "renamed outside"}) // правка мимо хуков (F532)
	f.Add(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})                                 // создан без хука
	lists := f.ListCalls()
	c, rec, ok, err := s.Confirm(ctx, "Wireguard0")
	if err != nil || !ok || c.Name() != "Wireguard0" || rec.Description != "renamed outside" {
		t.Fatalf("c=%v rec=%#v ok=%v err=%v", c, rec, ok, err)
	}
	if f.ListCalls() != lists+1 || f.E != 0 {
		t.Fatalf("one list, no point reads: lists=%d E=%d", f.ListCalls()-lists, f.E)
	}
	if got, _ := s.Get(ctx, "Wireguard3"); got == nil {
		t.Fatal("Confirm must apply the whole list to the map")
	}
}

func TestConfirm_Absent(t *testing.T) {
	f := NewFakeNDMS()
	s := NewInterfaceStore(f, NopLogger())
	if _, _, ok, err := s.Confirm(context.Background(), "Proxy1"); ok || err != nil || f.E != 0 {
		t.Fatalf("ok=%v err=%v E=%d", ok, err, f.E)
	}
}

func TestConfirm_ListError_FailsClosed(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0"})
	s := NewInterfaceStore(f, NopLogger())
	_, _ = s.Get(context.Background(), "Wireguard0")
	f.FailList(errors.New("rci down"))
	if _, _, ok, err := s.Confirm(context.Background(), "Wireguard0"); err == nil || ok {
		t.Fatalf("cached presence must not stand in for a fresh list: ok=%v err=%v", ok, err)
	}
}

func TestConfirmEach_OneList(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	_, _ = s.Get(ctx, "Wireguard0")
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"}) // создан без хука
	lists := f.ListCalls()
	got, err := s.ConfirmEach(ctx, []string{"Wireguard0", "Wireguard1", "Wireguard9"})
	if err != nil || len(got) != 2 || got["Wireguard0"].Name() != "Wireguard0" || got["Wireguard1"].Name() != "Wireguard1" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if _, ok := got["Wireguard9"]; ok {
		t.Fatal("absent name confirmed")
	}
	if f.ListCalls() != lists+1 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("one list, no point reads: lists=%d E=%d phantoms=%d", f.ListCalls()-lists, f.E, f.Phantoms)
	}
	f.FailList(errors.New("rci down"))
	if got, err := s.ConfirmEach(ctx, []string{"Wireguard0"}); err == nil || got != nil {
		t.Fatalf("list error must fail closed: got=%v err=%v", got, err)
	}
}

// confirmInFlight бутстрапит стор на f, затем выполняет change (правка NDMS
// мимо стора) и запускает Confirm(name), который останавливается на запросе
// списка; hook приходит, пока список в полёте.
func confirmInFlight(t *testing.T, f *FakeNDMS, name string, change func(), hook func(*InterfaceStore)) (*InterfaceStore, *ndms.Interface, bool) {
	t.Helper()
	bg := &blockingGetter{Getter: f, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	change()
	bg.gate = make(chan struct{})
	type res struct {
		rec *ndms.Interface
		ok  bool
		err error
	}
	done := make(chan res, 1)
	go func() {
		_, rec, ok, err := s.Confirm(context.Background(), name)
		done <- res{rec, ok, err}
	}()
	bg.waitBlocked(t)
	hook(s)
	close(bg.gate)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	return s, r.rec, r.ok
}

// Back-to-back create: запись уже в NDMS и в ответе списка, а ifcreated
// доехал, пока список в полёте. Seq-гард не кладёт её в карту (хук новее
// ответа, имя ждёт в pending), но подтверждение отвечает по ответу NDMS.
func TestConfirm_CreatedHookDuringList_Confirms(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	add := func() { f.Add(ndms.Interface{ID: "Wireguard3", Type: "Wireguard", Description: "new"}) }
	_, rec, ok := confirmInFlight(t, f, "Wireguard3", add, func(s *InterfaceStore) { s.OnCreated("Wireguard3") })
	if !ok || rec == nil || rec.Description != "new" {
		t.Fatalf("record in the fresh list must be confirmed: ok=%v rec=%#v", ok, rec)
	}
	if f.E != 0 {
		t.Fatalf("E = %d", f.E)
	}
}

// noIfacePosts — сколько `no interface` послано в оракул.
func noIfacePosts(f *FakeNDMS) int {
	n := 0
	for _, p := range f.Posts {
		if strings.Contains(p, `"no":true`) {
			n++
		}
	}
	return n
}

// warmStore — стор на f после бутстрапа.
func warmStore(t *testing.T, f *FakeNDMS) *InterfaceStore {
	t.Helper()
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	return s
}

// S7: X снят по-настоящему, пока список в полёте: ответ с X противоречит
// ifdestroyed новее его начала — один повторный список, X в нём нет.
// Мутация: убрать перечитывание → ok=true, ListCalls +1.
func TestConfirm_RealRemovalDuringList_RereadsNotConfirmed(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	f.InList(func() {
		f.InList(nil)
		f.Remove("Wireguard0")
		for _, h := range f.HooksFor("Wireguard0") {
			s.OnDestroyed(h.ID)
		}
	})
	lists := f.ListCalls()
	_, rec, ok, err := s.Confirm(context.Background(), "Wireguard0")
	if err != nil || ok || rec != nil {
		t.Fatalf("снятое во время списка подтверждено: ok=%v rec=%#v err=%v", ok, rec, err)
	}
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("%d списков, want 2 (свой + повтор)", got)
	}
	if noIfacePosts(f) != 0 || f.E != 0 {
		t.Fatalf("posts=%v E=%d", f.Posts, f.E)
	}
}

// ifcreated пришёл, пока список в полёте, а в ответе имени нет: противоречие —
// повторный список; записи нет и в нём — не подтверждено.
// Мутация: убрать перечитывание → ListCalls +1.
func TestConfirm_CreatedHookNotInList_NotConfirmed(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	f.InList(func() {
		f.InList(nil)
		s.OnCreated("Wireguard3")
	})
	lists := f.ListCalls()
	_, rec, ok, err := s.Confirm(context.Background(), "Wireguard3")
	if err != nil || ok || rec != nil {
		t.Fatalf("name absent from the list must not be confirmed: ok=%v rec=%#v err=%v", ok, rec, err)
	}
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("%d списков, want 2 (свой + повтор)", got)
	}
}

// 2.1: X снят и создан заново (чужой цикл, переиспользованное имя), хуки не
// доставлены; ifdestroyed ПРЕЖНЕГО X приходит, пока список в полёте. Ответ с X
// противоречит хуку — повтор; X в нём есть — подтверждено.
// Мутации: убрать перечитывание → ListCalls +1; решение по карте (HEAD
// confirmedLocked: Forget снёс X из byID) → ok=false.
func TestConfirm_StaleDestroyedDuringList_Confirms(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	f.Remove("Wireguard0")
	f.Add(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"}) // без дренажа: старый ifdestroyed первым
	f.InList(func() {
		f.InList(nil)
		hooks := f.HooksFor("Wireguard0")
		if len(hooks) == 0 || hooks[0].Type != "ifdestroyed" {
			t.Errorf("hooks: %+v", hooks)
			return
		}
		s.OnDestroyed(hooks[0].ID)
	})
	lists := f.ListCalls()
	c, rec, ok, err := s.Confirm(context.Background(), "Wireguard0")
	if err != nil || !ok || c.Name() != "Wireguard0" || rec == nil {
		t.Fatalf("живой X не подтверждён: ok=%v rec=%#v err=%v", ok, rec, err)
	}
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("%d списков, want 2 (свой + повтор)", got)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// S14: свой ifcreated в полёте списка, X в ответе — противоречия нет, повтора
// нет. Мутация: перечитывать безусловно → ListCalls +2.
func TestConfirm_OwnCreatedDuringList_NoReread(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	f.Add(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	_ = f.DrainHooks()
	f.InList(func() {
		f.InList(nil)
		s.OnCreated("Wireguard3")
	})
	lists := f.ListCalls()
	if _, _, ok, err := s.Confirm(context.Background(), "Wireguard3"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("%d списков, want 1", got)
	}
}

// H2 (П12): в списке действия X есть, в более новом применённом (хук не
// доставлен) — нет. Решает самый новый и «в минус»: не подтверждено, без
// списка. Мутация: применённый только «в плюс» → ok=true.
func TestConfirm_ActionList_NewerAppliedLacksName_NotConfirmed(t *testing.T) {
	f, s := actionStore(t)
	ctx := WithActionList(context.Background())
	if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	f.Remove("Wireguard1")
	_ = f.DrainHooks() // хук не доставлен
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	lists := f.ListCalls()
	if _, rec, ok, err := s.Confirm(ctx, "Wireguard1"); err != nil || ok || rec != nil {
		t.Fatalf("снятое по новому списку подтверждено: ok=%v rec=%#v err=%v", ok, rec, err)
	}
	if got := f.ListCalls() - lists; got != 0 {
		t.Fatalf("%d списков, want 0", got)
	}
	if noIfacePosts(f) != 0 || f.E != 0 {
		t.Fatalf("posts=%v E=%d", f.Posts, f.E)
	}
}

// Противоречие хотя бы по одному имени — один общий повтор, решение по нему:
// W1 снят по-настоящему, по W2 — устаревший ifdestroyed (W2 жив).
// Мутация: убрать перечитывание → W1 подтверждён, ListCalls +1.
func TestConfirmEach_OneContradiction_OneReread(t *testing.T) {
	f, s := actionStore(t)
	f.InList(func() {
		f.InList(nil)
		f.Remove("Wireguard1")
		for _, h := range f.HooksFor("Wireguard1") {
			s.OnDestroyed(h.ID)
		}
		s.OnDestroyed("Wireguard2")
	})
	lists := f.ListCalls()
	got, err := s.ConfirmEach(context.Background(), []string{"Wireguard0", "Wireguard1", "Wireguard2"})
	if err != nil || len(got) != 2 || got["Wireguard0"].Name() != "Wireguard0" || got["Wireguard2"].Name() != "Wireguard2" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if n := f.ListCalls() - lists; n != 2 {
		t.Fatalf("%d списков, want 2 (свой + один общий повтор)", n)
	}
}

// Чужое создание Y в полёте списка ConfirmAll: Y — кандидат по метке, ответ
// без Y ей противоречит — повтор, Y подтверждён.
// Мутация: убрать повтор → Y нет.
func TestConfirmAll_CreatedDuringFlight_Reread(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	f.InList(func() {
		f.InList(nil)
		f.Add(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
		for _, h := range f.HooksFor("Wireguard3") {
			if h.Type == "ifcreated" {
				s.OnCreated(h.ID)
			}
		}
	})
	lists := f.ListCalls()
	got, err := s.ConfirmAll(context.Background())
	if err != nil || len(got) != 2 || got["Wireguard3"].Name() != "Wireguard3" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	if n := f.ListCalls() - lists; n != 2 {
		t.Fatalf("%d списков, want 2", n)
	}
}

// PeersRCFresh проверяет присутствие свежим списком (#713): сервер, снятый
// без хука, — ErrGone без точечного чтения (без E); созданный без хука —
// читается.
func TestPeersRCFresh_ConfirmsByFreshList(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	q := NewQueries(Deps{Getter: f, Logger: NopLogger()})
	ctx := context.Background()
	_, _ = q.Interfaces.List(ctx)
	f.Remove("Wireguard0")
	if _, err := q.WGServers.PeersRCFresh(ctx, "Wireguard0"); !errors.Is(err, ErrGone) || f.E != 0 {
		t.Fatalf("err=%v E=%d", err, f.E)
	}
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	if _, err := q.WGServers.PeersRCFresh(ctx, "Wireguard1"); err != nil || f.E != 0 {
		t.Fatalf("err=%v E=%d", err, f.E)
	}
}

// Стенд 5.01.C.6: на создание NDMS шлёт iflayerchanged ctrl ×2, а ifcreated —
// ~1 с спустя. Confirm сразу после импорта: оба layer-хука приходят, пока
// список в полёте, ifcreated — после ответа. Layer-хук по незнакомому id
// ничего не решает: Confirm подтверждает по ответу, а запись уже в карте —
// ifcreated совпадает с ней и списка не стоит.
func TestConfirm_StandHookOrder_LayerDuringList_Confirms(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	if _, err := f.Post(ctx, map[string]any{"interface": map[string]any{"wireguard": map[string]any{"import": "x"}}}); err != nil {
		t.Fatal(err)
	}
	hooks := f.DrainHooks()
	if len(hooks) != 3 || hooks[2].Type != "ifcreated" {
		t.Fatalf("hooks: %+v", hooks)
	}
	f.InList(func() {
		f.InList(nil)
		for _, h := range hooks[:2] {
			s.OnLayerChanged(h.ID, h.Layer, h.Level)
		}
	})
	c, rec, ok, err := s.Confirm(ctx, "Wireguard1")
	if err != nil || !ok || c.Name() != "Wireguard1" || rec == nil {
		t.Fatalf("created record must be confirmed: ok=%v rec=%#v err=%v", ok, rec, err)
	}
	s.OnCreated(hooks[2].ID)
	if err := s.ReconcileDirty(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "Wireguard1"); got == nil {
		t.Fatal("record must land in the map after ifcreated")
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d Phantoms=%d", f.E, f.Phantoms)
	}
}

// S9/S10: layer/ip-хук по незнакомому id игнорируется — ни метки «грязно», ни
// списка, сколько бы их ни пришло.
func TestLayerHook_UnknownID_Ignored(t *testing.T) {
	for name, hook := range map[string]func(*InterfaceStore){
		"layer": func(s *InterfaceStore) { s.OnLayerChanged("Nope", "ctrl", "running") },
		"ip":    func(s *InterfaceStore) { s.OnIPChanged("Nope", "10.0.0.1") },
	} {
		f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
		s := NewInterfaceStore(f, NopLogger())
		ctx := context.Background()
		if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
			t.Fatal(err)
		}
		lists := f.ListCalls()
		for range 1100 {
			hook(s)
		}
		s.mu.RLock()
		dirty := s.dirtyAt
		s.mu.RUnlock()
		if dirty != 0 {
			t.Fatalf("%s: dirtyAt=%d, want 0", name, dirty)
		}
		if err := s.ReconcileDirty(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.ListCalls() - lists; got != 0 {
			t.Fatalf("%s: %d списков, want 0", name, got)
		}
		if rec, _ := s.Get(ctx, "Nope"); rec != nil {
			t.Fatalf("%s: незнакомый id в карте: %#v", name, rec)
		}
	}
}

// displaced готовит вытесненный ответ: следующий список стора (его ведёт
// Confirm*) в полёте, снимок уже сделан, Wireguard3 в нём нет; тогда
// Wireguard3 создают, ifcreated доставлен, и ReconcileDirty применяет более
// новый список с ним. after — что происходит после нового списка, до ответа.
func displaced(t *testing.T, after func(f *FakeNDMS, s *InterfaceStore)) (*FakeNDMS, *InterfaceStore) {
	t.Helper()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	f.InList(func() {
		f.InList(nil)
		f.Add(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
		for _, h := range f.DrainHooks() {
			s.OnCreated(h.ID)
		}
		if err := s.ReconcileDirty(ctx); err != nil {
			t.Error(err)
		}
		after(f, s)
	})
	return f, s
}

func destroyW3(f *FakeNDMS, s *InterfaceStore) {
	f.Remove("Wireguard3")
	for _, h := range f.DrainHooks() {
		s.OnDestroyed(h.ID)
	}
}

func confirmAllDisplaced(t *testing.T, after func(f *FakeNDMS, s *InterfaceStore)) (map[string]Confirmed, *FakeNDMS) {
	t.Helper()
	f, s := displaced(t, after)
	got, err := s.ConfirmAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got, f
}

// F575 (раунд 1): Confirm и ConfirmEach — тот же вытесненный ответ.
func TestConfirm_DisplacedAnswer_NewerListConfirms(t *testing.T) {
	_, s := displaced(t, func(*FakeNDMS, *InterfaceStore) {})
	c, rec, ok, err := s.Confirm(context.Background(), "Wireguard3")
	if err != nil || !ok || c.Name() != "Wireguard3" || rec == nil || rec.ID != "Wireguard3" {
		t.Fatalf("c=%v rec=%#v ok=%v err=%v", c, rec, ok, err)
	}
}

func TestConfirm_DisplacedAnswer_DestroyedAfterNotConfirmed(t *testing.T) {
	_, s := displaced(t, destroyW3)
	if _, _, ok, err := s.Confirm(context.Background(), "Wireguard3"); ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestConfirmEach_DisplacedAnswer(t *testing.T) {
	_, s := displaced(t, func(*FakeNDMS, *InterfaceStore) {})
	got, err := s.ConfirmEach(context.Background(), []string{"Wireguard0", "Wireguard3", "Wireguard9"})
	if err != nil || len(got) != 2 || got["Wireguard3"].Name() != "Wireguard3" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	_, s = displaced(t, destroyW3)
	got, err = s.ConfirmEach(context.Background(), []string{"Wireguard0", "Wireguard3"})
	if _, ok := got["Wireguard3"]; ok || err != nil || len(got) != 1 {
		t.Fatalf("destroyed after the newer list: got=%v err=%v", got, err)
	}
}

// F575: свой ответ ConfirmAll вытеснен списком ReconcileDirty, начатым после
// него, — запись из применённого списка подтверждена.
func TestConfirmAll_DisplacedAnswer_NewerListConfirms(t *testing.T) {
	got, f := confirmAllDisplaced(t, func(*FakeNDMS, *InterfaceStore) {})
	if got["Wireguard3"].Name() != "Wireguard3" || got["Wireguard0"].Name() != "Wireguard0" {
		t.Fatalf("got=%v", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d Phantoms=%d", f.E, f.Phantoms)
	}
}

// Снятая хуком после нового списка — не подтверждена: доказательство не ослаблено.
func TestConfirmAll_DisplacedAnswer_DestroyedAfterNotConfirmed(t *testing.T) {
	got, _ := confirmAllDisplaced(t, destroyW3)
	if _, ok := got["Wireguard3"]; ok {
		t.Fatalf("destroyed after the newer list must not be confirmed: %v", got)
	}
	if got["Wireguard0"].Name() != "Wireguard0" {
		t.Fatalf("got=%v", got)
	}
}

// Запись, известная только по хуку (ни один список после вызова её не
// показал), не подтверждена.
func TestConfirmAll_HookOnlyNotConfirmed(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil {
		t.Fatal(err)
	}
	f.InList(func() {
		f.InList(nil)
		s.OnCreated("Wireguard3")
	})
	got, err := s.ConfirmAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["Wireguard3"]; ok || len(got) != 1 {
		t.Fatalf("got=%v", got)
	}
}

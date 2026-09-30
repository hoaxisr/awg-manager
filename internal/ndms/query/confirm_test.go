package query

import (
	"context"
	"errors"
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

// Список ушёл до сноса, ответ пришёл после ifdestroyed: имя в ответе есть,
// но хук новее — подтверждения нет.
func TestConfirm_DestroyedHookDuringList_NotConfirmed(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	_, _, ok := confirmInFlight(t, f, "Wireguard0", func() {}, func(s *InterfaceStore) { s.OnDestroyed("Wireguard0") })
	if ok {
		t.Fatal("destroyed after the list was sent must not be confirmed")
	}
}

// ifcreated пришёл, пока список в полёте, но в ответе имени нет (создан после
// ответа): свежий список его не доказывает — не подтверждено.
func TestConfirm_CreatedHookNotInList_NotConfirmed(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	_, rec, ok := confirmInFlight(t, f, "Wireguard3", func() {}, func(s *InterfaceStore) { s.OnCreated("Wireguard3") })
	if ok || rec != nil {
		t.Fatalf("name absent from the list must not be confirmed: ok=%v rec=%#v", ok, rec)
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
// список в полёте, ifcreated — после ответа. Layer-хук по незнакомому id —
// доказательство записи: имя ждёт в pending, и Confirm подтверждает по ответу.
// Раньше подтверждения не было — «импорт принят, но WireguardN нет в списке».
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
	if err := s.ReconcilePending(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "Wireguard1"); got == nil {
		t.Fatal("record must land in the map after ifcreated")
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d Phantoms=%d", f.E, f.Phantoms)
	}
}

// Layer/ip-хук по незнакомому id ставит его в pending, как ifcreated;
// ifdestroyed (Forget) снимает.
func TestHooks_UnknownIDPendingUntilDestroyed(t *testing.T) {
	for name, hook := range map[string]func(*InterfaceStore){
		"layer": func(s *InterfaceStore) { s.OnLayerChanged("Wireguard3", "ctrl", "") },
		"ip":    func(s *InterfaceStore) { s.OnIPChanged("Wireguard3", "10.0.0.1") },
	} {
		s := NewInterfaceStore(NewFakeNDMS(), NopLogger())
		hook(s)
		if !s.HasPending() {
			t.Fatalf("%s: unknown id must be pending", name)
		}
		s.OnDestroyed("Wireguard3")
		if s.HasPending() {
			t.Fatalf("%s: ifdestroyed must clear pending", name)
		}
	}
}

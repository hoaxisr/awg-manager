package query

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// actionStore — стор на тёплой карте с Wireguard0..2.
func actionStore(t *testing.T) (*FakeNDMS, *InterfaceStore) {
	t.Helper()
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard2", Type: "Wireguard"},
	)
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	return f, s
}

// F557: подтверждения одного действия — один полный список; вне действия
// каждое подтверждение читает свой.
func TestActionList_OneListPerAction(t *testing.T) {
	f, s := actionStore(t)
	confirmAll := func(ctx context.Context) {
		t.Helper()
		for _, n := range []string{"Wireguard0", "Wireguard1", "Wireguard2"} {
			if _, _, ok, err := s.Confirm(ctx, n); err != nil || !ok {
				t.Fatalf("Confirm %s: ok=%v err=%v", n, ok, err)
			}
		}
		got, err := s.ConfirmEach(ctx, []string{"Wireguard0", "Wireguard9"})
		if err != nil || len(got) != 1 {
			t.Fatalf("ConfirmEach: got=%v err=%v", got, err)
		}
	}
	lists := f.ListCalls()
	confirmAll(context.Background())
	if got := f.ListCalls() - lists; got != 4 {
		t.Fatalf("вне действия: %d списков, want 4", got)
	}
	lists = f.ListCalls()
	ctx := WithActionList(context.Background())
	confirmAll(ctx)
	confirmAll(WithActionList(ctx)) // вложенный вызов — то же действие
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("в действии: %d списков, want 1", got)
	}
	lists = f.ListCalls()
	confirmAll(WithActionList(context.Background()))
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("следующее действие: %d списков, want 1 свой", got)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d phantoms=%d", f.E, f.Phantoms)
	}
}

// Параллельные подтверждения одного действия ждут один запрос списка.
func TestActionList_ConcurrentConfirmsOneList(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	bg := &blockingGetter{Getter: f, entered: make(chan struct{}, 8)}
	s := NewInterfaceStore(bg, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	ctx := WithActionList(context.Background())
	lists := f.ListCalls()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
				errs <- errors.Join(err, errors.New("not confirmed"))
			}
		}()
	}
	bg.waitBlocked(t)
	time.Sleep(20 * time.Millisecond) // остальные успели встать в очередь
	close(bg.gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("%d списков, want 1 на всех", got)
	}
}

// Хук существования после списка действия противоречит ему — один повторный
// список (он становится списком действия), решение по нему: настоящее
// снятие и свой Forget (П7) — не подтверждено; устаревший ifdestroyed живого
// X — подтверждено, и повторный список кладёт X обратно в карту.
// Мутация: убрать перечитывание → ListCalls +0, снятое подтверждено.
func TestActionList_RemovedAfterListNotConfirmed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(f *FakeNDMS, s *InterfaceStore)
		want   bool
	}{
		{"real removal", func(f *FakeNDMS, s *InterfaceStore) {
			f.Remove("Wireguard1")
			for _, h := range f.HooksFor("Wireguard1") {
				s.OnDestroyed(h.ID)
			}
		}, false},
		{"stale ifdestroyed", func(_ *FakeNDMS, s *InterfaceStore) { s.OnDestroyed("Wireguard1") }, true},
		{"own Forget", func(f *FakeNDMS, s *InterfaceStore) {
			f.Remove("Wireguard1")
			_ = f.DrainHooks()
			s.Forget("Wireguard1")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := actionStore(t)
			ctx := WithActionList(context.Background())
			if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			tc.remove(f, s)
			lists := f.ListCalls()
			if _, _, ok, err := s.Confirm(ctx, "Wireguard1"); err != nil || ok != tc.want {
				t.Fatalf("ok=%v want %v err=%v", ok, tc.want, err)
			}
			if got := f.ListCalls() - lists; got != 1 {
				t.Fatalf("%d списков, want 1 повтор", got)
			}
			got, err := s.ConfirmEach(ctx, []string{"Wireguard1"})
			if _, ok := got["Wireguard1"]; err != nil || ok != tc.want {
				t.Fatalf("ConfirmEach: got=%v err=%v", got, err)
			}
			if n := f.ListCalls() - lists; n != 1 {
				t.Fatalf("ConfirmEach по повторному списку действия: %d списков, want 1", n)
			}
			if rec, _ := s.Get(ctx, "Wireguard1"); (rec != nil) != tc.want {
				t.Fatalf("карта после повтора: %#v", rec)
			}
			if noIfacePosts(f) != 0 || f.E != 0 {
				t.Fatalf("posts=%v E=%d", f.Posts, f.E)
			}
		})
	}
}

// M4′: повторный список заменяет список действия вместе с моментом его
// запроса — следующее подтверждение отсчитывает возраст от повтора.
// Мутация: не заменять al.at → возраст 2,5 с > actionListMaxAge, +1 список.
func TestConfirm_ActionList_RereadReplacesActionList(t *testing.T) {
	f, s := actionStore(t)
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	set := func(d time.Duration) { mu.Lock(); now = time.Unix(1000, 0).Add(d); mu.Unlock() }
	ctx := WithActionList(context.Background())
	if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok { // al.at = t0
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	set(time.Second)
	f.Remove("Wireguard1")
	for _, h := range f.HooksFor("Wireguard1") {
		s.OnDestroyed(h.ID)
	}
	lists := f.ListCalls()
	if _, _, ok, err := s.Confirm(ctx, "Wireguard1"); err != nil || ok { // повтор: al.at = t0+1 с
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("%d списков, want 1 повтор", got)
	}
	set(2500 * time.Millisecond)
	if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("список действия моложе 2 с не переиспользован: %d списков, want 1", got)
	}
}

// Ошибка чтения не запоминается: следующее подтверждение действия читает
// список заново.
func TestActionList_ErrorNotMemoized(t *testing.T) {
	f, s := actionStore(t)
	ctx := WithActionList(context.Background())
	f.FailList(errors.New("rci down"))
	if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err == nil || ok {
		t.Fatalf("непрочитанный список: ok=%v err=%v", ok, err)
	}
	f.FailList(nil)
	lists := f.ListCalls()
	if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
		t.Fatalf("после восстановления: ok=%v err=%v", ok, err)
	}
	// Подтверждено по своему новому списку, а не по применённому до отказа.
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("после отказа: %d списков, want 1", got)
	}
}

// Созданное внутри действия: ConfirmCreated читает свой список (записи нет в
// списке действия), и последующие подтверждения действия видят запись по
// применённому новее списку.
func TestActionList_CreatedInsideAction(t *testing.T) {
	f, s := actionStore(t)
	ctx := WithActionList(context.Background())
	if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	f.Add(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	lists := f.ListCalls()
	if c, err := s.ConfirmCreated(ctx, "Wireguard3", true); err != nil || c.Name() != "Wireguard3" {
		t.Fatalf("ConfirmCreated: c=%v err=%v", c, err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("ConfirmCreated: %d списков, want 1 свой", got)
	}
	if _, _, ok, err := s.Confirm(ctx, "Wireguard3"); err != nil || !ok {
		t.Fatalf("созданное в действии не подтверждено: ok=%v err=%v", ok, err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("после ConfirmCreated: %d списков, want 1", got)
	}
}

// R42 (F590): layer/ip-хук, опоздавший за ifdestroyed, не возвращает снятое
// имя в pending — ни список действия, взятый до сноса, ни свой список его
// не подтверждают, и ReconcilePending не будится.
func TestTomb_LateHookAfterDestroy_NotConfirmed(t *testing.T) {
	for _, withAction := range []bool{false, true} {
		for _, remove := range []string{"ifdestroyed", "Forget"} {
			t.Run(fmt.Sprintf("action=%v/%s", withAction, remove), func(t *testing.T) {
				f, s := actionStore(t)
				ctx := context.Background()
				if withAction {
					ctx = WithActionList(ctx)
					if _, _, ok, err := s.Confirm(ctx, "Wireguard1"); err != nil || !ok {
						t.Fatalf("до сноса: ok=%v err=%v", ok, err)
					}
				}
				f.Remove("Wireguard1")
				_ = f.DrainHooks()
				if remove == "Forget" {
					s.Forget("Wireguard1")
				} else {
					s.OnDestroyed("Wireguard1")
				}
				s.OnLayerChanged("Wireguard1", "link", "disabled")
				s.OnIPChanged("Wireguard1", "")
				if s.HasPending() {
					t.Fatal("поздний хук положил снятое имя в pending")
				}
				if _, _, ok, err := s.Confirm(ctx, "Wireguard1"); err != nil || ok {
					t.Fatalf("снятое подтверждено: ok=%v err=%v", ok, err)
				}
			})
		}
	}
}

// Создание на стенде: iflayerchanged ×2 раньше ifcreated. id без надгробия —
// хуки в pending, как раньше; Confirm подтверждает.
func TestTomb_CreateOrderLayerBeforeCreated_Confirmed(t *testing.T) {
	f, s := actionStore(t)
	f.Add(ndms.Interface{ID: "Wireguard5", Type: "Wireguard"})
	_ = f.DrainHooks()
	s.OnLayerChanged("Wireguard5", "ctrl", "running")
	s.OnLayerChanged("Wireguard5", "ctrl", "running")
	if !s.HasPending() {
		t.Fatal("хук создания до ifcreated не попал в pending")
	}
	s.OnCreated("Wireguard5")
	if _, _, ok, err := s.Confirm(context.Background(), "Wireguard5"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

// Снос, затем создание заново (тот же индекс): ifcreated снимает надгробие —
// подтверждено; дальше layer-хуки снова правят запись.
func TestTomb_DestroyThenRecreate_Confirmed(t *testing.T) {
	f, s := actionStore(t)
	f.Remove("Wireguard1")
	s.OnDestroyed("Wireguard1")
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	_ = f.DrainHooks()
	s.OnLayerChanged("Wireguard1", "ctrl", "running") // ×2 раньше ifcreated
	s.OnLayerChanged("Wireguard1", "ctrl", "running")
	s.OnCreated("Wireguard1")
	if !s.HasPending() {
		t.Fatal("ifcreated не положил пересозданное имя в pending")
	}
	if _, ok := s.tombs["Wireguard1"]; ok {
		t.Fatal("ifcreated не снял надгробие: хуки до списка терялись бы")
	}
	if _, _, ok, err := s.Confirm(context.Background(), "Wireguard1"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	s.OnIPChanged("Wireguard1", "10.0.0.9/32")
	if rec, _ := s.Get(context.Background(), "Wireguard1"); rec == nil || rec.Address != "10.0.0.9/32" {
		t.Fatalf("хук по пересозданному не применён: %#v", rec)
	}
}

// ifcreated потерян: надгробие снимает список, начатый после сноса и
// содержащий id; список, начатый до сноса, — нет.
func TestTomb_LiftedByLaterListOnly(t *testing.T) {
	f, s := actionStore(t)
	s.OnDestroyed("Wireguard1") // запись в NDMS жива: список её содержит
	if _, ok := s.tombs["Wireguard1"]; !ok {
		t.Fatal("надгробия нет")
	}
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.tombs["Wireguard1"]; ok {
		t.Fatal("список после сноса с записью не снял надгробие")
	}

	// Список, начатый ДО сноса: надгробие остаётся.
	bg := &blockingGetter{Getter: f, entered: make(chan struct{}, 1)}
	s2 := NewInterfaceStore(bg, NopLogger())
	if _, err := s2.Get(context.Background(), "Wireguard0"); err != nil {
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- s2.Refresh(context.Background()) }()
	bg.waitBlocked(t)
	s2.OnDestroyed("Wireguard1")
	close(bg.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.tombs["Wireguard1"]; !ok {
		t.Fatal("список, начатый до сноса, снял надгробие")
	}
}

// Надгробий не больше maxTombs; вытесняется старейшее.
func TestTomb_Bounded(t *testing.T) {
	_, s := actionStore(t)
	for i := range maxTombs + 10 {
		s.Forget(fmt.Sprintf("Gone%d", i))
		s.flights++ // каждое следующее новее
	}
	if len(s.tombs) != maxTombs {
		t.Fatalf("надгробий %d, want %d", len(s.tombs), maxTombs)
	}
	if _, ok := s.tombs["Gone0"]; ok {
		t.Fatal("старейшее не вытеснено")
	}
	if _, ok := s.tombs[fmt.Sprintf("Gone%d", maxTombs+9)]; !ok {
		t.Fatal("новейшее вытеснено")
	}
}

// R43: список действия живёт не дольше actionListMaxAge от НАЧАЛА запроса;
// старше — следующее подтверждение читает новый, и он становится списком
// действия.
func TestActionList_AgeCapFromRequestStart(t *testing.T) {
	f, s := actionStore(t)
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	s.now = clock
	confirm := func(ctx context.Context) {
		t.Helper()
		if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	}
	ctx := WithActionList(context.Background())
	f.InList(func() { advance(1500 * time.Millisecond) }) // запрос идёт 1,5 с
	lists := f.ListCalls()
	confirm(ctx)
	f.InList(nil)
	advance(400 * time.Millisecond) // 1,9 с от начала запроса
	confirm(ctx)
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("в пределах 2 с: %d списков, want 1", got)
	}
	advance(200 * time.Millisecond) // 2,1 с от начала, 0,6 с от ответа
	confirm(ctx)
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("старше 2 с от начала запроса: %d списков, want 2", got)
	}
	advance(time.Second) // новый список — список действия
	confirm(ctx)
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("новый список действия не переиспользован: %d списков, want 2", got)
	}
}

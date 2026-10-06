package query

import (
	"context"
	"errors"
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
			s.ExpectRemoval("Wireguard1").Removed()
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

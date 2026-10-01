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

// Список действия не подтверждает снятое после него: хуком ifdestroyed,
// нашим Forget и новым применённым списком без записи.
func TestActionList_RemovedAfterListNotConfirmed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(f *FakeNDMS, s *InterfaceStore)
	}{
		{"ifdestroyed", func(_ *FakeNDMS, s *InterfaceStore) { s.OnDestroyed("Wireguard1") }},
		{"Forget", func(_ *FakeNDMS, s *InterfaceStore) { s.Forget("Wireguard1") }},
		{"newer list", func(f *FakeNDMS, s *InterfaceStore) {
			f.Remove("Wireguard1")
			_ = f.DrainHooks() // хук не доставлен — снятие видит только список
			if err := s.Refresh(context.Background()); err != nil {
				panic(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s := actionStore(t)
			ctx := WithActionList(context.Background())
			if _, _, ok, err := s.Confirm(ctx, "Wireguard0"); err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			tc.remove(f, s)
			if _, _, ok, err := s.Confirm(ctx, "Wireguard1"); err != nil || ok {
				t.Fatalf("снятое после списка подтверждено: ok=%v err=%v", ok, err)
			}
			if got, err := s.ConfirmEach(ctx, []string{"Wireguard1"}); err != nil || len(got) != 0 {
				t.Fatalf("ConfirmEach: got=%v err=%v", got, err)
			}
		})
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
	if c, err := s.ConfirmCreated(ctx, "Wireguard3"); err != nil || c.Name() != "Wireguard3" {
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

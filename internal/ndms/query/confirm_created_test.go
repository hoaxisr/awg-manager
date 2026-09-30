package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// importLate — стор на тёплой карте и импорт WireguardN в оракуле, чья
// запись видна в списке с задержкой hide (FakeNDMS.HideCreated).
func importLate(t *testing.T, hide int, backoff ...time.Duration) (*FakeNDMS, *InterfaceStore) {
	t.Helper()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	s.SetCreatedBackoff(backoff...)
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	f.HideCreated(hide)
	if _, err := f.Post(context.Background(), map[string]any{"interface": map[string]any{"wireguard": map[string]any{"import": "x"}}}); err != nil {
		t.Fatal(err)
	}
	return f, s
}

// F584: запись появляется в списке на третьем чтении — подтверждено третьим
// своим списком, без чтений по имени.
func TestConfirmCreated_LateAfterLists_Confirms(t *testing.T) {
	f, s := importLate(t, 2, time.Millisecond, time.Millisecond, time.Millisecond)
	lists := f.ListCalls()
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1")
	if err != nil || c.Name() != "Wireguard1" {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.ListCalls()-lists != 3 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("lists=%d E=%d phantoms=%d", f.ListCalls()-lists, f.E, f.Phantoms)
	}
}

// Стенд 30.09: запись в списке появляется к ifcreated. Хуки доставлены после
// первого (пустого) списка, ReconcilePending читает список — он и будит
// ConfirmCreated: своего второго списка нет, паузу 5 с не ждёт.
func TestConfirmCreated_WokenByReconcileList(t *testing.T) {
	f, s := importLate(t, -1, 5*time.Second)
	ctx := context.Background()
	lists := f.ListCalls()
	firstListed := make(chan struct{})
	f.InList(func() {
		f.InList(nil)
		close(firstListed)
	})
	type res struct {
		c   Confirmed
		err error
	}
	done := make(chan res, 1)
	start := time.Now()
	go func() {
		c, err := s.ConfirmCreated(ctx, "Wireguard1")
		done <- res{c, err}
	}()
	<-firstListed
	for _, h := range f.DrainHooks() { // ctrl ×2, ifcreated; запись видна с этого момента
		switch h.Type {
		case "iflayerchanged":
			s.OnLayerChanged(h.ID, h.Layer, h.Level)
		case "ifcreated":
			s.OnCreated(h.ID)
		}
	}
	if err := s.ReconcilePending(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil || r.c.Name() != "Wireguard1" {
		t.Fatalf("c=%v err=%v", r.c, r.err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("ждал паузу, а не список ReconcilePending: %v", el)
	}
	if f.ListCalls()-lists != 2 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("lists=%d (ждали свой + ReconcilePending) E=%d phantoms=%d", f.ListCalls()-lists, f.E, f.Phantoms)
	}
}

// layerHooksInFirstList — хуки слоя по созданному доставляются, пока первый
// список ConfirmCreated в полёте (след «NDMS знает запись», F584). ifcreated
// не отдаётся — запись, скрытая до него, так и остаётся скрытой.
func layerHooksInFirstList(f *FakeNDMS, s *InterfaceStore) {
	hooks := f.DrainHooks()
	f.InList(func() {
		f.InList(nil)
		for _, h := range hooks {
			if h.Type == "iflayerchanged" {
				s.OnLayerChanged(h.ID, h.Layer, h.Level)
			}
		}
	})
}

// След есть (хуки слоя), а в списках записи нет за все попытки —
// ErrNotListed после 1+len(backoff) списков.
func TestConfirmCreated_NeverListed_ErrNotListed(t *testing.T) {
	f, s := importLate(t, 100, time.Millisecond, time.Millisecond)
	layerHooksInFirstList(f, s)
	lists := f.ListCalls()
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1")
	if !errors.Is(err, ErrNotListed) || c != (Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.ListCalls()-lists != 3 || f.E != 0 {
		t.Fatalf("lists=%d E=%d", f.ListCalls()-lists, f.E)
	}
}

// Ни списка, ни хуков — ErrNotSeen: знает ли NDMS запись, неизвестно.
func TestConfirmCreated_NoTrace_ErrNotSeen(t *testing.T) {
	f, s := importLate(t, -1, time.Millisecond, time.Millisecond)
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1")
	if !errors.Is(err, ErrNotSeen) || errors.Is(err, ErrNotListed) || c != (Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// ifdestroyed по имени во время ожидания (после хуков создания) —
// ErrCreatedThenRemoved сразу, паузу 5 с не ждёт.
func TestConfirmCreated_DestroyedDuringWait(t *testing.T) {
	f, s := importLate(t, 100, 5*time.Second)
	firstListed := make(chan struct{})
	f.InList(func() {
		f.InList(nil)
		close(firstListed)
	})
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := s.ConfirmCreated(context.Background(), "Wireguard1")
		done <- err
	}()
	<-firstListed
	s.OnLayerChanged("Wireguard1", "ctrl", "")
	f.Remove("Wireguard1")
	s.OnDestroyed("Wireguard1")
	err := <-done
	if !errors.Is(err, ErrCreatedThenRemoved) || errors.Is(err, ErrNotListed) {
		t.Fatalf("err=%v", err)
	}
	if el := time.Since(start); el > time.Second || f.E != 0 {
		t.Fatalf("elapsed=%v E=%d", el, f.E)
	}
}

// Список не прочитан — ошибка сразу, не ErrNotListed (решение 4): повторов нет.
func TestConfirmCreated_ListError_NoRetry(t *testing.T) {
	f, s := importLate(t, 0, time.Millisecond, time.Millisecond)
	f.FailList(errors.New("rci down"))
	lists := f.ListCalls()
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1")
	if err == nil || errors.Is(err, ErrNotListed) || c != (Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.ListCalls()-lists != 1 {
		t.Fatalf("lists=%d", f.ListCalls()-lists)
	}
}

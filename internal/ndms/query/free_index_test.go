package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// freeIndexStore — стор на тёплой карте с одним Wireguard0.
func freeIndexStore(t *testing.T) (*FakeNDMS, *InterfaceStore) {
	t.Helper()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	return f, s
}

// F574: чужой Wireguard1 создан в NDMS, хук не доставлен — память его не
// знает. Выбор — по своему свежему списку: индекс 1 пропущен.
func TestFreeIndex_ForeignNotInCache_Skipped(t *testing.T) {
	f, s := freeIndexStore(t)
	f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
	lists := f.ListCalls()
	idx, ok, err := s.FreeIndex(context.Background(), "Wireguard", 100, map[int]bool{2: true})
	if err != nil || !ok || idx != 3 {
		t.Fatalf("idx=%d ok=%v err=%v, want 3 (0,1 в NDMS, 2 reserved)", idx, ok, err)
	}
	if f.ListCalls()-lists != 1 || f.E != 0 {
		t.Fatalf("lists=%d E=%d", f.ListCalls()-lists, f.E)
	}
}

// S8a: метка ifcreated имя не занимает — запись снята до вызова (устаревший
// хук), в NDMS её нет: индекс 1 выбран по списку.
// Мутация: занимать имя по метке ifcreated → 1 пропущен, красный.
func TestFreeIndex_StaleCreated_Chosen(t *testing.T) {
	f, s := freeIndexStore(t)
	s.OnCreated("Wireguard1")
	idx, ok, err := s.FreeIndex(context.Background(), "Wireguard", 100, nil)
	if err != nil || !ok || idx != 1 {
		t.Fatalf("idx=%d ok=%v err=%v, want 1", idx, ok, err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// Список не прочитан — ошибка, по памяти не выбираем (решение 4).
func TestFreeIndex_ListError(t *testing.T) {
	f, s := freeIndexStore(t)
	f.FailList(errors.New("rci down"))
	if idx, ok, err := s.FreeIndex(context.Background(), "Wireguard", 100, nil); err == nil || ok {
		t.Fatalf("idx=%d ok=%v err=%v", idx, ok, err)
	}
}

// Все заняты — ok=false без ошибки.
func TestFreeIndex_Full(t *testing.T) {
	_, s := freeIndexStore(t)
	if _, ok, err := s.FreeIndex(context.Background(), "Wireguard", 1, nil); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

// S8b: чужой Wireguard1 создан, пока список FreeIndex в полёте, ifcreated
// доставлен там же: метка новее начала списка противоречит ответу без него —
// повтор, индекс 1 занят. Мутация: убрать перечитывание → ListCalls +1
// (индекс 1 до 51c держит ещё и pending).
func TestFreeIndex_ForeignCreatedInFlight_Reread(t *testing.T) {
	f, s := freeIndexStore(t)
	f.InList(func() {
		f.InList(nil)
		f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"})
		for _, h := range f.HooksFor("Wireguard1") {
			if h.Type == "ifcreated" {
				s.OnCreated(h.ID)
			}
		}
	})
	lists := f.ListCalls()
	idx, ok, err := s.FreeIndex(context.Background(), "Wireguard", 100, nil)
	if err != nil || !ok || idx != 2 {
		t.Fatalf("idx=%d ok=%v err=%v, want 2", idx, ok, err)
	}
	if got := f.ListCalls() - lists; got != 2 {
		t.Fatalf("%d списков, want 2", got)
	}
}

// Имя, которое ждёт ConfirmCreated (NDMS ответил «создано», в списке записи
// ещё нет), занято: FreeIndex, начатый во время паузы, его пропускает.
// Мутация: убрать creating из памяти FreeIndex → выбран 1.
func TestFreeIndex_CreatingTaken(t *testing.T) {
	f, s := importLate(t, 100, 5*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	firstListed := make(chan struct{})
	f.InList(func() {
		f.InList(nil)
		close(firstListed)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.ConfirmCreated(ctx, "Wireguard1", true)
	}()
	<-firstListed
	idx, ok, err := s.FreeIndex(context.Background(), "Wireguard", 100, nil)
	cancel()
	<-done
	if err != nil || !ok || idx != 2 {
		t.Fatalf("idx=%d ok=%v err=%v, want 2 (1 ждёт ConfirmCreated)", idx, ok, err)
	}
}

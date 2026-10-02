package query

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// blockingGetter держит запрос списка, пока тест не откроет gate: так хук приходит, когда запрос уже ушёл, а ответ
// ещё не применён. gate == nil — ничего не блокируется (бутстрап).
type blockingGetter struct {
	Getter
	gate    chan struct{}
	entered chan struct{}
}

func (b *blockingGetter) Get(ctx context.Context, path string, dst any) error {
	if path == ifaceListPath && b.gate != nil {
		b.entered <- struct{}{}
		<-b.gate
	}
	return b.Getter.Get(ctx, path, dst)
}

func (b *blockingGetter) waitBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("list request did not reach the getter")
	}
}

// startListInFlight бутстрапит стор и запускает InvalidateAll, который
// останавливается на запросе списка. Возвращает стор, геттер и канал
// завершения InvalidateAll.
func startListInFlight(t *testing.T) (*InterfaceStore, *blockingGetter, chan struct{}) {
	t.Helper()
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList) // Wireguard0, Bridge0
	bg := &blockingGetter{Getter: fg, entered: make(chan struct{}, 1)}
	s := NewInterfaceStore(bg, NopLogger())
	if _, err := s.Get(context.Background(), "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	bg.gate = make(chan struct{})
	done := make(chan struct{})
	go func() { s.InvalidateAll(); close(done) }() // список ушёл ДО хука
	bg.waitBlocked(t)
	return s, bg, done
}

// ifdestroyed, пришедший, пока список в полёте, — подсказка: запись из ответа
// кладётся, а следующий список (ReconcileDirty) её снимает. Наш Forget в
// полёте — нет: список, начатый до сноса, запись не воскрешает (S12).
func TestInterfaceStore_ListRace_DoesNotResurrectDestroyed(t *testing.T) {
	ctx := context.Background()
	t.Run("OnDestroyed", func(t *testing.T) {
		s, bg, done := startListInFlight(t)
		s.OnDestroyed("Wireguard0") // хук пришёл, пока список в полёте
		close(bg.gate)
		<-done
		s.mu.RLock()
		_, inMap := s.byID["Wireguard0"]
		s.mu.RUnlock()
		if !inMap {
			t.Fatal("хук снял запись из ответа: хук — подсказка, не решение")
		}
		bg.gate = nil
		bg.Getter.(*FakeGetter).SetJSON(ifaceListPath, `{"Bridge0":{"id":"Bridge0","type":"Bridge"}}`)
		if err := s.ReconcileDirty(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
			t.Fatalf("после списка без записи она осталась: %#v", got)
		}
	})
	t.Run("Forget", func(t *testing.T) {
		s, bg, done := startListInFlight(t)
		s.Forget("Wireguard0") // наш `no interface` прошёл, пока список в полёте
		close(bg.gate)
		<-done
		if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
			t.Fatalf("destroyed Wireguard0 resurrected by a stale list: %#v", got)
		}
		if got, _ := s.Get(ctx, "Bridge0"); got == nil {
			t.Fatal("untouched Bridge0 must be applied from the list")
		}
	})
}

// П1: список — истина для карты; layer-хук, пришедший, пока список в полёте,
// ответ не перекрывает.
// Мутация: вернуть правило «touched > start» → ConfLayer = disabled, красный.
func TestApplyList_ListIsTruth_NoHookVeto(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", ConfLayer: "running"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	f.InList(func() {
		f.InList(nil)
		s.OnLayerChanged("Wireguard0", "conf", "disabled")
	})
	if _, err := s.Snapshot(ctx, SnapshotLive); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "Wireguard0")
	if err != nil || got == nil || got.ConfLayer != "running" {
		t.Fatalf("Get = %#v, %v; want conf running из списка", got, err)
	}
}

// S12 + L3′: страж воскрешения — id, которого нет в карте и который снят
// меткой новее начала списка, не кладётся.
// Мутации: убрать страж → (1) и (3) красные; страж и для id в карте → (2)
// красный (запись не обновлена списком).
func TestApplyList_ResurrectionGuard_OwnForget(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*FakeNDMS, *InterfaceStore) {
		t.Helper()
		f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: "old"})
		s := NewInterfaceStore(f, NopLogger())
		if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
			t.Fatal(err)
		}
		return f, s
	}
	t.Run("own Forget, X in map", func(t *testing.T) {
		f, s := setup(t)
		f.InList(func() {
			f.InList(nil)
			f.Remove("Wireguard0")
			s.Forget("Wireguard0")
		})
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
			t.Fatalf("список, начатый до сноса, воскресил запись: %#v", got)
		}
	})
	t.Run("OnDestroyed, X in map", func(t *testing.T) {
		f, s := setup(t)
		f.Add(ndms.Interface{ID: "Wireguard0", Type: "Wireguard", Description: "new"})
		f.InList(func() {
			f.InList(nil)
			s.OnDestroyed("Wireguard0") // устаревший: запись жива
		})
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		s.mu.RLock()
		rec := s.byID["Wireguard0"]
		s.mu.RUnlock()
		if rec == nil || rec.Description != "new" {
			t.Fatalf("известный id не взят из списка: %#v", rec)
		}
	})
	t.Run("stale OnDestroyed, X not in map", func(t *testing.T) {
		f, s := setup(t)
		f.Add(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"}) // чужой, хук не доставлен
		f.InList(func() {
			f.InList(nil)
			s.OnDestroyed("Wireguard1") // устаревший ifdestroyed прежнего Wireguard1
		})
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.Get(ctx, "Wireguard1"); got != nil {
			t.Fatalf("страж не сработал: %#v", got)
		}
		s.OnCreated("Wireguard1")
		if got, _ := s.Get(ctx, "Wireguard1"); got == nil {
			t.Fatal("после ifcreated и списка записи нет")
		}
	})
}

// ifdestroyed карту не меняет: запись есть, «грязно»; ReconcileDirty — один
// список, по нему записи нет.
// Мутация: вернуть Forget в OnDestroyed → «запись есть» красный.
func TestOnDestroyed_MapUnchanged(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	f.Remove("Wireguard0")
	s.OnDestroyed("Wireguard0")
	s.mu.RLock()
	_, inMap := s.byID["Wireguard0"]
	dirty := s.dirtyAt
	s.mu.RUnlock()
	if !inMap || dirty == 0 {
		t.Fatalf("после ifdestroyed: в карте=%v dirtyAt=%d; want true, ≠0", inMap, dirty)
	}
	lists := f.ListCalls()
	if err := s.ReconcileDirty(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.ListCalls() - lists; got != 1 {
		t.Fatalf("ReconcileDirty: %d списков, want 1", got)
	}
	if got, _ := s.Get(ctx, "Wireguard0"); got != nil {
		t.Fatalf("после списка запись есть: %#v", got)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// П6′: хук, совпавший с картой, не стоит списка; расходящийся — один.
// Мутация: «грязно» на любом хуке существования → красный на первых двух.
// Подслучай Forget — сторож (зелёный и до 51c), в счёт мутаций не идёт.
func TestHooks_MatchingMap_NoDirty(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		hook  func(f *FakeNDMS, s *InterfaceStore)
		lists int
	}{
		{"known OnCreated", func(_ *FakeNDMS, s *InterfaceStore) { s.OnCreated("Wireguard0") }, 0},
		{"unknown OnDestroyed", func(_ *FakeNDMS, s *InterfaceStore) { s.OnDestroyed("Nope") }, 0},
		{"unknown OnCreated", func(f *FakeNDMS, s *InterfaceStore) {
			f.Add(ndms.Interface{ID: "Wireguard5", Type: "Wireguard"})
			s.OnCreated("Wireguard5")
		}, 1},
		{"known OnDestroyed", func(_ *FakeNDMS, s *InterfaceStore) { s.OnDestroyed("Wireguard0") }, 1},
		{"known Forget", func(_ *FakeNDMS, s *InterfaceStore) { s.Forget("Wireguard0") }, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
			s := NewInterfaceStore(f, NopLogger())
			if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
				t.Fatal(err)
			}
			lists := f.ListCalls()
			c.hook(f, s)
			s.mu.RLock()
			dirty := s.dirtyAt != 0
			s.mu.RUnlock()
			if dirty != (c.lists > 0) {
				t.Fatalf("dirty=%v, want %v", dirty, c.lists > 0)
			}
			if _, err := s.Get(ctx, "Wireguard0"); err != nil {
				t.Fatal(err)
			}
			if got := f.ListCalls() - lists; got != c.lists {
				t.Fatalf("Get: %d списков, want %d", got, c.lists)
			}
		})
	}
}

// П6: id, снятый, пока список в полёте, резолвер имён не спрашивает.
// Мутация: не пропускать в unnamedLocked → system-name по снятому, E == 1.
func TestResolver_SkipsGoneDuringFlight(t *testing.T) {
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "UsbQmi0", Type: "UsbQmi"},
	)
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	s.mu.Lock()
	delete(s.sysNames, "UsbQmi0") // имени ядра не знает никто
	s.mu.Unlock()
	posts := len(f.Posts)
	f.InList(func() {
		f.InList(nil)
		f.Remove("UsbQmi0")
		s.OnDestroyed("UsbQmi0")
	})
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.Posts[posts:] {
		if strings.Contains(p, "system-name") && strings.Contains(p, "UsbQmi0") {
			t.Fatalf("резолвер спросил снятый id: %s", p)
		}
	}
	if f.E != 0 {
		t.Fatalf("E=%d, want 0", f.E)
	}
}

// exist ограничен: применённый список снимает метки не новее своего начала;
// метка, поставленная, пока список в полёте, остаётся.
// Мутации: не снимать → len(exist) = 3, красный; снимать все → метки
// Wireguard0 из полёта нет, красный.
func TestExist_PrunedByAppliedList(t *testing.T) {
	f := NewFakeNDMS(
		ndms.Interface{ID: "Wireguard0", Type: "Wireguard"},
		ndms.Interface{ID: "Wireguard1", Type: "Wireguard"},
	)
	s := NewInterfaceStore(f, NopLogger())
	ctx := context.Background()
	if _, err := s.Get(ctx, "Wireguard0"); err != nil { // bootstrap
		t.Fatal(err)
	}
	s.OnCreated("Wireguard0")
	s.OnDestroyed("Nope")
	f.Remove("Wireguard1")
	s.Forget("Wireguard1")
	f.InList(func() {
		f.InList(nil)
		s.OnDestroyed("Wireguard0") // в полёте: новее начала списка
	})
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.exist["Wireguard0"]; len(s.exist) != 1 || !ok {
		t.Fatalf("exist = %v, want только метка Wireguard0 из полёта", s.exist)
	}
}

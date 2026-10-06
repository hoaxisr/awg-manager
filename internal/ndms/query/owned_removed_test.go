package query

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// ownedClock — подменные часы стора (s.now) для TTL метки removed.
type ownedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *ownedClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *ownedClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// ownedStore — тёплый стор над оракулом с Wireguard0 и OpkgTun10 и подменными часами.
func ownedStore(t *testing.T) (*FakeNDMS, *InterfaceStore, *ownedClock) {
	t.Helper()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"}, ndms.Interface{ID: "OpkgTun10", Type: "OpkgTun"})
	s := warmStore(t, f)
	clk := &ownedClock{t: time.Unix(1000, 0)}
	s.now = clk.now
	f.DrainHooks()
	return f, s, clk
}

func dirtyAt(s *InterfaceStore) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirtyAt
}

// П14: owned — «создано нашим доказанным created, до снятия»; свой ifcreated
// не расходится с картой и не публикуется.
func TestOwned_Lifecycle(t *testing.T) {
	ctx := context.Background()

	// (1) созданное нашим created и подтверждённое — своё, «грязно» нет.
	// (2) Forget снимает владение: следующий ifcreated того же имени — чужое
	// воплощение. Мутация «не снимать owned в Forget» → own=true, красный.
	t.Run("confirmed then forgotten", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		f.Add(ndms.Interface{ID: "OpkgTun1", Type: "OpkgTun"})
		if _, err := s.ConfirmCreated(ctx, "OpkgTun1", true); err != nil {
			t.Fatal(err)
		}
		if own, _ := s.OnCreated("OpkgTun1"); !own || dirtyAt(s) != 0 {
			t.Fatalf("(1) own=%v dirtyAt=%d, want true/0", own, dirtyAt(s))
		}
		f.Remove("OpkgTun1")
		s.ExpectRemoval("OpkgTun1").Removed()
		if own, _ := s.OnCreated("OpkgTun1"); own || dirtyAt(s) == 0 {
			t.Fatalf("(2) после Forget: own=%v dirtyAt=%d, want false/≠0", own, dirtyAt(s))
		}
	})

	// (3) ConfirmCreated с created, но записи нет ни в одном списке
	// (ErrNotListed) — владение снимает ошибка. Мутация «не снимать при
	// ошибке» → own=true, красный.
	t.Run("not listed", func(t *testing.T) {
		_, s := importLate(t, 100, time.Millisecond, time.Millisecond)
		if _, err := s.ConfirmCreated(ctx, "Wireguard1", true); err == nil {
			t.Fatal("want ErrNotListed")
		}
		if own, _ := s.OnCreated("Wireguard1"); own {
			t.Fatal("(3) own=true после ErrNotListed")
		}
	})

	// (4) запись ушла из карты списком — владение снято (D6.1: по циклу
	// удаления из byID, не по обходу owned).
	t.Run("gone by list", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		f.Add(ndms.Interface{ID: "OpkgTun3", Type: "OpkgTun"})
		if _, err := s.ConfirmCreated(ctx, "OpkgTun3", true); err != nil {
			t.Fatal(err)
		}
		f.Remove("OpkgTun3")
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		if own, _ := s.OnCreated("OpkgTun3"); own {
			t.Fatal("(4) own=true после ухода записи из списка")
		}
	})

	// (6) запись появилась в списке не сразу (F584): список ожидания без неё
	// владение не снимает. Мутация «снимать обходом owned по отсутствию в
	// ответе, а не циклом удаления из byID» (D6.1) → первый список снял бы
	// владение, own=false, красный.
	t.Run("late listed", func(t *testing.T) {
		_, s := importLate(t, 1, time.Millisecond)
		if _, err := s.ConfirmCreated(ctx, "Wireguard1", true); err != nil {
			t.Fatal(err)
		}
		if own, _ := s.OnCreated("Wireguard1"); !own {
			t.Fatal("(6) own=false: владение снято списком ожидания без записи")
		}
	})

	// (5) created=false (existingOK) — запись не наша по доказательству,
	// владения нет (D6.2). Мутация «ставить при created=false» → красный.
	t.Run("existing ok", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		f.Add(ndms.Interface{ID: "OpkgTun4", Type: "OpkgTun"})
		if _, err := s.ConfirmCreated(ctx, "OpkgTun4", false); err != nil {
			t.Fatal(err)
		}
		if own, _ := s.OnCreated("OpkgTun4"); own {
			t.Fatal("(5) own=true при created=false")
		}
	})
}

// П20: removed — «снято нашим успешным `no interface`»: метка непотребляемая,
// живёт ≤ removedProofTTL и снимается любым новым воплощением имени.
func TestRemoved_Lifecycle(t *testing.T) {
	ctx := context.Background()
	const x = "OpkgTun10"

	// (1) свой Forget — свой ifdestroyed: ни «грязно», ни списка. Мутация «не
	// записывать в Forget» → own=false, красный.
	// (2) старше TTL — уже не доказательство. Мутация «без TTL» → красный.
	t.Run("forget and ttl", func(t *testing.T) {
		f, s, clk := ownedStore(t)
		f.Remove(x)
		s.ExpectRemoval(x).Removed()
		lists := f.ListCalls()
		if own := s.OnDestroyed(x); !own || !s.RemovedByUs(x) || dirtyAt(s) != 0 {
			t.Fatalf("(1) own=%v RemovedByUs=%v dirtyAt=%d, want true/true/0", own, s.RemovedByUs(x), dirtyAt(s))
		}
		if err := s.ReconcileDirty(ctx); err != nil || f.ListCalls() != lists {
			t.Fatalf("(1) списков +%d err=%v, want 0", f.ListCalls()-lists, err)
		}
		clk.add(46 * time.Second)
		if own := s.OnDestroyed(x); own || s.RemovedByUs(x) {
			t.Fatalf("(2) через 46 с: own=%v RemovedByUs=%v, want false/false", own, s.RemovedByUs(x))
		}
	})

	// (3) T5: ifcreated имени, снятого нами, двусмыслен — verify, метку не
	// снимает; решает список. Запись в списке — чужое воплощение, метка снята;
	// записи нет — хук наш опоздавший, метка жива, свой ifdestroyed за ним —
	// свой. Мутации: «снимать метку в OnCreated» → «нет записи» own=false,
	// красный; «verify=false» → красный.
	t.Run("ifcreated after forget", func(t *testing.T) {
		for _, recreated := range []bool{true, false} {
			f, s, _ := ownedStore(t)
			f.Remove(x)
			s.ExpectRemoval(x).Removed()
			if recreated {
				f.Add(ndms.Interface{ID: x, Type: "OpkgTun"})
			}
			if own, verify := s.OnCreated(x); own || !verify || dirtyAt(s) == 0 {
				t.Fatalf("(3) recreated=%v: own=%v verify=%v dirtyAt=%d, want false/true/≠0", recreated, own, verify, dirtyAt(s))
			}
			if err := s.ReconcileDirty(ctx); err != nil {
				t.Fatal(err)
			}
			if s.Shown(x) != recreated {
				t.Fatalf("(3) recreated=%v: Shown=%v", recreated, s.Shown(x))
			}
			f.Remove(x)
			if own := s.OnDestroyed(x); own == recreated {
				t.Fatalf("(3) recreated=%v: ifdestroyed own=%v", recreated, own)
			}
		}
	})

	// (4) новое воплощение по списку снимает метку. Мутация «не снимать в
	// applyListLocked» → own=true, красный.
	t.Run("reborn by list", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		f.Remove(x)
		s.ExpectRemoval(x).Removed()
		f.Add(ndms.Interface{ID: x, Type: "OpkgTun"})
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		f.Remove(x)
		if own := s.OnDestroyed(x); own {
			t.Fatal("(4) ifdestroyed воплощения, показанного списком, принят за своё")
		}
	})

	// (5) своё новое создание снимает метку на входе ConfirmCreated: устаревший
	// ifdestroyed, дошедший пока ждём список, — не «снято нами». Мутация «не
	// снимать во входе ConfirmCreated» → own=true в полёте, красный (список
	// подтверждения снял бы метку лишь после).
	t.Run("reborn by own create", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		f.Remove(x)
		s.ExpectRemoval(x).Removed()
		f.Add(ndms.Interface{ID: x, Type: "OpkgTun"})
		var once sync.Once
		inFlight := true
		f.InList(func() { once.Do(func() { inFlight = s.OnDestroyed(x) }) })
		if _, err := s.ConfirmCreated(ctx, x, true); err != nil {
			t.Fatal(err)
		}
		f.InList(nil)
		if inFlight {
			t.Fatal("(5) ifdestroyed в полёте ConfirmCreated принят за своё снятие")
		}
		if own := s.OnDestroyed(x); own || dirtyAt(s) == 0 {
			t.Fatalf("(5) после ConfirmCreated: own=%v dirtyAt=%d, want false/≠0", own, dirtyAt(s))
		}
	})

	// (6) метка не потребляется: повтор ifdestroyed (FIFO, дубликат) — тоже
	// своё. Мутация «потреблять» → второй false, красный.
	t.Run("not consumed", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		f.Remove(x)
		s.ExpectRemoval(x).Removed()
		if a, b := s.OnDestroyed(x), s.OnDestroyed(x); !a || !b {
			t.Fatalf("(6) own=%v,%v, want true,true", a, b)
		}
	})

	// (7) список, начатый ДО Forget и показывающий X: страж воскрешения X не
	// кладёт — и метку не снимает (D6.3). Мутация «снимать до стража» →
	// RemovedByUs=false, красный.
	t.Run("list older than forget", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		var once sync.Once
		f.InList(func() {
			once.Do(func() {
				f.Remove(x)
				s.ExpectRemoval(x).Removed()
			})
		})
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		f.InList(nil)
		if !s.RemovedByUs(x) {
			t.Fatal("(7) список старше Forget снял метку removed")
		}
	})

	// (8) список начат до Forget и прочитал живой X; до его применения пришёл
	// опоздавший свой ifcreated X — его метка (gone=false) перекрыла метку
	// Forget. Список старше Forget не воскрешает X и метку removed не снимает:
	// страж сравнивает и seq Forget с началом списка. Мутация «страж только по
	// exist-метке» → X в карте, RemovedByUs=false, красный.
	t.Run("list older than forget, late ifcreated", func(t *testing.T) {
		f, s, _ := ownedStore(t)
		var once sync.Once
		f.InList(func() {
			once.Do(func() {
				f.Remove(x)
				s.ExpectRemoval(x).Removed()
				s.OnCreated(x)
			})
		})
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		f.InList(nil)
		if s.Shown(x) || !s.RemovedByUs(x) {
			t.Fatalf("(8) Shown=%v RemovedByUs=%v, want false/true", s.Shown(x), s.RemovedByUs(x))
		}
	})
}

// L3: TTL метки removed = expectedHookTTL оркестратора (45 с): стенд 01.10 —
// опоздание хуков под churn до ≈30 с, ×1,5; X6 — свой хук максимум 27 с.
// Мутация 15 с → красный: свой ifdestroyed на 20-й секунде стал бы чужим
// (публикация, список, проба оркестратора).
func TestRemovedProofTTL_Pin(t *testing.T) {
	if removedProofTTL != 45*time.Second {
		t.Fatalf("removedProofTTL = %s, want 45s", removedProofTTL)
	}
}

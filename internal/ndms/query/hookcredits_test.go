package query

import (
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

// creditsStore — тёплый стор над оракулом с X (Wireguard1) и Y (Wireguard2).
func creditsStore(t *testing.T) *InterfaceStore {
	t.Helper()
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"}, ndms.Interface{ID: "Wireguard2", Type: "Wireguard"})
	s := warmStore(t, f)
	s.SetCreatedBackoff()
	return s
}

func creditsLen(s *InterfaceStore) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.credits)
}

// П21: кредит created — только за доказанное создание, одно гашение на кредит.
// Мутации: выдавать без created → Y true, красный; не уменьшать при гашении →
// второй X true, красный.
func TestHookCredits_CreatedOnlyWhenProven(t *testing.T) {
	ctx := context.Background()
	s := creditsStore(t)
	if _, err := s.ConfirmCreated(ctx, "Wireguard1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmCreated(ctx, "Wireguard2", false); err != nil { // existingOK
		t.Fatal(err)
	}
	if !s.ClaimOwnCreated("Wireguard1") || s.ClaimOwnCreated("Wireguard1") {
		t.Fatal("X: ждали один кредит created")
	}
	if s.ClaimOwnCreated("Wireguard2") {
		t.Fatal("Y: создание не доказано — кредита нет")
	}
}

// П21: воплощения имени не трогают кредиты друг друга — FIFO хуков гасит их
// по порядку. Мутация: снимать кредиты на входе ConfirmCreated (новое
// воплощение) → третье гашение false, красный.
func TestHookCredits_FIFOAcrossIncarnations(t *testing.T) {
	ctx := context.Background()
	s := creditsStore(t)
	if _, err := s.ConfirmCreated(ctx, "Wireguard1", true); err != nil { // k
		t.Fatal(err)
	}
	s.ExpectRemoval("Wireguard1").Removed()
	if _, err := s.ConfirmCreated(ctx, "Wireguard1", true); err != nil { // k+1
		t.Fatal(err)
	}
	got := []bool{
		s.ClaimOwnCreated("Wireguard1"),
		s.ClaimOwnDestroyed("Wireguard1"),
		s.ClaimOwnCreated("Wireguard1"),
		s.ClaimOwnCreated("Wireguard1"),
		s.ClaimOwnDestroyed("Wireguard1"),
	}
	want := []bool{true, true, true, false, false}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("гашения %v, ждали %v", got, want)
		}
	}
}

// П21: запись с обоими нулями удаляется — память ограничена именами с хуками
// в полёте. Мутация: не удалять запись → красный.
func TestHookCredits_EntryDroppedAtZero(t *testing.T) {
	ctx := context.Background()
	s := creditsStore(t)
	if _, err := s.ConfirmCreated(ctx, "Wireguard1", true); err != nil {
		t.Fatal(err)
	}
	s.ExpectRemoval("Wireguard1").Removed()
	s.ClaimOwnCreated("Wireguard1")
	s.ClaimOwnDestroyed("Wireguard1")
	if n := creditsLen(s); n != 0 {
		t.Fatalf("len(credits)=%d, ждали 0", n)
	}
}

// П21: исходы жетона. Мутации: Absent оставляет кредит → красный; Refused
// чистит карту → красный.
func TestRemovalToken_Outcomes(t *testing.T) {
	ctx := context.Background()
	t.Run("Removed", func(t *testing.T) {
		s := creditsStore(t)
		s.ExpectRemoval("Wireguard1").Removed()
		if rec, _ := s.Get(ctx, "Wireguard1"); rec != nil {
			t.Fatal("снятое осталось в карте")
		}
		if !s.ClaimOwnDestroyed("Wireguard1") {
			t.Fatal("кредит снятия пропал")
		}
	})
	t.Run("Absent", func(t *testing.T) {
		s := creditsStore(t)
		s.ExpectRemoval("Wireguard1").Absent()
		if rec, _ := s.Get(ctx, "Wireguard1"); rec != nil {
			t.Fatal("отсутствующее осталось в карте")
		}
		if s.ClaimOwnDestroyed("Wireguard1") || creditsLen(s) != 0 {
			t.Fatal("кредит за несостоявшееся снятие остался")
		}
	})
	t.Run("Refused", func(t *testing.T) {
		s := creditsStore(t)
		s.ExpectRemoval("Wireguard1").Refused()
		if rec, _ := s.Get(ctx, "Wireguard1"); rec == nil {
			t.Fatal("отказ снятия убрал запись из карты")
		}
		if s.ClaimOwnDestroyed("Wireguard1") || creditsLen(s) != 0 {
			t.Fatal("кредит за отказ остался")
		}
	})
	t.Run("second outcome panics", func(t *testing.T) {
		s := creditsStore(t)
		tok := s.ExpectRemoval("Wireguard1")
		tok.Refused()
		defer func() {
			if recover() == nil {
				t.Fatal("повторный исход жетона не паникует")
			}
		}()
		tok.Removed()
	})
}

// п.3 ревью: хук погасил кредит раньше разбора ответа, исход — отказ или
// «unable to find»: снятие насыщенное, счётчик не уходит в 2^32−1.
// Мутация: destroyed-- без проверки → следующее гашение true, красный.
func TestRemovalToken_RefusedAfterClaim_NoUnderflow(t *testing.T) {
	for name, outcome := range map[string]func(*RemovalToken){
		"Refused": (*RemovalToken).Refused,
		"Absent":  (*RemovalToken).Absent,
	} {
		t.Run(name, func(t *testing.T) {
			s := creditsStore(t)
			tok := s.ExpectRemoval("Wireguard1")
			if !s.ClaimOwnDestroyed("Wireguard1") {
				t.Fatal("кредит до ответа не выдан")
			}
			outcome(tok)
			if s.ClaimOwnDestroyed("Wireguard1") || creditsLen(s) != 0 {
				t.Fatalf("после исхода: кредит есть, len(credits)=%d", creditsLen(s))
			}
		})
	}
}

// Сторож формы Unlisted: Confirmed с тем же именем.
func TestUnlisted_IsConfirmedName(t *testing.T) {
	if got := Unlisted("Wireguard1").Name(); got != "Wireguard1" {
		t.Fatalf("Name()=%q", got)
	}
}

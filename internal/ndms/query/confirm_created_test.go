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
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1", true)
	if err != nil || c.Name() != "Wireguard1" {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.ListCalls()-lists != 3 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("lists=%d E=%d phantoms=%d", f.ListCalls()-lists, f.E, f.Phantoms)
	}
}

// M3′: свой ifcreated пришёл, пока первый список в полёте, а записи в ответе
// нет — метка новее списка противоречит ему: повтор сразу, паузу 5 с не ждёт.
// Мутация: перечитывать только после паузы → ≥5 с.
func TestConfirmCreated_OwnCreatedInFlight_AbsentAnswer_RereadImmediately(t *testing.T) {
	f, s := importLate(t, 1, 5*time.Second)
	f.InList(func() {
		f.InList(nil)
		s.OnCreated("Wireguard1")
	})
	lists := f.ListCalls()
	start := time.Now()
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1", true)
	if err != nil || c.Name() != "Wireguard1" {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("ждал паузу вместо повтора: %v", el)
	}
	if f.ListCalls()-lists != 2 || f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("lists=%d E=%d phantoms=%d", f.ListCalls()-lists, f.E, f.Phantoms)
	}
}

// S11: записи нет за все попытки — ровно 1+len(backoff) списков; ошибку решает
// доказательство создания: created → ErrNotListed (снос разрешён), нет →
// ErrNotSeen (без сноса). Мутация: поменять местами → красный.
func TestConfirmCreated_NeverListed_ByCreated(t *testing.T) {
	for _, created := range []bool{true, false} {
		f, s := importLate(t, 100, time.Millisecond, time.Millisecond)
		lists := f.ListCalls()
		c, err := s.ConfirmCreated(context.Background(), "Wireguard1", created)
		if errors.Is(err, ErrNotListed) != created || errors.Is(err, ErrNotSeen) == created || c != (Confirmed{}) {
			t.Fatalf("created=%v: c=%v err=%v", created, c, err)
		}
		if f.ListCalls()-lists != 3 || f.E != 0 {
			t.Fatalf("created=%v: lists=%d E=%d", created, f.ListCalls()-lists, f.E)
		}
	}
}

// Создание доказано, а в списках записи нет за все попытки — ErrNotListed
// после 1+len(backoff) списков.
func TestConfirmCreated_NeverListed_ErrNotListed(t *testing.T) {
	f, s := importLate(t, 100, time.Millisecond, time.Millisecond)
	lists := f.ListCalls()
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1", true)
	if !errors.Is(err, ErrNotListed) || c != (Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.ListCalls()-lists != 3 || f.E != 0 {
		t.Fatalf("lists=%d E=%d", f.ListCalls()-lists, f.E)
	}
}

// Создание не доказано (ответ без «created»: запись уже была), записи в
// списках нет — ErrNotSeen: запись не наша, сносить нельзя.
func TestConfirmCreated_NotProvenNeverListed_ErrNotSeen(t *testing.T) {
	f, s := importLate(t, 100, time.Millisecond, time.Millisecond)
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1", false)
	if !errors.Is(err, ErrNotSeen) || errors.Is(err, ErrNotListed) || c != (Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.E != 0 {
		t.Fatalf("E=%d", f.E)
	}
}

// Устаревший ifdestroyed прежнего владельца имени при живой новой записи
// (ревью 51a M1/M2): хук ничего не решает — подтверждает свой список.
//   - pause: метка между списками (запись видна со второго) → Confirmed
//     вторым списком. Мутация: отказ по метке gone во время ожидания.
//   - in flight: метка, пока первый список в полёте, запись в ответе —
//     противоречие, один повтор, Confirmed. Мутация: проверять метку gone
//     раньше ответа списка.
func TestConfirmCreated_StaleDestroyedDuringWait_Confirms(t *testing.T) {
	t.Run("pause", func(t *testing.T) {
		f, s := importLate(t, 1, 300*time.Millisecond)
		firstListed := make(chan struct{})
		f.InList(func() {
			f.InList(nil)
			close(firstListed)
		})
		lists := f.ListCalls()
		done := make(chan error, 1)
		go func() {
			c, err := s.ConfirmCreated(context.Background(), "Wireguard1", true)
			if err == nil && c.Name() != "Wireguard1" {
				err = errors.New("не то имя: " + c.Name())
			}
			done <- err
		}()
		<-firstListed
		s.OnDestroyed("Wireguard1") // запись в NDMS жива
		if err := <-done; err != nil {
			t.Fatalf("err=%v", err)
		}
		if f.ListCalls()-lists != 2 || !f.Has("Wireguard1") || f.E != 0 || f.Phantoms != 0 {
			t.Fatalf("lists=%d has=%v E=%d phantoms=%d", f.ListCalls()-lists, f.Has("Wireguard1"), f.E, f.Phantoms)
		}
	})
	t.Run("in flight", func(t *testing.T) {
		f, s := importLate(t, 0, 5*time.Second)
		f.InList(func() {
			f.InList(nil)
			s.OnDestroyed("Wireguard1") // запись в NDMS жива
		})
		lists := f.ListCalls()
		c, err := s.ConfirmCreated(context.Background(), "Wireguard1", true)
		if err != nil || c.Name() != "Wireguard1" {
			t.Fatalf("c=%v err=%v", c, err)
		}
		if f.ListCalls()-lists != 2 || f.E != 0 || f.Phantoms != 0 {
			t.Fatalf("lists=%d (ждали свой + повтор) E=%d phantoms=%d", f.ListCalls()-lists, f.E, f.Phantoms)
		}
	})
}

// Список не прочитан — ошибка сразу, не ErrNotListed (решение 4): повторов нет.
func TestConfirmCreated_ListError_NoRetry(t *testing.T) {
	f, s := importLate(t, 0, time.Millisecond, time.Millisecond)
	f.FailList(errors.New("rci down"))
	lists := f.ListCalls()
	c, err := s.ConfirmCreated(context.Background(), "Wireguard1", true)
	if err == nil || errors.Is(err, ErrNotListed) || c != (Confirmed{}) {
		t.Fatalf("c=%v err=%v", c, err)
	}
	if f.ListCalls()-lists != 1 {
		t.Fatalf("lists=%d", f.ListCalls()-lists)
	}
}

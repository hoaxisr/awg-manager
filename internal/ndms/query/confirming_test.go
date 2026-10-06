package query

import (
	"context"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

func dirtyAt(s *InterfaceStore) uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirtyAt
}

func confirmingLen(s *InterfaceStore) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.confirming)
}

// П23: имя занято (confirming) только на время ожидания ConfirmCreated: во
// время ожидания FreeIndex его не отдаёт, после выхода (успех, created) оно в
// карте, а confirming пуст. Мутация «держать после успеха при created» →
// len(confirming)=1, красный.
func TestConfirming_OnlyDuringWait(t *testing.T) {
	ctx := context.Background()
	// Запись скрыта на два чтения: первый список ожидания и список FreeIndex.
	f, s := importLate(t, 2, time.Millisecond, time.Millisecond)
	var idx int
	var ok bool
	var err error
	f.InList(func() {
		f.InList(nil)
		idx, ok, err = s.FreeIndex(ctx, "Wireguard", 100, nil)
	})
	if _, cerr := s.ConfirmCreated(ctx, "Wireguard1", true); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil || !ok || idx != 2 {
		t.Fatalf("FreeIndex во время ожидания: idx=%d ok=%v err=%v, want 2", idx, ok, err)
	}
	if rec, _ := s.Get(ctx, "Wireguard1"); rec == nil || confirmingLen(s) != 0 {
		t.Fatalf("после выхода: rec=%v confirming=%d, want запись/0", rec, confirmingLen(s))
	}
}

// П23 (5): created=false (existingOK) не держит имя после выхода. Мутация
// «снимать только при ошибке» → len(confirming)=1, красный.
func TestConfirming_ExistingOK_NotHeldAfterExit(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "OpkgTun4", Type: "OpkgTun"})
	s := warmStore(t, f)
	if _, err := s.ConfirmCreated(context.Background(), "OpkgTun4", false); err != nil {
		t.Fatal(err)
	}
	if n := confirmingLen(s); n != 0 {
		t.Fatalf("confirming=%d после выхода, want 0", n)
	}
}

// Сторож прежней семантики П6′ (зелёный и до П22, в счёт мутаций не идёт):
// чужой ifcreated незнакомого id — «грязно», знакомого — нет.
func TestOnCreated_Foreign_DirtyWhenUnknown(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	s.OnCreated("Wireguard0")
	if d := dirtyAt(s); d != 0 {
		t.Fatalf("знакомый id: dirtyAt=%d, want 0", d)
	}
	s.OnCreated("Wireguard5")
	if dirtyAt(s) == 0 {
		t.Fatal("незнакомый id: не грязно")
	}
}

// Сторож прежней семантики П6′: чужой ifdestroyed знакомого id — «грязно»,
// забытого — нет.
func TestOnDestroyed_Foreign_DirtyWhenKnown(t *testing.T) {
	f := NewFakeNDMS(ndms.Interface{ID: "Wireguard0", Type: "Wireguard"})
	s := warmStore(t, f)
	s.OnDestroyed("Wireguard5")
	if d := dirtyAt(s); d != 0 {
		t.Fatalf("незнакомый id: dirtyAt=%d, want 0", d)
	}
	s.OnDestroyed("Wireguard0")
	if dirtyAt(s) == 0 {
		t.Fatal("знакомый id: не грязно")
	}
}

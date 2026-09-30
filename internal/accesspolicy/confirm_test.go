package accesspolicy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// newOracleService — сервис поверх оракула FakeNDMS: список, чтения и команды
// идут в f (F546).
func newOracleService(t *testing.T, f *query.FakeNDMS) *ServiceImpl {
	t.Helper()
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := command.NewCommands(command.Deps{
		Poster:  f,
		Queries: q,
		Save:    command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil),
		IsOS5:   func() bool { return true },
	})
	return &ServiceImpl{policies: cmds.Policies, interfaces: cmds.Interfaces, queries: q}
}

func oracleClean(t *testing.T, f *query.FakeNDMS) {
	t.Helper()
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d, want 0/0; posts=%v", f.E, f.Phantoms, f.Posts)
	}
}

// Интерфейса нет в NDMS — permit ушёл вместе с ним: снос без команды, nil.
func TestAccessPolicy_DenyAbsentInterface_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleService(t, f)
	if err := s.DenyInterface(context.Background(), "Policy0", "Wireguard3"); err != nil {
		t.Fatalf("DenyInterface: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Permit по отсутствующему — ссылка на него из ip policy пишет E: отказ до RCI.
func TestAccessPolicy_PermitAbsentInterface_Error(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleService(t, f)
	err := s.PermitInterface(context.Background(), "Policy0", "Wireguard3", 0)
	if err == nil {
		t.Fatal("ждали отказ по отсутствующему интерфейсу")
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Есть — команда уходит, E нет.
func TestAccessPolicy_PermitDenyPresent_Sends(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	s := newOracleService(t, f)
	ctx := context.Background()
	if err := s.PermitInterface(ctx, "Policy0", "Wireguard3", 0); err != nil {
		t.Fatalf("PermitInterface: %v", err)
	}
	if err := s.DenyInterface(ctx, "Policy0", "Wireguard3"); err != nil {
		t.Fatalf("DenyInterface: %v", err)
	}
	if len(f.Posts) < 2 {
		t.Fatalf("ждали permit и deny, posts=%v", f.Posts)
	}
	oracleClean(t, f)
}

// Сырой флип по отсутствующему: up — отказ, down — nil; команд нет (иначе
// `interface X up` создал бы X).
func TestAccessPolicy_SetInterfaceUpAbsent(t *testing.T) {
	f := query.NewFakeNDMS()
	s := newOracleService(t, f)
	ctx := context.Background()
	if err := s.SetInterfaceUp(ctx, "Wireguard3", true); err == nil {
		t.Fatal("up по отсутствующему: ждали отказ")
	}
	if err := s.SetInterfaceUp(ctx, "Wireguard3", false); err != nil {
		t.Fatalf("down по отсутствующему: %v", err)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды по отсутствующему: %v", f.Posts)
	}
	oracleClean(t, f)
}

// Список не прочитан (решение 4) — отказ без команды на всех путях.
func TestAccessPolicy_ListError_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
	boom := errors.New("rci down")
	f.FailList(boom)
	s := newOracleService(t, f)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"permit": func() error { return s.PermitInterface(ctx, "Policy0", "Wireguard3", 0) },
		"deny":   func() error { return s.DenyInterface(ctx, "Policy0", "Wireguard3") },
		"up":     func() error { return s.SetInterfaceUp(ctx, "Wireguard3", true) },
		"down":   func() error { return s.SetInterfaceUp(ctx, "Wireguard3", false) },
	} {
		if err := call(); !errors.Is(err, boom) {
			t.Errorf("%s: err=%v, want %v", name, err, boom)
		}
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды при ошибке списка: %v", f.Posts)
	}
}

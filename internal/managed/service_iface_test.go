package managed

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Две половины сервера получают политику двумя вызовами ApplyPolicyToInterface — список
// политик при этом читается с роутера ОДИН раз (PolicyStore кэширует 60 мин), а не два.
// Пункт трекера «Два ListPolicies на одно применение» закрывается этим пином без правки.
func TestApplyPolicyToInterface_TwoHalvesOneRCIRead(t *testing.T) {
	store := storage.NewSettingsStore(t.TempDir())
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	poster := &fakePoster{}
	getter := &fakePolicyGetter{body: []byte(`{"Policy0":{"description":"NL"}}`)}
	queries := &query.Queries{
		Policies:   query.NewPolicyStore(getter, query.NopLogger()),
		Interfaces: query.NewInterfaceStore(query.NewFakeNDMS(ndms.Interface{ID: "OpkgTun17"}, ndms.Interface{ID: "OpkgTun19"}), query.NopLogger()),
	}
	svc := New(poster, nil, queries, nil, store, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
	for _, iface := range []string{"OpkgTun17", "OpkgTun19"} {
		if err := svc.ApplyPolicyToInterface(ctx, iface, "Policy0"); err != nil {
			t.Fatalf("%s: %v", iface, err)
		}
	}
	if getter.raws != 1 || len(poster.posts) != 2 {
		t.Fatalf("GetRaw=%d (ждали 1), RCI-POST=%d (ждали 2)", getter.raws, len(poster.posts))
	}
}

// Нулевой Confirmed (пропущенное подтверждение) в rci*-помощнике — отказ без
// POST: иначе команда ушла бы по имени "" мимо подтверждения (F546).
func TestRCIHelpers_ZeroConfirmed_NoPost(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard1"})
	queries := &query.Queries{Interfaces: query.NewInterfaceStore(f, query.NopLogger())}
	svc := New(f, nil, queries, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
	wan, _, ok, err := queries.Interfaces.Confirm(ctx, "Wireguard1")
	if err != nil || !ok {
		t.Fatalf("confirm: ok=%v err=%v", ok, err)
	}
	for name, call := range map[string]func() error{
		"up":         func() error { return svc.rciInterfaceUp(ctx, query.Confirmed{}) },
		"delete":     func() error { return svc.rciDeleteInterface(ctx, query.Confirmed{}) },
		"static-nat": func() error { return svc.rciSetStaticNAT(ctx, wan, query.Confirmed{}, true) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: нулевой Confirmed принят", name)
		}
	}
	if len(f.Posts) != 0 || f.Phantoms != 0 {
		t.Fatalf("posts=%v phantoms=%d", f.Posts, f.Phantoms)
	}
}

// П24: снос сервера — единым путём команд (DeleteOpkgTun). Без команд —
// ошибка без POST; с командами кэши managed сбрасываются, как у rciPost (п.9):
// список серверов после сноса не показывает снятый.
// Мутация: не прокидывать invalidators в DeleteOpkgTun → список из кэша со
// снятым сервером, красный.
func TestRciDeleteInterface_ViaCommands(t *testing.T) {
	ctx := context.Background()
	t.Run("not wired", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard1"})
		queries := &query.Queries{Interfaces: query.NewInterfaceStore(f, query.NopLogger())}
		svc := New(f, nil, queries, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
		conf, _, ok, err := queries.Interfaces.Confirm(ctx, "Wireguard1")
		if err != nil || !ok {
			t.Fatalf("confirm: ok=%v err=%v", ok, err)
		}
		if err := svc.rciDeleteInterface(ctx, conf); err == nil || len(f.Posts) != 0 {
			t.Fatalf("err=%v posts=%v", err, f.Posts)
		}
	})
	t.Run("wired", func(t *testing.T) {
		f := query.NewFakeNDMS(ndms.Interface{ID: "Wireguard1", Type: "Wireguard"}, ndms.Interface{ID: "Wireguard3", Type: "Wireguard"})
		s := newServiceWithOracle(t, f, nil)
		conf, _, ok, err := s.queries.Interfaces.Confirm(ctx, "Wireguard1")
		if err != nil || !ok {
			t.Fatalf("confirm: ok=%v err=%v", ok, err)
		}
		if servers, err := s.queries.WGServers.List(ctx); err != nil || len(servers) != 2 {
			t.Fatalf("до сноса: %v %v", servers, err)
		}
		if err := s.rciDeleteInterface(ctx, conf); err != nil {
			t.Fatal(err)
		}
		servers, err := s.queries.WGServers.List(ctx)
		if err != nil || len(servers) != 1 || f.Has("Wireguard1") {
			t.Fatalf("после сноса список серверов %v (err=%v): кэш WGServers не сброшен", servers, err)
		}
	})
}

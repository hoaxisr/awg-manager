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

package managed

import (
	"context"
	"io"
	"log/slog"
	"testing"

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
	queries := &query.Queries{Policies: query.NewPolicyStore(getter, query.NopLogger())}
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

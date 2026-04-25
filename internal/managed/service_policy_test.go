package managed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakePoster records every RCI POST payload and can be primed with errors.
type fakePoster struct {
	posts []map[string]interface{}
	err   error
}

func (f *fakePoster) Post(ctx context.Context, payload any) (json.RawMessage, error) {
	if m, ok := payload.(map[string]interface{}); ok {
		f.posts = append(f.posts, m)
	}
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage("{}"), nil
}

// fakePolicyGetter satisfies query.Getter and returns a fixed
// /show/rc/ip/policy body.
type fakePolicyGetter struct {
	body []byte
	err  error
}

func (f *fakePolicyGetter) Get(ctx context.Context, path string, out any) error {
	return errors.New("Get not used in policy tests")
}

func (f *fakePolicyGetter) GetRaw(ctx context.Context, path string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.body, nil
}

func newTestService(t *testing.T, server *storage.ManagedServer, posterErr error, policyJSON string) (*Service, *fakePoster, *storage.SettingsStore) {
	t.Helper()
	tmpDir := t.TempDir()
	store := storage.NewSettingsStore(tmpDir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	if server != nil {
		if err := store.SaveManagedServer(server); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}
	poster := &fakePoster{err: posterErr}
	getter := &fakePolicyGetter{body: []byte(policyJSON)}
	queries := &query.Queries{
		Policies: query.NewPolicyStore(getter, query.NopLogger()),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(poster, nil, queries, nil, store, log, nil)
	return svc, poster, store
}

func TestSetPolicy_RejectsEmpty(t *testing.T) {
	svc, _, _ := newTestService(t, &storage.ManagedServer{InterfaceName: "Wireguard0"}, nil, `{}`)
	if err := svc.SetPolicy(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty policy")
	}
}

func TestSetPolicy_RejectsMissingServer(t *testing.T) {
	svc, _, _ := newTestService(t, nil, nil, `{}`)
	if err := svc.SetPolicy(context.Background(), "permit"); err == nil {
		t.Fatal("expected error when no managed server exists")
	}
}

func TestSetPolicy_RejectsUnknownPolicy(t *testing.T) {
	svc, _, _ := newTestService(t, &storage.ManagedServer{InterfaceName: "Wireguard0"}, nil, `{"Policy0":{"description":"NL"}}`)
	if err := svc.SetPolicy(context.Background(), "Policy999"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestSetPolicy_AcceptsLiterals(t *testing.T) {
	for _, lit := range []string{"permit", "deny", "none"} {
		t.Run(lit, func(t *testing.T) {
			svc, poster, store := newTestService(t, &storage.ManagedServer{InterfaceName: "Wireguard0", Policy: "none"}, nil, `{}`)
			if lit == "none" {
				if err := svc.SetPolicy(context.Background(), lit); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(poster.posts) != 0 {
					t.Fatalf("expected 0 RCI calls for no-op, got %d", len(poster.posts))
				}
				return
			}
			if err := svc.SetPolicy(context.Background(), lit); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(poster.posts) != 1 {
				t.Fatalf("expected 1 RCI call, got %d", len(poster.posts))
			}
			persisted := store.GetManagedServer()
			if persisted == nil || persisted.Policy != lit {
				t.Fatalf("policy not persisted: got %+v", persisted)
			}
		})
	}
}

func TestSetPolicy_AcceptsKnownProfile(t *testing.T) {
	svc, poster, store := newTestService(t, &storage.ManagedServer{InterfaceName: "Wireguard0", Policy: "none"}, nil, `{"Policy0":{"description":"NL"}}`)
	if err := svc.SetPolicy(context.Background(), "Policy0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(poster.posts) != 1 {
		t.Fatalf("expected 1 RCI call, got %d", len(poster.posts))
	}
	persisted := store.GetManagedServer()
	if persisted == nil || persisted.Policy != "Policy0" {
		t.Fatalf("policy not persisted: %+v", persisted)
	}
}

func TestSetPolicy_NoopWhenSame(t *testing.T) {
	svc, poster, _ := newTestService(t, &storage.ManagedServer{InterfaceName: "Wireguard0", Policy: "permit"}, nil, `{}`)
	if err := svc.SetPolicy(context.Background(), "permit"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(poster.posts) != 0 {
		t.Fatalf("expected no-op, got %d RCI calls", len(poster.posts))
	}
}

func TestListPolicies_ReturnsStoreContents(t *testing.T) {
	svc, _, _ := newTestService(t, nil, nil, `{"Policy0":{"description":"NL"},"Policy1":{"description":""}}`)
	got, err := svc.ListPolicies(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 policies, got %d", len(got))
	}
}

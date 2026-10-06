package hydraroute

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// fakePoster records Post payloads for assertion and returns a benign response.
type fakePoster struct {
	mu       sync.Mutex
	payloads []any
}

func (f *fakePoster) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	return json.RawMessage(`{}`), nil
}

func (f *fakePoster) Payloads() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]any, len(f.payloads))
	copy(out, f.payloads)
	return out
}

// nopPublisher satisfies command.StatusPublisher without side effects.
type nopPublisher struct{}

func (nopPublisher) Publish(string, any) {}

// newTestQueries builds a query.Queries backed by a controllable FakeGetter.
func newTestQueries() (*query.Queries, *query.FakeGetter) {
	g := query.NewFakeGetter()
	q := query.NewQueries(query.Deps{
		Getter: g,
		Logger: query.NopLogger(),
		IsOS5:  func() bool { return true },
	})
	return q, g
}

// newTestPolicyCommands builds a real *command.PolicyCommands wired to a fakePoster.
func newTestPolicyCommands(q *query.Queries) (*command.PolicyCommands, *fakePoster) {
	poster := &fakePoster{}
	sc := command.NewSaveCoordinator(poster, nopPublisher{}, 500*time.Millisecond, 5*time.Second, 0, nil)
	return command.NewPolicyCommands(poster, sc, q), poster
}

func TestListPolicyNames_ParsesKeys(t *testing.T) {
	q, g := newTestQueries()
	g.SetJSON("/show/rc/ip/policy", `{
		"Policy0": {"description": "Mallware"},
		"HydraRoute": {"description": ""}
	}`)
	svc := &Service{queries: q}

	got, err := svc.ListPolicyNames(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sort.Strings(got)
	want := []string{"HydraRoute", "Policy0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if g.Calls("/show/rc/ip/policy") != 1 {
		t.Errorf("expected 1 RCI fetch, got %d", g.Calls("/show/rc/ip/policy"))
	}
}

func TestListPolicyNames_NoNDMS(t *testing.T) {
	svc := &Service{}
	got, err := svc.ListPolicyNames(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestListPolicyNames_NDMSError(t *testing.T) {
	q, g := newTestQueries()
	g.SetError("/show/rc/ip/policy", errors.New("boom"))
	svc := &Service{queries: q}

	_, err := svc.ListPolicyNames(context.Background())
	if err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestEnsurePolicyInterfaces_OrderIsZeroBased(t *testing.T) {
	// Regression: Keenetic rejects 'ip policy permit order N' when N is
	// out of range. The first permit on a fresh policy MUST be order=0;
	// previously we sent order=1 and got "invalid order: 1".
	q, g := newTestQueries()
	g.SetJSON("/show/interface/", `{"PPPoE0":{"id":"PPPoE0"},"Wireguard0":{"id":"Wireguard0"},"Wireguard1":{"id":"Wireguard1"}}`)
	cmds, poster := newTestPolicyCommands(q)
	svc := &Service{policies: cmds, queries: q}

	err := svc.EnsurePolicyInterfaces(
		context.Background(),
		"NewPolicy",
		[]string{"PPPoE0", "Wireguard0", "Wireguard1"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	payloads := poster.Payloads()
	if len(payloads) != 3 {
		t.Fatalf("expected 3 Post calls, got %d", len(payloads))
	}

	wantOrders := []int{0, 1, 2}
	wantIfaces := []string{"PPPoE0", "Wireguard0", "Wireguard1"}
	for i, payload := range payloads {
		permit := digPermit(t, payload, "NewPolicy")
		gotOrder, ok := permit["order"].(int)
		if !ok {
			t.Fatalf("call %d: permit.order missing/wrong type: %v", i, permit["order"])
		}
		if gotOrder != wantOrders[i] {
			t.Errorf("call %d: order = %d, want %d", i, gotOrder, wantOrders[i])
		}
		if iface, _ := permit["interface"].(string); iface != wantIfaces[i] {
			t.Errorf("call %d: interface = %q, want %q", i, iface, wantIfaces[i])
		}
	}
}

// digPermit drills into the nested RCI payload to fetch the permit object.
func digPermit(t *testing.T, payload any, policyName string) map[string]any {
	t.Helper()
	root, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload not a map: %T", payload)
	}
	ip, _ := root["ip"].(map[string]any)
	policy, _ := ip["policy"].(map[string]any)
	named, _ := policy[policyName].(map[string]any)
	permit, _ := named["permit"].(map[string]any)
	if permit == nil {
		t.Fatalf("permit object missing from payload: %+v", root)
	}
	return permit
}

// Отсутствующий в NDMS интерфейс пропускается без команды (ссылка на него из
// ip policy — E), остальные ставятся подряд с order 0,1 — без дыры; все
// подтверждаются ОДНИМ списком (F546).
func TestEnsurePolicyInterfaces_SkipsAbsent_OneList(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "PPPoE0"}, ndms.Interface{ID: "Wireguard1"})
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := command.NewPolicyCommands(f, command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil), q)
	svc := &Service{policies: cmds, queries: q}

	if err := svc.EnsurePolicyInterfaces(context.Background(), "HydraRoute", []string{"Wireguard0", "PPPoE0", "Wireguard1"}); err != nil {
		t.Fatalf("EnsurePolicyInterfaces: %v", err)
	}
	want := []string{
		`{"ip":{"policy":{"HydraRoute":{"permit":{"global":true,"interface":"PPPoE0","order":0}}}}}`,
		`{"ip":{"policy":{"HydraRoute":{"permit":{"global":true,"interface":"Wireguard1","order":1}}}}}`,
	}
	if !reflect.DeepEqual(f.Posts, want) {
		t.Fatalf("posts:\n got %v\nwant %v", f.Posts, want)
	}
	if n := f.ListCalls(); n != 1 {
		t.Fatalf("чтений списка = %d, want 1", n)
	}
	if f.E != 0 || f.Phantoms != 0 {
		t.Fatalf("E=%d фантомов=%d", f.E, f.Phantoms)
	}
}

// Список не прочитан (решение 4) — ошибка, ни одной команды.
func TestEnsurePolicyInterfaces_ListError_NoCommand(t *testing.T) {
	f := query.NewFakeNDMS(ndms.Interface{ID: "PPPoE0"})
	boom := errors.New("rci down")
	f.FailList(boom)
	q := query.NewQueries(query.Deps{Getter: f, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	cmds := command.NewPolicyCommands(f, command.NewSaveCoordinator(f, nil, time.Hour, time.Hour, 0, nil), q)
	svc := &Service{policies: cmds, queries: q}

	if err := svc.EnsurePolicyInterfaces(context.Background(), "HydraRoute", []string{"PPPoE0"}); !errors.Is(err, boom) {
		t.Fatalf("err=%v, want %v", err, boom)
	}
	if len(f.Posts) != 0 {
		t.Fatalf("команды при ошибке списка: %v", f.Posts)
	}
}

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
	sc := command.NewSaveCoordinator(poster, nopPublisher{}, 500*time.Millisecond, 5*time.Second)
	return command.NewPolicyCommands(poster, sc, q, nil), poster
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
	q, _ := newTestQueries()
	cmds, poster := newTestPolicyCommands(q)
	svc := &Service{policies: cmds}

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

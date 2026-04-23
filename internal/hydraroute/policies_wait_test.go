package hydraroute

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// delayedGetter returns an empty policy set the first N calls, then a
// response containing the target name. Lets us verify WaitForPolicy
// actually waits rather than succeeding on the first lookup.
type delayedGetter struct {
	mu         sync.Mutex
	emptyCalls int
	target     string
	callCount  int
}

func (d *delayedGetter) body(path string) []byte {
	d.mu.Lock()
	d.callCount++
	c := d.callCount
	d.mu.Unlock()

	if path != "/show/rc/ip/policy" {
		return []byte(`{}`)
	}
	if c <= d.emptyCalls {
		return []byte(`{}`)
	}
	return []byte(`{"` + d.target + `": {"description": ""}}`)
}

func (d *delayedGetter) Get(_ context.Context, path string, dst any) error {
	return json.Unmarshal(d.body(path), dst)
}

func (d *delayedGetter) GetRaw(_ context.Context, path string) ([]byte, error) {
	return d.body(path), nil
}

func (d *delayedGetter) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.callCount
}

func TestWaitForPolicy_ReturnsWhenPolicyAppears(t *testing.T) {
	g := &delayedGetter{emptyCalls: 2, target: "NewPolicy"}
	q := query.NewQueries(query.Deps{Getter: g, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	svc := &Service{queries: q}

	start := time.Now()
	err := svc.WaitForPolicy(context.Background(), "NewPolicy", 3*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Calls() < 3 {
		t.Errorf("expected at least 3 getter calls (2 empty + 1 success), got %d", g.Calls())
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("completed suspiciously fast: %s — polling should have waited", elapsed)
	}
}

func TestWaitForPolicy_TimesOut(t *testing.T) {
	g := query.NewFakeGetter()
	g.SetJSON("/show/rc/ip/policy", `{}`)
	q := query.NewQueries(query.Deps{Getter: g, Logger: query.NopLogger(), IsOS5: func() bool { return true }})
	svc := &Service{queries: q}

	err := svc.WaitForPolicy(context.Background(), "Missing", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestWaitForPolicy_NoNDMSIsNoop(t *testing.T) {
	svc := &Service{}
	if err := svc.WaitForPolicy(context.Background(), "Anything", 100*time.Millisecond); err != nil {
		t.Errorf("expected nil when no queries registry is wired, got %v", err)
	}
}

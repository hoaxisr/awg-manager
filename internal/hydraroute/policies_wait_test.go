package hydraroute

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// delayedNDMS returns an empty policy set the first N calls, then the given
// target name. Lets us verify polling actually waits for the policy to
// appear rather than succeeding on the first lookup.
type delayedNDMS struct {
	emptyCalls int
	target     string
	callCount  int
}

func (d *delayedNDMS) RCIGet(_ context.Context, _ string) (json.RawMessage, error) {
	d.callCount++
	if d.callCount <= d.emptyCalls {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(`{"` + d.target + `": {"description": ""}}`), nil
}

func (d *delayedNDMS) RCIPost(_ context.Context, _ interface{}) (json.RawMessage, error) {
	return nil, nil
}

func TestWaitForPolicy_ReturnsWhenPolicyAppears(t *testing.T) {
	ndms := &delayedNDMS{emptyCalls: 2, target: "NewPolicy"}
	svc := &Service{ndms: ndms}

	start := time.Now()
	err := svc.WaitForPolicy(context.Background(), "NewPolicy", 3*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ndms.callCount < 3 {
		t.Errorf("expected at least 3 RCIGet calls (2 empty + 1 success), got %d", ndms.callCount)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("completed suspiciously fast: %s — polling should have waited", elapsed)
	}
}

func TestWaitForPolicy_TimesOut(t *testing.T) {
	ndms := &fakeNDMS{getResp: json.RawMessage(`{}`)}
	svc := &Service{ndms: ndms}

	err := svc.WaitForPolicy(context.Background(), "Missing", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestWaitForPolicy_NoNDMSIsNoop(t *testing.T) {
	svc := &Service{}
	if err := svc.WaitForPolicy(context.Background(), "Anything", 100*time.Millisecond); err != nil {
		t.Errorf("expected nil when no NDMS client is wired, got %v", err)
	}
}

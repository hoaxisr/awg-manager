package uplink

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type testResolver struct{ providers []Provider }

func (r *testResolver) Providers() []Provider { return append([]Provider(nil), r.providers...) }

type testAdapter struct {
	needsRebind        bool
	prepareUnavailable map[string]bool
	device             string
	enabled            bool
	failing            map[string]bool
	activated          []string
	applyError         error
	validateError      error
}

func (a *testAdapter) Validate(context.Context, Target) error { return a.validateError }
func (a *testAdapter) Observe(context.Context, Target) (Observation, error) {
	return Observation{Device: a.device, Enabled: a.enabled, NeedsRebind: a.needsRebind}, nil
}
func (a *testAdapter) Activate(_ context.Context, _ Target, expected, device string) error {
	if a.device != expected {
		return ErrConflict
	}
	if a.prepareUnavailable[device] {
		return ErrPathUnavailable
	}
	a.activated = append(a.activated, device)
	if a.applyError != nil {
		return a.applyError
	}
	a.device = device
	a.needsRebind = false
	return nil
}
func (a *testAdapter) Probe(_ context.Context, _ Target, device string) error {
	if a.device != device || a.failing[device] {
		return errors.New("path failed")
	}
	return nil
}

func fixture(t *testing.T) (*Controller, *testAdapter, *testResolver, *time.Time) {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "policies.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &testAdapter{device: "wan1", enabled: true, failing: map[string]bool{}}
	r := &testResolver{[]Provider{{ID: "ISP1", Device: "wan1", Up: true}, {ID: "ISP2", Device: "wan2", Up: true}}}
	c := New(s, r, map[string]Adapter{"test": a})
	now := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	_, err = c.Put(context.Background(), Policy{ID: "tunnel", Target: Target{Engine: "test", Ref: "native"}, Providers: []string{"ISP1", "ISP2"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// Most transition tests model a loaded policy after daemon restart. Initial
	// pre-publication binding is covered separately in initial_binding_test.go.
	c.states = map[string]State{}
	a.activated = nil
	return c, a, r, &now
}
func state(c *Controller) State { return c.List()[0].State }
func advance(c *Controller, now *time.Time) {
	*now = now.Add(time.Minute)
	c.Tick(context.Background())
}

func TestSelectiveProtocolFailureAndManualReturn(t *testing.T) {
	c, a, _, now := fixture(t)
	c.Tick(context.Background())
	if state(c).Phase != "healthy" {
		t.Fatal(state(c))
	}
	a.activated = nil
	a.failing["wan1"] = true
	advance(c, now)
	advance(c, now)
	if len(a.activated) != 0 {
		t.Fatal("switched before threshold")
	}
	advance(c, now)
	if state(c).Provider != "ISP2" || state(c).Phase != "healthy" {
		t.Fatal(state(c))
	}
	a.failing["wan1"] = false
	advance(c, now)
	if len(a.activated) != 1 {
		t.Fatal("unexpected automatic failback")
	}
	p := c.List()[0].Policy
	if err := c.Switch(context.Background(), p.ID, "ISP1", p.Revision); err != nil {
		t.Fatal(err)
	}
	if state(c).Provider != "ISP1" {
		t.Fatal(state(c))
	}
}

func TestPhysicalDownBypassesThreshold(t *testing.T) {
	c, a, r, _ := fixture(t)
	r.providers[0].Up = false
	c.Tick(context.Background())
	if state(c).Provider != "ISP2" || len(a.activated) != 1 {
		t.Fatal(state(c), a.activated)
	}
}

func TestBothPathsFailNoDirectAndBackoff(t *testing.T) {
	c, a, r, now := fixture(t)
	r.providers[0].Up = false
	a.failing["wan2"] = true
	c.Tick(context.Background())
	if state(c).Phase != "unavailable" || a.device != "wan2" {
		t.Fatal(state(c))
	}
	count := len(a.activated)
	*now = now.Add(time.Second)
	c.Tick(context.Background())
	if len(a.activated) != count {
		t.Fatal("ignored backoff")
	}
	for _, v := range a.activated {
		if v == "" {
			t.Fatal("automatic routing enabled")
		}
	}
}

func TestExternalEditIsNotOverwritten(t *testing.T) {
	c, a, _, now := fixture(t)
	c.Tick(context.Background())
	a.activated = nil
	a.device = "unmanaged"
	advance(c, now)
	if state(c).Phase != "conflict" || len(a.activated) != 0 {
		t.Fatal(state(c))
	}
}

func TestApplyFailureNeverReportsHealthy(t *testing.T) {
	c, a, r, _ := fixture(t)
	r.providers[0].Up = false
	a.applyError = errors.New("reload failed")
	c.Tick(context.Background())
	if state(c).Phase != "recovery-required" {
		t.Fatal(state(c))
	}
}

func TestStoppedTunnelNotResurrected(t *testing.T) {
	c, a, _, _ := fixture(t)
	a.enabled = false
	c.Tick(context.Background())
	if state(c).Phase != "stopped" || len(a.activated) != 0 {
		t.Fatal(state(c))
	}
}

func TestPolicyRevisionAndReturnedSlices(t *testing.T) {
	c, _, _, _ := fixture(t)
	p := c.List()[0].Policy
	p.Providers[0] = "mutated"
	if c.List()[0].Policy.Providers[0] != "ISP1" {
		t.Fatal("store leaked mutable slice")
	}
	p = c.List()[0].Policy
	p.Enabled = false
	saved, err := c.Put(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 2 {
		t.Fatal(saved)
	}
	if _, err = c.Put(context.Background(), p); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = c.Delete(context.Background(), p.ID, p.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestRestartReconcilesInsteadOfTrustingRuntime(t *testing.T) {
	c, a, r, _ := fixture(t)
	s, err := OpenStore(c.store.path)
	if err != nil {
		t.Fatal(err)
	}
	a.device = "wan2"
	restarted := New(s, r, map[string]Adapter{"test": a})
	if state(restarted).Phase != "pending" {
		t.Fatal(state(restarted))
	}
	restarted.Tick(context.Background())
	if state(restarted).Provider != "ISP2" || state(restarted).Phase != "healthy" {
		t.Fatal(state(restarted))
	}
}

func TestMissingDeviceNameAfterReconnect(t *testing.T) {
	c, a, r, now := fixture(t)
	c.Tick(context.Background())
	r.providers[0].Device = "ppp-new"
	advance(c, now)
	if a.device != "ppp-new" || state(c).Phase != "healthy" {
		t.Fatal(state(c), a.device)
	}
}

func TestUnsupportedEngineDoesNotMutate(t *testing.T) {
	c, a, _, _ := fixture(t)
	a.validateError = ErrUnsupported
	c.Tick(context.Background())
	if state(c).Phase != "degraded" || len(a.activated) != 0 {
		t.Fatal(state(c))
	}
}

func TestFailedManualSwitchRestoresVerifiedPreviousPath(t *testing.T) {
	c, a, _, _ := fixture(t)
	c.Tick(context.Background())
	a.failing["wan2"] = true
	p := c.List()[0].Policy
	if err := c.Switch(context.Background(), p.ID, "ISP2", p.Revision); err == nil {
		t.Fatal("failed target reported success")
	}
	if state(c).Phase != "healthy" || state(c).Provider != "ISP1" || a.device != "wan1" {
		t.Fatal(state(c))
	}
}

func TestWANDownInterruptsLongProbeInterval(t *testing.T) {
	c, _, r, now := fixture(t)
	c.Tick(context.Background())
	r.providers[0].Up = false
	*now = now.Add(time.Second)
	c.Tick(context.Background())
	if state(c).Provider != "ISP2" {
		t.Fatal(state(c))
	}
}

func TestStartupFallbackDoesNotTreatAdoptionAsManualPin(t *testing.T) {
	c, a, _, now := fixture(t)
	a.failing["wan1"] = true
	// После рестарта (LastSuccess пуст) одна неудачная проба — ещё не отказ.
	c.Tick(context.Background())
	if state(c).Phase != "suspect" || state(c).Provider != "ISP1" || len(a.activated) != 1 {
		t.Fatal("failover on first failure after restart", state(c), a.activated)
	}
	advance(c, now)
	if state(c).Phase != "suspect" || state(c).Provider != "ISP1" {
		t.Fatal("threshold skipped without LastSuccess", state(c))
	}
	advance(c, now)
	if state(c).Phase != "healthy" || state(c).Provider != "ISP2" {
		t.Fatal(state(c))
	}
}

func TestUnavailablePreparationTriesNextWANWithoutMutation(t *testing.T) {
	c, a, _, _ := fixture(t)
	a.prepareUnavailable = map[string]bool{"wan1": true}
	c.Tick(context.Background())
	if state(c).Phase != "healthy" || state(c).Provider != "ISP2" || len(a.activated) != 1 {
		t.Fatal(state(c), a.activated)
	}
}

func TestSourceAddressChangeRebindsCurrentWANWithoutWaitingForFailures(t *testing.T) {
	c, a, r, now := fixture(t)
	c.Tick(context.Background())
	a.activated = nil
	a.needsRebind = true
	r.providers[1].Up = false
	advance(c, now)
	if len(a.activated) != 1 || a.device != "wan1" || state(c).Phase != "healthy" {
		t.Fatal(state(c), a.activated)
	}
}

func TestWANNotificationCoalescesAndInterruptsProbeInterval(t *testing.T) {
	c, a, _, now := fixture(t)
	c.Tick(context.Background())
	a.activated = nil
	a.needsRebind = true
	*now = now.Add(time.Second)
	c.Tick(context.Background())
	if len(a.activated) != 0 {
		t.Fatal("ordinary tick did not respect interval")
	}
	for range 1000 {
		c.NotifyWANChange("ISP1")
	}
	if len(c.wake) != 1 {
		t.Fatal("WAN events were not coalesced")
	}
	<-c.wake
	c.tick(context.Background(), true)
	if len(a.activated) != 1 || state(c).Phase != "healthy" {
		t.Fatal(state(c), a.activated)
	}
}

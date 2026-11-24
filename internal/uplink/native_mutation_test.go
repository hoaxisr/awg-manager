package uplink

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type nativeTestAdapter struct {
	testAdapter
	releaseErr error
	releases   int
}

func (a *nativeTestAdapter) UplinkCapabilities(Target) Capabilities {
	return Capabilities{IndependentEngine: true}
}
func (a *nativeTestAdapter) ReleaseUplink(context.Context, Target) error {
	a.releases++
	return a.releaseErr
}
func nativeController(t *testing.T) (*Controller, *nativeTestAdapter, Policy) {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.Put(Policy{ID: "wg", Target: Target{Engine: "wireguard", Ref: "awg50"}, Providers: []string{"one", "two"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a := &nativeTestAdapter{testAdapter: testAdapter{device: "wan1", enabled: true}}
	return New(store, nil, map[string]Adapter{"wireguard": a}), a, p
}
func TestNativeMutationGuardAndIndependentEngine(t *testing.T) {
	c, _, p := nativeController(t)
	for _, op := range []string{"transport", "delete"} {
		if _, err := c.GuardNativeMutation(context.Background(), p.Target.Ref, op); !errors.Is(err, ErrConflict) {
			t.Fatal(op, err)
		}
	}
	for _, op := range []string{"metadata", "lifecycle"} {
		release, err := c.GuardNativeMutation(context.Background(), p.Target.Ref, op)
		if err != nil {
			t.Fatal(err)
		}
		release()
		release()
	}
	lease, err := c.AcquireEngineTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.CheckTarget("firc"); err != nil {
		t.Fatal("native policy blocked unrelated routing engine", err)
	}
	lease.Release()
	if err := c.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GuardNativeMutation(context.Background(), p.Target.Ref, "lifecycle"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}
func TestNativeDeleteFailureRetainsDisabledPolicyForRetry(t *testing.T) {
	c, a, p := nativeController(t)
	a.releaseErr = errors.New("route cleanup failed")
	if err := c.Delete(context.Background(), p.ID, p.Revision); err == nil {
		t.Fatal("cleanup failure ignored")
	}
	entry := c.List()[0]
	if entry.Policy.Enabled || entry.Policy.Revision != p.Revision+1 || entry.State.Phase != "recovery-required" {
		t.Fatal(entry)
	}
	if err := c.Delete(context.Background(), p.ID, p.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	a.releaseErr = nil
	if err := c.Delete(context.Background(), p.ID, entry.Policy.Revision); err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 0 || a.device != "wan1" || a.releases != 2 {
		t.Fatal(c.List(), a)
	}
}
func TestNativeDisabledPolicyRetriesCleanup(t *testing.T) {
	c, a, p := nativeController(t)
	p.Enabled = false
	a.releaseErr = errors.New("busy")
	saved, err := c.Put(context.Background(), p)
	if err == nil || saved.Enabled {
		t.Fatal(saved, err)
	}
	a.releaseErr = nil
	c.Tick(context.Background())
	if entry := c.List()[0]; entry.Policy.Enabled || entry.State.Phase != "disabled" || a.releases != 2 {
		t.Fatal(entry, a.releases)
	}
}

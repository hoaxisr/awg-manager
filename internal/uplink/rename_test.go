package uplink

import (
	"context"
	"errors"
	"testing"
)

type renameAdapter struct {
	*testAdapter
	targets map[string]bool
}

func (a *renameAdapter) TargetExists(_ context.Context, t Target) (bool, error) {
	return a.targets[t.Ref], nil
}

func TestRenameKeepsStablePolicyAndRevision(t *testing.T) {
	c, a, _, _ := fixture(t)
	lookup := &renameAdapter{a, map[string]bool{"native": true}}
	c.adapters["test"] = lookup
	p := c.List()[0].Policy
	next := p.Target
	next.Ref = "renamed"
	if err := c.RenameTarget(context.Background(), p.Target, next, func() error { delete(lookup.targets, "native"); lookup.targets["renamed"] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	got := c.List()[0].Policy
	if got.ID != p.ID || got.Revision != p.Revision+1 || got.Target != next || c.store.renameIntent() != nil {
		t.Fatal(got)
	}
	reopened, err := OpenStore(c.store.path)
	if err != nil || reopened.List()[0].Target != next {
		t.Fatal(err)
	}
}

func TestRenameCrashRecoveryAndAmbiguity(t *testing.T) {
	for _, stage := range []string{"before", "after", "ambiguous"} {
		t.Run(stage, func(t *testing.T) {
			c, a, r, _ := fixture(t)
			p := c.List()[0].Policy
			next := p.Target
			next.Ref = "next"
			if _, err := c.store.beginRename(p.Target, next); err != nil {
				t.Fatal(err)
			}
			disk, err := OpenStore(c.store.path)
			if err != nil {
				t.Fatal(err)
			}
			targets := map[string]bool{"native": stage != "after", "next": stage != "before"}
			lookup := &renameAdapter{a, targets}
			restarted := New(disk, r, map[string]Adapter{"test": lookup})
			restarted.Tick(context.Background())
			got := restarted.List()[0]
			if stage == "ambiguous" {
				if got.State.Phase != "recovery-required" || len(a.activated) > 0 {
					t.Fatal(got)
				}
				if _, err := disk.Put(p); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
				return
			}
			if disk.renameIntent() != nil {
				t.Fatal("intent not cleared")
			}
			if (stage == "after") != (got.Policy.Target == next) {
				t.Fatal(got)
			}
		})
	}
}

func TestRenameJournalWriteFailureDoesNotMutateEngine(t *testing.T) {
	c, a, _, _ := fixture(t)
	c.adapters["test"] = &renameAdapter{a, map[string]bool{"native": true}}
	c.store.path = t.TempDir()
	p := c.List()[0].Policy
	next := p.Target
	next.Ref = "next"
	called := false
	if err := c.RenameTarget(context.Background(), p.Target, next, func() error { called = true; return nil }); err == nil || called {
		t.Fatal(err, called)
	}
}

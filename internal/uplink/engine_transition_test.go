package uplink

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEngineTransitionPreservesPolicyAndRejectsMigration(t *testing.T) {
	c, _, _, _ := fixture(t)
	before := c.store.List()[0]
	guard, err := c.AcquireEngineTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.CheckTarget("other"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := guard.CheckTarget("test"); err != nil {
		t.Fatal(err)
	}
	guard.Release()
	guard.Release()
	after := c.store.List()[0]
	if after.Revision != before.Revision || after.Target != before.Target || !after.Enabled {
		t.Fatal(after)
	}
	after.Enabled = false
	if _, err := c.Put(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	guard, err = c.AcquireEngineTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	if err := guard.CheckTarget("other"); err != nil {
		t.Fatal(err)
	}
}

func TestEngineTransitionExcludesPolicyMutationsAndSupportsCancellation(t *testing.T) {
	c, _, _, _ := fixture(t)
	guard, err := c.AcquireEngineTransition(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := c.AcquireEngineTransition(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	p := c.store.List()[0]
	p.Enabled = false
	started := make(chan struct{})
	go func() { close(started); _, err := c.Put(context.Background(), p); done <- err }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("mutation entered during transition: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	guard.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("transition did not release policy mutation")
	}
}

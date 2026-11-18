package firc

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManualStopSurvivesOperatorRestart(t *testing.T) {
	m, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	root := t.TempDir()
	op := NewOperator(Options{ConfigDir: root})
	op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
	starts := 0
	op.SetLifecycleHooks(func(context.Context) error { starts++; return nil }, func(context.Context) error {
		m.mu.Lock()
		m.failHealth = true
		m.mu.Unlock()
		return nil
	})
	if err := op.StopByUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := op.Start(context.Background()); !errors.Is(err, ErrManuallyDisabled) {
			t.Fatalf("automatic start: %v", err)
		}
	}
	fresh := NewOperator(Options{ConfigDir: root})
	fresh.SetLifecycleHooks(func(context.Context) error { starts++; return nil }, nil)
	if err := fresh.Start(context.Background()); !errors.Is(err, ErrManuallyDisabled) {
		t.Fatalf("restart: %v", err)
	}
	if starts != 0 {
		t.Fatal("disabled core launched")
	}
	if err := fresh.StartByUser(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fresh.ManuallyDisabled() || starts != 1 {
		t.Fatal("explicit enable failed")
	}
}

func TestFailedManualStopKeepsDisabledIntent(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	op.SetLifecycleHooks(nil, func(context.Context) error { return errors.New("stop failed") })
	if err := op.StopByUser(context.Background()); err == nil {
		t.Fatal("expected stop error")
	}
	if !op.ManuallyDisabled() {
		t.Fatal("intent lost")
	}
	op.SetLifecycleHooks(func(context.Context) error { return errors.New("start failed") }, nil)
	if err := op.StartByUser(context.Background()); err == nil {
		t.Fatal("expected start error")
	}
	if !op.ManuallyDisabled() {
		t.Fatal("failed enable lost disabled intent")
	}
}

func TestInternalStopDoesNotDisable(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	op.SetLifecycleHooks(nil, func(context.Context) error { return nil })
	if err := op.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if op.ManuallyDisabled() {
		t.Fatal("internal shutdown changed user intent")
	}
}

func TestPackageOperationExcludesStart(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	started := make(chan struct{})
	op.SetLifecycleHooks(func(context.Context) error { close(started); return nil }, nil)
	unlock := op.LockRuntimeMutation()
	done := make(chan error, 1)
	go func() { done <- op.Start(context.Background()) }()
	select {
	case <-started:
		unlock()
		t.Fatal("start overlapped package operation")
	case <-time.After(30 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("start did not resume")
	}
}

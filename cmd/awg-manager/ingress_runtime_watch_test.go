package main

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestWatchDigestChanges_PublishesOnlyOnChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var value atomic.Value
		value.Store("a")
		var calls atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			watchDigestChanges(ctx, 5*time.Millisecond, func() string { return value.Load().(string) }, func() { calls.Add(1) })
			close(done)
		}()

		time.Sleep(40 * time.Millisecond)
		if n := calls.Load(); n != 0 {
			t.Fatalf("steady state fired %d calls", n)
		}
		value.Store("b")
		deadline := time.Now().Add(time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(40 * time.Millisecond)
		if n := calls.Load(); n != 1 {
			t.Fatalf("one change fired %d calls, want 1", n)
		}
		cancel()
		<-done
	})
}

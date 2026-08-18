package backend

import (
	"context"
	"testing"
	"time"
)

func TestKernelBackend_Basic(t *testing.T) {
	b := NewKernel()
	ctx := context.Background()

	// Start/Stop call /opt/sbin/ip — will fail on dev machines without it.
	// We just verify they return an error (not panic).
	if err := b.Start(ctx, "test_nonexistent"); err == nil {
		t.Error("Start() should return error without ip command or kernel module")
	}

	if err := b.Stop(ctx, "test_nonexistent"); err == nil {
		t.Error("Stop() should return error without ip command or interface")
	}

	// IsRunning checks /sys/class/net — non-existent interface returns false.
	running, pid := b.IsRunning(ctx, "test_nonexistent")
	if running || pid != 0 {
		t.Error("IsRunning() should return false, 0 for non-existent interface")
	}

	// WaitReady times out for non-existent interface.
	if err := b.WaitReady(ctx, "test_nonexistent", 100*time.Millisecond); err == nil {
		t.Error("WaitReady() should return error for non-existent interface")
	}
}

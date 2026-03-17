package backend

import (
	"context"
	"testing"
	"time"
)

func TestType_String(t *testing.T) {
	tests := []struct {
		typ  Type
		want string
	}{
		{TypeKernel, "kernel"},
		{Type(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.typ.String(); got != tt.want {
				t.Errorf("Type.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKernelBackend_Basic(t *testing.T) {
	b := NewKernel()
	ctx := context.Background()

	if b.Type() != TypeKernel {
		t.Error("Type() should return TypeKernel")
	}

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

type testLogger struct {
	infos []string
	warns []string
}

func (l *testLogger) Info(msg string, fields ...map[string]interface{}) {
	l.infos = append(l.infos, msg)
}

func (l *testLogger) Warn(msg string, fields ...map[string]interface{}) {
	l.warns = append(l.warns, msg)
}

func TestNew(t *testing.T) {
	log := &testLogger{}
	b := New(log)
	if b.Type() != TypeKernel {
		t.Errorf("New() type = %v, want TypeKernel", b.Type())
	}
}

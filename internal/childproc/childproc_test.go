package childproc

import (
	"os/exec"
	"testing"
)

func TestNewInstanceID(t *testing.T) {
	a := NewInstanceID()
	b := NewInstanceID()
	if len(a) != 24 {
		t.Fatalf("want 24 hex chars, got %d (%q)", len(a), a)
	}
	if a == b {
		t.Fatal("two ids collided")
	}
}

func TestRingBuffer_KeepsLastN(t *testing.T) {
	b := NewRingBuffer(3)
	for _, l := range []string{"1", "2", "3", "4", "5"} {
		b.WriteLine(l)
	}
	if got := b.String(); got != "3\n4\n5" {
		t.Fatalf("String: want last 3, got %q", got)
	}
	if got := b.LastLines(2); got != "4\n5" {
		t.Fatalf("LastLines(2): want %q, got %q", "4\n5", got)
	}
	if got := b.LastLines(10); got != "3\n4\n5" {
		t.Fatalf("LastLines(over): want whole buffer, got %q", got)
	}
	if got := b.LastLines(0); got != "" {
		t.Fatalf("LastLines(0): want empty, got %q", got)
	}
	b.Reset()
	if got := b.String(); got != "" {
		t.Fatalf("Reset: want empty, got %q", got)
	}
}

func TestIsAlive(t *testing.T) {
	if IsAlive(0) || IsAlive(-1) {
		t.Fatal("IsAlive must be false for non-positive pid")
	}
	// A live child is alive; once reaped it is not — this exercises the
	// Signal(0) probe on both the linux and non-linux implementations.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	pid := cmd.Process.Pid
	if !IsAlive(pid) {
		t.Fatalf("running child pid %d reported not alive", pid)
	}
	_ = Kill(pid)
	_ = cmd.Wait()
	if IsAlive(pid) {
		t.Fatalf("reaped child pid %d reported alive", pid)
	}
}

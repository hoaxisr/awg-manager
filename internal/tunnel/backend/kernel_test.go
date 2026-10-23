package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/netdev"
)

func TestKernelBackend_Basic(t *testing.T) {
	b := NewKernel(&netdev.SwapGate{})
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

// F721: тип устройства — из uevent (формы со стенда 23.10: amneziawg ставит
// DEVTYPE первой строкой, у plain tun строки DEVTYPE нет вовсе).
func TestDevType(t *testing.T) {
	root := t.TempDir()
	for iface, uevent := range map[string]string{
		"opkgtun7": "DEVTYPE=amneziawg\nINTERFACE=opkgtun7\nIFINDEX=81\n",
		"opkgtun8": "INTERFACE=opkgtun8\nIFINDEX=82\n",
		"nwg1":     "DEVTYPE=wireguard\nINTERFACE=nwg1\nIFINDEX=9\n",
	} {
		if err := os.MkdirAll(filepath.Join(root, iface), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, iface, "uevent"), []byte(uevent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for iface, want := range map[string]string{
		"opkgtun7": "amneziawg",
		"opkgtun8": "",
		"nwg1":     "wireguard",
		"opkgtun9": "", // устройства нет
	} {
		if got := devType(root, iface); got != want {
			t.Errorf("devType(%s) = %q, want %q", iface, got, want)
		}
	}
}

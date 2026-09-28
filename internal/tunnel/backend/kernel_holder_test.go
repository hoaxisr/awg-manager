package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// stubKernelRun подменяет шов ip и отдаёт журнал вызовов.
func stubKernelRun(t *testing.T) *[]string {
	t.Helper()
	old := kernelRun
	var calls []string
	kernelRun = func(_ context.Context, name string, args ...string) (*exec.Result, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return &exec.Result{}, nil
	}
	t.Cleanup(func() { kernelRun = old })
	return &calls
}

// stubTunHolder подменяет детектор держателя и отдаёт счётчик сканов.
func stubTunHolder(t *testing.T, held *HeldError) *int {
	t.Helper()
	old := tunHolder
	scans := 0
	tunHolder = func(string) *HeldError { scans++; return held }
	t.Cleanup(func() { tunHolder = old })
	return &scans
}

// stubIfaceExists подменяет проверку /sys/class/net/<iface>.
func stubIfaceExists(t *testing.T, exists bool) {
	t.Helper()
	old := ifaceExists
	ifaceExists = func(string) bool { return exists }
	t.Cleanup(func() { ifaceExists = old })
}

// F500: устройство на нашем номере держит чужая программа — старт отказывает
// ДО единственного `ip link del`, типизированной ошибкой с pid и именем.
func TestStart_HeldByForeignProcess_RefusesWithoutDelete(t *testing.T) {
	calls := stubKernelRun(t)
	stubIfaceExists(t, true)
	stubTunHolder(t, &HeldError{Iface: "opkgtun7", PID: 4242, Comm: "csqtt"})

	err := NewKernel().Start(context.Background(), "opkgtun7")

	var held *HeldError
	if !errors.As(err, &held) || held.PID != 4242 || held.Comm != "csqtt" {
		t.Fatalf("err = %v, want *HeldError{PID:4242, Comm:csqtt}", err)
	}
	if !strings.Contains(err.Error(), "занят сторонней программой") {
		t.Fatalf("текст отказа не для человека: %q", err.Error())
	}
	if want := []string{"/opt/sbin/ip -d link show dev opkgtun7"}; !slices.Equal(*calls, want) {
		t.Fatalf("ip calls = %v, want только проверку типа %v", *calls, want)
	}
}

// Plain tun без держателя (NDMS пересоздал OpkgTun после ребута) — прежнее
// лечение: снести и создать amneziawg.
func TestStart_UnheldTun_DeletesAndRecreates(t *testing.T) {
	calls := stubKernelRun(t)
	stubIfaceExists(t, true)
	stubTunHolder(t, nil)

	if err := NewKernel().Start(context.Background(), "opkgtun7"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/opt/sbin/ip -d link show dev opkgtun7",
		"/opt/sbin/ip link del dev opkgtun7",
		"/opt/sbin/ip link add dev opkgtun7 type amneziawg",
	}
	if !slices.Equal(*calls, want) {
		t.Fatalf("ip calls = %v, want %v", *calls, want)
	}
}

// Тот же гард в Stop: его зовёт откат неудавшегося старта (rollbackStart) —
// без гарда откат снёс бы то самое чужое устройство, из-за которого старт и
// отказал.
func TestStop_HeldByForeignProcess_Refuses(t *testing.T) {
	calls := stubKernelRun(t)
	stubIfaceExists(t, true)
	stubTunHolder(t, &HeldError{Iface: "opkgtun7", PID: 4242, Comm: "csqtt"})

	err := NewKernel().Stop(context.Background(), "opkgtun7")

	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want *HeldError", err)
	}
	if want := []string{"/opt/sbin/ip -d link show dev opkgtun7"}; !slices.Equal(*calls, want) {
		t.Fatalf("ip calls = %v, want только проверку типа %v", *calls, want)
	}
}

// Скан /proc нужен только существующему не-amneziawg устройству: устройства
// нет — держать нечего; наше amneziawg чужая программа не открывает.
func TestHolderScan_OnlyForExistingForeignTypedDevice(t *testing.T) {
	ctx := context.Background()
	t.Run("устройства нет", func(t *testing.T) {
		stubKernelRun(t)
		stubIfaceExists(t, false)
		scans := stubTunHolder(t, nil)
		_ = NewKernel().Start(ctx, "opkgtun7")
		_ = NewKernel().Stop(ctx, "opkgtun7")
		if *scans != 0 {
			t.Fatalf("сканов /proc %d, want 0", *scans)
		}
	})
	t.Run("наше amneziawg", func(t *testing.T) {
		old := kernelRun
		var calls []string
		kernelRun = func(_ context.Context, name string, args ...string) (*exec.Result, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return &exec.Result{Stdout: "7: opkgtun7: <POINTOPOINT,NOARP,UP> mtu 1420\n    amneziawg"}, nil
		}
		t.Cleanup(func() { kernelRun = old })
		stubIfaceExists(t, true)
		scans := stubTunHolder(t, &HeldError{Iface: "opkgtun7", PID: 1, Comm: "x"})
		if err := NewKernel().Stop(ctx, "opkgtun7"); err != nil {
			t.Fatalf("Stop нашего amneziawg: %v", err)
		}
		if *scans != 0 || !slices.Contains(calls, "/opt/sbin/ip link del dev opkgtun7") {
			t.Fatalf("сканов %d (want 0), calls %v", *scans, calls)
		}
	})
}

// Детектор по дереву /proc: fd на /dev/net/tun + строка `iff:\t<имя>` в
// fdinfo (формат стенда KN-1810, ядро 4.9 — Task 6). Ловушка префикса:
// держатель opkgtun70 не должен блокировать opkgtun7.
func TestFindTunHolder_ProcTree(t *testing.T) {
	root := t.TempDir()
	mk := func(pid, fd, target, fdinfo, comm string) {
		t.Helper()
		dir := filepath.Join(root, pid)
		for _, d := range []string{"fd", "fdinfo"} {
			if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(target, filepath.Join(dir, "fd", fd)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fdinfo", fd), []byte(fdinfo), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("100", "3", "/dev/null", "pos:\t0\nflags:\t0100002\nmnt_id:\t20\n", "sh\n")
	// fd не на /dev/net/tun, но с той же строкой iff: — не держатель
	// (сторожит проверку readlink; pid раньше настоящего держателя в обходе).
	mk("150", "6", "/dev/null", "pos:\t0\nflags:\t0100002\nmnt_id:\t20\niff:\topkgtun7\n", "decoy\n")
	mk("200", "5", "/dev/net/tun", "pos:\t0\nflags:\t0100002\nmnt_id:\t20\niff:\topkgtun70\n", "other\n")
	mk("300", "4", "/dev/net/tun", "pos:\t0\nflags:\t0100002\nmnt_id:\t20\niff:\topkgtun7\n", "csqtt\n")

	got := findTunHolder(root, "opkgtun7")
	if got == nil || got.PID != 300 || got.Comm != "csqtt" || got.Iface != "opkgtun7" {
		t.Fatalf("holder = %+v, want {opkgtun7 300 csqtt}", got)
	}
	if h := findTunHolder(root, "opkgtun8"); h != nil {
		t.Fatalf("ложный держатель: %+v", h)
	}

	// comm не прочитался (процесс ушёл) — «?», без двойного пробела в тексте.
	mk("400", "4", "/dev/net/tun", "iff:\topkgtun9\n", "")
	if err := os.Remove(filepath.Join(root, "400", "comm")); err != nil {
		t.Fatal(err)
	}
	h := findTunHolder(root, "opkgtun9")
	if h == nil || h.Comm != "?" || !strings.Contains(h.Error(), "программой ? (pid 400)") {
		t.Fatalf("holder = %+v, want Comm «?»", h)
	}
}

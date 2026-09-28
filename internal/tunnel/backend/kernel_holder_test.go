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

func stubTunHolder(t *testing.T, held *HeldError) {
	t.Helper()
	old := tunHolder
	tunHolder = func(string) *HeldError { return held }
	t.Cleanup(func() { tunHolder = old })
}

// F500: устройство на нашем номере держит чужая программа — старт отказывает
// ДО единственного `ip link del`, типизированной ошибкой с pid и именем.
func TestStart_HeldByForeignProcess_RefusesWithoutDelete(t *testing.T) {
	calls := stubKernelRun(t)
	stubTunHolder(t, &HeldError{Iface: "opkgtun7", PID: 4242, Comm: "csqtt"})

	err := NewKernel().Start(context.Background(), "opkgtun7")

	var held *HeldError
	if !errors.As(err, &held) || held.PID != 4242 || held.Comm != "csqtt" {
		t.Fatalf("err = %v, want *HeldError{PID:4242, Comm:csqtt}", err)
	}
	if !strings.Contains(err.Error(), "занят сторонней программой") {
		t.Fatalf("текст отказа не для человека: %q", err.Error())
	}
	if len(*calls) != 0 {
		t.Fatalf("ip вызван при чужом держателе: %v", *calls)
	}
}

// Plain tun без держателя (NDMS пересоздал OpkgTun после ребута) — прежнее
// лечение: снести и создать amneziawg.
func TestStart_UnheldTun_DeletesAndRecreates(t *testing.T) {
	calls := stubKernelRun(t)
	stubTunHolder(t, nil)

	if err := NewKernel().Start(context.Background(), "opkgtun7"); err != nil {
		t.Fatal(err)
	}
	want := []string{
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
	stubTunHolder(t, &HeldError{Iface: "opkgtun7", PID: 4242, Comm: "csqtt"})

	err := NewKernel().Stop(context.Background(), "opkgtun7")

	var held *HeldError
	if !errors.As(err, &held) {
		t.Fatalf("err = %v, want *HeldError", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("ip вызван при чужом держателе: %v", *calls)
	}
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
	mk("200", "5", "/dev/net/tun", "pos:\t0\nflags:\t0100002\nmnt_id:\t20\niff:\topkgtun70\n", "other\n")
	mk("300", "4", "/dev/net/tun", "pos:\t0\nflags:\t0100002\nmnt_id:\t20\niff:\topkgtun7\n", "csqtt\n")

	got := findTunHolder(root, "opkgtun7")
	if got == nil || got.PID != 300 || got.Comm != "csqtt" || got.Iface != "opkgtun7" {
		t.Fatalf("holder = %+v, want {opkgtun7 300 csqtt}", got)
	}
	if h := findTunHolder(root, "opkgtun8"); h != nil {
		t.Fatalf("ложный держатель: %+v", h)
	}
}

// Package backend provides tunnel interface management via the AmneziaWG
// kernel module.
package backend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	kernelPollInterval = 50 * time.Millisecond
)

// KernelBackend manages AmneziaWG kernel module interfaces.
// Uses ip link add/del type amneziawg for interface management.
type KernelBackend struct{}

// NewKernel creates a new kernel backend.
func NewKernel() *KernelBackend {
	return &KernelBackend{}
}

// HeldError — устройство iface открыто сторонним процессом (fd на
// /dev/net/tun): `ip link del` снёс бы интерфейс чужой программы (csqtt,
// issue #935), поэтому старт и остановка отказывают (F500). Ошибка
// типизирована: оператор и тесты отличают её от провала ip.
type HeldError struct {
	Iface string
	PID   int
	Comm  string
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("интерфейс %s занят сторонней программой %s (pid %d)", e.Iface, e.Comm, e.PID)
}

// Швы для тестов: бэкенд зовёт ip и читает /proc напрямую.
var (
	kernelRun = exec.Run
	tunHolder = func(iface string) *HeldError { return findTunHolder("/proc", iface) }
)

// findTunHolder ищет процесс, держащий tun-устройство iface открытым: у него
// есть fd на /dev/net/tun, чей /proc/<pid>/fdinfo/<fd> содержит строку
// `iff:\t<iface>\n` (drivers/net/tun.c, tun_chr_show_fdinfo; формат и путь fd
// проверены на стенде KN-1810, ядро 4.9 — docs/issues/wayfinder-foreign-iface/
// f500-stand-probe.md). Persistent-устройство без держателя — то, что NDMS
// пересоздаёт после ребута, — fd ни у кого нет → nil. Перевод строки в
// образце обязателен: иначе opkgtun70 совпадал бы с opkgtun7. Ошибки чтения
// (процесс исчез, чужой uid) — пропуск: нет доказательства держателя.
func findTunHolder(procRoot, iface string) *HeldError {
	want := "iff:\t" + iface + "\n"
	fdDirs, _ := filepath.Glob(filepath.Join(procRoot, "[0-9]*", "fd"))
	for _, fdDir := range fdDirs {
		entries, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		pidDir := filepath.Dir(fdDir)
		for _, e := range entries {
			if target, err := os.Readlink(filepath.Join(fdDir, e.Name())); err != nil || target != "/dev/net/tun" {
				continue
			}
			info, err := os.ReadFile(filepath.Join(pidDir, "fdinfo", e.Name()))
			if err != nil || !strings.Contains(string(info), want) {
				continue
			}
			pid, _ := strconv.Atoi(filepath.Base(pidDir))
			comm, _ := os.ReadFile(filepath.Join(pidDir, "comm"))
			return &HeldError{Iface: iface, PID: pid, Comm: strings.TrimSpace(string(comm))}
		}
	}
	return nil
}

// Start creates a kernel AmneziaWG interface.
// If interface already exists as amneziawg — does nothing (idempotent).
// If interface exists as wrong type (tun) — deletes and recreates, unless a
// foreign process holds it open (HeldError, F500).
// If interface doesn't exist — creates new.
func (b *KernelBackend) Start(ctx context.Context, ifaceName string) error {
	if running, _ := b.IsRunning(ctx, ifaceName); running {
		return nil // Already amneziawg, nothing to do
	}

	// Не-amneziawg устройство на нашем имени — обычно plain tun, который NDMS
	// пересоздал после ребута по сохранённой записи OpkgTun (держателя нет):
	// его сносим. Держатель есть — номер занят чужой программой, отказ.
	if held := tunHolder(ifaceName); held != nil {
		return held
	}
	_, _ = kernelRun(ctx, "/opt/sbin/ip", "link", "del", "dev", ifaceName)

	result, err := kernelRun(ctx, "/opt/sbin/ip", "link", "add", "dev", ifaceName, "type", "amneziawg")
	if err != nil {
		return fmt.Errorf("create kernel interface: %w", exec.FormatError(result, err))
	}
	return nil
}

// Stop removes the kernel AmneziaWG interface. Тот же гард держателя, что в
// Start: Stop зовёт и откат неудавшегося старта (ops.rollbackStart), и
// остановка туннеля, чей номер тем временем занял чужой tun.
func (b *KernelBackend) Stop(ctx context.Context, ifaceName string) error {
	if held := tunHolder(ifaceName); held != nil {
		return held
	}
	result, err := kernelRun(ctx, "/opt/sbin/ip", "link", "del", "dev", ifaceName)
	if err != nil {
		return fmt.Errorf("delete kernel interface: %w", exec.FormatError(result, err))
	}
	return nil
}

// IsRunning checks if the kernel interface exists AND is amneziawg type.
// Returns (running, pid) where pid is always 0 for kernel backend.
// At boot NDMS recreates opkgtun* devices as plain "tun" — we must verify the type.
func (b *KernelBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	if _, err := os.Stat("/sys/class/net/" + ifaceName); err != nil {
		return false, 0
	}

	// Verify interface is actually amneziawg (not plain tun recreated by NDMS)
	result, err := kernelRun(ctx, "/opt/sbin/ip", "-d", "link", "show", "dev", ifaceName)
	if err != nil {
		return false, 0
	}

	return strings.Contains(result.Stdout, "amneziawg"), 0
}

// WaitReady waits for the kernel interface to be ready.
// For kernel backend, only waits for interface to appear in /sys/class/net.
func (b *KernelBackend) WaitReady(ctx context.Context, ifaceName string, timeout time.Duration) error {
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(kernelPollInterval)
	defer ticker.Stop()

	for {
		// Check interface exists
		if _, err := os.Stat("/sys/class/net/" + ifaceName); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timeout waiting for kernel interface %s", ifaceName)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

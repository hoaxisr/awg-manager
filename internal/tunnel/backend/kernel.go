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

	"github.com/hoaxisr/awg-manager/internal/netdev"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	kernelPollInterval = 50 * time.Millisecond
)

// KernelBackend manages AmneziaWG kernel module interfaces.
// Устройства снимаются и создаются только под барьером gate (D-N1): пока
// запись OpkgTunN живёт без устройства, наши списки интерфейсов ждут.
type KernelBackend struct {
	gate *netdev.SwapGate
}

// NewKernel creates a new kernel backend. gate — барьер списков, один на
// процесс (общий с query.InterfaceStore).
func NewKernel(gate *netdev.SwapGate) *KernelBackend {
	if gate == nil {
		panic("backend.NewKernel: SwapGate обязателен")
	}
	return &KernelBackend{gate: gate}
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

// Швы для тестов: бэкенд зовёт ip (только чтение: `ip -d link show`; снос и
// создание — у netdev.Swapper) и читает /proc и /sys напрямую.
var (
	kernelRun   = exec.Run
	tunHolder   = func(iface string) *HeldError { return findTunHolder("/proc", iface) }
	ifaceExists = func(iface string) bool {
		_, err := os.Stat("/sys/class/net/" + iface)
		return err == nil
	}
)

// FindTunHolder — держатель tun-устройства iface или nil (см. findTunHolder).
// Нужен тем, кто сам решает о судьбе устройства: адаптер создания OpkgTunN
// называет держателя, когда opkgtunN ещё жив (F569).
func FindTunHolder(iface string) *HeldError { return tunHolder(iface) }

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
			name := "?" // comm не прочитался — процесс ушёл или чужой uid
			if comm, err := os.ReadFile(filepath.Join(pidDir, "comm")); err == nil {
				if c := strings.TrimSpace(string(comm)); c != "" {
					name = c
				}
			}
			return &HeldError{Iface: iface, PID: pid, Comm: name}
		}
	}
	return nil
}

// Start creates a kernel AmneziaWG interface.
// If interface already exists as amneziawg — does nothing (idempotent).
// If interface exists as wrong type (tun) — deletes and recreates, unless a
// foreign process holds it open (HeldError, F500).
// If interface doesn't exist — creates new.
//
// Проверки (тип, наличие, обход /proc) — до барьера (N11); снос и создание —
// одной подменой под ним.
func (b *KernelBackend) Start(ctx context.Context, ifaceName string) error {
	if running, _ := b.IsRunning(ctx, ifaceName); running {
		return nil // Already amneziawg, nothing to do
	}

	// Не-amneziawg устройство на нашем имени — обычно plain tun, который NDMS
	// пересоздал после ребута по сохранённой записи OpkgTun (держателя нет):
	// его сносим. Держатель есть — номер занят чужой программой, отказ.
	// Устройства нет — держать нечего, обход /proc не нужен.
	exists := ifaceExists(ifaceName)
	if exists {
		if held := tunHolder(ifaceName); held != nil {
			return held
		}
	}
	return b.gate.Hold(ctx, func(sw netdev.Swapper) error {
		if exists {
			if err := sw.LinkDel(ifaceName); err != nil {
				return fmt.Errorf("delete stale interface: %w", err)
			}
		}
		if err := sw.LinkAdd(ifaceName, "amneziawg"); err != nil {
			return fmt.Errorf("create kernel interface: %w", err)
		}
		return nil
	})
}

// Stop removes the kernel AmneziaWG interface. Тот же гард держателя, что в
// Start: Stop зовёт и откат неудавшегося старта (ops.rollbackStart), и
// остановка туннеля, чей номер тем временем занял чужой tun.
//
// Обход /proc — только существующему не-amneziawg устройству: отсутствующее
// держать некому, наше amneziawg чужая программа через /dev/net/tun не
// открывает.
func (b *KernelBackend) Stop(ctx context.Context, ifaceName string) error {
	if held := b.foreignHolder(ctx, ifaceName); held != nil {
		return held
	}
	return b.gate.Hold(ctx, func(sw netdev.Swapper) error {
		if err := sw.LinkDel(ifaceName); err != nil {
			return fmt.Errorf("delete kernel interface: %w", err)
		}
		return nil
	})
}

// Recreate — свежее amneziawg на имени одной подменой под барьером: снос
// существующего устройства и создание — в одном Hold, так что запись OpkgTunN
// не остаётся без устройства для наших читателей. Нужен Reconcile, когда
// живое amneziawg оказалось под только что созданной записью (Start его не
// тронул бы). Не-amneziawg с чужим держателем — HeldError (F500).
func (b *KernelBackend) Recreate(ctx context.Context, ifaceName string) error {
	if held := b.foreignHolder(ctx, ifaceName); held != nil {
		return held
	}
	exists := ifaceExists(ifaceName)
	return b.gate.Hold(ctx, func(sw netdev.Swapper) error {
		if exists {
			if err := sw.LinkDel(ifaceName); err != nil {
				return fmt.Errorf("recreate kernel interface: %w", err)
			}
		}
		if err := sw.LinkAdd(ifaceName, "amneziawg"); err != nil {
			return fmt.Errorf("recreate kernel interface: %w", err)
		}
		return nil
	})
}

// StopIfPresent — Stop, если устройство есть; нет — nil (снимать нечего,
// например NDMS снял tun вместе с записью).
func (b *KernelBackend) StopIfPresent(ctx context.Context, ifaceName string) error {
	if !ifaceExists(ifaceName) {
		return nil
	}
	return b.Stop(ctx, ifaceName)
}

// ReplaceWithTun ставит на имя plain persistent tun вместо нашего устройства
// одной подменой под барьером: запись OpkgTunN не остаётся без устройства
// ни для наших читателей, ни для `no interface` (П16/C3a, стенд П4 20/20 без
// C). Устройства нет — только `tuntap add`. Чужой держатель — HeldError, ip
// не зовётся. Отказ `tuntap add` — ошибка; что делать с записью, решает
// вызывающий.
func (b *KernelBackend) ReplaceWithTun(ctx context.Context, ifaceName string) error {
	if held := b.foreignHolder(ctx, ifaceName); held != nil {
		return held
	}
	exists := ifaceExists(ifaceName)
	return b.gate.Hold(ctx, func(sw netdev.Swapper) error {
		if exists {
			if err := sw.LinkDel(ifaceName); err != nil {
				return fmt.Errorf("replace with tun: %w", err)
			}
		}
		if err := sw.TuntapAdd(ifaceName); err != nil {
			return fmt.Errorf("replace with tun: %w", err)
		}
		return nil
	})
}

// foreignHolder — держатель существующего не-amneziawg устройства (F500).
func (b *KernelBackend) foreignHolder(ctx context.Context, ifaceName string) *HeldError {
	if !ifaceExists(ifaceName) {
		return nil
	}
	if running, _ := b.IsRunning(ctx, ifaceName); running {
		return nil
	}
	return tunHolder(ifaceName)
}

// IsRunning checks if the kernel interface exists AND is amneziawg type.
// Returns (running, pid) where pid is always 0 for kernel backend.
// At boot NDMS recreates opkgtun* devices as plain "tun" — we must verify the type.
func (b *KernelBackend) IsRunning(ctx context.Context, ifaceName string) (bool, int) {
	if !ifaceExists(ifaceName) {
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

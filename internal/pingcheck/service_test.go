package pingcheck

import (
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func newTunnelStore(t *testing.T) *storage.AWGTunnelStore {
	t.Helper()
	dir := t.TempDir()
	return storage.NewAWGTunnelStoreWithLockDir(dir, filepath.Join(dir, "locks"))
}

// Требование 8: health бьёт по живому интерфейсу зеркальной записи. Id
// raw-записи цифр не несёт, поэтому общий резолв даёт для неё opkgtun0 —
// чужой существующий интерфейс, а не безобидный промах.
func TestResolveIfaceName_WdttRawUsesLiveIface(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        "wdtt-raw",
		RawKernelIface: "opkgtun18",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store}

	if got := s.resolveIfaceName("wdttraw-de"); got != "opkgtun18" {
		t.Fatalf("resolveIfaceName = %q, want opkgtun18", got)
	}
}

// Запись без живого имени интерфейса (инстанс ещё не поднялся) остаётся на
// общем резолве — ветка не должна возвращать пустую строку.
func TestResolveIfaceName_WdttRawWithoutLiveIface(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID:      "wdttraw-de",
		Name:    "Германия",
		Backend: "wdtt-raw",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store}

	if got := s.resolveIfaceName("wdttraw-de"); got != "opkgtun0" {
		t.Fatalf("resolveIfaceName = %q, want opkgtun0", got)
	}
}

// Kernel-запись общий резолв не трогает.
func TestResolveIfaceName_KernelUnchanged(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID:      "awg3",
		Name:    "Kernel",
		Backend: "kernel",
		// Поле заполнено нарочно: kernel-ветка обязана его игнорировать.
		RawKernelIface: "opkgtun18",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store}

	if got := s.resolveIfaceName("awg3"); got != "opkgtun3" {
		t.Fatalf("resolveIfaceName = %q, want opkgtun3", got)
	}
}

// Живой монитор бывает не только у kernel-туннеля: зеркальную запись
// прокси-выхода первый цикл GetStatus тоже перечисляет, и зашитое
// "kernel" врало о её природе.
func TestGetStatus_ReportsRecordBackend(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID:             "wdttraw-de",
		Name:           "Германия",
		Backend:        "wdtt-raw",
		RawKernelIface: "opkgtun18",
		PingCheck:      &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 30, FailThreshold: 3},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		tunnels:  store,
		monitors: map[string]*tunnelMonitor{"wdttraw-de": {tunnelID: "wdttraw-de", tunnelName: "Германия"}},
	}

	got := s.GetStatus()
	if len(got) != 1 {
		t.Fatalf("статусов = %d, ожидали 1: %+v", len(got), got)
	}
	if got[0].Backend != "wdtt-raw" {
		t.Fatalf("backend = %q, want wdtt-raw", got[0].Backend)
	}
}

// Запись без бэкенда (legacy) обязана остаться "kernel": подстановка пустого
// значения из записи сломала бы страницу мониторинга у старых туннелей.
func TestGetStatus_LegacyRecordStaysKernel(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID:        "awg3",
		Name:      "Legacy",
		PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 30, FailThreshold: 3},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		tunnels:  store,
		monitors: map[string]*tunnelMonitor{"awg3": {tunnelID: "awg3", tunnelName: "Legacy"}},
	}

	got := s.GetStatus()
	if len(got) != 1 {
		t.Fatalf("статусов = %d, ожидали 1: %+v", len(got), got)
	}
	if got[0].Backend != "kernel" {
		t.Fatalf("backend = %q, want kernel", got[0].Backend)
	}
}

// tunnelRunning у kernel-записи отражает флаг UP интерфейса, а не остаётся
// false навсегда (репортёр #855 принял его за признак сбоя).
func TestGetStatus_KernelTunnelRunningFollowsIfaceUp(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID:        "awg3",
		Name:      "Kernel",
		PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 30, FailThreshold: 3},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		tunnels:  store,
		monitors: map[string]*tunnelMonitor{"awg3": {tunnelID: "awg3", tunnelName: "Kernel"}},
	}
	old := ifaceUp
	t.Cleanup(func() { ifaceUp = old })

	for _, up := range []bool{true, false} {
		var asked string
		ifaceUp = func(name string) bool { asked = name; return up }
		got := s.GetStatus()
		if len(got) != 1 || got[0].TunnelRunning != up {
			t.Fatalf("up=%v: статус %+v", up, got)
		}
		wantStatus := map[bool]string{true: "alive", false: "stopped"}[up]
		if got[0].Status != wantStatus {
			t.Fatalf("up=%v: status = %q, want %q (как nwgCardStatus при !bound)", up, got[0].Status, wantStatus)
		}
		if asked != "opkgtun3" {
			t.Fatalf("спросили интерфейс %q, ожидали opkgtun3", asked)
		}
	}
}

// Дефолт шва — настоящий флаг UP: lo поднят всегда, несуществующего нет.
func TestIfaceUpDefault(t *testing.T) {
	if !ifaceUp("lo") {
		t.Fatal("lo обязан быть UP")
	}
	if ifaceUp("no-such-iface-855") {
		t.Fatal("несуществующий интерфейс не может быть UP")
	}
}

// Лежащий интерфейс во время лечения (failCount на пороге) — «recovering», не «stopped»:
// окно down/up наше собственное.
func TestGetStatus_KernelHealingWindowIsRecovering(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{
		ID: "awg3", Name: "Kernel",
		PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 30, FailThreshold: 3},
	}); err != nil {
		t.Fatal(err)
	}
	s := &Service{
		tunnels:  store,
		monitors: map[string]*tunnelMonitor{"awg3": {tunnelID: "awg3", tunnelName: "Kernel", failCount: 3}},
	}
	old := ifaceUp
	t.Cleanup(func() { ifaceUp = old })
	ifaceUp = func(string) bool { return false }
	if got := s.GetStatus(); len(got) != 1 || got[0].Status != "recovering" {
		t.Fatalf("статус %+v, ожидали recovering", got)
	}
}

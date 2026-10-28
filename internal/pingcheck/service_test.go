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

// Карточка туннеля и страница мониторинга оценивают монитор одинаково:
// окно лечения у kernel — «recovering» в обоих; у зеркала wdtt-raw порог
// счётчика лечением не является.
func TestGetTunnelPingStatus_MatchesGetStatus(t *testing.T) {
	store := newTunnelStore(t)
	for _, tn := range []*storage.AWGTunnel{
		{ID: "awg3", Name: "Kernel", PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 30, FailThreshold: 3}},
		{ID: "wdttraw-de", Name: "Зеркало", Backend: "wdtt-raw", RawKernelIface: "opkgtun18", PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Interval: 30, FailThreshold: 3}},
	} {
		if err := store.Create(tn); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{tunnels: store, monitors: map[string]*tunnelMonitor{
		"awg3":       {tunnelID: "awg3", failCount: 3, failThreshold: 3},
		"wdttraw-de": {tunnelID: "wdttraw-de", failCount: 7, failThreshold: 3},
	}}
	old := ifaceUp
	t.Cleanup(func() { ifaceUp = old })
	ifaceUp = func(string) bool { return true }

	if got := s.GetTunnelPingStatus("awg3").Status; got != "recovering" {
		t.Fatalf("карточка kernel в окне лечения: %q, want recovering", got)
	}
	if got := s.GetTunnelPingStatus("wdttraw-de").Status; got != "alive" {
		t.Fatalf("карточка зеркала за порогом: %q, want alive (лечения нет)", got)
	}
	for _, st := range s.GetStatus() {
		want := map[string]string{"awg3": "recovering", "wdttraw-de": "alive"}[st.TunnelID]
		if st.Status != want {
			t.Fatalf("мониторинг %s: %q, want %q", st.TunnelID, st.Status, want)
		}
	}
}

// Карточка показывает то, что проверяется (F749), и политики, с которых
// мониторинг снял туннель (F748).
func TestGetStatus_CheckTargetAndPolicyHolds(t *testing.T) {
	store := newTunnelStore(t)
	for _, tun := range []*storage.AWGTunnel{
		{ID: "awg3", Name: "ip", PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Target: "8.8.8.8"},
			PolicyHolds: []storage.PolicyHold{{Policy: "HydraRoute", Order: 0}, {Policy: "Policy1", Order: 2}}},
		{ID: "awg4", Name: "host", PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", Target: "example.com"}},
		{ID: "awg5", Name: "icmp", PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "icmp", Target: "1.1.1.1"}},
	} {
		if err := store.Create(tun); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{tunnels: store, monitors: map[string]*tunnelMonitor{
		"awg3": {tunnelID: "awg3"}, "awg4": {tunnelID: "awg4"}, "awg5": {tunnelID: "awg5"},
	}}

	got := map[string]TunnelStatus{}
	for _, st := range s.GetStatus() {
		got[st.TunnelID] = st
	}
	want := map[string]string{
		"awg3": storage.DefaultConnectivityCheckURL,
		"awg4": "https://example.com",
		"awg5": "1.1.1.1",
	}
	for id, w := range want {
		if got[id].CheckTarget != w {
			t.Errorf("%s: checkTarget = %q, want %q", id, got[id].CheckTarget, w)
		}
	}
	if h := got["awg3"].PolicyHolds; len(h) != 2 || h[0] != "HydraRoute" || h[1] != "Policy1" {
		t.Errorf("awg3: policyHolds = %v", h)
	}
	if h := got["awg4"].PolicyHolds; len(h) != 0 {
		t.Errorf("awg4: policyHolds = %v, ожидали пусто", h)
	}
}

// Опущенный линк — «stopped», даже если до опускания шло лечение: проверок
// на нём нет, и «recovering» висел бы всё время, пока линк лежит (ревью F750).
func TestGetStatus_LinkDownIsStoppedEvenAfterRepair(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{ID: "awg3", Name: "k",
		PingCheck: &storage.TunnelPingCheck{Enabled: true, Method: "http", FailThreshold: 3}}); err != nil {
		t.Fatal(err)
	}
	old := ifaceUp
	t.Cleanup(func() { ifaceUp = old })
	ifaceUp = func(string) bool { return false }
	// Прежде «окно лечения» (failCount на пороге при лежащем линке) было
	// «recovering»: лечение само клало линк. С F746 оно линк не трогает.
	for _, m := range []*tunnelMonitor{
		{tunnelID: "awg3", restartCount: 1, lastResult: &CheckResult{Success: false}},
		{tunnelID: "awg3", failCount: 3},
	} {
		s := &Service{tunnels: store, monitors: map[string]*tunnelMonitor{"awg3": m}}
		if got := s.GetStatus(); len(got) != 1 || got[0].Status != "stopped" {
			t.Fatalf("монитор %+v: статус = %+v, want stopped", m, got)
		}
	}
}

// Мониторинг выключен, а возврат permit не прошёл: удержание видно и без
// монитора (ревью F748).
func TestGetStatus_HoldsWithoutMonitor(t *testing.T) {
	store := newTunnelStore(t)
	if err := store.Create(&storage.AWGTunnel{ID: "awg3", Name: "k", Backend: "kernel",
		PingCheck:   &storage.TunnelPingCheck{Enabled: false},
		PolicyHolds: []storage.PolicyHold{{Policy: "HydraRoute"}}}); err != nil {
		t.Fatal(err)
	}
	s := &Service{tunnels: store, monitors: map[string]*tunnelMonitor{}}
	stubDeviceExists(t, true)
	got := s.GetStatus()
	if len(got) != 1 || len(got[0].PolicyHolds) != 1 || got[0].PolicyHolds[0] != "HydraRoute" {
		t.Fatalf("статус = %+v", got)
	}
}

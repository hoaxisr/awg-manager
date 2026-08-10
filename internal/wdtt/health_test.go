package wdtt

import (
	"testing"
	"time"
)

const (
	logZeroActive = `2026/07/23 10:15:15 [СТАТИСТИКА] Активных: 0 | Трафик: 0.00 МБ
__WDTT_EVENT__|STATS|{"active":0,"bytes_down":0,"bytes_up":0}
`
	// Хвост без статистики: клиент жив, но в видимых строках её нет.
	logNoStatsTelemetry = `2026/07/23 10:15:15 [INFO] подключение к пиру
2026/07/23 10:15:16 [INFO] авторизация VK
`
)

func TestClientPeerUnhealthy(t *testing.T) {
	started := time.Now().Add(-6 * time.Minute)
	st := ProcessStatus{Running: true, StartedAt: &started, Log: logZeroActive}
	if !clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected unhealthy with zero active workers after grace")
	}

	st.Log = sampleStatsLog
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy with active workers")
	}

	fresh := time.Now().Add(-time.Minute)
	st = ProcessStatus{Running: true, StartedAt: &fresh, Log: logZeroActive}
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy during grace period")
	}
}

// «Статистики в логе нет» — это не «активных ноль»: перезапуск здорового
// клиента (например, во время долгой VK-авторизации) недопустим.
func TestClientPeerUnhealthyIgnoresMissingTelemetry(t *testing.T) {
	started := time.Now().Add(-6 * time.Minute)
	for name, log := range map[string]string{
		"пустой лог":     "",
		"без статистики": logNoStatsTelemetry,
	} {
		st := ProcessStatus{Running: true, StartedAt: &started, Log: log}
		if clientPeerUnhealthy(st, time.Now()) {
			t.Fatalf("%s: телеметрии нет — перезапускать нельзя", name)
		}
	}
}

type fakeIfaceChecker struct {
	exists map[string]bool
	operUp map[string]bool
}

func (f fakeIfaceChecker) InterfaceExists(name string) bool { return f.exists[name] }
func (f fakeIfaceChecker) InterfaceOperUp(name string) bool { return f.operUp[name] }

func TestClientRawNDMSUnhealthy(t *testing.T) {
	started := time.Now().Add(-6 * time.Minute)
	cfg := ClientConfig{
		ConnMode:  ConnModeRaw,
		NdmsIface: "OpkgTun18",
		RawIface:  "opkgtun18",
	}
	st := ProcessStatus{Running: true, StartedAt: &started}
	checker := fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": false},
	}
	if !clientRawNDMSUnhealthy(cfg, checker, st, time.Now()) {
		t.Fatal("expected unhealthy when opkg iface operstate is down")
	}
	checker.operUp["opkgtun18"] = true
	if clientRawNDMSUnhealthy(cfg, checker, st, time.Now()) {
		t.Fatal("expected healthy when iface is up")
	}
	if clientRawNDMSUnhealthy(ClientConfig{ConnMode: ConnModeWG}, checker, st, time.Now()) {
		t.Fatal("wg mode must not use raw NDMS health")
	}
	fresh := time.Now().Add(-time.Minute)
	st.StartedAt = &fresh
	if clientRawNDMSUnhealthy(cfg, checker, st, time.Now()) {
		t.Fatal("expected healthy during grace period")
	}
}

func TestClientRelayStalled(t *testing.T) {
	started := time.Now().Add(-6 * time.Minute)
	st := ProcessStatus{
		Running:         true,
		StartedAt:       &started,
		DtlsConnections: 9,
		Log:             stalledStatsLog(),
	}
	if !clientRelayStalled(st, time.Now()) {
		t.Fatal("expected stalled relay: downstream flat, uplink keepalives only")
	}
	// Активные воркеры есть — «нет сессий» тут ни при чём, лечит только
	// отдельный счётчик застоя.
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("stalled relay must not be reported as zero-session unhealthy")
	}

	fresh := time.Now().Add(-time.Minute)
	st.StartedAt = &fresh
	if clientRelayStalled(st, time.Now()) {
		t.Fatal("expected no verdict during grace period")
	}
}

func TestHealthTrackerStrikes(t *testing.T) {
	h := newHealthTracker(clientHealthStrikes)
	for i := 0; i < clientHealthStrikes-1; i++ {
		if h.note("c1", true) {
			t.Fatalf("strike %d should not restart yet", i+1)
		}
	}
	if !h.note("c1", true) {
		t.Fatal("expected restart after max strikes")
	}
}

// Порог застоя намеренно выше «нет сессий»: одного окна мало, чтобы отличить
// зомби-реле от живого простаивающего туннеля.
func TestStallTrackerNeedsLongerStreak(t *testing.T) {
	h := newHealthTracker(clientStallStrikes)
	for i := 0; i < clientHealthStrikes; i++ {
		if h.note("c1", true) {
			t.Fatalf("strike %d: рестарт слишком рано", i+1)
		}
	}
	for i := clientHealthStrikes; i < clientStallStrikes-1; i++ {
		h.note("c1", true)
	}
	if !h.note("c1", true) {
		t.Fatal("expected restart after clientStallStrikes")
	}
}

package wdtt

import (
	"strconv"
	"strings"
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

func TestClientPeerUnhealthyStalledTraffic(t *testing.T) {
	started := time.Now().Add(-4 * time.Minute)
	var sb strings.Builder
	for i := 0; i < stallMinEvents; i++ {
		up := 52000 + i*148
		sb.WriteString("__WDTT_EVENT__|STATS|{\"active\":9,\"bytes_down\":2424,\"bytes_up\":")
		sb.WriteString(strconv.Itoa(up))
		sb.WriteString("}\n")
	}
	st := ProcessStatus{
		Running:         true,
		StartedAt:       &started,
		DtlsConnections: 9,
		Log:             sb.String(),
	}
	if !clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected unhealthy for zombie relay with stalled downstream")
	}
}

func TestHealthTrackerStrikes(t *testing.T) {
	h := newHealthTracker()
	for i := 0; i < clientHealthStrikes-1; i++ {
		if h.note("c1", true) {
			t.Fatalf("strike %d should not restart yet", i+1)
		}
	}
	if !h.note("c1", true) {
		t.Fatal("expected restart after max strikes")
	}
}

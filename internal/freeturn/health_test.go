package freeturn

import (
	"testing"
	"time"
)

const (
	logAllStreamsDown = `2026/07/22 18:16:07 [INFO] [STREAM 1] Established DTLS connection
2026/07/22 18:16:19 [INFO] [STREAM 1] DTLS connection closed
`
	logStreamUp = `2026/07/22 18:16:07 [INFO] [STREAM 1] Established DTLS connection
2026/07/22 18:16:08 [INFO] [STREAM 6] Established DTLS connection
`
	// Хвост без единой строки [STREAM N]: маркеры вытеснены из ринга.
	logNoStreamTelemetry = `2026/07/22 18:20:00 [INFO] relay stats: 12 MB
2026/07/22 18:20:30 [INFO] relay stats: 13 MB
`
)

func TestClientPeerUnhealthy(t *testing.T) {
	started := time.Now().Add(-6 * time.Minute)
	st := ProcessStatus{Running: true, StartedAt: &started, Log: logAllStreamsDown}
	if !clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected unhealthy with all streams down after grace")
	}

	st.Log = logStreamUp
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy with established streams")
	}

	fresh := time.Now().Add(-30 * time.Second)
	st = ProcessStatus{Running: true, StartedAt: &fresh, Log: logAllStreamsDown}
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy during grace period")
	}

	st = ProcessStatus{
		Running:   true,
		StartedAt: &started,
		Log:       logAllStreamsDown + "Triggering manual captcha fallback\n",
	}
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy while captcha waiting")
	}
}

// Вытесненные из 500-строчного ринга маркеры стримов не должны выглядеть как
// «пира нет»: иначе health-check перезапускает исправно работающего клиента.
func TestClientPeerUnhealthyIgnoresMissingTelemetry(t *testing.T) {
	started := time.Now().Add(-6 * time.Minute)
	for name, log := range map[string]string{
		"пустой лог":   "",
		"без маркеров": logNoStreamTelemetry,
	} {
		st := ProcessStatus{Running: true, StartedAt: &started, Log: log}
		if clientPeerUnhealthy(st, time.Now()) {
			t.Fatalf("%s: телеметрии нет — перезапускать нельзя", name)
		}
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
	h.reset("c1")
	if h.note("c1", false) {
		t.Fatal("healthy tick must reset strikes")
	}
}

package freeturn

import (
	"testing"
	"time"
)

func TestClientPeerUnhealthy(t *testing.T) {
	started := time.Now().Add(-4 * time.Minute)
	st := ProcessStatus{
		Running:         true,
		StartedAt:       &started,
		DtlsConnections: 0,
	}
	if !clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected unhealthy with zero DTLS after grace")
	}

	st.DtlsConnections = 2
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy with active DTLS")
	}

	fresh := time.Now().Add(-30 * time.Second)
	st = ProcessStatus{Running: true, StartedAt: &fresh, DtlsConnections: 0}
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy during grace period")
	}

	st = ProcessStatus{
		Running:         true,
		StartedAt:       &started,
		DtlsConnections: 0,
		Log:             "Triggering manual captcha fallback\n",
	}
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy while captcha waiting")
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

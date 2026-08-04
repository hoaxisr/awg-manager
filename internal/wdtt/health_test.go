package wdtt

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
		t.Fatal("expected unhealthy with zero active workers after grace")
	}

	st.DtlsConnections = 5
	if clientPeerUnhealthy(st, time.Now()) {
		t.Fatal("expected healthy with active workers")
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

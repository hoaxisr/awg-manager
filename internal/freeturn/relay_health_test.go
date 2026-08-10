package freeturn

import (
	"context"
	"testing"
	"time"
)

type fakeRelayProbe struct {
	ok map[string]bool
}

func (f fakeRelayProbe) ProbeInterface(_ context.Context, iface string) bool {
	return f.ok[iface]
}

type fakeLinkedTunnels struct {
	iface string
	ok    bool
}

func (f fakeLinkedTunnels) FreeTurnLinkedIface(_ string) (string, bool) {
	return f.iface, f.ok
}

func TestClientRelayUnhealthy(t *testing.T) {
	started := time.Now().Add(-3 * time.Minute)
	st := ProcessStatus{Running: true, StartedAt: &started}
	probe := fakeRelayProbe{ok: map[string]bool{"opkgtun10": false}}
	tunnels := fakeLinkedTunnels{iface: "opkgtun10", ok: true}

	if !clientRelayUnhealthy(probe, tunnels, "default", st, time.Now()) {
		t.Fatal("expected unhealthy when relay probe fails")
	}

	probe.ok["opkgtun10"] = true
	if clientRelayUnhealthy(probe, tunnels, "default", st, time.Now()) {
		t.Fatal("expected healthy when relay probe ok")
	}

	fresh := time.Now().Add(-30 * time.Second)
	st.StartedAt = &fresh
	probe.ok["opkgtun10"] = false
	if clientRelayUnhealthy(probe, tunnels, "default", st, time.Now()) {
		t.Fatal("expected healthy during relay grace")
	}

	if clientRelayUnhealthy(nil, tunnels, "default", st, time.Now()) {
		t.Fatal("nil probe must not trigger restart")
	}
}

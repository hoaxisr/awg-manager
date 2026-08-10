package wdtt

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

func TestClientRawRelayUnhealthy(t *testing.T) {
	started := time.Now().Add(-3 * time.Minute)
	cfg := ClientConfig{
		ConnMode:  ConnModeRaw,
		NdmsIface: "OpkgTun18",
		RawIface:  "opkgtun18",
	}
	st := ProcessStatus{Running: true, StartedAt: &started}
	checker := fakeIfaceChecker{
		exists: map[string]bool{"opkgtun18": true},
		operUp: map[string]bool{"opkgtun18": true},
	}
	probe := fakeRelayProbe{ok: map[string]bool{"opkgtun18": false}}

	ctx := context.Background()
	if !clientRawRelayUnhealthy(ctx, cfg, probe, checker, st, time.Now()) {
		t.Fatal("expected unhealthy when relay probe fails on raw iface")
	}

	probe.ok["opkgtun18"] = true
	if clientRawRelayUnhealthy(ctx, cfg, probe, checker, st, time.Now()) {
		t.Fatal("expected healthy when relay probe ok")
	}

	checker.operUp["opkgtun18"] = false
	probe.ok["opkgtun18"] = false
	if clientRawRelayUnhealthy(ctx, cfg, probe, checker, st, time.Now()) {
		t.Fatal("iface down must be handled by NDMS health, not relay")
	}

	if clientRawRelayUnhealthy(ctx, ClientConfig{ConnMode: ConnModeWG}, probe, checker, st, time.Now()) {
		t.Fatal("wg mode must not use raw relay health")
	}
}

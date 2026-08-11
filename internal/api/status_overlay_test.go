package api

import (
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

func TestOverlayPendingStatus(t *testing.T) {
	now := time.Unix(2000, 0)
	future := now.Add(10 * time.Second)
	past := now.Add(-10 * time.Second)

	cases := []struct {
		name      string
		rawState  tunnel.State
		backend   string
		quiescent time.Time
		want      string
	}{
		{"nwg broken, no bring-up yet -> needs_start", tunnel.StateBroken, "nativewg", time.Time{}, "needs_start"},
		{"nwg broken, within window -> starting", tunnel.StateBroken, "nativewg", future, "starting"},
		{"nwg broken, window elapsed -> broken", tunnel.StateBroken, "nativewg", past, "broken"},
		{"kernel broken -> broken (untouched)", tunnel.StateBroken, "kernel", time.Time{}, "broken"},
		{"nwg running -> running (untouched)", tunnel.StateRunning, "nativewg", time.Time{}, "running"},
		{"nwg starting -> starting (untouched)", tunnel.StateStarting, "nativewg", time.Time{}, "starting"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overlayPendingStatus(tc.rawState, tc.backend, false /*supportsASC*/, tc.quiescent, now)
			if got != tc.want {
				t.Fatalf("overlayPendingStatus(%v, %q, q, now) = %q, want %q",
					tc.rawState, tc.backend, got, tc.want)
			}
		})
	}
}

// On the ASC path the classifier reports Broken for a tunnel that never
// handshook (conf=running, silent peer, no handshake, no uptime — the payload
// seen on the router), so the orchestrator bring-up window is the only thing
// that can tell "coming up right now" from "stuck" — and outside that window
// the tunnel is shown as broken, never as "needs start" (#702).
func TestOverlayPendingStatus_ASC(t *testing.T) {
	now := time.Unix(2000, 0)

	cases := []struct {
		name      string
		quiescent time.Time
		want      string
	}{
		{"bring-up window elapsed -> broken", now.Add(-10 * time.Second), "broken"},
		{"inside bring-up window -> starting", now.Add(10 * time.Second), "starting"},
		{"no bring-up this session -> broken", time.Time{}, "broken"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overlayPendingStatus(tunnel.StateBroken, "nativewg", true /*supportsASC*/, tc.quiescent, now)
			if got != tc.want {
				t.Fatalf("overlayPendingStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDisplayStatus verifies the single UI-status point derives backend from
// StateInfo, so list and detail (both routed through displayStatus) agree.
func TestDisplayStatus(t *testing.T) {
	// The cases below describe the non-ASC (kmod-proxy) overlay branch, so the
	// firmware flag displayStatus reads must be off: drop any ndmsinfo store a
	// neighbouring test may have installed instead of assuming it is empty.
	ndmsinfo.Reset()

	now := time.Unix(2000, 0)
	future := now.Add(10 * time.Second)

	cases := []struct {
		name string
		info tunnel.StateInfo
		q    time.Time
		want string
	}{
		{"nwg broken, no bring-up -> needs_start", tunnel.StateInfo{State: tunnel.StateBroken, BackendType: "nativewg"}, time.Time{}, "needs_start"},
		{"nwg broken, within window -> starting", tunnel.StateInfo{State: tunnel.StateBroken, BackendType: "nativewg"}, future, "starting"},
		{"kernel broken -> broken", tunnel.StateInfo{State: tunnel.StateBroken, BackendType: "kernel"}, time.Time{}, "broken"},
		{"disabled -> disabled", tunnel.StateInfo{State: tunnel.StateDisabled, BackendType: "nativewg"}, time.Time{}, "disabled"},
		{"not_created -> not_created", tunnel.StateInfo{State: tunnel.StateNotCreated, BackendType: "kernel"}, time.Time{}, "not_created"},
		{"running -> running", tunnel.StateInfo{State: tunnel.StateRunning, BackendType: "kernel"}, time.Time{}, "running"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := displayStatus(tc.info, tc.q, now)
			if got != tc.want {
				t.Fatalf("displayStatus(%+v, q, now) = %q, want %q", tc.info, got, tc.want)
			}
		})
	}
}

package api

import (
	"testing"
	"time"

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
			got := overlayPendingStatus(tc.rawState, tc.backend, tc.quiescent, now)
			if got != tc.want {
				t.Fatalf("overlayPendingStatus(%v, %q, q, now) = %q, want %q",
					tc.rawState, tc.backend, got, tc.want)
			}
		})
	}
}

// TestDisplayStatus verifies the single UI-status point derives backend from
// StateInfo, so list and detail (both routed through displayStatus) agree.
func TestDisplayStatus(t *testing.T) {
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

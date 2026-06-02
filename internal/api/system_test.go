package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/hydraroute"
)

func TestHydraRouteStatusData_MapsAllFields(t *testing.T) {
	in := hydraroute.Status{
		Installed:    true,
		Running:      false,
		Version:      "2.4.1",
		PID:          1234,
		StalePID:     5678,
		ProcessState: hydraroute.StateDead,
		LastError:    "neo restart: exit status 1",
	}

	got := hydraRouteStatusData(in)

	if got.Installed != in.Installed {
		t.Fatalf("Installed=%v want %v", got.Installed, in.Installed)
	}
	if got.Running != in.Running {
		t.Fatalf("Running=%v want %v", got.Running, in.Running)
	}
	if got.Version != in.Version {
		t.Fatalf("Version=%q want %q", got.Version, in.Version)
	}
	if got.PID != in.PID {
		t.Fatalf("PID=%d want %d", got.PID, in.PID)
	}
	if got.StalePID != in.StalePID {
		t.Fatalf("StalePID=%d want %d", got.StalePID, in.StalePID)
	}
	if got.ProcessState != string(in.ProcessState) {
		t.Fatalf("ProcessState=%q want %q", got.ProcessState, in.ProcessState)
	}
	if got.LastError != in.LastError {
		t.Fatalf("LastError=%q want %q", got.LastError, in.LastError)
	}
}

func TestEvalNativewg(t *testing.T) {
	cases := []struct {
		name          string
		hasComponent  bool
		supportsASC   bool
		awgProxy      bool
		wantAvailable bool
		wantReason    string
	}{
		{"no wireguard component", false, false, false, false, nwgReasonNoComponent},
		{"no component even with proxy", false, false, true, false, nwgReasonNoComponent},
		{"component + ASC firmware", true, true, false, true, ""},
		{"component + awg_proxy obfuscation", true, false, true, true, ""},
		{"component but no obfuscation path", true, false, false, false, nwgReasonNoObfuscation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			avail, reason := evalNativewg(c.hasComponent, c.supportsASC, c.awgProxy)
			if avail != c.wantAvailable {
				t.Fatalf("available=%v want %v", avail, c.wantAvailable)
			}
			if reason != c.wantReason {
				t.Fatalf("reason=%q want %q", reason, c.wantReason)
			}
		})
	}
}

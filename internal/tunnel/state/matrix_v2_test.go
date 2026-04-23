package state

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// TestStateMatrixV2_DetermineState tests the new state matrix that uses
// NDMS conf layer (intent) instead of the unreliable state: field.
func TestStateMatrixV2_DetermineState(t *testing.T) {
	m := StateMatrixV2{}

	tests := []struct {
		name    string
		input   StateInputs
		want    tunnel.State
	}{
		// === NotCreated: no OpkgTun ===
		{
			name: "not created - nothing exists",
			input: StateInputs{
				HasNDMS:       true,
				OpkgTunExists: false,
			},
			want: tunnel.StateNotCreated,
		},

		// === Running: conf=running, link=up, process alive, has peer ===
		{
			name: "running - fully operational",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentUp,
				LinkUp:         true,
				ProcessRunning: true,
				HasPeer:        true,
			},
			want: tunnel.StateRunning,
		},
		{
			name: "running - link up, process, but no peer (awg connected, no wg handshake yet)",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentUp,
				LinkUp:         true,
				ProcessRunning: true,
				HasPeer:        false,
			},
			want: tunnel.StateRunning,
		},

		// === Starting: conf=running, process alive, link not yet up ===
		{
			name: "starting - process alive but link not up yet",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentUp,
				LinkUp:         false,
				ProcessRunning: true,
			},
			want: tunnel.StateStarting,
		},

		// === NeedsStart: conf=running, no process (after reboot / after kill) ===
		{
			name: "needs start - after reboot, NDMS wants up but no process",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentUp,
				LinkUp:         false,
				ProcessRunning: false,
			},
			want: tunnel.StateNeedsStart,
		},
		{
			name: "needs start - after kill process",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentUp,
				LinkUp:         false,
				ProcessRunning: false,
			},
			want: tunnel.StateNeedsStart,
		},

		// === Disabled: conf=disabled, no process ===
		{
			name: "disabled - admin turned off, all clean",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentDown,
				LinkUp:         false,
				ProcessRunning: false,
			},
			want: tunnel.StateDisabled,
		},

		// === NeedsStop: conf=disabled, but process still alive ===
		{
			name: "needs stop - toggle off in router UI, process still alive",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentDown,
				LinkUp:         false,
				ProcessRunning: true,
			},
			want: tunnel.StateNeedsStop,
		},
		{
			name: "needs stop - conf disabled, link somehow still up",
			input: StateInputs{
				HasNDMS:        true,
				OpkgTunExists:  true,
				Intent:         ndms.IntentDown,
				LinkUp:         true,
				ProcessRunning: true,
			},
			want: tunnel.StateNeedsStop,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.DetermineState(tt.input)
			if got != tt.want {
				t.Errorf("DetermineState() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestStateMatrixV2_AllCombinations tests all combinations of inputs
// to ensure no unexpected states.
func TestStateMatrixV2_AllCombinations(t *testing.T) {
	m := StateMatrixV2{}

	type combo struct {
		opkgTun bool
		intent  ndms.InterfaceIntent
		linkUp  bool
		process bool
		hasPeer bool
		want    tunnel.State
	}

	combos := []combo{
		// OpkgTun=false: always NotCreated (regardless of other signals)
		{false, ndms.IntentUp, false, false, false, tunnel.StateNotCreated},
		{false, ndms.IntentUp, true, true, true, tunnel.StateNotCreated},
		{false, ndms.IntentDown, false, false, false, tunnel.StateNotCreated},
		{false, ndms.IntentDown, true, true, true, tunnel.StateNotCreated},

		// OpkgTun=true, Intent=UP, link=up, process=true: Running
		{true, ndms.IntentUp, true, true, true, tunnel.StateRunning},
		{true, ndms.IntentUp, true, true, false, tunnel.StateRunning},

		// OpkgTun=true, Intent=UP, link=down, process=true: Starting
		{true, ndms.IntentUp, false, true, false, tunnel.StateStarting},
		{true, ndms.IntentUp, false, true, true, tunnel.StateStarting},

		// OpkgTun=true, Intent=UP, process=false: NeedsStart
		{true, ndms.IntentUp, false, false, false, tunnel.StateNeedsStart},
		{true, ndms.IntentUp, false, false, true, tunnel.StateNeedsStart},
		// Edge case: link up but no process (shouldn't happen, but handle gracefully)
		{true, ndms.IntentUp, true, false, false, tunnel.StateNeedsStart},
		{true, ndms.IntentUp, true, false, true, tunnel.StateNeedsStart},

		// OpkgTun=true, Intent=DOWN, process=false: Disabled
		{true, ndms.IntentDown, false, false, false, tunnel.StateDisabled},
		{true, ndms.IntentDown, false, false, true, tunnel.StateDisabled},
		{true, ndms.IntentDown, true, false, false, tunnel.StateDisabled},
		{true, ndms.IntentDown, true, false, true, tunnel.StateDisabled},

		// OpkgTun=true, Intent=DOWN, process=true: NeedsStop
		{true, ndms.IntentDown, false, true, false, tunnel.StateNeedsStop},
		{true, ndms.IntentDown, false, true, true, tunnel.StateNeedsStop},
		{true, ndms.IntentDown, true, true, false, tunnel.StateNeedsStop},
		{true, ndms.IntentDown, true, true, true, tunnel.StateNeedsStop},
	}

	for i, c := range combos {
		input := StateInputs{
			HasNDMS:        true,
			OpkgTunExists:  c.opkgTun,
			Intent:         c.intent,
			LinkUp:         c.linkUp,
			ProcessRunning: c.process,
			HasPeer:        c.hasPeer,
		}
		got := m.DetermineState(input)
		if got != c.want {
			t.Errorf("combo %d: DetermineState(opkg=%v, intent=%v, link=%v, proc=%v, peer=%v) = %v, want %v",
				i, c.opkgTun, c.intent, c.linkUp, c.process, c.hasPeer, got, c.want)
		}
	}
}

// TestStateMatrixV2_BugScenario_RebootWithDisabledAutostart verifies
// the ORIGINAL BUG is fixed: tunnel with Enabled=false after reboot
// was shown as "Broken" instead of correct state.
//
// Old behavior: OpkgTun=true, Process=false, InterfaceUp=true → Broken
// New behavior: OpkgTun=true, Intent=DOWN, Process=false → Disabled
//
// The bug happened because IsInterfaceUp() checked NDMS state: field
// which showed "up" after reboot (admin intent), and deviceExists()
// always returned true (NDMS creates TUN for all OpkgTun).
func TestStateMatrixV2_BugScenario_RebootWithDisabledAutostart(t *testing.T) {
	m := StateMatrixV2{}

	// Scenario: tunnel was stopped (Enabled=false), after reboot
	// NDMS shows: state: down, conf: disabled, TUN device exists
	input := StateInputs{
		HasNDMS:        true,
		OpkgTunExists:  true,
		Intent:         ndms.IntentDown, // conf: disabled
		LinkUp:         false,
		ProcessRunning: false,
	}

	got := m.DetermineState(input)
	if got != tunnel.StateDisabled {
		t.Errorf("Reboot with disabled autostart: got %v, want StateDisabled (was StateBroken in old code)", got)
	}
}

// TestStateMatrixV2_BugScenario_RebootWithEnabledAutostart verifies
// correct behavior: tunnel that was running before reboot → NeedsStart.
//
// After reboot: NDMS state: up, conf: running, link: pending, no process.
// Old behavior: state: up → InterfaceUp=true + no process → Broken
// New behavior: conf: running (IntentUp) + no process → NeedsStart
func TestStateMatrixV2_BugScenario_RebootWithEnabledAutostart(t *testing.T) {
	m := StateMatrixV2{}

	input := StateInputs{
		HasNDMS:        true,
		OpkgTunExists:  true,
		Intent:         ndms.IntentUp, // conf: running
		LinkUp:         false,         // link: down (pending)
		ProcessRunning: false,         // no process yet
	}

	got := m.DetermineState(input)
	if got != tunnel.StateNeedsStart {
		t.Errorf("Reboot with enabled autostart: got %v, want StateNeedsStart (was StateBroken in old code)", got)
	}
}

// TestStateMatrixV2_Scenario_UserToggleOffInRouterUI verifies detection
// of user toggling off tunnel in the router's native web UI.
// NDMS sets conf: disabled, but our process is still alive.
func TestStateMatrixV2_Scenario_UserToggleOffInRouterUI(t *testing.T) {
	m := StateMatrixV2{}

	input := StateInputs{
		HasNDMS:        true,
		OpkgTunExists:  true,
		Intent:         ndms.IntentDown, // user toggled off → conf: disabled
		LinkUp:         false,
		ProcessRunning: true, // our process still running
	}

	got := m.DetermineState(input)
	if got != tunnel.StateNeedsStop {
		t.Errorf("User toggle off in router UI: got %v, want StateNeedsStop", got)
	}
}

// TestStateMatrixV2_Scenario_PingCheckDead_Kernel verifies state after
// PingCheck does ip link set down in kernel mode.
// NDMS: state: down, conf: running (intent preserved!), link: pending
func TestStateMatrixV2_Scenario_PingCheckDead_Kernel(t *testing.T) {
	m := StateMatrixV2{}

	// Key: conf: running even though state: down
	// This is what ip link set down produces (vs ndmc interface down → conf: disabled)
	input := StateInputs{
		HasNDMS:        true,
		OpkgTunExists:  true,
		Intent:         ndms.IntentUp, // conf: running (preserved after ip link down)
		LinkUp:         false,
		ProcessRunning: false, // kernel module still loaded but link is down
	}

	got := m.DetermineState(input)
	if got != tunnel.StateNeedsStart {
		t.Errorf("PingCheck dead (kernel): got %v, want StateNeedsStart", got)
	}
}

// === OS4-specific tests ===

// TestStateMatrixV2_OS4_Running verifies OS4 running state detection.
// OS4 has no NDMS — state is determined by process + peer.
func TestStateMatrixV2_OS4_Running(t *testing.T) {
	m := StateMatrixV2{}

	input := StateInputs{
		HasNDMS:        false,
		ProcessRunning: true,
		LinkUp:         true,
		HasPeer:        true,
	}

	got := m.DetermineState(input)
	if got != tunnel.StateRunning {
		t.Errorf("OS4 running: got %v, want StateRunning", got)
	}
}

// TestStateMatrixV2_OS4_Starting verifies OS4 starting state.
// Process alive but no peer yet (WG handshake pending).
func TestStateMatrixV2_OS4_Starting(t *testing.T) {
	m := StateMatrixV2{}

	input := StateInputs{
		HasNDMS:        false,
		ProcessRunning: true,
		HasPeer:        false,
	}

	got := m.DetermineState(input)
	if got != tunnel.StateStarting {
		t.Errorf("OS4 starting: got %v, want StateStarting", got)
	}
}

// TestStateMatrixV2_OS4_Stopped verifies OS4 stopped state.
// No process running.
func TestStateMatrixV2_OS4_Stopped(t *testing.T) {
	m := StateMatrixV2{}

	input := StateInputs{
		HasNDMS:        false,
		ProcessRunning: false,
	}

	got := m.DetermineState(input)
	if got != tunnel.StateStopped {
		t.Errorf("OS4 stopped: got %v, want StateStopped", got)
	}
}

// TestStateMatrixV2_OS4_NeverProducesNDMSStates exhaustively checks that
// OS4 branch never produces NDMS-specific states (NeedsStart, NeedsStop, Disabled, NotCreated).
func TestStateMatrixV2_OS4_NeverProducesNDMSStates(t *testing.T) {
	m := StateMatrixV2{}

	ndmsOnlyStates := map[tunnel.State]bool{
		tunnel.StateNeedsStart: true,
		tunnel.StateNeedsStop:  true,
		tunnel.StateDisabled:   true,
		tunnel.StateNotCreated: true,
	}

	// Exhaustive: all combos of bool fields that OS4 uses
	for _, process := range []bool{false, true} {
		for _, peer := range []bool{false, true} {
			// Also vary NDMS-only fields to prove they're ignored
			for _, opkgTun := range []bool{false, true} {
				for _, intent := range []ndms.InterfaceIntent{ndms.IntentDown, ndms.IntentUp} {
					for _, linkUp := range []bool{false, true} {
						input := StateInputs{
							HasNDMS:        false,
							OpkgTunExists:  opkgTun,
							Intent:         intent,
							LinkUp:         linkUp,
							ProcessRunning: process,
							HasPeer:        peer,
						}
						got := m.DetermineState(input)
						if ndmsOnlyStates[got] {
							t.Errorf("OS4 produced NDMS state %v for input proc=%v peer=%v opkg=%v intent=%v link=%v",
								got, process, peer, opkgTun, intent, linkUp)
						}
					}
				}
			}
		}
	}
}

// TestStateMatrixV2_BugScenario_OS4_ReconciledKill is a regression test for
// the original bug: OS4 tunnel (awgm0) with NDMSName="" caused:
//   - OpkgTunExists(ctx, "") → strings.Contains(output, "") → always true
//   - ShowInterface(ctx, "") → fails → Intent defaults to IntentDown (zero value)
//   - State matrix: OpkgTunExists=true + IntentDown + ProcessRunning → NeedsStop
//   - Reconcile loop kills the process every 15 seconds
//
// Fix: HasNDMS=false bypasses NDMS logic entirely, uses process+peer.
func TestStateMatrixV2_BugScenario_OS4_ReconciledKill(t *testing.T) {
	m := StateMatrixV2{}

	// Simulate what happened BEFORE the fix:
	// - tunnel awgm0 is running with peer
	// - but state matrix got NDMS garbage because NDMSName="" wasn't handled
	//
	// After fix: HasNDMS=false → OS4 branch → Running
	input := StateInputs{
		HasNDMS:        false,           // OS4: no NDMS
		OpkgTunExists:  false,           // irrelevant for OS4
		Intent:         ndms.IntentDown, // would be zero value from failed NDMS
		LinkUp:         true,            // running tunnel has link up (operstate=unknown → true)
		ProcessRunning: true,
		HasPeer:        true,
	}

	got := m.DetermineState(input)
	if got != tunnel.StateRunning {
		t.Errorf("OS4 reconcile kill bug: got %v, want StateRunning (was NeedsStop before fix)", got)
	}
}

// TestStateMatrixV2_Lightweight_KernelKillLink verifies that the lightweight
// state (used by list API) correctly detects kernel KillLink.
// After `ip link set down`: sysfs exists (ProcessRunning=true), WG peer data
// accessible (HasPeer=true), but link is down (LinkUp=false) → Starting.
func TestStateMatrixV2_Lightweight_KernelKillLink(t *testing.T) {
	m := StateMatrixV2{}

	input := StateInputs{
		HasNDMS:        false, // lightweight path
		ProcessRunning: true,  // sysfs exists after ip link set down
		LinkUp:         false, // operstate=down
		HasPeer:        true,  // awg show still works
	}

	got := m.DetermineState(input)
	if got != tunnel.StateStarting {
		t.Errorf("kernel KillLink lightweight: got %v, want StateStarting", got)
	}
}

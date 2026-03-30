package lifecycle

import "testing"

// TestLinkDown_StateDetection verifies that determineState logic correctly
// identifies the tunnel state when `ip link set opkgtunX down` is executed
// on a running kernel tunnel.
//
// The state is inferred from system conditions:
//   - ProcessRunning = true  (amneziawg device exists in sysfs)
//   - InterfaceUp = false    (link operstate = down)
//   - OpkgTunExists = true   (NDMS still has the OpkgTun)
//
// This is the SAME state as WAN-down suspend, so the behavior depends
// on stored.Enabled to distinguish "user stopped" from "system suspended".
func TestLinkDown_StateDetection(t *testing.T) {
	// determineState is a Manager method (needs NDMS client),
	// but its logic for ProcessRunning && !InterfaceUp is clear:
	//   enabled=true  → StateSuspended
	//   enabled=false → StateDisabled
	//
	// We test via the Decide matrix which consumes these states.

	t.Run("link_down+enabled=true → StateSuspended decisions", func(t *testing.T) {
		// ip link set opkgtunX down on a running, enabled tunnel.
		// determineState returns StateSuspended.
		// Verify every event produces the correct action.
		state := StateSuspended
		ctx := EventContext{StoredEnabled: true}

		cases := []struct {
			event Event
			want  Action
			desc  string
		}{
			{EventBoot, ActionColdStart, "boot should fully recreate suspended tunnel"},
			{EventDaemonRestart, ActionResume, "daemon restart should resume (ip link set up)"},
			{EventWANUp, ActionResume, "WAN up should resume (ip link set up)"},
			{EventUserEnable, ActionResume, "user enable from Keenetic UI should resume"},
			{EventUserDisable, ActionStop, "user disable should fully stop"},
			{EventAPIStart, ActionResume, "API start should resume"},
			{EventAPIStop, ActionStop, "API stop should fully stop"},
			{EventAPIRestart, ActionRestart, "API restart should stop+start"},
		}

		for _, tc := range cases {
			t.Run(tc.event.String(), func(t *testing.T) {
				got := Decide(tc.event, state, ctx)
				if got != tc.want {
					t.Errorf("Decide(%s, %s, enabled=true) = %s, want %s — %s",
						tc.event, state, got, tc.want, tc.desc)
				}
			})
		}
	})

	t.Run("link_down+enabled=false → only explicit actions work", func(t *testing.T) {
		// After user Stop + ip link set down. determineState returns StateDisabled.
		// The enabled=false guard in Decide blocks most events.
		state := StateDisabled
		ctx := EventContext{StoredEnabled: false}

		cases := []struct {
			event Event
			want  Action
			desc  string
		}{
			{EventBoot, ActionNone, "boot should NOT auto-start disabled tunnel"},
			{EventDaemonRestart, ActionNone, "daemon restart should NOT start disabled tunnel"},
			{EventWANUp, ActionNone, "WAN up should NOT start disabled tunnel"},
			{EventUserEnable, ActionStart, "user enable should start (device exists after Stop)"},
			{EventUserDisable, ActionNone, "user disable on already-disabled is no-op"},
			{EventAPIStart, ActionStart, "API start should start even when disabled"},
			{EventAPIStop, ActionNone, "API stop on disabled is no-op"},
			{EventAPIRestart, ActionStart, "API restart should start even when disabled"},
		}

		for _, tc := range cases {
			t.Run(tc.event.String(), func(t *testing.T) {
				got := Decide(tc.event, state, ctx)
				if got != tc.want {
					t.Errorf("Decide(%s, %s, enabled=false) = %s, want %s — %s",
						tc.event, state, got, tc.want, tc.desc)
				}
			})
		}
	})
}

// TestLinkDown_WANDownThenLinkDown verifies the scenario where WAN goes down
// (tunnel suspended), then separately ip link set down is executed.
// Both transitions produce the same determineState result (StateSuspended),
// so subsequent events should behave identically.
func TestLinkDown_WANDownThenLinkDown(t *testing.T) {
	// After WAN down: ActionSuspend executed → ip link set down → StateSuspended
	// Manual ip link set down on top: no-op (already down) → still StateSuspended
	// WAN comes back up: should resume
	got := Decide(EventWANUp, StateSuspended, EventContext{StoredEnabled: true})
	if got != ActionResume {
		t.Errorf("WAN up after double-suspend should resume, got %s", got)
	}
}

// TestLinkDown_NoHookFired documents that ip link set down does NOT trigger
// the NDMS conf-layer hook. The iflayerchanged.d script filters layer != "conf".
// Therefore ReconcileInterface is never called, and no lifecycle event fires.
//
// This test verifies the consequence: when NO event fires, the tunnel stays
// in StateSuspended and only recovers via:
//   1. WAN up event (if WAN was the cause)
//   2. PingCheck link toggle (if PingCheck is active)
//   3. Explicit user action (API start/restart, or Keenetic UI toggle)
func TestLinkDown_NoHookFired(t *testing.T) {
	// StateSuspended with WAN down event → ActionNone (already suspended)
	got := Decide(EventWANDown, StateSuspended, EventContext{
		StoredEnabled: true,
		HasOtherWAN:   false,
	})
	if got != ActionNone {
		t.Errorf("WAN down on suspended tunnel should be no-op, got %s", got)
	}
}

// TestLinkDown_PingCheckRecovery documents the PingCheck path:
// PingCheck does doLinkToggle (ip link set down + ip link set up).
// After link comes back up, the tunnel should be in StateRunning.
// No lifecycle Decide call happens — PingCheck operates outside the event system.
//
// If PingCheck brings link up and WireGuard handshake succeeds:
//   ProcessRunning=true, InterfaceUp=true → StateRunning
//
// If handshake fails (link up but no peer response):
//   ProcessRunning=true, InterfaceUp=true → StateRunning (WG link is up)
//   PingCheck continues monitoring → next check may fail → another toggle
func TestLinkDown_PingCheckRecovery(t *testing.T) {
	// After PingCheck brings link up → StateRunning
	// All events should work normally
	state := StateRunning
	ctx := EventContext{StoredEnabled: true}

	// User disable after PingCheck recovery → should stop
	got := Decide(EventUserDisable, state, ctx)
	if got != ActionStop {
		t.Errorf("user disable after pingcheck recovery should stop, got %s", got)
	}

	// WAN down after recovery → should suspend
	got = Decide(EventWANDown, state, EventContext{StoredEnabled: true, HasOtherWAN: false})
	if got != ActionSuspend {
		t.Errorf("WAN down after recovery should suspend, got %s", got)
	}
}

// TestLinkDown_ExternalLinkDownWhileDisabled verifies behavior when
// ip link set down is executed on a tunnel that was already stopped by user.
// State: ProcessRunning=true, InterfaceUp=false, Enabled=false → StateDisabled.
// The link-down is redundant — tunnel is already in the correct state.
func TestLinkDown_ExternalLinkDownWhileDisabled(t *testing.T) {
	state := StateDisabled
	ctx := EventContext{StoredEnabled: false}

	// Boot should NOT start
	got := Decide(EventBoot, state, ctx)
	if got != ActionNone {
		t.Errorf("boot on disabled tunnel should be none, got %s", got)
	}

	// Only explicit user action should start
	got = Decide(EventAPIStart, state, ctx)
	if got != ActionStart {
		t.Errorf("API start on disabled should start, got %s", got)
	}
}

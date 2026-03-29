package lifecycle

import "testing"

func TestDecide_Boot(t *testing.T) {
	tests := []struct {
		name    string
		state   TunnelState
		enabled bool
		want    Action
	}{
		{"enabled + boot_ready", StateBootReady, true, ActionColdStart},
		{"enabled + running", StateRunning, true, ActionNone},
		{"enabled + not_exist", StateDisabled, true, ActionStart},
		{"enabled + dead", StateDead, true, ActionColdStart},
		{"enabled + broken", StateBroken, true, ActionColdStart},
		{"disabled + any", StateBootReady, false, ActionNone},
		{"disabled + running", StateRunning, false, ActionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(EventBoot, tt.state, EventContext{StoredEnabled: tt.enabled})
			if got != tt.want {
				t.Errorf("Decide(Boot, %s, enabled=%v) = %s, want %s",
					tt.state, tt.enabled, got, tt.want)
			}
		})
	}
}

func TestDecide_DaemonRestart(t *testing.T) {
	tests := []struct {
		name    string
		state   TunnelState
		enabled bool
		hasPeer bool
		want    Action
	}{
		{"running + peer", StateRunning, true, true, ActionReconnect},
		{"running + no peer", StateRunning, true, false, ActionReconfig},
		{"suspended", StateSuspended, true, false, ActionResume},
		{"disabled (device exists)", StateDisabled, true, false, ActionStart},
		{"not_created", StateNotCreated, true, false, ActionColdStart},
		{"boot_ready (tun)", StateBootReady, true, false, ActionColdStart},
		{"dead", StateDead, true, false, ActionColdStart},
		{"broken", StateBroken, true, false, ActionReconfig},
		{"disabled by user", StateDisabled, false, false, ActionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(EventDaemonRestart, tt.state, EventContext{
				StoredEnabled: tt.enabled,
				HasPeer:       tt.hasPeer,
			})
			if got != tt.want {
				t.Errorf("Decide(DaemonRestart, %s, enabled=%v, peer=%v) = %s, want %s",
					tt.state, tt.enabled, tt.hasPeer, got, tt.want)
			}
		})
	}
}

func TestDecide_WANUp(t *testing.T) {
	tests := []struct {
		name    string
		state   TunnelState
		enabled bool
		want    Action
	}{
		{"running", StateRunning, true, ActionNone},
		{"suspended", StateSuspended, true, ActionResume},
		{"dead", StateDead, true, ActionColdStart},
		{"disabled (device exists)", StateDisabled, true, ActionStart},
		{"disabled by user", StateDisabled, false, ActionNone},
		{"boot_ready", StateBootReady, true, ActionColdStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(EventWANUp, tt.state, EventContext{StoredEnabled: tt.enabled})
			if got != tt.want {
				t.Errorf("Decide(WANUp, %s, enabled=%v) = %s, want %s",
					tt.state, tt.enabled, got, tt.want)
			}
		})
	}
}

func TestDecide_WANDown(t *testing.T) {
	tests := []struct {
		name        string
		state       TunnelState
		hasOtherWAN bool
		want        Action
	}{
		{"running, no other WAN", StateRunning, false, ActionSuspend},
		{"running, has other WAN (auto)", StateRunning, true, ActionSwitchRoute},
		{"suspended", StateSuspended, false, ActionNone},
		{"dead", StateDead, false, ActionNone},
		{"disabled", StateDisabled, false, ActionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(EventWANDown, tt.state, EventContext{
				StoredEnabled: true,
				HasOtherWAN:   tt.hasOtherWAN,
			})
			if got != tt.want {
				t.Errorf("Decide(WANDown, %s, otherWAN=%v) = %s, want %s",
					tt.state, tt.hasOtherWAN, got, tt.want)
			}
		})
	}
}

func TestDecide_UserToggle(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		state TunnelState
		want  Action
	}{
		{"enable + disabled", EventUserEnable, StateDisabled, ActionStart},
		{"enable + not_created", EventUserEnable, StateNotCreated, ActionColdStart},
		{"enable + suspended", EventUserEnable, StateSuspended, ActionResume},
		{"enable + running", EventUserEnable, StateRunning, ActionNone},
		{"enable + dead", EventUserEnable, StateDead, ActionColdStart},
		{"enable + boot_ready", EventUserEnable, StateBootReady, ActionColdStart},
		{"disable + running", EventUserDisable, StateRunning, ActionStop},
		{"disable + suspended", EventUserDisable, StateSuspended, ActionStop},
		{"disable + dead", EventUserDisable, StateDead, ActionStop},
		{"disable + broken", EventUserDisable, StateBroken, ActionStop},
		{"disable + disabled", EventUserDisable, StateDisabled, ActionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.event, tt.state, EventContext{StoredEnabled: true})
			if got != tt.want {
				t.Errorf("Decide(%s, %s) = %s, want %s",
					tt.event, tt.state, got, tt.want)
			}
		})
	}
}

func TestDecide_APIStartStop(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		state TunnelState
		want  Action
	}{
		{"start + disabled", EventAPIStart, StateDisabled, ActionStart},
		{"start + suspended", EventAPIStart, StateSuspended, ActionResume},
		{"start + running", EventAPIStart, StateRunning, ActionNone},
		{"start + boot_ready", EventAPIStart, StateBootReady, ActionColdStart},
		{"start + dead", EventAPIStart, StateDead, ActionColdStart},
		{"start + broken", EventAPIStart, StateBroken, ActionColdStart},
		{"stop + running", EventAPIStop, StateRunning, ActionStop},
		{"stop + suspended", EventAPIStop, StateSuspended, ActionStop},
		{"stop + dead", EventAPIStop, StateDead, ActionStop},
		{"stop + disabled", EventAPIStop, StateDisabled, ActionNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.event, tt.state, EventContext{StoredEnabled: true})
			if got != tt.want {
				t.Errorf("Decide(%s, %s) = %s, want %s",
					tt.event, tt.state, got, tt.want)
			}
		})
	}
}

func TestDecide_APIRestart(t *testing.T) {
	tests := []struct {
		name  string
		state TunnelState
		want  Action
	}{
		{"running", StateRunning, ActionRestart},
		{"suspended", StateSuspended, ActionRestart},
		{"dead", StateDead, ActionRestart},
		{"broken", StateBroken, ActionRestart},
		{"disabled", StateDisabled, ActionStart},
		{"not_created", StateNotCreated, ActionColdStart},
		{"boot_ready", StateBootReady, ActionColdStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(EventAPIRestart, tt.state, EventContext{StoredEnabled: true})
			if got != tt.want {
				t.Errorf("Decide(APIRestart, %s) = %s, want %s",
					tt.state, got, tt.want)
			}
		})
	}
}

func TestDecide_APIRestart_Disabled(t *testing.T) {
	// Restart should work even when stored.Enabled=false (force start intent).
	got := Decide(EventAPIRestart, StateDisabled, EventContext{StoredEnabled: false})
	if got != ActionStart {
		t.Errorf("Decide(APIRestart, disabled, enabled=false) = %s, want start", got)
	}
}


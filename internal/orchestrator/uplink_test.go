package orchestrator

import "testing"

func TestKernelUplinkOwnsWANEventsWithoutImplicitReturn(t *testing.T) {
	s := newState()
	s.managedUplinkOwns = func(id string) bool { return id == "owned" }
	s.tunnels["owned"] = &tunnelState{ID: "owned", Backend: "kernel", Enabled: true, Running: true, ISPInterface: "eth3", ActiveWAN: "eth3"}
	s.tunnels["ordinary"] = &tunnelState{ID: "ordinary", Backend: "kernel", Enabled: true, Running: true, ISPInterface: "eth3", ActiveWAN: "eth3"}
	actions := decideWANDown(Event{Type: EventWANDown, WANIface: "eth3"}, &s)
	assertNoActionForTunnel(t, actions, "owned", ActionSuspendKernel)
	if n := len(filterActions(actions, ActionSuspendKernel)); n != 1 {
		t.Fatal(actions)
	}
	actions = decideWANUp(Event{Type: EventWANUp, WANIface: "eth3"}, &s)
	for _, a := range actions {
		if a.Tunnel == "owned" {
			t.Fatal("primary return churned an externally managed tunnel", actions)
		}
	}
	s.tunnels["owned"].Running = false
	actions = decideWANUp(Event{Type: EventWANUp, WANIface: "apcli0"}, &s)
	starts := filterActions(actions, ActionColdStartKernel)
	if len(starts) != 1 || starts[0].Tunnel != "owned" {
		t.Fatal("stopped target did not recover via permitted alternate WAN", actions)
	}
	s.tunnels["owned"].Enabled = false
	actions = decideWANUp(Event{Type: EventWANUp, WANIface: "apcli0"}, &s)
	assertNoActionForTunnel(t, actions, "owned", ActionColdStartKernel)
}

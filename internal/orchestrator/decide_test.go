package orchestrator

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestDecide_Boot_StartsEnabledKernelTunnels(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Enabled: true}
	s.tunnels["awg1"] = &tunnelState{ID: "awg1", Backend: "kernel", Enabled: true}
	s.tunnels["awg2"] = &tunnelState{ID: "awg2", Backend: "kernel", Enabled: false}

	actions := decide(Event{Type: EventBoot}, &s)

	starts := filterActions(actions, ActionColdStartKernel)
	if len(starts) != 2 {
		t.Errorf("expected 2 ColdStartKernel, got %d", len(starts))
	}
	assertNoActionForTunnel(t, actions, "awg2", ActionColdStartKernel)
}

func TestDecide_Boot_StartsNativeWGWithoutASC(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "nativewg", Enabled: true, NWGIndex: 0}

	actions := decide(Event{Type: EventBoot}, &s)

	stops := filterActions(actions, ActionStopNativeWG)
	starts := filterActions(actions, ActionStartNativeWG)
	if len(stops) != 1 || len(starts) != 1 {
		t.Errorf("expected Stop+Start for NativeWG without ASC, got %d stops, %d starts", len(stops), len(starts))
	}
}

func TestDecide_Boot_SkipsNativeWGWithASC(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "nativewg", Enabled: true, NWGIndex: 0}

	actions := decide(Event{Type: EventBoot}, &s)

	starts := filterActions(actions, ActionStartNativeWG)
	if len(starts) != 0 {
		t.Errorf("NativeWG with ASC should not be started by us, got %d starts", len(starts))
	}
}

func TestDecide_Boot_IncludesMonitoring(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Enabled: true,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventBoot}, &s)

	monitors := filterActions(actions, ActionStartMonitoring)
	if len(monitors) != 1 {
		t.Errorf("expected 1 StartMonitoring, got %d", len(monitors))
	}
}

func TestDecide_Boot_IncludesRouting(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Enabled: true}

	actions := decide(Event{Type: EventBoot}, &s)

	if !hasAction(actions, ActionReconcileStaticRoutes) {
		t.Error("expected ActionReconcileStaticRoutes")
	}
	if !hasAction(actions, ActionReconcileDNSRoutes) {
		t.Error("expected ActionReconcileDNSRoutes")
	}
}

func TestDecide_Boot_NativeWGConfiguresPingCheck(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, NWGIndex: 0,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventBoot}, &s)

	if !hasAction(actions, ActionConfigurePingCheck) {
		t.Error("NativeWG boot should configure NDMS ping-check profile")
	}
}

func TestDecide_Boot_SkipsDisabledTunnels(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Enabled: false}
	s.tunnels["awg1"] = &tunnelState{ID: "awg1", Backend: "nativewg", Enabled: false, NWGIndex: 0}

	actions := decide(Event{Type: EventBoot}, &s)

	starts := filterActions(actions, ActionColdStartKernel)
	nwgStarts := filterActions(actions, ActionStartNativeWG)
	if len(starts) != 0 || len(nwgStarts) != 0 {
		t.Errorf("disabled tunnels should not be started")
	}
}

func TestDecide_Reconnect_StartsMonitoringOnce(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	monitors := filterActions(actions, ActionStartMonitoring)
	if len(monitors) != 1 {
		t.Errorf("expected exactly 1 StartMonitoring, got %d — DUPLICATE BUG!", len(monitors))
	}
}

func TestDecide_Reconnect_RestoresKmodWithoutASC(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	restores := filterActions(actions, ActionRestoreKmod)
	if len(restores) != 1 {
		t.Errorf("expected 1 RestoreKmod, got %d", len(restores))
	}
}

func TestDecide_Reconnect_SkipsStoppedTunnels(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: false,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	monitors := filterActions(actions, ActionStartMonitoring)
	if len(monitors) != 0 {
		t.Errorf("stopped tunnel should not get monitoring, got %d", len(monitors))
	}
}

func TestDecide_Reconnect_RestoresEndpointTracking(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Running: true}

	actions := decide(Event{Type: EventReconnect}, &s)

	if !hasAction(actions, ActionRestoreEndpointTracking) {
		t.Error("Reconnect should restore endpoint tracking")
	}
}

func TestDecide_Reconnect_IncludesRoutingReconcile(t *testing.T) {
	s := newState()

	actions := decide(Event{Type: EventReconnect}, &s)

	if !hasAction(actions, ActionReconcileStaticRoutes) {
		t.Error("expected ActionReconcileStaticRoutes")
	}
	if !hasAction(actions, ActionReconcileDNSRoutes) {
		t.Error("expected ActionReconcileDNSRoutes")
	}
}

func TestDecide_Reconnect_SkipsKmodWithASC(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	restores := filterActions(actions, ActionRestoreKmod)
	if len(restores) != 0 {
		t.Errorf("ASC firmware should not restore kmod, got %d", len(restores))
	}
}

func TestDecide_Start_KernelTunnel(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Enabled: false, Running: false,
		ISPInterface: "", Endpoint: "1.2.3.4:51820",
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventStart, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionColdStartKernel) {
		t.Error("expected ActionColdStartKernel")
	}
	if !hasAction(actions, ActionStartMonitoring) {
		t.Error("expected ActionStartMonitoring")
	}
	if !hasAction(actions, ActionApplyDNSRoutes) {
		t.Error("expected ActionApplyDNSRoutes")
	}
	if !hasAction(actions, ActionPersistRunning) {
		t.Error("expected ActionPersistRunning")
	}
}

func TestDecide_Start_NativeWGTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: false, NWGIndex: 0,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventStart, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("expected ActionStartNativeWG")
	}
	if !hasAction(actions, ActionConfigurePingCheck) {
		t.Error("expected ActionConfigurePingCheck for NativeWG")
	}
	if !hasAction(actions, ActionStartMonitoring) {
		t.Error("expected ActionStartMonitoring")
	}
}

func TestDecide_Start_AlreadyRunning(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Running: true}

	actions := decide(Event{Type: EventStart, Tunnel: "awg0"}, &s)

	if len(actions) != 0 {
		t.Errorf("already running tunnel should produce no actions, got %d", len(actions))
	}
}

func TestDecide_Start_NotFound(t *testing.T) {
	s := newState()

	actions := decide(Event{Type: EventStart, Tunnel: "awg99"}, &s)

	if len(actions) != 0 {
		t.Errorf("nonexistent tunnel should produce no actions, got %d", len(actions))
	}
}

func TestDecide_Start_NoPingCheck(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: false,
	}

	actions := decide(Event{Type: EventStart, Tunnel: "awg0"}, &s)

	if hasAction(actions, ActionStartMonitoring) {
		t.Error("no PingCheck config — should not start monitoring")
	}
	if hasAction(actions, ActionConfigurePingCheck) {
		t.Error("no PingCheck config — should not configure ping check")
	}
}

func TestDecide_Stop_RunningKernel(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: true, Monitoring: true,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventStop, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStopMonitoring) {
		t.Error("expected ActionStopMonitoring BEFORE stop")
	}
	if !hasAction(actions, ActionStopKernel) {
		t.Error("expected ActionStopKernel")
	}
	if !hasAction(actions, ActionRemoveStaticRoutes) {
		t.Error("expected ActionRemoveStaticRoutes")
	}
	if !hasAction(actions, ActionRemoveClientRoutes) {
		t.Error("expected ActionRemoveClientRoutes")
	}
	if !hasAction(actions, ActionPersistStopped) {
		t.Error("expected ActionPersistStopped")
	}
}

func TestDecide_Stop_NotRunning_StillFiresActions(t *testing.T) {
	// Regression: tunnel in NeedsStart (Running=false but NDMS intent up,
	// e.g. after router reboot when auto-start hasn't fired). User clicks
	// Stop to cancel that pending intent. We must still fire StopKernel +
	// PersistStopped — otherwise the API silently logs success while
	// nothing happens, trapping the user (the toggle will keep firing
	// Stop and never Start).
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Running: false}

	actions := decide(Event{Type: EventStop, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStopKernel) {
		t.Error("expected ActionStopKernel even when Running=false (cancel pending NDMS intent)")
	}
	if !hasAction(actions, ActionPersistStopped) {
		t.Error("expected ActionPersistStopped to clear stored.Enabled")
	}
}

func TestDecide_Stop_NativeWG_RemovesPingCheck(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0, Monitoring: true,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventStop, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionRemovePingCheck) {
		t.Error("NativeWG stop should remove NDMS ping-check profile")
	}
	if !hasAction(actions, ActionStopNativeWG) {
		t.Error("expected ActionStopNativeWG")
	}
}

func TestDecide_Stop_NotFound(t *testing.T) {
	s := newState()

	actions := decide(Event{Type: EventStop, Tunnel: "awg99"}, &s)

	if len(actions) != 0 {
		t.Errorf("nonexistent tunnel should produce no actions, got %d", len(actions))
	}
}

func TestDecide_Stop_MonitoringOrder(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: true, Monitoring: true,
	}

	actions := decide(Event{Type: EventStop, Tunnel: "awg0"}, &s)

	// StopMonitoring must come BEFORE StopKernel
	monIdx, stopIdx := -1, -1
	for i, a := range actions {
		if a.Type == ActionStopMonitoring {
			monIdx = i
		}
		if a.Type == ActionStopKernel {
			stopIdx = i
		}
	}
	if monIdx == -1 {
		t.Fatal("missing StopMonitoring")
	}
	if stopIdx == -1 {
		t.Fatal("missing StopKernel")
	}
	if monIdx > stopIdx {
		t.Error("StopMonitoring must come before StopKernel")
	}
}

// === NDMS Hook tests ===

func TestDecide_NDMSHook_IgnoresNonConf(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "nativewg", NWGIndex: 0}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "link", Level: "up"}, &s)

	if len(actions) != 0 {
		t.Errorf("non-conf layer should be ignored, got %d actions", len(actions))
	}
}

func TestDecide_NDMSHook_IgnoresAlreadyRunning(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, Enabled: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "conf", Level: "running"}, &s)

	if len(actions) != 0 {
		t.Errorf("already running should produce no actions, got %d", len(actions))
	}
}

func TestDecide_NDMSHook_IgnoresDisabled(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: false, Enabled: false, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "conf", Level: "running"}, &s)

	if len(actions) != 0 {
		t.Errorf("disabled tunnel should not be started, got %d actions", len(actions))
	}
}

func TestDecide_NDMSHook_IgnoresNoWAN(t *testing.T) {
	s := newState()
	// No WAN up
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: false, Enabled: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "conf", Level: "running"}, &s)

	if len(actions) != 0 {
		t.Errorf("no WAN up should not start tunnel, got %d actions", len(actions))
	}
}

func TestDecide_NDMSHook_StartsEnabledNotRunning(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: false, Enabled: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "conf", Level: "running"}, &s)

	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("enabled, not-running tunnel should be started")
	}
}

func TestDecide_NDMSHook_StopsRunningOnDisabled(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, Enabled: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "conf", Level: "disabled"}, &s)

	if !hasAction(actions, ActionStopNativeWG) {
		t.Error("running tunnel with level=disabled should be stopped")
	}
}

func TestDecide_NDMSHook_UnknownInterface(t *testing.T) {
	s := newState()

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "UnknownIface", Layer: "conf", Level: "running"}, &s)

	if len(actions) != 0 {
		t.Errorf("unknown interface should produce no actions, got %d", len(actions))
	}
}

func TestDecide_NDMSHook_StopAlreadyStopped(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: false, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventNDMSHook, NDMSName: "Wireguard0", Layer: "conf", Level: "disabled"}, &s)

	if len(actions) != 0 {
		t.Errorf("already stopped should produce no actions, got %d", len(actions))
	}
}

// === WAN event tests ===

func TestDecide_WANUp_ResumesNativeWG(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: false, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("WAN up should resume suspended NativeWG tunnel")
	}
}

func TestDecide_WANUp_SkipsWithASC(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: false, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	if hasAction(actions, ActionStartNativeWG) {
		t.Error("ASC firmware: WAN up should not start NativeWG")
	}
}

func TestDecide_WANUp_SkipsAlreadyRunning(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	if hasAction(actions, ActionStartNativeWG) {
		t.Error("already running NativeWG should not be restarted on WAN up")
	}
}

func TestDecide_WANDown_SuspendsNativeWG(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", // bound to eth3
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionSuspendProxy) {
		t.Error("WAN down should suspend NativeWG proxy")
	}
}

func TestDecide_WANDown_SkipsWithASC(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if hasAction(actions, ActionSuspendProxy) {
		t.Error("ASC firmware: WAN down should not suspend NativeWG")
	}
}

func TestDecide_WANDown_FailoverToAlternateWAN(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true } // alternate WAN still up
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", // bound to eth3
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionSuspendProxy) {
		t.Error("expected SuspendProxy")
	}
	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("expected StartNativeWG (failover to alternate WAN)")
	}
}

func TestDecide_WANDown_NoFailoverIfNoAlternateWAN(t *testing.T) {
	s := newState()
	s.supportsASC = false
	// No other WANs up
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", // bound to eth3
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionSuspendProxy) {
		t.Error("expected SuspendProxy")
	}
	if hasAction(actions, ActionStartNativeWG) {
		t.Error("no alternate WAN — should NOT failover")
	}
}

// === Delete tests ===

func TestDecide_Delete_RunningTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: true, Monitoring: true,
	}

	actions := decide(Event{Type: EventDelete, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStopMonitoring) {
		t.Error("delete should stop monitoring first")
	}
	if !hasAction(actions, ActionDeleteKernel) {
		t.Error("expected ActionDeleteKernel")
	}
	if !hasAction(actions, ActionDeleteStaticRoutes) {
		t.Error("expected static route delete cleanup")
	}
	if !hasAction(actions, ActionDeleteClientRoutes) {
		t.Error("expected client route delete cleanup")
	}
	if !hasAction(actions, ActionDeleteDNSRoutes) {
		t.Error("expected DNS route delete cleanup")
	}
}

func TestDecide_Delete_StoppedTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: false, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventDelete, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionDeleteNativeWG) {
		t.Error("expected ActionDeleteNativeWG even for stopped tunnel")
	}
	if hasAction(actions, ActionStopNativeWG) {
		t.Error("should not stop already stopped tunnel")
	}
}

func TestDecide_Delete_NotFound(t *testing.T) {
	s := newState()

	actions := decide(Event{Type: EventDelete, Tunnel: "awg99"}, &s)

	if len(actions) != 0 {
		t.Errorf("nonexistent tunnel should produce no actions, got %d", len(actions))
	}
}

func TestDecide_Delete_RunningNativeWG_RemovesPingCheck(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, Monitoring: true, NWGIndex: 0,
		PingCheck: &storage.TunnelPingCheck{Enabled: true},
	}

	actions := decide(Event{Type: EventDelete, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStopMonitoring) {
		t.Error("expected ActionStopMonitoring")
	}
	if !hasAction(actions, ActionRemovePingCheck) {
		t.Error("running NativeWG delete should remove ping-check profile")
	}
	if !hasAction(actions, ActionDeleteNativeWG) {
		t.Error("expected ActionDeleteNativeWG")
	}
}

func TestDecide_Delete_DNSRouteCleanup(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{ID: "awg0", Backend: "kernel", Running: false}

	actions := decide(Event{Type: EventDelete, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionDeleteDNSRoutes) {
		t.Error("delete should call OnTunnelDelete for DNS routes")
	}
	if hasAction(actions, ActionApplyDNSRoutes) {
		t.Error("delete should NOT use ApplyDNSRoutes (reconcile without cleanup)")
	}
}

func TestDecide_Delete_RouteCleanupBeforeInterfaceDelete(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventDelete, Tunnel: "awg0"}, &s)

	// All route Delete* actions must come BEFORE ActionDeleteNativeWG.
	deleteIdx := -1
	for i, a := range actions {
		if a.Type == ActionDeleteNativeWG {
			deleteIdx = i
			break
		}
	}
	if deleteIdx == -1 {
		t.Fatal("missing ActionDeleteNativeWG")
	}

	for _, routeAction := range []ActionType{ActionDeleteDNSRoutes, ActionDeleteStaticRoutes, ActionDeleteClientRoutes} {
		idx := -1
		for i, a := range actions {
			if a.Type == routeAction {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("missing action type %d", routeAction)
		}
		if idx > deleteIdx {
			t.Errorf("route cleanup action %d (index %d) must come before ActionDeleteNativeWG (index %d)", routeAction, idx, deleteIdx)
		}
	}
}

// === Restart tests ===

func TestDecide_Restart_StopThenStart(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: true, Monitoring: true,
	}

	actions := decide(Event{Type: EventRestart, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStopKernel) {
		t.Error("restart should stop first")
	}
	if !hasAction(actions, ActionColdStartKernel) {
		t.Error("restart should start after stop")
	}
}

func TestDecide_Restart_NativeWG(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventRestart, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionStopNativeWG) {
		t.Error("restart should stop NativeWG")
	}
	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("restart should start NativeWG")
	}
}

func TestDecide_Restart_NotRunning(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: false,
	}

	actions := decide(Event{Type: EventRestart, Tunnel: "awg0"}, &s)

	// Restart of stopped tunnel = just start
	if !hasAction(actions, ActionColdStartKernel) {
		t.Error("restart of stopped tunnel should just start it")
	}
	if hasAction(actions, ActionStopKernel) {
		t.Error("stopped tunnel should not be stopped again")
	}
}

func TestDecide_Restart_NotFound(t *testing.T) {
	s := newState()

	actions := decide(Event{Type: EventRestart, Tunnel: "awg99"}, &s)

	if len(actions) != 0 {
		t.Errorf("nonexistent tunnel should produce no actions, got %d", len(actions))
	}
}

func TestDecide_Restart_StopOrder(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: true, Monitoring: true,
	}

	actions := decide(Event{Type: EventRestart, Tunnel: "awg0"}, &s)

	// Stop actions must come before start actions
	stopIdx := -1
	startIdx := -1
	for i, a := range actions {
		if a.Type == ActionStopKernel && stopIdx == -1 {
			stopIdx = i
		}
		if a.Type == ActionColdStartKernel && startIdx == -1 {
			startIdx = i
		}
	}
	if stopIdx == -1 || startIdx == -1 {
		t.Fatal("missing stop or start action")
	}
	if stopIdx > startIdx {
		t.Error("stop must come before start in restart")
	}
}

// === PingCheck failure tests ===

func TestDecide_PingCheckFailed_KernelLinkToggle(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: true,
	}

	actions := decide(Event{Type: EventPingCheckFailed, Tunnel: "awg0"}, &s)

	if !hasAction(actions, ActionLinkToggle) {
		t.Error("kernel tunnel ping failure should produce ActionLinkToggle")
	}
}

func TestDecide_PingCheckFailed_NativeWGIgnored(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Running: true, NWGIndex: 0,
	}

	actions := decide(Event{Type: EventPingCheckFailed, Tunnel: "awg0"}, &s)

	if len(actions) != 0 {
		t.Errorf("NativeWG ping failure handled by NDMS, should produce no actions, got %d", len(actions))
	}
}

func TestDecide_PingCheckFailed_NotRunning(t *testing.T) {
	s := newState()
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "kernel", Running: false,
	}

	actions := decide(Event{Type: EventPingCheckFailed, Tunnel: "awg0"}, &s)

	if len(actions) != 0 {
		t.Errorf("not running tunnel should produce no actions, got %d", len(actions))
	}
}

// === WAN binding tests ===

func TestDecide_WANDown_OnlySuspendsBoundTunnels(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", // bound to eth3
	}
	s.tunnels["awg1"] = &tunnelState{
		ID: "awg1", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 1,
		ISPInterface: "ppp0", ActiveWAN: "ppp0", // bound to ppp0
	}

	// eth3 goes down — only awg0 should be suspended
	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	suspends := filterActions(actions, ActionSuspendProxy)
	if len(suspends) != 1 {
		t.Errorf("expected 1 suspend (awg0 only), got %d", len(suspends))
	}
	if len(suspends) > 0 && suspends[0].Tunnel != "awg0" {
		t.Errorf("expected awg0 suspended, got %s", suspends[0].Tunnel)
	}
}

func TestDecide_WANDown_AutoModeSuspendsOnAnyWAN(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "", ActiveWAN: "eth3", // auto mode, currently using eth3
	}

	// eth3 goes down — auto-mode tunnel should be suspended (it was using eth3)
	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionSuspendProxy) {
		t.Error("auto-mode tunnel using downed WAN should be suspended")
	}
}

func TestDecide_WANDown_AutoModeNotAffectedByOtherWAN(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "", ActiveWAN: "ppp0", // auto mode, currently using ppp0
	}

	// eth3 goes down — tunnel uses ppp0, not affected
	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	suspends := filterActions(actions, ActionSuspendProxy)
	if len(suspends) != 0 {
		t.Errorf("tunnel using different WAN should not be suspended, got %d suspends", len(suspends))
	}
}

func TestDecide_WANUp_OnlyResumesMatchingTunnels(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: false, NWGIndex: 0,
		ISPInterface: "eth3", // bound to eth3
	}
	s.tunnels["awg1"] = &tunnelState{
		ID: "awg1", Backend: "nativewg", Enabled: true, Running: false, NWGIndex: 1,
		ISPInterface: "ppp0", // bound to ppp0 — should NOT start on eth3 up
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	starts := filterActions(actions, ActionStartNativeWG)
	if len(starts) != 1 {
		t.Errorf("expected 1 start (awg0 only), got %d", len(starts))
	}
	if len(starts) > 0 && starts[0].Tunnel != "awg0" {
		t.Errorf("expected awg0 started, got %s", starts[0].Tunnel)
	}
}

func TestDecide_WANUp_AutoModeStartsOnAnyWAN(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: false, NWGIndex: 0,
		ISPInterface: "", // auto mode — starts on any WAN
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("auto-mode tunnel should start on any WAN up")
	}
}

func TestDecide_WANDown_FailoverOnlyForAffectedTunnels(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true } // alternate WAN
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", // bound to eth3 — affected
	}
	s.tunnels["awg1"] = &tunnelState{
		ID: "awg1", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 1,
		ISPInterface: "ppp0", ActiveWAN: "ppp0", // bound to ppp0 — NOT affected
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	// awg0: suspend + failover start. awg1: nothing.
	suspends := filterActions(actions, ActionSuspendProxy)
	starts := filterActions(actions, ActionStartNativeWG)
	if len(suspends) != 1 || suspends[0].Tunnel != "awg0" {
		t.Errorf("only awg0 should be suspended, got %v", suspends)
	}
	if len(starts) != 1 || starts[0].Tunnel != "awg0" {
		t.Errorf("only awg0 should failover, got %v", starts)
	}
}

// === Test helpers ===

func filterActions(actions []Action, typ ActionType) []Action {
	var result []Action
	for _, a := range actions {
		if a.Type == typ {
			result = append(result, a)
		}
	}
	return result
}

func hasAction(actions []Action, typ ActionType) bool {
	return len(filterActions(actions, typ)) > 0
}

func assertNoActionForTunnel(t *testing.T, actions []Action, tunnelID string, typ ActionType) {
	t.Helper()
	for _, a := range actions {
		if a.Type == typ && a.Tunnel == tunnelID {
			t.Errorf("unexpected %v for tunnel %s", typ, tunnelID)
		}
	}
}

func TestDecide_Boot_ReconcilesRunningKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
	}

	actions := decide(Event{Type: EventBoot}, &s)

	// Running kernel tunnel should be reconciled, not cold-started.
	reconciles := filterActions(actions, ActionReconcileKernel)
	if len(reconciles) != 1 {
		t.Errorf("expected 1 ReconcileKernel for running tunnel, got %d", len(reconciles))
	}
	coldStarts := filterActions(actions, ActionColdStartKernel)
	if len(coldStarts) != 0 {
		t.Errorf("expected 0 ColdStartKernel for running tunnel, got %d", len(coldStarts))
	}
}

func TestDecide_Boot_ColdStartsStoppedKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: false,
	}

	actions := decide(Event{Type: EventBoot}, &s)

	coldStarts := filterActions(actions, ActionColdStartKernel)
	if len(coldStarts) != 1 {
		t.Errorf("expected 1 ColdStartKernel for stopped tunnel, got %d", len(coldStarts))
	}
}

func TestDecide_Reconnect_ReconcilesRunningKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	reconciles := filterActions(actions, ActionReconcileKernel)
	if len(reconciles) != 1 {
		t.Errorf("expected 1 ReconcileKernel on reconnect, got %d", len(reconciles))
	}
}

func TestDecide_Reconnect_DoesNotReconcileStoppedKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: false,
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	reconciles := filterActions(actions, ActionReconcileKernel)
	if len(reconciles) != 0 {
		t.Errorf("stopped tunnel should not be reconciled on reconnect, got %d", len(reconciles))
	}
}

func TestDecide_WANDown_SuspendsAffectedKernelTunnel(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
		ISPInterface: "eth3", ActiveWAN: "eth3",
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	suspends := filterActions(actions, ActionSuspendKernel)
	if len(suspends) != 1 {
		t.Errorf("expected 1 SuspendKernel for affected tunnel, got %d", len(suspends))
	}
}

func TestDecide_WANDown_DoesNotSuspendUnaffectedKernelTunnel(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
		ISPInterface: "eth4", ActiveWAN: "eth4",
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	suspends := filterActions(actions, ActionSuspendKernel)
	if len(suspends) != 0 {
		t.Errorf("unaffected tunnel should not be suspended, got %d", len(suspends))
	}
}

func TestDecide_WANDown_SuspendsAutoBoundKernelTunnel(t *testing.T) {
	s := newState()
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
		ISPInterface: "", ActiveWAN: "eth3", // auto mode, currently on eth3
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	suspends := filterActions(actions, ActionSuspendKernel)
	if len(suspends) != 1 {
		t.Errorf("auto-mode tunnel on downed WAN should be suspended, got %d", len(suspends))
	}
}

func TestDecide_WANUp_ResumesKernelTunnelBoundToWAN(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
		ISPInterface: "eth3",
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	resumes := filterActions(actions, ActionResumeKernel)
	if len(resumes) != 1 {
		t.Errorf("expected 1 ResumeKernel for matching tunnel, got %d", len(resumes))
	}
}

func TestDecide_WANUp_StartsStoppedKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: false,
		ISPInterface: "eth3",
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	starts := filterActions(actions, ActionColdStartKernel)
	if len(starts) != 1 {
		t.Errorf("expected 1 ColdStartKernel for stopped tunnel on WAN up, got %d", len(starts))
	}
}

func TestDecide_WANUp_DoesNotResumeUnboundKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
		ISPInterface: "eth4",
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	resumes := filterActions(actions, ActionResumeKernel)
	if len(resumes) != 0 {
		t.Errorf("kernel tunnel bound to other WAN should not be resumed, got %d", len(resumes))
	}
}

// Auto-mode tunnel suspended (was on eth3 which went down).
// On any WAN coming up: must Reconcile (re-resolve WAN, refresh endpoint route),
// not just Resume (Resume only does ip link up on the dead interface).
func TestDecide_WANUp_ReconcilesAutoModeKernelTunnel(t *testing.T) {
	s := newState()
	s.tunnels["awg10"] = &tunnelState{
		ID: "awg10", Backend: "kernel", Enabled: true, Running: true,
		ISPInterface: "", ActiveWAN: "eth3", // auto mode, was on eth3
	}

	// eth4 (different WAN) comes up
	actions := decide(Event{Type: EventWANUp, WANIface: "eth4"}, &s)

	reconciles := filterActions(actions, ActionReconcileKernel)
	if len(reconciles) != 1 {
		t.Errorf("expected 1 ReconcileKernel for auto-mode tunnel on WAN up, got %d", len(reconciles))
	}
	resumes := filterActions(actions, ActionResumeKernel)
	if len(resumes) != 0 {
		t.Errorf("auto-mode tunnel should NOT be resumed (Resume only does link up on dead WAN), got %d", len(resumes))
	}
}

func TestDecide_WANDown_NativeWGAutoMode_WithActiveWAN_TriggersFailover(t *testing.T) {
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true } // backup WAN available
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "", ActiveWAN: "ppp0", // auto mode, NDMS picked ppp0
	}

	// ppp0 falls — auto-mode nativewg with ActiveWAN=ppp0 must suspend AND restart
	actions := decide(Event{Type: EventWANDown, WANIface: "ppp0"}, &s)

	if !hasAction(actions, ActionSuspendProxy) {
		t.Error("expected ActionSuspendProxy for auto-mode nativewg whose ActiveWAN matches downed WAN")
	}
	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("expected ActionStartNativeWG (failover restart) when backup WAN is available")
	}
}

func TestDecide_WANDown_NativeWGAutoMode_StaleActiveWAN_NoFailover(t *testing.T) {
	// Regression guard: this is the bug. If ActiveWAN is empty (the historical
	// state for nativewg before this fix), the tunnel is not considered affected.
	s := newState()
	s.supportsASC = false
	s.anyWANUpFn = func() bool { return true }
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "", ActiveWAN: "", // stale: ActiveWAN never populated
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "ppp0"}, &s)

	if hasAction(actions, ActionSuspendProxy) {
		t.Error("with empty ActiveWAN the tunnel must NOT be considered affected (documents the bug surface)")
	}
}

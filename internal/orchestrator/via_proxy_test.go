package orchestrator

import "testing"

// На ASC-прошивке туннель с конфигом 3.x идёт через awg_proxy: NDMS про слот
// не знает, поэтому ASC-ветки решений для такого туннеля неприменимы — иначе
// после ребута его никто не поднимет, а при падении WAN слот останется висеть
// на упавшем интерфейсе.

func TestDecide_Boot_ReconcilesProxyTunnelOnASCFirmware(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg20"] = &tunnelState{
		ID: "awg20", Backend: "nativewg", Enabled: true, NWGIndex: 0, ViaProxy: true,
	}

	actions := decide(Event{Type: EventBoot}, &s)

	if n := len(filterActions(actions, ActionReconcileNativeWG)); n != 1 {
		t.Errorf("proxy-туннель после ребута обязан получить ReconcileNativeWG, got %d", n)
	}
}

func TestDecide_Reconnect_RestoresProxySlotOnASCFirmware(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg20"] = &tunnelState{
		ID: "awg20", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0, ViaProxy: true,
	}

	actions := decide(Event{Type: EventReconnect}, &s)

	if !hasAction(actions, ActionRestoreKmod) {
		t.Error("после рестарта демона слот proxy-туннеля обязан восстанавливаться")
	}
}

func TestDecide_WANDown_SuspendsProxyTunnelOnASCFirmware(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg20"] = &tunnelState{
		ID: "awg20", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", ViaProxy: true,
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionSuspendProxy) {
		t.Error("падение WAN обязано снимать слот proxy-туннеля: он привязан к упавшему интерфейсу")
	}
}

func TestDecide_WANUp_ResumesProxyTunnelOnASCFirmware(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg20"] = &tunnelState{
		ID: "awg20", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3", ViaProxy: true,
	}

	actions := decide(Event{Type: EventWANUp, WANIface: "eth3"}, &s)

	if !hasAction(actions, ActionStartNativeWG) {
		t.Error("возврат WAN обязан перезапускать proxy-туннель")
	}
}

// ASC-туннель (конфиг <= 2.0) на той же прошивке остаётся на нативном пути.
func TestDecide_WANDown_StillSkipsASCTunnel(t *testing.T) {
	s := newState()
	s.supportsASC = true
	s.tunnels["awg0"] = &tunnelState{
		ID: "awg0", Backend: "nativewg", Enabled: true, Running: true, NWGIndex: 0,
		ISPInterface: "eth3", ActiveWAN: "eth3",
	}

	actions := decide(Event{Type: EventWANDown, WANIface: "eth3"}, &s)

	if hasAction(actions, ActionSuspendProxy) {
		t.Error("у ASC-туннеля слота нет — снимать нечего")
	}
}

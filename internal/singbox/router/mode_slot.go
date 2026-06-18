package router

import "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"

// RouterSlotForMode returns the orchestrator slot that carries the routing
// config for the given RoutingMode: SlotFakeIP for "fakeip-tun", else
// SlotRouter (tproxy). The two are mutually exclusive (XOR).
func RouterSlotForMode(mode string) orchestrator.Slot {
	if mode == "fakeip-tun" {
		return orchestrator.SlotFakeIP
	}
	return orchestrator.SlotRouter
}

// OtherRouterSlot returns the routing slot NOT selected by mode (the one to
// disable). It is the complement of RouterSlotForMode.
func OtherRouterSlot(mode string) orchestrator.Slot {
	if mode == "fakeip-tun" {
		return orchestrator.SlotRouter
	}
	return orchestrator.SlotFakeIP
}

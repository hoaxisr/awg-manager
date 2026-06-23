package router

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

func TestRouterSlotForMode(t *testing.T) {
	tests := []struct {
		mode string
		want orchestrator.Slot
	}{
		{"fakeip-tun", orchestrator.SlotFakeIP},
		{"tproxy", orchestrator.SlotRouter},
		{"", orchestrator.SlotRouter},
	}
	for _, tc := range tests {
		got := RouterSlotForMode(tc.mode)
		if got != tc.want {
			t.Errorf("RouterSlotForMode(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestOtherRouterSlot(t *testing.T) {
	tests := []struct {
		mode string
		want orchestrator.Slot
	}{
		{"fakeip-tun", orchestrator.SlotRouter},
		{"tproxy", orchestrator.SlotFakeIP},
		{"", orchestrator.SlotFakeIP},
	}
	for _, tc := range tests {
		got := OtherRouterSlot(tc.mode)
		if got != tc.want {
			t.Errorf("OtherRouterSlot(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestRouterSlotForMode_Complement(t *testing.T) {
	modes := []string{"fakeip-tun", "tproxy", ""}
	for _, mode := range modes {
		primary := RouterSlotForMode(mode)
		other := OtherRouterSlot(mode)
		if primary == other {
			t.Errorf("mode %q: RouterSlotForMode and OtherRouterSlot returned the same slot %q", mode, primary)
		}
		// verify they are exactly the two routing slots
		slots := map[orchestrator.Slot]bool{orchestrator.SlotRouter: true, orchestrator.SlotFakeIP: true}
		if !slots[primary] {
			t.Errorf("mode %q: RouterSlotForMode returned unexpected slot %q", mode, primary)
		}
		if !slots[other] {
			t.Errorf("mode %q: OtherRouterSlot returned unexpected slot %q", mode, other)
		}
	}
}

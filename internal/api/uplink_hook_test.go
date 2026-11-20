package api

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/events"
)

func TestUplinkWANHookNotifiesAddressChangeWithoutReplacingOtherHooks(t *testing.T) {
	h := NewHookHandler(nil, nil, nil)
	nudges, endpointNudges := 0, 0
	h.SetEndpointGuardNudge(func() { endpointNudges++ })
	h.SetUplinkWANChangeNudge(func(id, device string) {
		nudges++
		if id != "ISP" || device != "wan0" {
			t.Errorf("wrong WAN: %s %s", id, device)
		}
	})
	h.Handle(events.Event{Type: events.EventIfIPChanged, ID: "ISP", SystemName: "wan0"})
	if nudges != 1 || endpointNudges != 1 {
		t.Fatal("one hook replaced the other", nudges, endpointNudges)
	}
	h.Handle(events.Event{Type: events.EventIfLayerChanged, ID: "ISP", SystemName: "wan0", Layer: "conf"})
	if nudges != 1 {
		t.Fatal("non-WAN configuration event woke the controller")
	}
}

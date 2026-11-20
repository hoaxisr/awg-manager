package orchestrator

import "testing"

func TestKeenDNSCloudSlotPrecedesRoutingSlots(t *testing.T) {
	slots := KnownSlots()
	if len(slots) < 3 || slots[1].Slot != SlotKeenDNSCloud || slots[1].AlwaysOn {
		t.Fatalf("expected an optional KeenDNS Cloud slot immediately after base: %+v", slots)
	}
	for _, slot := range slots[2:] {
		if slot.Filename <= slots[1].Filename {
			t.Errorf("slot %s would be merged before KeenDNS Cloud", slot.Filename)
		}
	}
}

func TestKnownSlotsIncludesDNSRewritesBeforeRouter(t *testing.T) {
	slots := KnownSlots()
	idxRewrites, idxRouter := -1, -1
	for i, m := range slots {
		switch m.Slot {
		case SlotDNSRewrites:
			idxRewrites = i
		case SlotRouter:
			idxRouter = i
		}
	}
	if idxRewrites < 0 {
		t.Fatal("SlotDNSRewrites not registered")
	}
	if idxRewrites >= idxRouter {
		t.Errorf("slot order: rewrites=%d router=%d", idxRewrites, idxRouter)
	}
}

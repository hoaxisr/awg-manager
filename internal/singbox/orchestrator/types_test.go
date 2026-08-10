package orchestrator

import "testing"

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

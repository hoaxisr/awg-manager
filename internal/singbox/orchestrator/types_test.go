package orchestrator

import "testing"

func TestKnownSlotsIncludesDNSRewritesBeforeRouter(t *testing.T) {
	slots := KnownSlots()
	idxRewrites, idxSelective, idxRouter := -1, -1, -1
	for i, m := range slots {
		switch m.Slot {
		case SlotDNSRewrites:
			idxRewrites = i
		case SlotSelectiveRoutes:
			idxSelective = i
		case SlotRouting:
			idxRouter = i
		}
	}
	if idxRewrites < 0 {
		t.Fatal("SlotDNSRewrites not registered")
	}
	if idxSelective < 0 {
		t.Fatal("SlotSelectiveRoutes not registered")
	}
	if !(idxRewrites < idxSelective && idxSelective < idxRouter) {
		t.Errorf("slot order: rewrites=%d selective=%d router=%d", idxRewrites, idxSelective, idxRouter)
	}
}

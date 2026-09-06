package traffic

import (
	"testing"
	"time"
)

// prune: запись без живых точек удаляется целиком, частично протухшая —
// обрезается по префиксу. Граница ровно на cutoff здесь НЕ пинуется: cutoff
// считается от time.Now() без шва часов, и попасть в неё фикстурой нечем.
func TestPrune_ThreeOutcomes(t *testing.T) {
	h := New()
	defer h.Stop()
	h.mu.Lock()
	h.maxAge = time.Hour
	now := time.Now().Unix()
	h.tunnels["dead"] = &tunnelHistory{points: []Point{{Timestamp: now - 7200}, {Timestamp: now - 5400}}}
	h.tunnels["mixed"] = &tunnelHistory{points: []Point{{Timestamp: now - 7200}, {Timestamp: now - 1800, RxRate: 7}, {Timestamp: now - 60, RxRate: 9}}}
	h.tunnels["fresh"] = &tunnelHistory{points: []Point{{Timestamp: now - 60}}}
	h.mu.Unlock()

	h.prune()

	h.mu.RLock()
	defer h.mu.RUnlock()
	if _, ok := h.tunnels["dead"]; ok {
		t.Fatal("полностью протухшая запись обязана удаляться")
	}
	if got := h.tunnels["mixed"].points; len(got) != 2 || got[0].RxRate != 7 || got[1].RxRate != 9 {
		t.Fatalf("mixed = %+v, want две живые точки", got)
	}
	if got := h.tunnels["fresh"].points; len(got) != 1 {
		t.Fatalf("fresh = %+v", got)
	}
}

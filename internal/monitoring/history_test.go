package monitoring

import (
	"testing"
	"time"
)

func mkSample(ms int, ok bool) Sample {
	if !ok {
		return Sample{TS: time.Now(), OK: false}
	}
	v := ms
	return Sample{TS: time.Now(), LatencyMs: &v, OK: true}
}

func TestHistory_AppendAndGet(t *testing.T) {
	h := NewHistory()
	for i := 0; i < 10; i++ {
		h.Append("tgt", "tn", mkSample(i, true))
	}
	got := h.Get("tgt", "tn", 0)
	if len(got) != 10 {
		t.Errorf("got %d samples, want 10", len(got))
	}
	if got[0].LatencyMs == nil || *got[0].LatencyMs != 0 {
		t.Errorf("oldest sample latency mismatch: %+v", got[0])
	}
	if got[9].LatencyMs == nil || *got[9].LatencyMs != 9 {
		t.Errorf("newest sample latency mismatch: %+v", got[9])
	}
}

func TestHistory_RingCapacity(t *testing.T) {
	h := NewHistory()
	for i := 0; i < HistoryCapacity+25; i++ {
		h.Append("tgt", "tn", mkSample(i, true))
	}
	got := h.Get("tgt", "tn", 0)
	if len(got) != HistoryCapacity {
		t.Errorf("expected capacity %d, got %d", HistoryCapacity, len(got))
	}
	// Oldest should now be HistoryCapacity-th sample (i=25).
	if got[0].LatencyMs == nil || *got[0].LatencyMs != 25 {
		t.Errorf("oldest after rollover should be 25, got %+v", got[0])
	}
}

func TestHistory_GetLimit(t *testing.T) {
	h := NewHistory()
	for i := 0; i < 30; i++ {
		h.Append("tgt", "tn", mkSample(i, true))
	}
	got := h.Get("tgt", "tn", 5)
	if len(got) != 5 {
		t.Errorf("expected 5 samples with limit=5, got %d", len(got))
	}
	if got[0].LatencyMs == nil || *got[0].LatencyMs != 25 {
		t.Errorf("limit slice should be tail; first should be 25, got %+v", got[0])
	}
}

func TestHistory_Latest(t *testing.T) {
	h := NewHistory()
	if h.Latest("tgt", "tn") != nil {
		t.Errorf("expected nil for empty history")
	}
	h.Append("tgt", "tn", mkSample(42, true))
	got := h.Latest("tgt", "tn")
	if got == nil || got.LatencyMs == nil || *got.LatencyMs != 42 {
		t.Errorf("expected latest=42, got %+v", got)
	}
}

func TestHistory_PruneTunnels(t *testing.T) {
	h := NewHistory()
	h.Append("tgt", "tn-A", mkSample(1, true))
	h.Append("tgt", "tn-B", mkSample(2, true))
	h.Append("tgt", "tn-C", mkSample(3, true))
	h.PruneTunnels(map[string]bool{"tn-A": true, "tn-C": true})
	if len(h.Get("tgt", "tn-A", 0)) != 1 {
		t.Errorf("tn-A should remain")
	}
	if len(h.Get("tgt", "tn-B", 0)) != 0 {
		t.Errorf("tn-B should be pruned")
	}
	if len(h.Get("tgt", "tn-C", 0)) != 1 {
		t.Errorf("tn-C should remain")
	}
}

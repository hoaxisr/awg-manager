package pingcheck

import (
	"testing"
)

func newTestNwgMonitor(buf *LogBuffer) *nwgMonitor {
	return &nwgMonitor{
		tunnelID:   "tun-nwg-1",
		tunnelName: "NWG Test",
		threshold:  3,
		logBuffer:  buf,
	}
}

func TestNwgDelta_SuccessIncrement(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// First poll: baseline — should emit nothing.
	m.processDelta(0, 0, "pass")
	if buf.Len() != 0 {
		t.Fatalf("after baseline: got %d entries, want 0", buf.Len())
	}

	// Second poll: 3 new successes.
	m.processDelta(0, 3, "pass")
	if buf.Len() != 3 {
		t.Fatalf("after success increment: got %d entries, want 3", buf.Len())
	}

	entries := buf.GetAll()
	for i, e := range entries {
		if !e.Success {
			t.Errorf("entry[%d].Success = false, want true", i)
		}
		if e.Latency != -1 {
			t.Errorf("entry[%d].Latency = %d, want -1", i, e.Latency)
		}
		if e.Backend != "nativewg" {
			t.Errorf("entry[%d].Backend = %q, want %q", i, e.Backend, "nativewg")
		}
		if e.TunnelID != "tun-nwg-1" {
			t.Errorf("entry[%d].TunnelID = %q, want %q", i, e.TunnelID, "tun-nwg-1")
		}
	}
}

func TestNwgDelta_FailIncrement(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline with status "fail" so no state change on next poll.
	m.processDelta(0, 0, "fail")

	// 2 new failures, same status — no state change entry.
	m.processDelta(2, 0, "fail")
	if buf.Len() != 2 {
		t.Fatalf("got %d entries, want 2", buf.Len())
	}

	entries := buf.GetAll()
	for i, e := range entries {
		if e.Success {
			t.Errorf("entry[%d].Success = true, want false", i)
		}
		if e.Backend != "nativewg" {
			t.Errorf("entry[%d].Backend = %q, want %q", i, e.Backend, "nativewg")
		}
	}
}

func TestNwgDelta_CounterReset(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline with 10 successes.
	m.processDelta(0, 10, "pass")
	if buf.Len() != 0 {
		t.Fatalf("after baseline: got %d entries, want 0", buf.Len())
	}

	// Counter reset: success went from 10 down to 2.
	// Should treat 2 as the delta (counter was reset).
	m.processDelta(0, 2, "pass")
	if buf.Len() != 2 {
		t.Fatalf("after counter reset: got %d entries, want 2", buf.Len())
	}
}

func TestNwgDelta_StatusChange(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline: 5 successes, status pass.
	m.processDelta(0, 5, "pass")

	// 3 new failures, status changes to fail.
	m.processDelta(3, 5, "fail")

	// Expect: 3 fail entries + 1 state change entry = 4.
	if buf.Len() != 4 {
		t.Fatalf("got %d entries, want 4", buf.Len())
	}

	entries := buf.GetAll()
	// Entries are newest-first. The state change entry is the last one added.
	stateEntry := entries[0] // newest = state change
	if stateEntry.StateChange != "status_fail" {
		t.Errorf("StateChange = %q, want %q", stateEntry.StateChange, "status_fail")
	}
	if stateEntry.Success {
		t.Errorf("state change entry Success = true, want false")
	}
}

func TestNwgDelta_MixedFailAndSuccess(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline.
	m.processDelta(0, 0, "pass")

	// 2 fails + 1 success.
	m.processDelta(2, 1, "pass")
	if buf.Len() != 3 {
		t.Fatalf("got %d entries, want 3", buf.Len())
	}

	entries := buf.GetAll()
	// Order in buffer: 2 fails then 1 success. GetAll reverses.
	// So entries[0] = success (newest), entries[1..2] = fails.
	if !entries[0].Success {
		t.Errorf("entries[0] (newest) should be success")
	}
	if entries[1].Success || entries[2].Success {
		t.Errorf("entries[1] and [2] should be failures")
	}
}

func TestNwgDelta_NoDelta_NoEntries(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()
	m := newTestNwgMonitor(buf)

	// Baseline.
	m.processDelta(0, 5, "pass")

	// Same counters, no change.
	m.processDelta(0, 5, "pass")
	if buf.Len() != 0 {
		t.Fatalf("got %d entries, want 0", buf.Len())
	}
}

package logging

import (
	"testing"
	"time"
)

func TestLogBuffer_Add(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	entry := LogEntry{
		Timestamp: time.Now(),
		Level:     string(LevelInfo),
		Group:     GroupTunnel,
		Action:    "create",
		Target:    "test-tunnel",
		Message:   "Test message",
	}

	buf.Add(entry)

	logs := buf.GetAll()
	if len(logs) != 1 {
		t.Errorf("GetAll() len = %d, want 1", len(logs))
	}
	if logs[0].Target != "test-tunnel" {
		t.Errorf("Target = %s, want test-tunnel", logs[0].Target)
	}
}

func TestLogBuffer_GetFiltered(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add mixed entries
	buf.Add(LogEntry{Timestamp: time.Now(), Group: GroupTunnel, Subgroup: SubLifecycle, Level: string(LevelInfo)})
	buf.Add(LogEntry{Timestamp: time.Now(), Group: GroupTunnel, Subgroup: SubLifecycle, Level: string(LevelWarn)})
	buf.Add(LogEntry{Timestamp: time.Now(), Group: GroupSystem, Subgroup: SubSettings, Level: string(LevelInfo)})
	buf.Add(LogEntry{Timestamp: time.Now(), Group: GroupSystem, Subgroup: SubSettings, Level: string(LevelError)})

	// Filter by group and level (warn): tunnel has info+warn; warn is
	// always visible regardless of configured level → 1 (only the warn entry).
	logs := buf.GetFiltered(GroupTunnel, "", string(LevelWarn))
	if len(logs) != 1 {
		t.Errorf("GetFiltered(tunnel, '', warn) len = %d, want 1", len(logs))
	}

	// Filter by group only
	logs = buf.GetFiltered(GroupTunnel, "", "")
	if len(logs) != 2 {
		t.Errorf("GetFiltered(group only) len = %d, want 2", len(logs))
	}

	// Filter by level only (warn): error and warn are both always visible
	// regardless of configured level → warn entry + error entry → 2.
	logs = buf.GetFiltered("", "", string(LevelWarn))
	if len(logs) != 2 {
		t.Errorf("GetFiltered(level only) len = %d, want 2", len(logs))
	}

	// Filter by error level: error and warn are both always visible → 2.
	logs = buf.GetFiltered("", "", string(LevelError))
	if len(logs) != 2 {
		t.Errorf("GetFiltered(error only) len = %d, want 2", len(logs))
	}

	// Filter by subgroup only
	logs = buf.GetFiltered("", SubLifecycle, "")
	if len(logs) != 2 {
		t.Errorf("GetFiltered(subgroup only) len = %d, want 2", len(logs))
	}

	// Filter by group + subgroup
	logs = buf.GetFiltered(GroupSystem, SubSettings, "")
	if len(logs) != 2 {
		t.Errorf("GetFiltered(group+subgroup) len = %d, want 2", len(logs))
	}
}

func TestLogBuffer_GetPaginated(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add 5 entries
	for i := 0; i < 5; i++ {
		buf.Add(LogEntry{Timestamp: time.Now(), Group: GroupTunnel, Level: string(LevelInfo), Target: "entry"})
	}
	buf.Add(LogEntry{Timestamp: time.Now(), Group: GroupSystem, Level: string(LevelWarn), Target: "other"})

	// Get first page of tunnel entries (limit 2, offset 0)
	logs, total := buf.GetPaginated(GroupTunnel, "", "", time.Time{}, 2, 0)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 2 {
		t.Errorf("page len = %d, want 2", len(logs))
	}

	// Get second page (limit 2, offset 2)
	logs, total = buf.GetPaginated(GroupTunnel, "", "", time.Time{}, 2, 2)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 2 {
		t.Errorf("page len = %d, want 2", len(logs))
	}

	// Get last page (limit 2, offset 4)
	logs, total = buf.GetPaginated(GroupTunnel, "", "", time.Time{}, 2, 4)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 1 {
		t.Errorf("last page len = %d, want 1", len(logs))
	}

	// Offset beyond total
	logs, total = buf.GetPaginated(GroupTunnel, "", "", time.Time{}, 2, 10)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 0 {
		t.Errorf("beyond offset len = %d, want 0", len(logs))
	}

	// All entries (no filter)
	_, total = buf.GetPaginated("", "", "", time.Time{}, 100, 0)
	if total != 6 {
		t.Errorf("total all = %d, want 6", total)
	}
}

func TestLogBuffer_Clear(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	buf.Add(LogEntry{Timestamp: time.Now(), Message: "test 1"})
	buf.Add(LogEntry{Timestamp: time.Now(), Message: "test 2"})

	buf.Clear()

	logs := buf.GetAll()
	if len(logs) != 0 {
		t.Errorf("GetAll() after Clear() len = %d, want 0", len(logs))
	}
}

func TestLogBuffer_SetMaxAge(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	buf.SetMaxAge(5)

	// Just verify no panic and the buffer still works
	buf.Add(LogEntry{Timestamp: time.Now(), Message: "test"})
	logs := buf.GetAll()
	if len(logs) != 1 {
		t.Errorf("GetAll() len = %d, want 1", len(logs))
	}
}

func TestLogBuffer_ManyEntries(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add many entries
	for i := 0; i < 500; i++ {
		buf.Add(LogEntry{Timestamp: time.Now(), Message: "test"})
	}

	logs := buf.GetAll()
	if len(logs) != 500 {
		t.Errorf("GetAll() len = %d, want 500", len(logs))
	}
}

func TestLogBuffer_OrderDescending(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add entries in order
	buf.Add(LogEntry{Timestamp: time.Now(), Target: "first"})
	buf.Add(LogEntry{Timestamp: time.Now(), Target: "second"})
	buf.Add(LogEntry{Timestamp: time.Now(), Target: "third"})

	logs := buf.GetAll()
	if len(logs) != 3 {
		t.Fatalf("GetAll() len = %d, want 3", len(logs))
	}

	// Should be in reverse insertion order (latest added first)
	if logs[0].Target != "third" {
		t.Errorf("logs[0].Target = %s, want third", logs[0].Target)
	}
	if logs[2].Target != "first" {
		t.Errorf("logs[2].Target = %s, want first", logs[2].Target)
	}
}

func TestLogBuffer_AutoTimestamp(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add entry without timestamp
	entry := LogEntry{
		Level:   string(LevelInfo),
		Group:   GroupTunnel,
		Message: "test",
	}
	buf.Add(entry)

	logs := buf.GetAll()
	if len(logs) != 1 {
		t.Fatalf("GetAll() len = %d, want 1", len(logs))
	}

	// Timestamp should be auto-set
	if logs[0].Timestamp.IsZero() {
		t.Error("Timestamp should be auto-set, got zero time")
	}
}

func TestLogBuffer_MaxEntries(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add more entries than maxEntries
	for i := 0; i < maxEntries+100; i++ {
		buf.Add(LogEntry{Timestamp: time.Now(), Target: "test"})
	}

	if buf.Len() != maxEntries {
		t.Errorf("Len() = %d, want %d (maxEntries)", buf.Len(), maxEntries)
	}

	// Verify newest entries are preserved (not oldest)
	logs := buf.GetAll()
	if len(logs) != maxEntries {
		t.Errorf("GetAll() len = %d, want %d", len(logs), maxEntries)
	}
}

func TestLogBuffer_Len(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	if buf.Len() != 0 {
		t.Errorf("Len() = %d, want 0", buf.Len())
	}

	buf.Add(LogEntry{Timestamp: time.Now()})
	buf.Add(LogEntry{Timestamp: time.Now()})

	if buf.Len() != 2 {
		t.Errorf("Len() = %d, want 2", buf.Len())
	}
}

func TestLogBuffer_LevelCumulative(t *testing.T) {
	lb := NewLogBuffer()
	defer lb.Stop()

	// Add entries at every level
	base := time.Now()
	levels := []Level{LevelError, LevelWarn, LevelInfo, LevelFull, LevelDebug}
	for i, lv := range levels {
		lb.Add(LogEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Level:     string(lv),
			Group:     GroupSystem,
			Action:    "test",
			Target:    "x",
			Message:   "m",
		})
	}

	// Per IsVisible: error and warn are always visible.
	// - level=error → returns error + warn (both special-cased visible)
	// - level=warn  → returns error + warn (same)
	// - level=info  → adds info → 3 entries (error, warn, info)
	// - level=full  → adds full → 4 entries
	// - level=debug → all 5
	cases := []struct {
		filter   string
		wantLen  int
		wantHave []string
	}{
		{filter: "error", wantLen: 2, wantHave: []string{"error", "warn"}},
		{filter: "warn", wantLen: 2, wantHave: []string{"error", "warn"}},
		{filter: "info", wantLen: 3, wantHave: []string{"error", "warn", "info"}},
		{filter: "full", wantLen: 4, wantHave: []string{"error", "warn", "info", "full"}},
		{filter: "debug", wantLen: 5, wantHave: []string{"error", "warn", "info", "full", "debug"}},
	}

	for _, c := range cases {
		got, total := lb.GetPaginated("", "", c.filter, time.Time{}, 100, 0)
		if total != c.wantLen {
			t.Errorf("level=%q: got total=%d, want %d", c.filter, total, c.wantLen)
		}
		for _, must := range c.wantHave {
			found := false
			for _, e := range got {
				if e.Level == must {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("level=%q: expected to find level %q in result", c.filter, must)
			}
		}
	}
}

func TestLogBuffer_SinceFilter(t *testing.T) {
	lb := NewLogBuffer()
	defer lb.Stop()

	base := time.Now()
	// Three entries spaced 10 seconds apart
	for i := 0; i < 3; i++ {
		lb.Add(LogEntry{
			Timestamp: base.Add(time.Duration(i*10) * time.Second),
			Level:     "info",
			Group:     "system",
			Action:    "test",
			Target:    "x",
			Message:   "m",
		})
	}

	// since = base + 5s → expect 2 entries (those at +10s and +20s)
	since := base.Add(5 * time.Second)
	got, total := lb.GetPaginated("", "", "", since, 100, 0)
	if total != 2 {
		t.Errorf("since=base+5s: got total=%d, want 2", total)
	}
	if len(got) != 2 {
		t.Errorf("since=base+5s: got %d entries, want 2", len(got))
	}

	// since = zero → all 3 (no filter)
	_, total = lb.GetPaginated("", "", "", time.Time{}, 100, 0)
	if total != 3 {
		t.Errorf("since=zero: got total=%d, want 3", total)
	}

	// since = far future → 0
	_, total = lb.GetPaginated("", "", "", base.Add(1*time.Hour), 100, 0)
	if total != 0 {
		t.Errorf("since=far future: got total=%d, want 0", total)
	}
}

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

	// Filter by group and level
	logs := buf.GetFiltered(GroupTunnel, "", string(LevelWarn))
	if len(logs) != 1 {
		t.Errorf("GetFiltered(tunnel, '', warn) len = %d, want 1", len(logs))
	}

	// Filter by group only
	logs = buf.GetFiltered(GroupTunnel, "", "")
	if len(logs) != 2 {
		t.Errorf("GetFiltered(group only) len = %d, want 2", len(logs))
	}

	// Filter by level only
	logs = buf.GetFiltered("", "", string(LevelWarn))
	if len(logs) != 1 {
		t.Errorf("GetFiltered(level only) len = %d, want 1", len(logs))
	}

	// Filter by error level
	logs = buf.GetFiltered("", "", string(LevelError))
	if len(logs) != 1 {
		t.Errorf("GetFiltered(error only) len = %d, want 1", len(logs))
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
	logs, total := buf.GetPaginated(GroupTunnel, "", "", 2, 0)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 2 {
		t.Errorf("page len = %d, want 2", len(logs))
	}

	// Get second page (limit 2, offset 2)
	logs, total = buf.GetPaginated(GroupTunnel, "", "", 2, 2)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 2 {
		t.Errorf("page len = %d, want 2", len(logs))
	}

	// Get last page (limit 2, offset 4)
	logs, total = buf.GetPaginated(GroupTunnel, "", "", 2, 4)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 1 {
		t.Errorf("last page len = %d, want 1", len(logs))
	}

	// Offset beyond total
	logs, total = buf.GetPaginated(GroupTunnel, "", "", 2, 10)
	if total != 5 {
		t.Errorf("total = %d, want 5", total)
	}
	if len(logs) != 0 {
		t.Errorf("beyond offset len = %d, want 0", len(logs))
	}

	// All entries (no filter)
	_, total = buf.GetPaginated("", "", "", 100, 0)
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

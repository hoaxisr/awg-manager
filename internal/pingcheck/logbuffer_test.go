package pingcheck

import (
	"testing"
	"time"
)

func TestLogBuffer_Add(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	entry := LogEntry{
		Timestamp:  time.Now(),
		TunnelID:   "test-1",
		TunnelName: "Test Tunnel",
		Success:    true,
		Latency:    50,
	}

	buf.Add(entry)

	logs := buf.GetAll()
	if len(logs) != 1 {
		t.Errorf("GetAll() len = %d, want 1", len(logs))
	}
	if logs[0].TunnelID != "test-1" {
		t.Errorf("TunnelID = %s, want test-1", logs[0].TunnelID)
	}
}

func TestLogBuffer_GetByTunnel(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add entries for two tunnels
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "tunnel-1", TunnelName: "Tunnel 1"})
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "tunnel-2", TunnelName: "Tunnel 2"})
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "tunnel-1", TunnelName: "Tunnel 1"})

	logs := buf.GetByTunnel("tunnel-1")
	if len(logs) != 2 {
		t.Errorf("GetByTunnel() len = %d, want 2", len(logs))
	}

	for _, log := range logs {
		if log.TunnelID != "tunnel-1" {
			t.Errorf("TunnelID = %s, want tunnel-1", log.TunnelID)
		}
	}
}

func TestLogBuffer_Clear(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "test-1"})
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "test-2"})

	buf.Clear()

	logs := buf.GetAll()
	if len(logs) != 0 {
		t.Errorf("GetAll() after Clear() len = %d, want 0", len(logs))
	}
}

func TestLogBuffer_ManyEntries(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add many entries
	for i := 0; i < 500; i++ {
		buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "test"})
	}

	logs := buf.GetAll()
	if len(logs) != 500 {
		t.Errorf("GetAll() len = %d, want 500", len(logs))
	}
}

func TestLogBuffer_MaxEntries(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add more entries than maxEntries
	for i := 0; i < maxEntries+100; i++ {
		buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "test"})
	}

	if buf.Len() != maxEntries {
		t.Errorf("Len() = %d, want %d (maxEntries)", buf.Len(), maxEntries)
	}

	logs := buf.GetAll()
	if len(logs) != maxEntries {
		t.Errorf("GetAll() len = %d, want %d", len(logs), maxEntries)
	}
}

func TestLogBuffer_OrderDescending(t *testing.T) {
	buf := NewLogBuffer()
	defer buf.Stop()

	// Add entries in order
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "first"})
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "second"})
	buf.Add(LogEntry{Timestamp: time.Now(), TunnelID: "third"})

	logs := buf.GetAll()
	if len(logs) != 3 {
		t.Fatalf("GetAll() len = %d, want 3", len(logs))
	}

	// Should be in reverse insertion order (latest added first)
	if logs[0].TunnelID != "third" {
		t.Errorf("logs[0].TunnelID = %s, want third", logs[0].TunnelID)
	}
	if logs[2].TunnelID != "first" {
		t.Errorf("logs[2].TunnelID = %s, want first", logs[2].TunnelID)
	}
}

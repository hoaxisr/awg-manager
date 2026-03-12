package logging

import (
	"testing"
)

// mockSettings implements SettingsGetter for testing.
type mockSettings struct {
	enabled bool
	maxAge  int
}

func (m *mockSettings) IsLoggingEnabled() bool {
	return m.enabled
}

func (m *mockSettings) GetLoggingMaxAge() int {
	return m.maxAge
}

func TestService_IsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		settings *mockSettings
		want     bool
	}{
		{
			name:     "enabled",
			settings: &mockSettings{enabled: true},
			want:     true,
		},
		{
			name:     "disabled",
			settings: &mockSettings{enabled: false},
			want:     false,
		},
		{
			name:     "nil settings",
			settings: nil,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var svc *Service
			if tt.settings != nil {
				svc = NewService(tt.settings)
			} else {
				svc = &Service{buffer: NewLogBuffer()}
			}
			defer svc.Stop()

			if got := svc.IsEnabled(); got != tt.want {
				t.Errorf("IsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestService_LogWhenDisabled(t *testing.T) {
	settings := &mockSettings{enabled: false}
	svc := NewService(settings)
	defer svc.Stop()

	svc.Log(CategoryTunnel, "create", "test", "message")

	if svc.Len() != 0 {
		t.Errorf("Len() = %d, want 0 (logging disabled)", svc.Len())
	}
}

func TestService_LogWhenEnabled(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2}
	svc := NewService(settings)
	defer svc.Stop()

	svc.Log(CategoryTunnel, "create", "test-tunnel", "Tunnel created")

	if svc.Len() != 1 {
		t.Errorf("Len() = %d, want 1", svc.Len())
	}

	logs := svc.GetLogs("", "")
	if len(logs) != 1 {
		t.Fatalf("GetLogs() len = %d, want 1", len(logs))
	}

	entry := logs[0]
	if entry.Level != LevelInfo {
		t.Errorf("Level = %s, want %s", entry.Level, LevelInfo)
	}
	if entry.Category != CategoryTunnel {
		t.Errorf("Category = %s, want %s", entry.Category, CategoryTunnel)
	}
	if entry.Action != "create" {
		t.Errorf("Action = %s, want create", entry.Action)
	}
	if entry.Target != "test-tunnel" {
		t.Errorf("Target = %s, want test-tunnel", entry.Target)
	}
}

func TestService_LogError(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2}
	svc := NewService(settings)
	defer svc.Stop()

	svc.LogError(CategorySystem, "exec", "ndmc", "Command failed", "exit code 1")

	logs := svc.GetLogs("", "")
	if len(logs) != 1 {
		t.Fatalf("GetLogs() len = %d, want 1", len(logs))
	}

	entry := logs[0]
	if entry.Level != LevelError {
		t.Errorf("Level = %s, want %s", entry.Level, LevelError)
	}
	if entry.Error != "exit code 1" {
		t.Errorf("Error = %s, want 'exit code 1'", entry.Error)
	}
}

func TestService_LogWarn(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2}
	svc := NewService(settings)
	defer svc.Stop()

	svc.LogWarn(CategoryTunnel, "start", "awg0", "Tunnel already running")

	logs := svc.GetLogs("", "")
	if len(logs) != 1 {
		t.Fatalf("GetLogs() len = %d, want 1", len(logs))
	}

	if logs[0].Level != LevelWarn {
		t.Errorf("Level = %s, want %s", logs[0].Level, LevelWarn)
	}
}

func TestService_GetLogsFiltered(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2}
	svc := NewService(settings)
	defer svc.Stop()

	svc.Log(CategoryTunnel, "create", "t1", "msg1")
	svc.LogError(CategoryTunnel, "start", "t2", "msg2", "err")
	svc.Log(CategorySettings, "update", "", "msg3")

	// Filter by category
	logs := svc.GetLogs(CategoryTunnel, "")
	if len(logs) != 2 {
		t.Errorf("GetLogs(tunnel) len = %d, want 2", len(logs))
	}

	// Filter by level
	logs = svc.GetLogs("", LevelError)
	if len(logs) != 1 {
		t.Errorf("GetLogs(error) len = %d, want 1", len(logs))
	}

	// Filter by both
	logs = svc.GetLogs(CategoryTunnel, LevelInfo)
	if len(logs) != 1 {
		t.Errorf("GetLogs(tunnel, info) len = %d, want 1", len(logs))
	}
}

func TestService_Clear(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2}
	svc := NewService(settings)
	defer svc.Stop()

	svc.Log(CategoryTunnel, "create", "t1", "msg1")
	svc.Log(CategoryTunnel, "create", "t2", "msg2")

	svc.Clear()

	if svc.Len() != 0 {
		t.Errorf("Len() after Clear() = %d, want 0", svc.Len())
	}
}

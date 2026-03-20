package logging

import (
	"testing"
)

// mockSettings implements SettingsGetter for testing.
type mockSettings struct {
	enabled  bool
	maxAge   int
	logLevel string
}

func (m *mockSettings) IsLoggingEnabled() bool {
	return m.enabled
}

func (m *mockSettings) GetLoggingMaxAge() int {
	return m.maxAge
}

func (m *mockSettings) GetLogLevel() string {
	if m.logLevel == "" {
		return "info"
	}
	return m.logLevel
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

func TestService_AppLogWhenDisabled(t *testing.T) {
	settings := &mockSettings{enabled: false}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "test", "message")

	if svc.Len() != 0 {
		t.Errorf("Len() = %d, want 0 (logging disabled)", svc.Len())
	}
}

func TestService_AppLogWhenEnabled(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "test-tunnel", "Tunnel created")

	if svc.Len() != 1 {
		t.Errorf("Len() = %d, want 1", svc.Len())
	}

	logs, _ := svc.GetLogs("", "", "", 200, 0)
	if len(logs) != 1 {
		t.Fatalf("GetLogs() len = %d, want 1", len(logs))
	}

	entry := logs[0]
	if entry.Level != string(LevelInfo) {
		t.Errorf("Level = %s, want %s", entry.Level, LevelInfo)
	}
	if entry.Group != GroupTunnel {
		t.Errorf("Group = %s, want %s", entry.Group, GroupTunnel)
	}
	if entry.Subgroup != SubLifecycle {
		t.Errorf("Subgroup = %s, want %s", entry.Subgroup, SubLifecycle)
	}
	if entry.Action != "create" {
		t.Errorf("Action = %s, want create", entry.Action)
	}
	if entry.Target != "test-tunnel" {
		t.Errorf("Target = %s, want test-tunnel", entry.Target)
	}
}

func TestService_AppLogWarn(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelWarn, GroupTunnel, SubLifecycle, "start", "awg0", "Tunnel already running")

	logs, _ := svc.GetLogs("", "", "", 200, 0)
	if len(logs) != 1 {
		t.Fatalf("GetLogs() len = %d, want 1", len(logs))
	}

	if logs[0].Level != string(LevelWarn) {
		t.Errorf("Level = %s, want %s", logs[0].Level, LevelWarn)
	}
}

func TestService_AppLogError(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	// Error should always be visible regardless of configured level
	svc.AppLog(LevelError, GroupTunnel, SubLifecycle, "start", "awg0", "Critical failure")

	if svc.Len() != 1 {
		t.Errorf("Len() = %d, want 1 (error always visible)", svc.Len())
	}

	logs, total := svc.GetLogs("", "", "", 200, 0)
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if logs[0].Level != string(LevelError) {
		t.Errorf("Level = %s, want %s", logs[0].Level, LevelError)
	}
}

func TestService_GetLogsPagination(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	for i := 0; i < 10; i++ {
		svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t", "msg")
	}

	// First page
	logs, total := svc.GetLogs("", "", "", 3, 0)
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if len(logs) != 3 {
		t.Errorf("page len = %d, want 3", len(logs))
	}

	// Last page
	logs, total = svc.GetLogs("", "", "", 3, 9)
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if len(logs) != 1 {
		t.Errorf("last page len = %d, want 1", len(logs))
	}

	// Default limit (0 → 200)
	logs, total = svc.GetLogs("", "", "", 0, 0)
	if total != 10 {
		t.Errorf("total (default limit) = %d, want 10", total)
	}
	if len(logs) != 10 {
		t.Errorf("logs (default limit) = %d, want 10", len(logs))
	}
}

func TestService_LevelFiltering(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	// Info should pass at info level
	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "msg1")
	// Warn should always pass
	svc.AppLog(LevelWarn, GroupTunnel, SubLifecycle, "start", "t2", "msg2")
	// Full should NOT pass at info level
	svc.AppLog(LevelFull, GroupTunnel, SubOps, "setup", "t3", "msg3")
	// Debug should NOT pass at info level
	svc.AppLog(LevelDebug, GroupTunnel, SubOps, "trace", "t4", "msg4")

	if svc.Len() != 2 {
		t.Errorf("Len() = %d, want 2 (only info+warn at info level)", svc.Len())
	}
}

func TestService_LevelFull(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "full"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "msg1")
	svc.AppLog(LevelWarn, GroupTunnel, SubLifecycle, "start", "t2", "msg2")
	svc.AppLog(LevelFull, GroupTunnel, SubOps, "setup", "t3", "msg3")
	svc.AppLog(LevelDebug, GroupTunnel, SubOps, "trace", "t4", "msg4")

	if svc.Len() != 3 {
		t.Errorf("Len() = %d, want 3 (info+warn+full at full level)", svc.Len())
	}
}

func TestService_LevelDebug(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "debug"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "msg1")
	svc.AppLog(LevelWarn, GroupTunnel, SubLifecycle, "start", "t2", "msg2")
	svc.AppLog(LevelFull, GroupTunnel, SubOps, "setup", "t3", "msg3")
	svc.AppLog(LevelDebug, GroupTunnel, SubOps, "trace", "t4", "msg4")

	if svc.Len() != 4 {
		t.Errorf("Len() = %d, want 4 (all levels at debug)", svc.Len())
	}
}

func TestService_GetLogsFiltered(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "debug"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "msg1")
	svc.AppLog(LevelWarn, GroupTunnel, SubOps, "start", "t2", "msg2")
	svc.AppLog(LevelInfo, GroupSystem, SubSettings, "update", "", "msg3")

	// Filter by group
	logs, _ := svc.GetLogs(GroupTunnel, "", "", 200, 0)
	if len(logs) != 2 {
		t.Errorf("GetLogs(tunnel) len = %d, want 2", len(logs))
	}

	// Filter by level
	logs, _ = svc.GetLogs("", "", string(LevelWarn), 200, 0)
	if len(logs) != 1 {
		t.Errorf("GetLogs(warn) len = %d, want 1", len(logs))
	}

	// Filter by subgroup
	logs, _ = svc.GetLogs("", SubLifecycle, "", 200, 0)
	if len(logs) != 1 {
		t.Errorf("GetLogs(lifecycle) len = %d, want 1", len(logs))
	}

	// Filter by group + level
	logs, _ = svc.GetLogs(GroupTunnel, "", string(LevelInfo), 200, 0)
	if len(logs) != 1 {
		t.Errorf("GetLogs(tunnel, info) len = %d, want 1", len(logs))
	}
}

func TestService_Clear(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "msg1")
	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t2", "msg2")

	svc.Clear()

	if svc.Len() != 0 {
		t.Errorf("Len() after Clear() = %d, want 0", svc.Len())
	}
}

func TestService_AppLoggerInterface(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	// Verify Service implements AppLogger
	var logger AppLogger = svc
	logger.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "test", "msg")

	if svc.Len() != 1 {
		t.Errorf("Len() = %d, want 1", svc.Len())
	}
}

func TestScopedLogger(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "debug"}
	svc := NewService(settings)
	defer svc.Stop()

	sl := NewScopedLogger(svc, GroupTunnel, SubLifecycle)
	sl.Info("create", "t1", "created")
	sl.Warn("start", "t1", "warning")
	sl.Error("fail", "t1", "critical error")
	sl.Full("setup", "t1", "setting up")
	sl.Debug("trace", "t1", "details")

	if svc.Len() != 5 {
		t.Errorf("Len() = %d, want 5", svc.Len())
	}

	logs, _ := svc.GetLogs("", "", "", 200, 0)
	// All should have GroupTunnel and SubLifecycle
	for _, entry := range logs {
		if entry.Group != GroupTunnel {
			t.Errorf("Group = %s, want %s", entry.Group, GroupTunnel)
		}
		if entry.Subgroup != SubLifecycle {
			t.Errorf("Subgroup = %s, want %s", entry.Subgroup, SubLifecycle)
		}
	}
}

func TestScopedLogger_NilSafe(t *testing.T) {
	// nil ScopedLogger should not panic
	var sl *ScopedLogger
	sl.Info("create", "t1", "msg")
	sl.Warn("start", "t1", "msg")
	sl.Error("fail", "t1", "msg")
	sl.Full("setup", "t1", "msg")
	sl.Debug("trace", "t1", "msg")

	// ScopedLogger with nil appLogger should not panic
	sl2 := NewScopedLogger(nil, GroupTunnel, SubLifecycle)
	sl2.Info("create", "t1", "msg")
	sl2.Warn("start", "t1", "msg")
	sl2.Error("fail", "t1", "msg")
	sl2.Full("setup", "t1", "msg")
	sl2.Debug("trace", "t1", "msg")
}

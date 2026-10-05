package logging

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// mockSettings implements SettingsGetter for testing.
type mockSettings struct {
	enabled       bool
	maxAge        int
	logLevel      string
	appMaxEntries int
	sbMaxEntries  int
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

func (m *mockSettings) GetAppMaxEntries() int     { return m.appMaxEntries }
func (m *mockSettings) GetSingboxMaxEntries() int { return m.sbMaxEntries }

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
				svc = &Service{
					appBuffer:     NewLogBuffer(BucketApp),
					singboxBuffer: NewLogBuffer(BucketSingbox),
					mihomoBuffer:  NewLogBuffer(BucketMihomo),
				}
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

	logs, _ := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 200, 0)
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

	logs, _ := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 200, 0)
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

	logs, total := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 200, 0)
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
		svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t", fmt.Sprintf("msg %d", i))
	}

	// First page
	logs, total := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 3, 0)
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if len(logs) != 3 {
		t.Errorf("page len = %d, want 3", len(logs))
	}

	// Last page
	logs, total = svc.GetLogs(BucketApp, "", "", "", time.Time{}, 3, 9)
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if len(logs) != 1 {
		t.Errorf("last page len = %d, want 1", len(logs))
	}

	// Default limit (0 → 200)
	logs, total = svc.GetLogs(BucketApp, "", "", "", time.Time{}, 0, 0)
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
	logs, _ := svc.GetLogs(BucketApp, GroupTunnel, "", "", time.Time{}, 200, 0)
	if len(logs) != 2 {
		t.Errorf("GetLogs(tunnel) len = %d, want 2", len(logs))
	}

	// Filter by level
	logs, _ = svc.GetLogs(BucketApp, "", "", string(LevelWarn), time.Time{}, 200, 0)
	if len(logs) != 1 {
		t.Errorf("GetLogs(warn) len = %d, want 1", len(logs))
	}

	// Filter by subgroup
	logs, _ = svc.GetLogs(BucketApp, "", SubLifecycle, "", time.Time{}, 200, 0)
	if len(logs) != 1 {
		t.Errorf("GetLogs(lifecycle) len = %d, want 1", len(logs))
	}

	// Filter by group + level (info): tunnel has info+warn; warn is always
	// visible AND info <= info → both match → 2.
	logs, _ = svc.GetLogs(BucketApp, GroupTunnel, "", string(LevelInfo), time.Time{}, 200, 0)
	if len(logs) != 2 {
		t.Errorf("GetLogs(tunnel, info) len = %d, want 2", len(logs))
	}
}

func TestService_Clear(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "msg1")
	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t2", "msg2")

	svc.Clear(BucketApp)

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

// Sing-box bucket isolation: an entry with GroupSingbox routes to the
// singbox buffer and does NOT appear in the app buffer.
func TestService_SingboxRoutes(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "create", "t1", "app entry")
	svc.AppLog(LevelInfo, GroupSingbox, SubSBOutbound, "run", "veesp", "outbound")
	svc.AppLog(LevelInfo, GroupSingbox, SubSBInbound, "run", "tun", "inbound")

	appLogs, appTotal := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 200, 0)
	if appTotal != 1 {
		t.Errorf("app bucket total = %d, want 1", appTotal)
	}
	if len(appLogs) != 1 || appLogs[0].Group != GroupTunnel {
		t.Errorf("app bucket should contain only tunnel entry, got %+v", appLogs)
	}

	sbLogs, sbTotal := svc.GetLogs(BucketSingbox, "", "", "", time.Time{}, 200, 0)
	if sbTotal != 2 {
		t.Errorf("singbox bucket total = %d, want 2", sbTotal)
	}
	for _, e := range sbLogs {
		if e.Group != GroupSingbox {
			t.Errorf("singbox bucket has non-singbox entry: %+v", e)
		}
	}
}

func TestService_ClearOnlyOneBucket(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info"}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "x", "t", "app")
	svc.AppLog(LevelInfo, GroupSingbox, SubSBOutbound, "x", "t", "sb")

	svc.Clear(BucketApp)

	if _, total := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 200, 0); total != 0 {
		t.Errorf("app bucket should be empty after Clear(app), total=%d", total)
	}
	if _, total := svc.GetLogs(BucketSingbox, "", "", "", time.Time{}, 200, 0); total != 1 {
		t.Errorf("singbox bucket must be untouched, total=%d, want 1", total)
	}
}

func TestService_StatsReportsBucketState(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info", appMaxEntries: 100, sbMaxEntries: 200}
	svc := NewService(settings)
	defer svc.Stop()

	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "x", "t", "msg 1")
	svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "x", "t", "msg 2")

	stats := svc.Stats(BucketApp)
	if stats.Bucket != BucketApp {
		t.Errorf("Bucket = %s, want %s", stats.Bucket, BucketApp)
	}
	if stats.Size != 2 {
		t.Errorf("Size = %d, want 2", stats.Size)
	}
	if stats.Capacity != 100 {
		t.Errorf("Capacity = %d, want 100", stats.Capacity)
	}
	if stats.Oldest.IsZero() {
		t.Error("Oldest should be non-zero with entries present")
	}

	emptyStats := svc.Stats(BucketSingbox)
	if emptyStats.Capacity != 200 {
		t.Errorf("Singbox capacity = %d, want 200", emptyStats.Capacity)
	}
	if !emptyStats.Oldest.IsZero() {
		t.Error("Oldest should be zero on empty bucket")
	}
}

// ApplySettings re-reads the store and applies new caps to live buffers.
func TestService_ApplySettingsRetargetsCaps(t *testing.T) {
	settings := &mockSettings{enabled: true, maxAge: 2, logLevel: "info", appMaxEntries: 1000, sbMaxEntries: 1000}
	svc := NewService(settings)
	defer svc.Stop()

	for i := 0; i < 50; i++ {
		svc.AppLog(LevelInfo, GroupTunnel, SubLifecycle, "x", "t", fmt.Sprintf("msg %d", i))
	}

	// Shrink to 10.
	settings.appMaxEntries = 10
	svc.ApplySettings()

	if got := svc.Stats(BucketApp).Size; got != 10 {
		t.Errorf("after shrink, Size = %d, want 10", got)
	}
	if got := svc.Stats(BucketApp).Capacity; got != 10 {
		t.Errorf("after shrink, Capacity = %d, want 10", got)
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

	logs, _ := svc.GetLogs(BucketApp, "", "", "", time.Time{}, 200, 0)
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

// BucketForGroup isolates each proxy engine and routes everything else to app.
func TestBucketForGroup(t *testing.T) {
	if BucketForGroup(GroupSingbox) != BucketSingbox {
		t.Errorf("singbox should map to BucketSingbox")
	}
	if BucketForGroup(GroupMihomo) != BucketMihomo {
		t.Errorf("mihomo should map to BucketMihomo")
	}
	for _, g := range []string{GroupTunnel, GroupRouting, GroupServer, GroupSystem, ""} {
		if BucketForGroup(g) != BucketApp {
			t.Errorf("group %q should map to BucketApp", g)
		}
	}
}

// TestService_AppLogKeepsNoURLSecret — очистка в самом журнале, а не в
// каждом месте записи: обработчик REST подписок писал сырую ошибку мимо
// хелперов сервиса, и следующий новый Warn с err.Error() утёк бы так же.
// Журнал читают веб-интерфейс, /api/logs и get_logs ключа MCP только для
// чтения; маскирование при показе прячет хост, но не путь и не query.
func TestService_AppLogKeepsNoURLSecret(t *testing.T) {
	svc := NewService(&mockSettings{enabled: true, maxAge: 2, logLevel: "info"})
	defer svc.Stop()

	svc.AppLog(LevelWarn, GroupRouting, SubSubscription, "subscription-refresh",
		"https://sub.example.com/sub/TargetTok", `Get "https://cdn.example.net/r/Hop5ecret?key=K3y": EOF; parse "vless://3a3b1c2e-9999@h1.example:443": bad`)
	svc.AppLog(LevelWarn, GroupSingbox, SubSBRuntime, "x", "", "trojan://%21Passw0rd@h.example:443")

	var all string
	for _, b := range []Bucket{BucketApp, BucketSingbox} {
		logs, _ := svc.GetLogs(b, "", "", "", time.Time{}, 200, 0)
		for _, e := range logs {
			all += e.Target + " " + e.Message + "\n"
		}
	}
	for _, secret := range []string{"TargetTok", "Hop5ecret", "K3y", "3a3b1c2e", "Passw0rd"} {
		if strings.Contains(all, secret) {
			t.Fatalf("%q reached the journal: %q", secret, all)
		}
	}
	if !strings.Contains(all, "cdn.example.net") {
		t.Fatalf("the host must stay for diagnosis: %q", all)
	}
}

package dnsroute

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// mockRefreshService counts RefreshAllSubscriptions calls.
type mockRefreshService struct {
	refreshCount int32 // atomic
}

func (m *mockRefreshService) Create(ctx context.Context, list DomainList) (*DomainList, error) {
	return nil, nil
}
func (m *mockRefreshService) Get(ctx context.Context, id string) (*DomainList, error) {
	return nil, nil
}
func (m *mockRefreshService) List(ctx context.Context) ([]DomainList, error) { return nil, nil }
func (m *mockRefreshService) Update(ctx context.Context, list DomainList) (*DomainList, error) {
	return nil, nil
}
func (m *mockRefreshService) Delete(ctx context.Context, id string) error            { return nil }
func (m *mockRefreshService) DeleteBatch(ctx context.Context, ids []string) (int, error) {
	return 0, nil
}
func (m *mockRefreshService) CreateBatch(ctx context.Context, lists []DomainList) ([]*DomainList, error) {
	return nil, nil
}
func (m *mockRefreshService) SetEnabled(ctx context.Context, id string, e bool) error { return nil }
func (m *mockRefreshService) RefreshSubscriptions(ctx context.Context, id string) error {
	return nil
}
func (m *mockRefreshService) RefreshAllSubscriptions(ctx context.Context) error {
	atomic.AddInt32(&m.refreshCount, 1)
	return nil
}
func (m *mockRefreshService) Reconcile(ctx context.Context) error { return nil }

func (m *mockRefreshService) count() int {
	return int(atomic.LoadInt32(&m.refreshCount))
}

func newTestSettings(t *testing.T, ds storage.DNSRouteSettings) *storage.SettingsStore {
	t.Helper()
	dir := t.TempDir()
	store := storage.NewSettingsStore(dir)
	settings, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	settings.DNSRoute = ds
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestScheduler_DisabledDoesNotRefresh(t *testing.T) {
	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled:   false,
		RefreshIntervalHours: 1,
	})

	sched := &Scheduler{settings: store}
	if sched.shouldRefresh() {
		t.Error("expected no refresh when disabled")
	}
}

func TestScheduler_IntervalMode_Refreshes(t *testing.T) {
	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled:   true,
		RefreshIntervalHours: 6,
	})

	sched := &Scheduler{settings: store}
	// First call with zero lastRefresh should trigger refresh.
	if !sched.shouldRefresh() {
		t.Error("expected refresh on first call (lastRefresh is zero)")
	}

	// After refresh, within interval should not trigger.
	sched.lastRefresh = time.Now()
	if sched.shouldRefresh() {
		t.Error("expected no refresh within interval")
	}

	// After interval elapsed.
	sched.lastRefresh = time.Now().Add(-7 * time.Hour)
	if !sched.shouldRefresh() {
		t.Error("expected refresh after interval elapsed")
	}
}

func TestScheduler_IntervalMode_ZeroHoursDisabled(t *testing.T) {
	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled:   true,
		RefreshIntervalHours: 0,
	})

	sched := &Scheduler{settings: store}
	if sched.shouldRefresh() {
		t.Error("expected no refresh when hours=0")
	}
}

func TestScheduler_DailyMode_InWindow(t *testing.T) {
	now := time.Now()
	targetTime := now.Format("15:04")

	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled: true,
		RefreshMode:        "daily",
		RefreshDailyTime:   targetTime,
	})

	sched := &Scheduler{settings: store}
	if !sched.shouldRefresh() {
		t.Error("expected refresh when current time matches daily target")
	}
}

func TestScheduler_DailyMode_OutsideWindow(t *testing.T) {
	// Set target to 20 minutes ago — outside the 15-minute window.
	target := time.Now().Add(-20 * time.Minute)
	targetTime := target.Format("15:04")

	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled: true,
		RefreshMode:        "daily",
		RefreshDailyTime:   targetTime,
	})

	sched := &Scheduler{settings: store}
	if sched.shouldRefresh() {
		t.Error("expected no refresh outside daily window")
	}
}

func TestScheduler_DailyMode_NoDoubleFire(t *testing.T) {
	now := time.Now()
	targetTime := now.Format("15:04")

	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled: true,
		RefreshMode:        "daily",
		RefreshDailyTime:   targetTime,
	})

	sched := &Scheduler{settings: store}
	// Simulate already refreshed in this window.
	sched.lastRefresh = now
	if sched.shouldRefresh() {
		t.Error("expected no double refresh in same daily window")
	}
}

func TestScheduler_DailyMode_EmptyTimeDisabled(t *testing.T) {
	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled: true,
		RefreshMode:        "daily",
		RefreshDailyTime:   "",
	})

	sched := &Scheduler{settings: store}
	if sched.shouldRefresh() {
		t.Error("expected no refresh when daily time is empty")
	}
}

func TestScheduler_KickIsNonBlocking(t *testing.T) {
	sched := &Scheduler{
		kick: make(chan struct{}, 1),
	}
	// Should not block even when called multiple times.
	sched.Kick()
	sched.Kick()
	sched.Kick()

	// Channel should have exactly 1 item.
	select {
	case <-sched.kick:
	default:
		t.Error("expected kick in channel")
	}
	select {
	case <-sched.kick:
		t.Error("expected only 1 kick in channel")
	default:
	}
}

func TestScheduler_StartStop(t *testing.T) {
	mock := &mockRefreshService{}
	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled:   false,
		RefreshIntervalHours: 0,
	})

	sched := NewScheduler(mock, store, noopLogger())
	sched.Start()

	// Stop should return promptly (scheduler is in initial delay).
	done := make(chan struct{})
	go func() {
		sched.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not return within 3 seconds")
	}
}

func TestScheduler_DoRefreshCallsService(t *testing.T) {
	mock := &mockRefreshService{}
	store := newTestSettings(t, storage.DNSRouteSettings{
		AutoRefreshEnabled:   true,
		RefreshIntervalHours: 1,
	})

	sched := NewScheduler(mock, store, noopLogger())
	sched.doRefresh()

	if mock.count() != 1 {
		t.Errorf("expected 1 refresh call, got %d", mock.count())
	}
}

package dnsroute

import (
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

func TestFailoverManager_IsFailed_EmptyByDefault(t *testing.T) {
	fm := NewFailoverManager(nil)
	if fm.IsFailed("tunnel1") {
		t.Error("expected not failed by default")
	}
}

func TestFailoverManager_MarkFailed(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.MarkFailed("tunnel1")

	if !fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 to be failed")
	}
	if fm.IsFailed("tunnel2") {
		t.Error("expected tunnel2 to be not failed")
	}
}

func TestFailoverManager_MarkRecovered(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.MarkFailed("tunnel1")
	fm.MarkRecovered("tunnel1")

	if fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 to be recovered")
	}
}

func TestFailoverManager_MarkRecovered_Noop(t *testing.T) {
	fm := NewFailoverManager(nil)
	// Should not panic
	fm.MarkRecovered("nonexistent")
}

func TestFailoverManager_MultipleFailed(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.MarkFailed("tunnel1")
	fm.MarkFailed("tunnel2")

	if !fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 failed")
	}
	if !fm.IsFailed("tunnel2") {
		t.Error("expected tunnel2 failed")
	}

	fm.MarkRecovered("tunnel1")
	if fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 recovered")
	}
	if !fm.IsFailed("tunnel2") {
		t.Error("expected tunnel2 still failed")
	}
}

func TestFailoverManager_FailedTunnels(t *testing.T) {
	fm := NewFailoverManager(nil)
	fm.MarkFailed("tunnel1")
	fm.MarkFailed("tunnel2")

	failed := fm.FailedTunnels()
	if len(failed) != 2 {
		t.Fatalf("expected 2 failed, got %d", len(failed))
	}
}

func TestFailoverManager_ReconcileCalled(t *testing.T) {
	called := 0
	fm := NewFailoverManager(func() error { called++; return nil })
	fm.MarkFailed("tunnel1")

	if called != 1 {
		t.Errorf("expected reconcile called once, got %d", called)
	}

	fm.MarkRecovered("tunnel1")
	if called != 2 {
		t.Errorf("expected reconcile called twice, got %d", called)
	}
}

func TestFailoverManager_ReconcileNotCalledOnDuplicate(t *testing.T) {
	called := 0
	fm := NewFailoverManager(func() error { called++; return nil })
	fm.MarkFailed("tunnel1")
	fm.MarkFailed("tunnel1") // duplicate

	if called != 1 {
		t.Errorf("expected reconcile called once (no duplicate), got %d", called)
	}
}

func TestFailoverManager_ReconcileNotCalledOnRecoverNonFailed(t *testing.T) {
	called := 0
	fm := NewFailoverManager(func() error { called++; return nil })
	fm.MarkRecovered("tunnel1") // not failed

	if called != 0 {
		t.Errorf("expected reconcile not called, got %d", called)
	}
}

func TestFailoverManager_ListenPingCheckEvents(t *testing.T) {
	bus := events.NewBus()
	reconcileCalls := 0
	fm := NewFailoverManager(func() error { reconcileCalls++; return nil })
	fm.StartListener(bus)
	defer fm.StopListener()

	// Publish fail event (real failure has failCount > 0)
	bus.Publish("pingcheck:state", events.PingCheckStateEvent{
		TunnelID:  "tun1",
		Status:    "fail",
		FailCount: 3,
	})

	// Give listener goroutine time to process
	time.Sleep(50 * time.Millisecond)

	if !fm.IsFailed("tun1") {
		t.Error("expected tun1 to be failed after fail event")
	}
	if reconcileCalls != 1 {
		t.Errorf("expected 1 reconcile call, got %d", reconcileCalls)
	}

	// Publish pass event (recovery)
	bus.Publish("pingcheck:state", events.PingCheckStateEvent{
		TunnelID: "tun1",
		Status:   "pass",
	})

	time.Sleep(50 * time.Millisecond)

	if fm.IsFailed("tun1") {
		t.Error("expected tun1 to be recovered after pass event")
	}
	if reconcileCalls != 2 {
		t.Errorf("expected 2 reconcile calls, got %d", reconcileCalls)
	}
}

func TestFailoverManager_IgnoresNonFailPassEvents(t *testing.T) {
	bus := events.NewBus()
	reconcileCalls := 0
	fm := NewFailoverManager(func() error { reconcileCalls++; return nil })
	fm.StartListener(bus)
	defer fm.StopListener()

	// Publish a "check" status event (not fail/pass)
	bus.Publish("pingcheck:state", events.PingCheckStateEvent{
		TunnelID: "tun1",
		Status:   "check",
	})

	time.Sleep(50 * time.Millisecond)

	if fm.IsFailed("tun1") {
		t.Error("should not be failed from 'check' event")
	}
	if reconcileCalls != 0 {
		t.Errorf("expected no reconcile calls, got %d", reconcileCalls)
	}
}

func TestFailoverManager_RollbackOnReconcileError(t *testing.T) {
	fm := NewFailoverManager(func() error {
		return errors.New("reconcile failed")
	})
	fm.MarkFailed("tunnel1")

	if fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 NOT in failedSet after reconcile error (rollback)")
	}
}

func TestFailoverManager_RollbackRecoveredOnReconcileError(t *testing.T) {
	failNext := false
	fm := NewFailoverManager(func() error {
		if failNext {
			return errors.New("reconcile failed")
		}
		return nil
	})
	fm.MarkFailed("tunnel1")
	if !fm.IsFailed("tunnel1") {
		t.Fatal("setup: expected tunnel1 in failedSet")
	}

	failNext = true
	fm.MarkRecovered("tunnel1")

	if !fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 still in failedSet after recovery reconcile error (rollback)")
	}
}

func TestFailoverManager_DirtyFlagRetryOnNextEvent(t *testing.T) {
	calls := 0
	failNext := true
	fm := NewFailoverManager(func() error {
		calls++
		if failNext {
			return errors.New("reconcile failed")
		}
		return nil
	})

	fm.MarkFailed("tunnel1")
	if calls != 1 {
		t.Errorf("expected 1 reconcile call, got %d", calls)
	}
	if fm.IsFailed("tunnel1") {
		t.Error("expected rollback")
	}

	failNext = false
	fm.MarkFailed("tunnel1")
	if calls != 2 {
		t.Errorf("expected 2 reconcile calls (retry triggered), got %d", calls)
	}
	if !fm.IsFailed("tunnel1") {
		t.Error("expected tunnel1 in failedSet after successful retry")
	}
}

func TestFailoverManager_DirtyFlagRetryOnDifferentTunnel(t *testing.T) {
	calls := 0
	failNext := true
	fm := NewFailoverManager(func() error {
		calls++
		if failNext {
			return errors.New("reconcile failed")
		}
		return nil
	})

	fm.MarkFailed("tunnel1")
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}

	failNext = false
	fm.MarkFailed("tunnel2")
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
	if fm.IsFailed("tunnel1") {
		t.Error("tunnel1 should not be in failedSet (rolled back)")
	}
	if !fm.IsFailed("tunnel2") {
		t.Error("tunnel2 should be in failedSet")
	}
}

type captureLogger struct {
	warnings []string
}

func (c *captureLogger) Warnf(format string, args ...interface{}) {
	c.warnings = append(c.warnings, format)
}

func TestFailoverManager_HandlePingCheckEvent_LogsTypeMismatch(t *testing.T) {
	fm := NewFailoverManager(nil)
	logger := &captureLogger{}
	fm.SetLogger(logger)

	// Pass an unexpected type
	fm.handlePingCheckEvent("not a PingCheckStateEvent")

	if len(logger.warnings) != 1 {
		t.Errorf("expected 1 warning, got %d: %v", len(logger.warnings), logger.warnings)
	}
}

func TestFailoverManager_HandlePingCheckEvent_NoLogOnValidType(t *testing.T) {
	fm := NewFailoverManager(nil)
	logger := &captureLogger{}
	fm.SetLogger(logger)

	fm.handlePingCheckEvent(events.PingCheckStateEvent{
		TunnelID: "tun1",
		Status:   "fail",
	})

	if len(logger.warnings) != 0 {
		t.Errorf("expected 0 warnings, got %d: %v", len(logger.warnings), logger.warnings)
	}
}

func TestFailoverManager_PublishOnlyForAffectedLists(t *testing.T) {
	bus := events.NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	fm := NewFailoverManager(func() error { return nil })
	fm.SetEventBus(bus)
	fm.SetAffectedListsLookup(func(tunnelID, action string) []AffectedList {
		if tunnelID == "tun-affected" {
			return []AffectedList{
				{ListID: "list1", ListName: "Telegram", FromTunnel: "Wireguard0", ToTunnel: "Wireguard1"},
			}
		}
		return nil
	})

	// Mark unaffected tunnel — should NOT publish
	_ = fm.MarkFailed("tun-unaffected")

	// Mark affected tunnel — should publish
	_ = fm.MarkFailed("tun-affected")

	// Drain events with timeout
	var collected []events.Event
	timeout := time.After(100 * time.Millisecond)
	done := false
	for !done {
		select {
		case ev := <-ch:
			if ev.Type == "dnsroute:failover" {
				collected = append(collected, ev)
			}
		case <-timeout:
			done = true
		}
	}

	if len(collected) != 1 {
		t.Fatalf("expected 1 dnsroute:failover event, got %d", len(collected))
	}

	payload, ok := collected[0].Data.(events.DNSRouteFailoverEvent)
	if !ok {
		t.Fatalf("expected DNSRouteFailoverEvent, got %T", collected[0].Data)
	}
	if payload.ListName != "Telegram" {
		t.Errorf("expected ListName=Telegram, got %s", payload.ListName)
	}
	if payload.FromTunnel != "Wireguard0" {
		t.Errorf("expected FromTunnel=Wireguard0, got %s", payload.FromTunnel)
	}
	if payload.ToTunnel != "Wireguard1" {
		t.Errorf("expected ToTunnel=Wireguard1, got %s", payload.ToTunnel)
	}
	if payload.Action != "switched" {
		t.Errorf("expected Action=switched, got %s", payload.Action)
	}
}

func TestFailoverManager_PublishesErrorOnReconcileFailure(t *testing.T) {
	bus := events.NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	fm := NewFailoverManager(func() error { return errors.New("rci timeout") })
	fm.SetEventBus(bus)
	fm.SetAffectedListsLookup(func(tunnelID, action string) []AffectedList {
		return []AffectedList{
			{ListID: "list1", ListName: "Telegram", FromTunnel: "Wireguard0", ToTunnel: "Wireguard1"},
		}
	})

	_ = fm.MarkFailed("tun1")

	var collected []events.Event
	timeout := time.After(100 * time.Millisecond)
	done := false
	for !done {
		select {
		case ev := <-ch:
			if ev.Type == "dnsroute:failover" {
				collected = append(collected, ev)
			}
		case <-timeout:
			done = true
		}
	}

	if len(collected) != 1 {
		t.Fatalf("expected 1 event, got %d", len(collected))
	}
	payload := collected[0].Data.(events.DNSRouteFailoverEvent)
	if payload.Action != "error" {
		t.Errorf("expected Action=error, got %s", payload.Action)
	}
	if payload.Error == "" {
		t.Error("expected non-empty Error field")
	}
}

func TestFailoverManager_NoPublishOnNoChange(t *testing.T) {
	bus := events.NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	fm := NewFailoverManager(func() error { return nil })
	fm.SetEventBus(bus)
	fm.SetAffectedListsLookup(func(tunnelID, action string) []AffectedList {
		return []AffectedList{{ListID: "l1", ListName: "X"}}
	})

	_ = fm.MarkFailed("tun1")
	// Second call is no-op (already failed, not dirty)
	_ = fm.MarkFailed("tun1")

	var collected []events.Event
	timeout := time.After(100 * time.Millisecond)
	done := false
	for !done {
		select {
		case ev := <-ch:
			if ev.Type == "dnsroute:failover" {
				collected = append(collected, ev)
			}
		case <-timeout:
			done = true
		}
	}

	if len(collected) != 1 {
		t.Errorf("expected 1 event (only first MarkFailed published), got %d", len(collected))
	}
}

func TestFailoverManager_HandlePingCheckEvent_IgnoresFailWithZeroCount(t *testing.T) {
	called := 0
	fm := NewFailoverManager(func() error { called++; return nil })

	// NDMS reports "fail" with failCount=0 at tunnel startup before any check ran.
	// This should NOT trigger failover.
	fm.handlePingCheckEvent(events.PingCheckStateEvent{
		TunnelID:  "tunnel1",
		Status:    "fail",
		FailCount: 0,
	})

	if called != 0 {
		t.Errorf("expected 0 reconcile calls for fail with failCount=0, got %d", called)
	}
	if fm.IsFailed("tunnel1") {
		t.Error("tunnel1 should NOT be in failedSet for fail with failCount=0")
	}
}

func TestFailoverManager_HandlePingCheckEvent_TriggersFailWithNonZeroCount(t *testing.T) {
	called := 0
	fm := NewFailoverManager(func() error { called++; return nil })

	// Real failure: failCount > 0.
	fm.handlePingCheckEvent(events.PingCheckStateEvent{
		TunnelID:  "tunnel1",
		Status:    "fail",
		FailCount: 3,
	})

	if called != 1 {
		t.Errorf("expected 1 reconcile call for real failure, got %d", called)
	}
	if !fm.IsFailed("tunnel1") {
		t.Error("tunnel1 should be in failedSet for real failure")
	}
}

package zapret

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

func TestWatchdogPublishesManagedDaemonTransitionsAndStops(t *testing.T) {
	oldInterval := watchdogInterval
	watchdogInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchdogInterval = oldInterval })
	bases := setupResolverInstalls(t, "zapret-watchdog")
	base := bases[0]
	pidDir := filepath.Join(t.TempDir(), "pids")
	if err := os.MkdirAll(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	functions := filepath.Join(base, "init.d", "sysv", "functions")
	if err := os.WriteFile(functions, []byte("PIDDIR=\""+pidDir+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var live []nfqProc
	var scans int
	oldProcesses := nfqProcessesHook
	nfqProcessesHook = func() []nfqProc {
		mu.Lock()
		defer mu.Unlock()
		scans++
		return append([]nfqProc(nil), live...)
	}
	t.Cleanup(func() { nfqProcessesHook = oldProcesses })

	bus := events.NewBus()
	_, ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	stop := NewService(t.TempDir(), nil).StartWatchdog(ctx, bus)
	defer func() { stop(); cancel() }()

	waitForProcessScans(t, &mu, &scans, 3)
	mu.Lock()
	live = []nfqProc{{pid: 99, exe: filepath.Join(base, "nfq2", "nfqws2")}} // independent scanner
	mu.Unlock()
	waitForProcessScans(t, &mu, &scans, 6)
	assertNoZapretEvent(t, ch)

	pidFile := filepath.Join(pidDir, "nfqws2_42.pid")
	if err := os.WriteFile(pidFile, []byte("42\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	live = append(live, nfqProc{pid: 42, exe: filepath.Join(base, "nfq2", "nfqws2")})
	mu.Unlock()
	assertZapretWatchdogEvent(t, ch)

	if err := os.Remove(pidFile); err != nil {
		t.Fatal(err)
	}
	newPIDFile := filepath.Join(pidDir, "nfqws2_43.pid")
	if err := os.WriteFile(newPIDFile, []byte("43\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	live = []nfqProc{
		{pid: 99, exe: filepath.Join(base, "nfq2", "nfqws2")},
		{pid: 43, exe: filepath.Join(base, "nfq2", "nfqws2")},
	}
	mu.Unlock()
	assertZapretWatchdogEvent(t, ch)

	if err := os.Remove(newPIDFile); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	live = nil
	mu.Unlock()
	assertZapretWatchdogEvent(t, ch)

	// The app registers this same idempotent stop on both normal exit and the
	// server's exec-restart hook; exercising concurrent calls covers both paths.
	var stopWG sync.WaitGroup
	stopWG.Add(2)
	go func() { defer stopWG.Done(); stop() }()
	go func() { defer stopWG.Done(); stop() }()
	stopWG.Wait()
	mu.Lock()
	before := scans
	mu.Unlock()
	time.Sleep(3 * watchdogInterval)
	mu.Lock()
	after := scans
	mu.Unlock()
	if after != before {
		t.Fatalf("process scans continued after stop: before=%d after=%d", before, after)
	}
}

func TestWatchdogDoesNotPollWhenZapretIsAbsent(t *testing.T) {
	oldInterval := watchdogInterval
	watchdogInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchdogInterval = oldInterval })
	root := t.TempDir()
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = root, nil
	var processScans atomic.Int32
	nfqProcessesHook = func() []nfqProc {
		processScans.Add(1)
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := NewService(t.TempDir(), nil).StartWatchdog(ctx, events.NewBus())
	defer stop()
	time.Sleep(3 * watchdogInterval)
	if got := processScans.Load(); got != 0 {
		t.Fatalf("absent installation touched /proc: scans=%d", got)
	}
}

func TestWatchdogDiscoversZapretInstalledAfterStart(t *testing.T) {
	oldInterval := watchdogInterval
	watchdogInterval = 10 * time.Millisecond
	t.Cleanup(func() { watchdogInterval = oldInterval })
	root := t.TempDir()
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = root, nil
	var processScans atomic.Int32
	nfqProcessesHook = func() []nfqProc {
		processScans.Add(1)
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	bus := events.NewBus()
	_, ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := NewService(t.TempDir(), nil).StartWatchdog(ctx, bus)
	defer stop()
	time.Sleep(3 * watchdogInterval)
	if got := processScans.Load(); got != 0 {
		t.Fatalf("absent installation touched /proc before install: scans=%d", got)
	}

	base := filepath.Join(root, "zapret-late")
	for _, dir := range []string{"nfq2", filepath.Join("init.d", "sysv")} {
		if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "config"), []byte("NFQWS2_OPT=\"--filter-tcp=443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "nfq2", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	assertZapretWatchdogEvent(t, ch)
	if processScans.Load() == 0 {
		t.Fatal("watchdog published install without beginning managed-process observation")
	}
}

func TestWatchdogFallbackDiscoversInstallChangedBelowOpt(t *testing.T) {
	oldInterval, oldDiscoveryTTL := watchdogInterval, watchdogAbsentDiscoveryTTL
	watchdogInterval, watchdogAbsentDiscoveryTTL = 5*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() { watchdogInterval, watchdogAbsentDiscoveryTTL = oldInterval, oldDiscoveryTTL })
	root := t.TempDir()
	base := filepath.Join(root, "zapret-pending")
	if err := os.MkdirAll(filepath.Join(base, "nfq2"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = root, nil
	var processScans atomic.Int32
	nfqProcessesHook = func() []nfqProc {
		processScans.Add(1)
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	bus := events.NewBus()
	_, ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := NewService(t.TempDir(), nil).StartWatchdog(ctx, bus)
	defer stop()
	rootStamp := statusRootStamp()

	if err := os.WriteFile(filepath.Join(base, "config"), []byte("NFQWS2_OPT=\"--filter-tcp=443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "nfq2", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "init.d", "sysv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := statusRootStamp(); got != rootStamp {
		t.Fatalf("nested install changed /opt root stamp: %q -> %q", rootStamp, got)
	}

	assertZapretWatchdogEvent(t, ch)
	if processScans.Load() == 0 {
		t.Fatal("watchdog published fallback install without beginning managed-process observation")
	}
}

func TestGetStatusWakesAbsentWatchdogForNestedInstall(t *testing.T) {
	oldInterval, oldDiscoveryTTL := watchdogInterval, watchdogAbsentDiscoveryTTL
	watchdogInterval, watchdogAbsentDiscoveryTTL = 5*time.Millisecond, time.Hour
	t.Cleanup(func() { watchdogInterval, watchdogAbsentDiscoveryTTL = oldInterval, oldDiscoveryTTL })
	root := t.TempDir()
	base := filepath.Join(root, "zapret-pending")
	if err := os.MkdirAll(filepath.Join(base, "nfq2"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldDirs, oldProcesses := optRoot, initDirs, nfqProcessesHook
	optRoot, initDirs = root, nil
	var processScans atomic.Int32
	nfqProcessesHook = func() []nfqProc {
		processScans.Add(1)
		return nil
	}
	t.Cleanup(func() { optRoot, initDirs, nfqProcessesHook = oldRoot, oldDirs, oldProcesses })

	svc := NewService(t.TempDir(), nil)
	svc.runVersion = func(context.Context, string) (string, error) { return "v1.2.3", nil }
	svc.opkgListInstalled = func(context.Context) (string, error) { return "", nil }
	bus := events.NewBus()
	_, ch, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := svc.StartWatchdog(ctx, bus)
	defer stop()
	rootStamp := statusRootStamp()

	if err := os.WriteFile(filepath.Join(base, "config"), []byte("NFQWS2_OPT=\"--filter-tcp=443\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "nfq2", "nfqws2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "init.d", "sysv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "init.d", "sysv", "zapret2"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := statusRootStamp(); got != rootStamp {
		t.Fatalf("nested install changed /opt root stamp: %q -> %q", rootStamp, got)
	}
	assertNoZapretEvent(t, ch)

	status := svc.GetStatus()
	if !status.Installed {
		t.Fatalf("GetStatus did not discover nested install: %+v", status)
	}
	assertZapretWatchdogEvent(t, ch)
	if got := processScans.Load(); got < 2 {
		t.Fatalf("expected both GetStatus and the awakened watchdog to inspect process state, got %d scans", got)
	}
}

func waitForProcessScans(t *testing.T, mu *sync.Mutex, scans *int, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		mu.Lock()
		got := *scans
		mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("process scans = %d, want at least %d", got, want)
		case <-time.After(time.Millisecond):
		}
	}
}

func assertNoZapretEvent(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case event := <-ch:
		t.Fatalf("unexpected event while managed state was stopped: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func assertZapretWatchdogEvent(t *testing.T, ch <-chan events.Event) {
	t.Helper()
	select {
	case event := <-ch:
		if event.Type != events.EventResourceInvalidated {
			t.Fatalf("event type = %q, want resource invalidation", event.Type)
		}
		payload, ok := event.Data.(events.ResourceInvalidatedEvent)
		if !ok || payload.Resource != events.ResourceRoutingZapretStatus || payload.Reason != "watchdog" {
			t.Fatalf("event payload = %#v, want zapret status watchdog invalidation", event.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("watchdog did not publish a Zapret status transition")
	}
}

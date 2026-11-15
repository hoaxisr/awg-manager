package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/capture"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

type partialWriter struct {
	result WriteResult
	err    error
}

func (w partialWriter) Add(context.Context, model.Target, string) (WriteResult, error) {
	return w.result, w.err
}
func (w partialWriter) Remove(context.Context, model.Target, string) (WriteResult, error) {
	return w.result, w.err
}

func TestMonitorWANUsesResolvedDirectPath(t *testing.T) {
	h := newHarness(t, nil)
	seen := &sync.Map{}
	recorder := &pathRecordingProbes{inner: h.probes, seen: seen}
	h.engine.deps.Probes = recorder
	h.routes.mu.Lock()
	h.routes.path = awgm.DirectPath{Interface: "eth4", Outbound: "direct"}
	h.routes.mu.Unlock()
	h.probes.setWAN(model.VerdictReachable)
	if _, err := h.engine.PatchConfig([]byte(`{"wanMonitorEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
	h.waitFor(func() bool {
		_, ok := seen.Load("fresh-wan|eth4")
		return ok
	})
}

// wanSeriesHarness поднимает движок с мониторными интервалами, пригодными
// для тестов серии неудач: тики и повторы по 5 мс. Монитор включается через
// PatchConfig ПОСЛЕ настройки фейков: иначе первая проверка монитора
// стартует ещё в newHarness и успевает увидеть вердикт до setWAN.
func wanSeriesHarness(t *testing.T, configure func(*config.Config)) *testHarness {
	t.Helper()
	oldInterval := monitorInterval
	oldRetry := wanRetryInterval
	monitorInterval = 5 * time.Millisecond
	wanRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		monitorInterval = oldInterval
		wanRetryInterval = oldRetry
	})
	h := newHarness(t, configure)
	if _, err := h.engine.PatchConfig([]byte(`{"wanMonitorEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
	return h
}

// TestWANFailureSeriesRequiresThreeConsecutiveFailures proves the new
// failure series: one and two consecutive failures keep the capture
// running, the third pauses it.
func TestWANFailureSeriesRequiresThreeConsecutiveFailures(t *testing.T) {
	h := wanSeriesHarness(t, nil)
	h.probes.setWAN(model.VerdictError)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
}

// TestWANFailureSeriesRetryComesFast proves the series retries ~1s after a
// failure instead of waiting the full monitor period. The ticker stays at
// the real 30s: a pause can then only happen through the retry timer.
func TestWANFailureSeriesRetryComesFast(t *testing.T) {
	oldInterval := monitorInterval
	oldRetry := wanRetryInterval
	monitorInterval = 30 * time.Second
	wanRetryInterval = 5 * time.Millisecond
	t.Cleanup(func() {
		monitorInterval = oldInterval
		wanRetryInterval = oldRetry
	})
	h := newHarness(t, nil)
	h.probes.setWAN(model.VerdictError)
	if _, err := h.engine.PatchConfig([]byte(`{"wanMonitorEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
}

// TestWANFailureSeriesResetsOnSuccess proves that a successful check inside
// a series resets the counter: after two failures and one success the next
// single failure must not pause the capture.
func TestWANFailureSeriesResetsOnSuccess(t *testing.T) {
	oldInterval := monitorInterval
	oldRetry := wanRetryInterval
	// Редкий каденс: 50 мс между проверками, чтобы между неудачами и
	// сбросом были наблюдаемые интервалы.
	monitorInterval = 50 * time.Millisecond
	wanRetryInterval = 50 * time.Millisecond
	t.Cleanup(func() {
		monitorInterval = oldInterval
		wanRetryInterval = oldRetry
	})
	h := newHarness(t, nil)
	h.probes.setWAN(model.VerdictError)
	if _, err := h.engine.PatchConfig([]byte(`{"wanMonitorEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
	h.waitFor(func() bool { return h.engine.wanFailureCount() >= 2 })
	h.probes.setWAN(model.VerdictReachable)
	h.waitFor(func() bool { return h.engine.wanFailureCount() == 0 })
	h.probes.setWAN(model.VerdictError)
	// Первая неудача новой серии: паузы быть не должно.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if count, paused := h.engine.wanSnapshot(); paused {
			t.Fatalf("single failure after a reset paused the capture (count=%d)", count)
		}
		if count, _ := h.engine.wanSnapshot(); count >= 2 {
			return // the new series is growing: the reset really happened
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("failure series did not restart after a successful check")
}

// TestWANPausedResumesOnFirstSuccess preserves the resume semantics inside
// the new flow: the first reachable check while paused resumes the capture.
func TestWANPausedResumesOnFirstSuccess(t *testing.T) {
	h := wanSeriesHarness(t, nil)
	h.probes.setWAN(model.VerdictError)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
	h.probes.setWAN(model.VerdictReachable)
	h.waitFor(func() bool { return !h.engine.Status().CapturePaused })
	if !h.engine.Status().CaptureRunning {
		t.Fatal("capture is not running after resume")
	}
}

// TestMonitorCheckDoesNotHoldEngineLocksDuringCapturePause is the deadlock
// regression (task 9). The old engine called capture.Pause() under
// commitMu+mu while the capture readLoop sat in emit→enqueue on e.mu. The
// fake here reproduces that chain: its Pause() re-enters the engine
// (Status + enqueue, both take e.mu) before returning. Under the old
// locking the re-entry blocks forever; the fixed engine must complete the
// pause and answer the re-entrant calls.
func TestMonitorCheckDoesNotHoldEngineLocksDuringCapturePause(t *testing.T) {
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })
	h := newHarness(t, func(cfg *config.Config) { cfg.WANMonitorEnabled = true })

	release := make(chan struct{})
	pauseStarted := make(chan struct{})
	inner := h.capture
	eng := h.engine
	wrapper := &reentrantPauseCapture{inner: inner, eng: eng, release: release, pauseStarted: pauseStarted}
	eng.commitMu.Lock()
	eng.mu.Lock()
	eng.capture = wrapper
	eng.captureGen++
	eng.mu.Unlock()
	eng.commitMu.Unlock()

	h.probes.setWAN(model.VerdictError)
	select {
	case <-pauseStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("engine never called capture.Pause")
	}
	// Пока Pause «висит», движок обязан отвечать на вызовы, берущие e.mu:
	// в старом коде monitorCheck держал его до конца Pause.
	statusDone := make(chan engineStatus, 1)
	go func() { statusDone <- eng.Status() }()
	select {
	case <-statusDone:
	case <-time.After(2 * time.Second):
		t.Fatal("engine locks are held during capture.Pause (status blocked)")
	}
	close(release)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
}

type engineStatus = Status

// reentrantPauseCapture wraps the fake capture: Pause marks the pause,
// signals the test, blocks on release and then re-enters the engine
// (Status + enqueue) exactly like the old emit→enqueue chain under Pause.
type reentrantPauseCapture struct {
	inner        *fakeCapture
	eng          *Engine
	release      chan struct{}
	pauseStarted chan struct{}
	reentered    bool
}

func (r *reentrantPauseCapture) Start(ctx context.Context) error { return r.inner.Start(ctx) }
func (r *reentrantPauseCapture) Stop() error                     { return r.inner.Stop() }
func (r *reentrantPauseCapture) Status() capture.CaptureStatus   { return r.inner.Status() }

func (r *reentrantPauseCapture) Pause() error {
	r.inner.mu.Lock()
	r.inner.paused = true
	r.inner.running = false
	r.inner.mu.Unlock()
	select {
	case <-r.pauseStarted:
	default:
		close(r.pauseStarted)
	}
	<-r.release
	// Реентер в движок, как делал readLoop в emit→enqueue во время Pause.
	r.eng.Status()
	r.eng.enqueue(capture.Suspect{
		Target:    model.Target{Domain: "reenter.example", Network: model.NetworkTCP, Port: 443},
		Reason:    "tcp-rst",
		ClientIP:  "192.168.1.50",
		ClientMAC: "aa:bb:cc:dd:ee:ff",
		FirstAt:   time.Now(), LastAt: time.Now(),
	})
	return nil
}

func TestPauseInvalidationRemovesCheckingCard(t *testing.T) {
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })
	h := newHarness(t, func(cfg *config.Config) { cfg.WANMonitorEnabled = true })
	h.probes.setWAN(model.VerdictReachable)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.holdTunnel(t, key)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateChecking
	})
	h.probes.setWAN(model.VerdictError)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
	if _, ok := h.candidate(key); ok {
		t.Fatal("cancelled checking card survived WAN pause")
	}
}

func TestAutoAddPartialPersistenceIsNotFailureOnly(t *testing.T) {
	h := newHarness(t, func(cfg *config.Config) { cfg.AutoAdd = true })
	h.engine.deps.Writer = partialWriter{
		result: WriteResult{Outbound: "awg0-out", Persisted: true, NewEntry: true},
		err:    errors.New("apply failed after content update"),
	}
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.setWAN(model.VerdictReachable)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateAdded
	})
	candidate, _ := h.candidate(key)
	if !strings.Contains(candidate.LastError, "apply failed") || h.engine.Status().AdditionsUsed != 1 {
		t.Fatalf("partial candidate=%+v additions=%d", candidate, h.engine.Status().AdditionsUsed)
	}
	state := h.state.Snapshot(time.Now())
	if len(state.History) == 0 || !strings.Contains(state.History[len(state.History)-1].Result, "partial") {
		t.Fatalf("partial history missing: %+v", state.History)
	}
}

func TestCaptureReadErrorTriggersFreshGeneration(t *testing.T) {
	old := captureRecoveryInterval
	captureRecoveryInterval = 5 * time.Millisecond
	t.Cleanup(func() { captureRecoveryInterval = old })
	h := newHarness(t, nil)
	before := h.captureStarts()
	h.capture.mu.Lock()
	h.capture.running = false
	h.capture.mu.Unlock()
	h.engine.CaptureError(errors.New("test recv failure"))
	h.waitFor(func() bool { return h.captureStarts() > before && h.engine.Status().CaptureRunning })
	if got := h.engine.Status().LastError; got != "" {
		t.Fatalf("capture recovery left stale error: %q", got)
	}
}

func TestMonitorDisableClearsRecoveredWANError(t *testing.T) {
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })
	h := newHarness(t, func(cfg *config.Config) { cfg.WANMonitorEnabled = true })
	h.probes.setWAN(model.VerdictError)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
	if _, err := h.engine.PatchConfig([]byte(`{"wanMonitorEnabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Status().LastError; got != "" {
		t.Fatalf("stale WAN error after resume: %q", got)
	}
}

func TestFirstTunnelVisibleAtStartupWithoutPersistence(t *testing.T) {
	h := newHarness(t, func(cfg *config.Config) { cfg.TargetInterface = "" })
	if got := h.engine.Status().TargetInterface; got != "awg0" {
		t.Fatalf("effective tunnel=%q", got)
	}
	if saved := h.config.Config().TargetInterface; saved != "" {
		t.Fatalf("implicit default persisted: %q", saved)
	}
}

func TestStatusJSONMatchesOperationalContract(t *testing.T) {
	h := newHarness(t, func(cfg *config.Config) { cfg.UDPEnabled = true })
	data, err := json.Marshal(h.engine.Status())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"captureActive", "capturePaused", "queuedChecks", "addsUsedHour", "addsLimit", "protocols", "lastWAN"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing JSON field %q: %s", key, data)
		}
	}
	for _, old := range []string{"captureRunning", "queueDepth", "additionsUsed", "additionsLimit"} {
		if _, ok := fields[old]; ok {
			t.Errorf("legacy JSON field %q remains: %s", old, data)
		}
	}
}

func TestAutoAddWithoutWriterFailsClosed(t *testing.T) {
	h := newHarness(t, func(cfg *config.Config) { cfg.AutoAdd = true })
	h.engine.deps.Writer = nil
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.setWAN(model.VerdictReachable)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if !strings.Contains(h.engine.Status().LastError, ErrNoWriter.Error()) {
		t.Fatalf("lastError=%q", h.engine.Status().LastError)
	}
}

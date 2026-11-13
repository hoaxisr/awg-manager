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

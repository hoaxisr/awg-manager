// Engine behaviour tests: direct×WAN matrix, tunnel gating, auto-add gates,
// WAN pause/resume, user actions vs late results, stop, config revision,
// duplicate suppression and the rolling addition budget.
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

// configConfig, storeState and tunnelType are single-file aliases for table
// entries.
type configConfig = config.Config
type storeState = store.State
type tunnelType = awgm.Tunnel

var errFake = errors.New("fake coverage failure")

func waitingCandidate(t *testing.T, h *testHarness, key string) model.Candidate {
	t.Helper()
	candidate, ok := h.candidate(key)
	if !ok {
		t.Fatalf("candidate %s not stored", key)
	}
	if candidate.State != model.CandidateWaiting {
		t.Fatalf("candidate %s state=%s, want waiting", key, candidate.State)
	}
	return candidate
}

// TestDirectWANMatrix walks the decision table of section 5.3.
func TestDirectWANMatrix(t *testing.T) {
	cases := []struct {
		name       string
		direct     model.Verdict
		wan        model.Verdict
		wantCard   bool
		wantTunnel bool // tunnel check expected to run
	}{
		{"reachable target never needs a candidate", model.VerdictReachable, model.VerdictReachable, false, false},
		{"reachable target with dead WAN", model.VerdictReachable, model.VerdictError, false, false},
		{"blocked with reachable WAN creates candidate", model.VerdictBlocked, model.VerdictReachable, true, true},
		{"blocked with dead WAN is skipped", model.VerdictBlocked, model.VerdictError, false, false},
		{"blocked with WAN blocked is skipped", model.VerdictBlocked, model.VerdictBlocked, false, false},
		{"error target is never a candidate", model.VerdictError, model.VerdictReachable, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			target := h.tcpTarget("example.com")
			key := target.CandidateKey()
			h.probes.setDirect(key, tc.direct)
			h.probes.setWAN(tc.wan)
			if tc.wantCard {
				h.probes.setTunnel(key, model.VerdictReachable)
			}
			h.capture.suspect(target, "tcp-rst")
			h.waitFor(func() bool {
				_, ok := h.candidate(key)
				return h.coverage.callsCount() > 0 && h.engine.Status().ActiveChecks == 0 && ok == tc.wantCard
			})
			if _, ok := h.candidate(key); ok != tc.wantCard {
				t.Fatalf("candidate stored=%v, want %v", ok, tc.wantCard)
			}
			if tc.wantCard {
				candidate := waitingCandidate(t, h, key)
				if tc.wantTunnel && candidate.Tunnel.Verdict == model.VerdictNotChecked {
					t.Fatal("tunnel check did not run although enabled")
				}
			}
		})
	}
}

// TestTunnelOffCreatesWaitingCandidateWithoutProbe verifies section 7: with
// tunnel probing off the candidate is still available for manual actions and
// the tunnel verdict stays not-checked.
func TestTunnelOffCreatesWaitingCandidateWithoutProbe(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	candidate := waitingCandidate(t, h, key)
	if candidate.Tunnel.Verdict != model.VerdictNotChecked {
		t.Fatalf("tunnel verdict=%s, want not-checked", candidate.Tunnel.Verdict)
	}
}

// TestAutoAddGates covers the automatic write gates of section 9.
func TestAutoAddGates(t *testing.T) {
	t.Run("auto add writes after tunnel reachable", func(t *testing.T) {
		h := newHarness(t, func(cfg *configConfig) { cfg.AutoAdd = true })
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.setTunnel(key, model.VerdictReachable)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool {
			candidate, ok := h.candidate(key)
			return ok && candidate.State == model.CandidateAdded
		})
		if len(h.writer.adds) != 1 {
			t.Fatalf("writer adds=%v", h.writer.adds)
		}
		if used := h.engine.Status().AdditionsUsed; used != 1 {
			t.Fatalf("additions used=%d, want 1", used)
		}
	})
	t.Run("tunnel blocked discards instead of writing", func(t *testing.T) {
		h := newHarness(t, func(cfg *configConfig) { cfg.AutoAdd = true })
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.setTunnel(key, model.VerdictBlocked)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool { return h.coverage.callsCount() > 0 })
		h.waitFor(func() bool {
			_, ok := h.candidate(key)
			return !ok && h.engine.Status().ActiveChecks == 0
		})
		if len(h.writer.adds) != 0 {
			t.Fatalf("unexpected writes: %v", h.writer.adds)
		}
	})
	t.Run("autoAdd disabled writes nothing", func(t *testing.T) {
		h := newHarness(t, nil)
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.setTunnel(key, model.VerdictReachable)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool {
			candidate, ok := h.candidate(key)
			return ok && candidate.State == model.CandidateWaiting
		})
		if len(h.writer.adds) != 0 {
			t.Fatalf("unexpected writes: %v", h.writer.adds)
		}
	})
	t.Run("fresh WAN failure before write blocks the commit", func(t *testing.T) {
		h := newHarness(t, func(cfg *configConfig) { cfg.AutoAdd = true })
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.setTunnel(key, model.VerdictReachable)
		// The parallel WAN is reachable, but the fresh pre-commit check fails.
		h.probes.setWANSplit(model.VerdictReachable, model.VerdictError)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool {
			candidate, ok := h.candidate(key)
			return ok && candidate.State == model.CandidateWaiting
		})
		if len(h.writer.adds) != 0 {
			t.Fatalf("unexpected writes: %v", h.writer.adds)
		}
	})
}

// TestPauseResumeCleanGeneration walks the WAN monitor transitions of
// section 6: non-reachable WAN pauses the capture and invalidates decisions,
// reachable WAN resumes with clean state; transitions log once.
func TestPauseResumeCleanGeneration(t *testing.T) {
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })
	h := newHarness(t, func(cfg *configConfig) { cfg.WANMonitorEnabled = true })

	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)

	// Outage: monitor pauses capture, queued and running work is dropped.
	h.probes.setWAN(model.VerdictError)
	startsBefore := h.captureStarts()
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
	h.capture.suspect(target, "tcp-rst")
	time.Sleep(30 * time.Millisecond)
	if _, ok := h.candidate(key); ok {
		t.Fatal("candidate created during WAN outage")
	}

	// Recovery: capture resumes and a new suspect creates the candidate.
	h.probes.setWAN(model.VerdictReachable)
	h.waitFor(func() bool { return !h.engine.Status().CapturePaused })
	if h.captureStarts() <= startsBefore {
		t.Fatal("capture did not restart after resume")
	}
	starts := h.captureStarts()
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if h.captureStarts() < starts {
		t.Fatal("capture restarted during normal processing")
	}
}

func TestPauseDrainsAdmissionIndex(t *testing.T) {
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })
	h := newHarness(t, func(cfg *configConfig) {
		cfg.WANMonitorEnabled = true
		cfg.TunnelProbe = false
	})
	h.probes.setWAN(model.VerdictReachable)
	blockers := []model.Target{h.ipTarget("203.0.113.120", 3478), h.ipTarget("203.0.113.121", 3478)}
	for _, target := range blockers {
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.hold(t, key)
		h.capture.suspect(target, "udp-no-response")
	}
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == workerCount })
	target := h.tcpTarget("paused.example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.capture.suspect(target, "response-stall")
	h.waitFor(func() bool { return h.engine.Status().QueueDepth == 1 })

	h.probes.setWAN(model.VerdictError)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
	for _, blocker := range blockers {
		h.probes.unhold(blocker.CandidateKey())
	}
	h.engine.mu.Lock()
	queued := len(h.engine.queued)
	h.engine.mu.Unlock()
	if queued != 0 {
		t.Fatalf("WAN pause retained %d admission keys", queued)
	}

	h.probes.setWAN(model.VerdictReachable)
	h.waitFor(func() bool { return !h.engine.Status().CapturePaused })
	h.capture.suspect(target, "response-stall")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
}

// TestDeleteVsLateCompletion proves that a late tunnel result cannot
// resurrect a deleted candidate.
func TestDeleteVsLateCompletion(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.hold(t, key)
	h.capture.suspect(target, "tcp-rst")

	// Wait until the attempt is registered, then remove the candidate.
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 1 })
	if err := h.engine.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	h.probes.unhold(key)
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 0 })
	if _, ok := h.candidate(key); ok {
		t.Fatal("late completion resurrected a deleted candidate")
	}
}

// TestDismissStopsInFlightAndRejectsRestores checks dismiss/restore
// idempotence: the dismiss of a checking candidate stores the rejected card
// (created while the tunnel probe runs) and the late tunnel result cannot
// overwrite it.
func TestDismissStopsInFlightAndRejectsRestores(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.holdTunnel(t, key)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 1 })
	h.waitFor(func() bool {
		cand, ok := h.candidate(key)
		return ok && cand.State == model.CandidateChecking
	})
	if err := h.engine.Dismiss(key); err != nil {
		t.Fatal(err)
	}
	h.probes.unholdTunnel(key)
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 0 })
	candidate, ok := h.candidate(key)
	if !ok || candidate.State != model.CandidateRejected {
		t.Fatalf("state=%s ok=%v, want rejected", candidate.State, ok)
	}
	if err := h.engine.Restore(key); err != nil {
		t.Fatal(err)
	}
	candidate, ok = h.candidate(key)
	if !ok || candidate.State != model.CandidateWaiting {
		t.Fatalf("state=%s ok=%v, want waiting after restore", candidate.State, ok)
	}
}

// TestStopDropsWork verifies that Stop closes admission, cancels attempts and
// that no candidate is written afterwards.
func TestStopDropsWork(t *testing.T) {
	h := newHarness(t, nil)
	eng := h.engine
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.hold(t, key)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool { return eng.Status().ActiveChecks == 1 })
	eng.Stop()
	h.probes.unhold(key)
	time.Sleep(20 * time.Millisecond)
	if _, ok := h.candidate(key); ok {
		t.Fatal("candidate written after stop")
	}
	if eng.Status().Running {
		t.Fatal("engine still running after stop")
	}
}

// TestConfigPatchInvalidates verifies that a configuration patch invalidates
// the old generation: held in-flight decisions must not publish.
func TestConfigPatchInvalidates(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.hold(t, key)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 1 })
	if _, err := h.engine.PatchConfig([]byte(`{"verbose":true}`)); err != nil {
		t.Fatal(err)
	}
	h.probes.unhold(key)
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 0 })
	if _, ok := h.candidate(key); ok {
		t.Fatal("stale decision survived a configuration patch")
	}
}

func TestTCPRequiresPassiveDomainEvidence(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	unnamed, err := model.NewObservedTarget("", "203.0.113.80", model.NetworkTCP, 443)
	if err != nil {
		t.Fatal(err)
	}
	h.probes.setDirect(unnamed.CandidateKey(), model.VerdictBlocked)
	h.capture.suspect(unnamed, "connect-no-response")
	time.Sleep(20 * time.Millisecond)
	if got := h.coverage.callsCount(); got != 0 {
		t.Fatalf("unnamed TCP reached coverage %d times", got)
	}
	if status := h.engine.Status(); status.QueueDepth != 0 || status.ActiveChecks != 0 {
		t.Fatalf("unnamed TCP queued work: %+v", status)
	}

	udp := h.ipTarget("203.0.113.81", 3478)
	h.probes.setDirect(udp.CandidateKey(), model.VerdictBlocked)
	h.capture.suspect(udp, "udp-no-response")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(udp.CandidateKey())
		return ok && candidate.State == model.CandidateWaiting
	})
}

func TestAdmissionCoalescesQueuedAndDecidedTargets(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	blockers := []model.Target{h.ipTarget("203.0.113.90", 3478), h.ipTarget("203.0.113.91", 3478)}
	for _, target := range blockers {
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.hold(t, key)
		h.capture.suspect(target, "udp-no-response")
	}
	h.waitFor(func() bool { return h.engine.Status().ActiveChecks == workerCount })

	queued := h.tcpTarget("queued.example.com")
	h.probes.setDirect(queued.CandidateKey(), model.VerdictBlocked)
	for i := 0; i < 20; i++ {
		h.capture.suspect(queued, "response-stall")
	}
	h.waitFor(func() bool { return h.engine.Status().QueueDepth == 1 })
	time.Sleep(20 * time.Millisecond)
	if got := h.engine.Status().QueueDepth; got != 1 {
		t.Fatalf("duplicate queued suspects grew queue to %d", got)
	}
	for _, target := range blockers {
		h.probes.unhold(target.CandidateKey())
	}
	h.waitFor(func() bool {
		candidate, ok := h.candidate(queued.CandidateKey())
		return ok && candidate.State == model.CandidateWaiting && h.engine.Status().QueueDepth == 0
	})

	before := h.coverage.callsCount()
	for i := 0; i < 10; i++ {
		h.capture.suspect(queued, "response-stall")
	}
	time.Sleep(20 * time.Millisecond)
	if got := h.coverage.callsCount(); got != before {
		t.Fatalf("decided candidate re-entered coverage: %d -> %d", before, got)
	}
}

// TestDuplicateQueueDedup verifies that repeated signals for one target do
// not create parallel checks, and that IP-only dedup spans ports.
func TestDuplicateQueueDedup(t *testing.T) {
	t.Run("same target repeated", func(t *testing.T) {
		h := newHarness(t, nil)
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.hold(t, key)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 1 })
		h.capture.suspect(target, "tcp-rst")
		h.capture.suspect(target, "response-stall")
		time.Sleep(20 * time.Millisecond)
		if h.engine.Status().ActiveChecks != 1 {
			t.Fatal("duplicate suspect created a second attempt")
		}
		h.probes.unhold(key)
	})
	t.Run("IP-only second port dedups", func(t *testing.T) {
		h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
		first := h.ipTarget("203.0.113.99", 443)
		second := h.ipTarget("203.0.113.99", 8443)
		if first.CandidateKey() != second.CandidateKey() {
			t.Fatal("IP-only targets must share one candidate key")
		}
		h.probes.setDirect(first.CandidateKey(), model.VerdictBlocked)
		h.probes.hold(t, first.CandidateKey())
		h.capture.suspect(first, "connect-no-response")
		h.waitFor(func() bool { return h.engine.Status().ActiveChecks == 1 })
		h.capture.suspect(second, "connect-no-response")
		time.Sleep(20 * time.Millisecond)
		if h.engine.Status().ActiveChecks != 1 {
			t.Fatal("second port of one IP created a second attempt")
		}
		h.probes.unhold(first.CandidateKey())
		h.waitFor(func() bool {
			candidate, ok := h.candidate(first.CandidateKey())
			return ok && candidate.State == model.CandidateWaiting
		})
	})
}

func TestConfigurableAdditionLimitKeepsRollingUsage(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.AddsPerHour = 2 })
	target := h.tcpTarget("limit.example.com")
	key := target.CandidateKey()
	now := time.Now()
	_ = h.state.Update(false, func(s *storeState) {
		s.Additions = append(s.Additions, now.Add(-time.Minute), now.Add(-time.Second))
		s.Candidates[key] = model.Candidate{ID: key, Target: target, State: model.CandidateWaiting, FirstAt: now}
	})
	if err := h.engine.Add(context.Background(), key); !errors.Is(err, ErrAddLimit) {
		t.Fatalf("add at configured limit: %v", err)
	}
	if got := h.engine.Status().AdditionsLimit; got != 2 {
		t.Fatalf("status limit=%d", got)
	}
	if _, err := h.engine.PatchConfig([]byte(`{"addsPerHour":3}`)); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Add(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Status().AdditionsUsed; got != 3 {
		t.Fatalf("rolling usage was reset or miscounted: %d", got)
	}
}

func TestClearHistoryPreservesCandidatesAndBudget(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("history.example.com")
	key := target.CandidateKey()
	now := time.Now()
	_ = h.state.Update(false, func(s *storeState) {
		s.Candidates[key] = model.Candidate{ID: key, Target: target, State: model.CandidateWaiting, FirstAt: now}
		s.History = append(s.History, model.HistoryEntry{At: now, Action: "added"})
		s.Additions = append(s.Additions, now)
	})
	if err := h.engine.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	state := h.state.Snapshot(time.Now())
	if len(state.History) != 0 || len(state.Candidates) != 1 || len(state.Additions) != 1 {
		t.Fatalf("clear history changed unrelated state: %+v", state)
	}
}

func TestClearHistoryPersistsEmptyJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := store.New(path)
	target, _ := model.NewTarget("history.example.com", "203.0.113.10", model.NetworkTCP, 443)
	now := time.Now()
	_ = state.Update(true, func(s *store.State) {
		s.Candidates[target.CandidateKey()] = model.Candidate{ID: target.CandidateKey(), Target: target, State: model.CandidateWaiting, FirstAt: now}
		s.History = append(s.History, model.HistoryEntry{At: now, Action: "added"})
		s.Additions = append(s.Additions, now)
	})
	eng := New(Dependencies{State: state})
	eng.running = true
	if err := eng.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	restarted := store.New(path)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	got := restarted.Snapshot(time.Now())
	if len(got.History) != 0 || len(got.Candidates) != 1 || len(got.Additions) != 1 {
		t.Fatalf("unexpected restarted state: %+v", got)
	}
}

func TestTunnelErrorKeepsCandidate(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("tunnel-error.example")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictError)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	candidate, _ := h.candidate(key)
	if candidate.Tunnel.Verdict != model.VerdictError {
		t.Fatalf("tunnel verdict=%s", candidate.Tunnel.Verdict)
	}
}

func TestPruneBlockedLegacyWaitingCandidates(t *testing.T) {
	h := newHarness(t, nil)
	now := time.Now()
	_ = h.state.Update(false, func(s *storeState) {
		for _, tc := range []struct {
			name   string
			state  model.CandidateState
			direct model.Verdict
			tunnel model.Verdict
		}{
			{"blocked-waiting.test", model.CandidateWaiting, model.VerdictBlocked, model.VerdictBlocked},
			{"blocked-added.test", model.CandidateAdded, model.VerdictBlocked, model.VerdictBlocked},
			{"blocked-rejected.test", model.CandidateRejected, model.VerdictBlocked, model.VerdictBlocked},
			{"tunnel-error.test", model.CandidateWaiting, model.VerdictBlocked, model.VerdictError},
			{"direct-reachable.test", model.CandidateWaiting, model.VerdictReachable, model.VerdictBlocked},
		} {
			target := h.tcpTarget(tc.name)
			s.Candidates[target.CandidateKey()] = model.Candidate{
				ID: target.CandidateKey(), Target: target, State: tc.state, FirstAt: now,
				Direct: model.CheckEvidence{Verdict: tc.direct}, Tunnel: model.CheckEvidence{Verdict: tc.tunnel},
			}
		}
	})
	h.engine.pruneBlockedCandidates(h.config.Config())
	if _, ok := h.candidate("domain|blocked-waiting.test|tcp|443"); ok {
		t.Fatal("legacy waiting candidate with both paths blocked survived")
	}
	for _, key := range []string{
		"domain|blocked-added.test|tcp|443", "domain|blocked-rejected.test|tcp|443",
		"domain|tunnel-error.test|tcp|443", "domain|direct-reachable.test|tcp|443",
	} {
		if _, ok := h.candidate(key); !ok {
			t.Fatalf("preserved candidate %s was removed", key)
		}
	}
}

// TestRollingAdditionBudget fills the 50 additions/hour budget and proves the
// gate blocks both auto and manual adds, then frees up as entries age out.
func TestRollingAdditionBudget(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.AutoAdd = true })
	now := time.Now()
	if err := h.state.Update(true, func(s *storeState) {
		for i := 0; i < 50; i++ {
			s.Additions = append(s.Additions, now.Add(-time.Duration(i+1)*time.Second))
		}
	}); err != nil {
		t.Fatal(err)
	}
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if len(h.writer.adds) != 0 {
		t.Fatal("auto-add ignored the rolling budget")
	}
	if err := h.engine.Add(context.Background(), key); err == nil {
		t.Fatal("manual add ignored the rolling budget")
	}
	if len(h.writer.adds) != 0 {
		t.Fatal("manual add wrote despite the budget")
	}
	// Age everything out: the next addition must succeed.
	if err := h.state.Update(true, func(s *storeState) { s.Additions = nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Add(context.Background(), key); err != nil {
		t.Fatalf("manual add after budget freed: %v", err)
	}
	if len(h.writer.adds) != 1 {
		t.Fatalf("writer adds=%v", h.writer.adds)
	}
}

// TestWANMonitorDisabledLeavesCaptureRunning verifies section 6: with the
// monitor disabled there is no pause logic and the parallel WAN still runs.
func TestWANMonitorDisabledLeavesCaptureRunning(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setWAN(model.VerdictError)
	h.capture.suspect(target, "tcp-rst")
	time.Sleep(30 * time.Millisecond)
	status := h.engine.Status()
	if status.CapturePaused {
		t.Fatal("capture paused although the WAN monitor is disabled")
	}
	if !status.CaptureRunning {
		t.Fatal("capture is not running")
	}
	if _, ok := h.candidate(key); ok {
		t.Fatal("candidate created with WAN down")
	}
}

// TestLastWANAndStatus checks the operational status fields.
func TestLastWANAndStatus(t *testing.T) {
	oldInterval := monitorInterval
	monitorInterval = 5 * time.Millisecond
	t.Cleanup(func() { monitorInterval = oldInterval })
	h := newHarness(t, func(cfg *configConfig) { cfg.WANMonitorEnabled = true })
	h.probes.setWAN(model.VerdictError)
	h.waitFor(func() bool { return h.engine.Status().CapturePaused })
	status := h.engine.Status()
	if status.LastWAN.Verdict != model.VerdictError {
		t.Fatalf("lastWAN=%s", status.LastWAN.Verdict)
	}
	if status.AdditionsLimit != 50 {
		t.Fatalf("additions limit=%d", status.AdditionsLimit)
	}
	if !strings.Contains(status.LastError, "WAN") {
		t.Fatalf("lastError=%q", status.LastError)
	}
	if status.QueueDepth < 0 || status.ActiveChecks < 0 {
		t.Fatalf("negative counts: %+v", status)
	}
}

// TestCoverageErrorAndExclusions verifies that coverage errors, covered and
// excluded targets never become candidates.
func TestCoverageErrorAndExclusions(t *testing.T) {
	t.Run("coverage error", func(t *testing.T) {
		h := newHarness(t, nil)
		h.coverage.setErr(errFake)
		target := h.tcpTarget("example.com")
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool { return h.coverage.callsCount() > 0 })
		time.Sleep(20 * time.Millisecond)
		if _, ok := h.candidate(target.CandidateKey()); ok {
			t.Fatal("coverage error produced a candidate")
		}
		if !strings.Contains(h.engine.Status().LastError, "coverage") {
			t.Fatalf("lastError=%q", h.engine.Status().LastError)
		}
	})
	t.Run("covered", func(t *testing.T) {
		h := newHarness(t, nil)
		h.coverage.setCovered(true)
		target := h.tcpTarget("example.com")
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool { return h.coverage.callsCount() > 0 })
		time.Sleep(20 * time.Millisecond)
		if _, ok := h.candidate(target.CandidateKey()); ok {
			t.Fatal("covered target produced a candidate")
		}
	})
	t.Run("excluded", func(t *testing.T) {
		h := newHarness(t, nil)
		h.coverage.setExcluded(true)
		target := h.tcpTarget("example.com")
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool { return h.coverage.callsCount() > 0 })
		time.Sleep(20 * time.Millisecond)
		if _, ok := h.candidate(target.CandidateKey()); ok {
			t.Fatal("excluded target produced a candidate")
		}
	})
}

// TestManualAddWithoutTunnelProof verifies that a manual add works on a
// waiting candidate even when the tunnel verdict is not-checked (section 7).
func TestManualAddWithoutTunnelProof(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if err := h.engine.Add(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	candidate, ok := h.candidate(key)
	if !ok || candidate.State != model.CandidateAdded {
		t.Fatalf("state=%s ok=%v after manual add", candidate.State, ok)
	}
	if len(h.writer.adds) != 1 {
		t.Fatalf("writer adds=%v", h.writer.adds)
	}
}

// TestDeleteRemovesOwnedRuleOnly verifies that Delete removes the owned rule
// only for an added candidate and that removal is recorded.
func TestDeleteRemovesOwnedRuleOnly(t *testing.T) {
	t.Run("added candidate removes rule", func(t *testing.T) {
		h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool {
			candidate, ok := h.candidate(key)
			return ok && candidate.State == model.CandidateWaiting
		})
		if err := h.engine.Add(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		if err := h.engine.Delete(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.candidate(key); ok {
			t.Fatal("candidate survived delete")
		}
		if len(h.writer.removes) != 1 {
			t.Fatalf("writer removes=%v", h.writer.removes)
		}
		history := h.state.Snapshot(time.Now()).History
		if len(history) == 0 || history[len(history)-1].Action != "removed" {
			t.Fatalf("history tail=%+v", history)
		}
	})
	t.Run("waiting candidate deletes without writer", func(t *testing.T) {
		h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
		target := h.tcpTarget("example.com")
		key := target.CandidateKey()
		h.probes.setDirect(key, model.VerdictBlocked)
		h.capture.suspect(target, "tcp-rst")
		h.waitFor(func() bool {
			candidate, ok := h.candidate(key)
			return ok && candidate.State == model.CandidateWaiting
		})
		if err := h.engine.Delete(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		if len(h.writer.removes) != 0 {
			t.Fatalf("writer removes=%v", h.writer.removes)
		}
	})
}

// TestStopIsIdempotentAndUserActionsAfterStop verify the stop contract.
func TestStopIsIdempotentAndUserActionsAfterStop(t *testing.T) {
	h := newHarness(t, nil)
	eng := h.engine
	eng.Stop()
	eng.Stop() // second call must not panic or block
	if err := eng.Add(context.Background(), "anything"); err == nil {
		t.Fatal("user action accepted after stop")
	}
}

// TestTunnelUnavailableProducesWaitingCandidate verifies that a missing
// tunnel interface still yields a waiting candidate with tunnel error
// evidence and never an automatic write.
func TestTunnelUnavailableProducesWaitingCandidate(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) {
		cfg.AutoAdd = true
		cfg.TargetInterface = ""
	})
	h.engine.deps.DefaultTunnel = func(context.Context) (tunnelType, error) {
		return tunnelType{}, ErrNoTunnel
	}
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	candidate := waitingCandidate(t, h, key)
	if candidate.Tunnel.Verdict != model.VerdictError {
		t.Fatalf("tunnel verdict=%s, want error", candidate.Tunnel.Verdict)
	}
	if len(h.writer.adds) != 0 {
		t.Fatalf("unexpected writes: %v", h.writer.adds)
	}
}

// TestDefaultTunnelNotPersisted verifies that the implicit first tunnel is
// used but never written back into the saved configuration.
func TestDefaultTunnelNotPersisted(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) {
		cfg.TunnelProbe = false
		cfg.TargetInterface = ""
	})
	h.engine.deps.DefaultTunnel = func(context.Context) (tunnelType, error) {
		return tunnelType{Interface: "nocli0", Outbound: "out", Enabled: true}, nil
	}
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	candidate := waitingCandidate(t, h, key)
	if candidate.TargetInterface != "nocli0" {
		t.Fatalf("candidate interface=%q, want implicit nocli0", candidate.TargetInterface)
	}
	if saved := h.config.Config(); saved.TargetInterface != "" {
		t.Fatalf("implicit default persisted: %q", saved.TargetInterface)
	}
}

// TestQueueOverflowDrops proves the bounded queue drops instead of growing.
func TestQueueOverflowDrops(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	// Two distinct targets occupy both workers; further distinct targets
	// overflow the bounded queue.
	held := []string{"ip|198.51.100.1", "ip|198.51.100.2"}
	for _, key := range held {
		h.probes.setDirect(key, model.VerdictBlocked)
		h.probes.hold(t, key)
	}
	for i := 0; i < queueCapacity+10; i++ {
		h.capture.suspect(h.ipTarget(fmt.Sprintf("198.51.100.%d", i+1), 443), "connect-no-response")
	}
	deadline := time.Now().Add(time.Second)
	for h.engine.Status().QueueDropped == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if h.engine.Status().QueueDropped == 0 {
		t.Fatal("bounded queue never dropped a suspect")
	}
	for _, key := range held {
		h.probes.unhold(key)
	}
}

// TestDirectPathUsedForProbesAndWAN verifies that route.Resolve feeds the
// direct path into target and WAN checks (same path guarantee, section 5.1).
func TestDirectPathUsedForProbesAndWAN(t *testing.T) {
	h := newHarness(t, nil)
	h.routes.path = awgm.DirectPath{Interface: "eth4", Outbound: "direct-out"}
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)

	var seen sync.Map
	wrapped := &pathRecordingProbes{inner: h.probes, seen: &seen}
	h.engine.deps.Probes = wrapped
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if _, ok := seen.Load("direct|eth4"); !ok {
		t.Fatal("direct target check did not use the resolved path")
	}
	if _, ok := seen.Load("wan|eth4"); !ok {
		t.Fatal("parallel WAN check did not use the same direct path")
	}
	if _, ok := seen.Load("tunnel|awg0"); !ok {
		t.Fatal("tunnel check did not use the configured tunnel interface")
	}
}

// pathRecordingProbes records the interfaces used per check kind.
type pathRecordingProbes struct {
	inner Probes
	seen  *sync.Map
}

func (p *pathRecordingProbes) record(kind string, path probe.Path) {
	p.seen.Store(kind+"|"+path.Interface, true)
}

func (p *pathRecordingProbes) Check(ctx context.Context, target model.Target, path probe.Path) probe.Result {
	p.record("tunnel", path)
	return p.inner.Check(ctx, target, path)
}

func (p *pathRecordingProbes) DirectAndWAN(ctx context.Context, target model.Target, url string, path probe.Path) probe.DirectAndWANResult {
	p.record("direct", path)
	result := p.inner.DirectAndWAN(ctx, target, url, path)
	p.record("wan", path)
	return result
}

func (p *pathRecordingProbes) CheckWAN(ctx context.Context, url string, path probe.Path) probe.Result {
	p.record("fresh-wan", path)
	return p.inner.CheckWAN(ctx, url, path)
}

// TestManualAddAfterAutoDuplicateIsFree proves that a duplicate write does
// not spend the rolling budget (section 9.4).
func TestManualAddAfterAutoDuplicateIsFree(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if err := h.engine.Add(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	used := h.engine.Status().AdditionsUsed
	if used != 1 {
		t.Fatalf("additions used=%d, want 1", used)
	}
	// The rule survives the delete (external rule): the second add of the
	// same target is a duplicate no-op.
	h.writer.stubbornRemove = true
	// Delete the card, re-create it via a suspect, add again: the writer
	// reports duplicate, the candidate becomes added, but no budget is spent.
	if err := h.engine.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateWaiting
	})
	if err := h.engine.Add(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if got := h.engine.Status().AdditionsUsed; got != used {
		t.Fatalf("duplicate addition spent budget: %d -> %d", used, got)
	}
}

// TestCaptureFactoryReceivesUDPMode verifies the UDP toggle reaches the
// capture factory and that a UDP change rebuilds the capture.
func TestCaptureFactoryReceivesUDPMode(t *testing.T) {
	h := newHarness(t, nil)
	if h.capture.udpAll {
		t.Fatal("UDP mode enabled by default")
	}
	if _, err := h.engine.PatchConfig([]byte(`{"udpEnabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if !h.capture.udpAll {
		t.Fatal("UDP toggle did not reach the capture factory")
	}
}

// TestSuspectAfterStopDoesNotPanic guards the enqueue path after Stop.
func TestSuspectAfterStopDoesNotPanic(t *testing.T) {
	h := newHarness(t, nil)
	eng := h.engine
	eng.Stop()
	target := h.tcpTarget("example.com")
	h.capture.suspect(target, "tcp-rst") // must be ignored, not panic
	time.Sleep(10 * time.Millisecond)
	if _, ok := h.candidate(target.CandidateKey()); ok {
		t.Fatal("suspect processed after stop")
	}
}

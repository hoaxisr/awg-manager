// Recheck behaviour: evidence refresh of a decided card, FirstAt
// preservation, state preservation of an added entry and the missing-id error.
package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// rejectedFixture stores a decided candidate directly in the store, as a
// previous decision would have left it.
func rejectedFixture(t *testing.T, h *testHarness) (model.Target, string) {
	t.Helper()
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	firstAt := time.Now().Add(-24 * time.Hour)
	if err := h.state.Update(true, func(s *storeState) {
		s.Candidates[key] = model.Candidate{
			ID:      key,
			Target:  target,
			Reason:  "tcp-rst",
			State:   model.CandidateRejected,
			FirstAt: firstAt,
			LastAt:  firstAt.Add(time.Hour),
			Direct:  model.CheckEvidence{Verdict: model.VerdictBlocked, DurationMS: 21_000, CheckedAt: firstAt.Add(time.Hour)},
			WAN:     model.CheckEvidence{Verdict: model.VerdictReachable, DurationMS: 400, CheckedAt: firstAt.Add(time.Hour)},
			Tunnel:  model.CheckEvidence{Verdict: model.VerdictReachable, DurationMS: 610, CheckedAt: firstAt.Add(time.Hour)},
		}
	}); err != nil {
		t.Fatal(err)
	}
	return target, key
}

// TestRecheckRefreshesDecidedCandidate reruns the full pipeline for a
// rejected card: fresh direct+WAN+tunnel evidence, preserved FirstAt, and a
// waiting state again now that both paths pass.
func TestRecheckRefreshesDecidedCandidate(t *testing.T) {
	h := newHarness(t, nil)
	_, key := rejectedFixture(t, h)
	firstAt, _ := h.candidate(key)
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.setWAN(model.VerdictReachable)

	if err := h.engine.Recheck(context.Background(), key); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	updated, ok := h.candidate(key)
	if !ok {
		t.Fatal("candidate disappeared after recheck")
	}
	if updated.State != model.CandidateWaiting {
		t.Fatalf("state=%s, want waiting after a fresh both-paths-pass rerun", updated.State)
	}
	if !updated.FirstAt.Equal(firstAt.FirstAt) {
		t.Fatalf("FirstAt changed: %v -> %v", firstAt.FirstAt, updated.FirstAt)
	}
	if updated.LastAt.Before(firstAt.LastAt) {
		t.Fatalf("LastAt went backwards: %v -> %v", firstAt.LastAt, updated.LastAt)
	}
	if updated.Direct.DurationMS != 20 || updated.Tunnel.DurationMS != 10 || updated.WAN.DurationMS != 5 {
		t.Fatalf("stale probe evidence: direct=%d wan=%d tunnel=%d ms",
			updated.Direct.DurationMS, updated.WAN.DurationMS, updated.Tunnel.DurationMS)
	}
	if updated.Tunnel.Verdict != model.VerdictReachable {
		t.Fatalf("tunnel verdict=%s, want reachable", updated.Tunnel.Verdict)
	}
}

// TestRecheckFinalizesCardOnEarlyExit verifies that a manual recheck never
// leaves an existing card in checking. Every early outcome keeps the previous
// decision state and records the evidence or error that stopped the check.
func TestRecheckFinalizesCardOnEarlyExit(t *testing.T) {
	tests := []struct {
		name         string
		initial      model.CandidateState
		setup        func(*testHarness, string)
		wantState    model.CandidateState
		wantDirect   model.Verdict
		wantWAN      model.Verdict
		wantCovered  bool
		wantExcluded bool
		wantError    string
	}{
		{
			name: "coverage error", initial: model.CandidateRejected,
			setup:     func(h *testHarness, _ string) { h.coverage.setErr(errors.New("coverage unavailable")) },
			wantState: model.CandidateRejected, wantDirect: model.VerdictNotChecked, wantWAN: model.VerdictNotChecked,
			wantError: "coverage",
		},
		{
			name: "covered", initial: model.CandidateRejected,
			setup:     func(h *testHarness, _ string) { h.coverage.setCovered(true) },
			wantState: model.CandidateRejected, wantDirect: model.VerdictNotChecked, wantWAN: model.VerdictNotChecked,
			wantCovered: true,
		},
		{
			name: "excluded", initial: model.CandidateRejected,
			setup:     func(h *testHarness, _ string) { h.coverage.setExcluded(true) },
			wantState: model.CandidateRejected, wantDirect: model.VerdictNotChecked, wantWAN: model.VerdictNotChecked,
			wantExcluded: true,
		},
		{
			name: "direct path error", initial: model.CandidateRejected,
			setup:     func(h *testHarness, _ string) { h.routes.setErr(errors.New("route unavailable")) },
			wantState: model.CandidateRejected, wantDirect: model.VerdictNotChecked, wantWAN: model.VerdictNotChecked,
			wantError: "direct path",
		},
		{
			name: "direct reachable", initial: model.CandidateRejected,
			setup: func(h *testHarness, key string) {
				h.probes.setDirect(key, model.VerdictReachable)
				h.probes.setWAN(model.VerdictReachable)
			},
			wantState: model.CandidateRejected, wantDirect: model.VerdictReachable, wantWAN: model.VerdictReachable,
		},
		{
			name: "direct error", initial: model.CandidateRejected,
			setup: func(h *testHarness, key string) {
				h.probes.setDirect(key, model.VerdictError)
				h.probes.setWAN(model.VerdictReachable)
			},
			wantState: model.CandidateRejected, wantDirect: model.VerdictError, wantWAN: model.VerdictReachable,
			wantError: "direct",
		},
		{
			name: "WAN error", initial: model.CandidateRejected,
			setup: func(h *testHarness, key string) {
				h.probes.setDirect(key, model.VerdictBlocked)
				h.probes.setWAN(model.VerdictError)
			},
			wantState: model.CandidateRejected, wantDirect: model.VerdictBlocked, wantWAN: model.VerdictError,
			wantError: "WAN",
		},
		{
			name: "WAN blocked", initial: model.CandidateRejected,
			setup: func(h *testHarness, key string) {
				h.probes.setDirect(key, model.VerdictBlocked)
				h.probes.setWAN(model.VerdictBlocked)
			},
			wantState: model.CandidateRejected, wantDirect: model.VerdictBlocked, wantWAN: model.VerdictBlocked,
			wantError: "WAN",
		},
		{
			name: "capture paused", initial: model.CandidateRejected,
			setup: func(h *testHarness, _ string) {
				h.engine.mu.Lock()
				h.engine.capturePaused = true
				h.engine.mu.Unlock()
			},
			wantState: model.CandidateRejected, wantDirect: model.VerdictNotChecked, wantWAN: model.VerdictNotChecked,
			wantError: "paused",
		},
		{
			name: "already checking", initial: model.CandidateChecking,
			setup: func(h *testHarness, key string) {
				h.probes.setDirect(key, model.VerdictReachable)
				h.probes.setWAN(model.VerdictReachable)
			},
			wantState: model.CandidateWaiting, wantDirect: model.VerdictReachable, wantWAN: model.VerdictReachable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			_, key := rejectedFixture(t, h)
			if tc.initial != model.CandidateRejected {
				if err := h.state.Update(true, func(s *storeState) {
					candidate := s.Candidates[key]
					candidate.State = tc.initial
					s.Candidates[key] = candidate
				}); err != nil {
					t.Fatal(err)
				}
			}
			tc.setup(h, key)

			if err := h.engine.Recheck(context.Background(), key); err != nil {
				t.Fatalf("recheck: %v", err)
			}
			updated, ok := h.candidate(key)
			if !ok {
				t.Fatal("candidate disappeared after recheck")
			}
			if updated.State != tc.wantState {
				t.Fatalf("state=%s, want %s", updated.State, tc.wantState)
			}
			if updated.Direct.Verdict != tc.wantDirect || updated.WAN.Verdict != tc.wantWAN {
				t.Fatalf("verdicts direct=%s wan=%s, want direct=%s wan=%s",
					updated.Direct.Verdict, updated.WAN.Verdict, tc.wantDirect, tc.wantWAN)
			}
			if updated.Coverage.Covered != tc.wantCovered || updated.Coverage.Excluded != tc.wantExcluded {
				t.Fatalf("coverage=%+v", updated.Coverage)
			}
			if tc.wantError == "" {
				if updated.LastError != "" {
					t.Fatalf("lastError=%q", updated.LastError)
				}
			} else if !strings.Contains(updated.LastError, tc.wantError) {
				t.Fatalf("lastError=%q, want %q", updated.LastError, tc.wantError)
			}
		})
	}
}

// TestRecheckDoesNotResurrectRemovedCandidate proves that a card removed
// while the recheck is waiting for the previous attempt is not recreated.
func TestRecheckDoesNotResurrectRemovedCandidate(t *testing.T) {
	h := newHarness(t, nil)
	_, key := rejectedFixture(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	h.engine.mu.Lock()
	h.engine.active[key] = &attempt{id: "old", ctx: ctx, cancel: cancel}
	h.engine.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- h.engine.Recheck(context.Background(), key) }()
	h.waitFor(func() bool { return ctx.Err() != nil })
	if err := h.engine.Forget(key); err != nil {
		t.Fatal(err)
	}
	h.engine.mu.Lock()
	delete(h.engine.active, key)
	h.engine.mu.Unlock()
	if err := <-done; err != ErrNotFound {
		t.Fatalf("recheck err=%v, want ErrNotFound", err)
	}
	if _, ok := h.candidate(key); ok {
		t.Fatal("recheck resurrected a removed candidate")
	}
}

// TestRecheckAddedKeepsState verifies the added path: probes rerun, evidence
// is refreshed, but the card stays added and the writer is not called again.
func TestRecheckAddedKeepsState(t *testing.T) {
	h := newHarness(t, nil)
	_, key := rejectedFixture(t, h)
	firstAt, _ := h.candidate(key)
	if err := h.state.Update(true, func(s *storeState) {
		added := s.Candidates[key]
		added.State = model.CandidateAdded
		added.TargetInterface = "awg0"
		added.TargetOutbound = "awg0-out"
		s.Candidates[key] = added
	}); err != nil {
		t.Fatal(err)
	}
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.setWAN(model.VerdictReachable)

	if err := h.engine.Recheck(context.Background(), key); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	updated, ok := h.candidate(key)
	if !ok {
		t.Fatal("added entry disappeared after recheck")
	}
	if updated.State != model.CandidateAdded {
		t.Fatalf("state=%s, want added to stay", updated.State)
	}
	if len(h.writer.adds) != 0 {
		t.Fatalf("recheck of an added entry must not write rules, adds=%v", h.writer.adds)
	}
	if updated.Direct.Verdict != model.VerdictBlocked || updated.Tunnel.Verdict != model.VerdictReachable {
		t.Fatalf("stale evidence after recheck: direct=%s tunnel=%s", updated.Direct.Verdict, updated.Tunnel.Verdict)
	}
	if updated.Direct.DurationMS != 20 || updated.Direct.CheckedAt.Equal(firstAt.Direct.CheckedAt) {
		t.Fatalf("direct evidence not refreshed: %+v", updated.Direct)
	}
	if updated.Tunnel.DurationMS != 10 || updated.Tunnel.CheckedAt.Equal(firstAt.Tunnel.CheckedAt) {
		t.Fatalf("tunnel evidence not refreshed: %+v", updated.Tunnel)
	}
	if !updated.FirstAt.Equal(firstAt.FirstAt) {
		t.Fatalf("FirstAt changed: %v -> %v", firstAt.FirstAt, updated.FirstAt)
	}
}

// TestRecheckAddedWithTunnelOff verifies the added path without tunnel
// probing: the tunnel evidence resets to not-checked, the state stays added.
func TestRecheckAddedWithTunnelOff(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.TunnelProbe = false })
	_, key := rejectedFixture(t, h)
	if err := h.state.Update(true, func(s *storeState) {
		added := s.Candidates[key]
		added.State = model.CandidateAdded
		added.Tunnel = model.CheckEvidence{Verdict: model.VerdictReachable, CheckedAt: time.Now().Add(-time.Hour)}
		s.Candidates[key] = added
	}); err != nil {
		t.Fatal(err)
	}
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setWAN(model.VerdictReachable)

	if err := h.engine.Recheck(context.Background(), key); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	updated, ok := h.candidate(key)
	if !ok {
		t.Fatal("added entry disappeared after recheck")
	}
	if updated.State != model.CandidateAdded {
		t.Fatalf("state=%s, want added to stay", updated.State)
	}
	if updated.Tunnel.Verdict != model.VerdictNotChecked {
		t.Fatalf("tunnel verdict=%s, want not-checked with the probe off", updated.Tunnel.Verdict)
	}
}

// TestRecheckDoesNotAutoAdd verifies that a manual recheck never writes a
// rule on its own, even with autoAdd enabled: the decision belongs to the
// user, so the candidate stays waiting and the writer is not called.
func TestRecheckDoesNotAutoAdd(t *testing.T) {
	h := newHarness(t, func(cfg *configConfig) { cfg.AutoAdd = true })
	_, key := rejectedFixture(t, h)
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.setWAN(model.VerdictReachable)

	if err := h.engine.Recheck(context.Background(), key); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	updated, ok := h.candidate(key)
	if !ok {
		t.Fatal("candidate disappeared after recheck")
	}
	if updated.State != model.CandidateWaiting {
		t.Fatalf("state=%s, want waiting: a manual recheck must not add the rule", updated.State)
	}
	if len(h.writer.adds) != 0 {
		t.Fatalf("manual recheck must not write rules, adds=%v", h.writer.adds)
	}
	if used := h.engine.Status().AdditionsUsed; used != 0 {
		t.Fatalf("additions used=%d, want 0", used)
	}
}

// TestRecheckMissingID verifies the error for an unknown id.
func TestRecheckMissingID(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.engine.Recheck(context.Background(), "domain|gone.example|tcp|443"); err != ErrNotFound {
		t.Fatalf("err=%v, want ErrNotFound", err)
	}
}

// TestRecheckCancelsInFlightAttempt verifies that a rerun interrupts the
// previous attempt for the same candidate instead of racing with it: the
// stale tunnel probe is cancelled, its late result never publishes, and the
// fresh attempt runs to a waiting card with a new correlation id.
func TestRecheckCancelsInFlightAttempt(t *testing.T) {
	h := newHarness(t, nil)
	target := h.tcpTarget("example.com")
	key := target.CandidateKey()
	h.probes.setDirect(key, model.VerdictBlocked)
	h.probes.setTunnel(key, model.VerdictReachable)
	h.probes.setWAN(model.VerdictReachable)
	h.probes.holdTunnel(t, key)
	h.capture.suspect(target, "tcp-rst")
	h.waitFor(func() bool {
		candidate, ok := h.candidate(key)
		return ok && candidate.State == model.CandidateChecking
	})
	stale, _ := h.candidate(key)

	// The rerun is synchronous and shares the probe gate with the stale
	// attempt, so it runs in the background; the gate opens after the rerun
	// has replaced the attempt.
	done := make(chan error, 1)
	go func() { done <- h.engine.Recheck(context.Background(), key) }()

	// The stale attempt must leave the active table (cancelled by the
	// recheck) before the fresh one is admitted.
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.engine.mu.Lock()
		_, busy := h.engine.active[key]
		h.engine.mu.Unlock()
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.probes.unholdTunnel(key)
	if err := <-done; err != nil {
		t.Fatalf("recheck: %v", err)
	}
	updated, ok := h.candidate(key)
	if !ok {
		t.Fatal("candidate disappeared after recheck")
	}
	if updated.State != model.CandidateWaiting {
		t.Fatalf("state=%s, want waiting", updated.State)
	}
	if updated.CorrelationID == stale.CorrelationID {
		t.Fatal("the stale attempt published its decision after the recheck")
	}
}

// TestRecheckStoppedEngine verifies the guard on a stopped engine.
func TestRecheckStoppedEngine(t *testing.T) {
	h := newHarness(t, nil)
	_, key := rejectedFixture(t, h)
	h.engine.Stop()
	if err := h.engine.Recheck(context.Background(), key); err != ErrStopped {
		t.Fatalf("err=%v, want ErrStopped", err)
	}
}

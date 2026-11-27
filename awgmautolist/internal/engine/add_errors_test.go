// Focused regressions for backlog section 2: any Add error — even one that
// already persisted an auxiliary object or the destination entry — leaves
// the candidate in its previous state, never added and never dropped. The
// error stays on the card and in the journal, and a retry completes the
// same operation idempotently before the card becomes added.
package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// flakyWriter fails the first Add with a scripted partial outcome and then
// delegates to a healthy fakeWriter. When the partial outcome reports
// NewEntry, the failed call really created the destination entry, so the
// retry sees a duplicate and must not spend the rolling budget a second time.
type flakyWriter struct {
	mu      sync.Mutex
	partial WriteResult
	err     error
	failed  bool
	inner   *fakeWriter
}

func (w *flakyWriter) Add(_ context.Context, target model.Target, iface string) (WriteResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.failed {
		w.failed = true
		if w.partial.NewEntry {
			w.inner.existing[iface+"|"+target.CandidateKey()] = true
		}
		return w.partial, w.err
	}
	return w.inner.Add(context.Background(), target, iface)
}

func (w *flakyWriter) Remove(ctx context.Context, target model.Target, iface string) (WriteResult, error) {
	return w.inner.Remove(ctx, target, iface)
}

func (w *flakyWriter) RefreshTargetOutbound(ctx context.Context, iface string) (WriteResult, error) {
	return w.inner.RefreshTargetOutbound(ctx, iface)
}

var errScriptedWrite = errors.New("staging apply failed: validation")

// addErrorCases enumerates the two confirmed partial-failure shapes:
// an auxiliary outbound written before a rule-set error (no destination
// entry, no budget spent) and a destination entry created before an
// apply/owner error (budget spent once).
var addErrorCases = []struct {
	name       string
	partial    WriteResult
	wantBudget int
}{
	{
		name:       "outbound mutated then ruleset error",
		partial:    WriteResult{Outbound: "awg0-out", Persisted: true, NewEntry: false},
		wantBudget: 0,
	},
	{
		name:       "entry created then apply error",
		partial:    WriteResult{Outbound: "awg0-out", Persisted: true, NewEntry: true},
		wantBudget: 1,
	},
}

func seedWaitingCandidate(t *testing.T, h *testHarness, target model.Target) string {
	t.Helper()
	key := target.CandidateKey()
	now := time.Now()
	_ = h.state.Update(false, func(s *storeState) {
		s.Candidates[key] = model.Candidate{ID: key, Target: target, State: model.CandidateWaiting, FirstAt: now}
	})
	return key
}

func assertAddErrorState(t *testing.T, h *testHarness, key string, wantBudget int) model.Candidate {
	t.Helper()
	candidate, ok := h.candidate(key)
	if !ok {
		t.Fatal("candidate disappeared after failed add")
	}
	if candidate.State != model.CandidateWaiting {
		t.Fatalf("state=%s, want waiting: an error must not mark the card added", candidate.State)
	}
	if !strings.Contains(candidate.LastError, "staging apply failed") {
		t.Fatalf("LastError=%q, want the write error on the card", candidate.LastError)
	}
	if got := h.engine.Status().AdditionsUsed; got != wantBudget {
		t.Fatalf("additions used=%d, want %d", got, wantBudget)
	}
	history := h.state.Snapshot(time.Now()).History
	if len(history) == 0 || !strings.Contains(history[len(history)-1].Result, "staging apply failed") {
		t.Fatalf("error history missing: %+v", history)
	}
	return candidate
}

func assertRetryAdded(t *testing.T, h *testHarness, key string, wantBudget int) {
	t.Helper()
	candidate, ok := h.candidate(key)
	if !ok || candidate.State != model.CandidateAdded {
		t.Fatalf("retry: state=%s ok=%v, want added", candidate.State, ok)
	}
	if candidate.LastError != "" {
		t.Fatalf("stale error after successful retry: %q", candidate.LastError)
	}
	if got := h.engine.Status().AdditionsUsed; got != wantBudget {
		t.Fatalf("additions used=%d, want %d: the retry must not spend the budget twice", got, wantBudget)
	}
	history := h.state.Snapshot(time.Now()).History
	if len(history) == 0 {
		t.Fatal("retry left no history")
	}
	last := history[len(history)-1]
	if last.Action != "added" || (last.Result != "ok" && !strings.Contains(last.Result, "duplicate")) {
		t.Fatalf("retry history=%+v, want a successful added entry", history)
	}
}

func TestManualAddErrorKeepsCandidate(t *testing.T) {
	for _, tc := range addErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			target := h.tcpTarget("example.com")
			key := seedWaitingCandidate(t, h, target)
			h.engine.deps.Writer = &flakyWriter{
				partial: tc.partial,
				err:     errScriptedWrite,
				inner:   h.writer,
			}

			// Regression 1/2: Persisted=true with an error must not move the
			// card to added, must not drop it, and must keep the error on the
			// card and in the journal (userAction stores neither removeCard
			// nor state=added here).
			if err := h.engine.Add(context.Background(), key); err == nil {
				t.Fatal("expected the write error to surface")
			}
			assertAddErrorState(t, h, key, tc.wantBudget)

			// Regression 3/4: a retry completes the idempotent operation and
			// only now the card becomes added, with the error cleared.
			if err := h.engine.Add(context.Background(), key); err != nil {
				t.Fatalf("retry add: %v", err)
			}
			assertRetryAdded(t, h, key, 1)
		})
	}
}

func TestAutoAddErrorKeepsCandidate(t *testing.T) {
	for _, tc := range addErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(cfg *config.Config) { cfg.AutoAdd = true })
			target := h.tcpTarget("example.com")
			key := target.CandidateKey()
			h.probes.setDirect(key, model.VerdictBlocked)
			h.probes.setTunnel(key, model.VerdictReachable)
			h.probes.setWAN(model.VerdictReachable)
			h.engine.deps.Writer = &flakyWriter{
				partial: tc.partial,
				err:     errScriptedWrite,
				inner:   h.writer,
			}
			h.capture.suspect(target, "tcp-rst")

			h.waitFor(func() bool {
				candidate, ok := h.candidate(key)
				return ok && candidate.State == model.CandidateWaiting && strings.Contains(candidate.LastError, "staging apply failed")
			})
			assertAddErrorState(t, h, key, tc.wantBudget)

			// Regression 3/4: the retry finishes the same operation and only
			// then the card becomes added, with the error cleared.
			if err := h.engine.Add(context.Background(), key); err != nil {
				t.Fatalf("retry add: %v", err)
			}
			assertRetryAdded(t, h, key, 1)
		})
	}
}

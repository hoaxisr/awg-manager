// Candidate publication, automatic write gating and user actions.
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/awgm"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/config"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/store"
)

// publishDecision stores a candidate card under commitMu. persist controls
// whether the store writes to flash: intermediate checking states stay in
// memory only. A stale attempt never writes, and a candidate whose state
// advanced beyond checking (user action or previous decision) cannot be
// resurrected by a late result.
func (e *Engine) publishDecision(att *attempt, key string, cand model.Candidate, state model.CandidateState, persist bool) bool {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if !att.valid(e) {
		e.logf("%s decision stale, no write for %s", att.id, key)
		return false
	}
	cand.State = state
	cand.LastAt = time.Now()
	var stored bool
	perr := e.deps.State.Update(persist, func(s *store.State) {
		existing, ok := s.Candidates[key]
		if ok && existing.State != model.CandidateChecking {
			// A decided candidate keeps its state; a repeated waiting card
			// is a no-op.
			stored = existing.State == state
			return
		}
		s.Candidates[key] = cand
		stored = true
	})
	if perr != nil {
		e.setLastError(fmt.Sprintf("%s state write: %v", att.id, perr))
	}
	return stored
}

// resolveTunnel returns the tunnel used for checks and rules. A saved
// TargetInterface wins; DefaultTunnel is consulted only while the saved value
// is empty and the implicit choice is never persisted back into config.
func (e *Engine) resolveTunnel(ctx context.Context, cfg config.Config) (awgm.Tunnel, error) {
	var tunnel awgm.Tunnel
	if cfg.TargetInterface != "" {
		tunnel = awgm.Tunnel{Interface: cfg.TargetInterface, Enabled: true}
	} else {
		if e.deps.DefaultTunnel == nil {
			return awgm.Tunnel{}, ErrNoTunnel
		}
		var err error
		tunnel, err = e.deps.DefaultTunnel(ctx)
		if err != nil {
			return awgm.Tunnel{}, err
		}
		if !tunnel.Enabled || tunnel.Interface == "" {
			return awgm.Tunnel{}, ErrNoTunnel
		}
	}
	e.mu.Lock()
	e.tunnelInterface = tunnel.Interface
	e.tunnelOutbound = tunnel.Outbound
	e.mu.Unlock()
	return tunnel, nil
}

// autoAdd performs the gated automatic write. Every gate is re-checked under
// commitMu immediately before the writer call, so a user action, a
// configuration change, a WAN outage or a budget change cannot slip between
// the checks and the write:
//   - tunnel probe enabled, AutoAdd enabled and tunnel verdict reachable;
//   - current decision generation;
//   - fresh WAN reachable on the same direct path;
//   - budget: at most 50 actual new additions in the rolling hour.
func (e *Engine) autoAdd(att *attempt, cand *model.Candidate, directPath awgm.DirectPath) bool {
	if cand.Tunnel.Verdict != model.VerdictReachable {
		return false
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()

	refuse := func(reason string) {
		e.logf("%s auto-add refused: %s", att.id, reason)
	}
	cfg := e.deps.Config.Config()
	if !cfg.TunnelProbe || !cfg.AutoAdd {
		refuse("autoAdd is disabled in configuration")
		return false
	}
	if e.deps.Writer == nil {
		refuse(ErrNoWriter.Error())
		e.setLastError(ErrNoWriter.Error())
		return false
	}
	if att.ctx.Err() != nil || e.gen.Load() != att.generation {
		refuse("decision is stale")
		return false
	}
	if e.additionsUsed() >= cfg.AddsPerHour {
		refuse(ErrAddLimit.Error())
		e.setLastError(fmt.Sprintf("%s auto-add: %v", att.id, ErrAddLimit))
		return false
	}
	wan := e.deps.Probes.CheckWAN(att.ctx, cfg.WANCheckURL, probe.Path{Interface: directPath.Interface})
	if wan.Verdict != model.VerdictReachable {
		refuse(fmt.Sprintf("fresh WAN %s before write", wan.Verdict))
		return false
	}

	iface := cand.TargetInterface
	if iface == "" {
		tunnel, err := e.resolveTunnel(att.ctx, cfg)
		if err != nil {
			refuse(err.Error())
			return false
		}
		iface = tunnel.Interface
	}

	result, err := e.deps.Writer.Add(att.ctx, cand.Target, iface)
	if err != nil && !result.Persisted {
		e.setLastError(fmt.Sprintf("%s writer: %v", att.id, err))
		e.logf("%s auto-add write failed: %v", att.id, err)
		failed := *cand
		failed.TargetInterface = iface
		failed.TargetOutbound = result.Outbound
		_ = e.deps.State.Update(true, func(s *store.State) {
			s.History = append(s.History, historyEntry(failed, "added", "auto", "error: "+err.Error()))
		})
		return false
	}
	if err != nil {
		// Content was persisted, but a later owner/apply step failed. Preserve
		// that partial success instead of presenting the operation as no-op.
		cand.LastError = err.Error()
		e.setLastError(fmt.Sprintf("%s writer partial: %v", att.id, err))
	}

	stored := false
	updateErr := e.deps.State.Update(true, func(s *store.State) {
		if existing, ok := s.Candidates[cand.ID]; ok && existing.State != model.CandidateChecking {
			return // a user decision appeared first: keep it
		}
		cand.State = model.CandidateAdded
		cand.TargetInterface = iface
		if result.Outbound != "" {
			cand.TargetOutbound = result.Outbound
		}
		cand.LastAt = time.Now()
		s.Candidates[cand.ID] = *cand
		stored = true
		if result.NewEntry {
			// Confirmed new destination: spend the rolling budget once. Owner/
			// outbound repairs and duplicate no-ops are free.
			s.Additions = append(s.Additions, time.Now())
			note := ""
			if err != nil {
				note = "partial: " + err.Error()
			}
			s.History = append(s.History, historyEntry(*cand, "added", "auto", note))
		}
	})
	if updateErr != nil {
		e.setLastError(fmt.Sprintf("%s state write: %v", att.id, updateErr))
	}
	if result.Persisted && err == nil {
		e.logf("%s auto-added %s via %s (%s)", att.id, cand.ID, iface, result.Outbound)
	} else if result.Persisted {
		e.logf("%s auto-add partially persisted %s via %s: %v", att.id, cand.ID, iface, err)
	} else {
		e.logf("%s auto-add was a duplicate, no budget spent", att.id)
	}
	return stored
}

// historyEntry builds one history record for a candidate.
func historyEntry(cand model.Candidate, action, source, note string) model.HistoryEntry {
	entry := model.HistoryEntry{
		At:        time.Now(),
		Domain:    cand.Target.Domain,
		Host:      cand.Target.Host,
		IP:        cand.Target.IP,
		Network:   cand.Target.Network,
		Port:      cand.Target.Port,
		Action:    action,
		Source:    source,
		Interface: cand.TargetInterface,
		Outbound:  cand.TargetOutbound,
		Reason:    cand.Reason,
	}
	switch {
	case note != "":
		entry.Result = note
	case action == "added" || action == "removed":
		entry.Result = "ok"
	}
	return entry
}

// pruneBlockedCandidates removes legacy waiting cards that can use neither
// direct nor tunnel. Added rules and rejected user decisions are preserved.
func (e *Engine) pruneBlockedCandidates(cfg config.Config) {
	if !cfg.TunnelProbe {
		return
	}
	var ids []string
	for id, candidate := range e.deps.State.Snapshot(time.Now()).Candidates {
		if candidate.State == model.CandidateWaiting &&
			candidate.Direct.Verdict == model.VerdictBlocked && candidate.Tunnel.Verdict == model.VerdictBlocked {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.deps.State.Update(true, func(s *store.State) {
		for _, id := range ids {
			delete(s.Candidates, id)
		}
	}); err != nil {
		e.setLastError("legacy blocked candidates prune: " + err.Error())
	}
}

// discardCandidate removes a decision card without touching routing rules or
// adding a history event. It is used when both usable paths are blocked.
func (e *Engine) discardCandidate(att *attempt, key string) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if !att.valid(e) {
		return
	}
	if err := e.deps.State.Update(true, func(s *store.State) {
		delete(s.Candidates, key)
	}); err != nil {
		e.setLastError(fmt.Sprintf("%s candidate discard: %v", att.id, err))
		return
	}
	e.logf("%s direct+tunnel blocked, candidate discarded: %s", att.id, key)
}

// ClearHistory clears only the persisted event journal. Candidates, routing
// rules and the rolling addition budget deliberately remain untouched.
func (e *Engine) ClearHistory() error {
	if !e.Running() {
		return ErrStopped
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if err := e.deps.State.Update(true, func(s *store.State) {
		s.History = []model.HistoryEntry{}
	}); err != nil {
		e.setLastError("history clear: " + err.Error())
		return err
	}
	e.logf("history cleared")
	return nil
}

// User actions ------------------------------------------------------------

// Add adds a candidate manually. A manual add does not require tunnel proof;
// only a tunnel interface is needed for the writer mapping. Idempotent: an
// already added candidate is not written twice. The rolling hourly limit
// covers manual and automatic additions alike and is checked immediately
// before the writer call.
func (e *Engine) Add(ctx context.Context, id string) error {
	return e.userAction(ctx, id, "added", false, func(cand model.Candidate) (model.Candidate, string, bool, error) {
		if cand.State == model.CandidateAdded {
			return cand, "", false, nil // already routed: no second write
		}
		if e.additionsUsed() >= e.deps.Config.Config().AddsPerHour {
			return cand, "", false, ErrAddLimit
		}
		if e.deps.Writer == nil {
			return cand, "", false, ErrNoWriter
		}
		tunnel, err := e.resolveTunnel(ctx, e.deps.Config.Config())
		if err != nil {
			return cand, "", false, err
		}
		result, err := e.deps.Writer.Add(ctx, cand.Target, tunnel.Interface)
		if result.Outbound != "" {
			cand.TargetOutbound = result.Outbound
		} else {
			cand.TargetOutbound = tunnel.Outbound
		}
		if err != nil {
			if result.Persisted {
				cand.State = model.CandidateAdded
				cand.TargetInterface = tunnel.Interface
				cand.LastError = err.Error()
				if result.NewEntry {
					e.spendAddition()
				}
			}
			return cand, "", false, err
		}
		cand.State = model.CandidateAdded
		cand.TargetInterface = tunnel.Interface
		if result.NewEntry {
			e.spendAddition()
			return cand, "", false, nil
		}
		return cand, "duplicate: rule already present", false, nil
	})
}

// Dismiss hides a candidate while it is still being checked or waiting. A
// routed (added) candidate is not hidden by a dismissal. Idempotent: a
// candidate that does not exist yet is still marked rejected when it appears,
// so this action also blocks the attempt in flight. Returns ErrNotFound only
// when no card exists and no attempt is running.
func (e *Engine) Dismiss(id string) error {
	return e.userAction(context.Background(), id, "dismissed", false, func(cand model.Candidate) (model.Candidate, string, bool, error) {
		switch cand.State {
		case model.CandidateRejected:
			return cand, "", false, nil
		case model.CandidateAdded:
			return cand, "", false, ErrInvalidState
		}
		cand.State = model.CandidateRejected
		return cand, "", false, nil
	})
}

// Delete forgets a candidate and removes an owned rule only if this module
// added it. Idempotent: deleting an already removed candidate does nothing.
func (e *Engine) Delete(ctx context.Context, id string) error {
	return e.userAction(ctx, id, "removed", true, func(cand model.Candidate) (model.Candidate, string, bool, error) {
		if cand.State == model.CandidateAdded {
			if e.deps.Writer == nil {
				return cand, "", false, ErrNoWriter
			}
			result, err := e.deps.Writer.Remove(ctx, cand.Target, removeKey(cand))
			if err != nil {
				if result.Persisted {
					cand.LastError = err.Error()
					return cand, "", true, err
				}
				return cand, "", false, err
			}
		}
		return cand, "", true, nil
	})
}

// Forget drops the candidate card without touching routing rules.
func (e *Engine) Forget(id string) error {
	return e.userAction(context.Background(), id, "forgotten", true, func(cand model.Candidate) (model.Candidate, string, bool, error) {
		return cand, "", true, nil
	})
}

// Restore returns a rejected candidate to waiting. Idempotent.
func (e *Engine) Restore(id string) error {
	return e.userAction(context.Background(), id, "restored", false, func(cand model.Candidate) (model.Candidate, string, bool, error) {
		if cand.State != model.CandidateRejected {
			return cand, "", false, nil
		}
		cand.State = model.CandidateWaiting
		return cand, "", false, nil
	})
}

// userAction runs one user decision under commitMu. In-flight attempts for
// the candidate are cancelled after the decision is stored, so a late result
// can neither overwrite the user decision nor resurrect a removed card. The
// apply closure may perform writer I/O: the store is not locked while it
// runs, and commitMu guarantees no other mutator changes the store meanwhile.
func (e *Engine) userAction(ctx context.Context, id, action string, missingOK bool, apply func(model.Candidate) (model.Candidate, string, bool, error)) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !e.Running() {
		return ErrStopped
	}
	e.commitMu.Lock()
	defer e.commitMu.Unlock()

	cand, exists := e.deps.State.Snapshot(time.Now()).Candidates[id]
	if !exists {
		e.cancelCandidateAttempt(id)
		if !missingOK {
			return ErrNotFound
		}
		e.logf("user %s %s: no stored candidate", action, id)
		return nil
	}
	before := cand.State
	updated, note, removeCard, actionErr := apply(cand)
	if actionErr != nil {
		e.setLastError(fmt.Sprintf("user %s %s: %v", action, id, actionErr))
		failed := updated
		failed.LastAt = time.Now()
		_ = e.deps.State.Update(true, func(s *store.State) {
			if removeCard {
				delete(s.Candidates, id)
			} else if updated.State != before {
				s.Candidates[id] = updated
			}
			s.History = append(s.History, historyEntry(failed, action, "user", "error: "+actionErr.Error()))
		})
		return actionErr
	}
	if !removeCard {
		updated.LastAt = time.Now()
	}
	after := updated.State
	changed := removeCard || before != after || note != ""
	entry := historyEntry(updated, action, "user", note)
	var perr error
	if removeCard {
		perr = e.deps.State.Update(true, func(s *store.State) {
			delete(s.Candidates, id)
			s.History = append(s.History, entry)
		})
	} else if changed {
		perr = e.deps.State.Update(true, func(s *store.State) {
			s.Candidates[id] = updated
			s.History = append(s.History, entry)
		})
	}
	if perr != nil {
		e.setLastError(fmt.Sprintf("user %s %s: %v", action, id, perr))
		return perr
	}
	e.cancelCandidateAttempt(id)
	e.logf("user %s %s: %s -> %s%s", action, id, before, after, noteSuffix(note))
	return nil
}

// cancelCandidateAttempt stops the in-flight decision for one candidate.
func (e *Engine) cancelCandidateAttempt(id string) {
	e.mu.Lock()
	if att, busy := e.active[id]; busy {
		att.cancel()
	}
	e.mu.Unlock()
}

// spendAddition records one actual addition in the rolling budget.
func (e *Engine) spendAddition() {
	_ = e.deps.State.Update(true, func(s *store.State) {
		s.Additions = append(s.Additions, time.Now())
	})
}

// removeKey is the interface identifier handed back to the writer for
// removal; it matches what the writer received for the addition.
func removeKey(cand model.Candidate) string {
	if cand.TargetInterface != "" {
		return cand.TargetInterface
	}
	return cand.TargetOutbound
}

func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}

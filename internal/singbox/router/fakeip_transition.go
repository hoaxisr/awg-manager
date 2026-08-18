package router

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// Routing-mode transition orchestration (Task 1D.4).
//
// A mode switch (off↔tproxy↔fakeip-tun↔policy-tun) must TEAR DOWN the old mode then BRING
// UP the new one, with directional fail-closed rollback and per-step progress
// events for the UI. The old UpdateSettings→Reconcile path dispatches by the NEW
// mode and never disables the OLD mode's resources — SwitchRoutingMode closes
// that by COMPOSING the existing Enable/Disable (it does not reimplement them).
// ---------------------------------------------------------------------------

// transitionEventType is the bus event the UI progress screen subscribes to.
// It flows through the existing SSE bridge (internal/api/events.go) with no new
// stream endpoint — it is just another bus Publish.
const transitionEventType = "singbox-router:transition"

// TransitionStep is one orchestration milestone in a routing-mode switch.
type TransitionStep struct {
	Step    string `json:"step"`              // e.g. "start", "teardown", "provision", "readiness", "ready", "rollback", "error"
	Status  string `json:"status"`            // "current" | "done" | "error"
	Message string `json:"message,omitempty"` // optional human-readable detail
}

// TransitionEvent is published as "singbox-router:transition" for the UI
// progress screen. Milestone-level granularity is the honest resolution here:
// Enable/Disable are monolithic, so finer Enable-internal step events are a
// follow-up (Slice 1E) — not emitted by this task.
type TransitionEvent struct {
	TransitionID string         `json:"transitionId"`
	From         string         `json:"from"`                 // source state: off|tproxy|fakeip-tun|policy-tun
	To           string         `json:"to"`                   // target state
	Step         TransitionStep `json:"step"`                 // the milestone this event reports
	Done         bool           `json:"done,omitempty"`       // terminal event
	FinalState   string         `json:"finalState,omitempty"` // resulting state when Done
	Error        string         `json:"error,omitempty"`
}

// routing-mode / state string constants shared by the orchestration.
const (
	stateOff       = "off"
	stateTProxy    = "tproxy"
	stateFakeIPTun = "fakeip-tun"
)

// transitionSeq is a process-wide monotonic counter backing the default
// TransitionID. A counter (not time/rand) keeps the orchestration testable
// without injecting a clock — SwitchRoutingMode allocates one id per call.
var transitionSeq atomic.Uint64

func nextTransitionID() string {
	return "switch-" + strconv.FormatUint(transitionSeq.Add(1), 10)
}

// emitTransition publishes a single "singbox-router:transition" event. A nil
// Events bus (tests that don't wire it) silently no-ops, mirroring emitStatus.
func (s *ServiceImpl) emitTransition(id, from, to string, step TransitionStep, done bool, finalState, errMsg string) {
	if s.deps.Events == nil {
		return
	}
	s.deps.Events.Publish(transitionEventType, TransitionEvent{
		TransitionID: id,
		From:         from,
		To:           to,
		Step:         step,
		Done:         done,
		FinalState:   finalState,
		Error:        errMsg,
	})
}

// validTransitionTarget reports whether target is in the closed set
// SwitchRoutingMode accepts.
func validTransitionTarget(target string) bool {
	switch target {
	case stateOff, stateTProxy, stateFakeIPTun, statePolicyTun:
		return true
	default:
		return false
	}
}

// currentState reads the persisted state: off when !Enabled, else the raw
// RoutingMode. Raw (not normalized) so a corrupt RoutingMode does not silently
// reclassify the source — the teardown must target whatever Disable will target.
func (s *ServiceImpl) currentState() (string, error) {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return "", err
	}
	if !settings.SingboxRouter.Enabled {
		return stateOff, nil
	}
	mode := settings.SingboxRouter.RoutingMode
	if mode == "" {
		mode = stateTProxy // empty mode normalizes to tproxy (legacy default)
	}
	return mode, nil
}

// SwitchRoutingMode orchestrates a routing-mode transition with directional
// fail-closed rollback (FE-spec §7.4) and progress events. target ∈
// {"off","tproxy","fakeip-tun","policy-tun"}.
//
// It COMPOSES the existing Enable/Disable: the teardown calls Disable while the
// OLD mode is still persisted (Disable dispatches by the persisted mode), then
// flips RoutingMode/Enabled and calls Enable (which dispatches by the new mode).
// Directional rollback restores a consistent state on Enable failure, and is
// fail-closed: if rollback itself fails the router is left OFF, never
// half-assembled. finalState in every emitted event reflects reality.
//
// Concurrency: serialized by transitionMu (NOT s.mu — Enable/Disable take s.mu).
func (s *ServiceImpl) SwitchRoutingMode(ctx context.Context, target string) error {
	if !validTransitionTarget(target) {
		return fmt.Errorf("invalid routing mode %q (want off|tproxy|fakeip-tun|policy-tun)", target)
	}

	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()

	id := nextTransitionID()

	source, err := s.currentState()
	if err != nil {
		s.emitTransition(id, stateOff, target,
			TransitionStep{Step: "error", Status: "error", Message: err.Error()},
			true, stateOff, err.Error())
		return err
	}

	// No-op: already in the target state. Emit a single done event so the UI's
	// progress screen still resolves, and return without any teardown/enable.
	if source == target {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "ready", Status: "done", Message: "already in target state"},
			true, target, "")
		return nil
	}

	s.emitTransition(id, source, target,
		TransitionStep{Step: "start", Status: "current"}, false, "", "")

	// ── Teardown the OLD mode (if any) ──────────────────────────────────────
	// Disable dispatches by the STILL-persisted source mode, so it tears down
	// the correct resources (tproxy iptables OR fakeip opkgtun/routes) and
	// persists Enabled=false (+ clears fakeip persist).
	if source != stateOff {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "teardown", Status: "current"}, false, "", "")
		if err := s.Disable(ctx); err != nil {
			// Teardown failed: the old mode may be partially down. Surface with
			// the failing step + a best-effort finalState. We do NOT proceed to
			// bring up the target on top of a failed teardown.
			s.emitTransition(id, source, target,
				TransitionStep{Step: "teardown", Status: "error", Message: err.Error()},
				true, source, fmt.Sprintf("teardown failed (step=teardown, finalState=%s): %v", source, err))
			return fmt.Errorf("switch %s→%s: teardown failed (finalState=%s): %w", source, target, source, err)
		}
		s.emitTransition(id, source, target,
			TransitionStep{Step: "teardown", Status: "done"}, false, "", "")
	}

	// ── Target == off: teardown is the whole job ────────────────────────────
	if target == stateOff {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "ready", Status: "done"}, true, stateOff, "")
		return nil
	}

	// ── Bring up the TARGET mode ────────────────────────────────────────────
	if err := s.persistMode(target, true); err != nil {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "error", Status: "error", Message: err.Error()},
			true, source, err.Error())
		return fmt.Errorf("switch %s→%s: persist target mode: %w", source, target, err)
	}
	s.transitionReadinessProgress = func(msg string) {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "readiness", Status: "current", Message: msg},
			false, "", "")
	}
	defer func() { s.transitionReadinessProgress = nil }()

	// tproxy Enable waits for sing-box BEFORE iptables install — emit readiness
	// (not provision) so the UI spinner matches reality. fakeip-tun still uses
	// provision:current because its long bring-up is mapped to that milestone.
	if target == stateTProxy {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "readiness", Status: "current", Message: "запуск sing-box"},
			false, "", "")
	} else {
		s.emitTransition(id, source, target,
			TransitionStep{Step: "provision", Status: "current"}, false, "", "")
	}

	if enableErr := s.Enable(ctx); enableErr != nil {
		failStep := "provision"
		if target == stateTProxy {
			failStep = "readiness"
		}
		s.emitTransition(id, source, target,
			TransitionStep{Step: failStep, Status: "error", Message: enableErr.Error()},
			false, "", "")
		return s.rollbackSwitch(ctx, id, source, target, enableErr)
	}

	s.emitTransition(id, source, target,
		TransitionStep{Step: "provision", Status: "done"}, false, "", "")
	s.emitTransition(id, source, target,
		TransitionStep{Step: "readiness", Status: "done"}, false, "", "")
	s.emitTransition(id, source, target,
		TransitionStep{Step: "ready", Status: "done"}, true, target, "")
	return nil
}

// persistMode sets RoutingMode + Enabled in settings and saves. Used to flip
// the persisted target before Enable (which dispatches by the persisted mode)
// and to set the rollback's resting state.
func (s *ServiceImpl) persistMode(mode string, enabled bool) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	// Mutate a private copy: Load/Get hand out the store's live cache, which
	// other goroutines read without a lock.
	cp := *settings
	settings = &cp
	settings.SingboxRouter.RoutingMode = mode
	settings.SingboxRouter.Enabled = enabled
	return s.deps.Settings.Save(settings)
}

// rollbackSwitch performs the directional fail-closed rollback (FE-spec §7.4)
// after Enable(target) fails. It returns the ORIGINAL Enable error (wrapped with
// the failing step + final state). The table:
//
//	source=tproxy,     target=fakeip-tun|policy-tun → restore tproxy;   finalState=tproxy
//	source=policy-tun, target=tproxy                → restore policy-tun; finalState=policy-tun
//	source=fakeip,     target=tproxy|policy-tun     → DO NOT restore fakeip (its teardown
//	                                                  already freed the index); go to OFF;
//	                                                  finalState=off (explicit error)
//	source=policy-tun, target=fakeip-tun            → same: go to OFF;  finalState=off
//	source=off,        target=любая                 → leave disabled;   finalState=off
//
// If the rollback Enable ALSO fails, the router is left OFF + Enabled=false
// (fail-closed), and both errors are surfaced.
func (s *ServiceImpl) rollbackSwitch(ctx context.Context, id, source, target string, enableErr error) error {
	s.emitTransition(id, source, target,
		TransitionStep{Step: "error", Status: "error", Message: enableErr.Error()}, false, "", "")
	s.emitTransition(id, source, target,
		TransitionStep{Step: "rollback", Status: "current"}, false, "", "")

	// slot-20-пары восстанавливают прежний режим: tproxy↔policy-tun. К моменту
	// rollback'а teardown источника уже прошёл, но для policy-tun он теперь
	// УДЕРЖИВАЮЩИЙ: интерфейс жив, персист хранит {Provisioned:false, Index}, и
	// restore-Enable переиспользует тот же номер вместе с разрешением в
	// политике. (Прежде персист очищался и индекс аллоцировался заново.)
	restorable := (source == stateTProxy && (target == stateFakeIPTun || target == statePolicyTun)) ||
		(source == statePolicyTun && target == stateTProxy)
	if restorable {
		if perr := s.persistMode(source, true); perr != nil {
			return s.failClosed(id, source, target, enableErr,
				fmt.Errorf("rollback persist %s: %w", source, perr))
		}
		if rerr := s.Enable(ctx); rerr != nil {
			// Restoring the source also failed → fail closed to OFF.
			return s.failClosed(id, source, target, enableErr,
				fmt.Errorf("rollback re-enable %s: %w", source, rerr))
		}
		final := source
		s.emitTransition(id, source, target,
			TransitionStep{Step: "rollback", Status: "done", Message: "restored " + final},
			true, final,
			fmt.Sprintf("switch to %s failed (step=provision, finalState=%s); rolled back: %v", target, final, enableErr))
		return fmt.Errorf("switch %s→%s failed (finalState=%s, rolled back): %w", source, target, final, enableErr)
	}

	// fakeip→(tproxy|policy-tun) и policy-tun→fakeip failed AFTER the source
	// teardown already freed its index. Restoring the source blindly would
	// re-allocate/re-provision — NOT a clean rollback. Go to OFF and say so
	// explicitly.
	if source == stateFakeIPTun || (source == statePolicyTun && target == stateFakeIPTun) {
		msg := fmt.Sprintf("switch to %s failed after %s teardown; left disabled", target, source)
		if perr := s.persistMode(target, false); perr != nil {
			return s.failClosed(id, source, target, enableErr,
				fmt.Errorf("rollback persist disabled: %w", perr))
		}
		s.emitTransition(id, source, target,
			TransitionStep{Step: "rollback", Status: "done", Message: msg},
			true, stateOff,
			fmt.Sprintf("%s (step=provision, finalState=%s): %v", msg, stateOff, enableErr))
		return fmt.Errorf("switch %s→%s failed (finalState=%s): %s: %w", source, target, stateOff, msg, enableErr)
	}

	// source=off (→ любая цель): nothing to restore — leave disabled.
	if perr := s.persistMode(target, false); perr != nil {
		return s.failClosed(id, source, target, enableErr,
			fmt.Errorf("rollback persist disabled: %w", perr))
	}
	s.emitTransition(id, source, target,
		TransitionStep{Step: "rollback", Status: "done", Message: "left disabled"},
		true, stateOff,
		fmt.Sprintf("switch to %s failed (step=provision, finalState=%s): %v", target, stateOff, enableErr))
	return fmt.Errorf("switch %s→%s failed (finalState=%s): %w", source, target, stateOff, enableErr)
}

// failClosed is the last-resort resting state when even the rollback failed:
// persist Enabled=false (best-effort) and surface BOTH errors. finalState=off.
func (s *ServiceImpl) failClosed(id, source, target string, primary, rollbackErr error) error {
	_ = s.persistMode(target, false) // best-effort; we are already fail-closing

	s.emitTransition(id, source, target,
		TransitionStep{Step: "error", Status: "error", Message: rollbackErr.Error()},
		true, stateOff,
		fmt.Sprintf("switch to %s failed AND rollback failed (step=rollback, finalState=%s): enable=%v; rollback=%v",
			target, stateOff, primary, rollbackErr))
	return fmt.Errorf("switch %s→%s failed (finalState=%s): enable=%w; rollback=%v", source, target, stateOff, primary, rollbackErr)
}

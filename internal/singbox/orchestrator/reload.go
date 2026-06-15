package orchestrator

import (
	"fmt"
	"time"
)

// scheduleReload arms (or re-arms) the debounce timer. Caller MUST
// hold o.mu. Calling repeatedly within the window coalesces into one
// reload.
func (o *Orchestrator) scheduleReload() {
	if o.reloadTimer != nil {
		o.reloadTimer.Reset(reloadDebounce)
		return
	}
	o.reloadTimer = time.AfterFunc(reloadDebounce, func() {
		if err := o.Reload(); err != nil {
			o.log("error", fmt.Sprintf("orchestrator reload: %v", err))
		}
	})
}

// Reload validates the merged enabled config and applies it to sing-box:
//   - If validation fails — log + return validation error, NO process change.
//   - If validation passes:
//   - If at least one non-base slot is enabled → ensure running:
//     start if stopped, SIGHUP if running.
//   - If only base (or nothing) is enabled → stop if running.
//
// Reload may be called manually (e.g. by Apply UI button) or fired by
// the internal debounce timer. Safe for concurrent callers — internally
// serialized by mu and the reloading flag.
func (o *Orchestrator) Reload() error {
	o.mu.Lock()
	if o.reloading {
		o.mu.Unlock()
		return nil // collapse re-entrancy
	}
	o.reloading = true
	o.dirty = false
	res := o.validateLocked()
	if !res.Ok() {
		o.reloading = false
		o.mu.Unlock()
		msg := fmt.Sprintf("orchestrator validation failed; reload skipped: %s", res.Error())
		o.log("error", msg)
		return res
	}
	needRunning := o.hasActiveWorkLocked()
	proc := o.proc
	shouldRun := o.shouldRun
	prevHasTun := o.prevHasTun
	newHasTun := res.HasTun
	o.mu.Unlock()

	var err error
	if proc == nil {
		// Test mode or pre-wiring — nothing to do.
	} else {
		running, _ := proc.IsRunning()
		switch {
		case needRunning && !running:
			// Honour the sticky-stop intent: if the user pressed Stop,
			// shouldRun returns false and we must not resurrect the
			// daemon merely because a slot file changed. SIGHUP and
			// stop branches stay unaffected — they don't cold-start.
			if shouldRun != nil && !shouldRun() {
				o.log("info", "orchestrator: cold-start suppressed by manual-stop intent")
				break
			}
			o.log("info", "orchestrator: starting sing-box (active slots present)")
			err = proc.Start()
		case needRunning && running:
			if newHasTun != prevHasTun {
				// sing-box cannot add/remove a tun inbound via SIGHUP — the
				// tun device never gets carrier and readiness times out. A
				// presence toggle therefore requires a full restart.
				o.log("info", "orchestrator: restarting sing-box (tun inbound toggled)")
				if e := proc.Stop(); e != nil {
					o.log("warn", "orchestrator: stop before tun-restart: "+e.Error())
				}
				err = proc.Start()
			} else {
				o.log("info", "orchestrator: SIGHUP sing-box (config changed)")
				err = proc.Reload()
			}
		case !needRunning && running:
			o.log("info", "orchestrator: stopping sing-box (no active slots)")
			err = proc.Stop()
		default:
			// !needRunning && !running — nothing to do.
		}
	}

	o.mu.Lock()
	o.reloading = false
	// Record the tun presence of the config we just applied so the next
	// Reload compares against reality. Updated for every apply branch
	// (start / SIGHUP / restart / stop): after a stop newHasTun is false
	// anyway, after a fresh start prevHasTun == newHasTun.
	o.prevHasTun = newHasTun
	o.mu.Unlock()
	return err
}

// hasActiveWorkLocked reports whether sing-box has anything to do
// beyond hosting base + catalog slots. Two activation paths:
//
//   - any non-AlwaysOn slot is enabled (router / deviceproxy /
//     subscriptions) — its mere presence is the signal;
//   - an AlwaysOn slot whose meta.HasContent returns true (currently
//     SlotTunnels, when the user has defined at least one sing-box
//     tunnel).
//
// AlwaysOn catalog slots without HasContent (SlotBase, SlotAwg) never
// activate the daemon on their own — they are infrastructure for
// other slots, not a reason to keep sing-box running.
//
// Caller MUST hold o.mu.
func (o *Orchestrator) hasActiveWorkLocked() bool {
	for slot, enabled := range o.enabled {
		if !enabled {
			continue
		}
		meta, ok := o.slots[slot]
		if !ok {
			continue
		}
		if meta.AlwaysOn {
			if meta.HasContent != nil && meta.HasContent() {
				return true
			}
			continue
		}
		return true
	}
	return false
}

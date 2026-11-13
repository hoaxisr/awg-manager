// Periodic WAN monitoring and its interaction with capture and decisions.
package engine

import (
	"context"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probe"
)

// startMonitorLocked launches the periodic WAN check: the first check runs
// immediately, then every monitorInterval. Disabled by default.
func (e *Engine) startMonitorLocked() {
	ctx, cancel := context.WithCancel(e.baseCtx)
	e.monitorCancel = cancel
	e.monitor.Add(1)
	go func() {
		defer e.monitor.Done()
		e.logf("WAN monitor started")
		ticker := time.NewTicker(monitorInterval)
		defer ticker.Stop()
		for {
			e.monitorCheck(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (e *Engine) stopMonitorLocked() {
	if e.monitorCancel != nil {
		e.monitorCancel()
		e.monitorCancel = nil
	}
}

// monitorCheck performs one periodic WAN check and applies the transition:
// a non-reachable WAN pauses the capture, invalidates in-flight decisions and
// blocks commits; the first reachable check resumes the capture with clean
// flow state. Transitions are logged once per state change, never every 30
// seconds. A cancelled monitor (disabled or stopped) applies nothing. Lock
// order is commitMu before mu everywhere.
func (e *Engine) monitorCheck(ctx context.Context) {
	url := e.deps.Config.Config().WANCheckURL
	var result probe.Result
	path, err := e.deps.Routes.Resolve(ctx)
	if err != nil {
		result = probe.Result{Verdict: model.VerdictError, Detail: "direct path: " + err.Error()}
	} else {
		result = e.deps.Probes.CheckWAN(ctx, url, probe.Path{Interface: path.Interface})
	}
	if ctx.Err() != nil {
		return // the monitor was disabled or the engine stopped meanwhile
	}
	evidence := model.CheckEvidence{
		Verdict:    result.Verdict,
		DurationMS: result.Duration.Milliseconds(),
		Detail:     result.Detail,
		CheckedAt:  time.Now(),
	}

	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	wasPaused := e.capturePaused
	e.lastWAN = evidence
	if result.Verdict == model.VerdictReachable {
		if wasPaused {
			e.resumeCaptureLocked("WAN reachable again")
		}
		e.setLastErrorLocked("")
	} else {
		if !wasPaused {
			e.pauseCaptureLocked("WAN " + string(result.Verdict))
		}
		e.setLastErrorLocked("WAN " + string(result.Verdict) + ": " + result.Detail)
	}
}

// pauseCaptureLocked pauses the capture and clears the queue; deferred
// suspects from before the pause are stale and must not create decisions.
// In-flight decisions are invalidated with commitMu already held.
func (e *Engine) pauseCaptureLocked(reason string) {
	e.logf("WAN monitor: capture paused: %s", reason)
	if e.capture != nil {
		if err := e.capture.Pause(); err != nil {
			e.setLastErrorLocked("capture pause: " + err.Error())
		}
	}
	e.captureRunning = false
	e.capturePaused = true
	e.drainQueueLocked()
	e.invalidateLocked("WAN unavailable")
}

// resumeCaptureLocked resumes the capture after a clean pause; the fresh
// capture generation starts with empty flow state, so old packets cannot
// create candidates.
func (e *Engine) resumeCaptureLocked(reason string) {
	e.logf("WAN monitor: capture resumed: %s", reason)
	if e.capture != nil {
		if err := e.capture.Start(e.baseCtx); err != nil {
			e.setLastErrorLocked("capture resume: " + err.Error())
			return
		}
	}
	e.captureRunning = true
	e.capturePaused = false
	if strings.HasPrefix(e.lastError, "WAN ") {
		e.lastError = ""
	}
}

// drainQueueLocked empties the bounded queue. Called with mu held.
func (e *Engine) drainQueueLocked() {
	for {
		select {
		case <-e.queue:
		default:
			e.queued = make(map[string]struct{})
			return
		}
	}
}

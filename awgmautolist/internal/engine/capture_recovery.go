package engine

import (
	"strings"
	"time"
)

// captureRecoveryInterval is short enough to recover an AF_PACKET generation
// without making a persistent failure spin. Tests may shorten it.
var captureRecoveryInterval = 2 * time.Second

// CaptureError is the engine-side error hook used by the real capture factory.
func (e *Engine) CaptureError(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	e.captureRunning = false
	e.lastError = "capture read: " + err.Error()
	e.mu.Unlock()
}

func (e *Engine) superviseCapture() {
	defer e.captureWatch.Done()
	ticker := time.NewTicker(captureRecoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.baseCtx.Done():
			return
		case <-ticker.C:
			e.recoverCapture()
		}
	}
}

func (e *Engine) recoverCapture() {
	e.commitMu.Lock()
	e.mu.Lock()
	if !e.running || e.capturePaused || e.capture == nil {
		e.mu.Unlock()
		e.commitMu.Unlock()
		return
	}
	captureRef := e.capture
	gen := e.captureGen
	baseCtx := e.baseCtx
	if captureRef.Status().Running {
		e.captureRunning = true
		e.mu.Unlock()
		e.commitMu.Unlock()
		return
	}
	e.captureRunning = false
	e.mu.Unlock()
	e.commitMu.Unlock()

	// No engine locks are held while Start creates a fresh generation. The
	// mutex serializes against the monitor pause/resume and the UDP-toggle
	// stop; if one of those is in flight, this tick yields and retries.
	if !captureOpMu.TryLock() {
		return
	}
	defer captureOpMu.Unlock()
	// Re-check under the locks: while Start was being prepared, the decision
	// may have gone stale (a newer generation or a pause in between).
	e.commitMu.Lock()
	e.mu.Lock()
	stale := gen != e.captureGen || e.capturePaused || !e.running || e.capture != captureRef
	e.mu.Unlock()
	e.commitMu.Unlock()
	if stale {
		return
	}

	// If the previous generation is still closing, the next tick retries
	// instead of treating that transient state as permanent.
	err := captureRef.Start(baseCtx)
	e.mu.Lock()
	e.captureRunning = err == nil
	if err != nil {
		e.lastError = "capture restart: " + err.Error()
	} else if strings.HasPrefix(e.lastError, "capture ") {
		e.lastError = ""
	}
	e.mu.Unlock()
}

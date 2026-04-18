package command

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// Poster is the minimum surface SaveCoordinator needs from the NDMS
// transport. Real implementations use *transport.Client.
type Poster interface {
	Post(ctx context.Context, payload any) (json.RawMessage, error)
}

// savePayload is the NDMS command for "persist running-config to flash".
var savePayload = map[string]any{"save": true}

// SaveCoordinator debounces flash-write Save requests into a single POST
// per burst. See design spec §5.2-5.3.
type SaveCoordinator struct {
	poster     Poster
	publisher  StatusPublisher
	debounce   time.Duration
	maxWait    time.Duration
	retryDelay time.Duration
	maxRetries int

	mu              sync.Mutex
	timer           *time.Timer
	firstAt         time.Time // zero if no pending batch
	pendingCount    int
	state           SaveState
	lastError       string
	lastSaveAt      time.Time
	retryCount      int // consecutive failures in current batch
	flushInProgress bool
	saveMu          sync.Mutex
}

const (
	defaultRetryDelay = 5 * time.Second
	defaultMaxRetries = 3
)

// NewSaveCoordinator constructs a coordinator with production defaults.
// debounce — delay before firing Save after the last Request().
// maxWait  — hard ceiling from first Request() in the current batch.
// Retries: 3 attempts 5 seconds apart after a failed fire.
func NewSaveCoordinator(poster Poster, pub StatusPublisher, debounce, maxWait time.Duration) *SaveCoordinator {
	return &SaveCoordinator{
		poster:     poster,
		publisher:  pub,
		debounce:   debounce,
		maxWait:    maxWait,
		retryDelay: defaultRetryDelay,
		maxRetries: defaultMaxRetries,
		state:      SaveStateIdle,
	}
}

// SetRetryPolicy overrides the retry delay and max retries — used by tests
// that need sub-second timings.
func (s *SaveCoordinator) SetRetryPolicy(delay time.Duration, maxRetries int) {
	s.mu.Lock()
	s.retryDelay = delay
	s.maxRetries = maxRetries
	s.mu.Unlock()
}

// Request schedules a debounced Save. Non-blocking.
func (s *SaveCoordinator) Request() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if s.firstAt.IsZero() {
		s.firstAt = now
	}
	s.pendingCount++

	fireAt := now.Add(s.debounce)
	maxFireAt := s.firstAt.Add(s.maxWait)
	if fireAt.After(maxFireAt) {
		fireAt = maxFireAt
	}

	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(fireAt.Sub(now), s.fire)

	s.setStateLocked(SaveStatePending, "")
}

// fire runs on the timer goroutine. Performs the Save POST, publishes
// status transitions, and schedules a retry on failure.
//
// Race with Flush: timer.Stop() in Flush returns false if fire has
// already been dispatched. We guard with flushInProgress — fire yields
// its work to Flush rather than racing two Save POSTs and clobbering
// the state machine.
func (s *SaveCoordinator) fire() {
	s.mu.Lock()
	// Clear the timer/firstAt so a new Request() starts a fresh batch.
	// pendingCount is intentionally preserved so the SSE status reflects
	// how many mutations accumulated since the last successful Save.
	s.timer = nil
	s.firstAt = time.Time{}
	if s.flushInProgress {
		s.mu.Unlock()
		return
	}
	s.setStateLocked(SaveStateSaving, "")
	s.mu.Unlock()

	// Serialise concurrent Save POSTs.
	s.saveMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err := s.poster.Post(ctx, savePayload)
	cancel()
	s.saveMu.Unlock()

	s.mu.Lock()
	// Flush may have started while we were POSTing. If so, Flush owns
	// the state transition — don't step on it.
	if s.flushInProgress {
		s.mu.Unlock()
		return
	}
	if err == nil {
		s.pendingCount = 0
		s.retryCount = 0
		s.lastSaveAt = time.Now()
		s.setStateLocked(SaveStateIdle, "")
		s.mu.Unlock()
		return
	}

	s.retryCount++
	if s.retryCount > s.maxRetries {
		s.setStateLocked(SaveStateFailed, err.Error())
		s.mu.Unlock()
		return
	}
	s.setStateLocked(SaveStateError, err.Error())
	// Schedule retry — fresh fire after retryDelay.
	s.timer = time.AfterFunc(s.retryDelay, s.fire)
	s.mu.Unlock()
}

// Flush runs Save synchronously, bypassing debounce. Called on graceful
// shutdown and by the UI "Retry save" button. Clears Failed state on
// success. On failure, transitions directly to SaveStateFailed — Flush is
// itself the explicit retry, so there is no point in scheduling another.
// Returns the underlying error (nil on success).
func (s *SaveCoordinator) Flush(ctx context.Context) error {
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.firstAt = time.Time{}
	// Claim exclusive state ownership — any fire() that slipped past
	// timer.Stop() will see this flag and yield.
	s.flushInProgress = true
	s.setStateLocked(SaveStateSaving, "")
	s.mu.Unlock()

	// saveMu serialises against any fire() POST already in flight.
	s.saveMu.Lock()
	_, err := s.poster.Post(ctx, savePayload)
	s.saveMu.Unlock()

	s.mu.Lock()
	s.flushInProgress = false
	if err == nil {
		s.pendingCount = 0
		s.retryCount = 0
		s.lastSaveAt = time.Now()
		s.setStateLocked(SaveStateIdle, "")
	} else {
		// Flush IS the explicit retry — failure is terminal, go
		// straight to Failed. Mark retry budget exhausted.
		s.retryCount = s.maxRetries + 1
		s.setStateLocked(SaveStateFailed, err.Error())
	}
	s.mu.Unlock()
	return err
}

// setStateLocked updates state + publishes SSE. Must be called with mu held.
func (s *SaveCoordinator) setStateLocked(next SaveState, errMsg string) {
	s.state = next
	s.lastError = errMsg
	if s.publisher != nil {
		s.publisher.Publish("save:status", events.SaveStatusEvent{
			State:        next.String(),
			LastError:    errMsg,
			LastSaveAt:   s.lastSaveAt,
			PendingCount: s.pendingCount,
		})
	}
}

// Status returns a snapshot of the current SaveStatus. Intended for
// inclusion in the SSE reconnect snapshot so clients that open mid-save
// still see the right indicator.
func (s *SaveCoordinator) Status() SaveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SaveStatus{
		State:        s.state,
		LastError:    s.lastError,
		LastSaveAt:   s.lastSaveAt,
		PendingCount: s.pendingCount,
	}
}

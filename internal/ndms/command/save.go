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

// PostSaveInvalidator is the minimal cache-invalidation surface
// SaveCoordinator needs after a successful save. RunningConfigStore
// from internal/ndms/query satisfies it via the embedded
// *cache.ListStore[T].InvalidateAll() promoted method.
//
// The interface is declared here (not in query/) to keep
// SaveCoordinator's package free of an import cycle with query/.
type PostSaveInvalidator interface {
	InvalidateAll()
}

// savePayload is the NDMS command for "persist running-config to flash".
// Matches the exact shape Keenetic's own web UI uses:
//
//	{"system":{"configuration":{"save":{}}}}
//
// (This is `system configuration save` in ndmc CLI form.)
// The previous shorthand {"save": true} was not a valid RCI path and
// silently no-oped on OS5 — changes survived the session but certain
// state fields (e.g. dns-proxy.route "disable" flag) never made it
// into `/show/sc/...` views that the router UI reads from.
var savePayload = map[string]any{
	"system": map[string]any{
		"configuration": map[string]any{
			"save": map[string]any{},
		},
	},
}

// SaveCoordinator debounces flash-write Save requests into a single POST
// per burst. See design spec §5.2-5.3.
type SaveCoordinator struct {
	poster     Poster
	publisher  StatusPublisher
	debounce   time.Duration
	maxWait    time.Duration
	retryDelay time.Duration
	maxRetries int

	settleDelay time.Duration
	invalidator PostSaveInvalidator

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
// debounce    — delay before firing Save after the last Request().
// maxWait     — hard ceiling from first Request() in the current batch.
// settleDelay — pause after a successful save before invalidating the
//
//	RunningConfig cache. NDMS publishes running-config
//	asynchronously after writing flash, so reads issued
//	immediately after save would still see the old view.
//	Pass 0 to disable settle entirely (skip both sleep and
//	invalidate).
//
// invalidator — cache surface invalidated after settle. Pass nil to
//
//	disable settle (sleep is still skipped). Typically
//	wired to query.Queries.RunningConfig.
//
// Retries: 3 attempts 5 seconds apart after a failed fire.
func NewSaveCoordinator(
	poster Poster,
	pub StatusPublisher,
	debounce, maxWait, settleDelay time.Duration,
	invalidator PostSaveInvalidator,
) *SaveCoordinator {
	return &SaveCoordinator{
		poster:      poster,
		publisher:   pub,
		debounce:    debounce,
		maxWait:     maxWait,
		retryDelay:  defaultRetryDelay,
		maxRetries:  defaultMaxRetries,
		settleDelay: settleDelay,
		invalidator: invalidator,
		state:       SaveStateIdle,
	}
}

// SetSettleDelay overrides the post-save settle delay — used by tests
// that need sub-second timings. Mirrors the SetRetryPolicy pattern.
func (s *SaveCoordinator) SetSettleDelay(d time.Duration) {
	s.mu.Lock()
	s.settleDelay = d
	s.mu.Unlock()
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
		// Snapshot settle deps under the lock so SetSettleDelay races
		// can't tear our view of (delay, invalidator).
		settleDelay := s.settleDelay
		invalidator := s.invalidator
		s.mu.Unlock()

		// Post-save settle (outside all mutexes — semaphore slot already
		// released by postJSON's defer above): wait for NDMS to publish
		// the updated running-config view, then invalidate the cache so
		// the next reader gets fresh data.
		if settleDelay > 0 && invalidator != nil {
			time.Sleep(settleDelay)
			invalidator.InvalidateAll()
			events.PublishInvalidatedTo(s.publisher, events.ResourceSaveStatus, "save-settled")
		}
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
	successful := err == nil
	if successful {
		s.pendingCount = 0
		s.retryCount = 0
		s.lastSaveAt = time.Now()
		s.setStateLocked(SaveStateIdle, "")
	} else {
		// Flush IS the explicit retry — failure is terminal, go straight
		// to Failed. Mark retry budget exhausted.
		s.retryCount = s.maxRetries + 1
		s.setStateLocked(SaveStateFailed, err.Error())
	}
	invalidator := s.invalidator
	s.mu.Unlock()

	// On Flush — invalidate WITHOUT sleep. Flush is a terminal operation
	// (shutdown or explicit UI retry); callers either don't read after
	// (shutdown) or are already waiting on a UI spinner (retry button).
	if successful && invalidator != nil {
		invalidator.InvalidateAll()
	}
	return err
}

// setStateLocked updates state + publishes a resource:invalidated hint.
// Must be called with mu held.
//
// Before the state-sync redesign (Task 13) this published the full
// SaveStatus as a "save:status" SSE event; the payload is now fetched
// on-demand via GET /api/ndms/save-status by a polling store. Emitting
// just the hint keeps the save indicator reactive without pushing full
// state over SSE.
func (s *SaveCoordinator) setStateLocked(next SaveState, errMsg string) {
	s.state = next
	s.lastError = errMsg
	events.PublishInvalidatedTo(s.publisher, events.ResourceSaveStatus, "state-change")
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

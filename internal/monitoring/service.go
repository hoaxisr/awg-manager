package monitoring

import (
	"context"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// Service is the public facade — the rest of the codebase depends on this
// type, not on Scheduler/History directly.
type Service struct {
	scheduler *Scheduler
	history   *History

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewService wires Scheduler + History.
func NewService(deps SchedulerDeps) *Service {
	hist := NewHistory()
	sched := NewScheduler(deps, hist)
	return &Service{scheduler: sched, history: hist}
}

// Start begins probing in the background. The provided ctx scopes the
// goroutine lifetime; Stop() also halts it.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	scopedCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.scheduler.Start(scopedCtx)
}

// Stop halts the scheduler.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
	s.scheduler.Stop()
}

// SetEventBus delegates to the scheduler so SSE events get published.
func (s *Service) SetEventBus(bus *events.Bus) {
	s.scheduler.SetEventBus(bus)
}

// Scheduler exposes the underlying scheduler so collaborators (e.g. the
// connectivity Monitor) can request an immediate matrix tick without
// importing internal types.
func (s *Service) Scheduler() *Scheduler {
	return s.scheduler
}

// Snapshot returns the most-recent matrix snapshot.
func (s *Service) Snapshot() Snapshot {
	return s.scheduler.LatestSnapshot()
}

// History returns recent samples for (targetID, tunnelID), bounded by limit.
func (s *Service) History(targetID, tunnelID string, limit int) []Sample {
	return s.history.Get(targetID, tunnelID, limit)
}

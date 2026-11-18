package serverwizard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const DefaultPlanTTL = 10 * time.Minute

// PlanStore manages server-side plan records with TTL and single-use guarantees.
type PlanStore struct {
	mu             sync.RWMutex
	plans          map[string]*ServerPlanRecord
	clock          func() time.Time
	markConsumedFn func(planID, jobID string) (*ServerPlanRecord, error)
}

func NewPlanStore() *PlanStore {
	return &PlanStore{
		plans: make(map[string]*ServerPlanRecord),
		clock: time.Now,
	}
}

func (s *PlanStore) generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "plan-" + hex.EncodeToString(b)
}

// SavePlan generates a new server-side plan record bound to the caller's session.
func (s *PlanStore) SavePlan(
	req WizardPlanRequest,
	desired DesiredWizardConfig,
	sessionID string,
	plan ChangePlan,
	fingerprint string,
	ttl time.Duration,
) *ServerPlanRecord {
	if ttl <= 0 {
		ttl = DefaultPlanTTL
	}
	now := s.clock()
	planID := s.generateID()

	rec := &ServerPlanRecord{
		PlanID:           planID,
		Kind:             req.Kind,
		SessionID:        sessionID,
		Request:          req,
		Desired:          desired,
		Plan:             plan,
		StateFingerprint: fingerprint,
		ExpiresAt:        now.Add(ttl),
		State:            PlanStateAvailable,
		Used:             false,
		CreatedAt:        now,
	}

	s.mu.Lock()
	s.plans[planID] = rec
	s.mu.Unlock()

	return rec
}

// GetPlan retrieves a stored plan, validating session match and expiration.
func (s *PlanStore) GetPlan(planID, sessionID string) (*ServerPlanRecord, error) {
	s.mu.RLock()
	rec, ok := s.plans[planID]
	s.mu.RUnlock()

	if !ok {
		return nil, ErrPlanNotFound
	}

	now := s.clock()
	if now.After(rec.ExpiresAt) {
		return nil, ErrPlanExpired
	}

	if sessionID != "" && rec.SessionID != "" && rec.SessionID != sessionID {
		return nil, fmt.Errorf("%w: plan belongs to another session", ErrUnauthorized)
	}

	return rec, nil
}

// Reserve atomically transitions an available plan to reserved for jobID.
func (s *PlanStore) Reserve(planID, sessionID, jobID string) (*ServerPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.plans[planID]
	if !ok {
		return nil, ErrPlanNotFound
	}

	now := s.clock()
	if now.After(rec.ExpiresAt) {
		return nil, ErrPlanExpired
	}

	if sessionID != "" && rec.SessionID != "" && rec.SessionID != sessionID {
		return nil, fmt.Errorf("%w: plan belongs to another session", ErrUnauthorized)
	}

	if rec.State == PlanStateConsumed || rec.Used {
		return nil, ErrPlanAlreadyUsed
	}

	if rec.State == PlanStateReserved {
		if rec.ReservedJobID == jobID {
			return rec, nil
		}
		return nil, fmt.Errorf("%w: plan already reserved by another job", ErrOperationInProgress)
	}

	rec.State = PlanStateReserved
	rec.ReservedJobID = jobID
	return rec, nil
}

// Release releases a reservation if the job failed before committing.
func (s *PlanStore) Release(planID, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.plans[planID]
	if !ok {
		return nil
	}
	if rec.State == PlanStateReserved && (rec.ReservedJobID == jobID || jobID == "") {
		rec.State = PlanStateAvailable
		rec.ReservedJobID = ""
	}
	return nil
}

// SetMarkConsumedSeamForTest configures a custom MarkConsumed implementation for testing.
func (s *PlanStore) SetMarkConsumedSeamForTest(fn func(planID, jobID string) (*ServerPlanRecord, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.markConsumedFn = fn
}

// MarkConsumed marks the plan as permanently consumed.
// It verifies reservation ownership and is idempotent for repeated calls by the same job.
func (s *PlanStore) MarkConsumed(planID, jobID string) (*ServerPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.markConsumedFn != nil {
		return s.markConsumedFn(planID, jobID)
	}

	rec, ok := s.plans[planID]
	if !ok {
		return nil, ErrPlanNotFound
	}

	if rec.State == PlanStateConsumed {
		if (rec.ConsumedJobID != "" && rec.ConsumedJobID == jobID) || (rec.ReservedJobID != "" && rec.ReservedJobID == jobID) {
			return rec, nil
		}
		return nil, ErrPlanAlreadyUsed
	}

	if rec.State == PlanStateAvailable {
		return nil, fmt.Errorf("%w: plan %s is available, must be reserved first", ErrPlanInvalidState, planID)
	}

	if rec.State == PlanStateReserved {
		if rec.ReservedJobID != jobID {
			return nil, fmt.Errorf("%w: plan %s is reserved by job %s, not %s", ErrOperationInProgress, planID, rec.ReservedJobID, jobID)
		}
		rec.State = PlanStateConsumed
		rec.ConsumedJobID = jobID
		rec.Used = true
		return rec, nil
	}

	return nil, fmt.Errorf("%w: unknown plan state %s", ErrPlanInvalidState, rec.State)
}

// MarkUsed marks a plan as used to prevent duplicate executions.
func (s *PlanStore) MarkUsed(planID, sessionID string) (*ServerPlanRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, ok := s.plans[planID]
	if !ok {
		return nil, ErrPlanNotFound
	}

	now := s.clock()
	if now.After(rec.ExpiresAt) {
		return nil, ErrPlanExpired
	}

	if sessionID != "" && rec.SessionID != "" && rec.SessionID != sessionID {
		return nil, fmt.Errorf("%w: plan belongs to another session", ErrUnauthorized)
	}

	if rec.Used || rec.State == PlanStateConsumed {
		return nil, ErrPlanAlreadyUsed
	}

	rec.Used = true
	rec.State = PlanStateConsumed
	return rec, nil
}

// PruneExpired purges expired plan records.
func (s *PlanStore) PruneExpired() {
	now := s.clock()
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, rec := range s.plans {
		if now.After(rec.ExpiresAt) {
			delete(s.plans, id)
		}
	}
}

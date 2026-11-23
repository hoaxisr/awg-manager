package serverwizard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrJobNotFound     = errors.New("job not found")
	ErrJobUnauthorized = errors.New("unauthorized to access job")
	ErrCannotCancel    = errors.New("job cannot be cancelled in its current phase")
	ErrRateLimited     = errors.New("rate limit exceeded: max 5 requests per minute")
	ErrSecretsExpired  = errors.New("credentials have expired or were not found")
)

const (
	SecretTTL            = 15 * time.Minute
	RevealRateLimitCount = 5
	RevealRateLimitWin   = 1 * time.Minute
)

// InternalJob tracks the in-memory state of an asynchronous wizard operation.
type InternalJob struct {
	mu              sync.RWMutex
	ID              string
	Kind            string
	SessionID       string
	Phase           JobPhase
	Progress        int
	CurrentStep     string
	Error           string
	ErrorCode       string
	ResultAvailable bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
	cancelFn        context.CancelFunc
	cancelRequested bool
}

func (j *InternalJob) ToResponse() JobStatusResponse {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return JobStatusResponse{
		ID:              j.ID,
		Kind:            j.Kind,
		Phase:           j.Phase,
		Progress:        j.Progress,
		CurrentStep:     j.CurrentStep,
		Error:           j.Error,
		ErrorCode:       j.ErrorCode,
		ResultAvailable: j.ResultAvailable,
		CreatedAt:       j.CreatedAt,
		UpdatedAt:       j.UpdatedAt,
	}
}

type secretRecord struct {
	SessionID string
	Creds     RevealCredentials
	ExpiresAt time.Time
}

// JobRunner coordinates job tracking, cancellation, and isolated secret exposure.
type JobRunner struct {
	jobsMu    sync.RWMutex
	jobs      map[string]*InternalJob
	secretsMu sync.RWMutex
	secrets   map[string]secretRecord
	limitsMu  sync.Mutex
	limits    map[string][]time.Time // sessionID -> timestamps
	clock     func() time.Time
}

func NewJobRunner() *JobRunner {
	jr := &JobRunner{
		jobs:    make(map[string]*InternalJob),
		secrets: make(map[string]secretRecord),
		limits:  make(map[string][]time.Time),
		clock:   time.Now,
	}
	return jr
}

func (jr *JobRunner) generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateJobID generates a unique wizard job ID.
func (jr *JobRunner) GenerateJobID() string {
	return "wizjob-" + jr.generateID()
}

// CreateJobWithID registers a new wizard apply job with a predetermined ID.
func (jr *JobRunner) CreateJobWithID(id, kind, sessionID string, cancelFn context.CancelFunc) *InternalJob {
	// Чистка ленивая, на каждом новом задании: иначе завершённые задания и
	// выданные учётные данные копились бы в памяти до перезапуска демона.
	jr.PruneExpired()
	now := jr.clock()

	job := &InternalJob{
		ID:          id,
		Kind:        kind,
		SessionID:   sessionID,
		Phase:       JobPhasePending,
		Progress:    0,
		CurrentStep: "Задача поставлена в очередь",
		CreatedAt:   now,
		UpdatedAt:   now,
		cancelFn:    cancelFn,
	}

	jr.jobsMu.Lock()
	jr.jobs[id] = job
	jr.jobsMu.Unlock()

	return job
}

// CreateJob registers a new wizard apply job.
func (jr *JobRunner) CreateJob(kind, sessionID string, cancelFn context.CancelFunc) *InternalJob {
	return jr.CreateJobWithID(jr.GenerateJobID(), kind, sessionID, cancelFn)
}

// GetJob returns safe status for a job, verifying session ownership.
func (jr *JobRunner) GetJob(jobID, sessionID string) (JobStatusResponse, error) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return JobStatusResponse{}, ErrJobNotFound
	}

	job.mu.RLock()
	defer job.mu.RUnlock()

	if sessionID != "" && job.SessionID != "" && job.SessionID != sessionID {
		return JobStatusResponse{}, ErrJobUnauthorized
	}

	return JobStatusResponse{
		ID:              job.ID,
		Kind:            job.Kind,
		Phase:           job.Phase,
		Progress:        job.Progress,
		CurrentStep:     job.CurrentStep,
		Error:           job.Error,
		ErrorCode:       job.ErrorCode,
		ResultAvailable: job.ResultAvailable,
		CreatedAt:       job.CreatedAt,
		UpdatedAt:       job.UpdatedAt,
	}, nil
}

// UpdateStep updates the current step and progress percentage.
func (jr *JobRunner) UpdateStep(jobID string, phase JobPhase, progress int, step string) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Phase = phase
	job.Progress = progress
	job.CurrentStep = step
	job.UpdatedAt = jr.clock()
}

// SetPhaseNonCancellable transitions the job to a non-cancellable phase (e.g. JobPhaseCommitting)
// and detaches the cancel function.
func (jr *JobRunner) SetPhaseNonCancellable(jobID string, phase JobPhase, progress int, step string) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Phase = phase
	job.Progress = progress
	job.CurrentStep = step
	job.UpdatedAt = jr.clock()
	job.cancelFn = nil
}

// FailJob marks a job as failed.
func (jr *JobRunner) FailJob(jobID, errCode, errMsg string) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Phase = JobPhaseFailed
	job.ErrorCode = errCode
	job.Error = errMsg
	job.Progress = 100
	job.CurrentStep = "Применение настроек завершилось ошибкой"
	job.UpdatedAt = jr.clock()
}

// MarkRecoveryRequired marks a job with unrecoverable error requiring manual recovery.
func (jr *JobRunner) MarkRecoveryRequired(jobID, reason string) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	job.Phase = JobPhaseRecoveryRequired
	job.ErrorCode = ErrCodeRecoveryRequired
	job.Error = reason
	job.Progress = 100
	job.CurrentStep = "Требуется ручное восстановление конфигурации"
	job.UpdatedAt = jr.clock()
	job.cancelFn = nil
}

// SucceedJob marks a job as successful and securely stores credentials for reveal.
func (jr *JobRunner) SucceedJob(jobID string, creds *RevealCredentials) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return
	}

	now := jr.clock()

	job.mu.Lock()
	job.Phase = JobPhaseSucceeded
	job.Progress = 100
	job.CurrentStep = "Конфигурация успешно применена и проверена"
	job.ResultAvailable = creds != nil
	job.UpdatedAt = now
	sessionID := job.SessionID
	job.mu.Unlock()

	if creds != nil {
		jr.secretsMu.Lock()
		jr.secrets[jobID] = secretRecord{
			SessionID: sessionID,
			Creds:     *creds,
			ExpiresAt: now.Add(SecretTTL),
		}
		jr.secretsMu.Unlock()
	}
}

// CancelJob attempts to cancel a job before the point of no return (committing).
func (jr *JobRunner) CancelJob(jobID, sessionID string) error {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()

	if !ok {
		return ErrJobNotFound
	}

	job.mu.Lock()
	defer job.mu.Unlock()

	if sessionID != "" && job.SessionID != "" && job.SessionID != sessionID {
		return ErrJobUnauthorized
	}

	switch job.Phase {
	case JobPhaseSucceeded, JobPhaseFailed, JobPhaseCancelled, JobPhaseRecoveryRequired:
		return fmt.Errorf("%w: job is already %s", ErrCannotCancel, job.Phase)
	case JobPhaseCommitting:
		// Point of no return: commit is underway and must complete to prevent split-brain
		job.cancelRequested = true
		return fmt.Errorf("%w: job is already committing, commit will finalize", ErrCannotCancel)
	case JobPhaseCancelling, JobPhaseRollingBack:
		return nil // already cancelling
	default:
		// Safe to cancel
		job.cancelRequested = true
		job.Phase = JobPhaseCancelling
		job.CurrentStep = "Отмена операции..."
		job.UpdatedAt = jr.clock()
		if job.cancelFn != nil {
			job.cancelFn()
		}
		return nil
	}
}

// IsCancelRequested checks whether a cancellation has been requested.
func (jr *JobRunner) IsCancelRequested(jobID string) bool {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()
	if !ok {
		return false
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	return job.cancelRequested
}

// MarkCancelled transitions job from cancelling to cancelled.
func (jr *JobRunner) MarkCancelled(jobID string) {
	jr.jobsMu.RLock()
	job, ok := jr.jobs[jobID]
	jr.jobsMu.RUnlock()
	if !ok {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Phase = JobPhaseCancelled
	job.CurrentStep = "Операция отменена пользователем"
	job.UpdatedAt = jr.clock()
}

// RevealSecrets exposes credentials for a completed job after enforcing rate limiting and session match.
func (jr *JobRunner) RevealSecrets(jobID, sessionID string) (*RevealCredentials, error) {
	now := jr.clock()

	// 1. Rate limiting check (5 per minute per session)
	if sessionID != "" {
		jr.limitsMu.Lock()
		timestamps := jr.limits[sessionID]
		validFrom := now.Add(-RevealRateLimitWin)
		var recent []time.Time
		for _, ts := range timestamps {
			if ts.After(validFrom) {
				recent = append(recent, ts)
			}
		}
		if len(recent) >= RevealRateLimitCount {
			jr.limits[sessionID] = recent
			jr.limitsMu.Unlock()
			return nil, ErrRateLimited
		}
		recent = append(recent, now)
		jr.limits[sessionID] = recent
		jr.limitsMu.Unlock()
	}

	// 2. Secret lookup and session match
	jr.secretsMu.Lock()
	sec, ok := jr.secrets[jobID]
	if ok && now.After(sec.ExpiresAt) {
		delete(jr.secrets, jobID)
		ok = false
	}
	jr.secretsMu.Unlock()

	if !ok {
		return nil, ErrSecretsExpired
	}

	if sessionID != "" && sec.SessionID != "" && sec.SessionID != sessionID {
		return nil, ErrJobUnauthorized
	}

	credsCopy := sec.Creds
	return &credsCopy, nil
}

// PruneExpired cleans up old secrets and finished jobs.
func (jr *JobRunner) PruneExpired() {
	now := jr.clock()

	jr.secretsMu.Lock()
	for id, sec := range jr.secrets {
		if now.After(sec.ExpiresAt) {
			delete(jr.secrets, id)
		}
	}
	jr.secretsMu.Unlock()

	jr.limitsMu.Lock()
	for sid, ts := range jr.limits {
		if len(ts) == 0 || !ts[len(ts)-1].After(now.Add(-RevealRateLimitWin)) {
			delete(jr.limits, sid)
		}
	}
	jr.limitsMu.Unlock()

	jr.jobsMu.Lock()
	for id, job := range jr.jobs {
		job.mu.RLock()
		isOld := now.Sub(job.UpdatedAt) > 1*time.Hour
		isDone := job.Phase == JobPhaseSucceeded || job.Phase == JobPhaseFailed || job.Phase == JobPhaseCancelled
		job.mu.RUnlock()
		if isDone && isOld {
			delete(jr.jobs, id)
		}
	}
	jr.jobsMu.Unlock()
}

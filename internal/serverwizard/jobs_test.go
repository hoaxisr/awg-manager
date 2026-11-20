package serverwizard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJobRunner_LifecycleAndSecretsIsolation(t *testing.T) {
	jr := NewJobRunner()
	sessionID := "sess-user-1"

	ctx, cancel := context.WithCancel(context.Background())
	job := jr.CreateJob("xray", sessionID, cancel)

	if job.Phase != JobPhasePending {
		t.Fatalf("expected pending phase, got %s", job.Phase)
	}

	// Verify safe status does not contain secrets
	st, err := jr.GetJob(job.ID, sessionID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if st.ResultAvailable {
		t.Errorf("result should not be available yet")
	}

	// Advance phases
	jr.UpdateStep(job.ID, JobPhasePreparing, 20, "Preparing candidate")
	jr.UpdateStep(job.ID, JobPhaseApplying, 40, "Applying configuration")
	jr.UpdateStep(job.ID, JobPhaseVerifying, 60, "Verifying listener")
	jr.UpdateStep(job.ID, JobPhaseCommitting, 80, "Committing changes")

	// Finish with credentials
	creds := &RevealCredentials{
		Kind:     "xray",
		VlessURL: "vless://user@example.com:443?security=none",
		UUID:     "abc-123",
	}
	jr.SucceedJob(job.ID, creds)

	st, err = jr.GetJob(job.ID, sessionID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if st.Phase != JobPhaseSucceeded || !st.ResultAvailable {
		t.Errorf("expected succeeded and resultAvailable=true, got %+v", st)
	}

	// Unauthorized session cannot get job
	_, err = jr.GetJob(job.ID, "sess-user-hacker")
	if !errors.Is(err, ErrJobUnauthorized) {
		t.Errorf("expected ErrJobUnauthorized, got %v", err)
	}

	// Unauthorized session cannot reveal secrets
	_, err = jr.RevealSecrets(job.ID, "sess-user-hacker")
	if !errors.Is(err, ErrJobUnauthorized) {
		t.Errorf("expected ErrJobUnauthorized on hacker reveal, got %v", err)
	}

	// Authorized session can reveal secrets
	revealed, err := jr.RevealSecrets(job.ID, sessionID)
	if err != nil {
		t.Fatalf("failed to reveal secrets: %v", err)
	}
	if revealed.VlessURL != creds.VlessURL || revealed.UUID != creds.UUID {
		t.Errorf("unexpected revealed creds: %+v", revealed)
	}
	_ = ctx
}

func TestJobRunner_CancellationRules(t *testing.T) {
	jr := NewJobRunner()
	sessionID := "sess-user-2"

	// 1. Cancel before committing
	cancelled := false
	cancelFn := func() { cancelled = true }
	job1 := jr.CreateJob("tgwebproxy", sessionID, cancelFn)
	jr.UpdateStep(job1.ID, JobPhaseApplying, 30, "Applying")

	err := jr.CancelJob(job1.ID, sessionID)
	if err != nil {
		t.Fatalf("expected cancel to succeed, got %v", err)
	}
	if !cancelled {
		t.Errorf("expected cancelFn to be executed")
	}

	// 2. Cancel during committing (point of no return)
	job2 := jr.CreateJob("tgwebproxy", sessionID, nil)
	jr.UpdateStep(job2.ID, JobPhaseCommitting, 80, "Committing")

	err = jr.CancelJob(job2.ID, sessionID)
	if !errors.Is(err, ErrCannotCancel) {
		t.Errorf("expected ErrCannotCancel during committing, got %v", err)
	}
	if !jr.IsCancelRequested(job2.ID) {
		t.Errorf("expected cancelRequested to be marked true")
	}
}

func TestJobRunner_RevealRateLimiting(t *testing.T) {
	jr := NewJobRunner()
	sessionID := "sess-rate-limit"

	job := jr.CreateJob("xray", sessionID, nil)
	creds := &RevealCredentials{Kind: "xray", UUID: "123"}
	jr.SucceedJob(job.ID, creds)

	// Call 5 times - should succeed
	for i := 0; i < RevealRateLimitCount; i++ {
		_, err := jr.RevealSecrets(job.ID, sessionID)
		if err != nil {
			t.Fatalf("call %d failed unexpectedly: %v", i+1, err)
		}
	}

	// 6th call should be rate limited
	_, err := jr.RevealSecrets(job.ID, sessionID)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on 6th call, got %v", err)
	}
}

func TestJobRunner_SecretExpiration(t *testing.T) {
	jr := NewJobRunner()
	simulatedTime := time.Now()
	jr.clock = func() time.Time { return simulatedTime }

	sessionID := "sess-exp"
	job := jr.CreateJob("xray", sessionID, nil)
	creds := &RevealCredentials{Kind: "xray", UUID: "exp-uuid"}
	jr.SucceedJob(job.ID, creds)

	// Before TTL: accessible
	_, err := jr.RevealSecrets(job.ID, sessionID)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	// Advance time past 15 min
	simulatedTime = simulatedTime.Add(16 * time.Minute)

	_, err = jr.RevealSecrets(job.ID, sessionID)
	if !errors.Is(err, ErrSecretsExpired) {
		t.Fatalf("expected ErrSecretsExpired, got %v", err)
	}
}

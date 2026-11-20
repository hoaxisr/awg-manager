package serverwizard

import (
	"errors"
	"testing"
	"time"
)

func TestPlanStore(t *testing.T) {
	store := NewPlanStore()
	simulatedTime := time.Now()
	store.clock = func() time.Time { return simulatedTime }

	sessionID := "sess-plan-1"
	req := WizardPlanRequest{Kind: "tgwebproxy", PublicDomain: "tg.example.com"}
	desired := DesiredWizardConfig{ServerKind: req.Kind, PublicHostname: req.PublicDomain}
	plan := ChangePlan{Summary: "Setup Telegram Web Proxy"}
	fp := "fp-abc-123"

	rec := store.SavePlan(req, desired, sessionID, plan, fp, 5*time.Minute)
	if rec.PlanID == "" || rec.Used {
		t.Fatalf("unexpected plan record: %+v", rec)
	}

	// 1. Successful lookup
	got, err := store.GetPlan(rec.PlanID, sessionID)
	if err != nil {
		t.Fatalf("failed to get plan: %v", err)
	}
	if got.StateFingerprint != fp {
		t.Errorf("expected fingerprint %s, got %s", fp, got.StateFingerprint)
	}

	// 2. Cross-session lookup rejected
	_, err = store.GetPlan(rec.PlanID, "other-session")
	if err == nil {
		t.Errorf("expected cross-session lookup to fail")
	}

	// 3. Mark used
	usedRec, err := store.MarkUsed(rec.PlanID, sessionID)
	if err != nil {
		t.Fatalf("failed to mark used: %v", err)
	}
	if !usedRec.Used {
		t.Errorf("expected Used=true")
	}

	// 4. Duplicate mark used rejected
	_, err = store.MarkUsed(rec.PlanID, sessionID)
	if !errors.Is(err, ErrPlanAlreadyUsed) {
		t.Errorf("expected ErrPlanAlreadyUsed on duplicate mark, got %v", err)
	}

	// 5. Expiration
	rec2 := store.SavePlan(req, desired, sessionID, plan, fp, 1*time.Minute)
	simulatedTime = simulatedTime.Add(2 * time.Minute)

	_, err = store.GetPlan(rec2.PlanID, sessionID)
	if !errors.Is(err, ErrPlanExpired) {
		t.Errorf("expected ErrPlanExpired, got %v", err)
	}
	_, err = store.MarkUsed(rec2.PlanID, sessionID)
	if !errors.Is(err, ErrPlanExpired) {
		t.Errorf("expected ErrPlanExpired on MarkUsed, got %v", err)
	}

	// 6. Prune
	store.PruneExpired()
	if len(store.plans) != 1 { // rec is still there (5min TTL, now +2min = 3min left), rec2 was pruned
		t.Errorf("expected 1 remaining plan after prune, got %d", len(store.plans))
	}
}

func TestPlanStore_MarkConsumed_OwnershipAndIdempotency(t *testing.T) {
	store := NewPlanStore()
	sessionID := "sess-own-1"
	req := WizardPlanRequest{Kind: "tgwebproxy", PublicDomain: "tg.example.com"}
	desired := DesiredWizardConfig{ServerKind: req.Kind, PublicHostname: req.PublicDomain}
	plan := ChangePlan{Summary: "Setup Telegram Web Proxy"}
	fp := "fp-own-123"

	rec := store.SavePlan(req, desired, sessionID, plan, fp, 5*time.Minute)

	// 1. Available plan cannot be consumed directly
	_, err := store.MarkConsumed(rec.PlanID, "job-1")
	if !errors.Is(err, ErrPlanInvalidState) {
		t.Fatalf("expected ErrPlanInvalidState when consuming available plan, got: %v", err)
	}

	// 2. Reserve for job-1
	reserved, err := store.Reserve(rec.PlanID, sessionID, "job-1")
	if err != nil {
		t.Fatalf("failed to reserve plan: %v", err)
	}
	if reserved.State != PlanStateReserved || reserved.ReservedJobID != "job-1" {
		t.Fatalf("unexpected reserved record: %+v", reserved)
	}

	// 3. Foreign job cannot consume reservation
	_, err = store.MarkConsumed(rec.PlanID, "job-2")
	if !errors.Is(err, ErrOperationInProgress) {
		t.Fatalf("expected ErrOperationInProgress when foreign job consumes, got: %v", err)
	}

	// 4. Correct owner consumes successfully
	consumed, err := store.MarkConsumed(rec.PlanID, "job-1")
	if err != nil {
		t.Fatalf("expected successful consume by owner, got: %v", err)
	}
	if consumed.State != PlanStateConsumed || consumed.ConsumedJobID != "job-1" || !consumed.Used {
		t.Fatalf("unexpected consumed record: %+v", consumed)
	}

	// 5. Repeated consume by same owner is idempotent
	consumed2, err := store.MarkConsumed(rec.PlanID, "job-1")
	if err != nil {
		t.Fatalf("expected idempotent consume by same owner, got: %v", err)
	}
	if consumed2.State != PlanStateConsumed || consumed2.ConsumedJobID != "job-1" {
		t.Fatalf("unexpected idempotent record: %+v", consumed2)
	}

	// 6. Foreign job consuming an already consumed plan fails with ErrPlanAlreadyUsed
	_, err = store.MarkConsumed(rec.PlanID, "job-2")
	if !errors.Is(err, ErrPlanAlreadyUsed) {
		t.Fatalf("expected ErrPlanAlreadyUsed when foreign job consumes already consumed plan, got: %v", err)
	}

	// 7. Test reservation release ownership
	rec2 := store.SavePlan(req, desired, sessionID, plan, fp, 5*time.Minute)
	_, err = store.Reserve(rec2.PlanID, sessionID, "job-3")
	if err != nil {
		t.Fatalf("failed to reserve rec2: %v", err)
	}
	// Wrong job release does not affect state
	_ = store.Release(rec2.PlanID, "job-wrong")
	checkRec, _ := store.GetPlan(rec2.PlanID, sessionID)
	if checkRec.State != PlanStateReserved {
		t.Errorf("expected plan to remain reserved after wrong job release")
	}
	// Owner release resets to available
	_ = store.Release(rec2.PlanID, "job-3")
	checkRec, _ = store.GetPlan(rec2.PlanID, sessionID)
	if checkRec.State != PlanStateAvailable || checkRec.ReservedJobID != "" {
		t.Errorf("expected plan to reset to available after owner release, got %+v", checkRec)
	}
}

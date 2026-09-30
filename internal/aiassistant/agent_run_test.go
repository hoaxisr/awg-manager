package aiassistant

import (
	"testing"
)

func TestAgentRunLifecycle(t *testing.T) {
	run := NewAgentRun("sing-box memory leak")

	if run.Status != AgentRunPending {
		t.Errorf("expected status 'pending', got %s", run.Status)
	}
	if run.Iteration != 1 {
		t.Errorf("expected iteration 1, got %d", run.Iteration)
	}

	run.AddLog("started diagnostics")
	if len(run.DiagLog) != 1 {
		t.Errorf("expected 1 log entry, got %d", len(run.DiagLog))
	}

	proposal := &ChangeProposal{
		ID:     "prop_1",
		Goal:   "Restart sing-box",
		Status: "pending",
	}
	run.AddProposal(proposal)

	if len(run.Proposals) != 1 {
		t.Errorf("expected 1 proposal, got %d", len(run.Proposals))
	}

	latest := run.GetLatestProposal()
	if latest.ID != "prop_1" {
		t.Errorf("expected latest proposal 'prop_1', got %s", latest.ID)
	}

	if run.CanRetry() {
		run.NextIteration()
	}

	if run.Iteration != 2 {
		t.Errorf("expected iteration 2, got %d", run.Iteration)
	}

	run.Complete(true, "fixed")
	if run.Status != AgentRunSuccess {
		t.Errorf("expected status 'success', got %s", run.Status)
	}
}

func TestAgentRunJournal(t *testing.T) {
	run := NewAgentRun("connection failure")
	run.AddJournal("diagnose", "analyzing iptables rules", "", "")
	run.AddJournal("apply", "restarting tunnel", "exit status 1", "success")

	if len(run.Journal) != 2 {
		t.Fatalf("expected 2 journal entries, got %d", len(run.Journal))
	}
	if run.Journal[1].Phase != "apply" || run.Journal[1].Error != "exit status 1" || run.Journal[1].Rollback != "success" {
		t.Fatalf("unexpected journal entry: %+v", run.Journal[1])
	}

	cloned := run.Clone()
	if len(cloned.Journal) != 2 {
		t.Fatalf("expected 2 cloned journal entries, got %d", len(cloned.Journal))
	}
}

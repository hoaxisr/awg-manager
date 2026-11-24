package serverwizard

import "testing"

// Конечная фаза задания мастера — подсказка карточкам серверов; промежуточные
// шаги — нет (их показывает опрос самого мастера).
func TestJobRunner_OnFinishedOnlyOnTerminalPhases(t *testing.T) {
	jr := NewJobRunner()
	calls := 0
	jr.SetOnFinished(func() { calls++ })

	job := jr.CreateJob("xray", "s1", nil)
	jr.UpdateStep(job.ID, JobPhaseCommitting, 50, "step")
	if calls != 0 {
		t.Fatalf("intermediate step fired %d calls", calls)
	}
	jr.SucceedJob(job.ID, nil)
	jr.FailJob(jr.CreateJob("xray", "s1", nil).ID, "E", "boom")
	jr.MarkRecoveryRequired(jr.CreateJob("xray", "s1", nil).ID, "r")
	jr.MarkCancelled(jr.CreateJob("xray", "s1", nil).ID)
	if calls != 4 {
		t.Fatalf("terminal phases fired %d calls, want 4", calls)
	}
	jr.FailJob("missing", "E", "x")
	if calls != 4 {
		t.Fatalf("unknown job fired: %d", calls)
	}
}

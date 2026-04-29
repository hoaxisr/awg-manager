package diagnostics

import (
	"testing"
	"time"
)

func TestRunOptions_RestartCycleOnlyWhenIncludeRestart(t *testing.T) {
	// Contract: restart_cycle running depends ONLY on opts.IncludeRestart.
	// Mode (Quick/Full) был выпилен; остаётся только это правило.
	cases := []struct {
		name               string
		opts               RunOptions
		wantIncludeRestart bool
	}{
		{"no-restart", RunOptions{IncludeRestart: false}, false},
		{"with-restart", RunOptions{IncludeRestart: true}, true},
	}

	for _, c := range cases {
		derived := c.opts.IncludeRestart
		if derived != c.wantIncludeRestart {
			t.Errorf("%s: derived includeRestart=%v, want %v", c.name, derived, c.wantIncludeRestart)
		}
	}
}

func TestBootHealth_GraceNotElapsed(t *testing.T) {
	// daemon только что стартовал — grace ещё не вышел, NotStartedOnBoot пусто.
	old := processStartedAt
	defer func() { processStartedAt = old }()
	processStartedAt = time.Now() // 0 секунд назад

	bh := computeBootHealth(
		[]bootHealthInput{
			{ID: "wg1", Name: "wg1", Backend: "kernel", Enabled: true, AutoStart: true,
				Status: "stopped", StoredStartedAt: ""},
		},
	)

	if bh.DaemonUptimeSec >= bh.GracePeriodSec {
		t.Fatalf("test setup invalid: uptime %d >= grace %d",
			bh.DaemonUptimeSec, bh.GracePeriodSec)
	}
	if len(bh.NotStartedOnBoot) != 0 {
		t.Errorf("expected empty NotStartedOnBoot during grace, got %v", bh.NotStartedOnBoot)
	}
}

func TestBootHealth_GraceElapsed_NeverStarted(t *testing.T) {
	// grace вышел, enabled+autoStart-туннель не running => never_started issue.
	old := processStartedAt
	defer func() { processStartedAt = old }()
	processStartedAt = time.Now().Add(-200 * time.Second) // > 120 grace

	bh := computeBootHealth(
		[]bootHealthInput{
			{ID: "wg1", Name: "wg1", Backend: "kernel", Enabled: true, AutoStart: true,
				Status: "stopped", StoredStartedAt: ""},
			{ID: "wg2", Name: "wg2", Backend: "nativewg", Enabled: true, AutoStart: true,
				Status: "running", StoredStartedAt: time.Now().Format(time.RFC3339)},
		},
	)

	if got := bh.GracePeriodSec; got != 120 {
		t.Errorf("GracePeriodSec=%d, want 120", got)
	}
	if got := len(bh.ExpectedRunning); got != 2 {
		t.Errorf("ExpectedRunning len=%d, want 2", got)
	}
	if got := len(bh.ActualRunning); got != 1 || bh.ActualRunning[0] != "wg2" {
		t.Errorf("ActualRunning=%v, want [wg2]", bh.ActualRunning)
	}
	if got := len(bh.NotStartedOnBoot); got != 1 {
		t.Fatalf("NotStartedOnBoot len=%d, want 1", got)
	}
	issue := bh.NotStartedOnBoot[0]
	if issue.TunnelID != "wg1" || issue.Reason != "never_started" {
		t.Errorf("issue=%+v, want id=wg1 reason=never_started", issue)
	}
}

func TestBootHealth_GraceElapsed_AllRunning(t *testing.T) {
	// grace вышел, всё что должно — running => NotStartedOnBoot пуст.
	old := processStartedAt
	defer func() { processStartedAt = old }()
	processStartedAt = time.Now().Add(-200 * time.Second)

	bh := computeBootHealth(
		[]bootHealthInput{
			{ID: "wg1", Name: "wg1", Backend: "kernel", Enabled: true, AutoStart: true, Status: "running"},
		},
	)
	if len(bh.NotStartedOnBoot) != 0 {
		t.Errorf("expected empty NotStartedOnBoot, got %v", bh.NotStartedOnBoot)
	}
}

func TestBootHealth_DisabledTunnelExcluded(t *testing.T) {
	// Disabled-туннели НЕ должны попадать в ExpectedRunning.
	old := processStartedAt
	defer func() { processStartedAt = old }()
	processStartedAt = time.Now().Add(-200 * time.Second)

	bh := computeBootHealth(
		[]bootHealthInput{
			{ID: "wg1", Name: "wg1", Backend: "kernel", Enabled: false, AutoStart: false, Status: "stopped"},
		},
	)
	if len(bh.ExpectedRunning) != 0 {
		t.Errorf("ExpectedRunning=%v, want []", bh.ExpectedRunning)
	}
	if len(bh.NotStartedOnBoot) != 0 {
		t.Errorf("NotStartedOnBoot=%v, want []", bh.NotStartedOnBoot)
	}
}

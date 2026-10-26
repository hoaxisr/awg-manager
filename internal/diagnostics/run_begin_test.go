package diagnostics

import "testing"

// TestRunnerBegin_SingleRunKeepsTheReport — прогон одного туннеля отчёта
// не даёт; стирать при нём последний полный отчёт значило оставить «отчёта
// нет» после проверки, которая ничего не меняла. Полный прогон отчёт
// сбрасывает, повторный старт во время прогона — только подписка.
func TestRunnerBegin_SingleRunKeepsTheReport(t *testing.T) {
	r := &Runner{result: &Report{}}
	if r.begin(RunOptions{TunnelID: "tn-1"}) || r.result == nil {
		t.Fatal("a single-tunnel run must keep the last full report")
	}
	if !r.begin(RunOptions{}) || r.result == nil {
		t.Fatal("a run in progress is joined, not restarted")
	}
	r.status.Status = "done"
	if r.begin(RunOptions{}) || r.result != nil {
		t.Fatal("a full run replaces the report")
	}
	if r.opts.TunnelID != "" || r.status.Status != "running" {
		t.Fatalf("status %+v, opts %+v", r.status, r.opts)
	}
}

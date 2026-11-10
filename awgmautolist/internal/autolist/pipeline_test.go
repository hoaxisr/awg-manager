package autolist

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func autoProbeTask(t *testing.T, eng *Engine, domain string) probeTask {
	t.Helper()
	if !eng.table.Note(Host{Name: domain}, WhyReset, "packet", time.Now()) {
		t.Fatal("candidate not created")
	}
	if !eng.enqueue(probeTask{host: domain, domain: domain, source: "packet"}) {
		t.Fatal("probe not queued")
	}
	task, ok := eng.nextProbe()
	if !ok {
		t.Fatal("probe not issued")
	}
	return task
}

func confirmedPair() ProbeResult {
	return ProbeResult{
		Verdict: VerdictBlocked, Why: WhyReset,
		TunnelProbed: true, TunnelVerdict: VerdictReachable,
	}
}

func TestDismissQueuedCandidateCancelsProbeAndAutoAdd(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	domain := "queued-dismiss.example"
	eng.table.Note(Host{Name: domain}, WhyReset, "packet", time.Now())
	if !eng.enqueue(probeTask{host: domain, domain: domain, source: "packet"}) {
		t.Fatal("probe not queued")
	}
	eng.Dismiss(domain)
	if task, ok := eng.nextProbe(); ok {
		// Даже если отмена регрессировала, старый generation обязан запретить запись.
		eng.completeProbe(task, confirmedPair(), time.Now())
	}
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("queued dismissed task added rules: %v", got)
	}
}

func TestDismissDuringProbePreventsAutoAdd(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "dismissed.example")
	eng.Dismiss(task.domain)
	eng.completeProbe(task, confirmedPair(), time.Now())
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("dismissed task added rules: %v", got)
	}
}

func TestTargetChangeDuringProbePreventsAutoAdd(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "changed.example")
	cfg := eng.Config()
	cfg.TargetInterface = "nwg1"
	if err := eng.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	eng.completeProbe(task, confirmedPair(), time.Now())
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("stale target added rules: %v", got)
	}
}

func TestAutoAddDisabledDuringProbePreventsWrite(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "disabled.example")
	cfg := eng.Config()
	cfg.AutoAdd = false
	if err := eng.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	eng.completeProbe(task, confirmedPair(), time.Now())
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("disabled automation added rules: %v", got)
	}
}

func TestUnrelatedDismissDoesNotInvalidateOtherDomain(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "keep-active.example")
	eng.table.Note(Host{Name: "other.example"}, WhyReset, "packet", time.Now())
	eng.Dismiss("other.example")
	eng.completeProbe(task, confirmedPair(), time.Now())
	if got := awgm.addedRules(); len(got) != 1 || got[0] != "keep-active.example" {
		t.Fatalf("unrelated dismiss cancelled active domain: %v", got)
	}
}

func TestVerboseChangeDoesNotInvalidateProbe(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "verbose-safe.example")
	cfg := eng.Config()
	cfg.Verbose = true
	if err := eng.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	eng.completeProbe(task, confirmedPair(), time.Now())
	if got := awgm.addedRules(); len(got) != 1 || got[0] != "verbose-safe.example" {
		t.Fatalf("verbose change invalidated probe: %v", got)
	}
}

func TestStaleResultDoesNotLeaveCheckingState(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "stale-state.example")
	eng.table.MarkChecking(task.domain)
	cfg := eng.Config()
	cfg.TargetInterface = "nwg1"
	if err := eng.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	eng.completeProbe(task, confirmedPair(), time.Now())
	row, exists := eng.table.get(task.domain)
	if !exists || row.State != StateCandidate.String() || row.Stage != "" {
		t.Fatalf("stale result left checking state: %+v, exists=%v", row, exists)
	}
}

func TestDismissCancelsActiveCurl(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{AutoAdd: true, TunnelProbe: true, TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	task := autoProbeTask(t, eng, "cancel-curl.example")
	started := make(chan struct{})
	eng.prober.OnStart = func(string) { close(started) }
	eng.prober.CurlPath = fakeCurl(t, "exec sleep 30")
	done := make(chan struct{})
	go func() {
		eng.probeOneContext(context.Background(), task)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("curl did not start")
	}
	eng.Dismiss(task.domain)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dismiss did not cancel active curl")
	}
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("cancelled curl added rules: %v", got)
	}
}

func TestCandidateTableIsBounded(t *testing.T) {
	table := NewTable()
	now := time.Now()
	for i := 0; i < HostsCap; i++ {
		if !table.Note(Host{Name: fmt.Sprintf("h%d.example", i)}, WhyReset, "packet", now) {
			t.Fatalf("candidate %d rejected before capacity", i)
		}
	}
	if table.Note(Host{Name: "overflow.example"}, WhyReset, "packet", now) {
		t.Fatal("candidate table exceeded its capacity")
	}
	if got := len(table.List()); got != HostsCap {
		t.Fatalf("candidate capacity = %d, want %d", got, HostsCap)
	}
}

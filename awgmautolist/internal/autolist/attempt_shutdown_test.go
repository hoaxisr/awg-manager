package autolist

import (
	"context"
	"testing"
	"time"
)

func TestNewAttemptClearsPreviousProbeResults(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Now()
	eng.table.Note(Host{Name: "www.fresh.example"}, WhyReset, "packet", now)
	eng.table.Confirm("fresh.example", VerdictBlocked, now)
	eng.table.ConfirmTunnel("fresh.example", VerdictReachable, now)
	eng.table.SetProbeDiagnostics("fresh.example", "old direct", "old tunnel")

	eng.table.MarkChecking("fresh.example")
	row, ok := eng.table.get("fresh.example")
	if !ok || row.Verdict != "" || !row.ProbedAt.IsZero() || row.ProbeError != "" ||
		row.TunnelVerdict != "" || !row.TunnelProbedAt.IsZero() || row.TunnelError != "" {
		t.Fatalf("new attempt retained old evidence: %+v", row)
	}

	eng.completeProbe(probeTask{host: "www.fresh.example", domain: "fresh.example"},
		ProbeResult{Verdict: VerdictUnknown, Err: "new direct failure"}, now.Add(time.Second))
	row, ok = eng.table.get("fresh.example")
	if !ok || row.Verdict != VerdictUnknown.String() || row.ProbeError != "new direct failure" ||
		row.TunnelVerdict != "" || !row.TunnelProbedAt.IsZero() || row.TunnelError != "" {
		t.Fatalf("completed direct-only attempt mixed attempts: %+v", row)
	}
}

func TestStopWaitsForActiveProbeWorker(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	started := make(chan struct{})
	eng.prober.OnStart = func(string) { close(started) }
	eng.prober.CurlPath = fakeCurl(t, "exec sleep 30")
	eng.table.Note(Host{Name: "stop-active.example"}, WhyReset, "packet", time.Now())
	if !eng.enqueue(probeTask{host: "stop-active.example", domain: "stop-active.example", source: "packet"}) {
		t.Fatal("active probe was not queued")
	}

	go eng.Run(context.Background())
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("probe worker did not start curl")
	}

	stopped := make(chan struct{})
	go func() {
		eng.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel and join the active probe worker")
	}
	eng.mu.Lock()
	busy, host := eng.workerBusy, eng.probingHost
	eng.mu.Unlock()
	if busy || host != "" {
		t.Fatalf("Stop returned with active worker state: busy=%v host=%q", busy, host)
	}
}

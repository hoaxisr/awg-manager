package autolist

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probeq"
)

func openObservationTick(awgm *fakeAWGM, eng *Engine, id, host string) {
	awgm.resetConns()
	autolistConn(awgm, id, host, 10000, 90*time.Second)
	eng.tick(context.Background())
}

func TestOpenEventRetriesTemporaryCoverageFailureOnceAccepted(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	now := time.Now()
	eng.obs.now = func() time.Time { return now }
	const host = "www.retry.example"

	openObservationTick(awgm, eng, "same-id", host)
	now = now.Add(PollIntervalSec * time.Second)
	awgm.resolveError = true
	openObservationTick(awgm, eng, "same-id", host)
	if _, queued := eng.probeQueue.Counts(now.Unix()); queued != 0 {
		t.Fatalf("failed preparation queued %d tasks", queued)
	}

	awgm.resolveError = false
	now = now.Add(PollIntervalSec * time.Second)
	openObservationTick(awgm, eng, "same-id", host)
	if _, queued := eng.probeQueue.Counts(now.Unix()); queued != 1 {
		t.Fatalf("after DNS recovery queued=%d, want 1", queued)
	}

	task, ok := eng.nextProbe()
	if !ok {
		t.Fatal("accepted open event was not issued")
	}
	eng.completeProbe(task, ProbeResult{Verdict: VerdictUnknown, Err: "finished unknown"}, now)
	now = now.Add(PollIntervalSec * time.Second)
	openObservationTick(awgm, eng, "same-id", host)
	if _, queued := eng.probeQueue.Counts(now.Unix()); queued != 0 {
		t.Fatalf("accepted connection retried after completed unknown: queued=%d", queued)
	}
}

func TestOpenEventRetriesAfterEvictionBeforeProbe(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	now := time.Now()
	eng.obs.now = func() time.Time { return now }
	const host = "www.evicted.example"
	const id = "evicted-open"

	openObservationTick(awgm, eng, id, host)
	now = now.Add(PollIntervalSec * time.Second)
	openObservationTick(awgm, eng, id, host)
	if _, queued := eng.probeQueue.Counts(now.Unix()); queued != 1 {
		t.Fatalf("open event was not initially queued: %d", queued)
	}
	for i := 0; i < probeq.QueueCapacity; i++ {
		other := fmt.Sprintf("h%02d.example", i)
		if !eng.enqueue(probeTask{host: other, domain: other, probeOnly: true}) {
			t.Fatalf("queue filler %d was rejected", i)
		}
	}
	eng.obs.stallMu.Lock()
	probedAfterEviction := eng.obs.stallPrev[id].probed
	eng.obs.stallMu.Unlock()
	if probedAfterEviction {
		t.Fatal("evicted open event remained acknowledged")
	}

	now = now.Add(PollIntervalSec * time.Second)
	openObservationTick(awgm, eng, id, host)
	eng.mu.Lock()
	_, accepted := eng.probeTasks[host]
	eng.mu.Unlock()
	if !accepted {
		t.Fatal("evicted open event was not accepted again")
	}
}

func TestOpenEventPreparationRetriesAreBounded(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.resolveError = true
	eng := NewEngine(awgm.srv.URL, "")
	now := time.Now()
	eng.obs.now = func() time.Time { return now }
	const host = "www.bounded.example"

	openObservationTick(awgm, eng, "bounded", host)
	for i := 0; i < openEventMaxAttempts+2; i++ {
		now = now.Add(PollIntervalSec * time.Second)
		openObservationTick(awgm, eng, "bounded", host)
	}
	eng.obs.stallMu.Lock()
	attempts := eng.obs.stallPrev["bounded"].openAttempts
	eng.obs.stallMu.Unlock()
	if attempts != openEventMaxAttempts {
		t.Fatalf("preparation attempts=%d, want %d", attempts, openEventMaxAttempts)
	}
	awgm.resolveError = false
	now = now.Add(PollIntervalSec * time.Second)
	openObservationTick(awgm, eng, "bounded", host)
	if _, queued := eng.probeQueue.Counts(now.Unix()); queued != 0 {
		t.Fatalf("exhausted preparation retried after recovery: queued=%d", queued)
	}
}

func TestDismissAndExcludeForbidOpenEventRetry(t *testing.T) {
	for _, action := range []string{"dismiss", "exclude"} {
		t.Run(action, func(t *testing.T) {
			awgm := newFakeAWGM(t)
			eng := NewEngine(awgm.srv.URL, "")
			now := time.Now()
			eng.obs.now = func() time.Time { return now }
			const host = "www.suppressed.example"

			openObservationTick(awgm, eng, "suppressed", host)
			now = now.Add(PollIntervalSec * time.Second)
			awgm.resolveError = true
			openObservationTick(awgm, eng, "suppressed", host)
			if action == "dismiss" {
				eng.Dismiss("suppressed.example")
			} else if err := eng.SetConfig(Config{ExcludeDomains: []string{"suppressed.example"}}); err != nil {
				t.Fatal(err)
			}
			awgm.resolveError = false
			now = now.Add(PollIntervalSec * time.Second)
			openObservationTick(awgm, eng, "suppressed", host)
			if _, queued := eng.probeQueue.Counts(now.Unix()); queued != 0 {
				t.Fatalf("%s allowed a retry: queued=%d", action, queued)
			}
		})
	}
}

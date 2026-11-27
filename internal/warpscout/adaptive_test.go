package warpscout

import (
	"context"
	"reflect"
	"testing"
)

func TestAdaptiveKeepsTestedSignatureAndStopsAtRTTTarget(t *testing.T) {
	s, runner, _, _ := newTestService(t)
	runner.hits = []Hit{{Endpoint: "162.159.192.1:2408", PingMs: 24}}
	base := s.awgForUse()
	for _, tc := range []struct{ target, calls int }{{35, 1}, {20, 4}, {0, 4}} {
		runner.calls = nil
		hits := 0
		err := s.runCandidates(context.Background(), ScanParams{MaxCandidates: 32}, base, Filters{Adaptive: true, MaxRTTMs: tc.target}, false, "", func(e EngineEvent, a *AWGParams) {
			if e.Type == "hit" {
				hits++
				if a == nil || *a != base {
					t.Fatal("wrong signature for selected endpoint")
				}
			}
		})
		if err != nil || len(runner.calls) != tc.calls {
			t.Fatalf("target %d calls %d error %v", tc.target, len(runner.calls), err)
		}
		if (hits == 1) != (tc.target != 20) {
			t.Fatalf("target %d hits %d", tc.target, hits)
		}
		for _, p := range runner.calls {
			if p.Seed != runner.calls[0].Seed || p.DeadlineMs > 30000 || p.MaxHits != 1 {
				t.Fatal("inconsistent search limits")
			}
		}
	}
}

type latencyTrialRunner struct{ fakeRunner }

func (r *latencyTrialRunner) Run(ctx context.Context, p ScanParams, on func(EngineEvent)) error {
	latency := 85
	if len(r.calls) == 1 {
		latency = 24
	}
	r.hits = []Hit{{Endpoint: "162.159.192.1:2408", PingMs: latency}}
	return r.fakeRunner.Run(ctx, p, on)
}

func TestAdaptiveChangesMaskAfterSlowBase(t *testing.T) {
	s, _, _, _ := newTestService(t)
	r := &latencyTrialRunner{}
	s.d.Runner = r
	base := s.awgForUse()
	err := s.runCandidates(context.Background(), ScanParams{Ifname: "eth3"}, base, Filters{Adaptive: true, MaxRTTMs: 35}, false, "", func(e EngineEvent, a *AWGParams) {
		if e.Type != "hit" {
			return
		}
		if e.PingMs != 24 || a == nil || a.SignatureHostPreset != "ru_whitelist" {
			t.Fatalf("wrong candidate: %+v", e)
		}
		sig, err := signatureHex(*a)
		if err != nil || !reflect.DeepEqual(sig, r.calls[1].AWG.I) {
			t.Fatal("returned signature differs from tested one")
		}
	})
	if err != nil || len(r.calls) != 2 || s.lastPoorAWG == nil {
		t.Fatalf("unexpected trials %d, err %v", len(r.calls), err)
	}
}

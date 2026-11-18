package uplink

import (
	"context"
	"errors"
	"testing"
	"time"
)

type candidateTestAdapter struct {
	*testAdapter
	candidateFails bool
	candidateCalls int
}

func (a *candidateTestAdapter) ProbeCandidate(context.Context, Target, string) error {
	a.candidateCalls++
	if a.candidateFails {
		return errors.New("candidate failed")
	}
	return nil
}

func TestAutomaticReturnUsesIndependentThresholdAndHoldDown(t *testing.T) {
	c, a, _, now := fixture(t)
	candidate := &candidateTestAdapter{testAdapter: a}
	c.adapters["test"] = candidate
	p := c.List()[0].Policy
	p.Failback = "automatic"
	p.RecoveryThreshold = 2
	p.RecoveryIntervalSeconds = 10
	if _, err := c.Put(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a.device = "wan2"
	c.Tick(context.Background())
	if state(c).Provider != "ISP2" {
		t.Fatal(state(c))
	}
	*now = now.Add(5 * time.Second)
	c.Tick(context.Background())
	if candidate.candidateCalls != 0 {
		t.Fatal("hold-down bypassed")
	}
	*now = now.Add(time.Minute)
	c.Tick(context.Background())
	if candidate.candidateCalls != 1 || state(c).Provider != "ISP2" {
		t.Fatal(state(c))
	}
	candidate.candidateFails = true
	advance(c, now)
	if state(c).RecoverySuccesses != 0 || a.device != "wan2" {
		t.Fatal(state(c))
	}
	candidate.candidateFails = false
	advance(c, now)
	if a.device != "wan2" {
		t.Fatal("threshold bypassed")
	}
	advance(c, now)
	if state(c).Phase != "healthy" || state(c).Provider != "ISP1" {
		t.Fatal(state(c))
	}
}

func TestAutomaticReturnUnsupportedAndPinned(t *testing.T) {
	c, _, _, _ := fixture(t)
	p := c.List()[0].Policy
	p.Failback = "automatic"
	if _, err := c.Put(context.Background(), p); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	p.Failback = "manual"
	p.Mode = "pinned"
	p.Providers = p.Providers[:1]
	if _, err := c.Put(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

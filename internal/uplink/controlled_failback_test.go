package uplink

import (
	"context"
	"strings"
	"testing"
	"time"
)

type controlledAdapter struct{ *testAdapter }

func (controlledAdapter) UplinkCapabilities(Target) Capabilities {
	return Capabilities{ControlledFailback: true, RestartScope: "target"}
}
func TestControlledReturnVerifiesPayloadAndRestoresWorkingReserve(t *testing.T) {
	for _, primaryWorks := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "return"}[primaryWorks], func(t *testing.T) {
			c, a, _, now := fixture(t)
			c.adapters["test"] = controlledAdapter{a}
			p := c.store.List()[0]
			p.Failback = "controlled"
			p.RecoveryThreshold = 2
			p.RecoveryIntervalSeconds = 10
			p.HoldDownSeconds = 10
			if _, err := c.Put(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			c.Tick(context.Background())
			a.failing["wan1"] = true
			for i := 0; i < 4 && state(c).Provider != "ISP2"; i++ {
				advance(c, now)
			}
			if state(c).Provider != "ISP2" {
				t.Fatal(state(c))
			}
			a.failing["wan1"] = !primaryWorks
			a.activated = nil
			*now = now.Add(time.Minute)
			c.Tick(context.Background())
			if len(a.activated) != 0 {
				t.Fatal("threshold bypassed")
			}
			advance(c, now)
			want := "ISP2"
			if primaryWorks {
				want = "ISP1"
			}
			if state(c).Phase != "healthy" || state(c).Provider != want {
				t.Fatal(state(c))
			}
			if !primaryWorks && (len(a.activated) != 2 || a.activated[0] != "wan1" || a.activated[1] != "wan2") {
				t.Fatalf("working reserve not restored: %v", a.activated)
			}
			if !primaryWorks && !strings.Contains(state(c).LastError, "automatic return failed; previous path restored and verified: tunnel probe:") {
				t.Fatalf("probe failure hidden after rollback: %s", state(c).LastError)
			}
		})
	}
}

func TestManualSwitchWithControlledPolicyReportsProbeFailure(t *testing.T) {
	c, a, _, _ := fixture(t)
	c.adapters["test"] = controlledAdapter{a}
	p := c.store.List()[0]
	p.Failback = "controlled"
	p, err := c.Put(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	c.Tick(context.Background())
	a.failing["wan2"] = true
	err = c.Switch(context.Background(), p.ID, "ISP2", p.Revision)
	if err == nil || !strings.Contains(err.Error(), "manual switch failed; previous path restored and verified: tunnel probe:") {
		t.Fatalf("manual failure misclassified or hidden: %v", err)
	}
	if state(c).Phase != "healthy" || state(c).Provider != "ISP1" {
		t.Fatal(state(c))
	}
}

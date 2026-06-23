package monitoring

import (
	"testing"
)

func TestEffectiveTargets_Empty(t *testing.T) {
	if got := EffectiveTargets(nil); len(got) != 0 {
		t.Fatalf("got %d targets, want 0 (self-only, no tunnels)", len(got))
	}
}

func TestEffectiveTargets_PingcheckTargetIgnored(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", Name: "A", IfaceName: "wg0", PingcheckTarget: "bingo.com"},
	}
	if got := EffectiveTargets(tunnels); len(got) != 0 {
		t.Errorf("expected 0 targets (pingcheck no longer probed), got %d: %+v", len(got), got)
	}
}

func TestEffectiveTargets_NoSelfTarget(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", Name: "A", IfaceName: "wg0", SelfTarget: ""},
	}
	if got := EffectiveTargets(tunnels); len(got) != 0 {
		t.Errorf("expected 0 targets when self-target empty, got %d", len(got))
	}
}

func TestEffectiveTargets_SelfTargetAdded(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", SelfTarget: "10.0.0.1", SelfMethod: "ping"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 1 {
		t.Fatalf("expected 1 self target, got %d", len(got))
	}
	if got[0].Host != "10.0.0.1" || got[0].ID != "cc-10.0.0.1" || got[0].Name != "10.0.0.1" {
		t.Errorf("unexpected self target: %+v", got[0])
	}
}

func TestEffectiveTargets_SelfTargetWithPingcheckYieldsOnlySelf(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", PingcheckTarget: "ya.ru", SelfTarget: "10.0.0.1", SelfMethod: "ping"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 1 {
		t.Fatalf("expected 1 target (self only, pingcheck dropped), got %d: %+v", len(got), got)
	}
	if got[0].ID != "cc-10.0.0.1" {
		t.Errorf("expected only the cc- self target, got %s", got[0].ID)
	}
}

func TestEffectiveTargets_SelfTargetDedupedAcrossTunnels(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", SelfTarget: "connectivitycheck.gstatic.com", SelfMethod: "http"},
		{ID: "tn-B", SelfTarget: "connectivitycheck.gstatic.com", SelfMethod: "http"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 1 {
		t.Errorf("expected 1 dedup'd self target, got %d", len(got))
	}
}

func TestEffectiveTargets_DistinctSelfTargets(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", SelfTarget: "10.0.0.1", SelfMethod: "ping"},
		{ID: "tn-B", SelfTarget: "10.0.0.2", SelfMethod: "ping"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 2 {
		t.Fatalf("expected 2 distinct self targets, got %d", len(got))
	}
	if got[0].ID != "cc-10.0.0.1" || got[1].ID != "cc-10.0.0.2" {
		t.Errorf("unexpected ordering: %s, %s", got[0].ID, got[1].ID)
	}
}

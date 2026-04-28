package monitoring

import (
	"testing"
)

func TestEffectiveTargets_BaseOnly(t *testing.T) {
	got := EffectiveTargets(nil)
	if len(got) != 3 {
		t.Fatalf("got %d targets, want 3 base targets", len(got))
	}
	if got[0].Host != "1.1.1.1" || got[1].Host != "8.8.8.8" || got[2].Host != "9.9.9.9" {
		t.Errorf("unexpected base ordering: %+v", got)
	}
}

func TestEffectiveTargets_OverlappingPingcheckTarget(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", Name: "A", IfaceName: "wg0", PingcheckTarget: "8.8.8.8"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 3 {
		t.Errorf("expected 3 targets when pingcheck target overlaps base, got %d", len(got))
	}
}

func TestEffectiveTargets_UniquePingcheckTarget(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", Name: "A", IfaceName: "wg0", PingcheckTarget: "bingo.com"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 4 {
		t.Errorf("expected 4 targets (3 base + 1 custom), got %d", len(got))
	}
	last := got[3]
	if last.Host != "bingo.com" || last.ID != "pc-bingo.com" || last.Name != "bingo.com" {
		t.Errorf("unexpected synthesised target: %+v", last)
	}
}

func TestEffectiveTargets_NoPingcheck(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", Name: "A", IfaceName: "wg0", PingcheckTarget: ""},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 3 {
		t.Errorf("expected 3 base targets when pingcheck disabled, got %d", len(got))
	}
}

func TestEffectiveTargets_MultipleTunnelsSameCustomTarget(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", PingcheckTarget: "bingo.com"},
		{ID: "tn-B", PingcheckTarget: "bingo.com"},
		{ID: "tn-C", PingcheckTarget: "ya.ru"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 5 {
		t.Errorf("expected 5 (3 base + 2 unique custom), got %d", len(got))
	}
	if got[3].Host != "bingo.com" || got[4].Host != "ya.ru" {
		t.Errorf("unexpected custom-target order: %s, %s", got[3].Host, got[4].Host)
	}
}

func TestEffectiveTargets_SelfTargetAdded(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", SelfTarget: "10.0.0.1", SelfMethod: "ping"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 4 {
		t.Errorf("expected 4 targets (3 base + 1 self), got %d", len(got))
	}
	if got[3].Host != "10.0.0.1" || got[3].ID != "cc-10.0.0.1" {
		t.Errorf("unexpected self target: %+v", got[3])
	}
}

func TestEffectiveTargets_SelfAndPingcheckCoexist(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", PingcheckTarget: "ya.ru", SelfTarget: "10.0.0.1", SelfMethod: "ping"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 5 {
		t.Errorf("expected 5 (3 base + pc + cc), got %d", len(got))
	}
	if got[3].ID != "pc-ya.ru" || got[4].ID != "cc-10.0.0.1" {
		t.Errorf("unexpected ordering: %s, %s", got[3].ID, got[4].ID)
	}
}

func TestEffectiveTargets_SelfTargetDedupedAcrossTunnels(t *testing.T) {
	tunnels := []Tunnel{
		{ID: "tn-A", SelfTarget: "connectivitycheck.gstatic.com", SelfMethod: "http"},
		{ID: "tn-B", SelfTarget: "connectivitycheck.gstatic.com", SelfMethod: "http"},
	}
	got := EffectiveTargets(tunnels)
	if len(got) != 4 {
		t.Errorf("expected 4 (3 base + 1 dedup'd self), got %d", len(got))
	}
}

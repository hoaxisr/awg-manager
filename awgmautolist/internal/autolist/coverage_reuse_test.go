package autolist

import (
	"context"
	"testing"
	"time"
)

func TestAddRunsCoverageForEveryNewDomain(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	if !eng.noteIfUnrouted(context.Background(), Host{Name: "www.first.example"}, WhyReset, "packet") {
		t.Fatal("candidate was not created")
	}
	before := awgm.inspectCalls
	if err := eng.AddToTunnel([]string{"first.example"})["first.example"]; err != nil {
		t.Fatal(err)
	}
	if awgm.inspectCalls <= before {
		t.Fatal("new domain bypassed current coverage checks")
	}
	before = awgm.inspectCalls
	if err := eng.AddToTunnel([]string{"second.example"})["second.example"]; err != nil {
		t.Fatal(err)
	}
	if awgm.inspectCalls <= before {
		t.Fatal("second new domain reused stale coverage")
	}
	if got := awgm.addedRules(); len(got) != 2 {
		t.Fatalf("rule-set contents = %v", got)
	}
	if rows := eng.table.List(); len(rows) != 0 {
		t.Fatalf("added domains remain in candidates: %+v", rows)
	}
}

func TestAddExistingDomainIsNoOpWithoutDuplicates(t *testing.T) {
	awgm := newFakeAWGM(t)
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := eng.AddToTunnel([]string{"same.example"})["same.example"]; err != nil {
			t.Fatal(err)
		}
	}
	if got := awgm.addedRules(); len(got) != 1 || got[0] != "same.example" {
		t.Fatalf("duplicate additions = %v", got)
	}
}

func TestExplicitDirectRuleIsNotOverridden(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.directDomains["direct.example"] = true
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddToTunnel([]string{"direct.example"})["direct.example"]; err == nil {
		t.Fatal("explicit direct rule was overridden")
	}
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("explicit direct domain added: %v", got)
	}
}

func TestResolveFailureIsFailClosedWhenIPCoverageEnabled(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.resolveError = true
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0"}); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddToTunnel([]string{"unresolved.example"})["unresolved.example"]; err == nil {
		t.Fatal("required IP coverage error was ignored")
	}
	if got := awgm.addedRules(); len(got) != 0 {
		t.Fatalf("unresolved domain added: %v", got)
	}
}

func TestDisabledIPCoverageSkipsResolve(t *testing.T) {
	awgm := newFakeAWGM(t)
	awgm.resolveError = true
	resolveIP := false
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{TargetInterface: "nwg0", ResolveIPEnabled: &resolveIP}); err != nil {
		t.Fatal(err)
	}
	if err := eng.AddToTunnel([]string{"without-ip.example"})["without-ip.example"]; err != nil {
		t.Fatalf("disabled IP coverage performed resolve: %v", err)
	}
}

func TestPacketObservedIPInspectionRespectsDisabledOption(t *testing.T) {
	awgm := newFakeAWGM(t)
	resolveIP := false
	eng := NewEngine(awgm.srv.URL, "")
	if err := eng.SetConfig(Config{ResolveIPEnabled: &resolveIP}); err != nil {
		t.Fatal(err)
	}
	domain, ok := eng.coverageAllowsDomain(context.Background(), Host{Name: "www.packet.example", IP: "203.0.113.10"}, "packet.example", true)
	if !ok || domain != "packet.example" {
		t.Fatalf("packet coverage rejected with IP option disabled: %q, %v", domain, ok)
	}
	if awgm.inspectCalls != 2 {
		t.Fatalf("expected host+domain only, observed-IP Inspector leaked in: calls=%d", awgm.inspectCalls)
	}
}

func TestHistoryFiltersRemovedLifecycleStatuses(t *testing.T) {
	eng := NewEngine("http://127.0.0.1:1", "")
	now := time.Now()
	eng.recordHistory([]HistoryEntry{
		{Domain: "a.example", Action: "added", Source: "user", At: now},
		{Domain: "a.example", Action: "removed", Source: "reachable", At: now},
		{Domain: "a.example", Action: "dismissed", Source: "user", At: now},
	})
	history := snapshotHistory(t, eng)
	if len(history) != 1 || history[0].Action != "dismissed" {
		t.Fatalf("history statuses = %+v", history)
	}
}

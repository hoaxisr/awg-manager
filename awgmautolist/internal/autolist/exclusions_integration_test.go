package autolist

import (
	"context"
	"testing"
)

func TestManualExclusionsGateNominationAndManualAdd(t *testing.T) {
	tests := []struct {
		name     string
		excluded string
		host     string
		domain   string
	}{
		{name: "parent excludes subdomain", excluded: "safe.example", host: "www.safe.example", domain: "safe.example"},
		{name: "excluded subdomain blocks broader suffix rule", excluded: "ads.example.com", host: "www.example.com", domain: "example.com"},
		{name: "exact registered domain", excluded: "example.org", host: "example.org", domain: "example.org"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			awgm := newFakeAWGM(t)
			eng := NewEngine(awgm.srv.URL, "")
			if err := eng.SetConfig(Config{ExcludeDomains: []string{tc.excluded}, TargetInterface: "nwg0"}); err != nil {
				t.Fatal(err)
			}
			if eng.noteIfUnrouted(context.Background(), Host{Name: tc.host}, WhyReset, "packet") {
				t.Fatal("excluded host was nominated")
			}
			if rows := eng.table.List(); len(rows) != 0 {
				t.Fatalf("excluded candidate = %+v", rows)
			}
			results := eng.AddToTunnel([]string{tc.domain})
			if results[tc.domain] == nil {
				t.Fatal("excluded domain was manually added without an error")
			}
			if len(awgm.addedRules()) != 0 || awgm.applied != 0 {
				t.Fatalf("excluded domain changed router draft: added=%v applied=%d", awgm.addedRules(), awgm.applied)
			}
		})
	}
}

func TestRuleSetExclusionsMatchAndFailClosed(t *testing.T) {
	t.Run("host and registered domain are checked", func(t *testing.T) {
		for _, input := range []string{"www.geo.example", "geo.example"} {
			awgm := newFakeAWGM(t)
			awgm.ruleSetMatches[input] = true
			eng := NewEngine(awgm.srv.URL, "")
			if err := eng.SetConfig(Config{ExcludeRuleSets: []string{"category"}}); err != nil {
				t.Fatal(err)
			}
			if eng.noteIfUnrouted(context.Background(), Host{Name: "www.geo.example"}, WhyFreeze, "stall") {
				t.Fatalf("rule-set match on %q did not exclude candidate", input)
			}
		}
	})

	t.Run("unsupported selected set rejects nomination and add", func(t *testing.T) {
		awgm := newFakeAWGM(t)
		awgm.unsupportedRuleSets["missing"] = true
		eng := NewEngine(awgm.srv.URL, "")
		if err := eng.SetConfig(Config{ExcludeRuleSets: []string{"missing"}, TargetInterface: "nwg0"}); err != nil {
			t.Fatal(err)
		}
		if eng.noteIfUnrouted(context.Background(), Host{Name: "www.failclosed.example"}, WhyNoConnect, "dial") {
			t.Fatal("unsupported exclusion rule-set was treated as no match")
		}
		if err := eng.AddToTunnel([]string{"failclosed.example"})["failclosed.example"]; err == nil {
			t.Fatal("manual add did not fail closed")
		}
		if awgm.applied != 0 {
			t.Fatal("failed exclusion check applied staging")
		}
	})

	t.Run("empty lists preserve existing nomination behavior", func(t *testing.T) {
		awgm := newFakeAWGM(t)
		eng := NewEngine(awgm.srv.URL, "")
		if !eng.noteIfUnrouted(context.Background(), Host{Name: "www.ordinary.example"}, WhyFreeze, "stall") {
			t.Fatal("empty exclusion lists changed nomination behavior")
		}
	})
}

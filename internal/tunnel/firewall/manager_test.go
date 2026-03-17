package firewall

import (
	"testing"
)

func TestStandardRules(t *testing.T) {
	rules := StandardRules("OpkgTun0")

	// Should have 5 rules (4 filter + 1 nat)
	if len(rules) != 5 {
		t.Errorf("StandardRules returned %d rules, want 5", len(rules))
	}

	// Verify filter rules
	filterCount := 0
	natCount := 0
	hasInputRule := false
	hasOutputRule := false
	hasForwardInRule := false
	hasForwardOutRule := false
	hasMasqueradeRule := false

	for _, rule := range rules {
		if rule.Table == "filter" {
			filterCount++
		} else if rule.Table == "nat" {
			natCount++
		}

		switch {
		case rule.Chain == "INPUT" && rule.Direction == "-i":
			hasInputRule = true
		case rule.Chain == "OUTPUT" && rule.Direction == "-o":
			hasOutputRule = true
		case rule.Chain == "FORWARD" && rule.Direction == "-i":
			hasForwardInRule = true
		case rule.Chain == "FORWARD" && rule.Direction == "-o":
			hasForwardOutRule = true
		case rule.Chain == "POSTROUTING" && rule.Target == "MASQUERADE":
			hasMasqueradeRule = true
		}
	}

	if filterCount != 4 {
		t.Errorf("Expected 4 filter rules, got %d", filterCount)
	}
	if natCount != 1 {
		t.Errorf("Expected 1 nat rule, got %d", natCount)
	}
	if !hasInputRule {
		t.Error("Missing INPUT rule")
	}
	if !hasOutputRule {
		t.Error("Missing OUTPUT rule")
	}
	if !hasForwardInRule {
		t.Error("Missing FORWARD -i rule")
	}
	if !hasForwardOutRule {
		t.Error("Missing FORWARD -o rule")
	}
	if !hasMasqueradeRule {
		t.Error("Missing MASQUERADE rule - this was the v1 bug!")
	}
}

func TestStandardRules_Interface(t *testing.T) {
	rules := StandardRules("TestIface")

	for _, rule := range rules {
		if rule.Interface != "TestIface" {
			t.Errorf("Rule has interface %q, want %q", rule.Interface, "TestIface")
		}
	}
}

func TestManagerImpl_buildRuleArgs(t *testing.T) {
	m := &ManagerImpl{}

	tests := []struct {
		name     string
		action   string
		rule     Rule
		wantArgs []string
	}{
		{
			name:   "filter INPUT",
			action: "-A",
			rule: Rule{
				Table:     "filter",
				Chain:     "INPUT",
				Interface: "OpkgTun0",
				Direction: "-i",
				Target:    "ACCEPT",
			},
			wantArgs: []string{"-w", "-A", "INPUT", "-i", "OpkgTun0", "-j", "ACCEPT"},
		},
		{
			name:   "nat POSTROUTING",
			action: "-A",
			rule: Rule{
				Table:     "nat",
				Chain:     "POSTROUTING",
				Interface: "OpkgTun0",
				Direction: "-o",
				Target:    "MASQUERADE",
			},
			wantArgs: []string{"-w", "-t", "nat", "-A", "POSTROUTING", "-o", "OpkgTun0", "-j", "MASQUERADE"},
		},
		{
			name:   "delete rule",
			action: "-D",
			rule: Rule{
				Table:     "filter",
				Chain:     "FORWARD",
				Interface: "OpkgTun0",
				Direction: "-o",
				Target:    "ACCEPT",
			},
			wantArgs: []string{"-w", "-D", "FORWARD", "-o", "OpkgTun0", "-j", "ACCEPT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.buildRuleArgs(tt.action, tt.rule)
			if len(got) != len(tt.wantArgs) {
				t.Errorf("buildRuleArgs() len = %d, want %d", len(got), len(tt.wantArgs))
				t.Errorf("got: %v", got)
				t.Errorf("want: %v", tt.wantArgs)
				return
			}
			for i := range got {
				if got[i] != tt.wantArgs[i] {
					t.Errorf("buildRuleArgs()[%d] = %q, want %q", i, got[i], tt.wantArgs[i])
				}
			}
		})
	}
}

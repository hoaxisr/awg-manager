package mihomo

import (
	"fmt"
	"strings"
	"testing"
)

func TestAllRuleSpecs_Contract(t *testing.T) {
	specs := AllRuleSpecs()
	if len(specs) != 36 {
		t.Fatalf("expected 36 rule specs, got %d", len(specs))
	}

	for _, spec := range specs {
		t.Run(spec.Type, func(t *testing.T) {
			var ruleLine string
			if spec.Type == "MATCH" {
				ruleLine = "MATCH,DIRECT"
			} else if spec.Type == "RULE-SET" {
				ruleLine = "RULE-SET,test-provider,DIRECT"
			} else {
				ruleLine = fmt.Sprintf("%s,%s,DIRECT", spec.Type, spec.SamplePayload)
			}

			cfg := Config{
				Rules: []string{ruleLine},
				RuleProvider: map[string]map[string]interface{}{
					"test-provider": {
						"type":     "http",
						"behavior": "classical",
						"path":     "/tmp/test.yaml",
						"url":      "https://example.com/rules.yaml",
					},
				},
			}

			if err := validateCompiledConfig(&cfg); err != nil {
				t.Fatalf("spec for %s failed validation with sample payload: %v (rule: %q)", spec.Type, err, ruleLine)
			}
		})
	}
}

func TestRuleSpecs_ArityValidation(t *testing.T) {
	// Too few fields
	cfgTooFew := Config{
		Rules: []string{"DOMAIN,example.com"},
	}
	if err := validateCompiledConfig(&cfgTooFew); err == nil {
		t.Fatal("expected error for DOMAIN rule with missing target, got nil")
	}

	// Too many fields on DOMAIN (max is 3)
	cfgTooMany := Config{
		Rules: []string{"DOMAIN,example.com,DIRECT,extra"},
	}
	if err := validateCompiledConfig(&cfgTooMany); err == nil {
		t.Fatal("expected error for DOMAIN rule with extra field, got nil")
	}

	// Modifiers not allowed on DOMAIN
	cfgModOnDomain := Config{
		Rules: []string{"DOMAIN,example.com,DIRECT,no-resolve"},
	}
	if err := validateCompiledConfig(&cfgModOnDomain); err == nil {
		t.Fatal("expected error for DOMAIN rule with no-resolve, got nil")
	}
}

func TestRuleSpecs_Modifiers(t *testing.T) {
	// Valid modifier on IP-CIDR
	cfgValidMod := Config{
		Rules: []string{"IP-CIDR,192.168.1.0/24,DIRECT,no-resolve"},
	}
	if err := validateCompiledConfig(&cfgValidMod); err != nil {
		t.Fatalf("unexpected error for IP-CIDR with no-resolve: %v", err)
	}

	// Invalid modifier on IP-CIDR
	cfgInvalidMod := Config{
		Rules: []string{"IP-CIDR,192.168.1.0/24,DIRECT,invalid-modifier"},
	}
	if err := validateCompiledConfig(&cfgInvalidMod); err == nil {
		t.Fatal("expected error for IP-CIDR with invalid modifier, got nil")
	}
}

func TestCompositeRules_Semantics(t *testing.T) {
	tests := []struct {
		name    string
		rule    string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid AND with 2 children",
			rule:    "AND,((DOMAIN,example.com),(NETWORK,tcp)),DIRECT",
			wantErr: false,
		},
		{
			name:    "invalid AND with only 1 child",
			rule:    "AND,((DOMAIN,example.com)),DIRECT",
			wantErr: true,
			errMsg:  "at least 2 child rules",
		},
		{
			name:    "valid OR with 2 children",
			rule:    "OR,((DOMAIN,example.com),(DOMAIN-SUFFIX,google.com)),DIRECT",
			wantErr: false,
		},
		{
			name:    "invalid OR with 1 child",
			rule:    "OR,((DOMAIN,example.com)),DIRECT",
			wantErr: true,
			errMsg:  "at least 2 child rules",
		},
		{
			name:    "valid NOT with 1 child",
			rule:    "NOT,((DOMAIN,example.com)),DIRECT",
			wantErr: false,
		},
		{
			name:    "invalid NOT with 2 children",
			rule:    "NOT,((DOMAIN,example.com),(NETWORK,tcp)),DIRECT",
			wantErr: true,
			errMsg:  "exactly 1 child rule",
		},
		{
			name:    "nested composite rules within depth limit",
			rule:    "AND,((NOT,((DOMAIN,example.com))),(NETWORK,tcp)),DIRECT",
			wantErr: false,
		},
		{
			name:    "exceeded nesting depth (> 3)",
			rule:    "AND,((AND,((AND,((AND,((DOMAIN,example.com),(NETWORK,tcp))),(NETWORK,tcp))),(NETWORK,tcp))),(NETWORK,tcp)),DIRECT",
			wantErr: true,
			errMsg:  "depth exceeds limit",
		},
		{
			name:    "child rule with MATCH is forbidden",
			rule:    "AND,((MATCH,DIRECT),(NETWORK,tcp)),DIRECT",
			wantErr: true,
			errMsg:  "MATCH rule is not allowed",
		},
		{
			name:    "child rule with SUB-RULE is rejected",
			rule:    "AND,((SUB-RULE,(DOMAIN,example.com),DIRECT),(NETWORK,tcp)),DIRECT",
			wantErr: true,
			errMsg:  "SUB-RULE is not supported",
		},
		{
			name:    "unbalanced parentheses in payload",
			rule:    "AND,((DOMAIN,example.com,(NETWORK,tcp))),DIRECT",
			wantErr: true,
			errMsg:  "at least 2 child rules",
		},
		{
			name:    "missing enclosing parentheses",
			rule:    "AND,DOMAIN_example_com,DIRECT",
			wantErr: true,
			errMsg:  "expected parentheses",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				Rules: []string{tc.rule},
			}
			err := validateCompiledConfig(&cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestSubRule_Rejected(t *testing.T) {
	cfg := Config{
		Rules: []string{"SUB-RULE,(DOMAIN,example.com),DIRECT"},
	}
	err := validateCompiledConfig(&cfg)
	if err == nil {
		t.Fatal("expected SUB-RULE to be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "SUB-RULE is not supported in AWG Manager") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

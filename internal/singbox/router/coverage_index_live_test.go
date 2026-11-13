package router

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCoverageIndexSingBoxParity is opt-in because a development machine does
// not necessarily have sing-box. On the router run with AWGM_SINGBOX set to the
// real binary; it compiles temporary fixtures and changes no router state.
func TestCoverageIndexSingBoxParity(t *testing.T) {
	binary := os.Getenv("AWGM_SINGBOX")
	if binary == "" {
		t.Skip("AWGM_SINGBOX is not set")
	}
	dir := t.TempDir()
	fixtures := []struct {
		name   string
		rules  []map[string]any
		inputs []string
	}{
		{
			name: "domains",
			rules: []map[string]any{{
				"domain":         []any{"example.com"},
				"domain_suffix":  []any{"example.org"},
				"domain_keyword": []any{"video"},
				"domain_regex":   []any{`^cdn\.`},
			}},
			inputs: []string{"example.com", "www.example.com", "example.org", "a.example.org", "badexample.org", "my-video.test", "cdn.example", "other.test"},
		},
		{
			name: "addresses",
			rules: []map[string]any{
				{"ip_cidr": []any{"192.0.2.0/24", "2001:db8::/32", "198.51.100.7"}},
			},
			inputs: []string{"192.0.2.1", "192.0.3.1", "2001:db8::1", "2001:db9::1", "198.51.100.7", "198.51.100.8", "10.0.0.1", "100.64.0.1", "8.8.8.8"},
		},
		{
			name: "logical",
			rules: []map[string]any{
				{"type": "logical", "mode": "and", "rules": []any{
					map[string]any{"domain_suffix": []any{"example.net"}},
					map[string]any{"domain_keyword": []any{"login"}},
				}},
				{"type": "logical", "mode": "or", "rules": []any{
					map[string]any{"domain": []any{"exact.example"}},
					map[string]any{"domain_suffix": []any{"suffix.example"}},
				}},
			},
			inputs: []string{"login.example.net", "example.net", "other.example.net", "exact.example", "a.suffix.example", "suffix.example", "none.test"},
		},
		{
			name:   "invert",
			rules:  []map[string]any{{"domain": []any{"blocked.example"}, "invert": true}},
			inputs: []string{"blocked.example", "allowed.example"},
		},
		{
			name: "network-port",
			rules: []map[string]any{
				{"ip_cidr": []any{"34.0.0.0/15"}},
				{"network": "udp", "ip_cidr": []any{"35.192.0.0/11"}, "port_range": []any{"50000:50099"}},
			},
			inputs: []string{"34.0.0.1", "35.192.0.1", "1.1.1.1"},
		},
		{
			name:   "invert-context",
			rules:  []map[string]any{{"network": "udp", "domain": []any{"blocked.example"}, "invert": true}},
			inputs: []string{"blocked.example", "allowed.example"},
		},
		{
			name:   "case-regex",
			rules:  []map[string]any{{"domain_regex": []any{`^ABC\.`, `[A-Z]`, `^cdn\.`}}},
			inputs: []string{"ABC.EXAMPLE", "abc.example", "AbC.ExAmPlE", "CDN.EXAMPLE"},
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			_, source, err := buildInlineRuleSetSource(fixture.rules)
			if err != nil {
				t.Fatal(err)
			}
			jsonPath := filepath.Join(dir, fixture.name+".json")
			srsPath := filepath.Join(dir, fixture.name+".srs")
			if err := os.WriteFile(jsonPath, source, 0600); err != nil {
				t.Fatal(err)
			}
			_, stderr, err := inlineRuleSetCompileExec(binary, []string{"rule-set", "compile", "--output", srsPath, jsonPath})
			if err != nil {
				t.Fatalf("compile: %v: %s", err, stderr)
			}
			entry, err := compileCoverageIndexEntry(fixture.rules)
			if err != nil {
				t.Fatal(err)
			}
			rs := RuleSet{Tag: fixture.name, Type: "local", Format: "binary", Path: srsPath}
			for _, input := range fixture.inputs {
				cliMatch, supported, cliErr := matchRuleSet(input, rs, binary, nil, nil)
				if cliErr != nil || !supported {
					t.Fatalf("CLI %s: matched=%v supported=%v err=%v", input, cliMatch, supported, cliErr)
				}
				indexed := entry.match(input)
				if indexed != cliMatch {
					t.Errorf("input %s: CLI=%v indexed=%v", input, cliMatch, indexed)
				}
			}
		})
	}
}

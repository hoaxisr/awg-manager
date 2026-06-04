package router

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withFakeRuleSetCompiler(t *testing.T, fn func(binary string, args []string) (string, string, error)) {
	t.Helper()
	old := inlineRuleSetCompileExec
	inlineRuleSetCompileExec = fn
	t.Cleanup(func() { inlineRuleSetCompileExec = old })
}

func writeCompiledOutput(t *testing.T, args []string, body string) {
	t.Helper()
	out := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--output" {
			out = args[i+1]
			break
		}
	}
	if out == "" {
		t.Fatalf("compile args missing --output: %v", args)
	}
	if err := os.WriteFile(out, []byte(body), 0644); err != nil {
		t.Fatalf("write compiled output: %v", err)
	}
}

func TestInlineRuleSetMaterializer_CompilesLocalBinary(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		calls++
		if binary != "/opt/bin/sing-box" {
			t.Fatalf("binary = %q", binary)
		}
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	rs := RuleSet{
		Tag:  "geosite/example",
		Type: "inline",
		Rules: []map[string]any{
			{"domain_suffix": []any{"example.com"}},
			{"domain_suffix": []any{"example.com"}},
			{"domain_suffix": []any{"example.org"}},
		},
	}
	got, err := m.materializeRuleSet(rs)
	if err != nil {
		t.Fatalf("materializeRuleSet: %v", err)
	}
	wantTag := inlineSRSTag(rs.Tag)
	if got.Tag != wantTag || got.Type != "local" || got.Format != "binary" {
		t.Fatalf("unexpected materialized ruleset: %+v", got)
	}
	wantPath := filepath.Join(dir, "rule-sets", "inline", "geosite-example.srs")
	if got.Path != wantPath {
		t.Fatalf("path = %q, want %q", got.Path, wantPath)
	}
	if _, err := os.Stat(got.Path); err != nil {
		t.Fatalf("compiled .srs missing: %v", err)
	}

	raw, err := os.ReadFile(strings.TrimSuffix(got.Path, ".srs") + ".json")
	if err != nil {
		t.Fatalf("source json missing: %v", err)
	}
	var source inlineRuleSetSource
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatalf("source json invalid: %v", err)
	}
	if source.Version != inlineRuleSetSourceVersion {
		t.Fatalf("version = %d", source.Version)
	}
	if len(source.Rules) != 2 {
		t.Fatalf("deduped rules len = %d, rules=%v", len(source.Rules), source.Rules)
	}

	again, err := m.materializeRuleSet(rs)
	if err != nil {
		t.Fatalf("second materializeRuleSet: %v", err)
	}
	if again.Path != got.Path {
		t.Fatalf("stable path changed: %q -> %q", got.Path, again.Path)
	}
	if calls != 2 {
		t.Fatalf("expected compile on each save (overwrite), got %d", calls)
	}
}

func TestInlineRuleSetMaterializer_CompileErrorDoesNotPublishBinary(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		return "", "bad rule", errors.New("exit status 1")
	})

	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	_, err := m.materializeRuleSet(RuleSet{
		Tag:   "bad",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "bad rule") {
		t.Fatalf("expected compile error with stderr, got %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(dir, "rule-sets", "inline", "*.srs"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("unexpected published .srs after failed compile: %v", matches)
	}
}

func TestInlineRuleSetMaterializer_RestoresManagedLocalAsInline(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	inline := RuleSet{
		Tag:   "inline-a",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	}
	local, err := m.materializeRuleSet(inline)
	if err != nil {
		t.Fatal(err)
	}
	restored := m.restoreRuleSet(local)
	if restored.Type != "inline" || restored.Tag != inline.Tag || len(restored.Rules) != 1 {
		t.Fatalf("unexpected restored ruleset: %+v", restored)
	}
	if !restored.MaterializedSRS {
		t.Fatal("expected materialized_srs on restore")
	}
}

func TestInlineRuleSetMaterializer_RestoreConfigHidesSRSCompanion(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	inline := RuleSet{
		Tag:   "pair",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{"example.com"}}},
	}
	local, err := m.materializeRuleSet(inline)
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{inline, local}
	out := m.restoreConfig(cfg)
	if len(out.Route.RuleSet) != 1 {
		t.Fatalf("want one visible ruleset, got %d: %+v", len(out.Route.RuleSet), out.Route.RuleSet)
	}
	if out.Route.RuleSet[0].Tag != "pair" || out.Route.RuleSet[0].Type != "inline" || !out.Route.RuleSet[0].MaterializedSRS {
		t.Fatalf("unexpected list projection: %+v", out.Route.RuleSet[0])
	}
}

func TestInlineRuleSetMaterializer_MaterializeConfigWritesSRSCompanion(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:   "foo",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	}}
	out, err := m.materializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Route.RuleSet) != 1 {
		t.Fatalf("rule_set len = %d", len(out.Route.RuleSet))
	}
	if out.Route.RuleSet[0].Tag != "foo-srs" || out.Route.RuleSet[0].Type != "local" {
		t.Fatalf("unexpected persisted ruleset: %+v", out.Route.RuleSet[0])
	}
}

func TestInlineRuleSetMaterializer_RemoveInlineArtifacts(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	_, err := m.materializeRuleSet(RuleSet{
		Tag:   "gone",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.removeInlineArtifacts("gone")
	for _, ext := range []string{".json", ".srs"} {
		if _, err := os.Stat(filepath.Join(dir, "rule-sets", "inline", "gone"+ext)); !os.IsNotExist(err) {
			t.Fatalf("expected gone%s removed, stat err=%v", ext, err)
		}
	}
}

func TestInlineRuleSetMaterializer_RewritesRuleRefsOnMaterialize(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:   "foo",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	}}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"foo"}, Action: "route", Outbound: "direct"}}
	out, err := m.materializeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Route.Rules) != 1 || len(out.Route.Rules[0].RuleSet) != 1 || out.Route.Rules[0].RuleSet[0] != "foo-srs" {
		t.Fatalf("route rule_set refs = %+v", out.Route.Rules[0].RuleSet)
	}
}

func TestInlineRuleSetMaterializer_RestoreRewritesSRSRefsToInline(t *testing.T) {
	dir := t.TempDir()
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})
	m := ruleSetMaterializer{configDir: dir, binary: "/opt/bin/sing-box"}
	local, err := m.materializeRuleSet(RuleSet{
		Tag:   "foo",
		Type:  "inline",
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{local}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"foo-srs"}, Action: "route", Outbound: "direct"}}
	out := m.restoreConfig(cfg)
	if len(out.Route.Rules) != 1 || out.Route.Rules[0].RuleSet[0] != "foo" {
		t.Fatalf("restored route rule_set refs = %+v", out.Route.Rules[0].RuleSet)
	}
}

func TestInlineRuleSetMaterializer_RestoreRewritesSRSRefsWithoutManagedEntryInOutput(t *testing.T) {
	// Simulates persisted config: only foo-srs in rule_set[], refs still foo-srs.
	m := ruleSetMaterializer{configDir: t.TempDir(), binary: "/opt/bin/sing-box"}
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{{
		Tag:    "geosite-samsung-srs",
		Type:   "local",
		Format: "binary",
		Path:   filepath.Join(m.configDir, "rule-sets", "inline", "geosite-samsung.srs"),
	}}
	cfg.Route.Rules = []Rule{{RuleSet: []string{"geosite-samsung-srs"}, Action: "route", Outbound: "direct"}}
	out := m.restoreConfig(cfg)
	if len(out.Route.RuleSet) != 1 || out.Route.RuleSet[0].Tag != "geosite-samsung" {
		t.Fatalf("rule_set projection = %+v", out.Route.RuleSet)
	}
	if len(out.Route.Rules) != 1 || out.Route.Rules[0].RuleSet[0] != "geosite-samsung" {
		t.Fatalf("rule refs = %+v", out.Route.Rules[0].RuleSet)
	}
}

func TestDeleteRuleSet_RemovesCompanionTag(t *testing.T) {
	cfg := NewEmptyConfig()
	cfg.Route.RuleSet = []RuleSet{
		{Tag: "foo", Type: "inline", Rules: []map[string]any{{"domain_suffix": []any{".x"}}}},
		{Tag: "foo-srs", Type: "local", Format: "binary", Path: "/tmp/foo.srs"},
	}
	if err := cfg.DeleteRuleSet("foo", false); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Route.RuleSet) != 0 {
		t.Fatalf("expected empty rule sets, got %+v", cfg.Route.RuleSet)
	}
}

// TestMapTagSlice locks the shared rewriter skeleton: unchanged input returns
// the SAME backing slice (no churn), changed input returns a new slice.
func TestMapTagSlice(t *testing.T) {
	in := []string{"a", "b", "c"}
	same := mapTagSlice(in, func(s string) (string, bool) { return "", false })
	if &same[0] != &in[0] {
		t.Error("unchanged input must return the original slice (same backing array)")
	}
	got := mapTagSlice(in, func(s string) (string, bool) {
		if s == "b" {
			return "B", true
		}
		return "", false
	})
	if got[1] != "B" || got[0] != "a" || got[2] != "c" {
		t.Errorf("got %v, want [a B c]", got)
	}
	if &got[0] == &in[0] {
		t.Error("changed input must return a new slice")
	}
	// rewriteTagSlice keeps its from==to / empty guards.
	if r := rewriteTagSlice(in, "a", "a"); &r[0] != &in[0] {
		t.Error("rewriteTagSlice from==to must be a no-op returning original")
	}
}

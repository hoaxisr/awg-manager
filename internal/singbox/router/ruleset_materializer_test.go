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
			{"domain_suffix": []any{".example.com"}},
			{"domain_suffix": []any{".example.com"}},
			{"domain_suffix": []any{".example.org"}},
		},
	}
	got, err := m.materializeRuleSet(rs)
	if err != nil {
		t.Fatalf("materializeRuleSet: %v", err)
	}
	if got.Tag != rs.Tag || got.Type != "local" || got.Format != "binary" {
		t.Fatalf("unexpected materialized ruleset: %+v", got)
	}
	if !strings.HasPrefix(got.Path, filepath.Join(dir, "rule-sets", "inline")) {
		t.Fatalf("path outside managed dir: %q", got.Path)
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
		t.Fatalf("content-addressed path changed: %q -> %q", got.Path, again.Path)
	}
	if calls != 1 {
		t.Fatalf("expected compile once with cached artifact, got %d", calls)
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
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
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
		Rules: []map[string]any{{"domain_suffix": []any{".example.com"}}},
	}
	local, err := m.materializeRuleSet(inline)
	if err != nil {
		t.Fatal(err)
	}
	restored := m.restoreRuleSet(local)
	if restored.Type != "inline" || restored.Tag != inline.Tag || len(restored.Rules) != 1 {
		t.Fatalf("unexpected restored ruleset: %+v", restored)
	}
}

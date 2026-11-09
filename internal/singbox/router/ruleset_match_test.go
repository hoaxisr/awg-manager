package router

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMatchRuleSets_ReportsPerTagWithoutAborting(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"hit.srs", "miss.srs", "broken.srs"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	origExec := ruleSetMatchExec
	ruleSetMatchExec = func(_ string, args []string) (string, string, error) {
		switch filepath.Base(args[len(args)-2]) {
		case "hit.srs":
			return "", "match rules.\n", nil
		case "broken.srs":
			return "", "FATAL broken rule-set", errors.New("exit status 1")
		default:
			return "", "", nil
		}
	}
	defer func() { ruleSetMatchExec = origExec }()

	sets := []RuleSet{
		{Tag: "hit", Type: "local", Path: filepath.Join(dir, "hit.srs"), Format: "binary"},
		{Tag: "miss", Type: "local", Path: filepath.Join(dir, "miss.srs"), Format: "binary"},
		{Tag: "broken", Type: "local", Path: filepath.Join(dir, "broken.srs"), Format: "binary"},
		{Tag: "unavailable", Type: "local", Path: filepath.Join(dir, "missing.srs"), Format: "binary"},
	}
	tags := []string{"unavailable", "hit", "unknown", "broken", "miss"}
	got := MatchRuleSets("example.com", tags, sets, "/usr/bin/sing-box", nil)
	want := []RuleSetMatchResult{
		{Tag: "unavailable", Error: "rule-set cannot be evaluated: sing-box binary or rule-set file is unavailable, or the type is unsupported"},
		{Tag: "hit", Matched: true, Supported: true},
		{Tag: "unknown", Error: "rule-set is not defined"},
		{Tag: "broken", Supported: true, Error: "sing-box: FATAL broken rule-set"},
		{Tag: "miss", Supported: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchRuleSets() = %#v, want %#v", got, want)
	}
}

func TestMatchRuleSets_EmptySelectionReturnsEmptyNonNilSlice(t *testing.T) {
	got := MatchRuleSets("example.com", nil, nil, "", nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("MatchRuleSets() = %#v, want non-nil empty slice", got)
	}
}

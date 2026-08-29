package router

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestParseDatRuleSetURL(t *testing.T) {
	kind, tags, ok := parseDatRuleSetURL("http://127.0.0.1:8080/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE&tag=YOUTUBE&token=x")
	if !ok {
		t.Fatal("expected dat URL to parse")
	}
	if kind != "geosite" || len(tags) != 2 || tags[0] != "GOOGLE" || tags[1] != "YOUTUBE" {
		t.Fatalf("got kind=%q tags=%v", kind, tags)
	}
	if _, _, ok := parseDatRuleSetURL("https://example.com/geosite-youtube.srs"); ok {
		t.Fatal("external URL must not parse as dat")
	}
}

type fakeGeoExpander struct {
	lines []string
	path  string
	err   error
}

func (f fakeGeoExpander) ExpandGeoTag(_, _ string) ([]string, string, error) {
	return f.lines, f.path, f.err
}

func (f fakeGeoExpander) ExpandGeoTagTyped(kind, tag string) ([]string, string, error) {
	return f.ExpandGeoTag(kind, tag)
}

type fakeGeoExpanderByTag struct {
	lines map[string][]string
	path  string
	calls []string
}

func (f *fakeGeoExpanderByTag) ExpandGeoTag(_ string, tag string) ([]string, string, error) {
	f.calls = append(f.calls, tag)
	return f.lines[tag], f.path, nil
}

func (f *fakeGeoExpanderByTag) ExpandGeoTagTyped(kind, tag string) ([]string, string, error) {
	return f.ExpandGeoTag(kind, tag)
}

// TestDatLinesToRuleSetRules_GeositeTypedMapping pins the typed dat→SRS
// mapping (issue #448): keyword:→domain_keyword, full:→domain,
// domain_regex:→domain_regex, dot-prefixed→domain_suffix PLUS the apex as an
// exact domain (sing-box domain_suffix ".x" does NOT match the apex), legacy
// domain:/suffix:/domain_keyword: prefixes kept, bare values fall back to
// domain_suffix.
func TestDatLinesToRuleSetRules_GeositeTypedMapping(t *testing.T) {
	rules, err := datLinesToRuleSetRules("geosite", []string{
		"keyword:google",
		"full:chatgpt.com",
		`domain_regex:^ads\.`,
		".openai.com",
		"bare.example",
		"domain:legacy-domain.example",
		"suffix:legacy-suffix.example",
		"domain_keyword:legacykw",
	})
	if err != nil {
		t.Fatalf("datLinesToRuleSetRules: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules = %+v, want single headless rule", rules)
	}
	rule := rules[0]
	check := func(field string, want []string) {
		t.Helper()
		got, _ := rule[field].([]string)
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", field, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s[%d] = %q, want %q", field, i, got[i], want[i])
			}
		}
	}
	check("domain_keyword", []string{"google", "legacykw"})
	// full: and the apex of dot-prefixed entries are EXACT domain matches.
	check("domain", []string{"chatgpt.com", "openai.com", "legacy-domain.example"})
	check("domain_suffix", []string{".openai.com", "bare.example", "legacy-suffix.example"})
	check("domain_regex", []string{`^ads\.`})
}

// TestDatRuleSetFile_StaleFormatVersionRecompiles verifies the cache
// invalidation for .srs artifacts compiled with the pre-#448 (lossy) mapping:
// a meta.json without formatVersion (or with an older one) must not validate
// against the current datRuleSetFormatVersion, forcing a recompile.
func TestDatRuleSetFile_StaleFormatVersionRecompiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "geosite.dat")
	if err := os.WriteFile(source, []byte("dat"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	settings := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	svc := &ServiceImpl{deps: Deps{
		Settings: settings,
		Singbox:  &fakeSingbox{dir: filepath.Join(dir, "config.d"), binary: "sing-box"},
		GeoData: fakeGeoExpander{
			lines: []string{".example.com"},
			path:  source,
		},
	}}
	u, err := svc.DatRuleSetURL(context.Background(), "geosite", []string{"EXAMPLE"})
	if err != nil {
		t.Fatalf("DatRuleSetURL: %v", err)
	}
	token := u[strings.LastIndex(u, "token=")+len("token="):]

	compileCalls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		compileCalls++
		writeCompiledOutput(t, args, "compiled")
		return "", "", nil
	})

	srsPath, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"EXAMPLE"}, token)
	if err != nil {
		t.Fatalf("DatRuleSetFile: %v", err)
	}
	if compileCalls != 1 {
		t.Fatalf("compileCalls = %d, want 1", compileCalls)
	}

	// Simulate an artifact from an older AWGM: strip formatVersion from meta.
	metaPath := strings.TrimSuffix(srsPath, ".srs") + datRuleSetMetaExt
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	if got, _ := meta["formatVersion"].(float64); int(got) != datRuleSetFormatVersion {
		t.Fatalf("meta formatVersion = %v, want %d", meta["formatVersion"], datRuleSetFormatVersion)
	}
	delete(meta, "formatVersion")
	stale, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal stale meta: %v", err)
	}
	if err := os.WriteFile(metaPath, stale, 0644); err != nil {
		t.Fatalf("write stale meta: %v", err)
	}

	if _, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"EXAMPLE"}, token); err != nil {
		t.Fatalf("DatRuleSetFile after stale meta: %v", err)
	}
	if compileCalls != 2 {
		t.Fatalf("compileCalls = %d, want 2 (stale format version must recompile)", compileCalls)
	}
	// Fresh meta carries the current version — cache holds again.
	if _, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"EXAMPLE"}, token); err != nil {
		t.Fatalf("DatRuleSetFile third: %v", err)
	}
	if compileCalls != 2 {
		t.Fatalf("compileCalls = %d, want 2 (recompiled meta must cache-validate)", compileCalls)
	}
}

func TestDatRuleSetURL_UsesLocalhostPortAndToken(t *testing.T) {
	settings := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	if err := settings.Update(func(cur *storage.Settings) error {
		cur.Server.Port = 3456
		return nil
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	svc := &ServiceImpl{deps: Deps{
		Settings: settings,
		Singbox:  &fakeSingbox{dir: t.TempDir()},
	}}
	u, err := svc.DatRuleSetURL(context.Background(), "geosite", []string{"GOOGLE"})
	if err != nil {
		t.Fatalf("DatRuleSetURL: %v", err)
	}
	if !strings.HasPrefix(u, "http://127.0.0.1:3456/api/singbox/router/rulesets/dat-srs?") {
		t.Fatalf("url = %q", u)
	}
	if !strings.Contains(u, "kind=geosite") || !strings.Contains(u, "tag=GOOGLE") || !strings.Contains(u, "token=") {
		t.Fatalf("url missing expected query params: %q", u)
	}
}

func TestDatRuleSetFile_RejectsBadToken(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{
		Singbox: &fakeSingbox{dir: t.TempDir(), binary: "sing-box"},
	}}
	if _, err := svc.DatRuleSetFile(context.Background(), "geoip", []string{"RU"}, "bad"); err != ErrDatRuleSetForbidden {
		t.Fatalf("err = %v, want ErrDatRuleSetForbidden", err)
	}
}

func TestDatRuleSetFile_CompilesAndCaches(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "geosite.dat")
	if err := os.WriteFile(source, []byte("dat"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	settings := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	svc := &ServiceImpl{deps: Deps{
		Settings: settings,
		Singbox:  &fakeSingbox{dir: filepath.Join(dir, "config.d"), binary: "sing-box"},
		GeoData: fakeGeoExpander{
			lines: []string{".example.com", "domain_regex:^x\\.example$"},
			path:  source,
		},
	}}
	u, err := svc.DatRuleSetURL(context.Background(), "geosite", []string{"EXAMPLE"})
	if err != nil {
		t.Fatalf("DatRuleSetURL: %v", err)
	}
	token := u[strings.LastIndex(u, "token=")+len("token="):]

	compileCalls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		compileCalls++
		if binary != "sing-box" {
			t.Fatalf("binary = %q", binary)
		}
		out := args[3]
		if err := os.WriteFile(out, []byte("compiled"), 0644); err != nil {
			t.Fatalf("write compiled: %v", err)
		}
		return "", "", nil
	})

	first, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"EXAMPLE"}, token)
	if err != nil {
		t.Fatalf("DatRuleSetFile first: %v", err)
	}
	second, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"EXAMPLE"}, token)
	if err != nil {
		t.Fatalf("DatRuleSetFile second: %v", err)
	}
	if first != second {
		t.Fatalf("paths differ: %q vs %q", first, second)
	}
	if compileCalls != 1 {
		t.Fatalf("compileCalls = %d, want 1", compileCalls)
	}
}

func TestDatRuleSetFile_CompilesMultipleTagsAsOneRuleSet(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "geosite.dat")
	if err := os.WriteFile(source, []byte("dat"), 0644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	settings := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	expander := &fakeGeoExpanderByTag{
		lines: map[string][]string{
			"GOOGLE":  {".google.com", ".youtube.com"},
			"YOUTUBE": {".youtube.com", "domain:youtube.com"},
		},
		path: source,
	}
	svc := &ServiceImpl{deps: Deps{
		Settings: settings,
		Singbox:  &fakeSingbox{dir: filepath.Join(dir, "config.d"), binary: "sing-box"},
		GeoData:  expander,
	}}
	u, err := svc.DatRuleSetURL(context.Background(), "geosite", []string{"GOOGLE", "YOUTUBE"})
	if err != nil {
		t.Fatalf("DatRuleSetURL: %v", err)
	}
	if !strings.Contains(u, "tag=GOOGLE") || !strings.Contains(u, "tag=YOUTUBE") {
		t.Fatalf("url missing multi-tag query params: %q", u)
	}
	token := u[strings.LastIndex(u, "token=")+len("token="):]

	compileCalls := 0
	withFakeRuleSetCompiler(t, func(binary string, args []string) (string, string, error) {
		compileCalls++
		sourceJSON, err := os.ReadFile(args[4])
		if err != nil {
			t.Fatalf("read source json: %v", err)
		}
		text := string(sourceJSON)
		if strings.Count(text, ".youtube.com") != 1 {
			t.Fatalf("source JSON should dedupe duplicate lines, got: %s", text)
		}
		out := args[3]
		if err := os.WriteFile(out, []byte("compiled"), 0644); err != nil {
			t.Fatalf("write compiled: %v", err)
		}
		return "", "", nil
	})

	first, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"GOOGLE", "YOUTUBE"}, token)
	if err != nil {
		t.Fatalf("DatRuleSetFile first: %v", err)
	}
	second, err := svc.DatRuleSetFile(context.Background(), "geosite", []string{"GOOGLE", "YOUTUBE"}, token)
	if err != nil {
		t.Fatalf("DatRuleSetFile second: %v", err)
	}
	if first != second {
		t.Fatalf("paths differ: %q vs %q", first, second)
	}
	if compileCalls != 1 {
		t.Fatalf("compileCalls = %d, want 1", compileCalls)
	}
	if got := strings.Join(expander.calls, ","); got != "GOOGLE,YOUTUBE,GOOGLE,YOUTUBE" {
		t.Fatalf("ExpandGeoTag calls = %q", got)
	}
}

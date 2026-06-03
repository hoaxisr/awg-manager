package router

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeGeoExpander struct {
	lines []string
	path  string
	err   error
}

func (f fakeGeoExpander) ExpandGeoTag(_, _ string) ([]string, string, error) {
	return f.lines, f.path, f.err
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

func TestDatRuleSetURL_UsesLocalhostPortAndToken(t *testing.T) {
	settings := newTestSettingsStore(t, storage.SingboxRouterSettings{})
	all, err := settings.Get()
	if err != nil {
		t.Fatalf("settings.Get: %v", err)
	}
	all.Server.Port = 3456
	if err := settings.Save(all); err != nil {
		t.Fatalf("settings.Save: %v", err)
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

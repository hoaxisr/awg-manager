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
	u, err := svc.DatRuleSetURL(context.Background(), "geosite", "GOOGLE")
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
	if _, err := svc.DatRuleSetFile(context.Background(), "geoip", "RU", "bad"); err != ErrDatRuleSetForbidden {
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
	u, err := svc.DatRuleSetURL(context.Background(), "geosite", "EXAMPLE")
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

	first, err := svc.DatRuleSetFile(context.Background(), "geosite", "EXAMPLE", token)
	if err != nil {
		t.Fatalf("DatRuleSetFile first: %v", err)
	}
	second, err := svc.DatRuleSetFile(context.Background(), "geosite", "EXAMPLE", token)
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

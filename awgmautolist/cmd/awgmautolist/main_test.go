package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/migrate"
)

type flakyLANDiscovery struct {
	calls int
}

func (f *flakyLANDiscovery) LANBridges(context.Context) ([]string, error) {
	f.calls++
	if f.calls < 3 {
		return nil, errors.New("AWGM is still booting")
	}
	return []string{"br0"}, nil
}

func TestDiscoverLANBridgesRetriesUntilAvailable(t *testing.T) {
	old := lanDiscoveryRetry
	lanDiscoveryRetry = time.Millisecond
	t.Cleanup(func() { lanDiscoveryRetry = old })
	client := &flakyLANDiscovery{}
	interfaces, err := discoverLANBridges(context.Background(), client, log.New(&discardWriter{}, "", 0))
	if err != nil || len(interfaces) != 1 || client.calls != 3 {
		t.Fatalf("interfaces=%v calls=%d err=%v", interfaces, client.calls, err)
	}
}

func TestDiscoverLANBridgesHonorsCancellation(t *testing.T) {
	old := lanDiscoveryRetry
	lanDiscoveryRetry = 20 * time.Millisecond
	t.Cleanup(func() { lanDiscoveryRetry = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := discoverLANBridges(ctx, &flakyLANDiscovery{}, log.New(&discardWriter{}, "", 0))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Legacy flat files move into the config directory before the stores open
// them: the config path is the new one, and the old flat files must not
// survive as stale unused copies. main.go wires LegacyFlat() as the source
// and filepath.Dir(*configPath) as the target; the same shapes are exercised
// here with temp paths.
func TestLegacyFlatFilesMoveToConfigDir(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := migrate.Flat{
		Config:  filepath.Join(dir, "awgmautolist.json"),
		State:   filepath.Join(dir, "awgmautolist-state.json"),
		History: filepath.Join(dir, "awgmautolist-history.json"),
	}
	writeFile("awgmautolist.json", `{"autoAdd":false}`)
	writeFile("awgmautolist-state.json", `{"history":[]}`)
	writeFile("awgmautolist-history.json", `[{"domain":"example.com"}]`)

	moved, err := migrate.FromFlatFiles(dir, legacy, nil)
	if err != nil || moved != 3 {
		t.Fatalf("moved=%d err=%v", moved, err)
	}
	for _, name := range []string{"config.json", "state.json", "history.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"awgmautolist.json", "awgmautolist-state.json", "awgmautolist-history.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived migration: %v", name, err)
		}
	}
}

// The state path follows the layout: in the config directory config.json is
// paired with state.json (not the flat "config-state.json"); a custom -config
// outside the directory keeps the legacy "-state.json" derivation.
func TestStatePathFor(t *testing.T) {
	if got := statePathFor("/opt/etc/awgmautolist/config.json"); got != "/opt/etc/awgmautolist/state.json" {
		t.Fatalf("dir layout: statePath = %q", got)
	}
	got := statePathFor(filepath.Join(t.TempDir(), "custom.json"))
	if base := filepath.Base(got); base != "custom-state.json" {
		t.Fatalf("custom layout: base = %q", base)
	}
}

// AWGM_AUTOLIST_DIR overrides the default directory (see DefaultDir in the
// migrate package); here the wiring through configDir is verified.
func TestConfigDirHonorsEnv(t *testing.T) {
	old, had := os.LookupEnv("AWGM_AUTOLIST_DIR")
	defer func() {
		if had {
			_ = os.Setenv("AWGM_AUTOLIST_DIR", old)
		} else {
			_ = os.Unsetenv("AWGM_AUTOLIST_DIR")
		}
	}()
	if err := os.Setenv("AWGM_AUTOLIST_DIR", "/tmp/awgm-autolist-test"); err != nil {
		t.Fatal(err)
	}
	if got := configDir(); got != "/tmp/awgm-autolist-test" {
		t.Fatalf("configDir = %q", got)
	}
	_ = os.Unsetenv("AWGM_AUTOLIST_DIR")
	if got := configDir(); got != "/opt/etc/awgmautolist" {
		t.Fatalf("default configDir = %q", got)
	}
}

// The flag wins over env, env over settings.json, settings over 2222.
func TestResolveRouterURLFallbackChain(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	logOut := &discardWriter{}
	logger := log.New(logOut, "", 0)

	t.Run("explicit flag wins", func(t *testing.T) {
		if err := os.Setenv("AWGM_ROUTER_URL", "http://127.0.0.1:9999"); err != nil {
			t.Fatal(err)
		}
		defer os.Unsetenv("AWGM_ROUTER_URL")
		if got := resolveRouterURL("http://127.0.0.1:7777", logger); got != "http://127.0.0.1:7777" {
			t.Fatalf("url = %q", got)
		}
	})
	t.Run("env wins over settings", func(t *testing.T) {
		if err := os.Setenv("AWGM_ROUTER_URL", "http://127.0.0.1:9999"); err != nil {
			t.Fatal(err)
		}
		defer os.Unsetenv("AWGM_ROUTER_URL")
		if got := resolveRouterURL(defaultRouterFlagValue, logger); got != "http://127.0.0.1:9999" {
			t.Fatalf("url = %q", got)
		}
	})
	t.Run("settings port used without env", func(t *testing.T) {
		if err := os.WriteFile(settings, []byte(`{"schemaVersion":41,"server":{"port":8123,"interface":"br0"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		old := DefaultPanelSettingsPath
		DefaultPanelSettingsPath = settings
		t.Cleanup(func() { DefaultPanelSettingsPath = old })
		if got := resolveRouterURL(defaultRouterFlagValue, logger); got != "http://127.0.0.1:8123" {
			t.Fatalf("url = %q", got)
		}
	})
	t.Run("unreadable settings fall back to 2222", func(t *testing.T) {
		old := DefaultPanelSettingsPath
		DefaultPanelSettingsPath = filepath.Join(dir, "missing.json")
		t.Cleanup(func() { DefaultPanelSettingsPath = old })
		if got := resolveRouterURL(defaultRouterFlagValue, logger); got != "http://127.0.0.1:2222" {
			t.Fatalf("url = %q", got)
		}
	})
}

// settings.json of the panel: valid JSON with a port, no server block,
// port 0, broken JSON and a missing file all fall back to 2222.
func TestParseServerPort(t *testing.T) {
	dir := t.TempDir()
	write := func(data string) string {
		t.Helper()
		path := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	port, err := parseServerPort(write(`{"schemaVersion":41,"server":{"port":8080,"interface":"br0"},"pingCheck":{}}`))
	if err != nil || port != 8080 {
		t.Fatalf("port=%d err=%v", port, err)
	}
	port, err = parseServerPort(write(`{"schemaVersion":41,"pingCheck":{}}`))
	if err == nil || port != 0 {
		t.Fatalf("no server: port=%d err=%v", port, err)
	}
	port, err = parseServerPort(write(`{"schemaVersion":41,"server":{"port":0}}`))
	if err == nil || port != 0 {
		t.Fatalf("port 0: port=%d err=%v", port, err)
	}
	port, err = parseServerPort(write(`{"server":{"port": `))
	if err == nil || port != 0 {
		t.Fatalf("broken JSON: port=%d err=%v", port, err)
	}
	port, err = parseServerPort(filepath.Join(dir, "missing.json"))
	if err == nil || port != 0 {
		t.Fatalf("missing file: port=%d err=%v", port, err)
	}
	// port внутри другого блока (не server) не читается.
	port, err = parseServerPort(write(`{"singboxRouter":{"clashPort":9100,"listenPort":9101}}`))
	if err == nil || port != 0 {
		t.Fatalf("foreign port: port=%d err=%v", port, err)
	}
}

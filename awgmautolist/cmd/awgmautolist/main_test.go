package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// initScriptPath locates the Entware lifecycle script relative to the test
// file (cmd/awgmautolist -> ../../entware).
func initScriptPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "entware", "S99awgmautolist")
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("init script not available: %v", err)
	}
	return abs
}

// rewriteInitScript copies the init script with the absolute Entware prefixes
// replaced by a temporary root so the migration and sourcing can run without
// touching the real /opt.
func rewriteInitScript(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(initScriptPath(t))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(data), "/opt/etc", filepath.Join(root, "etc"))
	script = strings.ReplaceAll(script, "/opt/var", filepath.Join(root, "var"))
	script = strings.ReplaceAll(script, "/opt/bin/awgmautolist", filepath.Join(root, "bin", "awgmautolist"))
	path := filepath.Join(root, "S99awgmautolist")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// The init script migrates the legacy flat key/enabled into the working
// directory before sourcing them: the moved files keep content and
// permissions, the legacy paths disappear and the migrated intent is what the
// script actually sources.
func TestInitScriptMigratesLegacyShellFilesBeforeSourcing(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "awgmautolist.key"), []byte("AWGM_API_KEY=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "awgmautolist.enabled"), []byte("ENABLED=no"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := rewriteInitScript(t, root)

	// "start" proves sourcing: ENABLED=no from the migrated file stops the
	// start path with the disabled message.
	out, err := exec.Command("sh", script, "start").CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "disabled") {
		t.Fatalf("migrated enabled file was not sourced: %q", out)
	}
	key, err := os.ReadFile(filepath.Join(etc, "awgmautolist", "key"))
	if err != nil || string(key) != "AWGM_API_KEY=secret" {
		t.Fatalf("migrated key=%q err=%v", key, err)
	}
	info, err := os.Stat(filepath.Join(etc, "awgmautolist", "key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key perm=%v err=%v", info, err)
	}
	for _, legacy := range []string{"awgmautolist.key", "awgmautolist.enabled"} {
		if _, err := os.Stat(filepath.Join(etc, legacy)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy %s survived the move: %v", legacy, err)
		}
	}
}

// A fresh key/enabled in the working directory has priority: the init script
// neither moves nor reads the legacy file and never overwrites the new one.
func TestInitScriptKeepsFreshShellFiles(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	confDir := filepath.Join(etc, "awgmautolist")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "key"), []byte("AWGM_API_KEY=new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "awgmautolist.key"), []byte("AWGM_API_KEY=old"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := rewriteInitScript(t, root)

	out, err := exec.Command("sh", script, "status").CombinedOutput()
	if err != nil {
		t.Fatalf("script: %v (%s)", err, out)
	}
	key, err := os.ReadFile(filepath.Join(confDir, "key"))
	if err != nil || string(key) != "AWGM_API_KEY=new" {
		t.Fatalf("fresh key overwritten: %q err=%v", key, err)
	}
	// The legacy key stays on disk as an unread leftover.
	legacy, err := os.ReadFile(filepath.Join(etc, "awgmautolist.key"))
	if err != nil || string(legacy) != "AWGM_API_KEY=old" {
		t.Fatalf("legacy key changed: %q err=%v", legacy, err)
	}
}

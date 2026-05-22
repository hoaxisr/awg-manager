package singbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

// Sentinel error used by preflight tests to assert validator delegation.
var errSyntheticValidator = errors.New("synthetic validator")

func TestParseSingboxVersionOutput(t *testing.T) {
	t.Run("typical 1.13.x output", func(t *testing.T) {
		out := "sing-box version 1.13.8\n" +
			"\n" +
			"Environment: go1.25.9 linux/arm64\n" +
			"Tags: with_gvisor,with_quic,with_dhcp,with_wireguard,with_utls,with_acme,with_clash_api,with_tailscale,with_ccm,with_ocm,with_naive_outbound,badlinkname,tfogo_checklinkname0,with_musl\n" +
			"Revision: d5adb54bc6c6b2c21ab6f748276c4ec62d9bb650\n" +
			"CGO: enabled\n"
		version, features := parseSingboxVersionOutput(out)
		if version != "1.13.8" {
			t.Errorf("version = %q, want 1.13.8", version)
		}
		wantFeatures := []string{
			"with_gvisor", "with_quic", "with_dhcp", "with_wireguard",
			"with_utls", "with_acme", "with_clash_api", "with_tailscale",
			"with_ccm", "with_ocm", "with_naive_outbound", "badlinkname",
			"tfogo_checklinkname0", "with_musl",
		}
		if !reflect.DeepEqual(features, wantFeatures) {
			t.Errorf("features mismatch:\n  got  %v\n  want %v", features, wantFeatures)
		}
	})

	t.Run("missing Tags line — version only", func(t *testing.T) {
		out := "sing-box version 1.10.0\nEnvironment: go1.22 linux/amd64\n"
		version, features := parseSingboxVersionOutput(out)
		if version != "1.10.0" {
			t.Errorf("version = %q", version)
		}
		if len(features) != 0 {
			t.Errorf("features = %v, want empty", features)
		}
	})

	t.Run("tags with spaces around commas", func(t *testing.T) {
		out := "sing-box version 1.0\nTags: with_a , with_b ,with_c\n"
		_, features := parseSingboxVersionOutput(out)
		want := []string{"with_a", "with_b", "with_c"}
		if !reflect.DeepEqual(features, want) {
			t.Errorf("features = %v, want %v", features, want)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		v, f := parseSingboxVersionOutput("")
		if v != "" || f != nil {
			t.Errorf("want empty, got version=%q features=%v", v, f)
		}
	})

	t.Run("version line alone", func(t *testing.T) {
		v, f := parseSingboxVersionOutput("sing-box version 1.2.3\n")
		if v != "1.2.3" {
			t.Errorf("version = %q", v)
		}
		if len(f) != 0 {
			t.Errorf("features = %v, want empty", f)
		}
	})

	t.Run("accepts singbox alias and mixed case", func(t *testing.T) {
		out := "SingBox Version 1.13.11\nTaGs: with_quic, with_naive_outbound\n"
		v, f := parseSingboxVersionOutput(out)
		if v != "1.13.11" {
			t.Errorf("version = %q, want 1.13.11", v)
		}
		want := []string{"with_quic", "with_naive_outbound"}
		if !reflect.DeepEqual(f, want) {
			t.Errorf("features = %v, want %v", f, want)
		}
	})
}

func TestOperator_ConfigPaths(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	if op.configPath != filepath.Join(dir, "config.d") {
		t.Errorf("configPath: %s", op.configPath)
	}
	if op.pidPath != filepath.Join(dir, "sing-box.pid") {
		t.Errorf("pidPath: %s", op.pidPath)
	}
	if op.tunnelsFile() != filepath.Join(dir, "config.d", "10-tunnels.json") {
		t.Errorf("tunnelsFile: %s", op.tunnelsFile())
	}
}

func TestOperator_GetStatus_UpdateAvailableWhenSameVersionSHADiffers(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	body := []byte("#!/bin/sh\necho 'sing-box version 1.2.3'\n")
	sum := sha256.Sum256(body)
	currentSHA := hex.EncodeToString(sum[:])
	if err := os.WriteFile(binary, body, 0755); err != nil {
		t.Fatal(err)
	}

	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	op.SetInstaller(installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3",
		SHA256:  strings.Repeat("f", 64),
	}, nil))

	status := op.GetStatus(context.Background())
	if !status.UpdateAvailable {
		t.Fatal("UpdateAvailable = false, want true for same version with different SHA")
	}
	if status.CurrentVersion != "1.2.3" || status.RequiredVersion != "1.2.3" {
		t.Fatalf("version pair = %q/%q, want 1.2.3/1.2.3", status.CurrentVersion, status.RequiredVersion)
	}
	if status.CurrentSHA256 != currentSHA {
		t.Errorf("CurrentSHA256 = %q, want %q", status.CurrentSHA256, currentSHA)
	}
	if status.RequiredSHA256 != strings.Repeat("f", 64) {
		t.Errorf("RequiredSHA256 = %q", status.RequiredSHA256)
	}
}

func TestEnsureBaseConfig_FullSkeleton(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	ensureBaseConfig(configDir)

	raw, err := os.ReadFile(filepath.Join(configDir, "00-base.json"))
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}

	outbounds, ok := base["outbounds"].([]any)
	if !ok || len(outbounds) != 1 {
		t.Fatalf("outbounds: want 1 (direct), got %#v", base["outbounds"])
	}
	direct := outbounds[0].(map[string]any)
	if direct["tag"] != "direct" || direct["type"] != "direct" {
		t.Errorf("direct outbound: %#v", direct)
	}

	route, ok := base["route"].(map[string]any)
	if !ok {
		t.Fatalf("route block missing: %#v", base["route"])
	}
	if _, has := route["final"]; has {
		t.Errorf("route.final should be absent (owned by 20-router.json), got %v", route["final"])
	}
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("default_domain_resolver: want dns-bootstrap, got %v", route["default_domain_resolver"])
	}

	dns, ok := base["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns block missing: %#v", base["dns"])
	}
	if dns["strategy"] != "ipv4_only" {
		t.Errorf("dns.strategy: want ipv4_only, got %v", dns["strategy"])
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("dns.servers: want 1 bootstrap, got %d", len(servers))
	}
	bs := servers[0].(map[string]any)
	if bs["tag"] != "dns-bootstrap" || bs["type"] != "udp" || bs["server"] != "1.1.1.1" {
		t.Errorf("bootstrap: %#v", bs)
	}
	if dns["final"] != "dns-bootstrap" {
		t.Errorf("dns.final: want dns-bootstrap, got %v", dns["final"])
	}
}

func TestEnsureBaseConfig_Idempotent(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	existing := `{"log":{"level":"debug"}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}
	// First call applies surgical heals (e.g. route.default_domain_resolver
	// for sing-box 1.13+). Second call must be a no-op — same bytes.
	ensureBaseConfig(configDir)
	first, _ := os.ReadFile(basePath)
	ensureBaseConfig(configDir)
	second, _ := os.ReadFile(basePath)
	if string(first) != string(second) {
		t.Errorf("ensureBaseConfig not idempotent: first=%s second=%s", first, second)
	}
	// User-chosen log.level must be preserved across both runs.
	var m map[string]any
	_ = json.Unmarshal(second, &m)
	if m["log"].(map[string]any)["level"] != "debug" {
		t.Errorf("log.level must be preserved, got %v", m["log"])
	}
}

func TestEnsureBaseConfig_PatchesStaleClashPort(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	stale := `{"log":{"level":"debug"},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9090"},"cache_file":{"enabled":true}},"dns":{"final":"my-dns"}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	exp := m["experimental"].(map[string]any)
	clash := exp["clash_api"].(map[string]any)
	if clash["external_controller"] != "127.0.0.1:9099" {
		t.Errorf("expected port 9099, got %v", clash["external_controller"])
	}
	// User customizations preserved.
	if m["log"].(map[string]any)["level"] != "debug" {
		t.Errorf("log.level lost: %v", m["log"])
	}
	if m["dns"].(map[string]any)["final"] != "my-dns" {
		t.Errorf("dns.final lost: %v", m["dns"])
	}
	// Other experimental fields preserved.
	if _, ok := exp["cache_file"]; !ok {
		t.Errorf("experimental.cache_file lost")
	}
}

func TestEnsureBaseConfig_NoClashApiBlockUntouched(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// User explicitly removed clash_api — respect that, don't re-add.
	// log.level is "debug" so the log-level heal also leaves it alone.
	// route.default_domain_resolver IS materialised because sing-box 1.13+
	// FATALs without it; that injection is unrelated to clash_api.
	custom := `{"log":{"level":"debug"}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := m["experimental"]; has {
		t.Errorf("experimental block must NOT be re-added, got %s", raw)
	}
	if m["log"].(map[string]any)["level"] != "debug" {
		t.Errorf("log.level must be preserved, got %v", m["log"])
	}
	route, ok := m["route"].(map[string]any)
	if !ok {
		t.Fatalf("route block must be materialised for sing-box 1.13+, got %s", raw)
	}
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("default_domain_resolver want dns-bootstrap, got %v", route["default_domain_resolver"])
	}
}

func TestEnsureBaseConfig_PatchesStaleLogLevel(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	stale := `{"log":{"level":"info","timestamp":true},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level should be heal-patched to trace, got %v", m["log"])
	}
}

func TestEnsureBaseConfig_RespectsDebugLogLevel(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// User-chosen debug — heal must NOT reduce verbosity.
	custom := `{"log":{"level":"debug","timestamp":true},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["log"].(map[string]any)["level"] != "debug" {
		t.Errorf("debug must be preserved, got %v", m["log"])
	}
}

func TestEnsureBaseConfig_PatchesMissingDomainResolver(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// Legacy 00-base.json from a pre-1.12 build — has route block (final +
	// rules) but no default_domain_resolver. sing-box 1.13+ FATALs on this.
	stale := `{"log":{"level":"trace","timestamp":true},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}},"route":{"final":"direct","rules":[]}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	route, ok := m["route"].(map[string]any)
	if !ok {
		t.Fatalf("route block lost: %v", m["route"])
	}
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("default_domain_resolver want dns-bootstrap, got %v", route["default_domain_resolver"])
	}
	// Existing route fields preserved (for now; removal wired in Task 3).
	if route["final"] != "direct" {
		t.Errorf("route.final lost: %v", route["final"])
	}
}

func TestEnsureBaseConfig_RespectsExistingDomainResolver(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// User picked a custom resolver. Heal must NOT clobber.
	custom := `{"log":{"level":"trace","timestamp":true},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}},"route":{"final":"direct","default_domain_resolver":"my-resolver"}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["route"].(map[string]any)["default_domain_resolver"] != "my-resolver" {
		t.Errorf("user resolver clobbered, got %v", m["route"])
	}
}

func TestEnsureBaseConfig_MaterialisesMissingRouteBlock(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// Real-world shape: legacy 00-base.json from a pre-route-fix awg-manager
	// build. The file has no route block at all. sing-box 1.13+ FATALs on
	// this — we materialise the route block + resolver unconditionally.
	stale := `{"dns":{"final":"dns-doh","servers":[{"server":"1.1.1.1","tag":"dns-bootstrap","type":"udp"}],"strategy":"ipv4_only"},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}},"log":{"level":"trace","timestamp":true}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	route, ok := m["route"].(map[string]any)
	if !ok {
		t.Fatalf("route block must be materialised, got %s", raw)
	}
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("default_domain_resolver want dns-bootstrap, got %v", route["default_domain_resolver"])
	}
	// Other blocks preserved.
	if m["dns"].(map[string]any)["strategy"] != "ipv4_only" {
		t.Errorf("dns block lost: %v", m["dns"])
	}
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level lost: %v", m["log"])
	}
}

func TestClassifyProcessLine(t *testing.T) {
	tests := []struct {
		in   string
		want logging.Level
	}{
		{"sing-box version 1.9.3 starting", logging.LevelInfo},
		{"FATAL: failed to bind tproxy", logging.LevelError},
		{"ERROR connecting to outbound", logging.LevelError},
		{"panic: nil dereference", logging.LevelError},
		{"failed to load config", logging.LevelError},
		{"WARN deprecated config field", logging.LevelWarn},
		{"warning: unused outbound", logging.LevelWarn},
		{"INFO route table updated", logging.LevelInfo},
		{"", logging.LevelInfo},
		{"some random output", logging.LevelInfo},
	}
	for _, tc := range tests {
		got := classifyProcessLine(tc.in)
		if got != tc.want {
			t.Errorf("classifyProcessLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFreshBaseConfig_CacheFilePathIsAbsolute(t *testing.T) {
	cfg := freshBaseConfig()
	exp := cfg["experimental"].(map[string]any)
	cf := exp["cache_file"].(map[string]any)
	if cf["enabled"] != true {
		t.Errorf("cache_file.enabled=%v want true", cf["enabled"])
	}
	got := cf["path"]
	want := defaultCacheDBPath
	if got != want {
		t.Errorf("cache_file.path=%q want %q", got, want)
	}
}

func TestEnsureBaseConfig_PatchesRelativeCachePath(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	stale := `{"log":{"level":"debug"},"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"},"cache_file":{"enabled":true,"path":"cache.db"}}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	exp := m["experimental"].(map[string]any)
	cf := exp["cache_file"].(map[string]any)
	if cf["path"] != defaultCacheDBPath {
		t.Errorf("expected %s, got %v", defaultCacheDBPath, cf["path"])
	}
	// User customizations preserved (log.level)
	if m["log"].(map[string]any)["level"] != "debug" {
		t.Errorf("log.level lost: %v", m["log"])
	}
}

func TestEnsureBaseConfig_LeavesAbsoluteCachePathUntouched(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	custom := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"},"cache_file":{"enabled":true,"path":"/custom/path/cache.db"}}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	json.Unmarshal(raw, &m)
	exp := m["experimental"].(map[string]any)
	cf := exp["cache_file"].(map[string]any)
	if cf["path"] != "/custom/path/cache.db" {
		t.Errorf("user-customized path overwritten: %v", cf["path"])
	}
}

func TestPatchBaseCacheFilePath_AddsMissingBlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "00-base.json")
	// experimental exists but no cache_file
	stale := `{"experimental":{"clash_api":{"external_controller":"127.0.0.1:9099"}}}`
	if err := os.WriteFile(p, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	patchBaseCacheFilePath(p)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	exp := m["experimental"].(map[string]any)
	cf, ok := exp["cache_file"].(map[string]any)
	if !ok {
		t.Fatal("cache_file block not added")
	}
	if cf["enabled"] != true {
		t.Errorf("enabled=%v want true", cf["enabled"])
	}
	if got := cf["path"]; got != defaultCacheDBPath {
		t.Errorf("path=%q want %q", got, defaultCacheDBPath)
	}
}

func TestPatchBaseCacheFilePath_MigratesLegacyAbsolute(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "00-base.json")
	stale := `{"experimental":{"cache_file":{"enabled":true,"path":"/opt/etc/sing-box/cache.db"}}}`
	if err := os.WriteFile(p, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	patchBaseCacheFilePath(p)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	exp := m["experimental"].(map[string]any)
	cf := exp["cache_file"].(map[string]any)
	if got := cf["path"]; got != defaultCacheDBPath {
		t.Errorf("path=%q want %q (legacy should be replaced)", got, defaultCacheDBPath)
	}
}

func TestPatchBaseCacheFilePath_PreservesUserCustomPath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "00-base.json")
	custom := "/srv/sing-box/my-cache.db"
	stale := `{"experimental":{"cache_file":{"enabled":true,"path":"` + custom + `"}}}`
	if err := os.WriteFile(p, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	patchBaseCacheFilePath(p)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	exp := m["experimental"].(map[string]any)
	cf := exp["cache_file"].(map[string]any)
	if got := cf["path"]; got != custom {
		t.Errorf("path=%q want %q (custom path should be preserved)", got, custom)
	}
}

// TestPatchTunnelsSlotStripBaseDNS_RemovesPollutedDNSBlock covers the
// exact shape of pollution observed in the wild: 10-tunnels.json with
// the full NewConfig() dns block (dns-bootstrap + dns-doh, no user
// rules). After patching, the dns key must be gone entirely so the
// cross-slot validator no longer reports duplicate-dns.
func TestPatchTunnelsSlotStripBaseDNS_RemovesPollutedDNSBlock(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	polluted := `{
		"inbounds": [{"type":"mixed","tag":"DE-in","listen":"127.0.0.1","listen_port":1080}],
		"outbounds": [{"type":"vless","tag":"DE","server":"de.tld","server_port":443}],
		"route": {"rules":[{"inbound":"DE-in","outbound":"DE"}]},
		"dns": {
			"strategy": "ipv4_only",
			"servers": [
				{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"},
				{"tag":"dns-doh","type":"https","server":"cloudflare-dns.com","domain_resolver":"dns-bootstrap"}
			],
			"final": "dns-doh"
		}
	}`
	if err := os.WriteFile(p, []byte(polluted), 0644); err != nil {
		t.Fatal(err)
	}
	patchTunnelsSlotStripBaseDNS(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["dns"]; present {
		t.Errorf("dns key must be removed entirely after patch, got: %s", raw)
	}
	// Tunnels content must survive untouched.
	if inbounds, _ := m["inbounds"].([]any); len(inbounds) != 1 {
		t.Errorf("inbounds: %v", inbounds)
	}
	if outbounds, _ := m["outbounds"].([]any); len(outbounds) != 1 {
		t.Errorf("outbounds: %v", outbounds)
	}
}

// TestPatchTunnelsSlotStripBaseDNS_PreservesUserCustomDNS keeps any
// dns server NOT in the owned set. A user who manually added e.g. a
// quad9 resolver via hand-edited json must keep it after self-heal.
func TestPatchTunnelsSlotStripBaseDNS_PreservesUserCustomDNS(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	mixed := `{
		"inbounds": [],
		"outbounds": [],
		"route": {"rules":[]},
		"dns": {
			"servers": [
				{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"},
				{"tag":"dns-quad9","type":"udp","server":"9.9.9.9"}
			]
		}
	}`
	if err := os.WriteFile(p, []byte(mixed), 0644); err != nil {
		t.Fatal(err)
	}
	patchTunnelsSlotStripBaseDNS(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	dns, ok := m["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns must remain (custom server present): %s", raw)
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("want 1 surviving server, got %d: %v", len(servers), servers)
	}
	srv := servers[0].(map[string]any)
	if srv["tag"] != "dns-quad9" {
		t.Errorf("surviving server tag=%v want dns-quad9", srv["tag"])
	}
}

// TestPatchTunnelsSlotStripBaseDNS_IdempotentOnClean is the steady-state
// case: a clean slot file (no dns block) must round-trip unchanged.
func TestPatchTunnelsSlotStripBaseDNS_IdempotentOnClean(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	clean := `{
  "inbounds": [],
  "outbounds": [],
  "route": {
    "rules": []
  }
}`
	if err := os.WriteFile(p, []byte(clean), 0644); err != nil {
		t.Fatal(err)
	}
	patchTunnelsSlotStripBaseDNS(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["dns"]; present {
		t.Errorf("dns must remain absent on clean file: %s", raw)
	}
}

// TestPatchTunnelsSlotStripBaseDNS_MissingFile must be a silent no-op.
// First boot before any tunnel-add has no 10-tunnels.json; the patcher
// runs every NewOperator and must not error.
func TestPatchTunnelsSlotStripBaseDNS_MissingFile(t *testing.T) {
	dir := t.TempDir()
	patchTunnelsSlotStripBaseDNS(filepath.Join(dir, "10-tunnels.json"))
	// No assertion — just confirm no panic.
}

// TestPatchTunnelsSlotStripBaseDNS_StripsDanglingFinalReference covers
// the combination case: user has a custom server (so dns block survives)
// AND `final` still points at one of the owned-set tags that just got
// removed. The patcher must strip that dangling reference along with
// the polluted servers, otherwise tunnels-slot dns.final = "dns-doh"
// references a tag whose owner now lives only in 00-base.
func TestPatchTunnelsSlotStripBaseDNS_StripsDanglingFinalReference(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	mixed := `{
		"inbounds": [],
		"outbounds": [],
		"route": {"rules":[]},
		"dns": {
			"strategy": "ipv4_only",
			"final": "dns-doh",
			"servers": [
				{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"},
				{"tag":"dns-doh","type":"https","server":"cloudflare-dns.com","domain_resolver":"dns-bootstrap"},
				{"tag":"dns-quad9","type":"udp","server":"9.9.9.9"}
			]
		}
	}`
	if err := os.WriteFile(p, []byte(mixed), 0644); err != nil {
		t.Fatal(err)
	}
	patchTunnelsSlotStripBaseDNS(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	dns, ok := m["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns must remain (custom server present): %s", raw)
	}
	if _, present := dns["final"]; present {
		t.Errorf("final must be stripped (dangling reference to removed dns-doh): %s", raw)
	}
	if _, present := dns["strategy"]; present {
		t.Errorf("strategy=ipv4_only must be stripped (mirrors base default): %s", raw)
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("want 1 surviving server (dns-quad9), got %d: %v", len(servers), servers)
	}
	if tag, _ := servers[0].(map[string]any)["tag"].(string); tag != "dns-quad9" {
		t.Errorf("surviving server tag=%q want dns-quad9", tag)
	}
}

// TestPatchTunnelsSlotStripBaseDNS_PreservesUserDNSRules keeps the dns
// block alive when servers got filtered to zero but the user has rules
// pointing at base-owned tags (dns rules reference, but don't own,
// server tags — sing-box merges across slots).
func TestPatchTunnelsSlotStripBaseDNS_PreservesUserDNSRules(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	mixed := `{
		"inbounds": [],
		"outbounds": [],
		"route": {"rules":[]},
		"dns": {
			"servers": [
				{"tag":"dns-bootstrap","type":"udp","server":"1.1.1.1"}
			],
			"rules": [
				{"domain":["example.com"],"server":"dns-doh"}
			]
		}
	}`
	if err := os.WriteFile(p, []byte(mixed), 0644); err != nil {
		t.Fatal(err)
	}
	patchTunnelsSlotStripBaseDNS(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	dns, ok := m["dns"].(map[string]any)
	if !ok {
		t.Fatalf("dns must remain (user rule present): %s", raw)
	}
	if _, hasServers := dns["servers"]; hasServers {
		t.Errorf("servers must be removed (only owned-set was present): %s", raw)
	}
	rules, _ := dns["rules"].([]any)
	if len(rules) != 1 {
		t.Errorf("want 1 surviving rule, got %d", len(rules))
	}
}

func TestEnsureBaseConfig_PatchesMissingDirectOutbound(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// Legacy 00-base.json with no outbounds key at all — the real-world
	// shape that produced FATAL "default outbound not found: direct" on
	// startup after router.NewEmptyConfig began emitting route.final=direct.
	stale := `{"log":{"level":"trace","timestamp":true},"route":{"default_domain_resolver":"dns-bootstrap"}}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	obs, ok := m["outbounds"].([]any)
	if !ok || len(obs) != 1 {
		t.Fatalf("outbounds want 1 direct entry, got %#v", m["outbounds"])
	}
	d := obs[0].(map[string]any)
	if d["tag"] != "direct" || d["type"] != "direct" {
		t.Errorf("direct entry malformed: %#v", d)
	}
}

func TestEnsureBaseConfig_PreservesExistingDirectOutbound(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// User customised direct (e.g. bind to a specific interface). Heal
	// must NOT clobber — bail as soon as a tag=="direct" entry is seen.
	custom := `{"log":{"level":"trace"},"outbounds":[{"type":"direct","tag":"direct","bind_interface":"eth0"}]}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	obs := m["outbounds"].([]any)
	if len(obs) != 1 {
		t.Fatalf("want 1 outbound, got %d", len(obs))
	}
	d := obs[0].(map[string]any)
	if d["bind_interface"] != "eth0" {
		t.Errorf("user bind_interface clobbered: %#v", d)
	}
}

func TestEnsureBaseConfig_AppendsDirectAlongsideOtherOutbounds(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// outbounds present but no direct — append, don't replace.
	custom := `{"log":{"level":"trace"},"outbounds":[{"type":"selector","tag":"sub-x","outbounds":["sub-x-1"]}]}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	obs := m["outbounds"].([]any)
	if len(obs) != 2 {
		t.Fatalf("want 2 outbounds (selector + direct), got %d: %s", len(obs), raw)
	}
	tags := map[string]bool{}
	for _, v := range obs {
		ob := v.(map[string]any)
		tags[ob["tag"].(string)] = true
	}
	if !tags["sub-x"] || !tags["direct"] {
		t.Errorf("missing required tags, got %v", tags)
	}
}

// Production regression (sing-box 1.14.0-alpha.21):
// "duplicate outbound/endpoint tag: direct" reported by two users on
// 2.9.17.x. A v2.8.x config.json migrated into 10-tunnels.json *before*
// commit 1186280b added filterOutDirectPlaceholder to the migration
// path. patchBaseDirectOutbound then also injects "direct" into
// 00-base.json, producing the collision sing-box rejects.
// stripStrayDirectPlaceholder repairs the install on next boot by
// removing the canonical placeholder from every slot file EXCEPT
// 00-base.json.
func TestStripStrayDirectPlaceholder_RemovesDuplicateFromTunnelsSlot(t *testing.T) {
	configDir := t.TempDir()
	// 00-base.json — canonical direct stays.
	base := `{"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(filepath.Join(configDir, "00-base.json"), []byte(base), 0644); err != nil {
		t.Fatal(err)
	}
	// 10-tunnels.json — legacy v2.8.x placeholder alongside a real tunnel.
	tunnels := `{"outbounds":[
		{"type":"direct","tag":"direct"},
		{"type":"vless","tag":"my-tunnel","server":"h.example","server_port":443,"uuid":"u"}
	]}`
	if err := os.WriteFile(filepath.Join(configDir, "10-tunnels.json"), []byte(tunnels), 0644); err != nil {
		t.Fatal(err)
	}

	stripStrayDirectPlaceholder(configDir)

	// 00-base.json untouched.
	raw, _ := os.ReadFile(filepath.Join(configDir, "00-base.json"))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if obs := m["outbounds"].([]any); len(obs) != 1 || obs[0].(map[string]any)["tag"] != "direct" {
		t.Errorf("00-base.json must be untouched: %s", raw)
	}

	// 10-tunnels.json — placeholder dropped, real tunnel preserved.
	raw, _ = os.ReadFile(filepath.Join(configDir, "10-tunnels.json"))
	_ = json.Unmarshal(raw, &m)
	obs := m["outbounds"].([]any)
	if len(obs) != 1 {
		t.Fatalf("10-tunnels.json: want 1 outbound (placeholder dropped), got %d: %s", len(obs), raw)
	}
	if tag := obs[0].(map[string]any)["tag"].(string); tag != "my-tunnel" {
		t.Errorf("10-tunnels.json: surviving outbound must be the real tunnel, got tag=%q", tag)
	}
}

// stripStrayDirectPlaceholder must not touch direct outbounds with
// non-placeholder tags (e.g. awg-tunnel-foo with bind_interface) —
// those are 15-awg.json's legitimate per-WAN direct outbounds and
// they don't collide with 00-base.json's canonical "direct".
func TestStripStrayDirectPlaceholder_PreservesNonPlaceholderDirect(t *testing.T) {
	configDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(configDir, "00-base.json"),
		[]byte(`{"outbounds":[{"type":"direct","tag":"direct"}]}`), 0644)
	awg := `{"outbounds":[{"type":"direct","tag":"awg-tunnel-a","bind_interface":"t2s0"}]}`
	_ = os.WriteFile(filepath.Join(configDir, "15-awg.json"), []byte(awg), 0644)

	stripStrayDirectPlaceholder(configDir)

	raw, _ := os.ReadFile(filepath.Join(configDir, "15-awg.json"))
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	obs := m["outbounds"].([]any)
	if len(obs) != 1 {
		t.Fatalf("15-awg.json: want 1 outbound (non-placeholder direct preserved), got %d", len(obs))
	}
	if tag := obs[0].(map[string]any)["tag"].(string); tag != "awg-tunnel-a" {
		t.Errorf("15-awg.json: per-WAN direct outbound must survive, got tag=%q", tag)
	}
}

// No-op on clean tree: every slot file unchanged.
func TestStripStrayDirectPlaceholder_NoOpWhenCleanTree(t *testing.T) {
	configDir := t.TempDir()
	files := map[string]string{
		"00-base.json":          `{"outbounds":[{"type":"direct","tag":"direct"}]}`,
		"10-tunnels.json":       `{"outbounds":[{"type":"vless","tag":"x","server":"h","server_port":443,"uuid":"u"}]}`,
		"15-awg.json":           `{"outbounds":[{"type":"direct","tag":"awg-x","bind_interface":"t2s0"}]}`,
		"40-subscriptions.json": `{"outbounds":[{"type":"selector","tag":"sub","outbounds":["sub-x"]}]}`,
	}
	mtimes := map[string]int64{}
	for name, body := range files {
		p := filepath.Join(configDir, name)
		_ = os.WriteFile(p, []byte(body), 0644)
		st, _ := os.Stat(p)
		mtimes[name] = st.ModTime().UnixNano()
	}

	// Sleep 10ms so a rewrite would be observable in mtime nanos.
	time.Sleep(10 * time.Millisecond)
	stripStrayDirectPlaceholder(configDir)

	for name := range files {
		st, _ := os.Stat(filepath.Join(configDir, name))
		if st.ModTime().UnixNano() != mtimes[name] {
			t.Errorf("%s rewritten despite clean tree", name)
		}
	}
}

// Subdirectories (disabled/, pending/) must be skipped — sing-box does
// not merge them, so a stray placeholder there is harmless.
func TestStripStrayDirectPlaceholder_SkipsSubdirectories(t *testing.T) {
	configDir := t.TempDir()
	sub := filepath.Join(configDir, "disabled")
	_ = os.MkdirAll(sub, 0755)
	staleInDisabled := `{"outbounds":[{"type":"direct","tag":"direct"}]}`
	_ = os.WriteFile(filepath.Join(sub, "98-old.json"), []byte(staleInDisabled), 0644)

	stripStrayDirectPlaceholder(configDir)

	raw, _ := os.ReadFile(filepath.Join(sub, "98-old.json"))
	if string(raw) != staleInDisabled {
		t.Errorf("disabled/ subdir must be untouched, got: %s", raw)
	}
}

// preflightConfigDir runs our local configmerge first so duplicate
// tags surface with both conflicting slot file names — actionable
// where `sing-box check` alone reports only the tag.
func TestPreflightConfigDir_SurfacesCollisionWithBothFilenames(t *testing.T) {
	configDir := t.TempDir()
	base := `{"outbounds":[{"type":"direct","tag":"direct"}]}`
	dup := `{"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(filepath.Join(configDir, "00-base.json"), []byte(base), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "10-tunnels.json"), []byte(dup), 0644); err != nil {
		t.Fatal(err)
	}

	// Validator that would falsely pass — proves preflight stops at
	// configmerge before delegating, so the diagnostic stays ours.
	op := &Operator{
		configPath: configDir,
		validator: &Validator{
			binary: "/nonexistent",
			exec:   func(string, ...string) ([]byte, error) { return nil, nil },
		},
	}

	err := op.preflightConfigDir()
	if err == nil {
		t.Fatal("preflightConfigDir: want error, got nil")
	}
	msg := err.Error()
	for _, want := range []string{`outbounds`, `"direct"`, `00-base.json`, `10-tunnels.json`} {
		if !strings.Contains(msg, want) {
			t.Errorf("preflight error missing %q: %s", want, msg)
		}
	}
}

// On a clean tree, preflight falls through to sing-box check — the
// failure path we use to prove the second stage actually runs.
func TestPreflightConfigDir_FallsThroughToValidator(t *testing.T) {
	configDir := t.TempDir()
	// One slot, one direct outbound — no collisions.
	if err := os.WriteFile(filepath.Join(configDir, "00-base.json"),
		[]byte(`{"outbounds":[{"type":"direct","tag":"direct"}]}`), 0644); err != nil {
		t.Fatal(err)
	}

	validatorCalls := 0
	op := &Operator{
		configPath: configDir,
		validator: &Validator{
			binary: "/nonexistent",
			exec: func(string, ...string) ([]byte, error) {
				validatorCalls++
				return []byte("synthetic schema failure"), errSyntheticValidator
			},
		},
	}

	err := op.preflightConfigDir()
	if err == nil {
		t.Fatal("want validator error to surface, got nil")
	}
	if validatorCalls != 1 {
		t.Errorf("validator must be called exactly once after configmerge success, got %d", validatorCalls)
	}
	if !strings.Contains(err.Error(), "synthetic schema failure") {
		t.Errorf("validator error must propagate verbatim, got: %s", err)
	}
}

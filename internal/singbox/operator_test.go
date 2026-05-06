package singbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

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
	if route["final"] != "direct" {
		t.Errorf("route.final: want direct, got %v", route["final"])
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
	// Existing route fields preserved.
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

func TestInstalledBinaryPath_PrefersManagedBinary(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed-sing-box")
	if err := os.WriteFile(managed, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "legacy-sing-box")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	prevLegacy := legacyBinaryPath
	legacyBinaryPath = legacy
	t.Cleanup(func() { legacyBinaryPath = prevLegacy })

	op := NewOperator(OperatorDeps{Dir: dir, Binary: managed})
	got, ok := op.installedBinaryPath()
	if !ok {
		t.Fatal("installedBinaryPath() reported not installed")
	}
	if got != managed {
		t.Fatalf("installedBinaryPath() = %q, want %q", got, managed)
	}
}

func TestInstalledBinaryPath_FallsBackToLegacyBinary(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy-sing-box")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	prevLegacy := legacyBinaryPath
	legacyBinaryPath = legacy
	t.Cleanup(func() { legacyBinaryPath = prevLegacy })

	op := NewOperator(OperatorDeps{Dir: dir, Binary: filepath.Join(dir, "missing-managed")})
	got, ok := op.installedBinaryPath()
	if !ok {
		t.Fatal("installedBinaryPath() reported not installed")
	}
	if got != legacy {
		t.Fatalf("installedBinaryPath() = %q, want %q", got, legacy)
	}
}

func TestEnsureRuntimeBinaryBinding_RebindsToLegacyBinary(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "legacy-sing-box")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	prevLegacy := legacyBinaryPath
	legacyBinaryPath = legacy
	t.Cleanup(func() { legacyBinaryPath = prevLegacy })

	op := NewOperator(OperatorDeps{Dir: dir, Binary: filepath.Join(dir, "missing-managed")})
	op.ensureRuntimeBinaryBinding()

	if got := op.proc.Binary(); got != legacy {
		t.Fatalf("proc binary = %q, want %q", got, legacy)
	}
	if got := op.validator.binary; got != legacy {
		t.Fatalf("validator binary = %q, want %q", got, legacy)
	}
}

func TestEnsureRuntimeBinaryBinding_PrefersManagedBinary(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed-sing-box")
	if err := os.WriteFile(managed, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "legacy-sing-box")
	if err := os.WriteFile(legacy, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	prevLegacy := legacyBinaryPath
	legacyBinaryPath = legacy
	t.Cleanup(func() { legacyBinaryPath = prevLegacy })

	op := NewOperator(OperatorDeps{Dir: dir, Binary: managed})
	op.ensureRuntimeBinaryBinding()

	if got := op.proc.Binary(); got != managed {
		t.Fatalf("proc binary = %q, want %q", got, managed)
	}
	if got := op.validator.binary; got != managed {
		t.Fatalf("validator binary = %q, want %q", got, managed)
	}
}

func TestIsManagedInboundServerMap(t *testing.T) {
	if !isManagedInboundServerMap(map[string]any{"tag": "srv-vless-1", "type": "vless"}) {
		t.Fatal("expected vless server inbound to be managed")
	}
	if isManagedInboundServerMap(map[string]any{"tag": "tun-a-in", "type": "mixed"}) {
		t.Fatal("mixed tunnel inbound must not be managed server")
	}
	if isManagedInboundServerMap(map[string]any{"tag": "device-proxy-in", "type": "mixed"}) {
		t.Fatal("device-proxy inbound must not be managed server")
	}
}

func TestValidateInboundServerInfo(t *testing.T) {
	base := InboundServerInfo{
		Tag:        "srv-vless-1",
		Protocol:   "vless",
		Listen:     "0.0.0.0",
		ListenPort: 443,
		TLS:        &TLSConfig{Enabled: true},
		Users:      []InboundUser{{UUID: "bf000d23-0752-40b4-affe-68f7707a9661"}},
	}
	if err := validateInboundServerInfo(base); err != nil {
		t.Fatalf("unexpected vless validation error: %v", err)
	}

	hy2 := InboundServerInfo{
		Tag:        "srv-hy2-1",
		Protocol:   "hysteria2",
		Listen:     "0.0.0.0",
		ListenPort: 8443,
		TLS:        &TLSConfig{Enabled: true},
		Users:      []InboundUser{{Name: "user", Password: "secret"}},
	}
	if err := validateInboundServerInfo(hy2); err != nil {
		t.Fatalf("unexpected hysteria2 validation error: %v", err)
	}

	naive := InboundServerInfo{
		Tag:        "srv-naive-1",
		Protocol:   "naive",
		Listen:     "0.0.0.0",
		ListenPort: 443,
		TLS:        &TLSConfig{Enabled: true},
		Users:      []InboundUser{{Username: "u", Password: "p"}},
	}
	if err := validateInboundServerInfo(naive); err != nil {
		t.Fatalf("unexpected naive validation error: %v", err)
	}

	bad := base
	bad.Users = nil
	if err := validateInboundServerInfo(bad); err == nil {
		t.Fatal("expected validation error for missing vless uuid")
	}
}

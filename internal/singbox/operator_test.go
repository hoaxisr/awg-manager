package singbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
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

func TestOperator_GetStatus_PopulatesInstallStateAndBytes(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box") // не установлен
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	inst := installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3", URL: "u", SHA256: "s", Size: 100 << 20,
	}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 50 << 20, true })
	op.SetInstaller(inst)

	s := op.GetStatus(context.Background())

	if s.InstallState != string(installer.InstallStateMissingNoSpace) {
		t.Fatalf("InstallState=%q want %q", s.InstallState, installer.InstallStateMissingNoSpace)
	}
	if s.RequiredBytes == 0 {
		t.Fatalf("RequiredBytes=0, want > 0")
	}
	if s.FreeBytes == 0 {
		t.Fatalf("FreeBytes=0, want > 0")
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
	if dns["strategy"] != "prefer_ipv4" {
		t.Errorf("dns.strategy: want prefer_ipv4, got %v", dns["strategy"])
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
	// Default desired sing-box level is trace (when not explicitly provided).
	var m map[string]any
	_ = json.Unmarshal(second, &m)
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level must be patched to trace, got %v", m["log"])
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
	// Desired level defaults to trace.
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level want trace, got %v", m["log"])
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
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level want trace, got %v", m["log"])
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

func TestEnsureBaseConfig_DefaultDesiredLevelOverridesDebug(t *testing.T) {
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
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level want trace, got %v", m["log"])
	}
}

func TestEnsureBaseConfigWithLogLevel_UsesDesiredLevel(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	ensureBaseConfigWithLogLevel(configDir, "warn")

	raw, err := os.ReadFile(filepath.Join(configDir, "00-base.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	logBlock, _ := m["log"].(map[string]any)
	if got, _ := logBlock["level"].(string); got != "warn" {
		t.Fatalf("log.level = %q, want warn", got)
	}
	if ts, ok := logBlock["timestamp"].(bool); !ok || !ts {
		t.Fatalf("log.timestamp missing/false: %#v", logBlock["timestamp"])
	}
}

func TestPatchBaseLogLevel_AppliesDesiredLevel(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0o755)
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(`{"log":{"level":"info"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	patchBaseLogLevel(basePath, "error")
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	logBlock, _ := m["log"].(map[string]any)
	if got, _ := logBlock["level"].(string); got != "error" {
		t.Fatalf("log.level = %q, want error", got)
	}
	if ts, ok := logBlock["timestamp"].(bool); !ok || !ts {
		t.Fatalf("log.timestamp missing/false: %#v", logBlock["timestamp"])
	}
}

func TestOperatorApplyLogLevel_UpdatesBaseConfig(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(op.ConfigDir(), "00-base.json")
	if err := op.ApplyLogLevel("warn"); err != nil {
		t.Fatalf("ApplyLogLevel: %v", err)
	}
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	logBlock, _ := m["log"].(map[string]any)
	if got, _ := logBlock["level"].(string); got != "warn" {
		t.Fatalf("log.level = %q, want warn", got)
	}
}

func TestOperatorApplyLogLevel_BrokenBaseJSONReturnsError(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	basePath := filepath.Join(op.ConfigDir(), "00-base.json")
	if err := os.WriteFile(basePath, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := op.ApplyLogLevel("warn")
	if err == nil {
		t.Fatal("expected parse error for broken 00-base.json")
	}
	raw, readErr := os.ReadFile(basePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(raw) != "{broken" {
		t.Fatalf("broken file must remain untouched, got: %s", raw)
	}
}

func TestOperatorApplyLogLevel_UsesOrchestratorSlotBase(t *testing.T) {
	dir := t.TempDir()
	op := NewOperator(OperatorDeps{Dir: dir})
	orch := singboxorch.New(op.ConfigDir(), op.Process())
	for _, meta := range singboxorch.KnownSlots() {
		if meta.Slot == singboxorch.SlotBase {
			if err := orch.Register(meta); err != nil {
				t.Fatalf("register slot base: %v", err)
			}
		}
	}
	if err := orch.Bootstrap(); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	op.SetOrch(orch)

	if err := op.ApplyLogLevel("error"); err != nil {
		t.Fatalf("ApplyLogLevel via orch: %v", err)
	}

	basePath := filepath.Join(op.ConfigDir(), "00-base.json")
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile(basePath)
		if err == nil {
			var m map[string]any
			if json.Unmarshal(raw, &m) == nil {
				logBlock, _ := m["log"].(map[string]any)
				if got, _ := logBlock["level"].(string); got == "error" {
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("00-base.json was not updated to error via orchestrator in time")
		}
		time.Sleep(50 * time.Millisecond)
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
	// ensureBaseConfig preserves existing route.final — removal is done
	// separately by removeFinalFromBase, called after ensureBaseConfig in
	// Operator.New. This test only exercises ensureBaseConfig, so final
	// stays "direct" here.
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
	// dns block preserved, but legacy ipv4_only strategy migrated to prefer_ipv4.
	if m["dns"].(map[string]any)["strategy"] != "prefer_ipv4" {
		t.Errorf("dns.strategy must be migrated ipv4_only→prefer_ipv4: %v", m["dns"])
	}
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level lost: %v", m["log"])
	}
}

// Existing installs carry 00-base.json with the legacy dns.strategy
// "ipv4_only" (issue #180 — drops AAAA/IPv6). ensureBaseConfig must migrate
// it to "prefer_ipv4" on startup; a non-legacy strategy is left untouched.
func TestEnsureBaseConfig_MigratesIpv4OnlyStrategy(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config.d")
	_ = os.MkdirAll(configDir, 0755)
	basePath := filepath.Join(configDir, "00-base.json")
	stale := `{"dns":{"final":"dns-bootstrap","servers":[{"server":"1.1.1.1","tag":"dns-bootstrap","type":"udp"}],"strategy":"ipv4_only"},"route":{"default_domain_resolver":"dns-bootstrap"},"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(basePath, []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := m["dns"].(map[string]any)["strategy"]; got != "prefer_ipv4" {
		t.Errorf("strategy: want prefer_ipv4 (migrated), got %v", got)
	}
}

// A non-legacy strategy in 00-base.json must NOT be rewritten by the migration.
func TestEnsureBaseConfig_KeepsNonLegacyStrategy(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config.d")
	_ = os.MkdirAll(configDir, 0755)
	basePath := filepath.Join(configDir, "00-base.json")
	cfg := `{"dns":{"final":"dns-bootstrap","servers":[{"server":"1.1.1.1","tag":"dns-bootstrap","type":"udp"}],"strategy":"ipv6_only"},"route":{"default_domain_resolver":"dns-bootstrap"},"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(basePath, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, _ := os.ReadFile(basePath)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if got := m["dns"].(map[string]any)["strategy"]; got != "ipv6_only" {
		t.Errorf("non-legacy strategy must be preserved, got %v", got)
	}
}

func TestOperator_HandleStdoutLine_RedactsSensitiveHosts(t *testing.T) {
	cap := &captureLogger{}
	op := &Operator{
		processLogger: logging.NewScopedLogger(cap, logging.GroupSingbox, logging.SubSBProcess),
	}

	op.handleStdoutLine("lookup domain node.example.org and dial 203.0.113.77")

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected one log, got %d", len(got))
	}
	msg := got[0].Message
	if strings.Contains(msg, "node.example.org") || strings.Contains(msg, "203.0.113.77") {
		t.Fatalf("raw sensitive value leaked: %q", msg)
	}
	if !strings.Contains(msg, "no************rg") || !strings.Contains(msg, "20********77") {
		t.Fatalf("redacted values missing: %q", msg)
	}
	if got[0].Sub != logging.SubSBProcess {
		t.Fatalf("subgroup = %q, want %q", got[0].Sub, logging.SubSBProcess)
	}
}

func TestOperator_HandleStderrLine_RedactsAndSetsLastError(t *testing.T) {
	op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	op.handleStderrLine("FATAL[0000] lookup failed for node.example.org: 203.0.113.77")
	last := op.LastError()
	if strings.Contains(last, "node.example.org") || strings.Contains(last, "203.0.113.77") {
		t.Fatalf("raw sensitive value leaked into LastError: %q", last)
	}
	if !strings.Contains(last, "no************rg") || !strings.Contains(last, "20********77") {
		t.Fatalf("redacted values missing in LastError: %q", last)
	}
}

func TestOperator_HandleExit_RedactsLastError(t *testing.T) {
	op := &Operator{log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	op.handleExit(errors.New("exit for node.example.org: 203.0.113.77"), "")
	last := op.LastError()
	if strings.Contains(last, "node.example.org") || strings.Contains(last, "203.0.113.77") {
		t.Fatalf("raw sensitive value leaked from err: %q", last)
	}
	if !strings.Contains(last, "no************rg") || !strings.Contains(last, "20********77") {
		t.Fatalf("redacted values missing from err: %q", last)
	}

	op.handleExit(errors.New("exit"), "stderr for node.example.org: 203.0.113.77")
	last = op.LastError()
	if strings.Contains(last, "node.example.org") || strings.Contains(last, "203.0.113.77") {
		t.Fatalf("raw sensitive value leaked from stderrTail: %q", last)
	}
	if !strings.Contains(last, "no************rg") || !strings.Contains(last, "20********77") {
		t.Fatalf("redacted values missing from stderrTail: %q", last)
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
	if m["log"].(map[string]any)["level"] != "trace" {
		t.Errorf("log.level want trace, got %v", m["log"])
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

// TestPatchTunnelsSlotStripBaseOwnedBlocks_RemovesPollutedDNSBlock covers the
// exact shape of pollution observed in the wild: 10-tunnels.json with
// the full NewConfig() dns block (dns-bootstrap + dns-doh, no user
// rules). After patching, the dns key must be gone entirely so the
// cross-slot validator no longer reports duplicate-dns.
func TestPatchTunnelsSlotStripBaseOwnedBlocks_RemovesPollutedDNSBlock(t *testing.T) {
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
	patchTunnelsSlotStripBaseOwnedBlocks(p)

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

// TestPatchTunnelsSlotStripBaseOwnedBlocks_PreservesUserCustomDNS keeps any
// dns server NOT in the owned set. A user who manually added e.g. a
// quad9 resolver via hand-edited json must keep it after self-heal.
func TestPatchTunnelsSlotStripBaseOwnedBlocks_PreservesUserCustomDNS(t *testing.T) {
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
	patchTunnelsSlotStripBaseOwnedBlocks(p)

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

func TestPatchTunnelsSlotStripBaseOwnedBlocks_StripsTopLevelLog(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	withLog := `{
		"inbounds": [],
		"outbounds": [],
		"route": {"rules":[]},
		"log": {"level":"trace","timestamp":true}
	}`
	if err := os.WriteFile(p, []byte(withLog), 0o644); err != nil {
		t.Fatal(err)
	}
	patchTunnelsSlotStripBaseOwnedBlocks(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["log"]; ok {
		t.Fatalf("top-level log must be stripped from 10-tunnels.json, got: %s", raw)
	}
}

func TestPatchTunnelsSlotEnsureNaiveUDPOverTCP(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "10-tunnels.json")
	legacy := `{
		"inbounds": [],
		"outbounds": [
			{"type":"naive","tag":"N","server":"h","server_port":443,"username":"u","password":"p"},
			{"type":"vless","tag":"V","server":"h","server_port":443,"uuid":"u"}
		],
		"route": {"rules":[]}
	}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	patchTunnelsSlotEnsureNaiveUDPOverTCP(p)

	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	outbounds := m["outbounds"].([]any)
	naive := outbounds[0].(map[string]any)
	uot, _ := naive["udp_over_tcp"].(map[string]any)
	if uot == nil || uot["enabled"] != true || uot["version"] != float64(2) {
		t.Fatalf("udp_over_tcp=%v", uot)
	}
	vless := outbounds[1].(map[string]any)
	if _, ok := vless["udp_over_tcp"]; ok {
		t.Fatalf("vless must not get udp_over_tcp: %v", vless)
	}
}

// TestPatchTunnelsSlotStripBaseOwnedBlocks_IdempotentOnClean is the steady-state
// case: a clean slot file (no dns block) must round-trip unchanged.
func TestPatchTunnelsSlotStripBaseOwnedBlocks_IdempotentOnClean(t *testing.T) {
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
	patchTunnelsSlotStripBaseOwnedBlocks(p)

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

// TestPatchTunnelsSlotStripBaseOwnedBlocks_MissingFile must be a silent no-op.
// First boot before any tunnel-add has no 10-tunnels.json; the patcher
// runs every NewOperator and must not error.
func TestPatchTunnelsSlotStripBaseOwnedBlocks_MissingFile(t *testing.T) {
	dir := t.TempDir()
	patchTunnelsSlotStripBaseOwnedBlocks(filepath.Join(dir, "10-tunnels.json"))
	// No assertion — just confirm no panic.
}

// TestPatchTunnelsSlotStripBaseOwnedBlocks_StripsDanglingFinalReference covers
// the combination case: user has a custom server (so dns block survives)
// AND `final` still points at one of the owned-set tags that just got
// removed. The patcher must strip that dangling reference along with
// the polluted servers, otherwise tunnels-slot dns.final = "dns-doh"
// references a tag whose owner now lives only in 00-base.
func TestPatchTunnelsSlotStripBaseOwnedBlocks_StripsDanglingFinalReference(t *testing.T) {
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
	patchTunnelsSlotStripBaseOwnedBlocks(p)

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
		t.Errorf("strategy=ipv4_only must be stripped (legacy base default): %s", raw)
	}
	servers, _ := dns["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("want 1 surviving server (dns-quad9), got %d: %v", len(servers), servers)
	}
	if tag, _ := servers[0].(map[string]any)["tag"].(string); tag != "dns-quad9" {
		t.Errorf("surviving server tag=%q want dns-quad9", tag)
	}
}

// TestPatchTunnelsSlotStripBaseOwnedBlocks_PreservesUserDNSRules keeps the dns
// block alive when servers got filtered to zero but the user has rules
// pointing at base-owned tags (dns rules reference, but don't own,
// server tags — sing-box merges across slots).
func TestPatchTunnelsSlotStripBaseOwnedBlocks_PreservesUserDNSRules(t *testing.T) {
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
	patchTunnelsSlotStripBaseOwnedBlocks(p)

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

func TestEnsureBaseConfig_PrependsDirectWhenMissing(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)
	// outbounds present but no direct — add canonical direct at index 0
	// so fallback stays direct when route.final is absent.
	custom := `{"log":{"level":"trace"},"outbounds":[{"type":"selector","tag":"sub-x","outbounds":["sub-x-1"]}]}`
	basePath := filepath.Join(configDir, "00-base.json")
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}
	ensureBaseConfig(configDir)
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	obs := m["outbounds"].([]any)
	if len(obs) != 2 {
		t.Fatalf("want 2 outbounds (direct + selector), got %d: %s", len(obs), raw)
	}
	first := obs[0].(map[string]any)
	if first["tag"] != "direct" {
		t.Fatalf("direct must be first when added, got %v", first["tag"])
	}
	second := obs[1].(map[string]any)
	if second["tag"] != "sub-x" {
		t.Fatalf("existing non-direct outbound should follow direct, got %v", second["tag"])
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

func TestEnsureBaseConfig_MovesExistingDirectToFirstOutbound(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config.d")
	_ = os.MkdirAll(configDir, 0755)

	basePath := filepath.Join(configDir, "00-base.json")
	custom := `{"log":{"level":"trace"},"outbounds":[{"type":"selector","tag":"custom-first","outbounds":["direct"]},{"type":"direct","tag":"direct","bind_interface":"eth0"}]}`
	if err := os.WriteFile(basePath, []byte(custom), 0644); err != nil {
		t.Fatal(err)
	}

	ensureBaseConfig(configDir)

	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	obs := m["outbounds"].([]any)
	if len(obs) != 2 {
		t.Fatalf("want 2 outbounds, got %d: %s", len(obs), raw)
	}
	first := obs[0].(map[string]any)
	if first["tag"] != "direct" {
		t.Fatalf("direct must be first after self-heal, got first tag=%v", first["tag"])
	}
	if first["bind_interface"] != "eth0" {
		t.Errorf("existing direct outbound fields must be preserved, got %#v", first)
	}
	second := obs[1].(map[string]any)
	if second["tag"] != "custom-first" {
		t.Errorf("custom outbound should move after direct, got second tag=%v", second["tag"])
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

func TestParseProxyIdx_EmptyReturnsSentinel(t *testing.T) {
	idx, err := parseProxyIdx("")
	if err != nil {
		t.Errorf("parseProxyIdx(\"\") err = %v, want nil", err)
	}
	if idx != -1 {
		t.Errorf("parseProxyIdx(\"\") idx = %d, want -1 (sentinel)", idx)
	}
}

func TestParseProxyIdx_ValidProxy(t *testing.T) {
	idx, err := parseProxyIdx("Proxy3")
	if err != nil || idx != 3 {
		t.Errorf("parseProxyIdx(Proxy3) = (%d, %v), want (3, nil)", idx, err)
	}
}

func TestParseProxyIdx_Malformed(t *testing.T) {
	if _, err := parseProxyIdx("garbage"); err == nil {
		t.Error("parseProxyIdx(garbage) should error")
	}
}

// TestRemoveTunnel_EmptyProxyInterface_ParseSentinelSkipsNDMS verifies the
// branch where a tunnel has empty ProxyInterface (NDMS Proxy toggle was off
// when added): the sentinel from parseProxyIdx flows into the
// `if proxyIdx >= 0` guard in RemoveTunnel (~operator.go:1446), skipping
// RemoveProxy without error. End-to-end RemoveTunnel needs a live sing-box
// binary (applyConfig forks it) — covered by manual scenarios in T23 (S4).
// This unit test pins the sentinel contract.
func TestRemoveTunnel_EmptyProxyInterface_ParseSentinelSkipsNDMS(t *testing.T) {
	idx, err := parseProxyIdx("")
	if err != nil {
		t.Fatalf("sentinel broken: parseProxyIdx(\"\") err = %v", err)
	}
	if idx >= 0 {
		t.Errorf("sentinel must be < 0 (so guard skips RemoveProxy), got %d", idx)
	}
}

// TestListTunnels_Running_NDMSDisabled_UsesClash verifies that when the
// NDMS Proxy toggle is off, ListTunnels falls back from the kernel-iface
// probe (which would always return false — no t2sN exists) to the Clash
// /proxies endpoint. It also pins the post-condition that derived
// ProxyInterface/KernelInterface (which Tunnels() computes from
// listenPort regardless of mode) are cleared in the returned slice so
// the API/UI consistently reflect the NDMS-free state.
func TestListTunnels_Running_NDMSDisabled_UsesClash(t *testing.T) {
	// Test config has listen_port = firstPort (= 1080), so Tunnels()
	// parser DOES derive ProxyInterface=Proxy0 / KernelInterface=t2s0
	// from listenPort — we then assert the disabled path clears them.
	tunnelsJSON := `{
		"inbounds":[{"type":"mixed","tag":"us-vless-in","listen":"127.0.0.1","listen_port":1080}],
		"outbounds":[{"type":"vless","tag":"us-vless","server":"x","server_port":443}],
		"route":{"rules":[{"inbound":"us-vless-in","outbound":"us-vless"}]}
	}`

	cases := []struct {
		name         string
		clashHandler func(http.ResponseWriter, *http.Request)
		clashAddr    string // overrides if non-empty
		wantRunning  bool
	}{
		{
			name: "clash reports outbound present → Running true",
			clashHandler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/proxies" {
					_, _ = io.WriteString(w, `{"proxies":{"us-vless":{"name":"us-vless","type":"vless"}}}`)
					return
				}
				http.NotFound(w, r)
			},
			wantRunning: true,
		},
		{
			name:        "clash unreachable → Running false",
			clashAddr:   "127.0.0.1:1", // unused port
			wantRunning: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configDir := filepath.Join(dir, "config.d")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "10-tunnels.json"), []byte(tunnelsJSON), 0o644); err != nil {
				t.Fatal(err)
			}
			pidPath := filepath.Join(dir, "sing-box.pid")
			if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
				t.Fatal(err)
			}

			op := newOperatorForTest(t, withNDMSProxyEnabled(func() bool { return false }))
			op.dir = dir
			op.configPath = configDir
			op.pidPath = pidPath
			op.proc = NewProcess(op.binary, configDir, pidPath)

			var srvAddr string
			if tc.clashHandler != nil {
				srv := httptest.NewServer(http.HandlerFunc(tc.clashHandler))
				defer srv.Close()
				srvAddr = strings.TrimPrefix(srv.URL, "http://")
			} else {
				srvAddr = tc.clashAddr
			}
			op.clash = NewClashClient(srvAddr)

			tuns, err := op.ListTunnels(context.Background())
			if err != nil {
				t.Fatalf("ListTunnels: %v", err)
			}
			if len(tuns) != 1 {
				t.Fatalf("tunnels = %d, want 1", len(tuns))
			}
			if tuns[0].Running != tc.wantRunning {
				t.Errorf("Running = %v, want %v", tuns[0].Running, tc.wantRunning)
			}
			if tuns[0].ProxyInterface != "" {
				t.Errorf("ProxyInterface = %q, want empty in disabled mode", tuns[0].ProxyInterface)
			}
			if tuns[0].KernelInterface != "" {
				t.Errorf("KernelInterface = %q, want empty in disabled mode", tuns[0].KernelInterface)
			}
		})
	}
}

func TestOutboundFingerprint(t *testing.T) {
	tests := []struct {
		name string
		ob   map[string]any
		want string
	}{
		{
			"vless full",
			map[string]any{"type": "vless", "server": "ex.com", "server_port": 443, "uuid": "uuid-1"},
			"vless|ex.com|443|uuid-1",
		},
		{
			"trojan password",
			map[string]any{"type": "trojan", "server": "ex.com", "server_port": 443, "password": "secret"},
			"trojan|ex.com|443|secret",
		},
		{
			"hysteria2 password",
			map[string]any{"type": "hysteria2", "server": "ex.com", "server_port": 8443, "password": "p"},
			"hysteria2|ex.com|8443|p",
		},
		{
			"naive concat",
			map[string]any{"type": "naive", "server": "ex.com", "server_port": 443, "username": "u", "password": "p"},
			"naive|ex.com|443|u:p",
		},
		{
			"unknown type → empty",
			map[string]any{"type": "wireguard", "server": "ex.com", "server_port": 443},
			"",
		},
		{
			"missing server → empty",
			map[string]any{"type": "vless", "server_port": 443, "uuid": "x"},
			"",
		},
		{
			"missing port → empty",
			map[string]any{"type": "vless", "server": "ex.com", "uuid": "x"},
			"",
		},
		{
			"missing uuid → empty (для vless)",
			map[string]any{"type": "vless", "server": "ex.com", "server_port": 443},
			"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := outboundFingerprint(tc.ob); got != tc.want {
				t.Errorf("outboundFingerprint = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers for constructing a minimal Operator in tests.
// ---------------------------------------------------------------------------

type operatorOpt func(*OperatorDeps)

func withNDMSProxyEnabled(fn func() bool) operatorOpt {
	return func(d *OperatorDeps) { d.IsNDMSProxyEnabled = fn }
}

func newOperatorForTest(t *testing.T, opts ...operatorOpt) *Operator {
	t.Helper()
	d := OperatorDeps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir: t.TempDir(),
	}
	for _, o := range opts {
		o(&d)
	}
	return NewOperator(d)
}

// TestNextFreeListenPortSlot covers the NDMS-free slot allocator used by
// AddTunnels when the NDMS Proxy toggle is off. Full AddTunnels integration
// requires a live sing-box binary (preflight + startAndWait fork/exec) — out
// of scope for a unit test; manual scenarios (Task 23, S2) cover that path.
func TestNextFreeListenPortSlot(t *testing.T) {
	tests := []struct {
		name     string
		existing []int // existing listen ports
		reserved map[int]bool
		want     int
	}{
		{"empty config, no reserved", nil, nil, 0},
		{"one tunnel at slot 0", []int{firstPort}, nil, 1},
		{"gap reuse: slot 1 free", []int{firstPort, firstPort + 2}, nil, 1},
		{"reserved within batch", nil, map[int]bool{0: true, 1: true}, 2},
		{"existing + reserved", []int{firstPort}, map[int]bool{1: true}, 2},
		{"sub-firstPort port ignored", []int{1000, firstPort}, nil, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := newTestConfigWithListenPorts(t, tc.existing)
			got := nextFreeListenPortSlot(cfg, tc.reserved)
			if got != tc.want {
				t.Errorf("nextFreeListenPortSlot = %d, want %d", got, tc.want)
			}
		})
	}
}

// newTestConfigWithListenPorts builds a minimal *Config whose Tunnels()
// reports tunnels with the given listenPorts. Used by TestNextFreeListenPortSlot.
func newTestConfigWithListenPorts(t *testing.T, ports []int) *Config {
	t.Helper()
	cfg := NewConfig()
	for i, p := range ports {
		tag := fmt.Sprintf("test-%d", i)
		ob := json.RawMessage(fmt.Sprintf(`{"type":"vless","server":"x","server_port":443,"tag":%q}`, tag))
		if err := cfg.AddTunnelWithListenPort(tag, "vless", "x", 443, p, ob); err != nil {
			t.Fatalf("seed tunnel listenPort=%d: %v", p, err)
		}
	}
	return cfg
}

func TestGetStatus_NDMSProxyEnabled_Mirrors(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
	}{
		{"enabled true", true},
		{"enabled false", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			op := newOperatorForTest(t, withNDMSProxyEnabled(func() bool { return tc.enabled }))
			got := op.GetStatus(context.Background())
			if got.NDMSProxyEnabled != tc.enabled {
				t.Errorf("NDMSProxyEnabled = %v, want %v", got.NDMSProxyEnabled, tc.enabled)
			}
		})
	}
}

// --- removeFinalFromBase tests ---

func TestRemoveFinalFromBase_DropsKey(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	if err := os.WriteFile(basePath,
		[]byte(`{"log":{"level":"trace"},"route":{"final":"direct","rules":[]},"outbounds":[{"type":"direct","tag":"direct"}]}`),
		0644); err != nil {
		t.Fatal(err)
	}

	removeFinalFromBase(basePath)

	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	route, _ := m["route"].(map[string]any)
	if _, has := route["final"]; has {
		t.Errorf("route.final should be removed, got %v", route["final"])
	}
	// Other route keys preserved.
	if _, has := route["rules"]; !has {
		t.Errorf("route.rules unexpectedly removed")
	}
	// Outbounds preserved.
	if _, has := m["outbounds"]; !has {
		t.Errorf("outbounds unexpectedly removed")
	}
}

func TestRemoveFinalFromBase_Idempotent(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	original := `{"route":{"rules":[]},"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(basePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	removeFinalFromBase(basePath)
	removeFinalFromBase(basePath) // second call: should be no-op

	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	route, _ := m["route"].(map[string]any)
	if _, has := route["final"]; has {
		t.Errorf("route.final should remain absent")
	}
}

func TestRemoveFinalFromBase_PreservesOtherRouteFields(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	if err := os.WriteFile(basePath,
		[]byte(`{"route":{"final":"direct","default_domain_resolver":"dns-bootstrap","rules":[{"action":"sniff"}]}}`),
		0644); err != nil {
		t.Fatal(err)
	}

	removeFinalFromBase(basePath)

	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	route, _ := m["route"].(map[string]any)
	if _, has := route["final"]; has {
		t.Errorf("route.final not removed")
	}
	if route["default_domain_resolver"] != "dns-bootstrap" {
		t.Errorf("default_domain_resolver lost: %v", route["default_domain_resolver"])
	}
	rules, _ := route["rules"].([]any)
	if len(rules) != 1 {
		t.Errorf("rules lost: %v", route["rules"])
	}
}

func TestRemoveFinalFromBase_NoRouteSection_NoOp(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	original := `{"log":{"level":"trace"},"outbounds":[{"type":"direct","tag":"direct"}]}`
	if err := os.WriteFile(basePath, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	removeFinalFromBase(basePath)

	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Errorf("file truncated")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("file became invalid JSON: %v", err)
	}
	if _, has := m["outbounds"]; !has {
		t.Errorf("outbounds lost")
	}
}

func TestRemoveFinalFromBase_MissingFile_NoPanic(t *testing.T) {
	// No setup — file does not exist.
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")

	// Should not panic, should not create the file.
	removeFinalFromBase(basePath)

	if _, err := os.Stat(basePath); !os.IsNotExist(err) {
		t.Errorf("file should not be created when missing")
	}
}

func TestRemoveFinalFromBase_MalformedJSON_NoOp(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "00-base.json")
	garbage := `{this is not json`
	if err := os.WriteFile(basePath, []byte(garbage), 0644); err != nil {
		t.Fatal(err)
	}

	removeFinalFromBase(basePath)

	// Файл должен остаться неизменным — мы не должны trash bad config.
	raw, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != garbage {
		t.Errorf("malformed file mutated: got %q, want %q", string(raw), garbage)
	}
}

func TestOperator_Install_NoSpace_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box") // не существует
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	inst := installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3", URL: "u", SHA256: "s", Size: 100 << 20,
	}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 50 << 20, true })
	op.SetInstaller(inst)

	if err := op.Install(context.Background()); err != nil {
		t.Fatalf("Install returned error, expected nil: %v", err)
	}
	if got := inst.EvaluateInstallState(); got != installer.InstallStateMissingNoSpace {
		t.Fatalf("EvaluateInstallState=%q, want %q", got, installer.InstallStateMissingNoSpace)
	}
}

func TestOperator_Update_NoSpace_ReturnsNil(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	if err := os.WriteFile(binary, []byte("old-content"), 0o755); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	inst := installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3", URL: "u", SHA256: "different-sha", Size: 100 << 20,
	}, nil)
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 50 << 20, true })
	op.SetInstaller(inst)

	if err := op.Update(context.Background()); err != nil {
		t.Fatalf("Update returned error, expected nil: %v", err)
	}
	if got := inst.EvaluateInstallState(); got != installer.InstallStateOutdatedNoSpace {
		t.Fatalf("EvaluateInstallState=%q, want %q", got, installer.InstallStateOutdatedNoSpace)
	}
}

// same-version+same-sha → MatchesRequired==true → Update должен быть no-op
// без обращения к gate (gate стоит ПОСЛЕ MatchesRequired early-return).
func TestOperator_Update_SameVersionSameSHA_NoOp(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "sing-box")
	// Скрипт, выводящий точный version-format, который парсит installer.versionRe.
	body := []byte("#!/bin/sh\necho 'sing-box version 1.2.3'\n")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	if err := os.WriteFile(binary, body, 0o755); err != nil {
		t.Fatal(err)
	}
	op := NewOperator(OperatorDeps{Dir: dir, Binary: binary})
	inst := installer.New(binary, "test-arch", installer.BinarySpec{
		Version: "1.2.3", URL: "u", SHA256: sha, Size: 100 << 20,
	}, nil)
	// freeDisk специально мал — gate сработал бы, если бы достигся
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 1 << 10, true })
	op.SetInstaller(inst)

	if err := op.Update(context.Background()); err != nil {
		t.Fatalf("Update returned error, expected no-op nil: %v", err)
	}
	// Бинарь не тронут — Update вернулся через MatchesRequired до gate'а.
	if _, err := os.Stat(binary); err != nil {
		t.Fatalf("binary disappeared: %v", err)
	}
}

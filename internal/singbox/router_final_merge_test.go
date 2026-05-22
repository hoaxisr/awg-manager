package singbox

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// locateSingboxBinary returns the host-arch sing-box binary path, or "" if
// none is available. Test should t.Skip when this returns "".
func locateSingboxBinary(t *testing.T) string {
	t.Helper()
	// Prefer PATH (developer-installed sing-box).
	if p, err := exec.LookPath("sing-box"); err == nil {
		return p
	}
	// Fall back to dist/ build artifacts. Skip ARM/MIPS targets.
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return ""
	}
	// Match files like dist/singbox-binaries/1.14.0-alpha.21/sing-box-...-linux-amd64*/sing-box
	matches, err := filepath.Glob(filepath.FromSlash(
		"../../dist/singbox-binaries/*/sing-box-*-linux-" + runtime.GOARCH + "*/sing-box"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// writeBaseNoFinal writes a freshBaseConfig-shaped 00-base.json AFTER the
// route.final has been removed (post-spec layout).
func writeBaseNoFinal(t *testing.T, dir string) {
	t.Helper()
	base := map[string]any{
		"outbounds": []any{
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{
			"rules": []any{map[string]any{"action": "sniff"}},
		},
	}
	raw, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00-base.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeRouterSlot(t *testing.T, dir, finalTag, selectorTag string) {
	t.Helper()
	router := map[string]any{
		"outbounds": []any{
			map[string]any{
				"type":      "selector",
				"tag":       selectorTag,
				"outbounds": []any{"direct"},
			},
		},
		"route": map[string]any{
			"final": finalTag,
			"rules": []any{},
		},
	}
	raw, err := json.MarshalIndent(router, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20-router.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
}

// mergeViaSingbox runs `sing-box merge <out> -C <dir>` and returns the
// parsed merged config.
func mergeViaSingbox(t *testing.T, binPath, configDir string) map[string]any {
	t.Helper()
	out := filepath.Join(t.TempDir(), "merged.json")
	cmd := exec.Command(binPath, "merge", out, "-C", configDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sing-box merge failed: %v\nstderr: %s", err, stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestIntegration_RouterFinal_OverridesAfterFreshBaseConfig — regression
// for the dead-code scenario described in spec
// 2026-05-21-route-final-router-owned-design.md. Without the fix
// (removeFinalFromBase + freshBaseConfig change), base's "direct" would
// shadow router's "myproxy". With the fix, router's value wins.
func TestIntegration_RouterFinal_OverridesAfterFreshBaseConfig(t *testing.T) {
	bin := locateSingboxBinary(t)
	if bin == "" {
		t.Skip("no host sing-box binary available; build via scripts/build-singbox.sh or `apt install sing-box`")
	}

	dir := t.TempDir()
	writeBaseNoFinal(t, dir)
	writeRouterSlot(t, dir, "myproxy", "myproxy")

	merged := mergeViaSingbox(t, bin, dir)
	route, _ := merged["route"].(map[string]any)
	if route == nil {
		t.Fatalf("merged config missing route block: %v", merged)
	}
	if route["final"] != "myproxy" {
		t.Errorf("route.final: want myproxy, got %v", route["final"])
	}
}

func TestIntegration_RouterFinal_DisabledRouter_NoFinal(t *testing.T) {
	bin := locateSingboxBinary(t)
	if bin == "" {
		t.Skip("no host sing-box binary available")
	}

	dir := t.TempDir()
	writeBaseNoFinal(t, dir)
	// No 20-router.json — simulates router disabled.

	merged := mergeViaSingbox(t, bin, dir)
	route, _ := merged["route"].(map[string]any)
	if route == nil {
		t.Fatalf("merged config missing route block: %v", merged)
	}
	if _, has := route["final"]; has {
		t.Errorf("route.final should be absent when no router slot, got %v", route["final"])
	}
	// First outbound is direct → sing-box will fall back to direct.
	outbounds, _ := merged["outbounds"].([]any)
	if len(outbounds) == 0 {
		t.Fatalf("outbounds missing")
	}
	first, _ := outbounds[0].(map[string]any)
	if first["tag"] != "direct" {
		t.Errorf("first outbound should be direct, got %v", first["tag"])
	}
}

func TestIntegration_RouterFinal_DefaultDirect_NoConflict(t *testing.T) {
	bin := locateSingboxBinary(t)
	if bin == "" {
		t.Skip("no host sing-box binary available")
	}

	dir := t.TempDir()
	writeBaseNoFinal(t, dir)
	// Router-slot пишет тот же "direct" (default RouterConfig behavior).
	writeRouterSlot(t, dir, "direct", "myproxy")

	merged := mergeViaSingbox(t, bin, dir)
	route, _ := merged["route"].(map[string]any)
	if route["final"] != "direct" {
		t.Errorf("route.final: want direct, got %v", route["final"])
	}
	// Make sure outbounds were concat'd (base direct + router myproxy).
	outbounds, _ := merged["outbounds"].([]any)
	tags := make(map[string]bool, len(outbounds))
	for _, o := range outbounds {
		m, _ := o.(map[string]any)
		if tag, ok := m["tag"].(string); ok {
			tags[tag] = true
		}
	}
	if !tags["direct"] || !tags["myproxy"] {
		// Diagnostic in case sing-box merge semantics ever change.
		raw, _ := json.MarshalIndent(merged, "", "  ")
		t.Errorf("outbounds should include both direct + myproxy:\n%s", raw)
	}
}

package firc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExternalThirdPartyPackageOwnershipRefused(t *testing.T) {
	inst, pm, _ := runtimeFixture(t)
	infoDir := filepath.Join(inst.layout.RootDir, "opt/lib/opkg/info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "firc.list"), []byte("/opt/bin/fircd\n/opt/bin/tunvless\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := inst.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "external package firc.list") {
		t.Fatalf("expected external package refusal on Install, got %v", err)
	}
	if pm.installs != 0 || pm.removes != 0 {
		t.Fatalf("unexpected package mutations: installs=%d removes=%d", pm.installs, pm.removes)
	}
}

func TestEnsureInitScriptNeverMutatesInstalledArtifact(t *testing.T) {
	inst, _, _ := runtimeFixture(t)
	if err := inst.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(inst.layout.InitScript)
	if err != nil {
		t.Fatal(err)
	}
	ensureInitScriptSparesDisabled(inst.layout.InitScript)
	after, err := os.ReadFile(inst.layout.InitScript)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("ensureInitScriptSparesDisabled mutated package-owned S99firc on disk")
	}
	pkg := filepath.Join(inst.layout.RootDir, "opt/var/cache/awg-manager/firc-runtime/verified.ipk")
	if err := verifyInstalledRuntime(pkg, inst.arch, inst.spec.Version, inst.layout); err != nil {
		t.Fatalf("verifyInstalledRuntime failed: %v", err)
	}
}

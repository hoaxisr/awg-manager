package firc

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

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

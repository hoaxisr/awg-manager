package mihomo_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod from %s", dir)
		}
		dir = parent
	}
}

// TestMihomoRuleTypes_ManifestParity ensures that the committed TypeScript generated
// file frontend/src/lib/types/mihomoRuleTypes.generated.ts is in 100% byte-for-byte parity
// with the Go rule registry. This test is STRICTLY READ-ONLY and does NOT write or modify files.
func TestMihomoRuleTypes_ManifestParity(t *testing.T) {
	repoRoot := findRepoRoot(t)
	genPath := filepath.Join(repoRoot, "frontend", "src", "lib", "types", "mihomoRuleTypes.generated.ts")

	committedBytes, err := os.ReadFile(genPath)
	if err != nil {
		t.Fatalf("Failed to read committed generated file %s: %v. Please run: go run ./internal/mihomo/cmd/genrules", genPath, err)
	}

	expectedBytes := mihomo.RenderRuleTypesTypeScript()

	if !bytes.Equal(committedBytes, expectedBytes) {
		t.Fatalf("frontend/src/lib/types/mihomoRuleTypes.generated.ts is out of sync with Go rule registry! Run 'go run ./internal/mihomo/cmd/genrules' to regenerate.")
	}
}

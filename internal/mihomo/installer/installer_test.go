package installer

import (
	"context"
	"testing"
)

func TestEmbeddedBinaries_PinsComplete(t *testing.T) {
	requiredArches := []string{"aarch64-3.10", "mipsel-3.4", "mips-3.4"}
	for _, arch := range requiredArches {
		spec, ok := EmbeddedBinaries[arch]
		if !ok {
			t.Errorf("missing EmbeddedBinaries for arch %q", arch)
			continue
		}
		if spec.Version == "" {
			t.Errorf("empty version for %q", arch)
		}
		if spec.SHA256 == "" {
			t.Errorf("empty SHA256 for %q", arch)
		}
		if spec.Size <= 0 {
			t.Errorf("invalid size for %q: %d", arch, spec.Size)
		}
		if spec.URL == "" {
			t.Errorf("missing URL for %q", arch)
		}
	}
}

func TestInstaller_SpecAccessors(t *testing.T) {
	spec := EmbeddedBinaries["aarch64-3.10"]
	inst := New("/tmp/mihomo", "aarch64-3.10", spec, nil)

	if inst.RequiredVersion() != spec.Version {
		t.Errorf("expected RequiredVersion %q, got %q", spec.Version, inst.RequiredVersion())
	}
	if inst.RequiredSize() != spec.Size {
		t.Errorf("expected RequiredSize %d, got %d", spec.Size, inst.RequiredSize())
	}
	if !inst.IsInstallAvailable() {
		t.Errorf("expected IsInstallAvailable to be true when URL is present")
	}
}

func TestInstaller_UpdateAvailable_EmptyWhenNotInstalled(t *testing.T) {
	spec := EmbeddedBinaries["aarch64-3.10"]
	inst := New("/nonexistent/mihomo", "aarch64-3.10", spec, nil)

	if inst.UpdateAvailable(context.Background()) {
		t.Errorf("expected UpdateAvailable to be false for non-installed binary")
	}
}

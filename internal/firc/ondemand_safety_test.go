package firc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUninstallPreservesPackageOwnedRuntime(t *testing.T) {
	root := t.TempDir()
	inst := NewOnDemandInstaller("aarch64-3.10", FircBinarySpec{})
	inst.SetPackageLayout(DefaultPackageLayout(root))
	info := filepath.Join(root, "opt/lib/opkg/info")
	if err := os.MkdirAll(info, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "awg-manager.list"), []byte("/opt/sbin/fircd\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := inst.Uninstall(context.Background(), nil, false, false); err == nil || !strings.Contains(err.Error(), "belongs to package") {
		t.Fatalf("expected ownership refusal: %v", err)
	}
}

func TestUninstallFailsClosedOnDependencyProbeError(t *testing.T) {
	inst := NewOnDemandInstaller("aarch64-3.10", FircBinarySpec{})
	inst.SetPackageLayout(DefaultPackageLayout(t.TempDir()))
	probeErr := errors.New("probe failed")
	inst.SetDependencyChecker(func(context.Context) ([]string, error) { return nil, probeErr })
	if err := inst.Uninstall(context.Background(), nil, false, false); !errors.Is(err, probeErr) {
		t.Fatalf("expected dependency error: %v", err)
	}
}

func TestParseOpkgStatusTupleRealEntwareVariants(t *testing.T) {
	cases := []struct {
		status  string
		wantVer string
		wantErr bool
	}{
		{"install ok installed", "0.7.0-1", false},
		{"install user installed", "0.7.0-1", false},
		{"install hold,user installed", "0.7.0-1", false},
		{"deinstall ok not-installed", "", false},
		{"deinstall user not-installed", "", false},
		{"deinstall ok config-files", "", false},
		{"deinstall user config-files", "", false},
		{"install user unpacked", "", true},
		{"install ok half-configured", "", true},
		{"install reinstreq installed", "", true},
		{"deinstall ok half-installed", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := parseOpkgStatusTuple(tc.status, "0.7.0-1")
		if (err != nil) != tc.wantErr {
			t.Fatalf("parseOpkgStatusTuple(%q) err=%v, wantErr=%v", tc.status, err, tc.wantErr)
		}
		if got != tc.wantVer {
			t.Fatalf("parseOpkgStatusTuple(%q) ver=%q, want %q", tc.status, got, tc.wantVer)
		}
	}
}

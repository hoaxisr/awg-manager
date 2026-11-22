package firc

import (
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOperator_ExternalFircBinFallbackAndIsolation(t *testing.T) {
	root := t.TempDir()
	fircDir := filepath.Join(root, "opt", "etc", "firc")
	sbinDir := filepath.Join(root, "opt", "sbin")
	binDir := filepath.Join(root, "opt", "bin")
	initScript := filepath.Join(root, "opt", "etc", "init.d", "S99firc")
	for _, dir := range []string{fircDir, binDir, filepath.Dir(initScript)} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	altFircd := filepath.Join(binDir, "fircd")
	altTun := filepath.Join(binDir, "tunvless")
	if err := os.WriteFile(altFircd, makeMinimalELF(elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS, "fircd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(altTun, makeMinimalELF(elf.ELFCLASS32, elf.ELFDATA2LSB, elf.EM_MIPS, "tunvless"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initScript, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	op := NewOperator(Options{
		ConfigDir:      fircDir,
		FircdBinary:    filepath.Join(sbinDir, "fircd"),
		AltFircdBinary: altFircd,
		TunvlessBin:    filepath.Join(sbinDir, "tunvless"),
		AltTunvlessBin: altTun,
		InitScript:     initScript,
	})

	if err := op.ValidateBinaries(); err != nil {
		t.Fatalf("expected ValidateBinaries() to succeed via Alt* fallback, got: %v", err)
	}
	if err := op.CheckRuntimeInstalledForSwitch(); err != nil {
		t.Fatalf("expected CheckRuntimeInstalledForSwitch() to succeed via Alt* fallback, got: %v", err)
	}
	st := op.GetOfflineStatus(false)
	if !st.FircdInstalled || !st.TunvlessInstalled {
		t.Fatalf("expected GetOfflineStatus to report installed=true via Alt* fallback, got %+v", st)
	}

	// Custom explicit FircdBinary without Alt* must remain isolated and fail when missing.
	isolated := NewOperator(Options{
		ConfigDir:   fircDir,
		FircdBinary: filepath.Join(root, "missing-custom-fircd"),
		TunvlessBin: filepath.Join(root, "missing-custom-tunvless"),
		InitScript:  initScript,
	})
	if err := isolated.ValidateBinaries(); err == nil {
		t.Fatal("expected custom explicit FircdBinary without Alt* to fail ValidateBinaries()")
	}
	isolatedSt := isolated.GetOfflineStatus(false)
	if isolatedSt.FircdInstalled || isolatedSt.TunvlessInstalled {
		t.Fatalf("expected isolated operator to report installed=false, got %+v", isolatedSt)
	}
}

func TestPurgeFircAndTunnels_ExternalPackagePreserved(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	awgmDir := filepath.Join(root, "opt", "etc", "awg-manager")
	sbDir := filepath.Join(awgmDir, "singbox", "config.d")
	fircDir := filepath.Join(root, "opt", "etc", "firc")
	if err := os.MkdirAll(sbDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(awgmDir, "settings.json"), []byte(`{"singboxRouter":{"routingEngine":"sing-box"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sbDir, "00-base.json"), []byte(`{"log":{"level":"info"}}`), 0644); err != nil {
		t.Fatal(err)
	}

	seedExternalFircTree(t, root)
	if err := os.WriteFile(filepath.Join(fircDir, "tunnels.yaml"), []byte("tunnels: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	inst := NewOnDemandInstaller("mipsel-3.4", FircBinarySpec{})
	inst.SetPackageLayout(DefaultPackageLayout(root))
	pm := &mockRuntimePackages{
		root:     root,
		versions: map[string]string{"firc": "0.8.2-1"},
	}
	inst.SetPackageManager(pm)

	op := NewOperator(Options{
		ConfigDir:      fircDir,
		FircdBinary:    filepath.Join(root, "opt", "sbin", "fircd"),
		AltFircdBinary: filepath.Join(root, "opt", "bin", "fircd"),
		TunvlessBin:    filepath.Join(root, "opt", "sbin", "tunvless"),
		AltTunvlessBin: filepath.Join(root, "opt", "bin", "tunvless"),
		InitScript:     filepath.Join(root, "opt", "etc", "init.d", "S99firc"),
	})
	op.SetSingboxValidator(func(context.Context, string) error { return nil })
	op.SetLifecycleHooks(
		func(context.Context) error { return nil },
		func(context.Context) error { return nil },
	)

	res, err := op.PurgeFircAndTunnels(ctx, sbDir, "sing-box", inst, nil, EnginePurgeHooks{})
	if err != nil {
		t.Fatalf("PurgeFircAndTunnels failed for external firc package: %v", err)
	}
	if res.RuntimeRemoved {
		t.Fatalf("expected RuntimeRemoved=false when FIRC belongs to external package, got %+v", res)
	}
	if pm.removes != 0 || pm.installs != 0 {
		t.Fatalf("expected zero opkg mutations on external package purge, got removes=%d installs=%d", pm.removes, pm.installs)
	}
	for _, binPath := range []string{
		filepath.Join(root, "opt", "bin", "fircd"),
		filepath.Join(root, "opt", "bin", "tunvless"),
	} {
		if _, statErr := os.Stat(binPath); statErr != nil {
			t.Fatalf("expected external binary %s preserved, stat err=%v", binPath, statErr)
		}
	}
}

func TestOperator_StartTimeoutAndDiagnosticSanitization(t *testing.T) {
	op := NewOperator(Options{ConfigDir: t.TempDir()})
	if op.startTimeout != 30*time.Second {
		t.Fatalf("expected default startTimeout=30s, got %s", op.startTimeout)
	}

	raw := "\x1b[32mStarting fircd...\x1b[0m\r\n\r\n\x1b[31mfailed to bind   socket\x1b[0m\n"
	got := sanitizeStartDiagnostic(raw)
	want := "Starting fircd...; failed to bind socket"
	if got != want {
		t.Fatalf("sanitizeStartDiagnostic() = %q, want %q", got, want)
	}

	lastPath := filepath.Join(t.TempDir(), "fircd.last")
	if err := os.WriteFile(lastPath, []byte("2026-11-21T20:11:58Z listen unix /tmp/firc/firc.sock: address already in use\n"), 0600); err != nil {
		t.Fatal(err)
	}
	op.lastOutPath = lastPath

	combined := op.fircStartDiagnostic([]byte("\x1b[33mStarting fircd...\x1b[0m\n"))
	if !strings.Contains(combined, "Starting fircd...") || !strings.Contains(combined, "; last: ") || !strings.Contains(combined, "address already in use") {
		t.Fatalf("unexpected combined fircStartDiagnostic: %q", combined)
	}

	deduped := op.fircStartDiagnostic([]byte("Starting fircd...\nfircd: the daemon's last line: 2026-11-21T20:11:58Z listen unix /tmp/firc/firc.sock: address already in use\n"))
	if strings.Contains(deduped, "; last: ") {
		t.Fatalf("expected fircStartDiagnostic not to duplicate fircd.last already in initOut, got: %q", deduped)
	}
}

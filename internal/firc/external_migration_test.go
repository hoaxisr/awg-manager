package firc

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func seedExternalMigrationRoot(t *testing.T, extPkg, extVer string) (*OnDemandInstaller, *mockRuntimePackages, string) {
	t.Helper()
	source := t.TempDir()
	root := t.TempDir()
	t.Setenv("FIRC_SOURCE_ROOT", source)

	for rel, content := range map[string]string{
		"LICENSE":                             "GPL-3.0-or-later test fixture\n",
		"files/entware_kn/etc/init.d/S99firc": "#!/bin/sh\n[ -e /opt/etc/firc/.awgm-disabled ] && exit 8\nexit 0\n",
		"files/entware_kn/etc/ndm/netfilter.d/100-firc": "#!/bin/sh\nexit 0\n",
		"files/common/etc/firc/firc.conf":               "configVersion: 0.7.0\n",
		"files/common/etc/firc/groups.yaml":             "groups: []\n",
	} {
		p := filepath.Join(source, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}

	fircdSrc := filepath.Join(source, "fircd")
	tunSrc := filepath.Join(source, "tunvless")
	if err := os.WriteFile(fircdSrc, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "fircd-awgm"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tunSrc, makeMinimalELF(elf.ELFCLASS64, elf.ELFDATA2LSB, elf.EM_AARCH64, "tunvless-awgm"), 0755); err != nil {
		t.Fatal(err)
	}
	ipk := filepath.Join(source, "runtime.ipk")
	if err := BuildRuntimeIPK(source, fircdSrc, tunSrc, "aarch64-3.10", "0.7.0-1", ipk); err != nil {
		t.Fatal(err)
	}
	ipkBytes, err := os.ReadFile(ipk)
	if err != nil {
		t.Fatal(err)
	}

	for rel, content := range map[string]string{
		"opt/bin/fircd":                         "external-fircd-binary",
		"opt/bin/tunvless":                      "external-tunvless-binary",
		"opt/etc/init.d/S99firc":                "#!/bin/sh\n# upstream external S99firc\nexit 0\n",
		"opt/etc/ndm/netfilter.d/100-firc":      "#!/bin/sh\nexit 0\n",
		"opt/etc/ndm/iflayerchanged.d/100-firc": "#!/bin/sh\nexit 0\n",
		"opt/etc/firc/firc.conf":                "configVersion: 0.7.0\napp:\n  httpWeb:\n    enabled: true\n",
		"opt/etc/firc/groups.yaml":              "configVersion: 0.8.2\ngroups:\n  - id: grp1\n    name: CustomGroup\n",
		"opt/etc/firc/tunnels.yaml":             "tunnels:\n  - id: vless0\n    device: tunvless0\n",
		"opt/etc/firc/awgm-ext.json":            "{\"serverRouting\":{\"vlessXhttpServer\":true}}\n",
		"opt/var/lib/firc/hwid":                 "deadbeef12345678\n",
		"opt/var/lib/firc/rci-token":            "rci-token-secret\n",
		"opt/var/lib/firc/tunnels/cache.txt":    "cached-sub-body\n",
		"opt/var/lib/firc/update/ignored.bin":   "transient-update-download\n",
		"opt/lib/opkg/info/" + extPkg + ".list": "/opt/bin/fircd\n/opt/bin/tunvless\n/opt/etc/init.d/S99firc\n/opt/etc/ndm/netfilter.d/100-firc\n/opt/etc/ndm/iflayerchanged.d/100-firc\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}

	spec := FircBinarySpec{
		Version:       "0.7.0-1",
		PackageURL:    "https://example.invalid/runtime.ipk",
		PackageSHA256: sha256HexBytes(ipkBytes),
		PackageSize:   int64(len(ipkBytes)),
	}
	inst := NewOnDemandInstaller("aarch64-3.10", spec)
	inst.SetPackageLayout(DefaultPackageLayout(root))
	pm := &mockRuntimePackages{
		root:     root,
		versions: map[string]string{extPkg: extVer},
	}
	inst.SetPackageManager(pm)
	inst.SetDownloader(&mockFircDownloader{blobs: map[string][]byte{spec.PackageURL: ipkBytes}})
	inst.SetFreeDiskFn(func(string) (int64, bool) { return 256 << 20, true })
	return inst, pm, root
}

func TestExternalMigration_SuccessPreservesConfigsAndVarState(t *testing.T) {
	ctx := context.Background()
	inst, pm, root := seedExternalMigrationRoot(t, "firc", "0.8.2-1")

	var running atomic.Bool
	running.Store(true)
	var starts, stops atomic.Int32

	op := NewOperator(Options{
		ConfigDir:      filepath.Join(root, "opt", "etc", "firc"),
		FircdBinary:    filepath.Join(root, "opt", "sbin", "fircd"),
		AltFircdBinary: filepath.Join(root, "opt", "bin", "fircd"),
		TunvlessBin:    filepath.Join(root, "opt", "sbin", "tunvless"),
		AltTunvlessBin: filepath.Join(root, "opt", "bin", "tunvless"),
		InitScript:     filepath.Join(root, "opt", "etc", "init.d", "S99firc"),
	})
	op.SetLifecycleHooks(
		func(context.Context) error {
			starts.Add(1)
			running.Store(true)
			return nil
		},
		func(context.Context) error {
			stops.Add(1)
			running.Store(false)
			return nil
		},
	)
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	preSt := inst.GetInstallStatus(ctx, op, true)
	if preSt.ExternalPackage != "firc" || !preSt.ExternalMigratable || preSt.InstalledVersion != "0.8.2-1" {
		t.Fatalf("unexpected pre-migration status: %+v", preSt)
	}

	if err := inst.MigrateExternalRuntime(ctx, op); err != nil {
		t.Fatalf("MigrateExternalRuntime failed: %v", err)
	}
	if stops.Load() != 1 || starts.Load() != 1 {
		t.Fatalf("expected 1 stop and 1 start during live migration, got stops=%d starts=%d", stops.Load(), starts.Load())
	}
	if pm.extRemoves != 1 || pm.installs != 1 {
		t.Fatalf("expected 1 external remove and 1 runtime install, got extRemoves=%d installs=%d", pm.extRemoves, pm.installs)
	}

	postSt := inst.GetInstallStatus(ctx, op, true)
	if postSt.ExternalPackage != "" || postSt.ExternalMigratable || postSt.InstalledVersion != "0.7.0-1" || postSt.State != InstallStateRunning {
		t.Fatalf("unexpected post-migration status: %+v", postSt)
	}
	if inst.PendingOperationAction() != "" {
		t.Fatalf("expected no pending journal action after success, got %q", inst.PendingOperationAction())
	}
	if _, err := os.Stat(inst.externalBackupDir()); !os.IsNotExist(err) {
		t.Fatalf("expected backup dir removed after completion, err=%v", err)
	}

	for rel, want := range map[string]string{
		"opt/etc/firc/firc.conf":             "configVersion: 0.7.0\napp:\n  httpWeb:\n    enabled: true\n",
		"opt/etc/firc/groups.yaml":           "configVersion: 0.8.2\ngroups:\n  - id: grp1\n    name: CustomGroup\n",
		"opt/etc/firc/tunnels.yaml":          "tunnels:\n  - id: vless0\n    device: tunvless0\n",
		"opt/etc/firc/awgm-ext.json":         "{\"serverRouting\":{\"vlessXhttpServer\":true}}\n",
		"opt/var/lib/firc/hwid":              "deadbeef12345678\n",
		"opt/var/lib/firc/rci-token":         "rci-token-secret\n",
		"opt/var/lib/firc/tunnels/cache.txt": "cached-sub-body\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Fatalf("file %s not preserved: got=%q err=%v want=%q", rel, string(got), err, want)
		}
	}
}

func TestExternalMigration_PreRemovalFailureRollsBackCleanly(t *testing.T) {
	ctx := context.Background()
	inst, pm, root := seedExternalMigrationRoot(t, "firc", "0.8.2-1")
	pm.failExtRemove = true

	var running atomic.Bool
	running.Store(true)
	var starts, stops atomic.Int32

	op := NewOperator(Options{
		ConfigDir:  filepath.Join(root, "opt", "etc", "firc"),
		InitScript: filepath.Join(root, "opt", "etc", "init.d", "S99firc"),
	})
	op.SetLifecycleHooks(
		func(context.Context) error { starts.Add(1); running.Store(true); return nil },
		func(context.Context) error { stops.Add(1); running.Store(false); return nil },
	)
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	if err := inst.MigrateExternalRuntime(ctx, op); err == nil {
		t.Fatal("expected MigrateExternalRuntime to fail when external remove fails")
	}
	if pm.installs != 0 {
		t.Fatalf("expected 0 runtime installs after failed external remove, got %d", pm.installs)
	}
	if starts.Load() != 1 || !running.Load() {
		t.Fatalf("expected external firc restarted after pre-removal failure, starts=%d running=%v", starts.Load(), running.Load())
	}
	if st := inst.GetInstallStatus(ctx, op, true); st.ExternalPackage != "firc" {
		t.Fatalf("expected external package preserved, got %+v", st)
	}
	if inst.PendingOperationAction() != "" {
		t.Fatalf("expected clean pending journal on pre-removal rollback, got %q", inst.PendingOperationAction())
	}
}

func TestExternalMigration_PostRemovalFailureAndStartupRecovery(t *testing.T) {
	ctx := context.Background()
	inst, pm, root := seedExternalMigrationRoot(t, "firc", "0.8.2-1")
	pm.failNextInstall = true
	inst.SetFircRunningChecker(func(context.Context) bool { return false })
	inst.SetTunvlessRunningChecker(func(context.Context) bool { return false })

	err := inst.MigrateExternalRuntime(ctx, nil)
	if err == nil {
		t.Fatal("expected error when runtime install fails after external package removal")
	}
	if inst.PendingOperationAction() != "migrate_external" {
		t.Fatalf("expected pending action migrate_external, got %q", inst.PendingOperationAction())
	}
	if st := inst.GetInstallStatus(ctx, nil, false); st.State != InstallStateRecoveryNeeded || st.ExternalPackage != "" {
		t.Fatalf("expected recovery_required with external package gone, got %+v", st)
	}

	// Simulate daemon restart offline with cached verified IPK and intact backup.
	inst.SetDownloader(nil)
	if recErr := inst.RecoverPendingOperationOnStartup(ctx, nil); recErr != nil {
		t.Fatalf("RecoverPendingOperationOnStartup failed: %v", recErr)
	}
	if inst.PendingOperationAction() != "" {
		t.Fatalf("expected pending action cleared after startup recovery, got %q", inst.PendingOperationAction())
	}
	if st := inst.GetInstallStatus(ctx, nil, false); st.State != InstallStateInstalled || st.InstalledVersion != "0.7.0-1" {
		t.Fatalf("unexpected status after recovery: %+v", st)
	}
	gotHWID, err := os.ReadFile(filepath.Join(root, "opt", "var", "lib", "firc", "hwid"))
	if err != nil || string(gotHWID) != "deadbeef12345678\n" {
		t.Fatalf("expected hwid restored during startup recovery, got=%q err=%v", string(gotHWID), err)
	}
}

func TestExternalMigration_RejectsUnsupportedPackageOrActiveDependents(t *testing.T) {
	ctx := context.Background()

	instCustom, pmCustom, _ := seedExternalMigrationRoot(t, "custom-firc", "1.0.0")
	instCustom.SetFircRunningChecker(func(context.Context) bool { return false })
	instCustom.SetTunvlessRunningChecker(func(context.Context) bool { return false })
	if st := instCustom.GetInstallStatus(ctx, nil, false); st.ExternalPackage != "custom-firc" || st.ExternalMigratable {
		t.Fatalf("expected custom-firc non-migratable, got %+v", st)
	}
	if err := instCustom.MigrateExternalRuntime(ctx, nil); err == nil {
		t.Fatal("expected unsupported external package migration to fail")
	}
	if pmCustom.extRemoves != 0 || pmCustom.installs != 0 {
		t.Fatalf("expected zero opkg calls for unsupported package")
	}

	instDeps, pmDeps, _ := seedExternalMigrationRoot(t, "firc", "0.8.2-1")
	instDeps.SetFircRunningChecker(func(context.Context) bool { return false })
	instDeps.SetTunvlessRunningChecker(func(context.Context) bool { return false })
	instDeps.SetDependencyChecker(func(context.Context) ([]string, error) {
		return []string{"vless_xhttp_server"}, nil
	})
	err := instDeps.MigrateExternalRuntime(ctx, nil)
	if !errors.Is(err, ErrFircDependencies) {
		t.Fatalf("expected ErrFircDependencies, got: %v", err)
	}
	if pmDeps.extRemoves != 0 || pmDeps.installs != 0 {
		t.Fatalf("expected zero opkg calls when dependent services exist")
	}
}

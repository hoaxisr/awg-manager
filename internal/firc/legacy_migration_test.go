package firc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func seedLegacyBundledInstallation(t *testing.T, root string) (origList, origConffiles []byte) {
	t.Helper()
	legacyFiles := map[string]string{
		"opt/sbin/fircd":                    "legacy-fircd-binary",
		"opt/sbin/tunvless":                 "legacy-tunvless-binary",
		"opt/etc/init.d/S99firc":            "#!/bin/sh\n# legacy S99firc\nexit 0\n",
		"opt/etc/ndm/netfilter.d/100-firc":  "#!/bin/sh\n# legacy 100-firc\nexit 0\n",
		"opt/etc/firc/SHA256SUMS":           "0000  fircd\n",
		"opt/etc/firc/firc.conf":            "customUserConfig: keep-me\nlistenPort: 5353\n",
		"opt/etc/firc/groups.yaml":          "groups:\n  - name: CustomUserGroup\n",
		"opt/etc/firc/tunnels.yaml":         "tunnels:\n  - id: tun-user-1\n",
		"opt/etc/firc/awgm-ext.json":        "{\"serverRouting\":{\"enabled\":true}}\n",
		"opt/etc/firc/nested/custom.yaml":   "nestedCustom: keep-me-too\n",
		"opt/etc/firc-other/unrelated.conf": "unrelated: stay-owned\n",
		"opt/bin/awg-manager":               "awg-manager-binary",
		"opt/etc/awg-manager/settings.json": "{\"port\":2222}\n",
	}
	for rel, body := range legacyFiles {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for linkRel, target := range map[string]string{
		"opt/bin/fircd":                         "../sbin/fircd",
		"opt/bin/tunvless":                      "../sbin/tunvless",
		"opt/etc/ndm/iflayerchanged.d/100-firc": "../netfilter.d/100-firc",
	} {
		full := filepath.Join(root, filepath.FromSlash(linkRel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}

	origList = []byte(strings.Join([]string{
		"/opt/bin/awg-manager",
		"/opt/sbin/awg",
		"/opt/sbin/fircd",
		"/opt/bin/fircd",
		"/opt/sbin/tunvless",
		"/opt/bin/tunvless",
		"/opt/etc/init.d/S99firc",
		"/opt/etc/ndm/netfilter.d/100-firc",
		"/opt/etc/ndm/iflayerchanged.d/100-firc",
		"/opt/etc/firc",
		"/opt/etc/firc/firc.conf",
		"/opt/etc/firc/groups.yaml",
		"/opt/etc/firc/tunnels.yaml",
		"/opt/etc/firc/awgm-ext.json",
		"/opt/etc/firc/nested/custom.yaml",
		"/opt/etc/firc/SHA256SUMS",
		"/opt/etc/firc-other/unrelated.conf",
		"/opt/etc/init.d/S99awg-manager",
	}, "\n") + "\n")
	origConffiles = []byte(strings.Join([]string{
		"/opt/etc/firc/firc.conf",
		"/opt/etc/firc/groups.yaml",
		"/opt/etc/firc/tunnels.yaml",
		"/opt/etc/firc/nested/custom.yaml",
		"/opt/etc/firc-other/unrelated.conf",
		"/opt/etc/awg-manager/settings.json",
	}, "\n") + "\n")

	infoDir := filepath.Join(root, "opt/lib/opkg/info")
	if err := os.MkdirAll(infoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "awg-manager.list"), origList, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infoDir, "awg-manager.conffiles"), origConffiles, 0644); err != nil {
		t.Fatal(err)
	}
	return origList, origConffiles
}

func TestLegacyBundledOwnershipMigration_PreservesConfigsAndActiveService(t *testing.T) {
	inst, pm, _ := runtimeFixture(t)
	root := inst.layout.RootDir
	seedLegacyBundledInstallation(t, root)

	var running atomic.Bool
	running.Store(true)
	var stopCalls, startCalls atomic.Int32

	op := NewOperator(Options{
		ConfigDir:   filepath.Join(root, "opt/etc/firc"),
		FircdBinary: inst.layout.FircdBin,
		TunvlessBin: inst.layout.TunvlessBin,
		InitScript:  inst.layout.InitScript,
	})
	op.SetLifecycleHooks(
		func(context.Context) error {
			startCalls.Add(1)
			running.Store(true)
			return nil
		},
		func(context.Context) error {
			stopCalls.Add(1)
			running.Store(false)
			return nil
		},
	)
	inst.SetRuntimeLocker(op.LockRuntimeMutation)
	inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })

	ctx := context.Background()
	stBefore := inst.GetInstallStatus(ctx, op, true)
	if !stBefore.LegacyBundledOwnership || stBefore.State != InstallStateRunning {
		t.Fatalf("unexpected pre-migration status: %+v", stBefore)
	}

	if err := inst.MigrateLegacyBundledRuntime(ctx, op); err != nil {
		t.Fatalf("MigrateLegacyBundledRuntime failed: %v", err)
	}
	if stopCalls.Load() != 1 || startCalls.Load() != 1 || !running.Load() {
		t.Fatalf("expected clean stop+restart of active FIRC: stops=%d starts=%d running=%v", stopCalls.Load(), startCalls.Load(), running.Load())
	}

	stAfter := inst.GetInstallStatus(ctx, op, true)
	if stAfter.LegacyBundledOwnership || stAfter.InstalledVersion != "0.7.0-1" || stAfter.State != InstallStateRunning {
		t.Fatalf("unexpected post-migration status: %+v", stAfter)
	}

	// Verify user configs (including nested files) were preserved verbatim rather than replaced by package defaults.
	for rel, want := range map[string]string{
		"opt/etc/firc/firc.conf":          "customUserConfig: keep-me\nlistenPort: 5353\n",
		"opt/etc/firc/groups.yaml":        "groups:\n  - name: CustomUserGroup\n",
		"opt/etc/firc/tunnels.yaml":       "tunnels:\n  - id: tun-user-1\n",
		"opt/etc/firc/awgm-ext.json":      "{\"serverRouting\":{\"enabled\":true}}\n",
		"opt/etc/firc/nested/custom.yaml": "nestedCustom: keep-me-too\n",
	} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s mismatch: got %q, want %q", rel, string(got), want)
		}
	}

	// Verify awg-manager.list keeps manager paths and /opt/etc/firc-other/*, while dropping all /opt/etc/firc(/.*) and FIRC runtime paths.
	newList, err := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.list"))
	if err != nil {
		t.Fatal(err)
	}
	listStr := string(newList)
	if !strings.Contains(listStr, "/opt/bin/awg-manager") || !strings.Contains(listStr, "/opt/etc/firc-other/unrelated.conf") {
		t.Fatalf("expected awg-manager and /opt/etc/firc-other/unrelated.conf to remain in awg-manager.list:\n%s", listStr)
	}
	if strings.Contains(listStr, "/opt/etc/firc\n") || strings.Contains(listStr, "/opt/etc/firc/") || strings.Contains(listStr, "fircd") || strings.Contains(listStr, "tunvless") || strings.Contains(listStr, "100-firc") || strings.Contains(listStr, "S99firc") {
		t.Fatalf("unexpected legacy FIRC entry left in awg-manager.list:\n%s", listStr)
	}
	newConffiles, err := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.conffiles"))
	if err != nil {
		t.Fatal(err)
	}
	wantConffiles := "/opt/etc/firc-other/unrelated.conf\n/opt/etc/awg-manager/settings.json"
	if strings.TrimSpace(string(newConffiles)) != wantConffiles {
		t.Fatalf("unexpected cleaned awg-manager.conffiles:\ngot:  %q\nwant: %q", strings.TrimSpace(string(newConffiles)), wantConffiles)
	}

	// Verify /opt/bin symlinks point to ../sbin/* without duplicate binary copies.
	for _, binName := range []string{"fircd", "tunvless"} {
		linkTarget, err := os.Readlink(filepath.Join(root, "opt/bin", binName))
		if err != nil || filepath.ToSlash(linkTarget) != "../sbin/"+binName {
			t.Fatalf("expected /opt/bin/%s -> ../sbin/%s, got %q (err=%v)", binName, binName, linkTarget, err)
		}
	}

	// Verify subsequent uninstall removes runtime + NDM ext hook while keeping user configs.
	extHook := filepath.Join(root, "opt/etc/ndm/netfilter.d/66-awgm-firc-ext.sh")
	if err := os.WriteFile(extHook, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	running.Store(false)
	if err := inst.Uninstall(ctx, op, false, false); err != nil {
		t.Fatalf("post-migration Uninstall failed: %v", err)
	}
	if _, err := os.Lstat(extHook); !os.IsNotExist(err) {
		t.Fatalf("expected 66-awgm-firc-ext.sh to be removed on uninstall, got err=%v", err)
	}
	if pm.removes != 1 {
		t.Fatalf("expected 1 package remove, got %d", pm.removes)
	}
}

func TestLegacyBundledOwnershipMigration_RollbackOnInstallOrStartFailure(t *testing.T) {
	t.Run("opkg_install_failure_restores_legacy_files_ownership_and_running_service", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		root := inst.layout.RootDir
		origList, origConffiles := seedLegacyBundledInstallation(t, root)

		var running atomic.Bool
		running.Store(true)
		op := NewOperator(Options{
			ConfigDir:   filepath.Join(root, "opt/etc/firc"),
			FircdBinary: inst.layout.FircdBin,
			TunvlessBin: inst.layout.TunvlessBin,
			InitScript:  inst.layout.InitScript,
		})
		op.SetLifecycleHooks(
			func(context.Context) error { running.Store(true); return nil },
			func(context.Context) error { running.Store(false); return nil },
		)
		inst.SetRuntimeLocker(op.LockRuntimeMutation)
		inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })

		pm.failNextInstall = true
		if err := inst.MigrateLegacyBundledRuntime(context.Background(), op); err == nil {
			t.Fatal("expected migration error when opkg install fails")
		}

		legacyOwned, err := inst.HasLegacyBundledOwnership()
		if err != nil || !legacyOwned {
			t.Fatalf("expected legacy ownership to be rolled back: owned=%v err=%v", legacyOwned, err)
		}
		if !running.Load() {
			t.Fatal("expected legacy FIRC service to be restarted on rollback")
		}
		gotFircd, _ := os.ReadFile(inst.layout.FircdBin)
		if string(gotFircd) != "legacy-fircd-binary" {
			t.Fatalf("legacy fircd not restored: %q", string(gotFircd))
		}
		gotList, _ := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.list"))
		gotConf, _ := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.conffiles"))
		if !bytes.Equal(gotList, origList) || !bytes.Equal(gotConf, origConffiles) {
			t.Fatal("opkg ownership metadata did not roll back byte-for-byte")
		}
	})

	t.Run("restart_failure_rolls_back_installed_package_to_legacy_owner", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		root := inst.layout.RootDir
		origList, _ := seedLegacyBundledInstallation(t, root)

		var running atomic.Bool
		running.Store(true)
		var startAttempt atomic.Int32
		op := NewOperator(Options{
			ConfigDir:   filepath.Join(root, "opt/etc/firc"),
			FircdBinary: inst.layout.FircdBin,
			TunvlessBin: inst.layout.TunvlessBin,
			InitScript:  inst.layout.InitScript,
		})
		op.SetLifecycleHooks(
			func(context.Context) error {
				if startAttempt.Add(1) == 1 {
					return errors.New("simulated new runtime startup failure")
				}
				running.Store(true)
				return nil
			},
			func(context.Context) error { running.Store(false); return nil },
		)
		inst.SetRuntimeLocker(op.LockRuntimeMutation)
		inst.SetFircRunningChecker(func(context.Context) bool { return running.Load() })

		if err := inst.MigrateLegacyBundledRuntime(context.Background(), op); err == nil {
			t.Fatal("expected migration error when post-migration service start fails")
		}
		if ver, _ := pm.Version(context.Background(), RuntimePackageName); ver != "" {
			t.Fatalf("expected awg-firc-runtime to be removed on rollback, got %q", ver)
		}
		gotList, _ := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.list"))
		if !bytes.Equal(gotList, origList) {
			t.Fatal("expected awg-manager.list to be restored on rollback")
		}
		gotCfg, _ := os.ReadFile(filepath.Join(root, "opt/etc/firc/firc.conf"))
		if string(gotCfg) != "customUserConfig: keep-me\nlistenPort: 5353\n" {
			t.Fatalf("custom firc.conf corrupted: %q", string(gotCfg))
		}
		if !running.Load() {
			t.Fatal("expected legacy service to be running after rollback")
		}
	})
}

func TestLegacyBundledOwnershipMigration_NestedConfigBoundaryAndInterruptedBackupIntegrity(t *testing.T) {
	t.Run("nested_configs_detached_while_firc_other_remains_owned", func(t *testing.T) {
		inst, _, _ := runtimeFixture(t)
		seedLegacyBundledInstallation(t, inst.layout.RootDir)
		listPath := filepath.Join(inst.layout.RootDir, "opt/lib/opkg/info/awg-manager.list")
		if err := inst.stripLegacyEntriesFromAwgManagerOpkgInfo(); err != nil {
			t.Fatal(err)
		}
		remaining, err := os.ReadFile(listPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(remaining), "/opt/etc/firc/") || strings.Contains(string(remaining), "/opt/etc/firc\n") {
			t.Fatalf("FIRC configs remain owned by old manager:\n%s", remaining)
		}
		if !strings.Contains(string(remaining), "/opt/etc/firc-other/unrelated.conf") {
			t.Fatalf("expected /opt/etc/firc-other/unrelated.conf to remain owned:\n%s", remaining)
		}
		owned, err := inst.HasLegacyBundledOwnership()
		if err != nil || owned {
			t.Fatalf("expected HasLegacyBundledOwnership=false after stripping, got owned=%v err=%v", owned, err)
		}
	})

	t.Run("missing_original_backup_during_pending_migration_fails_closed", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		seedLegacyBundledInstallation(t, inst.layout.RootDir)
		if err := inst.stripLegacyEntriesFromAwgManagerOpkgInfo(); err != nil {
			t.Fatal(err)
		}
		if err := inst.saveOperation(runtimeOperation{Action: "migrate_legacy", Pending: true}); err != nil {
			t.Fatal(err)
		}
		err := inst.MigrateLegacyBundledRuntime(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "recovery_required") {
			t.Fatalf("expected fail-closed recovery_required error when original backup is missing, got %v", err)
		}
		if pm.installs != 0 {
			t.Fatalf("expected 0 package installs when backup is missing, got %d", pm.installs)
		}
		st := inst.GetInstallStatus(context.Background(), nil, false)
		if st.State != InstallStateRecoveryNeeded {
			t.Fatalf("expected InstallStateRecoveryNeeded, got %s", st.State)
		}
	})

	t.Run("corrupted_backup_blob_during_pending_migration_fails_closed_without_deleting_backup", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		seedLegacyBundledInstallation(t, inst.layout.RootDir)
		backupDir := inst.legacyBackupDir()
		manifest, err := inst.captureLegacyBackup(backupDir, false, nil)
		if err != nil {
			t.Fatalf("captureLegacyBackup failed: %v", err)
		}
		var targetBlob string
		for _, entry := range manifest.RuntimeEntries {
			if !entry.IsSymlink && entry.BackupName != "" {
				targetBlob = entry.BackupName
				break
			}
		}
		if targetBlob == "" {
			t.Fatal("expected regular file blob in backup manifest")
		}
		if err := inst.saveOperation(runtimeOperation{Action: "migrate_legacy", Pending: true}); err != nil {
			t.Fatal(err)
		}
		blobPath := filepath.Join(backupDir, "blobs", targetBlob)
		if err := os.WriteFile(blobPath, []byte("corrupted-blob-bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		err = inst.MigrateLegacyBundledRuntime(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") && !strings.Contains(err.Error(), "size mismatch") {
			t.Fatalf("expected backup checksum/size verification error, got %v", err)
		}
		if pm.installs != 0 {
			t.Fatalf("expected 0 package installs on corrupted backup, got %d", pm.installs)
		}
		if _, statErr := os.Stat(filepath.Join(backupDir, "manifest.json")); statErr != nil {
			t.Fatalf("backup manifest must not be deleted on failed verification: %v", statErr)
		}
		opState, _ := inst.readOperation()
		if !opState.Pending {
			t.Fatal("expected Pending=true to remain set on failed backup verification")
		}
	})
}

func TestLegacyBundledOwnershipMigration_CrashDurabilityAndFaultInjection(t *testing.T) {
	t.Run("blob_fsync_failure_aborts_before_pending_or_mutation", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		origList, _ := seedLegacyBundledInstallation(t, inst.layout.RootDir)
		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{
			FileSync: func(relName string) error {
				if strings.HasPrefix(relName, "blobs/") {
					return errors.New("simulated EIO during blob fsync")
				}
				return nil
			},
		})
		err := inst.MigrateLegacyBundledRuntime(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "simulated EIO during blob fsync") {
			t.Fatalf("expected blob fsync error, got %v", err)
		}
		if pm.installs != 0 {
			t.Fatalf("expected 0 installs, got %d", pm.installs)
		}
		opState, _ := inst.readOperation()
		if opState.Pending {
			t.Fatal("expected Pending=false when backup capture fails before mutation")
		}
		gotList, _ := os.ReadFile(filepath.Join(inst.layout.RootDir, "opt/lib/opkg/info/awg-manager.list"))
		if !bytes.Equal(gotList, origList) {
			t.Fatal("awg-manager.list must remain untouched when backup fsync fails")
		}
	})

	t.Run("backup_rename_and_parent_dir_fsync_failures_abort_before_mutation", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		origList, _ := seedLegacyBundledInstallation(t, inst.layout.RootDir)
		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{
			BackupRename: func(src, dst string) error {
				return errors.New("simulated rename failure")
			},
		})
		if err := inst.MigrateLegacyBundledRuntime(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "simulated rename failure") {
			t.Fatalf("expected rename failure, got %v", err)
		}
		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{
			ParentDirSync: func(dir string) error {
				return errors.New("simulated parent dir fsync failure")
			},
		})
		if err := inst.MigrateLegacyBundledRuntime(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "simulated parent dir fsync failure") {
			t.Fatalf("expected parent dir fsync failure, got %v", err)
		}
		if pm.installs != 0 {
			t.Fatalf("expected 0 installs, got %d", pm.installs)
		}
		gotList, _ := os.ReadFile(filepath.Join(inst.layout.RootDir, "opt/lib/opkg/info/awg-manager.list"))
		if !bytes.Equal(gotList, origList) {
			t.Fatal("awg-manager.list must remain untouched when parent dir fsync fails")
		}
	})

	t.Run("final_journal_write_failure_preserves_backup_dir_and_recovers_on_startup", func(t *testing.T) {
		inst, _, _ := runtimeFixture(t)
		origList, origConffiles := seedLegacyBundledInstallation(t, inst.layout.RootDir)
		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{
			SaveOperation: func(action string, pending bool) error {
				if !pending {
					return errors.New("simulated ENOSPC on final journal clear")
				}
				return nil
			},
		})
		err := inst.MigrateLegacyBundledRuntime(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "simulated ENOSPC on final journal clear") {
			t.Fatalf("expected final journal save error, got %v", err)
		}
		// Crucial P1-3 invariant: backupDir MUST NOT be deleted when saving the final journal failed!
		if _, verifyErr := inst.verifyLegacyBackup(inst.legacyBackupDir()); verifyErr != nil {
			t.Fatalf("legacy backup must remain intact when final journal write fails: %v", verifyErr)
		}
		opState, _ := inst.readOperation()
		if !opState.Pending {
			t.Fatal("journal on disk must still be Pending=true after failed final journal write")
		}

		// Clear fault hook and run startup recovery gate: must recover cleanly using preserved backup.
		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{})
		if recErr := inst.RecoverPendingOperationOnStartup(context.Background(), nil); recErr != nil {
			t.Fatalf("RecoverPendingOperationOnStartup failed: %v", recErr)
		}
		opStateAfter, _ := inst.readOperation()
		if opStateAfter.Pending {
			t.Fatal("expected Pending=false after startup recovery")
		}
		gotList, _ := os.ReadFile(filepath.Join(inst.layout.RootDir, "opt/lib/opkg/info/awg-manager.list"))
		gotConf, _ := os.ReadFile(filepath.Join(inst.layout.RootDir, "opt/lib/opkg/info/awg-manager.conffiles"))
		if !bytes.Equal(gotList, origList) || !bytes.Equal(gotConf, origConffiles) {
			t.Fatal("startup recovery did not restore original ownership metadata byte-for-byte")
		}
	})
}

func TestStartupPackageRecoveryGate_LegacyMigrationAndManualDisable(t *testing.T) {
	t.Run("crash_after_ownership_detach_before_package_install_recovers_on_startup", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		root := inst.layout.RootDir
		origList, origConffiles := seedLegacyBundledInstallation(t, root)

		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{
			AfterOwnershipDetached: func() error {
				return ErrSimulatedProcessCrash
			},
		})
		err := inst.MigrateLegacyBundledRuntime(context.Background(), nil)
		if !errors.Is(err, ErrSimulatedProcessCrash) {
			t.Fatalf("expected simulated crash after ownership detach, got %v", err)
		}
		if pm.installs != 0 {
			t.Fatalf("expected crash before opkg install, got installs=%d", pm.installs)
		}
		if owned, _ := inst.HasLegacyBundledOwnership(); owned {
			t.Fatal("expected legacy ownership to be detached at crash point")
		}
		if _, statErr := os.Stat(inst.layout.FircdBin); !os.IsNotExist(statErr) {
			t.Fatal("expected legacy fircd binary to be removed at crash point")
		}

		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{})
		if recErr := inst.RecoverPendingOperationOnStartup(context.Background(), nil); recErr != nil {
			t.Fatalf("RecoverPendingOperationOnStartup failed: %v", recErr)
		}
		if owned, err := inst.HasLegacyBundledOwnership(); err != nil || !owned {
			t.Fatalf("expected legacy ownership restored on startup, got owned=%v err=%v", owned, err)
		}
		gotFircd, _ := os.ReadFile(inst.layout.FircdBin)
		if string(gotFircd) != "legacy-fircd-binary" {
			t.Fatalf("expected legacy-fircd-binary restored, got %q", string(gotFircd))
		}
		gotNested, _ := os.ReadFile(filepath.Join(root, "opt/etc/firc/nested/custom.yaml"))
		if string(gotNested) != "nestedCustom: keep-me-too\n" {
			t.Fatalf("expected nested custom.yaml restored, got %q", string(gotNested))
		}
		gotList, _ := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.list"))
		gotConf, _ := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.conffiles"))
		if !bytes.Equal(gotList, origList) || !bytes.Equal(gotConf, origConffiles) {
			t.Fatal("expected opkg metadata restored byte-for-byte on startup")
		}
		if _, statErr := os.Stat(inst.legacyBackupDir()); !os.IsNotExist(statErr) {
			t.Fatal("expected backupDir to be cleaned up after successful startup recovery")
		}
	})

	t.Run("crash_after_package_install_before_journal_clear_recovers_and_preserves_manual_disable", func(t *testing.T) {
		inst, pm, _ := runtimeFixture(t)
		root := inst.layout.RootDir
		origList, _ := seedLegacyBundledInstallation(t, root)

		var startCalls atomic.Int32
		op := NewOperator(Options{
			ConfigDir:   filepath.Join(root, "opt/etc/firc"),
			FircdBinary: inst.layout.FircdBin,
			TunvlessBin: inst.layout.TunvlessBin,
			InitScript:  inst.layout.InitScript,
		})
		op.SetLifecycleHooks(
			func(context.Context) error { startCalls.Add(1); return nil },
			func(context.Context) error { return nil },
		)
		inst.SetRuntimeLocker(op.LockRuntimeMutation)

		// User explicitly stopped/disabled FIRC before migration.
		if err := op.StopByUser(context.Background()); err != nil {
			t.Fatalf("StopByUser failed: %v", err)
		}
		if !op.ManuallyDisabled() {
			t.Fatal("expected ManuallyDisabled=true after StopByUser")
		}

		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{
			AfterPackageInstalled: func() error {
				// Simulate .awgm-disabled disappearing during mid-migration crash after package install.
				_ = os.Remove(op.disabledPath())
				return ErrSimulatedProcessCrash
			},
		})
		err := inst.MigrateLegacyBundledRuntime(context.Background(), op)
		if !errors.Is(err, ErrSimulatedProcessCrash) {
			t.Fatalf("expected simulated crash after package install, got %v", err)
		}
		if ver, _ := pm.Version(context.Background(), RuntimePackageName); ver != "0.7.0-1" {
			t.Fatalf("expected standalone runtime to be installed at crash point, got %q", ver)
		}

		inst.SetLegacyMigrationFaultHooks(LegacyMigrationFaultHooks{})
		if recErr := inst.RecoverPendingOperationOnStartup(context.Background(), op); recErr != nil {
			t.Fatalf("RecoverPendingOperationOnStartup failed: %v", recErr)
		}
		if ver, _ := pm.Version(context.Background(), RuntimePackageName); ver != "" {
			t.Fatalf("expected standalone runtime to be removed during startup rollback, got %q", ver)
		}
		gotList, _ := os.ReadFile(filepath.Join(root, "opt/lib/opkg/info/awg-manager.list"))
		if !bytes.Equal(gotList, origList) {
			t.Fatal("expected awg-manager.list restored on startup")
		}
		if !op.ManuallyDisabled() {
			t.Fatal("expected manual-disable (.awgm-disabled) to be restored across startup recovery")
		}
		if startCalls.Load() != 0 {
			t.Fatalf("expected 0 daemon starts during startup recovery of disabled FIRC, got %d", startCalls.Load())
		}
	})
}

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
	if err := inst.MigrateLegacyBundledRuntime(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "external package firc.list") {
		t.Fatalf("expected external package refusal on MigrateLegacyBundledRuntime, got %v", err)
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

type shellRunner struct {
	name string
	bin  string
	args []string
}

func discoverPOSIXShells(t *testing.T) []shellRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		candidates := []string{
			`C:\Program Files\Git\bin\bash.exe`,
			`C:\Program Files\Git\usr\bin\bash.exe`,
		}
		for _, c := range candidates {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return []shellRunner{{name: "git-bash", bin: c}}
			}
		}
		t.Skip("Git Bash not found on Windows; skipping POSIX shell fault-injection test")
	}
	var out []shellRunner
	if sh, err := exec.LookPath("sh"); err == nil {
		out = append(out, shellRunner{name: "sh", bin: sh})
	}
	if bb, err := exec.LookPath("busybox"); err == nil {
		out = append(out, shellRunner{name: "busybox-sh", bin: bb, args: []string{"sh"}})
	}
	if len(out) == 0 {
		t.Fatal("no POSIX shell found on non-Windows host")
	}
	return out
}

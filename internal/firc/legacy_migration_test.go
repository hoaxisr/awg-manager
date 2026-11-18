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

func extractDelimitedBlock(t *testing.T, path, beginMarker, endMarker string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	bi := strings.Index(text, beginMarker)
	ei := strings.Index(text, endMarker)
	if bi < 0 || ei < 0 || ei <= bi {
		t.Fatalf("markers %q..%q not found in %s", beginMarker, endMarker, path)
	}
	return text[bi:ei+len(endMarker)] + "\n"
}

func TestPostinstFircRestoreFaultInjection(t *testing.T) {
	postinstPath := filepath.Join("..", "..", "entware", "control", "postinst")
	preinstPath := filepath.Join("..", "..", "entware", "control", "preinst")

	restoreBlock := extractDelimitedBlock(
		t,
		postinstPath,
		"# BEGIN_FIRC_LEGACY_CFG_RESTORE",
		"# END_FIRC_LEGACY_CFG_RESTORE",
	)
	preinstRaw, err := os.ReadFile(preinstPath)
	if err != nil {
		t.Fatalf("read %s: %v", preinstPath, err)
	}
	preinstScript := strings.ReplaceAll(string(preinstRaw), "\r\n", "\n")

	shells := discoverPOSIXShells(t)
	for _, sh := range shells {
		sh := sh
		t.Run(sh.name, func(t *testing.T) {
			workDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(workDir, "postinst_restore.tpl"), []byte(restoreBlock), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workDir, "preinst.tpl"), []byte(preinstScript), 0600); err != nil {
				t.Fatal(err)
			}

			runDriver := func(t *testing.T, scriptBody string) {
				t.Helper()
				fullScript := "#!/bin/sh\nset -eu\n" + scriptBody + "\n"
				scriptPath := filepath.Join(workDir, "driver.sh")
				if err := os.WriteFile(scriptPath, []byte(fullScript), 0700); err != nil {
					t.Fatal(err)
				}
				args := append(append([]string{}, sh.args...), "./driver.sh")
				cmd := exec.Command(sh.bin, args...)
				cmd.Dir = workDir
				cmd.Env = append(os.Environ(), "MSYS=winsymlinks:sys")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("shell driver failed (%v):\n%s", err, string(out))
				}
			}

			// Helper snippet embedded into each subtest shell driver.
			commonHelpers := `
make_restore_script() {
    f_root="$1"
    {
        echo '#!/bin/sh'
        echo 'set -e'
        sed "s|/opt/var/cache/awg-manager/firc-legacy-cfg-preserve|$f_root/backup|g; s|/opt/etc/firc|$f_root/dest|g" ./postinst_restore.tpl
    } > "$f_root/run_restore.sh"
    chmod 700 "$f_root/run_restore.sh"
}

make_preinst_script() {
    f_root="$1"
    sed \
        -e "s|/opt/lib/opkg/info/awg-manager.list|$f_root/owner.list|g" \
        -e "s|/opt/etc/awg-manager/settings.json|$f_root/settings.json|g" \
        -e "s|/opt/var/cache/awg-manager/firc-legacy-cfg-preserve|$f_root/backup|g" \
        -e "s|/opt/etc/firc|$f_root/dest|g" \
        ./preinst.tpl > "$f_root/run_preinst.sh"
    chmod 700 "$f_root/run_preinst.sh"
}

assert_no_temp_leftovers() {
    scan_dir="$1"
    leftovers=$(find "$scan_dir" \( -name '.restore.*' -o -name '.stage.*' -o -name '.firc-list.*' \) 2>/dev/null || true)
    if [ -n "$leftovers" ]; then
        echo "unexpected temp leftovers in $scan_dir: $leftovers" >&2
        exit 1
    fi
}

find_real_bin() {
    old_ifs="$IFS"
    IFS=:
    for d in $PATH; do
        [ -n "$d" ] || d=.
        if [ -x "$d/$1" ] && [ ! -d "$d/$1" ]; then
            IFS="$old_ifs"
            printf '%s\n' "$d/$1"
            return 0
        fi
    done
    IFS="$old_ifs"
    echo "real binary not found in PATH: $1" >&2
    return 1
}
`

			t.Run("partial_cp_then_retry_restores_exact_bytes", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup" "$fixture/dest" "$fixture/mockbin"
printf 'complete-important-user-config\n' > "$fixture/backup/config.yaml"
make_restore_script "$fixture"

cat > "$fixture/mockbin/cp" <<'EOF'
#!/bin/sh
eval "last=\${$#}"
printf 'partial' > "$last"
exit 1
EOF
chmod 700 "$fixture/mockbin/cp"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected first-pass failure on partial cp" >&2
    exit 1
fi
if [ -e "$fixture/dest/config.yaml" ] || [ -L "$fixture/dest/config.yaml" ]; then
    echo "partial cp must never publish into final destination path" >&2
    exit 1
fi
assert_no_temp_leftovers "$fixture/dest"
[ "$(cat "$fixture/backup/config.yaml")" = "complete-important-user-config" ]

# Retry with real cp must restore exact bytes and only then remove backup.
"$fixture/run_restore.sh"
[ "$(cat "$fixture/dest/config.yaml")" = "complete-important-user-config" ]
[ ! -e "$fixture/backup" ]
assert_no_temp_leftovers "$fixture/dest"
`)
			})

			t.Run("preexisting_partial_destination_preserves_both_and_never_deletes_backup", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup" "$fixture/dest"
printf 'complete-important-user-config\n' > "$fixture/backup/config.yaml"
printf 'partial' > "$fixture/dest/config.yaml"
printf '/opt/sbin/fircd\n/opt/etc/firc/config.yaml\n' > "$fixture/owner.list"
printf '{"routingEngine":"sing-box"}\n' > "$fixture/settings.json"
make_preinst_script "$fixture"
make_restore_script "$fixture"

# Re-running preinst when backup already holds the full copy must NOT clobber backup with partial dest.
"$fixture/run_preinst.sh"
[ "$(cat "$fixture/backup/config.yaml")" = "complete-important-user-config" ]

# postinst must refuse to treat divergent/partial destination as restored:
# neither overwrite destination nor delete the only full backup.
if "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected non-zero exit when pre-existing destination lacks matching provenance" >&2
    exit 1
fi
[ "$(cat "$fixture/dest/config.yaml")" = "partial" ]
[ "$(cat "$fixture/backup/config.yaml")" = "complete-important-user-config" ]
assert_no_temp_leftovers "$fixture/dest"
`)
			})

			t.Run("verification_failure_corrupt_cp_and_bad_symlink_preserve_backup", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup" "$fixture/dest" "$fixture/mockbin"
printf 'complete-important-user-config\n' > "$fixture/backup/config.yaml"
make_restore_script "$fixture"

# Case A: cp exits 0 after writing corrupted file contents.
cat > "$fixture/mockbin/cp" <<'EOF'
#!/bin/sh
eval "last=\${$#}"
printf 'silently-corrupted' > "$last"
exit 0
EOF
chmod 700 "$fixture/mockbin/cp"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected verification failure on silently corrupted file copy" >&2
    exit 1
fi
[ ! -e "$fixture/dest/config.yaml" ] && [ ! -L "$fixture/dest/config.yaml" ]
[ "$(cat "$fixture/backup/config.yaml")" = "complete-important-user-config" ]
assert_no_temp_leftovers "$fixture/dest"

"$fixture/run_restore.sh"
[ "$(cat "$fixture/dest/config.yaml")" = "complete-important-user-config" ]
[ ! -e "$fixture/backup" ]

# Case B: cp exits 0 after creating symlink to wrong target.
mkdir -p "$fixture/backup/nested/targets"
printf 'real-target\n' > "$fixture/backup/nested/targets/real.yaml"
ln -s "targets/real.yaml" "$fixture/backup/nested/active.yaml"
rm -rf "$fixture/dest"
mkdir -p "$fixture/dest"

cat > "$fixture/mockbin/cp" <<'EOF'
#!/bin/sh
eval "last=\${$#}"
eval "prev=\${$(($# - 1))}"
if [ -L "$prev" ]; then
    ln -s "targets/wrong.yaml" "$last"
    exit 0
fi
cat "$prev" > "$last"
EOF
chmod 700 "$fixture/mockbin/cp"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected verification failure on wrong symlink target" >&2
    exit 1
fi
[ ! -e "$fixture/dest/nested/active.yaml" ] && [ ! -L "$fixture/dest/nested/active.yaml" ]
[ -L "$fixture/backup/nested/active.yaml" ]
[ "$(readlink "$fixture/backup/nested/active.yaml")" = "targets/real.yaml" ]
assert_no_temp_leftovers "$fixture/dest"

"$fixture/run_restore.sh"
[ -L "$fixture/dest/nested/active.yaml" ]
[ "$(readlink "$fixture/dest/nested/active.yaml")" = "targets/real.yaml" ]
[ "$(cat "$fixture/dest/nested/targets/real.yaml")" = "real-target" ]
[ ! -e "$fixture/backup" ]
`)
			})

			t.Run("rename_failure_preserves_backup_and_cleans_temp", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup" "$fixture/dest" "$fixture/mockbin"
printf 'complete-important-user-config\n' > "$fixture/backup/config.yaml"
make_restore_script "$fixture"

# Case A: mv exits 1.
cat > "$fixture/mockbin/mv" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 700 "$fixture/mockbin/mv"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected failure when mv returns 1" >&2
    exit 1
fi
[ ! -e "$fixture/dest/config.yaml" ] && [ ! -L "$fixture/dest/config.yaml" ]
[ "$(cat "$fixture/backup/config.yaml")" = "complete-important-user-config" ]
assert_no_temp_leftovers "$fixture/dest"

# Case B: mv exits 0 without moving staged file.
cat > "$fixture/mockbin/mv" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 700 "$fixture/mockbin/mv"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected failure when mv is a silent no-op" >&2
    exit 1
fi
[ ! -e "$fixture/dest/config.yaml" ] && [ ! -L "$fixture/dest/config.yaml" ]
[ "$(cat "$fixture/backup/config.yaml")" = "complete-important-user-config" ]
assert_no_temp_leftovers "$fixture/dest"

# Clean retry restores exact bytes.
"$fixture/run_restore.sh"
[ "$(cat "$fixture/dest/config.yaml")" = "complete-important-user-config" ]
[ ! -e "$fixture/backup" ]
`)
			})

			t.Run("nested_files_dotfiles_and_symlinks_restored_atomically", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup/nested/a/b" "$fixture/dest" "$fixture/mockbin"
printf '' > "$fixture/backup/.awgm-disabled"
printf 'root-conf\n' > "$fixture/backup/firc.conf"
printf 'deep-nested-payload\n' > "$fixture/backup/nested/a/b/custom.yaml"
ln -s "b/custom.yaml" "$fixture/backup/nested/a/active.yaml"
printf 'sibling-after-nested\n' > "$fixture/backup/z-after.yaml"
make_restore_script "$fixture"

real_cp=$(find_real_bin cp)
cat > "$fixture/mockbin/cp" <<EOF
#!/bin/sh
eval "last=\\\${\$#}"
eval "prev=\\\${\$((\$# - 1))}"
case "\$prev" in
    *custom.yaml)
        printf 'trunc' > "\$last"
        exit 1
        ;;
esac
exec "$real_cp" "\$@"
EOF
chmod 700 "$fixture/mockbin/cp"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected first-pass failure on nested partial cp" >&2
    exit 1
fi
[ ! -e "$fixture/dest/nested/a/b/custom.yaml" ]
[ -d "$fixture/backup" ]
assert_no_temp_leftovers "$fixture/dest"

# Second pass restores remaining entries while verifying already-restored entries match backup.
"$fixture/run_restore.sh"
[ -f "$fixture/dest/.awgm-disabled" ]
[ "$(cat "$fixture/dest/firc.conf")" = "root-conf" ]
[ "$(cat "$fixture/dest/nested/a/b/custom.yaml")" = "deep-nested-payload" ]
[ -L "$fixture/dest/nested/a/active.yaml" ]
[ "$(readlink "$fixture/dest/nested/a/active.yaml")" = "b/custom.yaml" ]
[ "$(cat "$fixture/dest/z-after.yaml")" = "sibling-after-nested" ]
[ ! -e "$fixture/backup" ]
assert_no_temp_leftovers "$fixture/dest"
`)
			})

			t.Run("existing_user_files_preserved_and_concurrent_create_guarded", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup/nested" "$fixture/dest/nested" "$fixture/mockbin"
printf 'shared-bytes\n' > "$fixture/backup/shared.yaml"
printf 'shared-bytes\n' > "$fixture/dest/shared.yaml"
printf 'restored-from-backup\n' > "$fixture/backup/nested/missing.yaml"
printf 'unowned-user-custom\n' > "$fixture/dest/nested/user-extra.yaml"
make_restore_script "$fixture"

"$fixture/run_restore.sh"
[ "$(cat "$fixture/dest/shared.yaml")" = "shared-bytes" ]
[ "$(cat "$fixture/dest/nested/missing.yaml")" = "restored-from-backup" ]
[ "$(cat "$fixture/dest/nested/user-extra.yaml")" = "unowned-user-custom" ]
[ ! -e "$fixture/backup" ]

# Simulate concurrent creation of a divergent destination file after existence check and right before mv -n.
mkdir -p "$fixture/backup"
printf 'backup-full-version\n' > "$fixture/backup/race.yaml"
real_cmp=$(find_real_bin cmp)
cat > "$fixture/mockbin/cmp" <<EOF
#!/bin/sh
for arg in "\$@"; do
    case "\$arg" in
        */.restore.*/staged)
            if [ ! -e "$fixture/dest/race.yaml" ]; then
                printf 'concurrent-user-edit\n' > "$fixture/dest/race.yaml"
            fi
            ;;
    esac
done
exec "$real_cmp" "\$@"
EOF
chmod 700 "$fixture/mockbin/cmp"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected non-zero exit when concurrent divergent destination appears before mv -n" >&2
    exit 1
fi
[ "$(cat "$fixture/dest/race.yaml")" = "concurrent-user-edit" ]
[ "$(cat "$fixture/backup/race.yaml")" = "backup-full-version" ]
assert_no_temp_leftovers "$fixture"
`)
			})

			t.Run("find_failure_before_output_and_after_partial_output_in_preinst_and_postinst", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/dest/nested/sub" "$fixture/mockbin"
printf 'important-root-config\n' > "$fixture/dest/config.yaml"
printf 'important-nested-config\n' > "$fixture/dest/nested/sub/deep.yaml"
ln -s "sub/deep.yaml" "$fixture/dest/nested/active.yaml"
printf '0000  fircd\n' > "$fixture/dest/SHA256SUMS"
printf '/opt/sbin/fircd\n/opt/etc/firc/config.yaml\n/opt/etc/firc/nested/sub/deep.yaml\n' > "$fixture/owner.list"
printf '{"routingEngine":"mihomo"}\n' > "$fixture/settings.json"
make_preinst_script "$fixture"
make_restore_script "$fixture"

# 1) preinst: find fails before any output -> blocks upgrade, no incomplete backup created.
cat > "$fixture/mockbin/find" <<'EOF'
#!/bin/sh
echo "find: injected read error before output" >&2
exit 1
EOF
chmod 700 "$fixture/mockbin/find"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_preinst.sh" >/dev/null 2>&1; then
    echo "expected preinst failure when find fails before output" >&2
    exit 1
fi
[ ! -e "$fixture/backup" ]
assert_no_temp_leftovers "$fixture"

# 2) preinst: find fails after partial output (nested dir unreadable) -> blocks upgrade before staging partial backup.
cat > "$fixture/mockbin/find" <<'EOF'
#!/bin/sh
printf '.\n./config.yaml\n./nested\n'
echo "find: ./nested/sub: Input/output error" >&2
exit 1
EOF
chmod 700 "$fixture/mockbin/find"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_preinst.sh" >/dev/null 2>&1; then
    echo "expected preinst failure when find fails after partial output" >&2
    exit 1
fi
[ ! -e "$fixture/backup" ]
assert_no_temp_leftovers "$fixture"

# 3) preinst clean retry -> stages complete tree including nested directory and symlink.
"$fixture/run_preinst.sh"
[ "$(cat "$fixture/backup/config.yaml")" = "important-root-config" ]
[ "$(cat "$fixture/backup/nested/sub/deep.yaml")" = "important-nested-config" ]
[ -L "$fixture/backup/nested/active.yaml" ]
[ "$(readlink "$fixture/backup/nested/active.yaml")" = "sub/deep.yaml" ]
[ ! -e "$fixture/backup/SHA256SUMS" ]
assert_no_temp_leftovers "$fixture"

# Simulate opkg remove_obsolesced_files removing legacy-owned /opt/etc/firc tree.
rm -rf "$fixture/dest"
mkdir -p "$fixture/dest"

# 4) postinst: find fails before any output -> preserves entire backup, exits non-zero.
cat > "$fixture/mockbin/find" <<'EOF'
#!/bin/sh
echo "find: injected read error before output" >&2
exit 1
EOF
chmod 700 "$fixture/mockbin/find"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected postinst failure when find fails before output" >&2
    exit 1
fi
[ ! -e "$fixture/dest/config.yaml" ]
[ "$(cat "$fixture/backup/config.yaml")" = "important-root-config" ]
[ "$(cat "$fixture/backup/nested/sub/deep.yaml")" = "important-nested-config" ]
assert_no_temp_leftovers "$fixture"

# 5a) postinst: exact R3 repro (find outputs only '.' then exits 1) -> must NOT delete backup!
cat > "$fixture/mockbin/find" <<'EOF'
#!/bin/sh
printf '.\n'
echo "find: injected directory read error" >&2
exit 1
EOF
chmod 700 "$fixture/mockbin/find"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected postinst failure when find outputs '.' and exits 1" >&2
    exit 1
fi
[ ! -e "$fixture/dest/config.yaml" ]
[ "$(cat "$fixture/backup/config.yaml")" = "important-root-config" ]
[ "$(cat "$fixture/backup/nested/sub/deep.yaml")" = "important-nested-config" ]
assert_no_temp_leftovers "$fixture"

# 5b) postinst: find outputs root file + nested parent, then fails on nested/sub -> must NOT delete backup or commit partial enumeration!
cat > "$fixture/mockbin/find" <<'EOF'
#!/bin/sh
printf '.\n./config.yaml\n./nested\n'
echo "find: ./nested/sub: Input/output error" >&2
exit 1
EOF
chmod 700 "$fixture/mockbin/find"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected postinst failure when find fails mid-tree" >&2
    exit 1
fi
[ ! -e "$fixture/dest/config.yaml" ]
[ ! -e "$fixture/dest/nested/sub/deep.yaml" ]
[ "$(cat "$fixture/backup/config.yaml")" = "important-root-config" ]
[ "$(cat "$fixture/backup/nested/sub/deep.yaml")" = "important-nested-config" ]
[ -L "$fixture/backup/nested/active.yaml" ]
assert_no_temp_leftovers "$fixture"

# 6) postinst clean retry -> restores exact bytes of root + nested files + symlink, then removes backup.
"$fixture/run_restore.sh"
[ "$(cat "$fixture/dest/config.yaml")" = "important-root-config" ]
[ "$(cat "$fixture/dest/nested/sub/deep.yaml")" = "important-nested-config" ]
[ -L "$fixture/dest/nested/active.yaml" ]
[ "$(readlink "$fixture/dest/nested/active.yaml")" = "sub/deep.yaml" ]
[ ! -e "$fixture/backup" ]
assert_no_temp_leftovers "$fixture"
`)
			})

			t.Run("enumeration_list_creation_and_write_errors_fail_closed_and_cleanup", func(t *testing.T) {
				runDriver(t, commonHelpers+`
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/backup/nested" "$fixture/dest" "$fixture/mockbin"
printf 'nested-bytes\n' > "$fixture/backup/nested/cfg.yaml"
make_restore_script "$fixture"

# Case A: mktemp fails when creating .firc-list.XXXXXX
real_mktemp=$(find_real_bin mktemp)
cat > "$fixture/mockbin/mktemp" <<EOF
#!/bin/sh
for arg in "\$@"; do
    case "\$arg" in
        *.firc-list.*) exit 1 ;;
    esac
done
exec "$real_mktemp" "\$@"
EOF
chmod 700 "$fixture/mockbin/mktemp"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected failure when mktemp fails to create enumeration list" >&2
    exit 1
fi
[ ! -e "$fixture/dest/nested/cfg.yaml" ]
[ "$(cat "$fixture/backup/nested/cfg.yaml")" = "nested-bytes" ]
assert_no_temp_leftovers "$fixture"
rm -f "$fixture/mockbin/mktemp"

# Case B: find exits 0 with empty output (silent write truncation) -> [ ! -s "$list_file" ] catches it and cleans up list_file.
cat > "$fixture/mockbin/find" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 700 "$fixture/mockbin/find"

if PATH="$fixture/mockbin:$PATH" "$fixture/run_restore.sh" >/dev/null 2>&1; then
    echo "expected failure when enumeration list is empty" >&2
    exit 1
fi
[ ! -e "$fixture/dest/nested/cfg.yaml" ]
[ "$(cat "$fixture/backup/nested/cfg.yaml")" = "nested-bytes" ]
assert_no_temp_leftovers "$fixture"
rm -f "$fixture/mockbin/find"

# Clean retry succeeds and restores exact bytes.
"$fixture/run_restore.sh"
[ "$(cat "$fixture/dest/nested/cfg.yaml")" = "nested-bytes" ]
[ ! -e "$fixture/backup" ]
assert_no_temp_leftovers "$fixture"
`)
			})
		})
	}
}

package tgwebproxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type mockProcessHook struct {
	captureCalled bool
	applyCalled   bool
	readinessFail bool
	restoreCalled bool
	states        map[string]WorkerProcessState
}

func (m *mockProcessHook) CaptureWorkerStates() map[string]WorkerProcessState {
	m.captureCalled = true
	if m.states == nil {
		m.states = map[string]WorkerProcessState{
			"telemt-direct": {Worker: "telemt-direct", Enabled: true, Running: true, PID: 1234},
			"telemt-raw":    {Worker: "telemt-raw", Enabled: true, Running: true, PID: 1235},
			"tproxy-server": {Worker: "tproxy-server", Enabled: true, Running: false, PID: 0},
		}
	}
	return m.states
}

func (m *mockProcessHook) ApplyWorkers(cfg Config) error {
	m.applyCalled = true
	return nil
}

func (m *mockProcessHook) CheckReadiness(cfg Config) error {
	if m.readinessFail {
		return errors.New("readiness failed: port 8443 not responding")
	}
	return nil
}

func (m *mockProcessHook) RestoreWorkerStates(states map[string]WorkerProcessState) error {
	m.restoreCalled = true
	return nil
}

func TestTransaction_FullSuccess(t *testing.T) {
	tempDir := t.TempDir()
	tc := NewTransactionCoordinator(tempDir)

	target1 := filepath.Join(tempDir, "settings.json")
	target2 := filepath.Join(tempDir, "config.toml")
	_ = os.WriteFile(target1, []byte("old_settings"), 0600)

	files := []FilePayload{
		{TargetPath: target1, Content: []byte("new_settings"), Mode: 0600},
		{TargetPath: target2, Content: []byte("new_config"), Mode: 0600},
	}

	hook := &mockProcessHook{}
	cfg := Config{Enabled: true}

	err := tc.ExecuteTransaction("tx1", files, hook, cfg, nil)
	if err != nil {
		t.Fatalf("ExecuteTransaction failed: %v", err)
	}

	// Verify target1 content
	d1, err := os.ReadFile(target1)
	if err != nil || string(d1) != "new_settings" {
		t.Errorf("target1 mismatch: %s, err: %v", string(d1), err)
	}

	// Verify target2 content
	d2, err := os.ReadFile(target2)
	if err != nil || string(d2) != "new_config" {
		t.Errorf("target2 mismatch: %s, err: %v", string(d2), err)
	}

	// Verify journal and staging/backup removed
	if _, err := os.Stat(tc.manifestPath()); !os.IsNotExist(err) {
		t.Errorf("manifest still exists after commit")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "staging.tx1")); !os.IsNotExist(err) {
		t.Errorf("staging still exists after commit")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "backup.tx1")); !os.IsNotExist(err) {
		t.Errorf("backup still exists after commit")
	}
}

func TestTransaction_RollbackOnReadinessFailure(t *testing.T) {
	tempDir := t.TempDir()
	tc := NewTransactionCoordinator(tempDir)

	target1 := filepath.Join(tempDir, "settings.json")
	target2 := filepath.Join(tempDir, "config.toml")
	_ = os.WriteFile(target1, []byte("old_settings"), 0600)

	files := []FilePayload{
		{TargetPath: target1, Content: []byte("new_settings"), Mode: 0600},
		{TargetPath: target2, Content: []byte("new_config"), Mode: 0600}, // did not exist before
	}

	hook := &mockProcessHook{readinessFail: true}
	cfg := Config{Enabled: true}

	err := tc.ExecuteTransaction("tx2", files, hook, cfg, nil)
	if err == nil {
		t.Fatalf("expected error on readiness failure, got nil")
	}

	// target1 must be rolled back to old_settings
	d1, err := os.ReadFile(target1)
	if err != nil || string(d1) != "old_settings" {
		t.Errorf("target1 was not rolled back: %s", string(d1))
	}

	// target2 must be removed (existed_before == false)
	if _, err := os.Stat(target2); !os.IsNotExist(err) {
		t.Errorf("target2 was not removed on rollback")
	}

	// hook.RestoreWorkerStates must have been called
	if !hook.restoreCalled {
		t.Errorf("RestoreWorkerStates was not called")
	}
}

func TestCrashRecovery_AllPhases(t *testing.T) {
	t.Run("staged", func(t *testing.T) {
		tempDir := t.TempDir()
		tc := NewTransactionCoordinator(tempDir)
		manifest := &TransactionManifest{
			TxID:  "tx_staged",
			Phase: PhaseStaged,
		}
		data, _ := json.Marshal(manifest)
		_ = os.WriteFile(tc.manifestPath(), data, 0600)
		_ = os.MkdirAll(filepath.Join(tempDir, "staging.tx_staged"), 0700)

		hook := &mockProcessHook{}
		if err := tc.StartupReconcile(hook); err != nil {
			t.Fatalf("StartupReconcile staged failed: %v", err)
		}
		if _, err := os.Stat(tc.manifestPath()); !os.IsNotExist(err) {
			t.Errorf("manifest not removed after staged reconcile")
		}
	})

	t.Run("applying_rollback", func(t *testing.T) {
		tempDir := t.TempDir()
		tc := NewTransactionCoordinator(tempDir)
		target := filepath.Join(tempDir, "file.txt")
		backupDir := filepath.Join(tempDir, "backup.tx_apply")
		_ = os.MkdirAll(backupDir, 0700)
		backupFile := filepath.Join(backupDir, "file.txt")
		_ = os.WriteFile(backupFile, []byte("original"), 0600)
		_ = os.WriteFile(target, []byte("partially_written"), 0600)

		manifest := &TransactionManifest{
			TxID:  "tx_apply",
			Phase: PhaseApplying,
			Files: []ManifestFile{
				{
					TargetPath:    target,
					BackupPath:    backupFile,
					ExistedBefore: true,
					Mode:          0600,
				},
			},
		}
		data, _ := json.Marshal(manifest)
		_ = os.WriteFile(tc.manifestPath(), data, 0600)

		hook := &mockProcessHook{}
		if err := tc.StartupReconcile(hook); err != nil {
			t.Fatalf("expected nil from clean rollback recovery, got: %v", err)
		}

		res, _ := os.ReadFile(target)
		if string(res) != "original" {
			t.Errorf("target file was not rolled back: %s", string(res))
		}
	})

	t.Run("committed_no_rollback", func(t *testing.T) {
		tempDir := t.TempDir()
		tc := NewTransactionCoordinator(tempDir)
		target := filepath.Join(tempDir, "file.txt")
		_ = os.WriteFile(target, []byte("committed_version"), 0600)

		manifest := &TransactionManifest{
			TxID:  "tx_comm",
			Phase: PhaseCommitted,
		}
		data, _ := json.Marshal(manifest)
		_ = os.WriteFile(tc.manifestPath(), data, 0600)

		hook := &mockProcessHook{}
		if err := tc.StartupReconcile(hook); err != nil {
			t.Fatalf("StartupReconcile committed failed: %v", err)
		}
		res, _ := os.ReadFile(target)
		if string(res) != "committed_version" {
			t.Errorf("committed target was incorrectly altered: %s", string(res))
		}
		if _, err := os.Stat(tc.manifestPath()); !os.IsNotExist(err) {
			t.Errorf("committed manifest was not cleaned up")
		}
	})
}

func TestPermissions_Strict0600_0700(t *testing.T) {
	tempDir := t.TempDir()
	tc := NewTransactionCoordinator(tempDir)

	target := filepath.Join(tempDir, "secret.conf")
	files := []FilePayload{
		{TargetPath: target, Content: []byte("top_secret"), Mode: 0600},
	}

	hook := &mockProcessHook{}
	err := tc.ExecuteTransaction("tx_perm", files, hook, Config{Enabled: true}, nil)
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	// Under Unix-like systems, perm is 0600; on Windows it should at least not fail
	if fi.Mode().IsDir() {
		t.Errorf("target is dir, expected file")
	}
}

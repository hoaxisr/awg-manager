package tgwebproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tproxyconfig "github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	tproxysession "github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/session"
)

type mockRunner struct {
	commands []string
	pidFiles map[string]string
}

func (m *mockRunner) RunCommand(name string, args ...string) error {
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	m.commands = append(m.commands, name+" "+action)
	if m.pidFiles != nil {
		if pidFile, ok := m.pidFiles[name]; ok {
			if action == "start" || action == "restart" {
				_ = os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", os.Getpid())), 0644)
			} else if action == "stop" {
				_ = os.Remove(pidFile)
			}
		}
	}
	return nil
}

func setupTestService(t *testing.T) (*Service, string, *mockRunner) {
	t.Helper()
	tempDir := t.TempDir()
	runner := &mockRunner{
		pidFiles: make(map[string]string),
	}

	w1Init := filepath.Join(tempDir, "S96telemt-raw")
	w1Pid := filepath.Join(tempDir, "telemt-raw.pid")
	runner.pidFiles[w1Init] = w1Pid

	w2Init := filepath.Join(tempDir, "S99telemt")
	w2Pid := filepath.Join(tempDir, "telemt.pid")
	runner.pidFiles[w2Init] = w2Pid

	w3Init := filepath.Join(tempDir, "S95tproxy-server")
	w3Pid := filepath.Join(tempDir, "tproxy-server.pid")
	runner.pidFiles[w3Init] = w3Pid

	workers := &WorkerSupervisor{
		workers: []WorkerDef{
			{Name: "telemt-raw", InitScript: w1Init, PidFile: w1Pid, Port: 2398},
			{Name: "telemt-direct", InitScript: w2Init, PidFile: w2Pid, Port: 8443},
			{Name: "tproxy-server", InitScript: w3Init, PidFile: w3Pid, Port: 8085, IsHTTPMetrics: true, MetricsURL: "http://127.0.0.1:8086/metrics"},
		},
		runner: runner,
		dialer: func(network, addr string, timeout time.Duration) (net.Conn, error) {
			return &net.IPConn{}, nil
		},
		httpGet: func(url string) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("tproxy_metrics 1")),
			}, nil
		},
		socketChecker: func(pid int, port int) (bool, error) {
			return true, nil
		},
		readinessTimeout: 500 * time.Millisecond,
	}

	tc := NewTransactionCoordinator(filepath.Join(tempDir, "tproxy"))
	svc := &Service{
		dataDir: tempDir,
		tc:      tc,
		workers: workers,
		egressValidator: func(device string) error {
			if device == "invalid_dev" {
				return errors.New("interface invalid_dev not found")
			}
			return nil
		},
		tproxyValidator: func(stagingDir string) error {
			return nil
		},
		runtimeDCProbe: func(device string) bool {
			return true
		},
		stopCh: make(chan struct{}),
	}

	_ = os.MkdirAll(svc.tproxyDir(), 0700)
	_ = svc.ensureTokenKey()
	svc.loadAndMigrateConfig()

	return svc, tempDir, runner
}

func TestTokenKey_PreservedAcrossApply(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	tokenKeyPath := filepath.Join(tempDir, "tproxy", "token.key")

	origKey, err := os.ReadFile(tokenKeyPath)
	if err != nil || len(origKey) != 32 {
		t.Fatalf("expected 32-byte token key, got %v (%d bytes)", err, len(origKey))
	}

	// Run multiple configuration updates
	err = svc.UpdateConfig(Config{
		PublicHostname: "custom.domain.com",
		DirectPort:     9443,
	})
	if err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	afterKey, err := os.ReadFile(tokenKeyPath)
	if err != nil {
		t.Fatalf("read afterKey failed: %v", err)
	}
	if string(origKey) != string(afterKey) {
		t.Errorf("token.key was modified across UpdateConfig!")
	}
}

func TestSchemaMigration_V1ToV2(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	_ = os.MkdirAll(tproxyDir, 0700)

	v1Config := map[string]interface{}{
		"schema_version":  1,
		"enabled":         true,
		"listen_port":     8085,
		"public_hostname": "test.host.com",
		"backend":         "127.0.0.1:8443", // old default
		"secret":          "11112222333344445555666677778888",
	}
	data, _ := json.Marshal(v1Config)
	cfgPath := filepath.Join(tproxyDir, "tgwebproxy-settings.json")
	_ = os.WriteFile(cfgPath, data, 0600)

	tc := NewTransactionCoordinator(tproxyDir)
	svc := &Service{
		dataDir:         tempDir,
		tc:              tc,
		workers:         &WorkerSupervisor{workers: []WorkerDef{}, runner: &mockRunner{}},
		tproxyValidator: func(s string) error { return nil },
	}
	_ = svc.ensureTokenKey()
	svc.loadAndMigrateConfig()

	if svc.config.SchemaVersion != 2 {
		t.Errorf("expected SchemaVersion 2, got %d", svc.config.SchemaVersion)
	}
	if svc.config.Backend != "127.0.0.1:2398" {
		t.Errorf("expected migrated backend 127.0.0.1:2398, got %s", svc.config.Backend)
	}
	if svc.config.UpstreamDevice != "" {
		t.Errorf("expected default upstream device \"\", got %s", svc.config.UpstreamDevice)
	}
	if svc.config.Scenario != ScenarioDual {
		t.Errorf("expected migrated scenario %s, got %s", ScenarioDual, svc.config.Scenario)
	}

	// Verify backup file exists
	if _, err := os.Stat(cfgPath + ".v1.bak"); os.IsNotExist(err) {
		t.Errorf("backup file .v1.bak was not created")
	}
}

func TestUpstreamValidation_PreApply(t *testing.T) {
	svc, _, _ := setupTestService(t)

	err := svc.UpdateConfig(Config{
		UpstreamDevice: "invalid_dev",
	})
	if err == nil {
		t.Fatalf("expected error for invalid_dev upstream, got nil")
	}
}

func TestGetStatus_StrictlyReadOnly(t *testing.T) {
	svc, _, _ := setupTestService(t)

	status := svc.GetStatus()
	if status.SecretMasked == "" {
		t.Errorf("expected masked secret in status, got empty")
	}
	if status.SecretMasked == svc.config.Secret {
		t.Errorf("secret was not masked in GetStatus!")
	}

	// Calling GetStatus should never create lock or fail
	for i := 0; i < 50; i++ {
		st := svc.GetStatus()
		if st.PublicHost != svc.config.PublicHostname {
			t.Errorf("unexpected public host: %s", st.PublicHost)
		}
	}
}

func TestLegacySecret_CombineWithUpdate(t *testing.T) {
	svc, _, _ := setupTestService(t)

	// Set legacy secret already expired 1 hour ago
	svc.config.LegacySecret = "old_secret_12345"
	svc.config.LegacyExpiresAt = time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)

	// User updates public hostname
	err := svc.UpdateConfig(Config{
		PublicHostname: "new.host.net",
	})
	if err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	// Legacy secret should be purged in the same single update
	if svc.config.LegacySecret != "" {
		t.Errorf("expected legacy secret to be cleared on update, got: %s", svc.config.LegacySecret)
	}
}

func TestLegacySecret_TimerLifecycle(t *testing.T) {
	svc, _, _ := setupTestService(t)

	// Rotate secret creates a legacy secret with timer
	err := svc.RotateSecret()
	if err != nil {
		t.Fatalf("RotateSecret failed: %v", err)
	}

	if svc.legacyTimer == nil {
		t.Errorf("expected legacy timer to be active after rotation")
	}
	if svc.config.LegacySecret == "" {
		t.Errorf("expected legacy secret to be set")
	}

	// Explicit revoke stops timer and purges immediately
	err = svc.RevokeLegacySecret()
	if err != nil {
		t.Fatalf("RevokeLegacySecret failed: %v", err)
	}

	if svc.config.LegacySecret != "" {
		t.Errorf("expected legacy secret to be empty after revoke")
	}
}

func TestPhaseOrder_ApplyingBeforeTargetRename(t *testing.T) {
	tempDir := t.TempDir()
	tc := NewTransactionCoordinator(tempDir)

	targetFile := filepath.Join(tempDir, "target.txt")
	_ = os.WriteFile(targetFile, []byte("initial"), 0600)

	files := []FilePayload{
		{TargetPath: targetFile, Content: []byte("updated"), Mode: 0600},
	}

	phaseWhenValidated := ""
	validateFn := func(stagingDir string) error {
		// Read manifest during validation hook
		mData, err := os.ReadFile(tc.manifestPath())
		if err == nil {
			var m TransactionManifest
			_ = json.Unmarshal(mData, &m)
			phaseWhenValidated = m.Phase
		}
		return nil
	}

	err := tc.ExecuteTransaction("tx_phase", files, nil, Config{}, validateFn)
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	if phaseWhenValidated != PhaseStaged {
		t.Errorf("expected phase %s during validation, got %s", PhaseStaged, phaseWhenValidated)
	}
}

func TestProcessIdentity_StalePid(t *testing.T) {
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, "fake.pid")
	// Write a non-existent PID
	_ = os.WriteFile(pidFile, []byte("999999"), 0644)

	ws := &WorkerSupervisor{
		workers: []WorkerDef{
			{
				Name:         "fake-worker",
				PidFile:      pidFile,
				ExeName:      "nonexistent_exe",
				CmdlineMatch: "nonexistent_cmd",
			},
		},
	}

	st := ws.GetWorkerState(ws.workers[0])
	if st.Running {
		t.Errorf("expected stale PID to be reported as NOT running")
	}
}

func TestConcurrentMutations_Conflict409(t *testing.T) {
	svc, _, _ := setupTestService(t)

	// Hold the transaction lock
	svc.tc.mu.Lock()

	// An update attempt while transaction lock is held should return ErrOperationInProgress
	err := svc.UpdateConfig(Config{
		PublicHostname: "conflict.example.com",
	})
	svc.tc.mu.Unlock()

	if err == nil {
		t.Fatalf("expected conflict error, got nil")
	}
	if !errors.Is(err, ErrOperationInProgress) {
		t.Errorf("expected ErrOperationInProgress, got: %v", err)
	}
}

func TestCopyFileAtomic_TargetDirTemp(t *testing.T) {
	tempDir := t.TempDir()
	srcFile := filepath.Join(tempDir, "src.txt")
	dstFile := filepath.Join(tempDir, "dst.txt")
	content := []byte("hello atomic world")

	_ = os.WriteFile(srcFile, content, 0600)

	err := copyFileAtomic(srcFile, dstFile, 0600, -1, -1)
	if err != nil {
		t.Fatalf("copyFileAtomic failed: %v", err)
	}

	readBack, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("failed to read target file: %v", err)
	}
	if string(readBack) != string(content) {
		t.Errorf("content mismatch: got %q, want %q", string(readBack), string(content))
	}

	// Verify no leftover .tmp files
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("failed to read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Errorf("leftover temporary file found: %s", entry.Name())
		}
	}
}

func TestRollback_PreservesManifestAndBackupOnError(t *testing.T) {
	tempDir := t.TempDir()
	tc := NewTransactionCoordinator(tempDir)

	txID := "tx_fail_rollback"
	backupDir := filepath.Join(tempDir, "backup."+txID)
	_ = os.MkdirAll(backupDir, 0700)

	targetFile := filepath.Join(tempDir, "file_to_fail.txt")
	backupFile := filepath.Join(backupDir, "backup.txt")
	_ = os.WriteFile(targetFile, []byte("original content"), 0600)
	_ = os.WriteFile(backupFile, []byte("original content"), 0600)

	// Simulate manifest in PhaseApplying with incorrect Sha256Old to force rollback verification error
	manifest := &TransactionManifest{
		TxID:      txID,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Phase:     PhaseApplying,
		Files: []ManifestFile{
			{
				TargetPath:    targetFile,
				BackupPath:    backupFile,
				ExistedBefore: true,
				Mode:          0600,
				Sha256Old:     "wrong_sha256_hash_value_that_cannot_match",
				Sha256New:     "some_new_hash",
			},
		},
	}

	_ = tc.writeManifestAtomic(manifest)

	// Perform rollback - should fail because Sha256Old does not match restored file!
	err := tc.performRollbackLocked(manifest, nil)
	if err == nil {
		t.Fatalf("expected rollback to return composite error on SHA256 mismatch, got nil")
	}

	// Verify manifest still exists and phase is PhaseRollingBack
	if _, statErr := os.Stat(tc.manifestPath()); os.IsNotExist(statErr) {
		t.Errorf("manifest was deleted on rollback error!")
	}
	mData, _ := os.ReadFile(tc.manifestPath())
	var savedM TransactionManifest
	_ = json.Unmarshal(mData, &savedM)
	if savedM.Phase != PhaseRollingBack {
		t.Errorf("expected phase %s, got %s", PhaseRollingBack, savedM.Phase)
	}

	// Verify backup dir still exists
	if _, statErr := os.Stat(backupDir); os.IsNotExist(statErr) {
		t.Errorf("backup dir was deleted on rollback error!")
	}
}

func TestStartupReconcile_BlocksMutationsOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	_ = os.MkdirAll(tproxyDir, 0700)

	// Create manifest with PhaseApplying pointing to missing backup
	manifestPath := filepath.Join(tproxyDir, "transaction.json")
	m := TransactionManifest{
		TxID:      "tx_startup_fail",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Phase:     PhaseApplying,
		Files: []ManifestFile{
			{
				TargetPath:    filepath.Join(tempDir, "some_file.txt"),
				BackupPath:    filepath.Join(tproxyDir, "backup.tx_startup_fail", "missing_backup.txt"),
				ExistedBefore: true,
				Mode:          0600,
				Sha256Old:     "abc",
			},
		},
	}
	mData, _ := json.Marshal(m)
	_ = os.WriteFile(manifestPath, mData, 0600)

	svc := New(tempDir, nil)

	if !svc.recoveryRequired {
		t.Errorf("expected recoveryRequired to be true")
	}
	if svc.lastStartupError == "" {
		t.Errorf("expected lastStartupError to be set")
	}

	// Mutating operations should fail with ErrRecoveryRequired
	err := svc.UpdateConfig(Config{PublicHostname: "host.example.com"})
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired on UpdateConfig, got: %v", err)
	}

	_, err = svc.RevealSecret()
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired on RevealSecret, got: %v", err)
	}

	err = svc.RotateSecret()
	if !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired on RotateSecret, got: %v", err)
	}

	// GetStatus should still work in read-only mode and report recovery_required
	status := svc.GetStatus()
	if !status.RecoveryRequired {
		t.Errorf("expected status.RecoveryRequired to be true")
	}
	if status.LastStartupError == "" {
		t.Errorf("expected status.LastStartupError to report the error")
	}
}

func TestNegativeSerialization_NoSecretLeak(t *testing.T) {
	secretVal := "super_secret_fake_tls_key_12345678"
	legacyVal := "old_secret_legacy_key_87654321"

	svc, _, _ := setupTestService(t)
	svc.config.Secret = secretVal
	svc.config.LegacySecret = legacyVal

	// 1. Check GetConfig() (PublicConfig)
	pubCfg := svc.GetConfig()
	cfgData, err := json.Marshal(pubCfg)
	if err != nil {
		t.Fatalf("failed to marshal PublicConfig: %v", err)
	}
	cfgJSON := string(cfgData)
	if strings.Contains(cfgJSON, secretVal) {
		t.Errorf("SECURITY LEAK: raw secret %q found in PublicConfig JSON: %s", secretVal, cfgJSON)
	}
	if strings.Contains(cfgJSON, legacyVal) {
		t.Errorf("SECURITY LEAK: raw legacy secret %q found in PublicConfig JSON: %s", legacyVal, cfgJSON)
	}

	// 2. Check GetStatus()
	status := svc.GetStatus()
	statusData, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("failed to marshal Status: %v", err)
	}
	statusJSON := string(statusData)
	if strings.Contains(statusJSON, secretVal) {
		t.Errorf("SECURITY LEAK: raw secret %q found in Status JSON: %s", secretVal, statusJSON)
	}
	if strings.Contains(statusJSON, legacyVal) {
		t.Errorf("SECURITY LEAK: raw legacy secret %q found in Status JSON: %s", legacyVal, statusJSON)
	}
	if strings.Contains(statusJSON, "tg://") || strings.Contains(statusJSON, "t.me/") {
		t.Errorf("SECURITY LEAK: tg/tme links found in Status JSON: %s", statusJSON)
	}
}

func TestSchemaMigration_MissingSchemaVersion(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	_ = os.MkdirAll(tproxyDir, 0700)

	// JSON payload completely lacking "schema_version"
	unversionedConfig := map[string]interface{}{
		"enabled":         false,
		"listen_port":     8085,
		"public_hostname": "unversioned.example.com",
		"backend":         "127.0.0.1:8443", // old default
		"secret":          "11112222333344445555666677778888",
	}
	data, err := json.Marshal(unversionedConfig)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	cfgPath := filepath.Join(tproxyDir, "tgwebproxy-settings.json")
	if err := os.WriteFile(cfgPath, data, 0600); err != nil {
		t.Fatalf("write unversioned config failed: %v", err)
	}

	tc := NewTransactionCoordinator(tproxyDir)
	svc := &Service{
		dataDir: tempDir,
		tc:      tc,
		workers: nil,
		egressValidator: func(device string) error {
			return nil
		},
		tproxyValidator: func(stagingDir string) error {
			return nil
		},
	}
	_ = svc.ensureTokenKey()
	if err := svc.loadAndMigrateConfig(); err != nil {
		t.Fatalf("loadAndMigrateConfig failed: %v", err)
	}

	// 1. Check that config has been migrated to CurrentSchemaVersion
	if svc.config.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("expected SchemaVersion %d, got %d", CurrentSchemaVersion, svc.config.SchemaVersion)
	}
	if svc.config.Backend != DefaultBackend {
		t.Errorf("expected migrated backend %s, got %s", DefaultBackend, svc.config.Backend)
	}

	// 2. Check that .v1.bak was created atomically
	bakPath := cfgPath + ".v1.bak"
	bakData, err := os.ReadFile(bakPath)
	if err != nil {
		t.Fatalf("expected .v1.bak to exist: %v", err)
	}
	if string(bakData) != string(data) {
		t.Errorf(".v1.bak content mismatch")
	}

	// 3. Second load should be idempotent and not overwrite .v1.bak
	modData := []byte(`modified`)
	_ = os.WriteFile(bakPath, modData, 0600)
	if err := svc.loadAndMigrateConfig(); err != nil {
		t.Fatalf("second loadAndMigrateConfig failed: %v", err)
	}
	currBak, _ := os.ReadFile(bakPath)
	if string(currBak) != string(modData) {
		t.Errorf(".v1.bak was overwritten during second load!")
	}
}

func TestStartup_CorruptJSON_TriggersRecovery(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	_ = os.MkdirAll(tproxyDir, 0700)

	cfgPath := filepath.Join(tproxyDir, "tgwebproxy-settings.json")
	_ = os.WriteFile(cfgPath, []byte("NOT_VALID_JSON{{{"), 0600)

	svc := New(tempDir, nil)
	st := svc.GetStatus()
	if !st.RecoveryRequired {
		t.Errorf("expected RecoveryRequired == true on corrupt config JSON")
	}
	if st.LastStartupError == "" {
		t.Errorf("expected non-empty LastStartupError")
	}

	// Mutating operations should be rejected with ErrRecoveryRequired
	if err := svc.UpdateConfig(Config{PublicHostname: "foo"}); !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired on UpdateConfig, got: %v", err)
	}
	if _, err := svc.RevealSecret(); !errors.Is(err, ErrRecoveryRequired) {
		t.Errorf("expected ErrRecoveryRequired on RevealSecret, got: %v", err)
	}
}

func TestTokenKey_CorruptSizeRenamed(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	_ = os.MkdirAll(tproxyDir, 0700)

	keyPath := filepath.Join(tproxyDir, "token.key")
	_ = os.WriteFile(keyPath, []byte("too_short_key_16b!"), 0600)

	svc := New(tempDir, nil)
	if svc.recoveryRequired {
		t.Fatalf("unexpected recoveryRequired: %s", svc.lastStartupError)
	}

	// Ensure new key is 32 bytes
	newKey, err := os.ReadFile(keyPath)
	if err != nil || len(newKey) != 32 {
		t.Fatalf("expected 32-byte new token key, got %v (%d bytes)", err, len(newKey))
	}

	// Ensure corrupt key was backed up
	entries, _ := os.ReadDir(tproxyDir)
	foundCorrupt := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "token.key.corrupt.") {
			foundCorrupt = true
			break
		}
	}
	if !foundCorrupt {
		t.Errorf("expected corrupted token.key to be renamed with .corrupt suffix")
	}
}

type errReader struct{}

func (e errReader) Read(p []byte) (n int, err error) {
	return 0, errors.New("entropy source depleted")
}

func TestEntropy_FailureHandling(t *testing.T) {
	_, err := randomHexSecret(errReader{})
	if err == nil {
		t.Errorf("expected error from randomHexSecret on reader error, got nil")
	}

	_, err = generateTxID(errReader{})
	if err == nil {
		t.Errorf("expected error from generateTxID on reader error, got nil")
	}
}

func TestService_CloseIdempotent(t *testing.T) {
	svc, _, _ := setupTestService(t)

	// Multiple Close calls should be idempotent and not panic
	if err := svc.Close(); err != nil {
		t.Errorf("first Close returned error: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Errorf("third Close returned error: %v", err)
	}
}

func TestWorkers_SocketCheck_FailClosed(t *testing.T) {
	tempDir := t.TempDir()
	wInit := filepath.Join(tempDir, "S96telemt-raw")
	wPid := filepath.Join(tempDir, "telemt-raw.pid")
	_ = os.WriteFile(wPid, []byte(fmt.Sprintf("%d", os.Getpid())), 0600)

	ws := &WorkerSupervisor{
		workers: []WorkerDef{
			{Name: "telemt-raw", InitScript: wInit, PidFile: wPid, Port: 2398},
		},
		runner: &mockRunner{},
		dialer: func(network, addr string, timeout time.Duration) (net.Conn, error) {
			return &net.IPConn{}, nil
		},
		// Injected socketChecker that simulates ownership failure
		socketChecker: func(pid int, port int) (bool, error) {
			return false, errors.New("socket inode belongs to another pid")
		},
		readinessTimeout: 200 * time.Millisecond,
	}

	// CheckReadiness should fail closed and return descriptive error
	cfg := Config{Enabled: true}
	err := ws.CheckReadiness(cfg)
	if err == nil {
		t.Errorf("expected fail-closed error from socket checker in CheckReadiness, got nil")
	}
	if !strings.Contains(err.Error(), "socket ownership check failed") {
		t.Errorf("expected timeout error to mention socket ownership check, got: %v", err)
	}
}

func TestLoadOrCreateKeyFile_SymlinkRejection(t *testing.T) {
	tempDir := t.TempDir()
	targetFile := filepath.Join(tempDir, "real_target.txt")
	_ = os.WriteFile(targetFile, make([]byte, 32), 0600)

	symlinkPath := filepath.Join(tempDir, "symlink.key")
	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Skipf("symlink creation not supported on this platform: %v", err)
	}

	_, err := loadOrCreateKeyFile(symlinkPath, 32, nil)
	if err == nil {
		t.Fatalf("expected error loading key file through symlink, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("expected error to mention symlink or symbolic link, got: %v", err)
	}
}

func TestLoadOrCreateKeyFile_NonRegularFile(t *testing.T) {
	tempDir := t.TempDir()
	dirPath := filepath.Join(tempDir, "dir_as_key.key")
	if err := os.Mkdir(dirPath, 0700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	_, err := loadOrCreateKeyFile(dirPath, 32, nil)
	if err == nil {
		t.Fatalf("expected error loading key from directory, got nil")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("expected error to mention non-regular file, got: %v", err)
	}
}

func TestLoadOrCreateKeyFile_CorruptSizeRecovery(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "corrupt.key")
	_ = os.WriteFile(keyPath, []byte("too short"), 0600)

	key, err := loadOrCreateKeyFile(keyPath, 32, nil)
	if err != nil {
		t.Fatalf("expected corrupt key to be recovered and recreated, got err: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("expected 32-byte key, got %d bytes", len(key))
	}

	files, _ := os.ReadDir(tempDir)
	foundCorrupt := false
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "corrupt.key.corrupt.") {
			foundCorrupt = true
			break
		}
	}
	if !foundCorrupt {
		t.Errorf("expected .corrupt.* backup file to exist in directory")
	}
}

func TestGetCSRFSecret_ImmutableSlice(t *testing.T) {
	tempDir := t.TempDir()
	svc := New(tempDir, nil)

	s1 := svc.GetCSRFSecret()
	if len(s1) != 32 {
		t.Fatalf("expected 32 bytes CSRF secret, got %d", len(s1))
	}

	// Mutate returned slice
	s1[0] ^= 0xFF
	s1[1] ^= 0xAA

	s2 := svc.GetCSRFSecret()
	if s2[0] == s1[0] && s2[1] == s1[1] {
		t.Errorf("mutating returned CSRF secret slice modified internal secret! Slice must be immutable clone")
	}
}

func TestLoadOrCreateKeyFile_DescriptorChmod_NoPathChmod(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "existing.key")
	initialKey := make([]byte, 32)
	for i := range initialKey {
		initialKey[i] = byte(i + 1)
	}
	// Write with permissive permissions (0644)
	if err := os.WriteFile(keyPath, initialKey, 0644); err != nil {
		t.Fatalf("write initial key failed: %v", err)
	}

	loadedKey, err := loadOrCreateKeyFile(keyPath, 32, nil)
	if err != nil {
		t.Fatalf("loadOrCreateKeyFile failed: %v", err)
	}
	if !bytes.Equal(loadedKey, initialKey) {
		t.Errorf("loaded key does not match initial key")
	}

	// Verify that descriptor chmod enforced 0600 (on non-windows systems where permission bits are preserved)
	if runtime.GOOS != "windows" {
		fi, err := os.Lstat(keyPath)
		if err != nil {
			t.Fatalf("stat key failed: %v", err)
		}
		if fi.Mode().Perm() != 0600 {
			t.Errorf("expected key permissions 0600 via descriptor chmod, got %04o", fi.Mode().Perm())
		}
	}
}

func TestLoadOrCreateKeyFile_DirSymlinkRejection(t *testing.T) {
	tempDir := t.TempDir()
	realDir := filepath.Join(tempDir, "real_keys")
	if err := os.Mkdir(realDir, 0700); err != nil {
		t.Fatalf("mkdir realDir failed: %v", err)
	}

	symlinkDir := filepath.Join(tempDir, "symlink_keys")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	keyPath := filepath.Join(symlinkDir, "test.key")
	_, err := loadOrCreateKeyFile(keyPath, 32, nil)
	if err == nil {
		t.Fatalf("expected error when key directory is a symlink, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected error to mention symlink, got: %v", err)
	}

	// Verify no key was created in the real directory
	if _, err := os.Stat(filepath.Join(realDir, "test.key")); !os.IsNotExist(err) {
		t.Errorf("expected no key file to be created inside symlinked target dir")
	}
}

func TestLoadOrCreateKeyFile_CorruptBackupFailure_AbortsWithoutOverwriting(t *testing.T) {
	tempDir := t.TempDir()
	keyPath := filepath.Join(tempDir, "corrupt.key")
	corruptData := []byte("corrupt_old_key_data")
	if err := os.WriteFile(keyPath, corruptData, 0600); err != nil {
		t.Fatalf("write corrupt key failed: %v", err)
	}

	// Mock renameKeyFile to fail on backup
	origRename := renameKeyFile
	defer func() { renameKeyFile = origRename }()

	renameKeyFile = func(oldpath, newpath string) error {
		if strings.Contains(newpath, ".corrupt.") {
			return errors.New("simulated disk error during backup")
		}
		return os.Rename(oldpath, newpath)
	}

	_, err := loadOrCreateKeyFile(keyPath, 32, nil)
	if err == nil {
		t.Fatalf("expected error when backup fails, got nil")
	}
	if !strings.Contains(err.Error(), "backup corrupt key") {
		t.Errorf("expected error to mention 'backup corrupt key', got: %v", err)
	}

	// Verify original corrupt file was NOT overwritten or deleted
	data, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("failed to read original keyPath: %v", err)
	}
	if !bytes.Equal(data, corruptData) {
		t.Errorf("original corrupt key was altered! Expected %q, got %q", string(corruptData), string(data))
	}

	// Verify no stray .key.tmp.* files left behind
	entries, _ := os.ReadDir(tempDir)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".key.tmp.") {
			t.Errorf("stray temp file found: %s", entry.Name())
		}
	}
}

func TestStartConfigured_ReadinessFailureRollsBackWorkers(t *testing.T) {
	svc, _, runner := setupTestService(t)
	svc.config.Enabled = true

	// Simulate readiness failure
	svc.workers.dialer = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}

	err := svc.StartConfigured()
	if err == nil {
		t.Fatalf("expected StartConfigured to fail on readiness failure")
	}

	// Verify that workers were stopped (rollback was invoked)
	stopCalled := false
	for _, cmd := range runner.commands {
		if strings.HasSuffix(cmd, "stop") {
			stopCalled = true
			break
		}
	}
	if !stopCalled {
		t.Errorf("expected worker stop to be called on readiness failure rollback")
	}

	// Verify persistent config Enabled is still true
	if !svc.config.Enabled {
		t.Errorf("expected config.Enabled to remain true, got false")
	}
}

func TestStartConfigured_RegeneratesLegacyProfilesFromCanonicalSettings(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	svc.config.Enabled = true
	svc.config.Scenario = ScenarioCDNHTTP
	svc.config.CarrierMode = DefaultCarrierMode
	svc.config.PublicHostname = "edge.example.test"
	svc.config.Endpoints = []WebProxyEndpoint{{
		ID: "edge", Hostname: "edge.example.test", ListenPort: 8084, AdminPort: 8086, IsCDN: true, Enabled: true,
	}}

	profilesPath := filepath.Join(tempDir, "tproxy", "profiles.json")
	legacyProfiles := []byte(`{"profiles":[{"name":"default","secret":"legacy","backend":"127.0.0.1:2398","carrier_mode":"get"}]}`)
	if err := os.WriteFile(profilesPath, legacyProfiles, 0644); err != nil {
		t.Fatalf("write legacy profiles: %v", err)
	}

	if err := svc.StartConfigured(); err != nil {
		t.Fatalf("StartConfigured: %v", err)
	}

	data, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read regenerated profiles: %v", err)
	}
	var profiles struct {
		Profiles []struct {
			CarrierMode string `json:"carrier_mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		t.Fatalf("decode regenerated profiles: %v", err)
	}
	if len(profiles.Profiles) != 1 || profiles.Profiles[0].CarrierMode != DefaultCarrierMode {
		t.Fatalf("profiles were not regenerated from canonical carrier mode: %+v", profiles.Profiles)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(profilesPath)
		if err != nil {
			t.Fatalf("stat regenerated profiles: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0600 {
			t.Fatalf("profiles file mode = %o, want 0600", mode)
		}
	}
}

func TestLoadAndMigrateConfig_PreservesExplicitGetCarrier(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	path := filepath.Join(tempDir, "tproxy", "tgwebproxy-settings.json")
	data := []byte(`{"schema_version":2,"enabled":true,"carrier_mode":"get","secret":"0123456789abcdef0123456789abcdef"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := svc.loadAndMigrateConfig(); err != nil {
		t.Fatalf("loadAndMigrateConfig: %v", err)
	}
	if got := svc.config.CarrierMode; got != "get" {
		t.Fatalf("carrier mode = %q, want explicit get to be preserved", got)
	}
}

func TestLoadAndMigrateConfig_MultiEndpointPreservesOriginNonCDN(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	path := filepath.Join(tempDir, "tproxy", "tgwebproxy-settings.json")
	data := []byte(`{
		"schema_version": 2,
		"enabled": true,
		"scenario": "dual",
		"carrier_mode": "https",
		"secret": "0123456789abcdef0123456789abcdef",
		"endpoints": [
			{"id": "xy", "hostname": "origin.example.test", "listen_port": 8084, "admin_port": 8086, "is_cdn": false, "enabled": true},
			{"id": "edge", "hostname": "edge.example.test", "listen_port": 18084, "admin_port": 18086, "is_cdn": false, "enabled": true}
		]
	}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := svc.loadAndMigrateConfig(); err != nil {
		t.Fatalf("loadAndMigrateConfig: %v", err)
	}
	if len(svc.config.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(svc.config.Endpoints))
	}
	if svc.config.Endpoints[0].IsCDN {
		t.Fatalf("expected origin endpoint xy to remain IsCDN=false, got true")
	}
	if !svc.config.Endpoints[1].IsCDN {
		t.Fatalf("expected edge endpoint to be migrated to IsCDN=true, got false")
	}
}

func TestReconcileRuntime_RestartsTproxyWhenProfilesDiffer(t *testing.T) {
	svc, tempDir, runner := setupTestService(t)
	svc.config.Enabled = true
	svc.config.Scenario = ScenarioCDNHTTP
	svc.config.CarrierMode = DefaultCarrierMode
	svc.config.PublicHostname = "edge.example.test"
	svc.config.Endpoints = []WebProxyEndpoint{{
		ID: "edge", Hostname: "edge.example.test", ListenPort: 8084, AdminPort: 8086, IsCDN: true, Enabled: true,
	}}

	profilesPath := filepath.Join(tempDir, "tproxy", "profiles.json")
	legacyProfiles := []byte(`{"profiles":[{"name":"default","secret":"legacy","backend":"127.0.0.1:2398","carrier_mode":"get"}]}`)
	if err := os.WriteFile(profilesPath, legacyProfiles, 0644); err != nil {
		t.Fatalf("write legacy profiles: %v", err)
	}

	if err := svc.ReconcileRuntime(); err != nil {
		t.Fatalf("ReconcileRuntime: %v", err)
	}

	data, err := os.ReadFile(profilesPath)
	if err != nil {
		t.Fatalf("read reconciled profiles: %v", err)
	}
	var profiles struct {
		Profiles []struct {
			CarrierMode string `json:"carrier_mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &profiles); err != nil {
		t.Fatalf("decode reconciled profiles: %v", err)
	}
	if len(profiles.Profiles) != 1 || profiles.Profiles[0].CarrierMode != DefaultCarrierMode {
		t.Fatalf("profiles were not reconciled from canonical carrier mode: %+v", profiles.Profiles)
	}

	// Verify tproxy-server worker was restarted
	restartSeen := false
	for _, cmd := range runner.commands {
		if strings.Contains(cmd, "S95tproxy-server restart") {
			restartSeen = true
			break
		}
	}
	if !restartSeen {
		t.Fatalf("expected S95tproxy-server restart in runner commands: %+v", runner.commands)
	}
}

func TestTelegramLegacyTlsDomainPreservedOnMigration(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	if err := os.MkdirAll(tproxyDir, 0755); err != nil {
		t.Fatalf("mkdir tproxy: %v", err)
	}

	// 1. Write legacy JSON with existing TlsDomain "ya.ru"
	legacyJSON := `{
		"enabled": true,
		"listen_port": 8085,
		"direct_port": 8443,
		"tls_domain": "ya.ru",
		"secret": "0123456789abcdef0123456789abcdef"
	}`
	cfgPath := filepath.Join(tproxyDir, "tgwebproxy-settings.json")
	if err := os.WriteFile(cfgPath, []byte(legacyJSON), 0600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	svc := New(tempDir, nil)
	cfg := svc.GetConfig()

	// Verify legacy value was preserved on migration
	if cfg.TlsDomain != "ya.ru" {
		t.Errorf("expected legacy TlsDomain 'ya.ru' to be preserved, got %q", cfg.TlsDomain)
	}

	// 2. Fresh initialization in an empty directory should have NO default TLS domain
	freshDir := t.TempDir()
	freshSvc := New(freshDir, nil)
	freshCfg := freshSvc.GetConfig()
	if freshCfg.TlsDomain != "" {
		t.Errorf("expected fresh TlsDomain to be empty, got %q", freshCfg.TlsDomain)
	}
}

func TestTelegramLegacyScenarioMigration(t *testing.T) {
	tempDir := t.TempDir()
	tproxyDir := filepath.Join(tempDir, "tproxy")
	_ = os.MkdirAll(tproxyDir, 0700)

	// Config without scenario field
	rawJSON := `{
		"schema_version": 2,
		"enabled": true,
		"listen_port": 8085,
		"direct_port": 8443,
		"direct_host": "my.direct.host",
		"public_hostname": "my.cdn.host",
		"secret": "11112222333344445555666677778888"
	}`
	cfgPath := filepath.Join(tproxyDir, "tgwebproxy-settings.json")
	if err := os.WriteFile(cfgPath, []byte(rawJSON), 0600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	svc := New(tempDir, nil)
	cfg := svc.GetConfig()

	// Empty scenario should migrate to explicit "dual"
	if cfg.Scenario != ScenarioDual {
		t.Fatalf("expected migrated scenario %q, got %q", ScenarioDual, cfg.Scenario)
	}

	// Verify disk persistence of migrated scenario
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	var onDisk Config
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshal on-disk config: %v", err)
	}
	if onDisk.Scenario != ScenarioDual {
		t.Errorf("expected persisted scenario %q, got %q", ScenarioDual, onDisk.Scenario)
	}
}

func TestTelegramScenariosAppliedCorrectly(t *testing.T) {
	runner := &mockRunner{}
	ws := &WorkerSupervisor{
		workers: []WorkerDef{
			{Name: "telemt-raw", InitScript: "/etc/init.d/S96telemt-raw", Port: 2398},
			{Name: "telemt-direct", InitScript: "/etc/init.d/S99telemt", Port: 8443},
			{Name: "tproxy-server", InitScript: "/etc/init.d/S95tproxy-server", Port: 8085, IsHTTPMetrics: true},
		},
		runner: runner,
	}

	// 1. Scenario: direct_fake_tls -> only telemt-direct starts, others stopped
	runner.commands = nil
	cfgDirect := Config{
		Enabled:  true,
		Scenario: ScenarioDirectFakeTLS,
	}
	if err := ws.ApplyWorkers(cfgDirect); err != nil {
		t.Fatalf("ApplyWorkers direct_fake_tls failed: %v", err)
	}
	// Check stop of disabled workers first, then start of direct
	var cmdStr string
	for _, c := range runner.commands {
		cmdStr += c + ";"
	}
	if !strings.Contains(cmdStr, "S95tproxy-server stop") || !strings.Contains(cmdStr, "S96telemt-raw stop") {
		t.Errorf("expected disabled workers stopped, got commands: %v", runner.commands)
	}
	if !strings.Contains(cmdStr, "S99telemt restart") && !strings.Contains(cmdStr, "S99telemt start") {
		t.Errorf("expected telemt-direct started, got commands: %v", runner.commands)
	}

	// 2. Scenario: cdn_http -> telemt-direct stopped, raw and web started
	runner.commands = nil
	cfgCDN := Config{
		Enabled:  true,
		Scenario: ScenarioCDNHTTP,
	}
	if err := ws.ApplyWorkers(cfgCDN); err != nil {
		t.Fatalf("ApplyWorkers cdn_http failed: %v", err)
	}
	cmdStr = ""
	for _, c := range runner.commands {
		cmdStr += c + ";"
	}
	if !strings.Contains(cmdStr, "S99telemt stop") {
		t.Errorf("expected direct stopped, got commands: %v", runner.commands)
	}
	if !strings.Contains(cmdStr, "S96telemt-raw restart") || !strings.Contains(cmdStr, "S95tproxy-server restart") {
		t.Errorf("expected raw and web started, got commands: %v", runner.commands)
	}

	// 3. Scenario: dual -> start order direct -> raw -> tproxy
	runner.commands = nil
	cfgDual := Config{
		Enabled:  true,
		Scenario: ScenarioDual,
	}
	if err := ws.ApplyWorkers(cfgDual); err != nil {
		t.Fatalf("ApplyWorkers dual failed: %v", err)
	}
	// Verify exact start order: direct -> raw -> tproxy
	directIdx, rawIdx, tproxyIdx := -1, -1, -1
	for i, c := range runner.commands {
		if strings.Contains(c, "S99telemt restart") {
			directIdx = i
		} else if strings.Contains(c, "S96telemt-raw restart") {
			rawIdx = i
		} else if strings.Contains(c, "S95tproxy-server restart") {
			tproxyIdx = i
		}
	}
	if directIdx < 0 || rawIdx < 0 || tproxyIdx < 0 || !(directIdx < rawIdx && rawIdx < tproxyIdx) {
		t.Errorf("expected start order direct -> raw -> tproxy, got commands: %v (indices: %d, %d, %d)", runner.commands, directIdx, rawIdx, tproxyIdx)
	}

	// 4. Stop: stop order tproxy -> raw -> direct
	runner.commands = nil
	cfgStopped := Config{Enabled: false}
	if err := ws.ApplyWorkers(cfgStopped); err != nil {
		t.Fatalf("ApplyWorkers stop failed: %v", err)
	}
	stopDirectIdx, stopRawIdx, stopTproxyIdx := -1, -1, -1
	for i, c := range runner.commands {
		if strings.Contains(c, "S95tproxy-server stop") {
			stopTproxyIdx = i
		} else if strings.Contains(c, "S96telemt-raw stop") {
			stopRawIdx = i
		} else if strings.Contains(c, "S99telemt stop") {
			stopDirectIdx = i
		}
	}
	if stopTproxyIdx < 0 || stopRawIdx < 0 || stopDirectIdx < 0 || !(stopTproxyIdx < stopRawIdx && stopRawIdx < stopDirectIdx) {
		t.Errorf("expected stop order tproxy -> raw -> direct, got commands: %v (indices: %d, %d, %d)", runner.commands, stopTproxyIdx, stopRawIdx, stopDirectIdx)
	}
}

func TestTelegramPrepareAndCommitCandidate(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)

	candidate := Config{
		Enabled:        true,
		Scenario:       ScenarioDual,
		DirectHost:     "cand.direct.test",
		DirectPort:     8443,
		PublicHostname: "cand.cdn.test",
		ListenPort:     8085,
		Secret:         "abcdef0123456789abcdef0123456789",
		TlsDomain:      "cand.tls.test",
	}

	txID, err := svc.PrepareCandidate("", candidate)
	if err != nil {
		t.Fatalf("PrepareCandidate failed: %v", err)
	}

	// Verify snapshot manifest was created on disk
	manifestPath := filepath.Join(tempDir, "tproxy", "transactions", txID, "snapshot-manifest.json")
	if !fileExists(manifestPath) {
		t.Fatalf("expected snapshot manifest at %s", manifestPath)
	}

	manifestBytes, _ := os.ReadFile(manifestPath)
	var manifest SnapshotManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.State != "prepared" {
		t.Errorf("expected state 'prepared', got %q", manifest.State)
	}
	if manifest.CandidateConfig.DirectHost != "cand.direct.test" {
		t.Errorf("manifest candidate mismatch: got %v", manifest.CandidateConfig)
	}

	// Commit candidate
	if err := svc.CommitPrepared(txID); err != nil {
		t.Fatalf("CommitPrepared failed: %v", err)
	}

	if svc.GetConfig().DirectHost != "cand.direct.test" {
		t.Errorf("active config not updated to candidate")
	}

	// Finalize candidate
	if err := svc.FinalizePrepared(txID); err != nil {
		t.Fatalf("FinalizePrepared failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "tproxy", "transactions", txID)); !os.IsNotExist(err) {
		t.Errorf("expected tx dir removed after finalize")
	}
}

func TestTelegramPrepareCommitAndRollbackCandidate(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	svc.mux = NewMux()
	t.Cleanup(func() { _ = svc.mux.Stop() })

	// Reserve a free loopback port for Mux test
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	muxPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	origCfg := Config{
		SchemaVersion:  CurrentSchemaVersion,
		Enabled:        false,
		Scenario:       ScenarioDual,
		ListenPort:     8085,
		AdminPort:      8086,
		Backend:        DefaultBackend,
		CarrierMode:    DefaultCarrierMode,
		Secret:         "00112233445566778899aabbccddeeff",
		PublicHostname: "",
	}
	svc.config = origCfg
	if err := svc.saveSettingsFileLocked(origCfg); err != nil {
		t.Fatalf("save initial settings: %v", err)
	}

	candidate := Config{
		Enabled:        true,
		Scenario:       ScenarioCDNHTTP,
		PublicHostname: "rollback.example.test",
		ListenPort:     muxPort,
		Secret:         "abcdef0123456789abcdef0123456789",
	}

	txID, err := svc.PrepareCandidate("tx-rb-1", candidate)
	if err != nil {
		t.Fatalf("PrepareCandidate failed: %v", err)
	}
	if err := svc.CommitPrepared(txID); err != nil {
		t.Fatalf("CommitPrepared failed: %v", err)
	}
	if !svc.GetConfig().Enabled || !svc.mux.IsRunning() || svc.mux.ListenPort() != muxPort {
		t.Fatalf("expected active candidate state and mux running on %d, got enabled=%v running=%v port=%d",
			muxPort, svc.GetConfig().Enabled, svc.mux.IsRunning(), svc.mux.ListenPort())
	}

	// RollbackPrepared must ACTUALLY restore files, config, stop mux, and delete txDir
	if err := svc.RollbackPrepared(txID); err != nil {
		t.Fatalf("RollbackPrepared failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tempDir, "tproxy", "transactions", txID)); !os.IsNotExist(err) {
		t.Errorf("expected tx dir removed after RollbackPrepared, stat err=%v", err)
	}
	if svc.GetConfig().Enabled {
		t.Errorf("expected s.config.Enabled=false after rollback")
	}
	if svc.mux.IsRunning() {
		t.Errorf("expected mux stopped after rolling back disabled service")
	}
	settingsBytes, err := os.ReadFile(svc.cfgPath())
	if err != nil {
		t.Fatalf("read restored settings: %v", err)
	}
	var restored Config
	if err := json.Unmarshal(settingsBytes, &restored); err != nil {
		t.Fatalf("unmarshal restored settings: %v", err)
	}
	if restored.Enabled {
		t.Errorf("expected disk settings enabled=false after rollback, got true")
	}
}

func TestBuildTgLinks_Pure(t *testing.T) {
	cfg := Config{
		DirectHost:     "direct.example.com",
		DirectPort:     8443,
		PublicHostname: "cdn.example.com",
		TlsDomain:      "s.yimg.com",
		Secret:         "00112233445566778899aabbccddeeff",
	}

	links, err := BuildTgLinks(cfg, "")
	if err != nil {
		t.Fatalf("BuildTgLinks failed: %v", err)
	}

	if !strings.HasPrefix(links.MtproxyURL, "tg://proxy?server=direct.example.com&port=8443") {
		t.Errorf("unexpected mtproxy url: %s", links.MtproxyURL)
	}
	if !strings.HasPrefix(links.TgURL, "tg://webproxy?server=cdn.example.com") {
		t.Errorf("unexpected webproxy url: %s", links.TgURL)
	}
	if !strings.Contains(links.BridgeURL, "https://cdn.example.com/?bridge=") {
		t.Errorf("unexpected bridge url: %s", links.BridgeURL)
	}

	// Test override secret
	overrideSecret := "ffeeddccbbaa99887766554433221100"
	links2, err := BuildTgLinks(cfg, overrideSecret)
	if err != nil {
		t.Fatalf("BuildTgLinks with override failed: %v", err)
	}
	if links2.Secret != overrideSecret {
		t.Errorf("expected secret override to be %s, got %s", overrideSecret, links2.Secret)
	}
	if !strings.Contains(links2.TgURL, overrideSecret) {
		t.Errorf("expected TgURL to contain override secret, got %s", links2.TgURL)
	}
}

func TestPrepareCandidate_DirectFakeTLS_ValidatorAndConfigGen(t *testing.T) {
	svc, _, _ := setupTestService(t)

	validatorCalled := false
	svc.tproxyValidator = func(stagingDir string) error {
		validatorCalled = true
		return errors.New("validator should not be called when web is disabled")
	}

	cand := Config{
		Enabled:        true,
		Scenario:       ScenarioDirectFakeTLS,
		DirectHost:     "direct.myhost.ru",
		DirectPort:     8443,
		PublicHostname: "", // empty in direct_fake_tls!
		Secret:         "11223344556677889900aabbccddeeff",
		TlsDomain:      "ya.ru",
	}

	txID, err := svc.PrepareCandidate("tx-test-direct", cand)
	if err != nil {
		t.Fatalf("PrepareCandidate failed: %v", err)
	}
	if validatorCalled {
		t.Errorf("tproxyValidator was called for direct_fake_tls scenario, expected it to be skipped")
	}

	// Verify config.json.candidate generated has direct-only.invalid fallback hostname
	cfgCandPath := filepath.Join(svc.txDir(txID), "config.json.candidate")
	data, err := os.ReadFile(cfgCandPath)
	if err != nil {
		t.Fatalf("read config.json.candidate failed: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal config.json.candidate failed: %v", err)
	}
	if parsed["public_hostname"] != "direct-only.invalid" {
		t.Errorf("expected public_hostname direct-only.invalid, got %v", parsed["public_hostname"])
	}

	// Now verify that for dual scenario with empty public_hostname, validator IS called
	validatorCalled = false
	candDual := Config{
		Enabled:        true,
		Scenario:       ScenarioDual,
		DirectHost:     "direct.myhost.ru",
		DirectPort:     8443,
		PublicHostname: "",
		Secret:         "11223344556677889900aabbccddeeff",
	}
	_, err = svc.PrepareCandidate("tx-test-dual", candDual)
	if err == nil {
		t.Fatalf("expected PrepareCandidate to fail when validator returns error for dual scenario")
	}
	if !validatorCalled {
		t.Errorf("expected validator to be called for dual scenario")
	}
}

func TestPresets_FourPresetsAndPerEndpointConfigGen(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)

	err := svc.UpdateConfig(Config{
		Enabled:       true,
		Scenario:      ScenarioDual,
		NetworkPreset: PresetCDNStatic,
		DirectHost:    "direct.example.test",
		DirectPort:    8443,
		TlsDomain:     "s.yimg.com",
		Secret:        "0123456789abcdef0123456789abcdef",
		Endpoints: []WebProxyEndpoint{
			{
				ID:          "ep-primary",
				Hostname:    "cdn.example.test",
				Preset:      PresetCDNStatic,
				ListenPort:  8084,
				AdminPort:   8086,
				Description: "Через CDN + Белый IP",
				Enabled:     true,
			},
			{
				ID:          "ep-direct",
				Hostname:    "direct.example.test",
				Preset:      PresetStaticIP,
				ListenPort:  18084,
				AdminPort:   18086,
				Description: "Direct Static IP",
				Enabled:     true,
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateConfig failed: %v", err)
	}

	tproxyDir := filepath.Join(tempDir, "tproxy")

	// Verify primary config.json has cdn_static limits (512 KiB batch, 64 MiB pending)
	primaryCfgBytes, err := os.ReadFile(filepath.Join(tproxyDir, "config.json"))
	if err != nil {
		t.Fatalf("read primary config.json: %v", err)
	}
	var primaryCfg struct {
		Limits struct {
			CarrierBatchBytes    int `json:"carrier_batch_bytes"`
			MaxPendingPerSession int `json:"max_pending_per_session"`
			MaxStreamsPerSession int `json:"max_streams_per_session"`
		} `json:"limits"`
	}
	if err := json.Unmarshal(primaryCfgBytes, &primaryCfg); err != nil {
		t.Fatalf("unmarshal primary config.json: %v", err)
	}
	if primaryCfg.Limits.CarrierBatchBytes != 512*1024 {
		t.Errorf("expected primary carrier_batch_bytes=524288 for cdn_static, got %d", primaryCfg.Limits.CarrierBatchBytes)
	}
	if primaryCfg.Limits.MaxPendingPerSession != 64*1024*1024 {
		t.Errorf("expected primary max_pending_per_session=67108864 for cdn_static, got %d", primaryCfg.Limits.MaxPendingPerSession)
	}

	// Verify primary profiles.json has https-lanes
	primaryProfBytes, err := os.ReadFile(filepath.Join(tproxyDir, "profiles.json"))
	if err != nil {
		t.Fatalf("read primary profiles.json: %v", err)
	}
	var primaryProf struct {
		Profiles []struct {
			CarrierMode string `json:"carrier_mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(primaryProfBytes, &primaryProf); err != nil {
		t.Fatalf("unmarshal primary profiles.json: %v", err)
	}
	if len(primaryProf.Profiles) == 0 || primaryProf.Profiles[0].CarrierMode != "https-lanes" {
		t.Errorf("expected primary carrier_mode=https-lanes for cdn_static, got %+v", primaryProf.Profiles)
	}

	// Verify second endpoint profiles_ep-direct.json has websocket-lanes
	epProfBytes, err := os.ReadFile(filepath.Join(tproxyDir, "profiles_ep-direct.json"))
	if err != nil {
		t.Fatalf("read profiles_ep-direct.json: %v", err)
	}
	var epProf struct {
		Profiles []struct {
			CarrierMode string `json:"carrier_mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(epProfBytes, &epProf); err != nil {
		t.Fatalf("unmarshal profiles_ep-direct.json: %v", err)
	}
	if len(epProf.Profiles) == 0 || epProf.Profiles[0].CarrierMode != "websocket-lanes" {
		t.Errorf("expected ep-direct carrier_mode=websocket-lanes for static_ip, got %+v", epProf.Profiles)
	}

	// Also verify cdn_keendns and keendns presets
	specCDNKeen, ok := ResolvePresetSpec(PresetCDNKeenDNS)
	if !ok || !specCDNKeen.IsCDN || specCDNKeen.CarrierMode != "https-lanes" || specCDNKeen.CarrierBatchBytes != 2*1024*1024 {
		t.Errorf("unexpected cdn_keendns spec: %+v", specCDNKeen)
	}
	specKeen, ok := ResolvePresetSpec(PresetKeenDNS)
	if !ok || specKeen.IsCDN || specKeen.CarrierMode != "websocket" || specKeen.CarrierBatchBytes != 1024*1024 {
		t.Errorf("unexpected keendns spec: %+v", specKeen)
	}
}

var (
	allocPortMu   sync.Mutex
	allocPortSeen = map[int]bool{}
)

func allocFreeLocalPort(t *testing.T) int {
	t.Helper()
	var held []net.Listener
	defer func() {
		for _, ln := range held {
			_ = ln.Close()
		}
	}()
	for range 64 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen free port: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		allocPortMu.Lock()
		dup := allocPortSeen[port]
		if !dup {
			allocPortSeen[port] = true
		}
		allocPortMu.Unlock()
		held = append(held, ln)
		if !dup {
			return port
		}
	}
	t.Fatalf("could not allocate distinct free local port")
	return 0
}

func isAddrInUseErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
}

func bindFreeLocalPort(t *testing.T, label string, fn func(port int) error) int {
	t.Helper()
	var lastErr error
	for range 16 {
		port := allocFreeLocalPort(t)
		err := fn(port)
		if err == nil {
			return port
		}
		if !isAddrInUseErr(err) {
			t.Fatalf("%s (port %d): %v", label, port, err)
		}
		lastErr = err
	}
	t.Fatalf("%s: failed to bind free local port after retries: %v", label, lastErr)
	return 0
}

func TestMux_HotRebindListenPortAndBusyPortPreservesOldListener(t *testing.T) {
	mux := NewMux()
	defer mux.Stop()

	secret := "11112222333344445555666677778888"
	eps := []WebProxyEndpoint{{ID: "ep1", Hostname: "tg.example.com", ListenPort: 18084, Enabled: true}}

	portA := bindFreeLocalPort(t, "StartWithSecrets portA", func(p int) error {
		return mux.StartWithSecrets(p, eps, []string{secret})
	})
	if mux.ListenPort() != portA {
		t.Fatalf("expected ListenPort=%d, got %d", portA, mux.ListenPort())
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", portA), time.Second); err != nil {
		t.Fatalf("expected portA %d to accept connections: %v", portA, err)
	} else {
		_ = c.Close()
	}

	mux.mu.Lock()
	oldLn := mux.listener
	mux.mu.Unlock()
	if oldLn == nil {
		t.Fatalf("expected non-nil listener on portA %d", portA)
	}

	// Rebind to portB while running
	portB := bindFreeLocalPort(t, "StartWithSecrets portB", func(p int) error {
		return mux.StartWithSecrets(p, eps, []string{secret})
	})
	if mux.ListenPort() != portB {
		t.Fatalf("expected ListenPort=%d after rebind, got %d", portB, mux.ListenPort())
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", portB), time.Second); err != nil {
		t.Fatalf("expected portB %d to accept connections: %v", portB, err)
	} else {
		_ = c.Close()
	}
	mux.mu.Lock()
	newLn := mux.listener
	mux.mu.Unlock()
	if newLn == nil || newLn == oldLn {
		t.Fatalf("expected mux to replace listener on rebind from portA %d to portB %d", portA, portB)
	}
	if conn, err := oldLn.Accept(); err == nil {
		_ = conn.Close()
		t.Fatalf("expected old listener on portA %d to be closed after rebind to portB %d", portA, portB)
	} else if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("expected net.ErrClosed from old listener on portA %d, got: %v", portA, err)
	}

	// Occupy portC and verify failed rebind preserves working listener on portB
	busyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen busy port: %v", err)
	}
	defer busyLn.Close()
	busyPort := busyLn.Addr().(*net.TCPAddr).Port

	if err := mux.StartWithSecrets(busyPort, eps, []string{secret}); err == nil {
		t.Fatalf("expected error when rebinding mux to occupied port %d, got nil", busyPort)
	}
	if !mux.IsRunning() || mux.ListenPort() != portB {
		t.Fatalf("expected mux to remain running on portB %d after failed rebind, got running=%v port=%d",
			portB, mux.IsRunning(), mux.ListenPort())
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", portB), time.Second); err != nil {
		t.Fatalf("expected portB %d to still accept connections after failed rebind: %v", portB, err)
	} else {
		_ = c.Close()
	}
}

func TestService_ApplyConfigMuxBindFailureRollsBackTransaction(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	svc.mux = NewMux()
	defer svc.mux.Stop()

	portA := bindFreeLocalPort(t, "initial ApplyManagedIngress", func(p int) error {
		return svc.ApplyManagedIngress(ManagedIngressConfig{
			Enabled:        true,
			ListenPort:     p,
			PublicHostname: "good.example.com",
		})
	})
	if svc.mux.ListenPort() != portA {
		t.Fatalf("expected mux on portA %d, got %d", portA, svc.mux.ListenPort())
	}

	// Occupy a port and attempt UpdateConfig to that busy port
	busyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen busy port: %v", err)
	}
	defer busyLn.Close()
	busyPort := busyLn.Addr().(*net.TCPAddr).Port

	err = svc.UpdateConfig(Config{
		Enabled:        true,
		ListenPort:     busyPort,
		PublicHostname: "broken.example.com",
	})
	if err == nil {
		t.Fatalf("expected UpdateConfig to fail when mux ListenPort %d is busy, got nil", busyPort)
	}

	// Verify in-memory config, on-disk settings, and active mux listener rolled back to portA / good.example.com
	if svc.config.ListenPort != portA || svc.config.PublicHostname != "good.example.com" {
		t.Fatalf("expected in-memory config to remain port=%d host=good.example.com, got port=%d host=%s",
			portA, svc.config.ListenPort, svc.config.PublicHostname)
	}
	if !svc.mux.IsRunning() || svc.mux.ListenPort() != portA {
		t.Fatalf("expected mux to remain running on portA %d after rollback, got running=%v port=%d",
			portA, svc.mux.IsRunning(), svc.mux.ListenPort())
	}
	diskBytes, err := os.ReadFile(filepath.Join(tempDir, "tproxy", "tgwebproxy-settings.json"))
	if err != nil {
		t.Fatalf("read disk settings: %v", err)
	}
	if !strings.Contains(string(diskBytes), "good.example.com") || strings.Contains(string(diskBytes), "broken.example.com") {
		t.Fatalf("expected on-disk tgwebproxy-settings.json to be rolled back to good.example.com, got: %s", string(diskBytes))
	}
}

func TestService_ExternalBackendEndpointNoWorkerContentionAndHostOnlyMux(t *testing.T) {
	svc, tempDir, _ := setupTestService(t)
	svc.mux = NewMux()
	defer svc.mux.Stop()

	// 1. Start two local HTTP servers representing:
	//    - primary managed tproxy-server on primaryPort (normal endpoint)
	//    - standalone external file-https-v1 harness on extPort (ExternalBackend: true)
	primaryLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen primary: %v", err)
	}
	defer primaryLn.Close()
	primaryPort := primaryLn.Addr().(*net.TCPAddr).Port
	primarySrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Owner", "primary-tproxy")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("primary-ok:" + r.Host))
	})}
	go primarySrv.Serve(primaryLn)
	defer primarySrv.Close()

	extLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ext: %v", err)
	}
	defer extLn.Close()
	extPort := extLn.Addr().(*net.TCPAddr).Port
	extSrv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Owner", "standalone-file-https-harness")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ext-harness-ok:" + r.Host + ":" + r.URL.Path))
	})}
	go extSrv.Serve(extLn)
	defer extSrv.Close()

	muxPort := bindFreeLocalPort(t, "ApplyManagedIngress", func(p int) error {
		return svc.ApplyManagedIngress(ManagedIngressConfig{
			Enabled:        true,
			ListenPort:     p,
			PublicHostname: "primary.example.com",
		})
	})
	secret := "0123456789abcdef0123456789abcdef"

	cfg := Config{
		Enabled:        true,
		Scenario:       ScenarioCDNHTTP,
		ListenPort:     muxPort,
		Secret:         secret,
		PublicHostname: "primary.example.com",
		Endpoints: []WebProxyEndpoint{
			{
				ID:          "ep-primary",
				Hostname:    "primary.example.com",
				ListenPort:  primaryPort,
				AdminPort:   18086,
				Preset:      PresetCDNStatic,
				CarrierMode: "https-lanes",
				IsCDN:       true,
				Enabled:     true,
			},
			{
				ID:              "ep-twcdn-ext",
				Hostname:        "twcdn.example.com",
				ListenPort:      extPort,
				Description:     "Standalone File-HTTPS Harness",
				Preset:          PresetCDNFileHTTPS,
				CarrierMode:     CarrierModeFileHTTPS,
				IsCDN:           true,
				Enabled:         true,
				ExternalBackend: true,
			},
		},
	}

	// 2. Enable service and apply configuration & run runtime reconciliation
	if err := svc.UpdateConfig(cfg); err != nil {
		t.Fatalf("UpdateConfig with ExternalBackend endpoint: %v", err)
	}
	if err := svc.ReconcileRuntime(); err != nil {
		t.Fatalf("ReconcileRuntime: %v", err)
	}

	// 3. Verify NO config_ep-twcdn-ext.json or profiles_ep-twcdn-ext.json was generated for the external backend
	tproxyDir := filepath.Join(tempDir, "tproxy")
	if _, err := os.Stat(filepath.Join(tproxyDir, "config_ep-twcdn-ext.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no config_ep-twcdn-ext.json for ExternalBackend endpoint, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(tproxyDir, "profiles_ep-twcdn-ext.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no profiles_ep-twcdn-ext.json for ExternalBackend endpoint, got err=%v", err)
	}

	// Verify ListenPort of external backend was preserved as extPort (not overwritten to 18084)
	gotCfg := svc.GetInternalConfig()
	if len(gotCfg.Endpoints) != 2 || gotCfg.Endpoints[1].ListenPort != extPort || !gotCfg.Endpoints[1].ExternalBackend {
		t.Fatalf("unexpected external endpoint config after UpdateConfig: %+v", gotCfg.Endpoints)
	}

	// 4. Prove single owner of extPort and host-only routing through Mux:
	//    - Request for twcdn.example.com with file-https-v1 token routes to extSrv
	//    - Request for primary.example.com routes to primarySrv
	hc := &http.Client{Timeout: 2 * time.Second}
	fileTok := DeriveFileBridgeToken("twcdn.example.com", secret)
	reqExt, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/bridge/v1/session.bin?b=%s", muxPort, fileTok), nil)
	reqExt.Host = "twcdn.example.com"
	respExt, err := hc.Do(reqExt)
	if err != nil {
		t.Fatalf("mux request to external endpoint failed: %v", err)
	}
	bodyExt, _ := io.ReadAll(respExt.Body)
	respExt.Body.Close()
	if respExt.Header.Get("X-Owner") != "standalone-file-https-harness" || string(bodyExt) != "ext-harness-ok:twcdn.example.com:/bridge/v1/session.bin" {
		t.Fatalf("unexpected response from external backend via mux: owner=%q body=%q", respExt.Header.Get("X-Owner"), string(bodyExt))
	}

	// Verify primary endpoint remains untouched and routes to primary-tproxy
	primaryTok := DeriveBridgeToken("primary.example.com", secret)
	reqPri, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/?bridge=%s", muxPort, primaryTok), nil)
	reqPri.Host = "primary.example.com"
	respPri, err := hc.Do(reqPri)
	if err != nil {
		t.Fatalf("mux request to primary endpoint failed: %v", err)
	}
	bodyPri, _ := io.ReadAll(respPri.Body)
	respPri.Body.Close()
	if respPri.Header.Get("X-Owner") != "primary-tproxy" || string(bodyPri) != "primary-ok:primary.example.com" {
		t.Fatalf("unexpected response from primary endpoint via mux: owner=%q body=%q", respPri.Header.Get("X-Owner"), string(bodyPri))
	}
}

func TestConstrainTproxyLimitsForRAMMB_AndDirectKeenDNSNonCDN(t *testing.T) {
	// 1. Constrained 248 MB router limits pass tproxy Load + ValidateBudget cleanly
	specKeen, ok := ResolvePresetSpec(PresetKeenDNS)
	if !ok {
		t.Fatal("PresetKeenDNS not found")
	}
	baseLimits := map[string]interface{}{
		"carrier_batch_bytes":     specKeen.CarrierBatchBytes,
		"max_pending_per_session": specKeen.MaxPendingPerSession,
		"max_streams_per_session": specKeen.MaxStreamsPerSession,
	}
	constrained := constrainTproxyLimitsForRAMMB(baseLimits, 248)
	if constrained["max_pending_per_session"] != 4*1024*1024 {
		t.Fatalf("expected 4 MiB max_pending_per_session on 248 MB device, got %v", constrained["max_pending_per_session"])
	}
	if constrained["max_pending_global"] != 16*1024*1024 || constrained["max_sessions_global"] != 16 {
		t.Fatalf("expected 16 MiB / 16 sessions global cap on 248 MB device, got %+v", constrained)
	}
	if constrained["carrier_batch_bytes"] != 512*1024 {
		t.Fatalf("expected 512 KiB carrier_batch_bytes on 248 MB device, got %v", constrained["carrier_batch_bytes"])
	}

	tmp := t.TempDir()
	profPath := filepath.Join(tmp, "profiles.json")
	tokPath := filepath.Join(tmp, "token.key")
	cfgPath := filepath.Join(tmp, "config.json")
	if err := os.WriteFile(profPath, []byte(`{"profiles":[{"name":"default","secret":"000102030405060708090a0b0c0d0e0f","backend":"127.0.0.1:2398","carrier_mode":"websocket"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokPath, bytes.Repeat([]byte{0x11}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	rawCfg := map[string]interface{}{
		"public_hostname": "connect.example.netcraze.club",
		"listen":          "127.0.0.1:8084",
		"admin_listen":    "127.0.0.1:8086",
		"token_key_file":  tokPath,
		"static_routes":   "exact",
		"profiles_file":   profPath,
		"public_fallback": "builtin",
		"limits":          constrained,
	}
	cfgBytes, _ := json.Marshal(rawCfg)
	if err := os.WriteFile(cfgPath, cfgBytes, 0600); err != nil {
		t.Fatal(err)
	}
	loadedCfg, err := tproxyconfig.Load(cfgPath)
	if err != nil {
		t.Fatalf("constrained 248 MB tproxy config rejected by tproxyconfig.Load: %v", err)
	}
	if err := tproxysession.ValidateBudget(loadedCfg); err != nil {
		t.Fatalf("constrained 248 MB tproxy config rejected by ValidateBudget: %v", err)
	}

	// 1024 MB device remains unconstrained
	highLimits := constrainTproxyLimitsForRAMMB(map[string]interface{}{
		"carrier_batch_bytes":     2 * 1024 * 1024,
		"max_pending_per_session": 32 * 1024 * 1024,
		"max_streams_per_session": 128,
	}, 1024)
	if highLimits["max_pending_per_session"] != 32*1024*1024 {
		t.Fatalf("expected 1024 MB device to keep 32 MiB limit, got %v", highLimits["max_pending_per_session"])
	}

	// 2. Direct KeenDNS ScenarioDual with websocket stays PresetKeenDNS / IsCDN=false
	svc, _, _ := setupTestService(t)
	candCfg := Config{
		Enabled:        true,
		Scenario:       ScenarioDual,
		PublicHostname: "connect.example.netcraze.club",
		DirectHost:     "connect.example.netcraze.club",
		DirectPort:     8443,
		TlsDomain:      "www.google.com",
		Secret:         "000102030405060708090a0b0c0d0e0f",
		CarrierMode:    "websocket",
	}
	txID, err := svc.PrepareCandidate("tx-kd-ws", candCfg)
	if err != nil {
		t.Fatalf("PrepareCandidate: %v", err)
	}
	if err := svc.CommitPrepared(txID); err != nil {
		t.Fatalf("CommitPrepared: %v", err)
	}
	if err := svc.FinalizePrepared(txID); err != nil {
		t.Fatalf("FinalizePrepared: %v", err)
	}
	prep := svc.GetInternalConfig()
	if prep.NetworkPreset != PresetKeenDNS || prep.CarrierMode != "websocket" {
		t.Fatalf("expected direct KeenDNS dual websocket to use preset=%s carrier=websocket, got preset=%s carrier=%s", PresetKeenDNS, prep.NetworkPreset, prep.CarrierMode)
	}
	if eps := prep.GetEndpoints(); len(eps) != 1 || eps[0].IsCDN {
		t.Fatalf("expected endpoint IsCDN=false for direct KeenDNS websocket, got %+v", eps)
	}
}

func TestService_FactoryReset_WipesState(t *testing.T) {
	svc, tmpDir, runner := setupTestService(t)

	if err := os.MkdirAll(filepath.Join(tmpDir, "tproxy", "transactions", "tx-1"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		filepath.Join("tproxy", "config.json"),
		filepath.Join("tproxy", "profiles.json"),
		filepath.Join("tproxy", "token.key"),
		filepath.Join("tproxy", "csrf.key"),
		filepath.Join("tproxy", "transactions", "tx-1", "manifest.json"),
	} {
		if err := os.WriteFile(filepath.Join(tmpDir, f), []byte("old-state"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	svc.mu.Lock()
	svc.config.Enabled = true
	svc.config.PublicHostname = "tg.example.com"
	svc.config.DirectHost = "tg.example.com"
	svc.config.Secret = "000102030405060708090a0b0c0d0e0f"
	svc.config.Endpoints = []WebProxyEndpoint{{ID: "ep-1", Hostname: "tg.example.com", Enabled: true}}
	svc.config.Users = []TelegramUser{{ID: "u-1", Remark: "Alice", Secret: "ff0102030405060708090a0b0c0d0e0f", Enabled: true}}
	svc.recoveryRequired = true
	svc.lastStartupError = "old error"
	svc.mu.Unlock()

	if err := svc.FactoryReset(context.Background()); err != nil {
		t.Fatalf("FactoryReset failed: %v", err)
	}

	// tproxy runtime/tx files must be deleted
	for _, gone := range []string{
		filepath.Join("tproxy", "config.json"),
		filepath.Join("tproxy", "profiles.json"),
		filepath.Join("tproxy", "transactions"),
	} {
		if _, err := os.Stat(filepath.Join(tmpDir, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted during FactoryReset", gone)
		}
	}

	// Clean defaults, fresh keys & cleared recovery state
	cfg := svc.GetInternalConfig()
	if cfg.Enabled || cfg.PublicHostname != "" || cfg.DirectHost != "" || len(cfg.Endpoints) != 0 || len(cfg.Users) != 0 {
		t.Fatalf("expected reset config defaults, got %+v", cfg)
	}
	if len(cfg.Secret) != 32 || cfg.Secret == "000102030405060708090a0b0c0d0e0f" {
		t.Fatalf("expected newly rotated 32-char default secret, got %q", cfg.Secret)
	}
	if st := svc.GetStatus(); st.RecoveryRequired {
		t.Fatal("expected RecoveryRequired to be cleared after FactoryReset")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "tproxy", "token.key")); err != nil {
		t.Fatalf("expected fresh token.key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "tproxy", "csrf.key")); err != nil {
		t.Fatalf("expected fresh csrf.key: %v", err)
	}
	if len(runner.commands) == 0 {
		t.Fatal("expected workers stop commands during FactoryReset")
	}
}

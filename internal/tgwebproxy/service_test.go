package tgwebproxy

import (
	"bytes"
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
	"testing"
	"time"
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
		dataDir:   tempDir,
		telemtDir: filepath.Join(tempDir, "telemt"),
		tc:        tc,
		workers:   workers,
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

	_ = os.MkdirAll(svc.telemtDir, 0700)
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
		telemtDir:       filepath.Join(tempDir, "telemt"),
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
		dataDir:   tempDir,
		telemtDir: filepath.Join(tempDir, "telemt"),
		tc:        tc,
		workers:   nil,
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
	if fileExists(filepath.Join(tempDir, "tproxy", "transactions", txID)) {
		t.Errorf("expected tx dir removed after finalize")
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



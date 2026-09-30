package tgwebproxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (s *Service) transactionsDir() string {
	return filepath.Join(s.tproxyDir(), "transactions")
}

func (s *Service) txDir(txID string) string {
	return filepath.Join(s.transactionsDir(), txID)
}

func isValidTgTxID(txID string) bool {
	if len(txID) == 0 || len(txID) > 128 {
		return false
	}
	for _, c := range txID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func generateShortTgID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()%0xffff)
	}
	return hex.EncodeToString(b)
}

// PrepareCandidate stages a candidate config into an isolated transaction snapshot directory with durable old and new state.
func (s *Service) PrepareCandidate(txID string, candidate Config) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.recoveryRequired {
		return "", ErrRecoveryRequired
	}

	if txID == "" {
		txID = fmt.Sprintf("tx-tg-%d-%s", time.Now().UnixNano(), generateShortTgID())
	} else if !isValidTgTxID(txID) {
		return "", fmt.Errorf("invalid transaction id: %s", txID)
	}

	// Normalize defaults for candidate
	if candidate.DirectPort <= 0 {
		candidate.DirectPort = DefaultDirectPort
	}
	if candidate.ListenPort <= 0 {
		candidate.ListenPort = DefaultListenPort
	}
	if candidate.AdminPort <= 0 {
		candidate.AdminPort = DefaultAdminPort
	}
	if candidate.Scenario == "" {
		candidate.Scenario = ScenarioDual
	}
	if candidate.Backend == "" {
		candidate.Backend = DefaultBackend
	}
	if candidate.CarrierMode == "" {
		candidate.CarrierMode = DefaultCarrierMode
	}
	if candidate.Secret == "" {
		sec, err := randomHexSecret(s.randReader)
		if err != nil {
			return "", err
		}
		candidate.Secret = sec
	}
	if candidate.SchemaVersion <= 0 {
		candidate.SchemaVersion = CurrentSchemaVersion
	}

	if candidate.Enabled && s.egressValidator != nil && candidate.UpstreamDevice != "" {
		if err := s.egressValidator(candidate.UpstreamDevice); err != nil {
			return "", fmt.Errorf("egress validation failed: %w", err)
		}
	}

	txPath := s.txDir(txID)
	if err := os.MkdirAll(txPath, 0700); err != nil {
		return "", fmt.Errorf("create tx dir: %w", err)
	}
	_ = os.Chmod(txPath, 0700)

	manifest := SnapshotManifest{
		TxID:             txID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		OldConfig:        s.config,
		CandidateConfig:  candidate,
		WasRunning:       s.isRunningLocked(),
		CandidateRunning: candidate.Enabled,
		State:            "prepared",
		Checksums:        make(map[string]string),
		SchemaVersion:    CurrentSchemaVersion,
	}

	// 1. Snapshot existing files
	filesToBackup := []struct {
		src     string
		bakName string
	}{
		{s.cfgPath(), "tgwebproxy-settings.json.bak"},
		{s.telemtDirectPath(), "config.toml.bak"},
		{s.telemtRawPath(), "raw.toml.bak"},
		{filepath.Join(s.tproxyDir(), "profiles.json"), "profiles.json.bak"},
		{filepath.Join(s.tproxyDir(), "config.json"), "config.json.bak"},
	}

	for _, fb := range filesToBackup {
		if fileExists(fb.src) {
			sum, err := fileSHA256(fb.src)
			if err != nil {
				_ = os.RemoveAll(txPath)
				return "", fmt.Errorf("hash file %s: %w", fb.src, err)
			}
			manifest.Checksums[fb.bakName] = sum
			data, err := os.ReadFile(fb.src)
			if err != nil {
				_ = os.RemoveAll(txPath)
				return "", fmt.Errorf("read file %s: %w", fb.src, err)
			}
			if err := copyFileAtomic(fb.src, filepath.Join(txPath, fb.bakName), 0600, 0, 0); err != nil {
				_ = os.RemoveAll(txPath)
				return "", fmt.Errorf("backup file %s: %w", fb.src, err)
			}
			_ = data
		}
	}

	// 2. Render candidate files
	telemtDirectTOML := GenerateTelemtDirectConfig(candidate)
	telemtRawTOML := GenerateTelemtRawConfig(candidate)

	profilesJSON, err := GenerateProfilesJSON(candidate)
	if err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("generate candidate profiles: %w", err)
	}

	tokenKeyPath := filepath.Join(s.tproxyDir(), "token.key")
	profilesPath := filepath.Join(s.tproxyDir(), "profiles.json")
	configJSON, err := GenerateTproxyConfigJSON(candidate, profilesPath, tokenKeyPath)
	if err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("generate candidate config.json: %w", err)
	}

	settingsJSON, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("marshal candidate settings: %w", err)
	}

	candFiles := []struct {
		name    string
		content []byte
	}{
		{"config.toml.candidate", []byte(telemtDirectTOML)},
		{"raw.toml.candidate", []byte(telemtRawTOML)},
		{"profiles.json.candidate", profilesJSON},
		{"config.json.candidate", configJSON},
		{"tgwebproxy-settings.json.candidate", settingsJSON},
	}

	for _, cf := range candFiles {
		p := filepath.Join(txPath, cf.name)
		if err := os.WriteFile(p, cf.content, 0600); err != nil {
			_ = os.RemoveAll(txPath)
			return "", fmt.Errorf("write candidate file %s: %w", cf.name, err)
		}
	}

	// 3. Validation hook
	if candidate.Enabled && candidate.IsWebEnabled() && s.tproxyValidator != nil {
		if err := s.tproxyValidator(txPath); err != nil {
			_ = os.RemoveAll(txPath)
			return "", fmt.Errorf("candidate validation failed: %w", err)
		}
	}

	// 4. Save manifest atomically
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("marshal snapshot manifest: %w", err)
	}

	// Write both snapshot-manifest.json and manifest.json for compatibility
	if err := os.WriteFile(filepath.Join(txPath, "snapshot-manifest.json"), manifestBytes, 0600); err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("write snapshot manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(txPath, "manifest.json"), manifestBytes, 0600); err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("write manifest: %w", err)
	}
	_ = fsyncDir(txPath)

	return txID, nil
}

func readTgSnapshotManifest(txPath string) (*SnapshotManifest, error) {
	manifestPath := filepath.Join(txPath, "snapshot-manifest.json")
	if !fileExists(manifestPath) {
		manifestPath = filepath.Join(txPath, "manifest.json")
	}
	if !fileExists(manifestPath) {
		return nil, fmt.Errorf("manifest not found in %s", txPath)
	}

	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m SnapshotManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return &m, nil
}

// CommitPrepared atomically applies the prepared candidate configuration.
func (s *Service) CommitPrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	txPath := s.txDir(txID)
	manifest, err := readTgSnapshotManifest(txPath)
	if err != nil {
		return err
	}

	if manifest.State == "active" || manifest.State == "committed" {
		return nil
	}

	rollbackAndJoin := func(origErr error) error {
		if rbErr := s.rollbackLocked(txID, manifest); rbErr != nil {
			return errors.Join(origErr, fmt.Errorf("rollback failed: %w", rbErr))
		}
		return origErr
	}

	candidate := manifest.CandidateConfig

	// 1. Commit active files
	if candidate.Enabled {
		filePairs := []struct {
			candPath string
			dstPath  string
		}{
			{filepath.Join(txPath, "config.toml.candidate"), s.telemtDirectPath()},
			{filepath.Join(txPath, "raw.toml.candidate"), s.telemtRawPath()},
			{filepath.Join(txPath, "profiles.json.candidate"), filepath.Join(s.tproxyDir(), "profiles.json")},
			{filepath.Join(txPath, "config.json.candidate"), filepath.Join(s.tproxyDir(), "config.json")},
			{filepath.Join(txPath, "tgwebproxy-settings.json.candidate"), s.cfgPath()},
		}

		for _, fp := range filePairs {
			if !fileExists(fp.candPath) {
				return rollbackAndJoin(fmt.Errorf("candidate file missing: %s", fp.candPath))
			}
			if err := copyFileAtomic(fp.candPath, fp.dstPath, 0600, 0, 0); err != nil {
				return rollbackAndJoin(fmt.Errorf("apply file %s: %w", fp.dstPath, err))
			}
		}

		// 2. Apply workers and check readiness
		if s.workers != nil {
			if err := s.workers.ApplyWorkers(candidate); err != nil {
				return rollbackAndJoin(fmt.Errorf("apply workers: %w", err))
			}
			if err := s.workers.CheckReadiness(candidate); err != nil {
				return rollbackAndJoin(fmt.Errorf("check readiness: %w", err))
			}
		}
	} else {
		// Stop workers and update settings file
		if s.workers != nil {
			_ = s.workers.ApplyWorkers(candidate)
		}
		candSettings := filepath.Join(txPath, "tgwebproxy-settings.json.candidate")
		if fileExists(candSettings) {
			if err := copyFileAtomic(candSettings, s.cfgPath(), 0600, 0, 0); err != nil {
				return rollbackAndJoin(fmt.Errorf("apply settings: %w", err))
			}
		}
	}

	s.config = candidate

	// 3. Mark manifest active
	manifest.State = "active"
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err == nil {
		_ = os.WriteFile(filepath.Join(txPath, "snapshot-manifest.json"), manifestBytes, 0600)
		_ = os.WriteFile(filepath.Join(txPath, "manifest.json"), manifestBytes, 0600)
		_ = fsyncDir(txPath)
	}

	s.scheduleLegacyExpiryLocked()
	s.notifyReload()
	go s.checkUpstreamHealth()

	return nil
}

// RollbackPrepared rolls back a prepared or committed transaction snapshot.
func (s *Service) RollbackPrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.rollbackLocked(txID, nil)
}

func (s *Service) rollbackLocked(txID string, manifest *SnapshotManifest) error {
	txPath := s.txDir(txID)
	if !fileExists(txPath) {
		return nil
	}

	if manifest == nil {
		var err error
		manifest, err = readTgSnapshotManifest(txPath)
		if err != nil {
			return err
		}
	}

	// 1. Verify backup checksums
	for filename, expectedHash := range manifest.Checksums {
		src := filepath.Join(txPath, filename)
		actualHash, err := fileSHA256(src)
		if err != nil || actualHash != expectedHash {
			return fmt.Errorf("snapshot corruption in %s: hash mismatch (want %s, got %s)", filename, expectedHash, actualHash)
		}
	}

	var rollbackErrs []error

	// 2. Restore backup files
	fileRestores := []struct {
		bakName string
		dstPath string
	}{
		{"config.toml.bak", s.telemtDirectPath()},
		{"raw.toml.bak", s.telemtRawPath()},
		{"profiles.json.bak", filepath.Join(s.tproxyDir(), "profiles.json")},
		{"config.json.bak", filepath.Join(s.tproxyDir(), "config.json")},
		{"tgwebproxy-settings.json.bak", s.cfgPath()},
	}

	for _, fr := range fileRestores {
		bakPath := filepath.Join(txPath, fr.bakName)
		if _, ok := manifest.Checksums[fr.bakName]; ok && fileExists(bakPath) {
			if err := copyFileAtomic(bakPath, fr.dstPath, 0600, 0, 0); err != nil {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("restore %s: %w", fr.dstPath, err))
			}
		} else {
			// Did not exist before: remove
			if err := os.Remove(fr.dstPath); err != nil && !os.IsNotExist(err) {
				rollbackErrs = append(rollbackErrs, fmt.Errorf("remove uncommitted %s: %w", fr.dstPath, err))
			}
		}
	}

	// 3. Restore worker states
	if s.workers != nil {
		if err := s.workers.ApplyWorkers(manifest.OldConfig); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore workers: %w", err))
		}
	}

	s.config = manifest.OldConfig

	if len(rollbackErrs) > 0 {
		return errors.Join(rollbackErrs...)
	}

	// 4. Clean up tx directory
	_ = os.RemoveAll(txPath)
	_ = fsyncDir(s.transactionsDir())
	s.notifyReload()
	return nil
}

// FinalizePrepared cleans up transaction directory after all multi-component commits succeed.
func (s *Service) FinalizePrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	txPath := s.txDir(txID)
	if _, err := os.Stat(txPath); os.IsNotExist(err) {
		return nil
	}

	manifest, err := readTgSnapshotManifest(txPath)
	if err != nil {
		return fmt.Errorf("read manifest for finalize: %w", err)
	}
	if manifest.State != "active" && manifest.State != "committed" {
		return fmt.Errorf("cannot finalize transaction %s in state %s: must be active", txID, manifest.State)
	}

	return os.RemoveAll(txPath)
}

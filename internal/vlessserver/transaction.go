package vlessserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type TxState string

const (
	TxStatePrepared         TxState = "prepared"
	TxStateTested           TxState = "tested"
	TxStateBackupReady      TxState = "backup_ready"
	TxStateActiveReplaced   TxState = "active_replaced"
	TxStateRestarting       TxState = "restarting"
	TxStateVerifying        TxState = "verifying"
	TxStateCommitted        TxState = "committed"
	TxStateRollingBack      TxState = "rolling_back"
	TxStateRolledBack       TxState = "rolled_back"
	TxStateRecoveryRequired TxState = "recovery_required"
	TxStateAborted          TxState = "aborted"
)

type ListenerRequirement struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
	Tag     string `json:"tag,omitempty"`
}

type SnapshotManifest struct {
	TxID              string                `json:"tx_id"`
	SchemaVersion     int                   `json:"schema_version"`
	CreatedAt         string                `json:"created_at"`
	UpdatedAt         string                `json:"updated_at,omitempty"`
	State             TxState               `json:"state"`
	BaseRevision      uint64                `json:"base_revision"`
	BaseConfigHash    string                `json:"base_config_hash,omitempty"`
	Checksums         map[string]string     `json:"checksums"`
	OldConfig         Config                `json:"old_config"`
	CandidateConfig   Config                `json:"candidate_config"`
	WasRunning        bool                  `json:"was_running"`
	CandidateRunning  bool                  `json:"candidate_running,omitempty"`
	RequiredListeners []ListenerRequirement `json:"required_listeners,omitempty"`
	Committed         bool                  `json:"committed,omitempty"`
	Error             string                `json:"error,omitempty"`
}

func computeConfigStateHash(cfg Config) string {
	normalized := cfg
	if cfg.Clients != nil {
		normalized.Clients = make([]Client, len(cfg.Clients))
		copy(normalized.Clients, cfg.Clients)
	}
	if len(cfg.Endpoints) > 0 {
		normalized.Endpoints = make([]EndpointConfig, len(cfg.Endpoints))
		for i, ep := range cfg.Endpoints {
			if ep.XHTTPTuning != nil {
				t := *ep.XHTTPTuning
				ep.XHTTPTuning = &t
			}
			normalized.Endpoints[i] = ep
		}
		NormalizeEndpoints(&normalized)
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isValidTxID(txID string) bool {
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

func (s *Service) transactionsDir() string {
	return filepath.Join(s.xrayDir(), "transactions")
}

func (s *Service) txDir(txID string) string {
	return filepath.Join(s.transactionsDir(), txID)
}

func (s *Service) cloneConfigLocked() Config {
	cfg := s.config
	if s.config.Clients != nil {
		cfg.Clients = make([]Client, len(s.config.Clients))
		copy(cfg.Clients, s.config.Clients)
	}
	if s.config.Endpoints != nil {
		cfg.Endpoints = make([]EndpointConfig, len(s.config.Endpoints))
		for i, ep := range s.config.Endpoints {
			if ep.XHTTPTuning != nil {
				t := *ep.XHTTPTuning
				ep.XHTTPTuning = &t
			}
			cfg.Endpoints[i] = ep
		}
	}
	return cfg
}

func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

func generateShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano()%0xffff)
	}
	return hex.EncodeToString(b)
}

func writeManifest(txPath string, manifest *SnapshotManifest) error {
	manifest.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	return storage.AtomicWritePerm(filepath.Join(txPath, "snapshot-manifest.json"), data, 0600)
}

// PrepareCandidate stages and validates a candidate configuration in an isolated transaction directory.
func (s *Service) PrepareCandidate(txID string, candidate Config) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareCandidateLocked(txID, candidate)
}

func (s *Service) prepareCandidateLocked(txID string, candidate Config) (string, error) {
	if s.recoveryRequired {
		return "", ErrRecoveryRequired
	}
	if err := validateConfigTransports(candidate); err != nil {
		return "", err
	}

	if txID == "" {
		txID = fmt.Sprintf("tx-%d-%s", time.Now().UnixNano(), generateShortID())
	} else if !isValidTxID(txID) {
		return "", fmt.Errorf("invalid transaction id: %s", txID)
	}

	txPath := s.txDir(txID)
	if err := os.MkdirAll(txPath, 0700); err != nil {
		return "", fmt.Errorf("create tx dir: %w", err)
	}

	if len(candidate.Endpoints) > 0 {
		NormalizeEndpoints(&candidate)
	}

	listenAddr := candidate.ListenAddress
	if listenAddr == "" {
		listenAddr = "127.0.0.1"
	}
	listenPort := candidate.ListenPort
	if listenPort <= 0 {
		listenPort = 9008
	}

	manifest := SnapshotManifest{
		TxID:             txID,
		SchemaVersion:    1,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		State:            TxStatePrepared,
		BaseRevision:     s.configRev,
		BaseConfigHash:   computeConfigStateHash(s.config),
		Checksums:        make(map[string]string),
		OldConfig:        s.cloneConfigLocked(),
		CandidateConfig:  candidate,
		WasRunning:       s.daemon != nil && s.daemon.IsRunning(),
		CandidateRunning: candidate.Enabled,
		RequiredListeners: []ListenerRequirement{
			{Address: listenAddr, Port: listenPort, Tag: "vless-in"},
		},
	}

	abortTx := func(reason error) (string, error) {
		manifest.State = TxStateAborted
		manifest.Error = reason.Error()
		_ = writeManifest(txPath, &manifest)
		return "", reason
	}

	// 1. Snapshot settings file
	cfgFile := s.cfgPath()
	if fileExists(cfgFile) {
		sum, err := sha256File(cfgFile)
		if err != nil {
			return abortTx(fmt.Errorf("hash settings file: %w", err))
		}
		manifest.Checksums["xray-server-settings.json.bak"] = sum
		data, err := os.ReadFile(cfgFile)
		if err != nil {
			return abortTx(fmt.Errorf("read settings file: %w", err))
		}
		if err := storage.AtomicWritePerm(filepath.Join(txPath, "xray-server-settings.json.bak"), data, 0600); err != nil {
			return abortTx(fmt.Errorf("backup settings: %w", err))
		}
	}

	// 2. Validate xhttpserver config rendering for awgm-xhttpd
	xCfg := buildXHTTPServerConfig(candidate)
	if candidate.Enabled {
		checkCfg := xCfg
		if len(checkCfg.Clients) == 0 {
			checkCfg.Clients = []string{"00000000-0000-4000-8000-000000000001"}
		}
		if err := checkCfg.Validate(); err != nil {
			return abortTx(fmt.Errorf("validate candidate xhttp config: %w", err))
		}
	}
	manifest.State = TxStateTested
	manifest.State = TxStateBackupReady
	if err := writeManifest(txPath, &manifest); err != nil {
		return abortTx(fmt.Errorf("write manifest: %w", err))
	}

	return txID, nil
}

// CommitPrepared atomically applies the prepared candidate to settings and the C awgm-xhttpd daemon.
func (s *Service) CommitPrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitPreparedLocked(txID)
}

func (s *Service) commitPreparedLocked(txID string) error {
	txPath := s.txDir(txID)
	manifestPath := filepath.Join(txPath, "snapshot-manifest.json")
	if !fileExists(manifestPath) {
		return fmt.Errorf("transaction %s not found", txID)
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("unmarshal manifest: %w", err)
	}

	if manifest.State == TxStateCommitted {
		return nil
	}
	if manifest.State == TxStateAborted || manifest.State == TxStateRolledBack || manifest.State == TxStateRecoveryRequired {
		return fmt.Errorf("cannot commit transaction %s in state %s", txID, manifest.State)
	}

	currentHash := computeConfigStateHash(s.config)
	if manifest.BaseRevision != s.configRev || (manifest.BaseConfigHash != "" && manifest.BaseConfigHash != currentHash) {
		manifest.State = TxStateAborted
		manifest.Error = ErrStaleTransactionConflict.Error()
		_ = writeManifest(txPath, &manifest)
		if s.secrets != nil {
			_ = s.secrets.RollbackTx(context.Background(), txID)
		}
		return fmt.Errorf("%w: transaction %s prepared at revision %d cannot be committed at revision %d",
			ErrStaleTransactionConflict, txID, manifest.BaseRevision, s.configRev)
	}

	rollbackAndJoin := func(origErr error) error {
		manifest.State = TxStateRollingBack
		manifest.Error = origErr.Error()
		_ = writeManifest(txPath, &manifest)

		rbErr := s.rollbackLocked(txID, &manifest)
		if rbErr != nil {
			manifest.State = TxStateRecoveryRequired
			_ = writeManifest(txPath, &manifest)
			s.recoveryRequired = true
			s.recoveryReason = fmt.Sprintf("commit failed (%v) and rollback failed (%v)", origErr, rbErr)
			return errors.Join(origErr, fmt.Errorf("rollback failed: %w", rbErr))
		}

		manifest.State = TxStateRolledBack
		_ = writeManifest(txPath, &manifest)
		return origErr
	}

	prevMode := s.config.OutboundMode
	prevOverride := s.fircRoutingOverride
	s.config = manifest.CandidateConfig
	if s.config.OutboundMode == "router" {
		s.fircRoutingOverride = true
	} else if s.config.OutboundMode != "" && prevMode == "router" {
		s.fircRoutingOverride = false
	}

	manifest.State = TxStateActiveReplaced
	_ = writeManifest(txPath, &manifest)

	if err := s.saveConfig(); err != nil {
		s.config = manifest.OldConfig
		s.fircRoutingOverride = prevOverride
		return rollbackAndJoin(fmt.Errorf("write candidate settings: %w", err))
	}

	if err := s.syncDaemonLocked(); err != nil {
		s.fircRoutingOverride = prevOverride
		return rollbackAndJoin(fmt.Errorf("apply to awgm-xhttpd daemon: %w", err))
	}

	if s.secrets != nil {
		_ = s.secrets.CommitTx(context.Background(), txID)
	}

	s.configRev++
	manifest.Committed = true
	manifest.State = TxStateCommitted
	manifest.CandidateRunning = (s.daemon != nil && s.daemon.IsRunning()) || (s.realitySrv != nil && s.realitySrv.IsRunning())
	if err := writeManifest(txPath, &manifest); err != nil {
		return fmt.Errorf("write committed manifest: %w", err)
	}
	return nil
}

// AbortPrepared cancels an uncommitted transaction in early stages.
func (s *Service) AbortPrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	txPath := s.txDir(txID)
	manifestPath := filepath.Join(txPath, "snapshot-manifest.json")
	if !fileExists(manifestPath) {
		return nil
	}

	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest for abort: %w", err)
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("unmarshal manifest for abort: %w", err)
	}

	if manifest.State == TxStateCommitted || manifest.State == TxStateActiveReplaced || manifest.State == TxStateRestarting || manifest.State == TxStateVerifying {
		return fmt.Errorf("cannot abort transaction in state %s: requires rollback", manifest.State)
	}

	manifest.State = TxStateAborted
	_ = writeManifest(txPath, &manifest)

	if s.secrets != nil {
		_ = s.secrets.RollbackTx(context.Background(), txID)
	}

	return os.RemoveAll(txPath)
}

// FinalizePrepared cleans up transaction snapshot directory after multi-component commits succeed.
func (s *Service) FinalizePrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalizePreparedLocked(txID)
}

func (s *Service) finalizePreparedLocked(txID string) error {
	txPath := s.txDir(txID)
	if !dirExists(txPath) {
		return nil
	}

	manifestBytes, err := os.ReadFile(filepath.Join(txPath, "snapshot-manifest.json"))
	if err != nil {
		return fmt.Errorf("read manifest for finalize: %w", err)
	}
	var manifest SnapshotManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("unmarshal manifest for finalize: %w", err)
	}
	if manifest.State != TxStateCommitted {
		return fmt.Errorf("cannot finalize transaction %s in state %s: must be committed", txID, manifest.State)
	}

	return os.RemoveAll(txPath)
}

// RollbackPrepared rolls back a prepared or committed transaction snapshot.
func (s *Service) RollbackPrepared(txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	txPath := s.txDir(txID)
	if !dirExists(txPath) {
		return nil
	}

	manifestBytes, err := os.ReadFile(filepath.Join(txPath, "snapshot-manifest.json"))
	if err != nil {
		return fmt.Errorf("read snapshot manifest: %w", err)
	}

	var manifest SnapshotManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return fmt.Errorf("unmarshal snapshot manifest: %w", err)
	}

	switch manifest.State {
	case TxStatePrepared, TxStateTested, TxStateBackupReady, TxStateAborted:
		manifest.State = TxStateAborted
		_ = writeManifest(txPath, &manifest)
		if s.secrets != nil {
			_ = s.secrets.RollbackTx(context.Background(), txID)
		}
		return os.RemoveAll(txPath)
	}

	if err := s.rollbackLocked(txID, &manifest); err != nil {
		return err
	}
	s.configRev++
	return nil
}

func (s *Service) rollbackLocked(txID string, manifest *SnapshotManifest) error {
	txPath := s.txDir(txID)

	for filename, expectedHash := range manifest.Checksums {
		src := filepath.Join(txPath, filename)
		actualHash, err := sha256File(src)
		if err != nil || actualHash != expectedHash {
			return fmt.Errorf("snapshot corruption in %s: hash mismatch (want %s, got %s)", filename, expectedHash, actualHash)
		}
	}

	var rollbackErrs []error
	if _, ok := manifest.Checksums["xray-server-settings.json.bak"]; ok {
		src := filepath.Join(txPath, "xray-server-settings.json.bak")
		data, err := os.ReadFile(src)
		if err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("read backup settings: %w", err))
		} else if err := storage.AtomicWritePerm(s.cfgPath(), data, 0600); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore settings: %w", err))
		}
	} else {
		_ = os.Remove(s.cfgPath())
	}

	if len(rollbackErrs) > 0 {
		_ = s.stopLocked()
		return errors.Join(rollbackErrs...)
	}

	s.config = manifest.OldConfig
	if manifest.CandidateConfig.OutboundMode != manifest.OldConfig.OutboundMode {
		s.fircRoutingOverride = (manifest.OldConfig.OutboundMode == "router")
	}

	if s.secrets != nil {
		_ = s.secrets.RollbackTx(context.Background(), txID)
	}

	if manifest.WasRunning && s.config.Enabled {
		if err := s.syncDaemonLocked(); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restart awgm-xhttpd daemon during rollback: %w", err))
		}
	} else {
		if s.daemon != nil {
			_ = s.daemon.Stop()
			s.daemon = nil
		}
		s.syncRealityLocked()
	}

	if len(rollbackErrs) > 0 {
		return errors.Join(rollbackErrs...)
	}

	manifest.State = TxStateRolledBack
	_ = writeManifest(txPath, manifest)
	_ = os.RemoveAll(txPath)
	return nil
}

// RecoverPendingTransactions scans transactions dir on startup to recover incomplete states.
func (s *Service) RecoverPendingTransactions() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	txBase := s.transactionsDir()
	if !dirExists(txBase) {
		return nil
	}

	entries, err := os.ReadDir(txBase)
	if err != nil {
		return fmt.Errorf("read transactions dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		txID := entry.Name()
		manifestPath := filepath.Join(txBase, txID, "snapshot-manifest.json")
		if !fileExists(manifestPath) {
			_ = os.RemoveAll(filepath.Join(txBase, txID))
			continue
		}
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var manifest SnapshotManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			continue
		}

		switch manifest.State {
		case TxStatePrepared, TxStateTested, TxStateBackupReady, TxStateAborted:
			manifest.State = TxStateAborted
			_ = writeManifest(filepath.Join(txBase, txID), &manifest)
			if s.secrets != nil {
				_ = s.secrets.RollbackTx(context.Background(), txID)
			}
			_ = os.RemoveAll(filepath.Join(txBase, txID))

		case TxStateActiveReplaced, TxStateRestarting, TxStateVerifying, TxStateRollingBack:
			if err := s.rollbackLocked(txID, &manifest); err != nil {
				s.recoveryRequired = true
				s.recoveryReason = fmt.Sprintf("crash recovery rollback failed for tx %s: %v", txID, err)
			}

		case TxStateCommitted:
			_ = os.RemoveAll(filepath.Join(txBase, txID))

		case TxStateRecoveryRequired:
			s.recoveryRequired = true
			s.recoveryReason = fmt.Sprintf("pending transaction %s in recovery_required state", txID)
		}
	}

	return nil
}

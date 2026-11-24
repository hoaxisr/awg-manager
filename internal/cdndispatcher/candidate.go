package cdndispatcher

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type Candidate struct {
	Enabled bool   `json:"enabled"`
	Config  Config `json:"config"`
}

type TransactionManifest struct {
	TxID             string            `json:"tx_id"`
	CreatedAt        string            `json:"created_at"`
	OldConfig        Config            `json:"old_config"`
	CandidateConfig  Config            `json:"candidate_config"`
	WasRunning       bool              `json:"was_running"`
	CandidateRunning bool              `json:"candidate_running"`
	State            string            `json:"state"` // "prepared", "active", "finalized", "rolled_back"
	Checksums        map[string]string `json:"checksums,omitempty"`
	SchemaVersion    int               `json:"schema_version"`
}

func (d *Dispatcher) transactionsDir() string {
	dir := d.dataDir
	if dir == "" {
		dir = "/opt/etc/awg-manager"
	}
	return filepath.Join(dir, "dispatcher", "tx")
}

func (d *Dispatcher) txDir(txID string) string {
	return filepath.Join(d.transactionsDir(), txID)
}

func isValidDispTxID(txID string) bool {
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

func generateDispTxID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("tx-disp-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("tx-disp-%d-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

func atomicWriteManifest(path string, m *TransactionManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Каталог транзакции — 0700; AtomicWritePerm создал бы его с 0755.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return storage.AtomicWritePerm(path, data, 0600)
}

// PrepareCandidate stages a candidate config into an isolated transaction directory with durable old and new state.
func (d *Dispatcher) PrepareCandidate(txID string, candidate Candidate) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if txID == "" {
		txID = generateDispTxID()
	} else if !isValidDispTxID(txID) {
		return "", fmt.Errorf("invalid transaction id: %s", txID)
	}

	// Normalize defaults
	if candidate.Config.ListenAddr == "" {
		candidate.Config.ListenAddr = ":9009"
	}
	if candidate.Config.XrayTarget == "" {
		candidate.Config.XrayTarget = "http://127.0.0.1:9008"
	}
	if candidate.Config.TgTarget == "" {
		candidate.Config.TgTarget = "http://127.0.0.1:8085"
	}
	if candidate.Config.XrayPathPrefix == "" {
		candidate.Config.XrayPathPrefix = "/cdn-bridge"
	}

	// Validate target URLs and non-self-referential targets fail-closed
	if err := ValidateConfig(candidate.Config); err != nil {
		return "", err
	}

	txPath := d.txDir(txID)
	if err := os.MkdirAll(txPath, 0700); err != nil {
		return "", fmt.Errorf("create tx dir %s: %w", txPath, err)
	}
	_ = os.Chmod(txPath, 0700)

	manifest := &TransactionManifest{
		TxID:             txID,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		OldConfig:        d.cfg,
		CandidateConfig:  candidate.Config,
		WasRunning:       d.running,
		CandidateRunning: candidate.Enabled,
		State:            "prepared",
		SchemaVersion:    1,
	}

	if err := atomicWriteManifest(filepath.Join(txPath, "manifest.json"), manifest); err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("write manifest: %w", err)
	}
	if err := atomicWriteManifest(filepath.Join(txPath, "snapshot-manifest.json"), manifest); err != nil {
		_ = os.RemoveAll(txPath)
		return "", fmt.Errorf("write snapshot manifest: %w", err)
	}

	return txID, nil
}

func readDispManifest(txPath string) (*TransactionManifest, error) {
	manifestPath := filepath.Join(txPath, "manifest.json")
	if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
		manifestPath = filepath.Join(txPath, "snapshot-manifest.json")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest from %s: %w", txPath, err)
	}
	var m TransactionManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshal manifest from %s: %w", txPath, err)
	}
	return &m, nil
}

// CommitPrepared atomically applies the prepared candidate configuration and updates running state.
func (d *Dispatcher) CommitPrepared(txID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	txPath := d.txDir(txID)
	manifest, err := readDispManifest(txPath)
	if err != nil {
		return err
	}

	if manifest.State == "active" || manifest.State == "committed" {
		return nil
	}

	rollbackAndJoin := func(origErr error) error {
		if rbErr := d.rollbackLocked(txID, manifest); rbErr != nil {
			return errors.Join(origErr, fmt.Errorf("rollback failed: %w", rbErr))
		}
		return origErr
	}

	// Apply candidate configuration
	if err := d.applyConfigLocked(manifest.CandidateConfig, modeReplace); err != nil {
		return rollbackAndJoin(fmt.Errorf("apply candidate config: %w", err))
	}

	// Transition running state
	if manifest.CandidateRunning && !d.running {
		if err := d.startLocked(); err != nil {
			return rollbackAndJoin(fmt.Errorf("start candidate dispatcher: %w", err))
		}
	} else if !manifest.CandidateRunning && d.running {
		if err := d.stopLocked(); err != nil {
			return rollbackAndJoin(fmt.Errorf("stop candidate dispatcher: %w", err))
		}
	}

	manifest.State = "active"
	_ = atomicWriteManifest(filepath.Join(txPath, "manifest.json"), manifest)
	_ = atomicWriteManifest(filepath.Join(txPath, "snapshot-manifest.json"), manifest)

	return nil
}

// RollbackPrepared restores previous dispatcher configuration and running state.
func (d *Dispatcher) RollbackPrepared(txID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.rollbackLocked(txID, nil)
}

func (d *Dispatcher) rollbackLocked(txID string, manifest *TransactionManifest) error {
	txPath := d.txDir(txID)
	if _, err := os.Stat(txPath); os.IsNotExist(err) {
		return nil
	}

	if manifest == nil {
		var err error
		manifest, err = readDispManifest(txPath)
		if err != nil {
			return err
		}
	}

	var rollbackErrs []error

	// Revert configuration
	if err := d.applyConfigLocked(manifest.OldConfig, modeReplace); err != nil {
		rollbackErrs = append(rollbackErrs, fmt.Errorf("revert config: %w", err))
	}

	// Revert running state
	if manifest.WasRunning && !d.running {
		if err := d.startLocked(); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restart dispatcher on rollback: %w", err))
		}
	} else if !manifest.WasRunning && d.running {
		if err := d.stopLocked(); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("stop dispatcher on rollback: %w", err))
		}
	}

	if len(rollbackErrs) > 0 {
		return errors.Join(rollbackErrs...)
	}

	_ = os.RemoveAll(txPath)
	return nil
}

// FinalizePrepared cleans up transaction directory after all multi-component commits succeed.
func (d *Dispatcher) FinalizePrepared(txID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	txPath := d.txDir(txID)
	if _, err := os.Stat(txPath); os.IsNotExist(err) {
		return nil
	}

	manifest, err := readDispManifest(txPath)
	if err != nil {
		return fmt.Errorf("read manifest for finalize: %w", err)
	}
	if manifest.State != "active" && manifest.State != "committed" {
		return fmt.Errorf("cannot finalize transaction %s in state %s: must be active", txID, manifest.State)
	}

	return os.RemoveAll(txPath)
}

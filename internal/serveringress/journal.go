package serveringress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

type JournalPhase string

const (
	CurrentJournalVersion = 2

	PhaseStaged          JournalPhase = "staged"
	PhaseCandidateActive JournalPhase = "candidate_active"
	PhaseCommitting      JournalPhase = "committing"
	PhaseCommitted       JournalPhase = "committed"
	PhaseRollingBack     JournalPhase = "rolling_back"
)

// TransactionJournal tracks cross-component ingress transactions with crash-safety.
type TransactionJournal struct {
	Version                 int               `json:"version"`
	Checksum                string            `json:"checksum"`
	TransactionID           string            `json:"transaction_id"`
	Phase                   JournalPhase      `json:"phase"`
	CreatedAt               string            `json:"created_at"`
	UpdatedAt               string            `json:"updated_at"`
	Desired                 IngressTopology   `json:"desired"`
	Previous                IngressTopology   `json:"previous"`
	Affected                []string          `json:"affected"` // e.g. ["xray", "dispatcher", "tgwebproxy"]
	ComponentTransactionIDs map[string]string `json:"component_tx_ids,omitempty"`
	Fingerprints            map[string]string `json:"fingerprints,omitempty"`
	Error                   string            `json:"error,omitempty"`
}

type canonicalJournalV1 struct {
	Version                 int               `json:"version"`
	TransactionID           string            `json:"transaction_id"`
	Phase                   JournalPhase      `json:"phase"`
	Desired                 IngressTopology   `json:"desired"`
	Previous                IngressTopology   `json:"previous"`
	Affected                []string          `json:"affected"`
	ComponentTransactionIDs map[string]string `json:"component_tx_ids,omitempty"`
	Fingerprints            map[string]string `json:"fingerprints,omitempty"`
	Error                   string            `json:"error,omitempty"`
}

type canonicalJournalV2 struct {
	Version                 int               `json:"version"`
	TransactionID           string            `json:"transaction_id"`
	Phase                   JournalPhase      `json:"phase"`
	Desired                 IngressTopology   `json:"desired"`
	Previous                IngressTopology   `json:"previous"`
	Affected                []string          `json:"affected"`
	ComponentTransactionIDs map[string]string `json:"component_tx_ids,omitempty"`
	Fingerprints            map[string]string `json:"fingerprints,omitempty"`
	Error                   string            `json:"error,omitempty"`
}

func computeJournalChecksumV1(j *TransactionJournal) string {
	cj := canonicalJournalV1{
		Version:                 j.Version,
		TransactionID:           j.TransactionID,
		Phase:                   j.Phase,
		Desired:                 j.Desired,
		Previous:                j.Previous,
		Affected:                j.Affected,
		ComponentTransactionIDs: j.ComponentTransactionIDs,
		Fingerprints:            j.Fingerprints,
		Error:                   j.Error,
	}
	data, _ := json.Marshal(cj)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func computeJournalChecksumV2(j *TransactionJournal) string {
	cj := canonicalJournalV2{
		Version:                 j.Version,
		TransactionID:           j.TransactionID,
		Phase:                   j.Phase,
		Desired:                 j.Desired,
		Previous:                j.Previous,
		Affected:                j.Affected,
		ComponentTransactionIDs: j.ComponentTransactionIDs,
		Fingerprints:            j.Fingerprints,
		Error:                   j.Error,
	}
	data, _ := json.Marshal(cj)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func computeJournalChecksum(j *TransactionJournal) string {
	if j.Version == 1 {
		return computeJournalChecksumV1(j)
	}
	return computeJournalChecksumV2(j)
}

func migrateTopologyV1toV2(t *IngressTopology) {
	if t.XrayPublicHostname == "" && t.PublicHostname != "" {
		t.XrayPublicHostname = t.PublicHostname
	}
	if t.TgPublicHostname == "" && t.PublicHostname != "" {
		t.TgPublicHostname = t.PublicHostname
	}
	if t.TgWebPort <= 0 && t.TgPort > 0 {
		t.TgWebPort = t.TgPort
	}
	if t.TgScenario == "" && t.TgEnabled {
		t.TgScenario = "cdn_http"
	}
}

func readJournal(path string) (*TransactionJournal, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read journal: %w", err)
	}

	var j TransactionJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, ErrCorruptJournal
	}

	// Structural & schema validation
	if j.Version != 1 && j.Version != 2 {
		return nil, ErrCorruptJournal
	}
	if j.TransactionID == "" {
		return nil, ErrCorruptJournal
	}
	switch j.Phase {
	case PhaseStaged, PhaseCandidateActive, PhaseCommitting, PhaseCommitted, PhaseRollingBack:
	default:
		return nil, ErrCorruptJournal
	}
	if len(j.Affected) == 0 {
		return nil, ErrCorruptJournal
	}

	// Checksum validation with version-specific canonical form
	if j.Version == 1 {
		expected := computeJournalChecksumV1(&j)
		if j.Checksum == "" || j.Checksum != expected {
			return nil, ErrCorruptJournal
		}
		// Migrate V1 journal to V2 in memory
		migrateTopologyV1toV2(&j.Desired)
		migrateTopologyV1toV2(&j.Previous)
		j.Version = 2
		j.Checksum = computeJournalChecksumV2(&j)
	} else {
		expected := computeJournalChecksumV2(&j)
		if j.Checksum == "" || j.Checksum != expected {
			return nil, ErrCorruptJournal
		}
	}

	return &j, nil
}

func writeJournal(path string, j *TransactionJournal) error {
	j.Version = 2
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if j.CreatedAt == "" {
		j.CreatedAt = j.UpdatedAt
	}
	j.Checksum = computeJournalChecksumV2(j)

	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal journal: %w", err)
	}

	// Каталог создаём сами: AtomicWritePerm создал бы его с 0755, а журнал
	// держится в 0700.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir journal dir: %w", err)
	}
	if err := storage.AtomicWritePerm(path, data, 0600); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	return nil
}

func archiveJournal(journalPath string, suffix string, txID string) error {
	if _, err := os.Stat(journalPath); os.IsNotExist(err) {
		return nil
	}
	archivePath := fmt.Sprintf("%s.%s.%s", journalPath, suffix, txID)
	// Архив никто не читает — он для разбора последнего сбоя. Хранить по файлу
	// на каждую транзакцию значило бы копить их на флеше без предела.
	for _, kind := range []string{"committed", "failed"} {
		if old, err := filepath.Glob(journalPath + "." + kind + ".*"); err == nil {
			for _, o := range old {
				_ = os.Remove(o)
			}
		}
	}
	if err := os.Rename(journalPath, archivePath); err != nil {
		return fmt.Errorf("archive journal to %s: %w", archivePath, err)
	}
	if d, err := os.Open(filepath.Dir(journalPath)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

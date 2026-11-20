package vlessserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SecretRef identifies a stored secret without exposing its cleartext value.
type SecretRef struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Version int    `json:"version,omitempty"`
}

// StoredSecret holds secret payload and its metadata on disk.
type StoredSecret struct {
	Ref       SecretRef `json:"ref"`
	Value     string    `json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SecretStore provides secure storage, resolution and transactional staging of secrets.
type SecretStore interface {
	StageSecret(ctx context.Context, txID string, secretType string, name string, value string) (SecretRef, error)
	ResolveSecret(ctx context.Context, ref SecretRef) (string, error)
	CommitTx(ctx context.Context, txID string) error
	RollbackTx(ctx context.Context, txID string) error
	ListSecrets(ctx context.Context) ([]SecretRef, error)
	DeleteSecret(ctx context.Context, id string) error
}

// DiskSecretStore stores secrets secured by filesystem permissions (0700/0600)
// and strict path isolation without deleting existing user secrets.
type DiskSecretStore struct {
	mu      sync.RWMutex
	rootDir string
}

func NewDiskSecretStore(rootDir string) (*DiskSecretStore, error) {
	if rootDir == "" {
		return nil, fmt.Errorf("root directory cannot be empty")
	}
	cleanRoot := filepath.Clean(rootDir)
	if err := os.MkdirAll(cleanRoot, 0700); err != nil {
		return nil, fmt.Errorf("create secret store root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cleanRoot, "active"), 0700); err != nil {
		return nil, fmt.Errorf("create active secrets dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(cleanRoot, "staging"), 0700); err != nil {
		return nil, fmt.Errorf("create staging secrets dir: %w", err)
	}
	return &DiskSecretStore{rootDir: cleanRoot}, nil
}

func validateID(id string) error {
	if id == "" || len(id) > 128 {
		return fmt.Errorf("invalid identifier length: %d", len(id))
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("invalid character in identifier %q: only alphanumeric, '-', '_', '.' allowed", id)
		}
	}
	return nil
}

func (s *DiskSecretStore) activeDir() string {
	return filepath.Join(s.rootDir, "active")
}

func (s *DiskSecretStore) stagingDir(txID string) string {
	return filepath.Join(s.rootDir, "staging", txID)
}

func generateSecretID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("sec-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("sec-%s", hex.EncodeToString(b))
}

func (s *DiskSecretStore) StageSecret(ctx context.Context, txID string, secretType string, name string, value string) (SecretRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateID(txID); err != nil {
		return SecretRef{}, fmt.Errorf("invalid txID: %w", err)
	}
	if value == "" {
		return SecretRef{}, fmt.Errorf("secret value cannot be empty")
	}
	txStagingDir := s.stagingDir(txID)
	if err := os.MkdirAll(txStagingDir, 0700); err != nil {
		return SecretRef{}, fmt.Errorf("create tx staging dir: %w", err)
	}
	secID := generateSecretID()
	ref := SecretRef{
		ID:      secID,
		Type:    secretType,
		Name:    name,
		Version: 1,
	}
	stored := StoredSecret{
		Ref:       ref,
		Value:     value,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return SecretRef{}, fmt.Errorf("marshal secret: %w", err)
	}
	if err := atomicWriteFile(filepath.Join(txStagingDir, secID+".json"), data, 0600); err != nil {
		return SecretRef{}, fmt.Errorf("write staged secret: %w", err)
	}
	return ref, nil
}

func (s *DiskSecretStore) ResolveSecret(ctx context.Context, ref SecretRef) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := validateID(ref.ID); err != nil {
		return "", fmt.Errorf("invalid secret ref ID: %w", err)
	}
	activePath := filepath.Join(s.activeDir(), ref.ID+".json")
	if !fileExists(activePath) {
		return "", fmt.Errorf("secret %q not found", ref.ID)
	}
	fi, err := os.Lstat(activePath)
	if err != nil {
		return "", fmt.Errorf("stat secret file: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("security violation: secret file cannot be a symlink")
	}
	data, err := os.ReadFile(activePath)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	var stored StoredSecret
	if err := json.Unmarshal(data, &stored); err != nil {
		return "", fmt.Errorf("unmarshal stored secret: %w", err)
	}
	return stored.Value, nil
}

func (s *DiskSecretStore) CommitTx(ctx context.Context, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateID(txID); err != nil {
		return fmt.Errorf("invalid txID: %w", err)
	}
	txStagingDir := s.stagingDir(txID)
	if !dirExists(txStagingDir) {
		return nil
	}
	entries, err := os.ReadDir(txStagingDir)
	if err != nil {
		return fmt.Errorf("read tx staging dir: %w", err)
	}
	activeDir := s.activeDir()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		src := filepath.Join(txStagingDir, entry.Name())
		dst := filepath.Join(activeDir, entry.Name())
		if err := os.Rename(src, dst); err != nil {
			data, readErr := os.ReadFile(src)
			if readErr != nil {
				return fmt.Errorf("fallback read secret %s: %w", entry.Name(), readErr)
			}
			if writeErr := atomicWriteFile(dst, data, 0600); writeErr != nil {
				return fmt.Errorf("fallback write secret %s: %w", entry.Name(), writeErr)
			}
			_ = os.Remove(src)
		}
	}
	if dirFile, err := os.Open(activeDir); err == nil {
		_ = dirFile.Sync()
		_ = dirFile.Close()
	}
	return os.RemoveAll(txStagingDir)
}

func (s *DiskSecretStore) RollbackTx(ctx context.Context, txID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateID(txID); err != nil {
		return fmt.Errorf("invalid txID: %w", err)
	}
	return os.RemoveAll(s.stagingDir(txID))
}

func (s *DiskSecretStore) ListSecrets(ctx context.Context) ([]SecretRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.activeDir())
	if err != nil {
		return nil, fmt.Errorf("read active secrets dir: %w", err)
	}
	results := make([]SecretRef, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.activeDir(), entry.Name()))
		if err != nil {
			continue
		}
		var stored StoredSecret
		if err := json.Unmarshal(data, &stored); err != nil {
			continue
		}
		results = append(results, stored.Ref)
	}
	return results, nil
}

func (s *DiskSecretStore) DeleteSecret(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateID(id); err != nil {
		return fmt.Errorf("invalid secret ID: %w", err)
	}
	activePath := filepath.Join(s.activeDir(), id+".json")
	if fileExists(activePath) {
		return os.Remove(activePath)
	}
	return nil
}

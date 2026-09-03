package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// McpKeyPrefix marks MCP bearer keys so a leaked value is recognisable.
const McpKeyPrefix = "awgm_"

const (
	mcpKeysFile      = "mcp_keys.json"
	mcpKeyNameMaxLen = 64
	// mcpTouchInterval bounds how often LastUsedAt is persisted per key —
	// every tool call would otherwise rewrite flash.
	mcpTouchInterval = time.Minute
)

// ErrMcpKeyNotFound is returned by Revoke for an unknown id.
var ErrMcpKeyNotFound = errors.New("mcp key not found")

// ErrMcpKeyInvalidName is wrapped into Create's error when the name fails
// validation, so callers can distinguish a bad request (400) from an
// infrastructure failure — a crypto/rand read or a persistence error — that
// must surface as 500 instead.
var ErrMcpKeyInvalidName = errors.New("invalid mcp key name")

// McpKey is one named bearer key for the /mcp endpoint. Only the SHA-256
// of the plaintext is stored; the plaintext is shown once at creation.
type McpKey struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Hash       string    `json:"hash"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
}

type mcpKeysFileV1 struct {
	// Version is reserved for a future on-disk format change. It is
	// written on every save but intentionally not validated on read
	// today — there is only one format so far.
	Version int      `json:"version"`
	Keys    []McpKey `json:"keys"`
}

// McpKeyStore persists MCP keys in <dataDir>/mcp_keys.json (mode 0600).
// It is separate from settings.json so hashes never travel with
// /settings/get responses; the data-dir backup still includes it.
type McpKeyStore struct {
	dataDir string
	mu      sync.RWMutex
	keys    []McpKey
	now     func() time.Time
}

// NewMcpKeyStore creates a store rooted at dataDir. Call Load before use.
func NewMcpKeyStore(dataDir string) *McpKeyStore {
	return &McpKeyStore{dataDir: dataDir, now: time.Now}
}

func (s *McpKeyStore) path() string { return filepath.Join(s.dataDir, mcpKeysFile) }

// Load reads the file; a missing file means no keys.
func (s *McpKeyStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			s.keys = nil
			return nil
		}
		return err
	}
	var f mcpKeysFileV1
	if err := json.Unmarshal(data, &f); err != nil {
		QuarantineCorrupt(s.path(), err)
		s.keys = nil
		return nil
	}
	s.keys = f.Keys
	return nil
}

func (s *McpKeyStore) saveLocked() error {
	data, err := json.MarshalIndent(mcpKeysFileV1{Version: 1, Keys: s.keys}, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWritePerm(s.path(), data, 0o600)
}

// List returns keys sorted by creation time with Hash blanked.
func (s *McpKeyStore) List() []McpKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]McpKey, len(s.keys))
	copy(out, s.keys)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	for i := range out {
		out[i].Hash = ""
	}
	return out
}

func hashMcpKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Create mints a new key. The returned plaintext is never stored.
func (s *McpKeyStore) Create(name string) (McpKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return McpKey{}, "", fmt.Errorf("%w: key name is required", ErrMcpKeyInvalidName)
	}
	if utf8.RuneCountInString(name) > mcpKeyNameMaxLen {
		return McpKey{}, "", fmt.Errorf("%w: key name longer than %d characters", ErrMcpKeyInvalidName, mcpKeyNameMaxLen)
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return McpKey{}, "", err
	}
	var idBytes [8]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return McpKey{}, "", err
	}
	plaintext := McpKeyPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	key := McpKey{
		ID:        hex.EncodeToString(idBytes[:]),
		Name:      name,
		Hash:      hashMcpKey(plaintext),
		CreatedAt: s.now().UTC(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	if err := s.saveLocked(); err != nil {
		s.keys = s.keys[:len(s.keys)-1]
		return McpKey{}, "", err
	}
	return key, plaintext, nil
}

// Revoke deletes a key by id.
func (s *McpKeyStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, k := range s.keys {
		if k.ID == id {
			before := s.keys
			s.keys = append(s.keys[:i:i], s.keys[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.keys = before
				return err
			}
			return nil
		}
	}
	return ErrMcpKeyNotFound
}

// Verify checks a presented plaintext against every stored hash in
// constant time per comparison and returns the matching key.
func (s *McpKeyStore) Verify(plaintext string) (McpKey, bool) {
	if !strings.HasPrefix(plaintext, McpKeyPrefix) {
		return McpKey{}, false
	}
	want := []byte(hashMcpKey(plaintext))
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found *McpKey
	for i := range s.keys {
		if subtle.ConstantTimeCompare(want, []byte(s.keys[i].Hash)) == 1 {
			found = &s.keys[i]
		}
	}
	if found == nil {
		return McpKey{}, false
	}
	return *found, true
}

// Touch records use of a key, persisting at most once per mcpTouchInterval.
func (s *McpKeyStore) Touch(id string) {
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.keys {
		if s.keys[i].ID != id {
			continue
		}
		if !s.keys[i].LastUsedAt.IsZero() && now.Sub(s.keys[i].LastUsedAt) < mcpTouchInterval {
			return
		}
		s.keys[i].LastUsedAt = now
		_ = s.saveLocked() // best effort: a failed touch must not break the request
		return
	}
}

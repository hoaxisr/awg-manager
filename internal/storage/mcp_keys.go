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
	"unicode"
	"unicode/utf8"
)

// McpKeyPrefix marks MCP bearer keys so a leaked value is recognisable.
const McpKeyPrefix = "awgm_"

const (
	mcpKeysFile      = "mcp_keys.json"
	mcpKeyNameMaxLen = 64
	// mcpTouchInterval bounds how often LastUsedAt is persisted per key.
	// Every tool call would otherwise rewrite flash, and even once a minute
	// is a full mcp_keys.json + .bak rewrite per active key — too much for
	// a router's NAND for a field that only says "last seen around then".
	mcpTouchInterval = time.Hour
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

const mcpKeysFileVersion = 1

type mcpKeysFileV1 struct {
	// Version is the on-disk format. 0 (absent) and 1 are the current
	// format; anything newer is refused on read — see Load.
	Version int      `json:"version"`
	Keys    []McpKey `json:"keys"`
}

// ErrMcpKeysFileVersion is the loadErr set when the file was written by a
// newer build. It is not corruption, so the file is neither quarantined nor
// overwritten: the store goes read-only until the binary is upgraded.
var ErrMcpKeysFileVersion = errors.New("mcp keys file written by a newer version")

// ErrMcpKeyStoreReadOnly is wrapped into every write error raised while the
// store is in read-only mode after a failed Load. See McpKeyStore.loadErr.
var ErrMcpKeyStoreReadOnly = errors.New("mcp key store unavailable")

// McpKeyStore persists MCP keys in <dataDir>/mcp_keys.json (mode 0600).
// It is separate from settings.json so hashes never travel with
// /settings/get responses; the data-dir backup still includes it.
type McpKeyStore struct {
	dataDir string
	mu      sync.RWMutex
	keys    []McpKey
	// loadErr, when non-nil, means the last Load could not read an existing
	// file (EIO, a permissions change, a short read on flash) and the
	// in-memory list is therefore NOT the file's contents. Every write is
	// refused while it is set: saving an empty (or partial) list would
	// rename it over the real file and silently revoke every issued key.
	// Verify keeps working against the empty list — failing closed is the
	// right direction for authentication.
	loadErr error
	// fileMu serialises the file rewrite AND orders it against the snapshot
	// it was taken from. Lock order is always mu → fileMu: every writer
	// acquires fileMu while still holding mu, so a snapshot can never be
	// overtaken by a newer one and then written on top of it. Touch is the
	// only caller that releases mu before writing (that is the whole point
	// — an fsync must not block Verify), which is exactly why it must take
	// fileMu BEFORE letting mu go.
	fileMu sync.Mutex
	now    func() time.Time
	// loaded is set by Load. A store that was never loaded has an empty
	// in-memory list that says nothing about the file, so every write is
	// refused exactly as after a failed load: saving would rename a
	// one-key list over the real file. Turns a wiring mistake into an
	// error instead of silent key loss.
	loaded bool
	// afterTouchUnlock, when non-nil, runs inside Touch after mu is released
	// and before the write, with fileMu held. Test hook only; nil in
	// production. It exists so the mu/fileMu ordering can be exercised
	// deterministically instead of by timing.
	afterTouchUnlock func()
}

// NewMcpKeyStore creates a store rooted at dataDir. Call Load before use.
func NewMcpKeyStore(dataDir string) *McpKeyStore {
	return &McpKeyStore{dataDir: dataDir, now: time.Now}
}

func (s *McpKeyStore) path() string { return filepath.Join(s.dataDir, mcpKeysFile) }

// Load reads the file; a missing file means no keys. Any OTHER read failure
// leaves the store read-only (see loadErr) instead of pretending the router
// has no keys.
func (s *McpKeyStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = true
	data, err := os.ReadFile(s.path())
	if err != nil {
		if os.IsNotExist(err) {
			s.keys, s.loadErr = nil, nil
			return nil
		}
		s.keys, s.loadErr = nil, err
		recordNotice("read-only", mcpKeysFile, fmt.Sprintf(
			"Файл %s не читается (%v): MCP не принимает ключи, а создание и отзыв ключей отключены, пока файл не станет доступен.", mcpKeysFile, err))
		return err
	}
	f, err := decodeMcpKeysFile(data)
	if err != nil {
		if errors.Is(err, ErrMcpKeysFileVersion) {
			// A downgrade after a format change: the keys may not decode
			// faithfully, and saving them back would destroy fields the
			// newer format relies on. Same read-only treatment as an
			// unreadable file — Verify fails closed, every write is refused.
			s.keys, s.loadErr = nil, err
			recordNotice("read-only", mcpKeysFile, fmt.Sprintf(
				"Файл %s записан более новой версией awg-manager (%v): MCP не принимает ключи, создание и отзыв отключены до обновления.", mcpKeysFile, err))
			return err
		}
		// A corrupt main file (typically a torn write after power loss) is
		// quarantined, and the previous good copy that persistFileLocked
		// keeps as .bak takes its place — the same recovery settings.json
		// makes. Without this every issued key would be gone while a good
		// copy sat next to the file.
		QuarantineCorrupt(s.path(), err)
		keys, restored := s.restoreFromBackup(err)
		s.keys, s.loadErr = keys, nil
		if !restored {
			return nil
		}
		// The main file is gone (renamed to .corrupt); write the restored
		// list back now, or the next boot would start empty again. A failed
		// write leaves the store read-only with the restored keys still
		// honoured by Verify — the safe side.
		if err := s.saveLocked(); err != nil {
			s.loadErr = err
			return err
		}
		return nil
	}
	s.keys, s.loadErr = f.Keys, nil
	return nil
}

// decodeMcpKeysFile parses one on-disk file. A newer format is an error
// wrapping ErrMcpKeysFileVersion so Load can tell it from corruption.
func decodeMcpKeysFile(data []byte) (mcpKeysFileV1, error) {
	var f mcpKeysFileV1
	if err := json.Unmarshal(data, &f); err != nil {
		return f, err
	}
	if f.Version > mcpKeysFileVersion {
		return f, fmt.Errorf("%w: file version %d, this build reads %d", ErrMcpKeysFileVersion, f.Version, mcpKeysFileVersion)
	}
	return f, nil
}

// restoreFromBackup reads <file>.bak after the main file was quarantined
// for parseErr. It reports the outcome to the app journal either way: the
// user must know whether their keys survived.
func (s *McpKeyStore) restoreFromBackup(parseErr error) ([]McpKey, bool) {
	bak := s.path() + ".bak"
	data, err := os.ReadFile(bak)
	if err != nil {
		recordNotice("quarantine", mcpKeysFile, fmt.Sprintf(
			"Файл %s повреждён (%v), резервной копии нет: все ключи MCP потеряны, создайте их заново.", mcpKeysFile, parseErr))
		return nil, false
	}
	f, err := decodeMcpKeysFile(data)
	if err != nil {
		recordNotice("quarantine", mcpKeysFile, fmt.Sprintf(
			"Файл %s повреждён (%v), резервная копия тоже не читается (%v): все ключи MCP потеряны, создайте их заново.", mcpKeysFile, parseErr, err))
		return nil, false
	}
	recordNotice("backup-restore", mcpKeysFile, fmt.Sprintf(
		"Файл %s был повреждён (%v) и ВОССТАНОВЛЕН из резервной копии (%d ключей). Ключ, созданный последним, мог не сохраниться.", mcpKeysFile, parseErr, len(f.Keys)))
	return f.Keys, true
}

// writableLocked reports why a write must be refused, or nil.
func (s *McpKeyStore) writableLocked() error {
	if !s.loaded {
		return fmt.Errorf("%w: Load was never called", ErrMcpKeyStoreReadOnly)
	}
	if s.loadErr != nil {
		return fmt.Errorf("%w: refusing to write after load failure: %v", ErrMcpKeyStoreReadOnly, s.loadErr)
	}
	return nil
}

// validateKeyName normalises and checks a key name. Validation runs before
// any store-state check so a bad request is a 400 even when the store is
// read-only. Control characters are rejected: the name is echoed into
// every MCP call's journal line, and a newline inside it would forge
// extra entries in a plain-text export.
func validateKeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: key name is required", ErrMcpKeyInvalidName)
	}
	if utf8.RuneCountInString(name) > mcpKeyNameMaxLen {
		return "", fmt.Errorf("%w: key name longer than %d characters", ErrMcpKeyInvalidName, mcpKeyNameMaxLen)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", fmt.Errorf("%w: key name contains a control character", ErrMcpKeyInvalidName)
		}
	}
	return name, nil
}

// persist writes snapshot to disk, taking fileMu itself.
func (s *McpKeyStore) persist(snapshot []McpKey) error {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	return s.persistFileLocked(snapshot)
}

// persistFileLocked writes snapshot to disk. The caller must already hold
// fileMu — that lock is what keeps a write ordered against the snapshot it
// came from, so it may not be acquired here.
func (s *McpKeyStore) persistFileLocked(snapshot []McpKey) error {
	data, err := json.MarshalIndent(mcpKeysFileV1{Version: mcpKeysFileVersion, Keys: snapshot}, "", "  ")
	if err != nil {
		return err
	}
	// Keep the previous good file as .bak (hardlink: no data copy, the old
	// inode survives the rename below) — the same cheap insurance
	// settings.json takes against a bad flash write.
	if _, statErr := os.Stat(s.path()); statErr == nil {
		bak := s.path() + ".bak"
		_ = os.Remove(bak)
		_ = os.Link(s.path(), bak)
	}
	return AtomicWritePerm(s.path(), data, 0o600)
}

func (s *McpKeyStore) saveLocked() error {
	if err := s.writableLocked(); err != nil {
		return err
	}
	return s.persist(append([]McpKey(nil), s.keys...))
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
	name, err := validateKeyName(name)
	if err != nil {
		return McpKey{}, "", err
	}
	// Checked up front so a read-only store answers before any entropy is
	// spent; saveLocked below is the gate that actually matters.
	s.mu.RLock()
	roErr := s.writableLocked()
	s.mu.RUnlock()
	if roErr != nil {
		return McpKey{}, "", roErr
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
	// Checked before the lookup: with the file unreadable the in-memory list
	// is empty, and "key not found" would be a misleading answer to
	// "revoke this key" — the key may well still be in the file.
	if err := s.writableLocked(); err != nil {
		return err
	}
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
// LastUsedAt is therefore coarse on purpose: it answers "is this key still
// in use", not "when was the last call".
//
// The timestamp is applied and the key list snapshotted under the write
// lock; the fsync'd rewrite then runs WITHOUT it. Holding mu across the
// write would block Verify — i.e. every concurrent MCP request — for the
// duration of a flash write. Two touches racing means last-writer-wins on
// a LastUsedAt field, which is not worth a lock for.
//
// fileMu is acquired BEFORE mu is released, and released only after the
// write. Dropping both and taking fileMu later would serialise the writes
// but not order them against the snapshots: a Revoke that ran in the gap
// would persist its own list and return success, and this older snapshot
// would then put the revoked key straight back into the file (and,
// symmetrically, erase a key Create had just handed out). Holding fileMu
// across the gap makes any such mutator block until this write has landed,
// so it can only ever persist a state that already includes it. Verify
// takes mu.RLock only and is untouched throughout.
func (s *McpKeyStore) Touch(id string) {
	now := s.now().UTC()
	s.mu.Lock()
	if s.writableLocked() != nil {
		// Read-only (failed or missing Load): persisting the in-memory list
		// here would wipe the file just like Create would.
		s.mu.Unlock()
		return
	}
	var snapshot []McpKey
	for i := range s.keys {
		if s.keys[i].ID != id {
			continue
		}
		if !s.keys[i].LastUsedAt.IsZero() && now.Sub(s.keys[i].LastUsedAt) < mcpTouchInterval {
			s.mu.Unlock()
			return
		}
		s.keys[i].LastUsedAt = now
		snapshot = append([]McpKey(nil), s.keys...)
		break
	}
	if snapshot == nil {
		s.mu.Unlock()
		return
	}
	hook := s.afterTouchUnlock // read under mu so -race sees no unguarded access
	s.fileMu.Lock()            // ordering: taken while mu is still held (mu → fileMu)
	s.mu.Unlock()
	defer s.fileMu.Unlock()
	if hook != nil {
		hook()
	}
	// Best effort: a failed touch must not break the request — auth is
	// already decided in memory — but it must not vanish either. A USB
	// stick that went read-only shows up here first, and the throttle
	// above keeps this to one notice per key per mcpTouchInterval.
	if err := s.persistFileLocked(snapshot); err != nil {
		recordNotice("write-failed", mcpKeysFile, fmt.Sprintf(
			"Не удалось записать %s (%v): время последнего использования ключей не сохраняется; создание и отзыв ключей, скорее всего, тоже не сработают.", mcpKeysFile, err))
	}
}

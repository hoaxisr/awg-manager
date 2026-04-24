package deviceproxy

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store persists a single Config to disk as JSON. Thread-safe: all
// access goes through the embedded mutex; callers see a defensive copy.
type Store struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

// NewStore returns a Store backed by path. The file is loaded eagerly;
// a missing or corrupt file yields defaultConfig().
func NewStore(path string) *Store {
	s := &Store{path: path}
	s.load()
	return s
}

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Save writes cfg to disk atomically and updates the in-memory copy.
// Holds the write lock for the full operation so disk and in-memory
// state stay in lock-step across concurrent callers.
func (s *Store) Save(cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := storage.AtomicWrite(s.path, raw); err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

// load reads data from disk. On missing or corrupt file, initializes
// default config. No error is returned — caller sees defaultConfig().
// Caller must NOT hold the lock.
func (s *Store) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.cfg = defaultConfig()
		return
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		s.cfg = defaultConfig()
		return
	}
	s.cfg = cfg
}

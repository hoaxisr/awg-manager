package presets

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Overlay is the on-disk, user-editable layer applied over the builtin defaults.
type Overlay struct {
	DisabledBuiltins []string `json:"disabledBuiltins,omitempty"` // builtin ids to hide
	Presets          []Preset `json:"presets,omitempty"`          // custom presets, or overrides by matching id
}

// Store persists the user overlay as presets.user.json in the data dir.
type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "presets.user.json")}
}

// Load returns the overlay, or an empty overlay if the file does not exist.
func (s *Store) Load() (*Overlay, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Overlay{}, nil
		}
		return nil, fmt.Errorf("read presets overlay: %w", err)
	}
	var o Overlay
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("parse presets overlay: %w", err)
	}
	return &o, nil
}

// Save atomically writes the overlay. (No caller in U0; used by U3 CRUD.)
func (s *Store) Save(o *Overlay) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal presets overlay: %w", err)
	}
	if err := storage.AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write presets overlay: %w", err)
	}
	return nil
}

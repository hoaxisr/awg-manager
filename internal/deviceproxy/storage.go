package deviceproxy

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store persists a Snapshot of Instances to disk as JSON. Thread-safe: all
// access goes through the embedded mutex; callers see a defensive copy via Get().
type Store struct {
	path     string
	mu       sync.RWMutex
	snapshot Snapshot
}

// NewStore returns a Store backed by path. The file is loaded eagerly;
// a missing or corrupt file yields defaultConfig().
func NewStore(path string) *Store {
	s := &Store{path: path}
	s.load()
	return s
}

// Get returns a copy of the "default" instance as legacy Config.
func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, in := range s.snapshot.Instances {
		if in.ID == "default" {
			return instanceToConfig(in)
		}
	}
	return defaultConfig()
}

// Save writes cfg to disk atomically by updating the "default" instance
// in the snapshot and persisting the full snapshot.
func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	found := false
	for i, in := range s.snapshot.Instances {
		if in.ID == "default" {
			next := configToDefaultInstance(cfg)
			if in.Name != "" {
				next.Name = in.Name
			}
			s.snapshot.Instances[i] = next
			found = true
			break
		}
	}
	if !found {
		s.snapshot.Instances = append(s.snapshot.Instances, configToDefaultInstance(cfg))
	}

	return s.saveLocked()
}

// Snapshot returns a defensive copy of all proxy instances.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := Snapshot{
		Instances: append([]Instance(nil), s.snapshot.Instances...),
	}
	return out
}

// GetInstance returns one proxy instance by id.
func (s *Store) GetInstance(id string) (Instance, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, in := range s.snapshot.Instances {
		if in.ID == id {
			return in, true
		}
	}
	return Instance{}, false
}

// SaveInstance inserts or replaces one proxy instance by id.
func (s *Store) SaveInstance(in Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.snapshot.Instances {
		if existing.ID == in.ID {
			s.snapshot.Instances[i] = in
			return s.saveLocked()
		}
	}

	s.snapshot.Instances = append(s.snapshot.Instances, in)
	return s.saveLocked()
}

// DeleteInstance removes one proxy instance by id.
func (s *Store) DeleteInstance(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Instance, 0, len(s.snapshot.Instances))
	for _, in := range s.snapshot.Instances {
		if in.ID == id {
			continue
		}
		out = append(out, in)
	}
	s.snapshot.Instances = out
	return s.saveLocked()
}

// saveLocked marshals the current snapshot and writes it atomically.
// Caller must hold s.mu.
func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.snapshot, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(s.path, raw)
}

// load reads data from disk. On missing or corrupt file, initializes
// a default snapshot with one "default" instance. Supports both the
// legacy single-Config format and the new Snapshot format.
// Caller must NOT hold the lock.
func (s *Store) load() {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.snapshot = Snapshot{Instances: []Instance{defaultInstance()}}
		return
	}

	// Новый формат распознаётся по наличию ключа "instances" (указатель),
	// а не по len>0: файл с пустым списком — результат удаления всех
	// инстансов, а не legacy-конфиг, иначе load() воскрешал бы удалённый
	// default из нулевого Config.
	var snap struct {
		Instances *[]Instance `json:"instances"`
	}
	if err := json.Unmarshal(raw, &snap); err == nil && snap.Instances != nil {
		s.snapshot = Snapshot{Instances: *snap.Instances}
		return
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err == nil {
		s.snapshot = Snapshot{Instances: []Instance{configToDefaultInstance(cfg)}}
		return
	}

	s.snapshot = Snapshot{Instances: []Instance{defaultInstance()}}
}

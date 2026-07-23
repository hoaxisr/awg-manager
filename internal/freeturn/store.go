package freeturn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store persists FreeTurn configuration to <dataDir>/freeturn.json.
//
// Deliberately its own file rather than a field on storage.Settings: that
// struct carries a schema-version migration chain (see
// internal/storage/settings.go), and a self-contained side-feature like
// this one doesn't need to participate in it.
type Store struct {
	path string

	mu  sync.RWMutex
	cfg *Config
}

func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "freeturn.json")}
}

// Load returns the cached config if already loaded, otherwise reads it
// from disk (writing out DefaultConfig() the first time the file doesn't
// exist yet, same pattern as storage.SettingsStore.Load).
func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cfg != nil {
		// Изолируем срезы от кэша: shallow-copy делит backing array,
		// normalizeConfig и мутации хендлеров иначе меняют кэш до Save.
		cfg := copyConfig(*s.cfg)
		if changed := normalizeConfig(&cfg); changed || len(cfg.Clients) != len(s.cfg.Clients) || len(cfg.Servers) != len(s.cfg.Servers) {
			if err := s.saveLocked(cfg); err != nil {
				return cfg, err
			}
			return cfg, nil
		}
		return cfg, nil
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if saveErr := s.saveLocked(cfg); saveErr != nil {
				return cfg, saveErr
			}
			return copyConfig(cfg), nil
		}
		return Config{}, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if changed := normalizeConfig(&cfg); changed {
		if err := s.saveLocked(cfg); err != nil {
			return cfg, err
		}
	}
	s.cfg = &cfg
	return copyConfig(cfg), nil
}

// copyConfig возвращает копию cfg, у которой срезы Clients и Servers лежат в
// свежих backing array — чтобы мутации возвращённого значения не протекали в
// кэш s.cfg (и наоборот). Применяется во всех точках возврата Load.
func copyConfig(cfg Config) Config {
	if cfg.Clients != nil {
		cfg.Clients = append([]ClientInstance(nil), cfg.Clients...)
	}
	if cfg.Servers != nil {
		cfg.Servers = append([]ServerInstance(nil), cfg.Servers...)
	}
	return cfg
}

func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(cfg)
}

// saveLocked writes via a temp file + rename so a crash mid-write can't
// leave freeturn.json truncated/corrupt.
func (s *Store) saveLocked(cfg Config) error {
	normalizeConfig(&cfg)
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.cfg = &cfg
	return nil
}

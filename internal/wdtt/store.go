package wdtt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Store struct {
	path string
	mu   sync.RWMutex
	cfg  *Config
}

func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "wdtt.json")}
}

func (s *Store) Load() (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg != nil {
		return *s.cfg, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			if saveErr := s.saveLocked(cfg); saveErr != nil {
				return cfg, saveErr
			}
			return cfg, nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	normalizeConfig(&cfg)
	s.cfg = &cfg
	return cfg, nil
}

func (s *Store) Save(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(cfg)
}

func (s *Store) saveLocked(cfg Config) error {
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

func normalizeConfig(cfg *Config) {
	if cfg.Version == 0 {
		cfg.Version = ConfigVersion
	}
	if len(cfg.Clients) == 0 {
		cfg.Clients = DefaultConfig().Clients
	}
}

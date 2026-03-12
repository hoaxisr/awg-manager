package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// StaticRouteList represents a named list of IP subnets routed through a tunnel.
type StaticRouteList struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	TunnelID  string   `json:"tunnelID"`
	Subnets   []string `json:"subnets"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

// StaticRouteData is the top-level static-routes.json structure.
type StaticRouteData struct {
	RouteLists []StaticRouteList `json:"routeLists"`
}

// StaticRouteStore manages static route lists storage.
type StaticRouteStore struct {
	path string
	mu   sync.RWMutex
	data *StaticRouteData
}

// NewStaticRouteStore creates a new static route store.
func NewStaticRouteStore(dataDir string) *StaticRouteStore {
	return &StaticRouteStore{
		path: filepath.Join(dataDir, "static-routes.json"),
	}
}

// Load reads static routes from disk. Returns defaults if file doesn't exist.
func (s *StaticRouteStore) Load() (*StaticRouteData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadUnlocked()
}

// loadUnlocked reads static routes from disk without acquiring lock.
// Caller must hold the lock.
func (s *StaticRouteStore) loadUnlocked() (*StaticRouteData, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = defaultStaticRouteData()
			return s.data, nil
		}
		return nil, fmt.Errorf("read static routes file: %w", err)
	}

	var data StaticRouteData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse static routes JSON: %w", err)
	}

	if data.RouteLists == nil {
		data.RouteLists = []StaticRouteList{}
	}

	s.data = &data
	return s.data, nil
}

// defaultStaticRouteData returns empty static route data with initialized collections.
func defaultStaticRouteData() *StaticRouteData {
	return &StaticRouteData{
		RouteLists: []StaticRouteList{},
	}
}

// Get returns cached static route data or loads from disk.
func (s *StaticRouteStore) Get() (*StaticRouteData, error) {
	s.mu.RLock()
	if s.data != nil {
		defer s.mu.RUnlock()
		return s.data, nil
	}
	s.mu.RUnlock()

	return s.Load()
}

// Save writes static route data to disk.
func (s *StaticRouteStore) Save(data *StaticRouteData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveUnlocked(data)
}

// saveUnlocked writes static route data to disk without acquiring lock.
// Caller must hold the lock.
func (s *StaticRouteStore) saveUnlocked(data *StaticRouteData) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal static routes: %w", err)
	}

	if err := AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write static routes file: %w", err)
	}

	s.data = data
	return nil
}

// ListRouteLists returns all static route lists.
func (s *StaticRouteStore) ListRouteLists() ([]StaticRouteList, error) {
	data, err := s.Get()
	if err != nil {
		return nil, fmt.Errorf("list route lists: %w", err)
	}
	return data.RouteLists, nil
}

// GetRouteList returns a static route list by ID.
func (s *StaticRouteStore) GetRouteList(id string) (*StaticRouteList, error) {
	data, err := s.Get()
	if err != nil {
		return nil, fmt.Errorf("get route list: %w", err)
	}

	for i := range data.RouteLists {
		if data.RouteLists[i].ID == id {
			return &data.RouteLists[i], nil
		}
	}

	return nil, fmt.Errorf("route list not found: %s", id)
}

// AddRouteList appends a new static route list and saves.
func (s *StaticRouteStore) AddRouteList(rl StaticRouteList) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("add route list: %w", err)
	}

	data.RouteLists = append(data.RouteLists, rl)
	return s.saveUnlocked(data)
}

// UpdateRouteList replaces an existing static route list by ID and saves.
func (s *StaticRouteStore) UpdateRouteList(rl StaticRouteList) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("update route list: %w", err)
	}

	for i := range data.RouteLists {
		if data.RouteLists[i].ID == rl.ID {
			data.RouteLists[i] = rl
			return s.saveUnlocked(data)
		}
	}

	return fmt.Errorf("route list not found: %s", rl.ID)
}

// DeleteRouteList removes a static route list by ID and saves.
func (s *StaticRouteStore) DeleteRouteList(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("delete route list: %w", err)
	}

	for i := range data.RouteLists {
		if data.RouteLists[i].ID == id {
			data.RouteLists = append(data.RouteLists[:i], data.RouteLists[i+1:]...)
			return s.saveUnlocked(data)
		}
	}

	return fmt.Errorf("route list not found: %s", id)
}

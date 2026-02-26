package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Policy represents a client routing policy.
type Policy struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	ClientIP       string `json:"clientIP"`
	ClientHostname string `json:"clientHostname"`
	TunnelID       string `json:"tunnelID"`
	Fallback       string `json:"fallback"` // "drop" or "bypass"
	Enabled        bool   `json:"enabled"`
}

// PolicyData is the top-level policies.json structure.
type PolicyData struct {
	Policies []Policy       `json:"policies"`
	Tables   map[string]int `json:"tables"` // tunnelID → routing table number
}

// PolicyStore manages access policies storage.
type PolicyStore struct {
	path string
	mu   sync.RWMutex
	data *PolicyData
}

// NewPolicyStore creates a new policy store.
func NewPolicyStore(dataDir string) *PolicyStore {
	return &PolicyStore{
		path: filepath.Join(dataDir, "policies.json"),
	}
}

// Load reads policies from disk. Returns defaults if file doesn't exist.
func (s *PolicyStore) Load() (*PolicyData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.loadUnlocked()
}

// loadUnlocked reads policies from disk without acquiring lock.
// Caller must hold the lock.
func (s *PolicyStore) loadUnlocked() (*PolicyData, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = defaultPolicyData()
			return s.data, nil
		}
		return nil, fmt.Errorf("read policies file: %w", err)
	}

	var data PolicyData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse policies JSON: %w", err)
	}

	if data.Policies == nil {
		data.Policies = []Policy{}
	}
	if data.Tables == nil {
		data.Tables = map[string]int{}
	}

	s.data = &data
	return s.data, nil
}

// defaultPolicyData returns empty policy data with initialized collections.
func defaultPolicyData() *PolicyData {
	return &PolicyData{
		Policies: []Policy{},
		Tables:   map[string]int{},
	}
}

// Get returns cached policy data or loads from disk.
func (s *PolicyStore) Get() (*PolicyData, error) {
	s.mu.RLock()
	if s.data != nil {
		defer s.mu.RUnlock()
		return s.data, nil
	}
	s.mu.RUnlock()

	return s.Load()
}

// Save writes policy data to disk.
func (s *PolicyStore) Save(data *PolicyData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveUnlocked(data)
}

// saveUnlocked writes policy data to disk without acquiring lock.
// Caller must hold the lock.
func (s *PolicyStore) saveUnlocked(data *PolicyData) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policies: %w", err)
	}

	if err := AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write policies file: %w", err)
	}

	s.data = data
	return nil
}

// ListPolicies returns all policies.
func (s *PolicyStore) ListPolicies() ([]Policy, error) {
	data, err := s.Get()
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	return data.Policies, nil
}

// GetPolicy returns a policy by ID.
func (s *PolicyStore) GetPolicy(id string) (*Policy, error) {
	data, err := s.Get()
	if err != nil {
		return nil, fmt.Errorf("get policy: %w", err)
	}

	for i := range data.Policies {
		if data.Policies[i].ID == id {
			return &data.Policies[i], nil
		}
	}

	return nil, fmt.Errorf("policy not found: %s", id)
}

// AddPolicy appends a new policy and saves.
func (s *PolicyStore) AddPolicy(p Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("add policy: %w", err)
	}

	data.Policies = append(data.Policies, p)
	return s.saveUnlocked(data)
}

// UpdatePolicy replaces an existing policy by ID and saves.
func (s *PolicyStore) UpdatePolicy(p Policy) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("update policy: %w", err)
	}

	for i := range data.Policies {
		if data.Policies[i].ID == p.ID {
			data.Policies[i] = p
			return s.saveUnlocked(data)
		}
	}

	return fmt.Errorf("policy not found: %s", p.ID)
}

// DeletePolicy removes a policy by ID and saves.
func (s *PolicyStore) DeletePolicy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}

	for i := range data.Policies {
		if data.Policies[i].ID == id {
			data.Policies = append(data.Policies[:i], data.Policies[i+1:]...)
			return s.saveUnlocked(data)
		}
	}

	return fmt.Errorf("policy not found: %s", id)
}

// GetTableForTunnel returns the routing table number for a tunnel.
func (s *PolicyStore) GetTableForTunnel(tunnelID string) (int, bool) {
	data, err := s.Get()
	if err != nil {
		return 0, false
	}

	tableNum, ok := data.Tables[tunnelID]
	return tableNum, ok
}

// SetTableForTunnel sets the routing table number for a tunnel and saves.
func (s *PolicyStore) SetTableForTunnel(tunnelID string, tableNum int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("set table for tunnel: %w", err)
	}

	data.Tables[tunnelID] = tableNum
	return s.saveUnlocked(data)
}

// RemoveTableForTunnel removes the routing table mapping for a tunnel and saves.
func (s *PolicyStore) RemoveTableForTunnel(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := s.loadUnlocked()
	if err != nil {
		return fmt.Errorf("remove table for tunnel: %w", err)
	}

	delete(data.Tables, tunnelID)
	return s.saveUnlocked(data)
}

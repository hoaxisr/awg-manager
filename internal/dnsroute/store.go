package dnsroute

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Store manages DNS route domain lists storage.
type Store struct {
	path string
	mu   sync.RWMutex
	data *StoreData
}

// NewStore creates a new DNS route store.
func NewStore(dataDir string) *Store {
	return &Store{
		path: filepath.Join(dataDir, "dns-routes.json"),
	}
}

// Load reads domain lists from disk. Returns defaults if file doesn't exist.
func (s *Store) Load() (*StoreData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.data = defaultStoreData()
			return s.data, nil
		}
		return nil, fmt.Errorf("read dns-routes file: %w", err)
	}

	var data StoreData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("parse dns-routes JSON: %w", err)
	}

	if data.Lists == nil {
		data.Lists = []DomainList{}
	}
	normalizeLists(data.Lists)

	s.data = &data
	return s.data, nil
}

// Save writes domain list data to disk atomically.
func (s *Store) Save(data *StoreData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal dns-routes: %w", err)
	}

	if err := storage.AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write dns-routes file: %w", err)
	}

	s.data = data
	return nil
}

// GetCached returns cached data with read lock. Returns nil if not loaded yet.
func (s *Store) GetCached() *StoreData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// defaultStoreData returns empty store data with initialized collections.
func defaultStoreData() *StoreData {
	return &StoreData{
		Lists: []DomainList{},
	}
}

// EmptyStoreData returns empty store data for cleanup (reconcile will remove all AWG_* objects).
func EmptyStoreData() *StoreData {
	return defaultStoreData()
}

// normalizeLists ensures no nil slices in DomainList fields (Go nil → JSON null → JS crash).
func normalizeLists(lists []DomainList) {
	for i := range lists {
		if lists[i].Domains == nil {
			lists[i].Domains = []string{}
		}
		if lists[i].ManualDomains == nil {
			lists[i].ManualDomains = []string{}
		}
		if lists[i].Routes == nil {
			lists[i].Routes = []RouteTarget{}
		}
	}
}

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
	migrateLegacyExcludes(&data)

	s.data = &data
	return s.data, nil
}

// migrateLegacyExcludes splits any CIDR-shaped entries out of Excludes
// (legacy: Excludes used to be a free-form list and may contain CIDRs)
// and merges them into ExcludeSubnets. In-memory only — written back
// to disk only on the next Save().
func migrateLegacyExcludes(data *StoreData) {
	if data == nil {
		return
	}
	for i := range data.Lists {
		list := &data.Lists[i]
		if len(list.Excludes) == 0 {
			continue
		}
		domains, subnets := splitDomainsAndSubnets(list.Excludes)
		if len(subnets) == 0 {
			continue
		}
		list.Excludes = domains
		// Merge subnets into ExcludeSubnets, dedup union.
		seen := make(map[string]bool, len(list.ExcludeSubnets)+len(subnets))
		merged := make([]string, 0, len(list.ExcludeSubnets)+len(subnets))
		for _, s := range list.ExcludeSubnets {
			if !seen[s] {
				seen[s] = true
				merged = append(merged, s)
			}
		}
		for _, s := range subnets {
			if !seen[s] {
				seen[s] = true
				merged = append(merged, s)
			}
		}
		list.ExcludeSubnets = merged
	}
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

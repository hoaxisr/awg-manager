package dnsroute

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if data.HRRuleIcons == nil {
		data.HRRuleIcons = map[string]string{}
	}
	normalizeLists(data.Lists)
	migrateLegacyExcludes(&data)
	migrateRawEditorText(&data)
	dropped := dropLegacyHRRows(&data)

	s.data = &data
	if dropped > 0 {
		// Persist the cleanup so the file itself is cleaned, not just the
		// in-memory cache. Best-effort: on write error the cache is already
		// clean and the next Save() rewrites disk — failing startup over
		// inert-row cleanup would be worse. (Store has no logger.)
		// ponytail: swallow write error here; next real Save() surfaces a
		// persistent disk problem.
		_ = s.writeLocked(&data)
	}
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

// migrateRawEditorText backfills raw editor text fields for configs created
// before manualText/excludesText existed. In-memory only — written back to disk
// on the next Save(), matching migrateLegacyExcludes behavior.
func migrateRawEditorText(data *StoreData) {
	if data == nil {
		return
	}

	for i := range data.Lists {
		list := &data.Lists[i]

		// HR Neo rules are sourced from HR files; keep this migration scoped to
		// NDMS/stored DNS routes only.
		if isHydraRoute(list.Backend) {
			continue
		}

		if list.ManualText == nil && len(list.ManualDomains) > 0 {
			raw := strings.Join(list.ManualDomains, "\n")
			list.ManualText = &raw
		}

		if list.ExcludesText == nil && (len(list.Excludes) > 0 || len(list.ExcludeSubnets) > 0) {
			lines := make([]string, 0, len(list.Excludes)+len(list.ExcludeSubnets))
			lines = append(lines, list.Excludes...)
			lines = append(lines, list.ExcludeSubnets...)
			raw := strings.Join(lines, "\n")
			list.ExcludesText = &raw
		}
	}
}

// writeLocked marshals and atomically writes data, updating the cache.
// Caller MUST already hold s.mu.
func (s *Store) writeLocked(data *StoreData) error {
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

// dropLegacyHRRows removes HydraRoute-backed rows from data.Lists. HR rules
// live in HR config files (source of truth); any hydraroute-backed row left in
// dns-routes.json is a legacy leftover — hidden from List(), never reconciled,
// and only pollutes the dedup index. Returns how many rows were removed.
func dropLegacyHRRows(data *StoreData) int {
	if data == nil || len(data.Lists) == 0 {
		return 0
	}
	kept := data.Lists[:0]
	removed := 0
	for _, l := range data.Lists {
		if isHydraRoute(l.Backend) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	data.Lists = kept
	return removed
}

// Save writes domain list data to disk atomically.
func (s *Store) Save(data *StoreData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(data)
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
		Lists:       []DomainList{},
		HRRuleIcons: map[string]string{},
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

package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/clientroute"
)

type clientRouteData struct {
	Routes []clientroute.ClientRoute `json:"routes"`
	Tables map[string]int            `json:"tables"` // tunnelID → routing table number
}

// ClientRouteStore manages per-device VPN routing rules and routing table allocations.
type ClientRouteStore struct {
	path string
	mu   sync.RWMutex
	data *clientRouteData
}

// NewClientRouteStore creates a new client route store backed by client-routes.json.
func NewClientRouteStore(dataDir string) *ClientRouteStore {
	s := &ClientRouteStore{
		path: filepath.Join(dataDir, "client-routes.json"),
	}
	s.load()
	return s
}

// load reads data from disk. On missing or corrupt file, initializes empty data.
// Caller must NOT hold the lock.
func (s *ClientRouteStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadUnlocked()
}

// loadUnlocked reads data from disk without acquiring the lock.
// Caller must hold the write lock.
func (s *ClientRouteStore) loadUnlocked() {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.data = defaultClientRouteData()
		return
	}

	var data clientRouteData
	if err := json.Unmarshal(raw, &data); err != nil {
		s.data = defaultClientRouteData()
		return
	}

	if data.Routes == nil {
		data.Routes = []clientroute.ClientRoute{}
	}
	if data.Tables == nil {
		data.Tables = map[string]int{}
	}

	s.data = &data
}

func defaultClientRouteData() *clientRouteData {
	return &clientRouteData{
		Routes: []clientroute.ClientRoute{},
		Tables: map[string]int{},
	}
}

// saveUnlocked writes data to disk. Caller must hold the write lock.
func (s *ClientRouteStore) saveUnlocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal client routes: %w", err)
	}
	if err := AtomicWrite(s.path, raw); err != nil {
		return fmt.Errorf("write client routes file: %w", err)
	}
	return nil
}

// List returns a copy of all client routes.
func (s *ClientRouteStore) List() []clientroute.ClientRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.data == nil || len(s.data.Routes) == 0 {
		return []clientroute.ClientRoute{}
	}
	result := make([]clientroute.ClientRoute, len(s.data.Routes))
	copy(result, s.data.Routes)
	return result
}

// Get returns a copy of the route with the given ID, or nil if not found.
func (s *ClientRouteStore) Get(id string) *clientroute.ClientRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, r := range s.data.Routes {
		if r.ID == id {
			cp := r
			return &cp
		}
	}
	return nil
}

// FindByClientIP returns a copy of the route matching the given IP, or nil.
func (s *ClientRouteStore) FindByClientIP(ip string) *clientroute.ClientRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, r := range s.data.Routes {
		if r.ClientIP == ip {
			cp := r
			return &cp
		}
	}
	return nil
}

// FindByTunnel returns all routes for the given tunnel ID.
func (s *ClientRouteStore) FindByTunnel(tunnelID string) []clientroute.ClientRoute {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []clientroute.ClientRoute
	for _, r := range s.data.Routes {
		if r.TunnelID == tunnelID {
			result = append(result, r)
		}
	}
	if result == nil {
		return []clientroute.ClientRoute{}
	}
	return result
}

// Add appends a new route and saves to disk.
func (s *ClientRouteStore) Add(r clientroute.ClientRoute) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadUnlocked()
	s.data.Routes = append(s.data.Routes, r)
	return s.saveUnlocked()
}

// Update replaces an existing route by ID and saves to disk.
func (s *ClientRouteStore) Update(r clientroute.ClientRoute) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadUnlocked()
	for i := range s.data.Routes {
		if s.data.Routes[i].ID == r.ID {
			s.data.Routes[i] = r
			return s.saveUnlocked()
		}
	}
	return fmt.Errorf("client route not found: %s", r.ID)
}

// Remove deletes a route by ID and saves to disk.
func (s *ClientRouteStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadUnlocked()
	for i := range s.data.Routes {
		if s.data.Routes[i].ID == id {
			s.data.Routes = append(s.data.Routes[:i], s.data.Routes[i+1:]...)
			return s.saveUnlocked()
		}
	}
	return fmt.Errorf("client route not found: %s", id)
}

// RemoveByTunnel removes all routes for a tunnel and saves to disk.
func (s *ClientRouteStore) RemoveByTunnel(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadUnlocked()
	filtered := s.data.Routes[:0]
	for _, r := range s.data.Routes {
		if r.TunnelID != tunnelID {
			filtered = append(filtered, r)
		}
	}
	s.data.Routes = filtered
	return s.saveUnlocked()
}

// GetTableForTunnel returns the allocated routing table number for a tunnel, if any.
func (s *ClientRouteStore) GetTableForTunnel(tunnelID string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	n, ok := s.data.Tables[tunnelID]
	return n, ok
}

// AllocateTable allocates a free routing table number (400-599) for a tunnel.
// Returns existing allocation if already allocated. usedTables lists externally
// reserved table numbers that must also be skipped.
func (s *ClientRouteStore) AllocateTable(tunnelID string, usedTables []int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadUnlocked()

	// Return existing allocation.
	if n, ok := s.data.Tables[tunnelID]; ok {
		return n, nil
	}

	// Build set of reserved table numbers.
	reserved := make(map[int]bool, len(usedTables)+len(s.data.Tables))
	for _, n := range usedTables {
		reserved[n] = true
	}
	for _, n := range s.data.Tables {
		reserved[n] = true
	}

	// Find first free table in 400-599.
	for n := 400; n <= 599; n++ {
		if !reserved[n] {
			s.data.Tables[tunnelID] = n
			if err := s.saveUnlocked(); err != nil {
				return 0, err
			}
			return n, nil
		}
	}

	return 0, fmt.Errorf("no free routing table numbers in range 400-599")
}

// FreeTable releases the routing table allocation for a tunnel and saves to disk.
func (s *ClientRouteStore) FreeTable(tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.loadUnlocked()
	delete(s.data.Tables, tunnelID)
	return s.saveUnlocked()
}

// GetAllTables returns a copy of all tunnel → table allocations.
func (s *ClientRouteStore) GetAllTables() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]int, len(s.data.Tables))
	for k, v := range s.data.Tables {
		result[k] = v
	}
	return result
}

// DeleteFile removes the backing JSON file from disk.
func (s *ClientRouteStore) DeleteFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete client routes file: %w", err)
	}
	s.data = defaultClientRouteData()
	return nil
}

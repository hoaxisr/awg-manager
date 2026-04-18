package query

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
)

// SystemInfoStore caches /show/version once at boot. Version does not
// change at runtime — no TTL, no invalidate.
type SystemInfoStore struct {
	getter Getter
	log    Logger

	mu     sync.RWMutex
	loaded bool
	value  ndms.Version

	initSF *cache.SingleFlight[struct{}, ndms.Version]
}

func NewSystemInfoStore(g Getter, log Logger) *SystemInfoStore {
	if log == nil {
		log = NopLogger()
	}
	return &SystemInfoStore{
		getter: g,
		log:    log,
		initSF: cache.NewSingleFlight[struct{}, ndms.Version](),
	}
}

// Init fetches /show/version and populates the cache. Safe to call
// concurrently — single-flight ensures only one HTTP call even under a
// race. A successful call is idempotent; subsequent callers see loaded=true
// and return immediately. A failed call leaves the store uninitialized so
// the next Init can try again.
func (s *SystemInfoStore) Init(ctx context.Context) error {
	s.mu.RLock()
	done := s.loaded
	s.mu.RUnlock()
	if done {
		return nil
	}

	_, err := s.initSF.Do(struct{}{}, func() (ndms.Version, error) {
		// Re-check under the single-flight guard — a concurrent caller
		// may have just populated the cache.
		s.mu.RLock()
		if s.loaded {
			v := s.value
			s.mu.RUnlock()
			return v, nil
		}
		s.mu.RUnlock()

		var wire versionWire
		if err := s.getter.Get(ctx, "/show/version", &wire); err != nil {
			return ndms.Version{}, fmt.Errorf("fetch version: %w", err)
		}
		v := ndms.Version{
			Release:      wire.Release,
			Title:        wire.Title,
			HardwareID:   wire.HwID,
			Description:  wire.Description,
			Manufacturer: wire.Manufacturer,
			Vendor:       wire.Vendor,
			Series:       wire.Series,
			Model:        wire.Model,
			Region:       wire.Region,
			Components:   append([]string(nil), wire.Components...),
			Uptime:       wire.Uptime,
			LastFetched:  time.Now(),
		}

		s.mu.Lock()
		s.value = v
		s.loaded = true
		s.mu.Unlock()
		return v, nil
	})
	return err
}

// Get returns the cached Version. Returns ErrNotInitialized if Init was
// never called successfully.
func (s *SystemInfoStore) Get() (ndms.Version, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.loaded {
		return ndms.Version{}, ErrNotInitialized
	}
	return s.value, nil
}

type versionWire struct {
	Release      string   `json:"release"`
	Title        string   `json:"title"`
	HwID         string   `json:"hw_id"`
	Description  string   `json:"description"`
	Manufacturer string   `json:"manufacturer"`
	Vendor       string   `json:"vendor"`
	Series       string   `json:"series"`
	Model        string   `json:"model"`
	Region       string   `json:"region"`
	Components   []string `json:"components"`
	Uptime       int64    `json:"uptime"`
}

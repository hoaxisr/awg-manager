package deviceproxy

import "sync"

// Deps groups the external collaborators Service needs. Wired once at
// startup in main.go. Fields are added by subsequent tasks as they
// introduce new dependencies (singbox integration in Task 8, NDMS query
// in Task 8, event bus in Task 10).
type Deps struct {
	Store *Store
}

// Service owns the deviceproxy storage + mutation surface. All public
// methods serialise through the embedded mutex.
type Service struct {
	d  Deps
	mu sync.Mutex
}

// NewService constructs a Service from the given dependencies.
func NewService(d Deps) *Service {
	return &Service{d: d}
}

// GetConfig returns the current persisted Config. Defensive copy via Store.
func (s *Service) GetConfig() Config {
	return s.d.Store.Get()
}

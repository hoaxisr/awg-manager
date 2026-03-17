package staticroute

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
)

// ServiceImpl is the concrete implementation of the static route Service.
type ServiceImpl struct {
	store    *storage.StaticRouteStore
	operator ops.Operator
	log      *logger.Logger
	mu       sync.Mutex

	// tunnelRunning checks if a tunnel is currently in StateRunning.
	// Set via SetTunnelRunningCheck to avoid circular imports with tunnel/service.
	tunnelRunning func(ctx context.Context, tunnelID string) bool

	// resolveIfaceName resolves a tunnelID to a kernel interface name.
	// Handles both managed (awg0 → opkgtun0) and system (system:Wireguard0 → nwg0) tunnels.
	// Set via SetResolveIfaceName from main.go.
	resolveIfaceName func(ctx context.Context, tunnelID string) string
}

// New creates a new static route service.
func New(store *storage.StaticRouteStore, operator ops.Operator, log *logger.Logger) *ServiceImpl {
	return &ServiceImpl{
		store:    store,
		operator: operator,
		log:      log,
	}
}

// SetTunnelRunningCheck sets the callback for checking if a tunnel is running.
func (s *ServiceImpl) SetTunnelRunningCheck(fn func(ctx context.Context, tunnelID string) bool) {
	s.tunnelRunning = fn
}

// SetResolveIfaceName sets the callback for resolving tunnelID to kernel interface name.
func (s *ServiceImpl) SetResolveIfaceName(fn func(ctx context.Context, tunnelID string) string) {
	s.resolveIfaceName = fn
}

// --- CRUD ---

// List returns all static route lists.
func (s *ServiceImpl) List() ([]storage.StaticRouteList, error) {
	return s.store.ListRouteLists()
}

// Get returns a static route list by ID.
func (s *ServiceImpl) Get(id string) (*storage.StaticRouteList, error) {
	return s.store.GetRouteList(id)
}

// Create validates and stores a new static route list.
func (s *ServiceImpl) Create(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rl.ID = fmt.Sprintf("srl%d", time.Now().UnixNano())
	now := time.Now().UTC().Format(time.RFC3339)
	rl.CreatedAt = now
	rl.UpdatedAt = now

	if err := validateRouteList(rl); err != nil {
		return nil, err
	}

	if err := s.store.AddRouteList(rl); err != nil {
		return nil, fmt.Errorf("create route list: %w", err)
	}

	if rl.Enabled {
		s.applyIfRunning(ctx, rl)
	}

	return &rl, nil
}

// Update validates and replaces an existing static route list.
func (s *ServiceImpl) Update(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validateRouteList(rl); err != nil {
		return nil, err
	}

	old, err := s.store.GetRouteList(rl.ID)
	if err != nil {
		return nil, fmt.Errorf("update route list: get existing: %w", err)
	}

	rl.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := s.store.UpdateRouteList(rl); err != nil {
		return nil, fmt.Errorf("update route list: %w", err)
	}

	// Reconcile routes: remove old, add new.
	if old.Enabled {
		s.removeRoutes(ctx, old.TunnelID, old.Subnets)
	}
	if rl.Enabled {
		s.applyIfRunning(ctx, rl)
	}

	return &rl, nil
}

// Delete removes a static route list, cleaning up its routes if active.
func (s *ServiceImpl) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.store.GetRouteList(id)
	if err != nil {
		return fmt.Errorf("delete route list: %w", err)
	}

	if existing.Enabled {
		s.removeRoutes(ctx, existing.TunnelID, existing.Subnets)
	}

	if err := s.store.DeleteRouteList(id); err != nil {
		return fmt.Errorf("delete route list: %w", err)
	}

	return nil
}

// SetEnabled toggles a route list's enabled state and hot-applies or removes routes.
func (s *ServiceImpl) SetEnabled(ctx context.Context, id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rl, err := s.store.GetRouteList(id)
	if err != nil {
		return fmt.Errorf("set enabled: %w", err)
	}

	if rl.Enabled == enabled {
		return nil // no change
	}

	rl.Enabled = enabled
	rl.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := s.store.UpdateRouteList(*rl); err != nil {
		return fmt.Errorf("set enabled: save: %w", err)
	}

	if enabled {
		s.applyIfRunning(ctx, *rl)
	} else {
		s.removeRoutes(ctx, rl.TunnelID, rl.Subnets)
	}

	return nil
}

// --- Import ---

// Import parses a .bat file and creates a route list from the extracted subnets.
func (s *ServiceImpl) Import(ctx context.Context, tunnelID, name, batContent string) (*storage.StaticRouteList, error) {
	subnets, parseErrors := ParseBat(batContent)
	if len(subnets) == 0 {
		if len(parseErrors) > 0 {
			return nil, fmt.Errorf("no valid routes found; parse errors: %s", parseErrors[0])
		}
		return nil, fmt.Errorf("no valid routes found in file")
	}

	if len(parseErrors) > 0 {
		s.log.Warnf("staticroute: import %q: %d parse errors (first: %s)", name, len(parseErrors), parseErrors[0])
	}

	rl := storage.StaticRouteList{
		Name:     name,
		TunnelID: tunnelID,
		Subnets:  subnets,
		Enabled:  true,
	}

	return s.Create(ctx, rl)
}

// --- Tunnel lifecycle hooks ---

// OnTunnelStart adds static routes for all enabled lists bound to this tunnel.
func (s *ServiceImpl) OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.listsForTunnel(tunnelID)
	if len(lists) == 0 {
		return nil
	}

	var allSubnets []string
	for _, rl := range lists {
		allSubnets = append(allSubnets, rl.Subnets...)
	}

	if err := s.operator.AddStaticRoutes(ctx, tunnelIface, allSubnets); err != nil {
		return fmt.Errorf("on tunnel start: add static routes: %w", err)
	}

	s.log.Infof("staticroute: tunnel %s started, applied %d routes from %d lists", tunnelID, len(allSubnets), len(lists))
	return nil
}

// OnTunnelStop removes static routes for all enabled lists bound to this tunnel.
func (s *ServiceImpl) OnTunnelStop(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.listsForTunnel(tunnelID)
	if len(lists) == 0 {
		return nil
	}

	ifaceName := s.getIfaceName(ctx, tunnelID)

	var allSubnets []string
	for _, rl := range lists {
		allSubnets = append(allSubnets, rl.Subnets...)
	}

	if err := s.operator.RemoveStaticRoutes(ctx, ifaceName, allSubnets); err != nil {
		s.log.Errorf("staticroute: tunnel %s stop, remove routes: %v", tunnelID, err)
	}

	s.log.Infof("staticroute: tunnel %s stopped, removed %d routes", tunnelID, len(allSubnets))
	return nil
}

// OnTunnelDelete logs the event. Routes are already removed by OnTunnelStop.
// Lists are kept for the user to manage in the UI.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	lists := s.listsForTunnel(tunnelID)
	if len(lists) > 0 {
		s.log.Infof("staticroute: tunnel %s deleted, %d route lists remain (orphaned)", tunnelID, len(lists))
	}
	return nil
}

// --- Reconcile ---

// Reconcile restores static routes for all running tunnels at daemon startup.
func (s *ServiceImpl) Reconcile(ctx context.Context, runningTunnels map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var totalRoutes int

	for tunnelID, tunnelIface := range runningTunnels {
		lists := s.listsForTunnel(tunnelID)
		if len(lists) == 0 {
			continue
		}

		var allSubnets []string
		for _, rl := range lists {
			allSubnets = append(allSubnets, rl.Subnets...)
		}

		if err := s.operator.AddStaticRoutes(ctx, tunnelIface, allSubnets); err != nil {
			s.log.Errorf("staticroute: reconcile add routes for %s: %v", tunnelID, err)
			continue
		}
		totalRoutes += len(allSubnets)
	}

	s.log.Infof("staticroute: reconcile complete, restored %d routes across %d tunnels", totalRoutes, len(runningTunnels))
	return nil
}

// --- Internal helpers ---

// applyIfRunning adds static routes for a route list if its tunnel is running.
func (s *ServiceImpl) applyIfRunning(ctx context.Context, rl storage.StaticRouteList) {
	if s.tunnelRunning == nil || !s.tunnelRunning(ctx, rl.TunnelID) {
		return
	}

	ifaceName := s.getIfaceName(ctx, rl.TunnelID)

	if err := s.operator.AddStaticRoutes(ctx, ifaceName, rl.Subnets); err != nil {
		s.log.Errorf("staticroute: hot-apply routes for %s: %v", rl.TunnelID, err)
		return
	}

	s.log.Infof("staticroute: hot-applied %d routes for tunnel %s", len(rl.Subnets), rl.TunnelID)
}

// removeRoutes removes static routes for a tunnel if it is running.
func (s *ServiceImpl) removeRoutes(ctx context.Context, tunnelID string, subnets []string) {
	if s.tunnelRunning == nil || !s.tunnelRunning(ctx, tunnelID) {
		return
	}

	ifaceName := s.getIfaceName(ctx, tunnelID)

	if err := s.operator.RemoveStaticRoutes(ctx, ifaceName, subnets); err != nil {
		s.log.Errorf("staticroute: remove routes for %s: %v", tunnelID, err)
	}
}

// listsForTunnel returns enabled route lists for a given tunnel.
func (s *ServiceImpl) listsForTunnel(tunnelID string) []storage.StaticRouteList {
	all, err := s.store.ListRouteLists()
	if err != nil {
		return nil
	}
	var result []storage.StaticRouteList
	for _, rl := range all {
		if rl.TunnelID == tunnelID && rl.Enabled {
			result = append(result, rl)
		}
	}
	return result
}

// SystemTunnelIDs returns unique system tunnel IDs from enabled route lists.
func (s *ServiceImpl) SystemTunnelIDs() []string {
	all, err := s.store.ListRouteLists()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, rl := range all {
		if rl.Enabled && tunnel.IsSystemTunnel(rl.TunnelID) && !seen[rl.TunnelID] {
			seen[rl.TunnelID] = true
			result = append(result, rl.TunnelID)
		}
	}
	return result
}

// getIfaceName resolves a tunnelID to kernel interface name.
func (s *ServiceImpl) getIfaceName(ctx context.Context, tunnelID string) string {
	if s.resolveIfaceName != nil {
		return s.resolveIfaceName(ctx, tunnelID)
	}
	return tunnel.NewNames(tunnelID).IfaceName
}

// validateRouteList checks required fields.
func validateRouteList(rl storage.StaticRouteList) error {
	if rl.TunnelID == "" {
		return fmt.Errorf("tunnelID is required")
	}
	if rl.Name == "" {
		return fmt.Errorf("name is required")
	}
	if len(rl.Subnets) == 0 {
		return fmt.Errorf("subnets must not be empty")
	}
	return nil
}

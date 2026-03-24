package clientroute

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// ServiceImpl implements the Service interface.
type ServiceImpl struct {
	mu             sync.Mutex
	store          Store
	operator       Operator
	getKernelIface GetKernelIfaceFunc
	tunnelExists   TunnelExistsFunc
	appLog         *logging.ScopedLogger
}

// New creates a new client route service.
func New(store Store, operator Operator, getKernelIface GetKernelIfaceFunc, tunnelExists TunnelExistsFunc, appLogger logging.AppLogger) *ServiceImpl {
	return &ServiceImpl{
		store:          store,
		operator:       operator,
		getKernelIface: getKernelIface,
		tunnelExists:   tunnelExists,
		appLog:         logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubClientRoute),
	}
}

// List returns all client routes.
func (s *ServiceImpl) List() ([]ClientRoute, error) {
	return s.store.List(), nil
}

// Create validates and stores a new client route, applying rules if the tunnel is running.
func (s *ServiceImpl) Create(ctx context.Context, route ClientRoute) (*ClientRoute, error) {
	// Validate IPv4.
	ip := net.ParseIP(route.ClientIP)
	if ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("invalid IPv4 address: %s", route.ClientIP)
	}
	route.ClientIP = ip.To4().String() // normalize

	// Validate fallback.
	if route.Fallback != "drop" && route.Fallback != "bypass" {
		return nil, fmt.Errorf("invalid fallback: %q (must be \"drop\" or \"bypass\")", route.Fallback)
	}

	// Check tunnel exists.
	if !s.tunnelExists(route.TunnelID) {
		return nil, fmt.Errorf("tunnel not found: %s", route.TunnelID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check duplicate IP.
	if existing := s.store.FindByClientIP(route.ClientIP); existing != nil {
		return nil, fmt.Errorf("client IP %s already has a route (id=%s)", route.ClientIP, existing.ID)
	}

	// Generate ID.
	route.ID = "cr-" + randomAlphanumeric(10)

	// Save.
	if err := s.store.Add(route); err != nil {
		return nil, fmt.Errorf("save client route: %w", err)
	}

	// Apply rules if enabled and tunnel is running.
	if route.Enabled {
		s.applyIfRunning(ctx, route)
	}

	s.appLog.Info("create", route.ClientIP, fmt.Sprintf("route to tunnel %s, fallback=%s", route.TunnelID, route.Fallback))
	return &route, nil
}

// Update modifies an existing client route.
func (s *ServiceImpl) Update(ctx context.Context, route ClientRoute) (*ClientRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := s.store.Get(route.ID)
	if old == nil {
		return nil, fmt.Errorf("client route not found: %s", route.ID)
	}

	// IP and hostname cannot change.
	route.ClientIP = old.ClientIP
	route.ClientHostname = old.ClientHostname

	tunnelChanged := route.TunnelID != old.TunnelID

	// If tunnel changed and old was enabled, remove old rules.
	if tunnelChanged && old.Enabled {
		s.removeRule(ctx, old.TunnelID, old.ClientIP)
		s.cleanupTableIfEmpty(ctx, old.TunnelID, old.ID)
	}

	// Save.
	if err := s.store.Update(route); err != nil {
		return nil, fmt.Errorf("update client route: %w", err)
	}

	// If enabled and tunnel changed, apply new rules.
	if route.Enabled && tunnelChanged {
		s.applyIfRunning(ctx, route)
	}

	s.appLog.Info("update", route.ClientIP, fmt.Sprintf("route to tunnel %s", route.TunnelID))
	return &route, nil
}

// Delete removes a client route and its ip rules.
func (s *ServiceImpl) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	route := s.store.Get(id)
	if route == nil {
		return fmt.Errorf("client route not found: %s", id)
	}

	// Remove ip rule if enabled.
	if route.Enabled {
		s.removeRule(ctx, route.TunnelID, route.ClientIP)
	}

	// Cleanup table if no more routes for this tunnel.
	s.cleanupTableIfEmpty(ctx, route.TunnelID, route.ID)

	// Remove from store.
	if err := s.store.Remove(id); err != nil {
		return fmt.Errorf("remove client route: %w", err)
	}

	s.appLog.Info("delete", route.ClientIP, fmt.Sprintf("removed route to tunnel %s", route.TunnelID))
	return nil
}

// SetEnabled enables or disables a client route.
func (s *ServiceImpl) SetEnabled(ctx context.Context, id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	route := s.store.Get(id)
	if route == nil {
		return fmt.Errorf("client route not found: %s", id)
	}

	if route.Enabled == enabled {
		return nil // already in desired state
	}

	route.Enabled = enabled
	if err := s.store.Update(*route); err != nil {
		return fmt.Errorf("update client route: %w", err)
	}

	if enabled {
		s.applyIfRunning(ctx, *route)
		s.appLog.Info("enable", route.ClientIP, fmt.Sprintf("enabled route to tunnel %s", route.TunnelID))
	} else {
		s.removeRule(ctx, route.TunnelID, route.ClientIP)
		s.cleanupTableIfEmpty(ctx, route.TunnelID, route.ID)
		s.appLog.Info("disable", route.ClientIP, fmt.Sprintf("disabled route to tunnel %s", route.TunnelID))
	}

	return nil
}

// OnTunnelStart sets up routing tables and ip rules for all enabled routes of a tunnel.
func (s *ServiceImpl) OnTunnelStart(ctx context.Context, tunnelID string, kernelIface string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	routes := s.store.FindByTunnel(tunnelID)
	var enabled []ClientRoute
	for _, r := range routes {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		return nil
	}

	// Allocate routing table.
	usedTables, err := s.operator.ListUsedRoutingTables(ctx)
	if err != nil {
		return fmt.Errorf("list used routing tables: %w", err)
	}

	tableNum, err := s.store.AllocateTable(tunnelID, usedTables)
	if err != nil {
		return fmt.Errorf("allocate routing table: %w", err)
	}

	// Setup route table (idempotent).
	if err := s.operator.SetupClientRouteTable(ctx, kernelIface, tableNum); err != nil {
		return fmt.Errorf("setup client route table %d: %w", tableNum, err)
	}

	// Add ip rule for each enabled client.
	for _, r := range enabled {
		if err := s.operator.AddClientRule(ctx, r.ClientIP, tableNum); err != nil {
			s.appLog.Warn("add-rule", r.ClientIP, fmt.Sprintf("failed to add rule for table %d: %v", tableNum, err))
		}
	}

	s.appLog.Info("tunnel-start", tunnelID, fmt.Sprintf("applied %d client routes via table %d", len(enabled), tableNum))
	return nil
}

// OnTunnelStop removes routing rules based on fallback policy.
// "bypass" routes are removed (traffic goes via default route).
// "drop" routes are kept (kill switch — traffic is blackholed while tunnel is down).
func (s *ServiceImpl) OnTunnelStop(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableNum, ok := s.store.GetTableForTunnel(tunnelID)
	if !ok {
		return nil // no table allocated, nothing to do
	}

	routes := s.store.FindByTunnel(tunnelID)
	hasDropRules := false

	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		if r.Fallback == "bypass" {
			if err := s.operator.RemoveClientRule(ctx, r.ClientIP, tableNum); err != nil {
				s.appLog.Warn("remove-rule", r.ClientIP, fmt.Sprintf("failed to remove bypass rule: %v", err))
			}
		} else {
			// "drop" — keep rule in place (kill switch).
			hasDropRules = true
		}
	}

	// Only cleanup table if no drop rules remain.
	if !hasDropRules {
		if err := s.operator.CleanupClientRouteTable(ctx, tableNum); err != nil {
			s.appLog.Warn("cleanup-table", tunnelID, fmt.Sprintf("failed to cleanup table %d: %v", tableNum, err))
		}
		if err := s.store.FreeTable(tunnelID); err != nil {
			s.appLog.Warn("free-table", tunnelID, fmt.Sprintf("failed to free table: %v", err))
		}
	}

	s.appLog.Info("tunnel-stop", tunnelID, "removed bypass client rules")
	return nil
}

// OnTunnelDelete removes all rules and routes for a deleted tunnel.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tableNum, hasTable := s.store.GetTableForTunnel(tunnelID)

	// Remove all ip rules.
	if hasTable {
		routes := s.store.FindByTunnel(tunnelID)
		for _, r := range routes {
			if r.Enabled {
				if err := s.operator.RemoveClientRule(ctx, r.ClientIP, tableNum); err != nil {
					s.appLog.Warn("remove-rule", r.ClientIP, fmt.Sprintf("failed on tunnel delete: %v", err))
				}
			}
		}

		// Cleanup routing table.
		if err := s.operator.CleanupClientRouteTable(ctx, tableNum); err != nil {
			s.appLog.Warn("cleanup-table", tunnelID, fmt.Sprintf("failed to cleanup table %d: %v", tableNum, err))
		}
	}

	// Remove all routes from store.
	if err := s.store.RemoveByTunnel(tunnelID); err != nil {
		s.appLog.Warn("remove-routes", tunnelID, fmt.Sprintf("failed to remove routes from store: %v", err))
	}

	// Free table allocation.
	if hasTable {
		if err := s.store.FreeTable(tunnelID); err != nil {
			s.appLog.Warn("free-table", tunnelID, fmt.Sprintf("failed to free table: %v", err))
		}
	}

	s.appLog.Info("tunnel-delete", tunnelID, "removed all client routes")
	return nil
}

// Reconcile re-applies routing rules for all running tunnels.
func (s *ServiceImpl) Reconcile(ctx context.Context, runningTunnels map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for tunnelID, kernelIface := range runningTunnels {
		routes := s.store.FindByTunnel(tunnelID)
		var enabled []ClientRoute
		for _, r := range routes {
			if r.Enabled {
				enabled = append(enabled, r)
			}
		}
		if len(enabled) == 0 {
			continue
		}

		usedTables, err := s.operator.ListUsedRoutingTables(ctx)
		if err != nil {
			s.appLog.Warn("reconcile", tunnelID, fmt.Sprintf("failed to list used tables: %v", err))
			continue
		}

		tableNum, err := s.store.AllocateTable(tunnelID, usedTables)
		if err != nil {
			s.appLog.Warn("reconcile", tunnelID, fmt.Sprintf("failed to allocate table: %v", err))
			continue
		}

		if err := s.operator.SetupClientRouteTable(ctx, kernelIface, tableNum); err != nil {
			s.appLog.Warn("reconcile", tunnelID, fmt.Sprintf("failed to setup table %d: %v", tableNum, err))
			continue
		}

		for _, r := range enabled {
			if err := s.operator.AddClientRule(ctx, r.ClientIP, tableNum); err != nil {
				s.appLog.Warn("reconcile", r.ClientIP, fmt.Sprintf("failed to add rule: %v", err))
			}
		}

		s.appLog.Full("reconcile", tunnelID, fmt.Sprintf("applied %d client routes via table %d", len(enabled), tableNum))
	}

	return nil
}

// CleanupAll removes all routing tables and deletes storage file.
func (s *ServiceImpl) CleanupAll(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tables := s.store.GetAllTables()
	for tunnelID, tableNum := range tables {
		if err := s.operator.CleanupClientRouteTable(ctx, tableNum); err != nil {
			s.appLog.Warn("cleanup-all", tunnelID, fmt.Sprintf("failed to cleanup table %d: %v", tableNum, err))
		}
	}

	if err := s.store.DeleteFile(); err != nil {
		return fmt.Errorf("delete client routes storage: %w", err)
	}

	s.appLog.Info("cleanup-all", "", "removed all client route tables and storage")
	return nil
}

// applyIfRunning applies ip rules for a route if the tunnel is currently running.
// Errors are logged but non-fatal.
func (s *ServiceImpl) applyIfRunning(ctx context.Context, route ClientRoute) {
	kernelIface, running := s.getKernelIface(ctx, route.TunnelID)
	if !running {
		return
	}

	usedTables, err := s.operator.ListUsedRoutingTables(ctx)
	if err != nil {
		s.appLog.Warn("apply", route.ClientIP, fmt.Sprintf("failed to list used tables: %v", err))
		return
	}

	tableNum, err := s.store.AllocateTable(route.TunnelID, usedTables)
	if err != nil {
		s.appLog.Warn("apply", route.ClientIP, fmt.Sprintf("failed to allocate table: %v", err))
		return
	}

	if err := s.operator.SetupClientRouteTable(ctx, kernelIface, tableNum); err != nil {
		s.appLog.Warn("apply", route.ClientIP, fmt.Sprintf("failed to setup table %d: %v", tableNum, err))
		return
	}

	if err := s.operator.AddClientRule(ctx, route.ClientIP, tableNum); err != nil {
		s.appLog.Warn("apply", route.ClientIP, fmt.Sprintf("failed to add rule: %v", err))
	}
}

// removeRule removes an ip rule for a client route.
func (s *ServiceImpl) removeRule(ctx context.Context, tunnelID, clientIP string) {
	tableNum, ok := s.store.GetTableForTunnel(tunnelID)
	if !ok {
		return // no table allocated
	}

	if err := s.operator.RemoveClientRule(ctx, clientIP, tableNum); err != nil {
		s.appLog.Warn("remove-rule", clientIP, fmt.Sprintf("failed to remove rule from table %d: %v", tableNum, err))
	}
}

// cleanupTableIfEmpty checks if a tunnel has any remaining enabled routes (excluding excludeID)
// and cleans up the routing table if empty.
func (s *ServiceImpl) cleanupTableIfEmpty(ctx context.Context, tunnelID, excludeID string) {
	routes := s.store.FindByTunnel(tunnelID)
	for _, r := range routes {
		if r.ID != excludeID && r.Enabled {
			return // still has enabled routes
		}
	}

	tableNum, ok := s.store.GetTableForTunnel(tunnelID)
	if !ok {
		return
	}

	if err := s.operator.CleanupClientRouteTable(ctx, tableNum); err != nil {
		s.appLog.Warn("cleanup-table", tunnelID, fmt.Sprintf("failed to cleanup table %d: %v", tableNum, err))
	}

	if err := s.store.FreeTable(tunnelID); err != nil {
		s.appLog.Warn("free-table", tunnelID, fmt.Sprintf("failed to free table: %v", err))
	}
}

// randomAlphanumeric generates a random alphanumeric string of the given length.
func randomAlphanumeric(n int) string {
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(idAlphabet))))
		if err != nil {
			// Fallback: should never happen with crypto/rand.
			b[i] = 'x'
			continue
		}
		b[i] = idAlphabet[idx.Int64()]
	}
	return string(b)
}

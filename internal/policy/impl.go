package policy

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

const (
	tableMin = 100
	tableMax = 199
)

// ServiceImpl is the concrete implementation of the policy Service.
type ServiceImpl struct {
	store    *storage.PolicyStore
	operator ops.Operator
	log      *logger.Logger
	mu       sync.Mutex

	// tunnelRunning checks if a tunnel is currently in StateRunning.
	// Set via SetTunnelRunningCheck to avoid circular imports with tunnel/service.
	tunnelRunning func(ctx context.Context, tunnelID string) bool

	// resolveIfaceName resolves a tunnelID to a kernel interface name.
	// Handles both managed (awg0 → opkgtun0) and system (system:Wireguard0 → nwg0) tunnels.
	resolveIfaceName func(ctx context.Context, tunnelID string) string
}

// New creates a new policy service.
func New(store *storage.PolicyStore, operator ops.Operator, log *logger.Logger) *ServiceImpl {
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

// applyRuleIfRunning adds an ip rule for a client if the tunnel is currently running.
// Idempotent: allocateTable and SetupPolicyTable are safe to call repeatedly.
func (s *ServiceImpl) applyRuleIfRunning(ctx context.Context, tunnelID, clientIP string) {
	if s.tunnelRunning == nil || !s.tunnelRunning(ctx, tunnelID) {
		return
	}

	ifaceName := s.getIfaceName(ctx, tunnelID)

	tableNum, err := s.allocateTable(ctx, tunnelID)
	if err != nil {
		s.log.Errorf("policy: allocate table for %s: %v", tunnelID, err)
		return
	}

	if err := s.operator.SetupPolicyTable(ctx, ifaceName, tableNum); err != nil {
		s.log.Errorf("policy: setup table %d for %s: %v", tableNum, tunnelID, err)
		return
	}

	if err := s.operator.AddClientRule(ctx, clientIP, tableNum); err != nil {
		s.log.Errorf("policy: add rule for %s table %d: %v", clientIP, tableNum, err)
		return
	}

	s.log.Infof("policy: hot-applied rule for %s → tunnel %s (table %d)", clientIP, tunnelID, tableNum)
}

// removeRule removes an ip rule for a client. Safe to call if rule doesn't exist.
func (s *ServiceImpl) removeRule(ctx context.Context, tunnelID, clientIP string) {
	tableNum, ok := s.store.GetTableForTunnel(tunnelID)
	if !ok {
		return
	}
	_ = s.operator.RemoveClientRule(ctx, clientIP, tableNum)
}

// --- CRUD ---

// List returns all policies.
func (s *ServiceImpl) List() ([]storage.Policy, error) {
	return s.store.ListPolicies()
}

// Get returns a policy by ID.
func (s *ServiceImpl) Get(id string) (*storage.Policy, error) {
	return s.store.GetPolicy(id)
}

// Create validates and stores a new policy.
func (s *ServiceImpl) Create(ctx context.Context, p storage.Policy) (*storage.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p.ID = fmt.Sprintf("pol%d", time.Now().UnixNano())

	if err := validatePolicy(p); err != nil {
		return nil, err
	}

	if p.Name == "" {
		if p.ClientHostname != "" {
			p.Name = p.ClientHostname
		} else {
			p.Name = p.ClientIP
		}
	}

	if err := s.store.AddPolicy(p); err != nil {
		return nil, fmt.Errorf("create policy: %w", err)
	}

	if p.Enabled {
		s.applyRuleIfRunning(ctx, p.TunnelID, p.ClientIP)
	}

	return &p, nil
}

// Update validates and replaces an existing policy.
func (s *ServiceImpl) Update(ctx context.Context, p storage.Policy) (*storage.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := validatePolicy(p); err != nil {
		return nil, err
	}

	// Fetch old policy BEFORE saving — needed to reconcile ip rules.
	old, err := s.store.GetPolicy(p.ID)
	if err != nil {
		return nil, fmt.Errorf("update policy: get existing: %w", err)
	}

	if err := s.store.UpdatePolicy(p); err != nil {
		return nil, fmt.Errorf("update policy: %w", err)
	}

	// Reconcile ip rules: remove old, add new.
	if old.Enabled {
		s.removeRule(ctx, old.TunnelID, old.ClientIP)
	}
	if p.Enabled {
		s.applyRuleIfRunning(ctx, p.TunnelID, p.ClientIP)
	}

	// If tunnel changed and old tunnel has no remaining policies, clean up its table.
	if old.TunnelID != p.TunnelID {
		remaining := s.policiesForTunnel(old.TunnelID)
		if len(remaining) == 0 {
			if tableNum, ok := s.store.GetTableForTunnel(old.TunnelID); ok {
				_ = s.operator.CleanupPolicyTable(ctx, tableNum)
				_ = s.store.RemoveTableForTunnel(old.TunnelID)
			}
		}
	}

	return &p, nil
}

// Delete removes a policy, cleaning up its ip rule if active.
func (s *ServiceImpl) Delete(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.store.GetPolicy(id)
	if err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}

	// Clean up ip rule if a table is assigned for this tunnel.
	if tableNum, ok := s.store.GetTableForTunnel(existing.TunnelID); ok {
		// Ignore errors — rule might not exist if tunnel is stopped.
		_ = s.operator.RemoveClientRule(ctx, existing.ClientIP, tableNum)
	}

	if err := s.store.DeletePolicy(id); err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}

	// If no remaining policies use this tunnel, clean up the routing table.
	remaining := s.policiesForTunnel(existing.TunnelID)
	if len(remaining) == 0 {
		if tableNum, ok := s.store.GetTableForTunnel(existing.TunnelID); ok {
			_ = s.operator.CleanupPolicyTable(ctx, tableNum)
			_ = s.store.RemoveTableForTunnel(existing.TunnelID)
		}
	}

	return nil
}

// --- Tunnel lifecycle hooks ---

// OnTunnelStart sets up policy routing when a tunnel starts.
func (s *ServiceImpl) OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	policies := s.policiesForTunnel(tunnelID)
	if len(policies) == 0 {
		return nil
	}

	tableNum, err := s.allocateTable(ctx, tunnelID)
	if err != nil {
		return fmt.Errorf("on tunnel start: %w", err)
	}

	if err := s.operator.SetupPolicyTable(ctx, tunnelIface, tableNum); err != nil {
		return fmt.Errorf("setup policy table: %w", err)
	}

	for _, p := range policies {
		if err := s.operator.AddClientRule(ctx, p.ClientIP, tableNum); err != nil {
			s.log.Errorf("policy: add rule for %s table %d: %v", p.ClientIP, tableNum, err)
		}
	}

	s.log.Infof("policy: tunnel %s started, applied %d rules (table %d)", tunnelID, len(policies), tableNum)
	return nil
}

// OnTunnelStop handles policy cleanup when a tunnel stops.
// For "bypass" clients, removes ip rules so traffic uses the default route.
// For "drop" clients, rules stay but the table route is removed — traffic drops.
func (s *ServiceImpl) OnTunnelStop(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	policies := s.policiesForTunnel(tunnelID)
	if len(policies) == 0 {
		return nil
	}

	tableNum, ok := s.store.GetTableForTunnel(tunnelID)
	if !ok {
		return nil
	}

	for _, p := range policies {
		if p.Fallback == "bypass" {
			_ = s.operator.RemoveClientRule(ctx, p.ClientIP, tableNum)
		}
		// "drop" clients: rule stays, table route removed below → traffic drops.
	}

	// Remove default route from table — drop-mode clients lose internet.
	if err := s.operator.CleanupPolicyTable(ctx, tableNum); err != nil {
		s.log.Errorf("policy: cleanup table %d for tunnel %s: %v", tableNum, tunnelID, err)
	}

	s.log.Infof("policy: tunnel %s stopped, cleaned up table %d", tunnelID, tableNum)
	return nil
}

// OnTunnelDelete cleans up all policy routing for a deleted tunnel.
// Policies are NOT deleted — they become orphaned for user to manage in the UI.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	policies := s.policiesForTunnel(tunnelID)
	tableNum, hasTable := s.store.GetTableForTunnel(tunnelID)

	if hasTable {
		for _, p := range policies {
			_ = s.operator.RemoveClientRule(ctx, p.ClientIP, tableNum)
		}
		_ = s.operator.CleanupPolicyTable(ctx, tableNum)
		_ = s.store.RemoveTableForTunnel(tunnelID)
	}

	s.log.Infof("policy: tunnel %s deleted, cleaned up %d rules", tunnelID, len(policies))
	return nil
}

// --- Reconcile ---

// Reconcile restores policy routing for all running tunnels at daemon startup.
func (s *ServiceImpl) Reconcile(ctx context.Context, runningTunnels map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var totalRules int

	for tunnelID, tunnelIface := range runningTunnels {
		policies := s.policiesForTunnel(tunnelID)
		if len(policies) == 0 {
			continue
		}

		tableNum, err := s.allocateTable(ctx, tunnelID)
		if err != nil {
			s.log.Errorf("policy: reconcile allocate table for %s: %v", tunnelID, err)
			continue
		}

		if err := s.operator.SetupPolicyTable(ctx, tunnelIface, tableNum); err != nil {
			s.log.Errorf("policy: reconcile setup table %d for %s: %v", tableNum, tunnelID, err)
			continue
		}

		for _, p := range policies {
			if err := s.operator.AddClientRule(ctx, p.ClientIP, tableNum); err != nil {
				s.log.Errorf("policy: reconcile add rule for %s: %v", p.ClientIP, err)
				continue
			}
			totalRules++
		}
	}

	s.log.Infof("policy: reconcile complete, restored %d rules across %d tunnels", totalRules, len(runningTunnels))
	return nil
}

// --- Internal helpers ---

// allocateTable finds or creates a routing table number for a tunnel.
func (s *ServiceImpl) allocateTable(ctx context.Context, tunnelID string) (int, error) {
	if num, ok := s.store.GetTableForTunnel(tunnelID); ok {
		return num, nil
	}

	// Get tables already in use by ip rules.
	systemTables, err := s.operator.ListUsedRoutingTables(ctx)
	if err != nil {
		systemTables = nil // non-fatal, proceed with store data only
	}
	usedSet := make(map[int]bool)
	for _, t := range systemTables {
		usedSet[t] = true
	}

	// Also mark tables assigned in store.
	data, err := s.store.Get()
	if err != nil {
		return 0, fmt.Errorf("load policy data: %w", err)
	}
	for _, t := range data.Tables {
		usedSet[t] = true
	}

	// Find minimum free table in range.
	for num := tableMin; num <= tableMax; num++ {
		if !usedSet[num] {
			if err := s.store.SetTableForTunnel(tunnelID, num); err != nil {
				return 0, fmt.Errorf("save table mapping: %w", err)
			}
			return num, nil
		}
	}

	return 0, fmt.Errorf("no free routing tables in range %d-%d", tableMin, tableMax)
}

// policiesForTunnel returns enabled policies for a given tunnel.
func (s *ServiceImpl) policiesForTunnel(tunnelID string) []storage.Policy {
	all, err := s.store.ListPolicies()
	if err != nil {
		return nil
	}
	var result []storage.Policy
	for _, p := range all {
		if p.TunnelID == tunnelID && p.Enabled {
			result = append(result, p)
		}
	}
	return result
}

// SystemTunnelIDs returns unique system tunnel IDs from enabled policies.
func (s *ServiceImpl) SystemTunnelIDs() []string {
	all, err := s.store.ListPolicies()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, p := range all {
		if p.Enabled && tunnel.IsSystemTunnel(p.TunnelID) && !seen[p.TunnelID] {
			seen[p.TunnelID] = true
			result = append(result, p.TunnelID)
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

// validatePolicy checks required fields.
func validatePolicy(p storage.Policy) error {
	if p.ClientIP == "" {
		return fmt.Errorf("clientIP is required")
	}
	if p.TunnelID == "" {
		return fmt.Errorf("tunnelID is required")
	}
	if p.Fallback != "drop" && p.Fallback != "bypass" {
		return fmt.Errorf("fallback must be \"drop\" or \"bypass\", got %q", p.Fallback)
	}
	return nil
}

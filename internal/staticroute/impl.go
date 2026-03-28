package staticroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// ndmsClient is the subset of ndms.Client needed for static routes.
type ndmsClient interface {
	RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error)
	Save(ctx context.Context) error
}

// ServiceImpl is the concrete implementation of the static route Service.
type ServiceImpl struct {
	store   *storage.StaticRouteStore
	ndms    ndmsClient
	catalog routing.Catalog
	log     *logger.Logger
	appLog  *logging.ScopedLogger
	mu      sync.Mutex

	// ifaceExists checks whether a network interface exists. Defaults to
	// net.InterfaceByName; override in tests.
	ifaceExists func(name string) bool
}

// New creates a new static route service.
func New(
	store *storage.StaticRouteStore,
	ndmsClient ndmsClient,
	catalog routing.Catalog,
	log *logger.Logger,
	appLogger logging.AppLogger,
) *ServiceImpl {
	return &ServiceImpl{
		store:       store,
		ndms:        ndmsClient,
		catalog:     catalog,
		log:         log,
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubStaticRoute),
		ifaceExists: defaultIfaceExists,
	}
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
		s.applyRoutes(ctx, rl)
		if !isOS4Kernel(rl.TunnelID) {
			s.save(ctx)
		}
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
		s.applyRoutes(ctx, rl)
	}

	// Save NDMS config if any affected tunnel uses NDMS routes.
	if !isOS4Kernel(old.TunnelID) || !isOS4Kernel(rl.TunnelID) {
		s.save(ctx)
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

	if !isOS4Kernel(existing.TunnelID) {
		s.save(ctx)
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
		s.applyRoutes(ctx, *rl)
	} else {
		s.removeRoutes(ctx, rl.TunnelID, rl.Subnets)
	}

	if !isOS4Kernel(rl.TunnelID) {
		s.save(ctx)
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

// OnTunnelStart applies routes when a tunnel starts.
// For NDMS-managed tunnels this is a no-op (NDMS "auto" flag handles it).
// For OS4 kernel tunnels, routes are applied via ip route using tunnelIface directly.
func (s *ServiceImpl) OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error {
	if !isOS4Kernel(tunnelID) {
		return nil // NDMS "auto" flag handles it
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.listsForTunnel(tunnelID)
	for _, rl := range lists {
		for _, subnet := range rl.Subnets {
			if err := s.ipRouteAdd(ctx, subnet, tunnelIface); err != nil {
				s.log.Errorf("staticroute: add route %s: %v", subnet, err)
			}
		}
	}
	return nil
}

// OnTunnelStop removes or keeps routes when a tunnel stops.
// For NDMS-managed tunnels this is a no-op (NDMS "auto" flag handles it).
// For OS4 kernel tunnels:
//   - fallback="" (bypass): remove routes so traffic falls back to WAN
//   - fallback="reject": keep routes — dead interface acts as blackhole
func (s *ServiceImpl) OnTunnelStop(ctx context.Context, tunnelID string) error {
	if !isOS4Kernel(tunnelID) {
		return nil // NDMS "auto" flag handles it
	}

	// If the interface is already gone, the kernel has already removed the routes.
	if !s.ifaceExists(tunnelID) { // OS4: tunnelID == ifaceName
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.listsForTunnel(tunnelID)
	for _, rl := range lists {
		if rl.Fallback == "reject" {
			continue // keep routes — blackhole via dead interface
		}
		s.removeRoutes(ctx, rl.TunnelID, rl.Subnets)
	}
	return nil
}

// OnTunnelDelete removes all routes and route lists for a deleted tunnel.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.allListsForTunnel(tunnelID)
	if len(lists) == 0 {
		return nil
	}

	// Remove active routes (NDMS or ip route).
	if !isOS4Kernel(tunnelID) {
		for _, rl := range lists {
			if rl.Enabled {
				s.removeRoutes(ctx, rl.TunnelID, rl.Subnets)
			}
		}
		s.save(ctx)
	}
	// OS4 kernel: routes are already gone (kernel cleans up on interface destroy).

	// Delete route lists from storage so they don't become orphaned.
	for _, rl := range lists {
		if err := s.store.DeleteRouteList(rl.ID); err != nil {
			s.log.Errorf("staticroute: delete list %s: %v", rl.ID, err)
		}
	}

	s.log.Infof("staticroute: tunnel %s deleted, removed %d route lists", tunnelID, len(lists))
	return nil
}

// --- Reconcile ---

// Reconcile re-applies all enabled route lists.
// For NDMS-managed tunnels, routes are applied unconditionally (NDMS "auto" flag
// ensures they only activate when the interface is up).
// For OS4 kernel tunnels, routes are only applied if the interface exists
// (checked via /sys/class/net/).
func (s *ServiceImpl) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.store.ListRouteLists()
	if err != nil {
		return fmt.Errorf("reconcile: list route lists: %w", err)
	}

	var totalRoutes int
	var hasNDMSRoutes bool
	for _, rl := range all {
		if !rl.Enabled {
			continue
		}
		if isOS4Kernel(rl.TunnelID) {
			// OS4 kernel: only apply if interface exists (tunnel is running)
			if !s.ifaceExists(rl.TunnelID) { // OS4: tunnelID == ifaceName
				continue
			}
		} else {
			hasNDMSRoutes = true
		}
		s.applyRoutes(ctx, rl)
		totalRoutes += len(rl.Subnets)
	}

	if hasNDMSRoutes {
		s.save(ctx)
	}
	s.log.Infof("staticroute: reconcile complete, applied %d routes", totalRoutes)
	s.appLog.Debug("reconcile", "", "Reconciling static routes")
	return nil
}

// defaultIfaceExists checks if a network interface exists via net.InterfaceByName.
func defaultIfaceExists(ifaceName string) bool {
	_, err := net.InterfaceByName(ifaceName)
	return err == nil
}

// --- Internal helpers ---

// isOS4Kernel returns true if tunnelID refers to an OS4 kernel tunnel (awgmX).
// These tunnels have no NDMS representation — routes must use ip route directly.
func isOS4Kernel(tunnelID string) bool {
	names := tunnel.NewNames(tunnelID)
	return names.NDMSName == ""
}

// parseCIDR splits a CIDR string into network and mask.
// Returns ("1.2.3.4", "", nil) for /32 host routes (use CmdAddHostRoute).
// Returns ("10.0.0.0", "255.255.255.0", nil) for subnet routes.
func parseCIDR(cidr string) (network, mask string, err error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", "", err
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 {
		return "", "", fmt.Errorf("IPv6 subnets not supported: %s", cidr)
	}
	if ones == 32 {
		return ipNet.IP.String(), "", nil // host route
	}
	return ipNet.IP.String(), net.IP(ipNet.Mask).String(), nil
}

// addRoute adds a single static route.
// OS4 kernel tunnels use ip route; all others use NDMS RCI.
func (s *ServiceImpl) addRoute(ctx context.Context, subnet, ifaceName, fallback string, os4kernel bool) error {
	if os4kernel {
		return s.ipRouteAdd(ctx, subnet, ifaceName)
	}
	network, mask, err := parseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("parse CIDR %s: %w", subnet, err)
	}
	reject := fallback == "reject"
	var cmd any
	if mask == "" {
		cmd = rci.CmdAddHostRoute(network, ifaceName, reject)
	} else {
		cmd = rci.CmdAddStaticRoute(network, mask, ifaceName, reject)
	}
	if _, err := s.ndms.RCIPost(ctx, cmd); err != nil {
		return fmt.Errorf("RCI add route %s via %s: %w", subnet, ifaceName, err)
	}
	return nil
}

// removeRoute removes a single static route.
// OS4 kernel tunnels use ip route; all others use NDMS RCI.
func (s *ServiceImpl) removeRoute(ctx context.Context, subnet, ifaceName string, os4kernel bool) error {
	if os4kernel {
		return s.ipRouteDel(ctx, subnet, ifaceName)
	}
	network, mask, err := parseCIDR(subnet)
	if err != nil {
		return err
	}
	var cmd any
	if mask == "" {
		cmd = rci.CmdRemoveStaticHostRoute(network, ifaceName)
	} else {
		cmd = rci.CmdRemoveStaticRoute(network, mask, ifaceName)
	}
	if _, err := s.ndms.RCIPost(ctx, cmd); err != nil {
		return fmt.Errorf("RCI remove route %s via %s: %w", subnet, ifaceName, err)
	}
	return nil
}

// ipRouteAdd adds a route via ip route replace.
func (s *ServiceImpl) ipRouteAdd(ctx context.Context, subnet, ifaceName string) error {
	result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "replace", subnet, "dev", ifaceName)
	if err != nil {
		return fmt.Errorf("ip route replace %s dev %s: %w", subnet, ifaceName, exec.FormatError(result, err))
	}
	return nil
}

// ipRouteDel removes a route via ip route del.
func (s *ServiceImpl) ipRouteDel(ctx context.Context, subnet, ifaceName string) error {
	result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "del", subnet, "dev", ifaceName)
	if err != nil {
		return fmt.Errorf("ip route del %s dev %s: %w", subnet, ifaceName, exec.FormatError(result, err))
	}
	return nil
}

// applyRoutes adds static routes for a route list.
// For OS4 kernel tunnels, silently skips if the interface doesn't exist
// (routes will be applied later by OnTunnelStart).
func (s *ServiceImpl) applyRoutes(ctx context.Context, rl storage.StaticRouteList) {
	os4k := isOS4Kernel(rl.TunnelID)
	if os4k && !s.ifaceExists(rl.TunnelID) {
		s.log.Debugf("staticroute: skip apply for %s (interface not up, will apply on start)", rl.TunnelID)
		return
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, rl.TunnelID)
	if err != nil {
		s.log.Errorf("staticroute: resolve interface name for %s: %v", rl.TunnelID, err)
		return
	}
	for _, subnet := range rl.Subnets {
		if err := s.addRoute(ctx, subnet, ifaceName, rl.Fallback, os4k); err != nil {
			s.log.Errorf("staticroute: add route %s: %v", subnet, err)
		}
	}
}

// removeRoutes removes static routes for a tunnel.
// For OS4 kernel tunnels, skips if the interface doesn't exist (kernel already cleaned up).
func (s *ServiceImpl) removeRoutes(ctx context.Context, tunnelID string, subnets []string) {
	os4k := isOS4Kernel(tunnelID)
	if os4k && !s.ifaceExists(tunnelID) {
		return // kernel already removed routes when interface was destroyed
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, tunnelID)
	if err != nil {
		s.log.Errorf("staticroute: resolve interface name for %s: %v", tunnelID, err)
		return
	}
	for _, subnet := range subnets {
		if err := s.removeRoute(ctx, subnet, ifaceName, os4k); err != nil {
			s.log.Debugf("staticroute: remove route %s: %v", subnet, err)
		}
	}
}

// save persists NDMS configuration. Errors are logged but not propagated.
func (s *ServiceImpl) save(ctx context.Context) {
	if err := s.ndms.Save(ctx); err != nil {
		s.log.Errorf("staticroute: NDMS save: %v", err)
	}
}

// allListsForTunnel returns all route lists (enabled and disabled) for a given tunnel.
func (s *ServiceImpl) allListsForTunnel(tunnelID string) []storage.StaticRouteList {
	all, err := s.store.ListRouteLists()
	if err != nil {
		return nil
	}
	var result []storage.StaticRouteList
	for _, rl := range all {
		if rl.TunnelID == tunnelID {
			result = append(result, rl)
		}
	}
	return result
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

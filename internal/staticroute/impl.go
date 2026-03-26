package staticroute

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// ndmsClient is the subset of ndms.Client needed for static routes.
type ndmsClient interface {
	RCIPost(ctx context.Context, payload interface{}) (json.RawMessage, error)
	Save(ctx context.Context) error
}

// wanModel resolves kernel interface names to NDMS IDs.
type wanModel interface {
	IDFor(kernelName string) string
}

// ServiceImpl is the concrete implementation of the static route Service.
type ServiceImpl struct {
	store       *storage.StaticRouteStore
	tunnelStore *storage.AWGTunnelStore
	ndms        ndmsClient
	wanModel    wanModel
	log         *logger.Logger
	appLog      *logging.ScopedLogger
	mu          sync.Mutex
}

// New creates a new static route service.
func New(
	store *storage.StaticRouteStore,
	tunnelStore *storage.AWGTunnelStore,
	ndmsClient ndmsClient,
	wanModel wanModel,
	log *logger.Logger,
	appLogger logging.AppLogger,
) *ServiceImpl {
	return &ServiceImpl{
		store:       store,
		tunnelStore: tunnelStore,
		ndms:        ndmsClient,
		wanModel:    wanModel,
		log:         log,
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubStaticRoute),
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

	s.save(ctx)
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

	s.save(ctx)
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

	s.save(ctx)
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

// OnTunnelStart is a no-op. NDMS "auto" flag ensures routes activate when the interface comes up.
func (s *ServiceImpl) OnTunnelStart(ctx context.Context, tunnelID, tunnelIface string) error {
	return nil
}

// OnTunnelStop is a no-op. NDMS "auto" flag ensures routes deactivate when the interface goes down.
func (s *ServiceImpl) OnTunnelStop(ctx context.Context, tunnelID string) error {
	return nil
}

// OnTunnelDelete removes all routes for a deleted tunnel and saves NDMS config.
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.listsForTunnel(tunnelID)
	if len(lists) == 0 {
		return nil
	}

	for _, rl := range lists {
		s.removeRoutes(ctx, rl.TunnelID, rl.Subnets)
	}

	s.save(ctx)
	s.log.Infof("staticroute: tunnel %s deleted, removed routes from %d lists", tunnelID, len(lists))
	return nil
}

// --- Reconcile ---

// Reconcile re-applies all enabled route lists via NDMS RCI.
// NDMS "auto" flag ensures routes only activate when the interface is up.
func (s *ServiceImpl) Reconcile(ctx context.Context, runningTunnels map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	all, err := s.store.ListRouteLists()
	if err != nil {
		return fmt.Errorf("reconcile: list route lists: %w", err)
	}

	var totalRoutes int
	for _, rl := range all {
		if !rl.Enabled {
			continue
		}
		s.applyRoutes(ctx, rl)
		totalRoutes += len(rl.Subnets)
	}

	s.save(ctx)
	s.log.Infof("staticroute: reconcile complete, applied %d routes", totalRoutes)
	s.appLog.Debug("reconcile", "", "Reconciling static routes")
	return nil
}

// --- Internal helpers ---

// resolveNDMSName resolves a tunnelID to an NDMS interface name for RCI commands.
func (s *ServiceImpl) resolveNDMSName(tunnelID string) (string, error) {
	// WAN: "wan:ppp0" → look up NDMS ID via WAN model
	if strings.HasPrefix(tunnelID, "wan:") {
		kernelName := strings.TrimPrefix(tunnelID, "wan:")
		if s.wanModel == nil {
			return "", fmt.Errorf("WAN model not available")
		}
		if ndmsID := s.wanModel.IDFor(kernelName); ndmsID != "" {
			return ndmsID, nil
		}
		return "", fmt.Errorf("WAN interface %s not found in model", kernelName)
	}

	// System tunnel: "system:Wireguard0" → "Wireguard0"
	if tunnel.IsSystemTunnel(tunnelID) {
		return tunnel.SystemTunnelName(tunnelID), nil
	}

	// Managed tunnel: check backend for NativeWG
	if s.tunnelStore != nil {
		if stored, err := s.tunnelStore.Get(tunnelID); err == nil && stored.Backend == "nativewg" {
			return nwg.NewNWGNames(stored.NWGIndex).NDMSName, nil
		}
	}

	// Kernel tunnel: awg10 → OpkgTun10
	return tunnel.NewNames(tunnelID).NDMSName, nil
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

// addRoute adds a single static route via NDMS RCI.
func (s *ServiceImpl) addRoute(ctx context.Context, subnet, ndmsName, fallback string) error {
	network, mask, err := parseCIDR(subnet)
	if err != nil {
		return fmt.Errorf("parse CIDR %s: %w", subnet, err)
	}
	reject := fallback == "reject"
	var cmd any
	if mask == "" {
		cmd = rci.CmdAddHostRoute(network, ndmsName, reject)
	} else {
		cmd = rci.CmdAddStaticRoute(network, mask, ndmsName, reject)
	}
	if _, err := s.ndms.RCIPost(ctx, cmd); err != nil {
		return fmt.Errorf("RCI add route %s via %s: %w", subnet, ndmsName, err)
	}
	return nil
}

// removeRoute removes a single static route via NDMS RCI.
func (s *ServiceImpl) removeRoute(ctx context.Context, subnet, ndmsName string) error {
	network, mask, err := parseCIDR(subnet)
	if err != nil {
		return err
	}
	var cmd any
	if mask == "" {
		cmd = rci.CmdRemoveStaticHostRoute(network, ndmsName)
	} else {
		cmd = rci.CmdRemoveStaticRoute(network, mask, ndmsName)
	}
	if _, err := s.ndms.RCIPost(ctx, cmd); err != nil {
		return fmt.Errorf("RCI remove route %s via %s: %w", subnet, ndmsName, err)
	}
	return nil
}

// applyRoutes adds static routes for a route list via NDMS RCI.
func (s *ServiceImpl) applyRoutes(ctx context.Context, rl storage.StaticRouteList) {
	ndmsName, err := s.resolveNDMSName(rl.TunnelID)
	if err != nil {
		s.log.Errorf("staticroute: resolve NDMS name for %s: %v", rl.TunnelID, err)
		return
	}
	for _, subnet := range rl.Subnets {
		if err := s.addRoute(ctx, subnet, ndmsName, rl.Fallback); err != nil {
			s.log.Errorf("staticroute: add route %s: %v", subnet, err)
		}
	}
}

// removeRoutes removes static routes for a tunnel via NDMS RCI.
func (s *ServiceImpl) removeRoutes(ctx context.Context, tunnelID string, subnets []string) {
	ndmsName, err := s.resolveNDMSName(tunnelID)
	if err != nil {
		s.log.Errorf("staticroute: resolve NDMS name for %s: %v", tunnelID, err)
		return
	}
	for _, subnet := range subnets {
		if err := s.removeRoute(ctx, subnet, ndmsName); err != nil {
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

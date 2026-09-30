package staticroute

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/routing"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

// ifaceConfirmer — подтверждение NDMS-интерфейсов свежим полным списком
// (F546); реализует *query.InterfaceStore.
type ifaceConfirmer interface {
	ConfirmEach(ctx context.Context, names []string) (map[string]query.Confirmed, error)
}

// ServiceImpl is the concrete implementation of the static route Service.
type ServiceImpl struct {
	store   *storage.StaticRouteStore
	routes  *command.RouteCommands
	ifaces  ifaceConfirmer
	catalog routing.Catalog
	appLog  *logging.ScopedLogger
	mu      sync.Mutex

	// ifaceExists checks whether a network interface exists. Defaults to
	// net.InterfaceByName; override in tests.
	ifaceExists func(name string) bool
}

// New creates a new static route service.
func New(
	store *storage.StaticRouteStore,
	routes *command.RouteCommands,
	ifaces ifaceConfirmer,
	catalog routing.Catalog,
	appLogger logging.AppLogger,
) *ServiceImpl {
	return &ServiceImpl{
		store:       store,
		routes:      routes,
		ifaces:      ifaces,
		catalog:     catalog,
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
		confirmed, cerr := s.confirmIfaces(ctx, rl.TunnelID)
		s.applyRoutes(ctx, rl, confirmed, cerr)
	}

	return &rl, nil
}

// Update validates and replaces an existing static route list.
func (s *ServiceImpl) Update(ctx context.Context, rl storage.StaticRouteList) (*storage.StaticRouteList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old, err := s.store.GetRouteList(rl.ID)
	if err != nil {
		return nil, fmt.Errorf("update route list: get existing: %w", err)
	}

	// Defense-in-depth for partial updates: Go's json decoder cannot
	// distinguish "field absent" from "field present with zero value", so
	// a payload missing any field decodes into a StaticRouteList where
	// that field is zero. Treat zero values as "not sent" and restore
	// from the existing record. Same policy as dnsroute.Update — empty
	// Name is not a valid user intent, and clearing Subnets would make
	// the list useless.
	if rl.Name == "" {
		rl.Name = old.Name
	}
	if rl.TunnelID == "" {
		rl.TunnelID = old.TunnelID
	}
	if rl.Subnets == nil {
		rl.Subnets = old.Subnets
	}
	// Enabled is a bool — can't distinguish "sent false" from "not sent".
	// For bulk partial updates (e.g. change tunnel only), the caller must
	// send the full object via spread. Full-list PUT semantics work.
	rl.CreatedAt = old.CreatedAt

	if err := validateRouteList(rl); err != nil {
		return nil, err
	}

	rl.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := s.store.UpdateRouteList(rl); err != nil {
		return nil, fmt.Errorf("update route list: %w", err)
	}

	// Reconcile routes: remove old, add new. Интерфейсы обоих туннелей —
	// одним списком на вызов.
	var ids []string
	if old.Enabled {
		ids = append(ids, old.TunnelID)
	}
	if rl.Enabled {
		ids = append(ids, rl.TunnelID)
	}
	confirmed, cerr := s.confirmIfaces(ctx, ids...)
	if old.Enabled {
		s.removeRoutes(ctx, old.TunnelID, old.Subnets, confirmed, cerr)
	}
	if rl.Enabled {
		s.applyRoutes(ctx, rl, confirmed, cerr)
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
		confirmed, cerr := s.confirmIfaces(ctx, existing.TunnelID)
		s.removeRoutes(ctx, existing.TunnelID, existing.Subnets, confirmed, cerr)
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

	confirmed, cerr := s.confirmIfaces(ctx, rl.TunnelID)
	if enabled {
		s.applyRoutes(ctx, *rl, confirmed, cerr)
	} else {
		s.removeRoutes(ctx, rl.TunnelID, rl.Subnets, confirmed, cerr)
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
		s.appLog.Warn("import", name, fmt.Sprintf("%d parse errors (first: %s)", len(parseErrors), parseErrors[0]))
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
			cidr, _ := ParseSubnetComment(subnet)
			if err := s.ipRouteAdd(ctx, cidr, tunnelIface); err != nil {
				s.appLog.Warn("add-route", cidr, err.Error())
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
		s.removeRoutes(ctx, rl.TunnelID, rl.Subnets, nil, nil) // OS4: NDMS не участвует
	}
	return nil
}

// OnTunnelDelete uninstalls any kernel/NDMS state for the tunnel's route
// lists and then orphans those lists (TunnelID = "") rather than deleting
// them. The user curated the CIDRs — they should survive a tunnel delete
// and be reassignable to another tunnel from the UI. Reconcile skips
// orphan lists; the frontend surfaces them as "Без туннеля".
func (s *ServiceImpl) OnTunnelDelete(ctx context.Context, tunnelID string) error {
	if tunnelID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.allListsForTunnel(tunnelID)
	if len(lists) == 0 {
		return nil
	}

	// Uninstall active NDMS routes first (OS4 kernel already cleaned up on
	// interface destroy).
	if !isOS4Kernel(tunnelID) {
		confirmed, cerr := s.confirmIfaces(ctx, tunnelID)
		for _, rl := range lists {
			if rl.Enabled {
				s.removeRoutes(ctx, rl.TunnelID, rl.Subnets, confirmed, cerr)
			}
		}
	}

	// Unbind: clear TunnelID but keep the list in storage.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rl := range lists {
		rl.TunnelID = ""
		rl.UpdatedAt = now
		if err := s.store.UpdateRouteList(rl); err != nil {
			s.appLog.Warn("orphan-list", rl.ID, err.Error())
		}
	}

	s.appLog.Info("tunnel-deleted", tunnelID, fmt.Sprintf("%d route lists orphaned", len(lists)))
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

	var active []storage.StaticRouteList
	var ids []string
	for _, rl := range all {
		if !rl.Enabled {
			continue
		}
		// Orphan list awaiting reassignment — nothing to install.
		if rl.TunnelID == "" {
			continue
		}
		if isOS4Kernel(rl.TunnelID) {
			// OS4 kernel: only apply if interface exists (tunnel is running)
			if !s.ifaceExists(rl.TunnelID) { // OS4: tunnelID == ifaceName
				continue
			}
		}
		active = append(active, rl)
		ids = append(ids, rl.TunnelID)
	}
	confirmed, cerr := s.confirmIfaces(ctx, ids...)
	var totalRoutes int
	for _, rl := range active {
		s.applyRoutes(ctx, rl, confirmed, cerr)
		totalRoutes += len(rl.Subnets)
	}

	s.appLog.Info("reconcile", "", fmt.Sprintf("complete, applied %d routes", totalRoutes))
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
// Returns ("1.2.3.4", "", nil) for /32 host routes.
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
// OS4 kernel tunnels use ip route; all others use NDMS RouteCommands по
// подтверждённому iface (у OS4 — нулевое, не используется).
func (s *ServiceImpl) addRoute(ctx context.Context, subnet, ifaceName string, iface query.Confirmed, fallback string, os4kernel bool) error {
	cidr, comment := ParseSubnetComment(subnet)
	if os4kernel {
		return s.ipRouteAdd(ctx, cidr, ifaceName)
	}
	network, mask, err := parseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parse CIDR %s: %w", cidr, err)
	}
	spec := command.StaticRouteSpec{
		Interface: iface,
		Reject:    fallback == "reject",
		Comment:   comment,
	}
	if mask == "" {
		spec.Host = network
	} else {
		spec.Network = network
		spec.Mask = mask
	}
	if err := s.routes.AddStaticRoute(ctx, spec); err != nil {
		return fmt.Errorf("add route %s via %s: %w", cidr, ifaceName, err)
	}
	return nil
}

// removeRoute removes a single static route.
// OS4 kernel tunnels use ip route; all others use NDMS RouteCommands по
// подтверждённому iface (у OS4 — нулевое, не используется).
func (s *ServiceImpl) removeRoute(ctx context.Context, subnet, ifaceName string, iface query.Confirmed, os4kernel bool) error {
	cidr, _ := ParseSubnetComment(subnet)
	if os4kernel {
		return s.ipRouteDel(ctx, cidr, ifaceName)
	}
	network, mask, err := parseCIDR(cidr)
	if err != nil {
		return err
	}
	spec := command.StaticRouteSpec{Interface: iface}
	if mask == "" {
		spec.Host = network
	} else {
		spec.Network = network
		spec.Mask = mask
	}
	if err := s.routes.RemoveStaticRoute(ctx, spec); err != nil {
		return fmt.Errorf("remove route %s via %s: %w", cidr, ifaceName, err)
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

// confirmIfaces подтверждает NDMS-интерфейсы туннелей ОДНИМ свежим списком на
// весь публичный вызов (F546). OS4-ядерные туннели в NDMS не живут, пустые и
// нерезолвящиеся пропускаются (applyRoutes/removeRoutes разберутся с ними
// сами); нет ни одного NDMS-имени — список не читается.
func (s *ServiceImpl) confirmIfaces(ctx context.Context, tunnelIDs ...string) (map[string]query.Confirmed, error) {
	var names []string
	for _, id := range tunnelIDs {
		if id == "" || isOS4Kernel(id) {
			continue
		}
		if name, err := s.catalog.ResolveInterface(ctx, id); err == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	return s.ifaces.ConfirmEach(ctx, names)
}

// applyRoutes adds static routes for a route list.
// For OS4 kernel tunnels, silently skips if the interface doesn't exist
// (routes will be applied later by OnTunnelStart). NDMS-маршрут ставится
// только на интерфейс из confirmed (confirmIfaces): ссылка ip route на
// отсутствующий пишет E в журнал ndm; список не прочитан (cerr) — ничего.
func (s *ServiceImpl) applyRoutes(ctx context.Context, rl storage.StaticRouteList, confirmed map[string]query.Confirmed, cerr error) {
	os4k := isOS4Kernel(rl.TunnelID)
	if os4k && !s.ifaceExists(rl.TunnelID) {
		s.appLog.Debug("apply", rl.TunnelID, "skip — interface not up, will apply on start")
		return
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, rl.TunnelID)
	if err != nil {
		s.appLog.Warn("resolve-interface", rl.TunnelID, err.Error())
		return
	}
	var iface query.Confirmed
	if !os4k {
		if cerr != nil {
			s.appLog.Warn("apply", ifaceName, cerr.Error())
			return
		}
		var ok bool
		if iface, ok = confirmed[ifaceName]; !ok {
			s.appLog.Warn("apply", ifaceName, "интерфейса нет в NDMS — маршруты списка "+rl.ID+" не поставлены")
			return
		}
	}
	for _, subnet := range rl.Subnets {
		if err := s.addRoute(ctx, subnet, ifaceName, iface, rl.Fallback, os4k); err != nil {
			s.appLog.Warn("add-route", subnet, err.Error())
		}
	}
}

// removeRoutes removes static routes for a tunnel.
// For OS4 kernel tunnels, skips if the interface doesn't exist (kernel already cleaned up).
// NDMS: интерфейса нет в confirmed — маршруты ушли вместе с ним, снимать
// нечего; список не прочитан (cerr) — ничего не шлём.
func (s *ServiceImpl) removeRoutes(ctx context.Context, tunnelID string, subnets []string, confirmed map[string]query.Confirmed, cerr error) {
	os4k := isOS4Kernel(tunnelID)
	if os4k && !s.ifaceExists(tunnelID) {
		return // kernel already removed routes when interface was destroyed
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, tunnelID)
	if err != nil {
		s.appLog.Warn("resolve-interface", tunnelID, err.Error())
		return
	}
	var iface query.Confirmed
	if !os4k {
		if cerr != nil {
			s.appLog.Warn("remove", ifaceName, cerr.Error())
			return
		}
		var ok bool
		if iface, ok = confirmed[ifaceName]; !ok {
			s.appLog.Debug("remove", ifaceName, "интерфейса нет в NDMS — снимать нечего")
			return
		}
	}
	for _, subnet := range subnets {
		if err := s.removeRoute(ctx, subnet, ifaceName, iface, os4k); err != nil {
			s.appLog.Debug("remove-route", subnet, err.Error())
		}
	}
}

// allListsForTunnel returns all route lists (enabled and disabled) for a
// given tunnel. Returns nil for empty tunnelID — orphan lists don't
// belong to any tunnel and must not match here.
func (s *ServiceImpl) allListsForTunnel(tunnelID string) []storage.StaticRouteList {
	if tunnelID == "" {
		return nil
	}
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
// Returns nil for empty tunnelID (orphan lists don't belong to any tunnel).
func (s *ServiceImpl) listsForTunnel(tunnelID string) []storage.StaticRouteList {
	if tunnelID == "" {
		return nil
	}
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

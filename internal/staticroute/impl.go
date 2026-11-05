package staticroute

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
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

// routeReader — свежее чтение /show/rc/ip/route (реализует
// *query.StaticRouteStore): старт OS5-туннеля и Reconcile ставят только
// недостающие.
type routeReader interface {
	Fetch(ctx context.Context) ([]query.StaticRouteEntry, error)
}

// ServiceImpl is the concrete implementation of the static route Service.
type ServiceImpl struct {
	store   *storage.StaticRouteStore
	routes  *command.RouteCommands
	ifaces  ifaceConfirmer
	rc      routeReader // nil — старт ставит всё (R37b follow-up 2)
	catalog routing.Catalog
	appLog  *logging.ScopedLogger
	mu      sync.Mutex

	// pending — включённые списки OS5-туннеля ядра, чьей записи OpkgTun ещё
	// нет: маршруты встанут в OnTunnelStart (R37b). Память, не хранилище:
	// после рестарта демона Reconcile на загрузке заполняет её заново.
	pendMu  sync.Mutex
	pending map[string]bool

	// ifaceExists checks whether a network interface exists. Defaults to
	// net.InterfaceByName; override in tests.
	ifaceExists func(name string) bool
}

// New creates a new static route service.
func New(
	store *storage.StaticRouteStore,
	routes *command.RouteCommands,
	ifaces ifaceConfirmer,
	rc routeReader,
	catalog routing.Catalog,
	appLogger logging.AppLogger,
) *ServiceImpl {
	return &ServiceImpl{
		store:       store,
		routes:      routes,
		ifaces:      ifaces,
		rc:          rc,
		catalog:     catalog,
		appLog:      logging.NewScopedLogger(appLogger, logging.GroupRouting, logging.SubStaticRoute),
		ifaceExists: defaultIfaceExists,
	}
}

// --- CRUD ---

// PendingIDs — списки, ждущие старта туннеля (R37b): статус для API.
func (s *ServiceImpl) PendingIDs() map[string]bool {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	out := make(map[string]bool, len(s.pending))
	for id := range s.pending {
		out[id] = true
	}
	return out
}

func (s *ServiceImpl) setPending(id string, on bool) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if !on {
		delete(s.pending, id)
		return
	}
	if s.pending == nil {
		s.pending = map[string]bool{}
	}
	s.pending[id] = true
}

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

	// Маршруты — ДО записи: отказ откатывает поставленное, список не
	// сохраняется (F565).
	var applied []string
	var confirmed map[string]query.Confirmed
	if rl.Enabled {
		var cerr, err error
		confirmed, cerr = s.confirmIfaces(ctx, rl.TunnelID)
		if applied, err = s.applyRoutes(ctx, rl, confirmed, cerr); err != nil {
			s.rollbackApplied(ctx, rl, applied, confirmed)
			return nil, fmt.Errorf("create route list: %w", err)
		}
	}

	if err := s.store.AddRouteList(rl); err != nil {
		s.setPending(rl.ID, false)
		s.rollbackApplied(ctx, rl, applied, confirmed)
		return nil, fmt.Errorf("create route list: %w", err)
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

	// Reconcile routes: remove old, add new. Интерфейсы обоих туннелей —
	// одним списком на вызов. Список не прочитан — маршруты не применить:
	// отказ ДО записи, иначе хранилище и роутер расходятся (F565).
	var ids []string
	if old.Enabled {
		ids = append(ids, old.TunnelID)
	}
	if rl.Enabled {
		ids = append(ids, rl.TunnelID)
	}
	confirmed, cerr := s.confirmIfaces(ctx, ids...)
	if cerr != nil {
		return nil, fmt.Errorf("update route list: %w", cerr)
	}

	if old.Enabled {
		_ = s.removeRoutes(ctx, old.TunnelID, old.Subnets, confirmed, cerr) // отказ в журнале; откат ниже и так ставит прежнее
	}
	var applied []string
	if rl.Enabled {
		applied, err = s.applyRoutes(ctx, rl, confirmed, cerr)
	} else {
		s.setPending(rl.ID, false)
	}
	if err == nil {
		err = s.store.UpdateRouteList(rl)
	}
	if err != nil {
		// Откат к прежнему списку: запись не менялась, роутер — тоже (F565).
		s.rollbackApplied(ctx, rl, applied, confirmed)
		if old.Enabled {
			if _, rbErr := s.applyRoutes(ctx, *old, confirmed, cerr); rbErr != nil {
				err = errors.Join(err, fmt.Errorf("откат к прежнему списку: %w", rbErr))
			}
		}
		return nil, fmt.Errorf("update route list: %w", err)
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
		// Список не прочитан — маршруты не сняты; запись остаётся для повтора:
		// без неё маршруты в NDMS остались бы сиротами (Reconcile только ставит).
		if cerr != nil {
			return fmt.Errorf("delete route list: %w", cerr)
		}
		// Пакет снятия не дошёл — запись остаётся для повтора, по той же причине.
		if err := s.removeRoutes(ctx, existing.TunnelID, existing.Subnets, confirmed, cerr); err != nil {
			return fmt.Errorf("delete route list: %w", err)
		}
	}

	if err := s.store.DeleteRouteList(id); err != nil {
		return fmt.Errorf("delete route list: %w", err)
	}
	s.setPending(id, false)

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

	// Список не прочитан — отказ ДО записи (F565), как в Update.
	confirmed, cerr := s.confirmIfaces(ctx, rl.TunnelID)
	if cerr != nil {
		return fmt.Errorf("set enabled: %w", cerr)
	}

	next := *rl
	next.Enabled = enabled
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	// Включение — маршруты ДО записи: отказ откатывает поставленное (F565).
	// Выключение тоже: пакет снятия не дошёл — список остаётся включённым, снятое
	// вернёт Reconcile, а выключенный с маршрутами на роутере не снял бы никто.
	var applied []string
	if enabled {
		if applied, err = s.applyRoutes(ctx, next, confirmed, cerr); err != nil {
			s.rollbackApplied(ctx, next, applied, confirmed)
			return fmt.Errorf("set enabled: %w", err)
		}
	} else if err := s.removeRoutes(ctx, next.TunnelID, next.Subnets, confirmed, cerr); err != nil {
		return fmt.Errorf("set enabled: %w", err)
	}

	if err := s.store.UpdateRouteList(next); err != nil {
		s.rollbackApplied(ctx, next, applied, confirmed)
		return fmt.Errorf("set enabled: save: %w", err)
	}

	if !enabled {
		s.setPending(id, false)
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
		return s.applyOnStart(ctx, tunnelID)
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

// applyOnStart ставит ВСЕ включённые списки OS5-туннеля ядра при его старте
// (R37b): запись OpkgTun могла быть создана только что (списки ждали старта)
// или пересоздана после внешнего снятия — NDMS сносит маршруты вместе с
// записью, и Reconcile вернул бы их лишь на загрузке или переподключении.
// Одно подтверждение свежим списком на все. Ставятся только недостающие:
// старт повторяется (перезапуски ping-check), а каждая команда — запрос
// сохранения, то есть запись во флеш. Нет недостающих — ни команды, ни
// сохранения. Маршруты не прочитались — ставим всё (лишнее сохранение
// дешевле потерянной связности). Прочим NDMS-туннелям делать нечего: их
// запись живёт и у остановленного, маршруты — в NDMS («auto»).
func (s *ServiceImpl) applyOnStart(ctx context.Context, tunnelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.listsForTunnel(tunnelID)
	if len(lists) == 0 {
		return nil
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, tunnelID)
	if err != nil || !isOS5Kernel(tunnelID, ifaceName) {
		return nil
	}
	confirmed, cerr := s.confirmIfaces(ctx, tunnelID)
	present, perr := s.presentRoutes(ctx)
	if perr != nil {
		s.appLog.Warn("apply-on-start", ifaceName, "маршруты не прочитаны — ставим все: "+perr.Error())
	}
	claimed := map[string]bool{}
	for _, rl := range lists {
		if present != nil {
			rl.Subnets = missingSubnets(rl, ifaceName, present, claimed) // пустой — только подтверждение, без команд
		}
		if _, err := s.applyRoutes(ctx, rl, confirmed, cerr); err != nil {
			s.appLog.Warn("apply-on-start", rl.ID, err.Error())
		}
	}
	return nil
}

// routeState — изменяемые поля записи rc, которые сверяются со списком.
// Повторная установка их перезаписывает (стенд 5.02.B.2: «renewed»).
type routeState struct {
	reject  bool
	comment string
}

// presentRoutes — маршруты по интерфейсам из свежего чтения; nil без ошибки —
// читать нечем.
func (s *ServiceImpl) presentRoutes(ctx context.Context) (map[string]map[string]routeState, error) {
	if s.rc == nil {
		return nil, nil
	}
	entries, err := s.rc.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]routeState{}
	for _, e := range entries {
		routes := out[e.Interface]
		if routes == nil {
			routes = map[string]routeState{}
			out[e.Interface] = routes
		}
		state := routeState{reject: e.Reject, comment: e.Comment}
		if e.Host != "" {
			routes["h:"+e.Host] = state
		} else {
			routes["n:"+e.Network+"/"+e.Mask] = state
		}
	}
	return out, nil
}

// missingSubnets — подсети списка, которых нет на интерфейсе или чьи reject и
// comment расходятся с записью. NDMS держит одну запись на пару (подсеть,
// интерфейс), поэтому подсеть, занятую другим списком прохода (claimed), сверяет
// только первый: иначе два списка с разными comment или fallback перезаписывали
// бы её на каждом проходе, а это запись во флеш.
func missingSubnets(rl storage.StaticRouteList, ifaceName string, present map[string]map[string]routeState, claimed map[string]bool) []string {
	var missing []string
	for _, subnet := range rl.Subnets {
		_, comment := ParseSubnetComment(subnet)
		want := routeState{reject: rl.Fallback == "reject", comment: comment}
		key := subnetKey(subnet)
		if key != "" { // нечитаемую подсеть отвергнет applyRoutes
			if claim := ifaceName + "|" + key; claimed[claim] {
				continue
			} else {
				claimed[claim] = true
			}
		}
		if got, ok := present[ifaceName][key]; !ok || got != want {
			missing = append(missing, subnet)
		}
	}
	return missing
}

// subnetKey — ключ подсети списка в форме presentRoutes; нечитаемая — "",
// её не найдёт никто, и applyRoutes отчитается об ошибке как обычно.
func subnetKey(subnet string) string {
	cidr, _ := ParseSubnetComment(subnet)
	network, mask, err := parseCIDR(cidr)
	if err != nil {
		return ""
	}
	if mask == "" {
		return "h:" + network
	}
	return "n:" + network + "/" + mask
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
		_ = s.removeRoutes(ctx, rl.TunnelID, rl.Subnets, nil, nil) // OS4: NDMS не участвует, отказов пакета нет
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
				_ = s.removeRoutes(ctx, rl.TunnelID, rl.Subnets, confirmed, cerr) // отказ в журнале
			}
		}
	}

	// Отвязку списков (хранилище) делает ForgetTunnel — после успешного
	// удаления туннеля: отказ удаления иначе оставлял бы живой туннель без
	// правил (F609).
	return nil
}

// ForgetTunnel отвязывает списки от удалённого туннеля: TunnelID очищается,
// сам список остаётся. Только после успешного удаления туннеля (F609).
func (s *ServiceImpl) ForgetTunnel(_ context.Context, tunnelID string) error {
	if tunnelID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	lists := s.allListsForTunnel(tunnelID)
	now := time.Now().UTC().Format(time.RFC3339)
	for _, rl := range lists {
		s.setPending(rl.ID, false)
		rl.TunnelID = ""
		rl.UpdatedAt = now
		if err := s.store.UpdateRouteList(rl); err != nil {
			s.appLog.Warn("orphan-list", rl.ID, err.Error())
		}
	}
	if len(lists) > 0 {
		s.appLog.Info("tunnel-deleted", tunnelID, fmt.Sprintf("%d route lists orphaned", len(lists)))
	}
	return nil
}

// --- Reconcile ---

// Reconcile приводит включённые списки к роутеру. NDMS-маршруты живут в его
// конфигурации, поэтому ставятся только недостающие: полный проход на тысячах
// маршрутов держал ndm минутами на каждом перезапуске и запрашивал сохранение
// (#1157). Маршруты не прочитались — NDMS-списки в этом проходе пропускаются и
// вызывающий получает ошибку: ndm занят, а полная установка добавила бы ему
// нагрузки. Отступление от applyOnStart, где после пересоздания записи ставить
// надо всё.
// For OS4 kernel tunnels, routes are only applied if the interface exists
// (checked via /sys/class/net/); `ip route replace` в RCI не ходит.
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
	var present map[string]map[string]routeState
	var perr error
	claimed := map[string]bool{}
	// Интерфейсы не подтверждены — applyRoutes откажет NDMS-спискам и без чтения.
	if cerr == nil && slices.ContainsFunc(active, func(rl storage.StaticRouteList) bool { return !isOS4Kernel(rl.TunnelID) }) {
		if present, perr = s.presentRoutes(ctx); perr != nil {
			s.appLog.Warn("reconcile", "", "маршруты не прочитаны — NDMS-списки пропущены: "+perr.Error())
		}
	}
	var totalRoutes int
	for _, rl := range active {
		switch {
		case isOS4Kernel(rl.TunnelID):
		case perr != nil:
			rl.Subnets = nil // только подтверждение, без команд
		case present != nil:
			ifaceName, _ := s.catalog.ResolveInterface(ctx, rl.TunnelID) // отказ разберёт applyRoutes
			rl.Subnets = missingSubnets(rl, ifaceName, present, claimed)
		}
		_, _ = s.applyRoutes(ctx, rl, confirmed, cerr) // отказы уже в журнале
		totalRoutes += len(rl.Subnets)
	}

	if perr != nil {
		return fmt.Errorf("reconcile: read routes: %w", perr)
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

// isOS5Kernel — туннель ядра OS5 (awgN → OpkgTunN), а не NativeWG, системный
// туннель или выход прокси: только у него запись OpkgTun может ещё не
// существовать при живой карточке (R37b).
func isOS5Kernel(tunnelID, ifaceName string) bool {
	if tunnel.IsSystemTunnel(tunnelID) || !strings.HasPrefix(tunnelID, "awg") {
		return false
	}
	ndmsName := tunnel.NewNames(tunnelID).NDMSName
	return ndmsName != "" && ifaceName == ndmsName
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

// routeSpec — команда NDMS для подсети списка.
func routeSpec(subnet string, iface query.Confirmed) (command.StaticRouteSpec, error) {
	cidr, comment := ParseSubnetComment(subnet)
	network, mask, err := parseCIDR(cidr)
	if err != nil {
		return command.StaticRouteSpec{}, fmt.Errorf("parse CIDR %s: %w", cidr, err)
	}
	spec := command.StaticRouteSpec{Interface: iface, Comment: comment}
	if mask == "" {
		spec.Host = network
	} else {
		spec.Network = network
		spec.Mask = mask
	}
	return spec, nil
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
//
// Возвращает поставленные подсети и отказы (F565): отказ одной подсети
// остальные не останавливает (Reconcile), а правка списка по нему откатывает
// поставленное (rollbackApplied) и запись не сохраняет. Интерфейса нет:
// NativeWG и прочие — ошибка; OS5 ядра и OS4 — отложено до старта (R37b).
func (s *ServiceImpl) applyRoutes(ctx context.Context, rl storage.StaticRouteList, confirmed map[string]query.Confirmed, cerr error) ([]string, error) {
	os4k := isOS4Kernel(rl.TunnelID)
	if os4k && !s.ifaceExists(rl.TunnelID) {
		s.appLog.Debug("apply", rl.TunnelID, "skip — interface not up, will apply on start")
		return nil, nil
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, rl.TunnelID)
	if err != nil {
		s.appLog.Warn("resolve-interface", rl.TunnelID, err.Error())
		return nil, err
	}
	var iface query.Confirmed
	if !os4k {
		if cerr != nil {
			s.appLog.Warn("apply", ifaceName, cerr.Error())
			return nil, cerr
		}
		var ok bool
		// Раздел (R37b). OS5-туннель ядра: записи OpkgTun нет, пока туннель ни
		// разу не стартовал или её сняли снаружи (туннель тогда остановлен) —
		// список ждёт старта, его поставит OnTunnelStart, RCI сейчас нет.
		// NativeWG и прочие NDMS-интерфейсы живут и у остановленного — нет
		// записи, значит интерфейс снят: явная ошибка (решение 3). OS4 выше:
		// интерфейс есть только у работающего, установка тоже отложена.
		if iface, ok = confirmed[ifaceName]; !ok {
			if isOS5Kernel(rl.TunnelID, ifaceName) {
				s.appLog.Info("apply", ifaceName, "записи туннеля ещё нет — маршруты списка "+rl.ID+" встанут при старте туннеля")
				s.setPending(rl.ID, true)
				return nil, nil
			}
			s.appLog.Warn("apply", ifaceName, "интерфейса нет в NDMS — маршруты списка "+rl.ID+" не поставлены")
			return nil, fmt.Errorf("интерфейса %s нет в NDMS", ifaceName)
		}
		s.setPending(rl.ID, false)
	}
	var applied []string
	var errs []error
	fail := func(subnet string, err error) {
		s.appLog.Warn("add-route", subnet, err.Error())
		errs = append(errs, err)
	}
	var specs []command.StaticRouteSpec
	var subnets []string // подсети specs по индексу
	for _, subnet := range rl.Subnets {
		if os4k {
			cidr, _ := ParseSubnetComment(subnet)
			if err := s.ipRouteAdd(ctx, cidr, ifaceName); err != nil {
				fail(subnet, err)
			} else {
				applied = append(applied, subnet)
			}
			continue
		}
		spec, err := routeSpec(subnet, iface)
		if err != nil {
			fail(subnet, err)
			continue
		}
		spec.Reject = rl.Fallback == "reject"
		specs = append(specs, spec)
		subnets = append(subnets, subnet)
	}
	batchFailed := false
	for i, err := range s.routes.AddStaticRoutes(ctx, specs) {
		if err == nil {
			applied = append(applied, subnets[i])
			continue
		}
		unknown := errors.Is(err, command.ErrRouteOutcomeUnknown)
		if unknown {
			applied = append(applied, subnets[i]) // мог примениться: откату снимать
		}
		// Отказ пакета один на все его маршруты: строка на подсеть утопила бы журнал.
		if bulk := unknown || errors.Is(err, command.ErrRouteNotSent); !bulk || !batchFailed {
			batchFailed = batchFailed || bulk
			fail(subnets[i], fmt.Errorf("add route %s via %s: %w", subnets[i], ifaceName, err))
		}
	}
	return applied, errors.Join(errs...)
}

// rollbackApplied снимает подсети, поставленные неудавшейся правкой списка.
func (s *ServiceImpl) rollbackApplied(ctx context.Context, rl storage.StaticRouteList, applied []string, confirmed map[string]query.Confirmed) {
	if len(applied) > 0 {
		_ = s.removeRoutes(ctx, rl.TunnelID, applied, confirmed, nil) // откат: отказ в журнале
	}
}

// removeRoutes removes static routes for a tunnel.
// For OS4 kernel tunnels, skips if the interface doesn't exist (kernel already cleaned up).
// NDMS: интерфейса нет в confirmed — маршруты ушли вместе с ним, снимать
// нечего; список не прочитан (cerr) — ничего не шлём.
func (s *ServiceImpl) removeRoutes(ctx context.Context, tunnelID string, subnets []string, confirmed map[string]query.Confirmed, cerr error) error {
	os4k := isOS4Kernel(tunnelID)
	if os4k && !s.ifaceExists(tunnelID) {
		return nil // kernel already removed routes when interface was destroyed
	}
	ifaceName, err := s.catalog.ResolveInterface(ctx, tunnelID)
	if err != nil {
		s.appLog.Warn("resolve-interface", tunnelID, err.Error())
		return nil
	}
	var iface query.Confirmed
	if !os4k {
		if cerr != nil {
			s.appLog.Warn("remove", ifaceName, cerr.Error())
			return nil
		}
		var ok bool
		if iface, ok = confirmed[ifaceName]; !ok {
			s.appLog.Debug("remove", ifaceName, "интерфейса нет в NDMS — снимать нечего")
			return nil
		}
	}
	var specs []command.StaticRouteSpec
	var named []string // подсети specs по индексу
	for _, subnet := range subnets {
		if os4k {
			cidr, _ := ParseSubnetComment(subnet)
			if err := s.ipRouteDel(ctx, cidr, ifaceName); err != nil {
				s.appLog.Debug("remove-route", subnet, err.Error())
			}
			continue
		}
		spec, err := routeSpec(subnet, iface)
		if err != nil {
			s.appLog.Debug("remove-route", subnet, err.Error())
			continue
		}
		specs = append(specs, spec)
		named = append(named, subnet)
	}
	var batchErr error
	for i, err := range s.routes.RemoveStaticRoutes(ctx, specs) {
		switch {
		case err == nil:
		case errors.Is(err, command.ErrRouteOutcomeUnknown), errors.Is(err, command.ErrRouteNotSent):
			if batchErr == nil {
				batchErr = err
				s.appLog.Warn("remove-route", named[i], err.Error())
			}
		default:
			s.appLog.Debug("remove-route", named[i], err.Error())
		}
	}
	return batchErr
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

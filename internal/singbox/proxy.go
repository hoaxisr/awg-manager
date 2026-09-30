package singbox

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// markProxyMgrDur — ВРЕМЕННЫЙ helper для perf-diagnostics. Логирует через
// slog.Default — в этом пакете нет ScopedLogger в ProxyManager. Удалить
// после perf-сессии 2026-05-23.
func markProxyMgrDur(label string, start time.Time) {
	slog.Info("perf-proxy", "label", label, "ms", time.Since(start).Milliseconds())
}

// maxProxySlots caps how many ProxyN slots we will scan when looking for
// a free index. Keenetic does not publish an official ceiling; 128 is
// well above any realistic tunnel count and bounds the loop in NextFreeIndex
// in case NDMS ever returns something unexpected.
const maxProxySlots = 128

// TunnelInboundPortRange returns the inclusive listen-port range reserved for
// sing-box tunnel inbounds (firstPort+slot). Exported so port-conflict
// validation elsewhere reads the same numbers instead of copying them.
func TunnelInboundPortRange() (first, last int) {
	return firstPort, firstPort + maxProxySlots - 1
}

// ErrProxyComponentMissing is returned when the router lacks the NDMS
// "proxy" component. Without it, no ProxyN interface can be created, so
// sing-box cannot route any traffic. Surfaced to the UI as a distinct
// state (separate from generic RCI errors) so we can show the user how
// to fix it instead of a raw NDMS error string.
var ErrProxyComponentMissing = fmt.Errorf("NDMS 'proxy' component is not installed — sing-box integration unavailable")

// ndmsProxies — то, что Operator знает про NDMS-прокси.
type ndmsProxies interface {
	EnsureProxy(ctx context.Context, index, port int, description string) error
	NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error)
	RemoveProxy(ctx context.Context, index int) error
	RemoveOrphanSingboxProxiesOwned(ctx context.Context, tunnelTags map[string]bool, ourPortSlots map[int]bool, legacyProxyIdx map[int]bool, proxyOwners map[int]map[string]bool) error
	ListNativeProxiesOwned(ctx context.Context, tunnelTags map[string]bool, ourPortSlots map[int]bool, legacyProxyIdx map[int]bool, proxyOwners map[int]map[string]bool) ([]string, error)
	SyncProxies(ctx context.Context, tunnels []TunnelInfo) error
}

var _ ndmsProxies = (*ProxyManager)(nil)

// ProxyManager orchestrates NDMS Proxy interfaces for sing-box tunnels.
// Reads go through queries.Interfaces (GetProxy helper); writes through
// commands.Proxies.
type ProxyManager struct {
	queries        *query.Queries
	commands       *command.Commands
	allocationMu   sync.Mutex
	pending        map[int]time.Time
	reserved       func() map[int]bool
	listInterfaces func(context.Context) ([]ndms.Interface, error)
	getProxy       func(context.Context, string) (*ndms.ProxyInfo, error)
	getInterface      func(context.Context, string) (*ndms.Interface, error)
	resolveSystemName func(context.Context, string) string
	createProxy       func(context.Context, string, string, string, int, bool) error
	downProxy      func(context.Context, string) error
	deleteProxy    func(context.Context, string) error
	hasComponent   func() bool
	getProxyPort   func(context.Context, int) (int, error)
}

func NewProxyManager(q *query.Queries, c *command.Commands) *ProxyManager {
	return &ProxyManager{queries: q, commands: c, pending: make(map[int]time.Time)}
}

const proxyReservationTTL = 30 * time.Second

// SetReservedIndices supplies persisted ProxyN allocations that may be absent
// from NDMS at the moment (for example while the global Proxy toggle is off).
// Every allocator shares this callback, so a dormant subscription/Mihomo slot
// cannot be handed to another in-process subsystem.
func (pm *ProxyManager) SetReservedIndices(fn func() map[int]bool) {
	pm.allocationMu.Lock()
	pm.reserved = fn
	pm.allocationMu.Unlock()
}

// EnsureProxy creates or refreshes ProxyN pointing at 127.0.0.1:port.
// Idempotent: re-creating with same params is safe. Returns
// ErrProxyComponentMissing before talking to NDMS when the required
// component is absent.
func (pm *ProxyManager) EnsureProxy(ctx context.Context, index, port int, description string) error {
	defer markProxyMgrDur(fmt.Sprintf("EnsureProxy(%d)", index), time.Now())
	pm.allocationMu.Lock()
	defer func() {
		delete(pm.pending, index)
		pm.allocationMu.Unlock()
	}()
	return pm.ensureProxyLocked(ctx, index, port, description)
}

func (pm *ProxyManager) ensureProxyLocked(ctx context.Context, index, port int, description string) error {
	hasComponent := pm.hasComponent
	if hasComponent == nil {
		hasComponent = ndmsinfo.HasProxyComponent
	}
	if !hasComponent() {
		return ErrProxyComponentMissing
	}
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	create := pm.createProxy
	if create == nil {
		create = pm.commands.Proxies.CreateProxy
	}
	return create(ctx, name, description, "127.0.0.1", port, true)
}

// EnsureProxyIfOwned creates ProxyN when absent or refreshes it only when its
// current description proves ownership. legacyOwners are accepted solely for
// one-time migration from the former user-label description format. The
// ownership check and mutation share allocationMu with NextFreeIndex, closing
// the in-process check/create race.
func (pm *ProxyManager) EnsureProxyIfOwned(ctx context.Context, index, port int, owner string, legacyOwners ...string) (bool, error) {
	defer markProxyMgrDur(fmt.Sprintf("EnsureProxyIfOwned(%d)", index), time.Now())
	pm.allocationMu.Lock()
	defer func() {
		delete(pm.pending, index)
		pm.allocationMu.Unlock()
	}()
	description, exists, err := pm.lookupProxyLocked(ctx, index)
	if err != nil {
		return false, err
	}
	if exists && !proxyOwnerMatches(description, owner, legacyOwners) {
		return false, nil
	}
	if err := pm.ensureProxyLocked(ctx, index, port, owner); err != nil {
		return true, err
	}
	return true, nil
}

// NextFreeIndex returns the lowest ProxyN index not occupied on the
// router. The NDMS namespace is shared with whatever the user created
// manually through the router UI, so we must scan /show/interface/
// before picking a slot — otherwise CreateProxy would silently mutate
// the user's existing Proxy0. reserved lets a batch allocator skip
// indices it has already handed out earlier in the same batch, before
// those ProxyN interfaces have been committed to NDMS.
func (pm *ProxyManager) NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error) {
	defer markProxyMgrDur("NextFreeIndex", time.Now())
	// Keep the scan and in-process reservation atomic. The NDMS interface is
	// only created later by EnsureProxy, so without this pending set two
	// concurrent creators can both observe the same free ProxyN and the second
	// one silently overwrites the first.
	pm.allocationMu.Lock()
	defer pm.allocationMu.Unlock()
	list := pm.listInterfaces
	if list == nil {
		list = pm.queries.Interfaces.List
	}
	ifaces, err := list(ctx)
	if err != nil {
		return 0, fmt.Errorf("list interfaces: %w", err)
	}
	if pm.pending == nil {
		pm.pending = make(map[int]time.Time)
	}
	now := time.Now()
	for index, reservedAt := range pm.pending {
		if now.Sub(reservedAt) >= proxyReservationTTL {
			delete(pm.pending, index)
		}
	}
	used := make(map[int]bool)
	if pm.reserved != nil {
		for idx := range pm.reserved() {
			used[idx] = true
		}
	}
	for idx := range reserved {
		used[idx] = true
	}
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, err := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); err != nil || n != 1 {
			continue
		}
		used[idx] = true
	}
	for idx := range pm.pending {
		used[idx] = true
	}
	for i := 0; i < maxProxySlots; i++ {
		if !used[i] {
			pm.pending[i] = now
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free Proxy slot (scanned %d)", maxProxySlots)
}

// ReleaseProxyIndex abandons an in-process allocation reservation. EnsureProxy
// and RemoveProxy call it automatically; transactional callers use it when a
// failure happens between NextFreeIndex and EnsureProxy.
func (pm *ProxyManager) ReleaseProxyIndex(index int) {
	pm.allocationMu.Lock()
	delete(pm.pending, index)
	pm.allocationMu.Unlock()
}

// LookupProxy returns the current NDMS identity at ProxyN. It is used by
// persisted allocators before refreshing an interface so a stale saved index
// can never overwrite a user-created proxy.
func (pm *ProxyManager) LookupProxy(ctx context.Context, index int) (description string, exists bool, err error) {
	pm.allocationMu.Lock()
	defer pm.allocationMu.Unlock()
	return pm.lookupProxyLocked(ctx, index)
}

func (pm *ProxyManager) lookupProxyLocked(ctx context.Context, index int) (description string, exists bool, err error) {
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	get := pm.getProxy
	if get == nil {
		get = pm.queries.Interfaces.GetProxy
	}
	info, err := get(ctx, name)
	if err != nil {
		return "", false, err
	}
	if info == nil || !info.Exists {
		return "", false, nil
	}
	return info.Description, true, nil
}

// SetProxyPortGetter overrides or supplies proxy port inspection.
func (pm *ProxyManager) SetProxyPortGetter(fn func(context.Context, int) (int, error)) {
	pm.allocationMu.Lock()
	pm.getProxyPort = fn
	pm.allocationMu.Unlock()
}

// SetSystemNameResolver supplies or overrides the kernel interface name resolver.
func (pm *ProxyManager) SetSystemNameResolver(fn func(context.Context, string) string) {
	pm.allocationMu.Lock()
	pm.resolveSystemName = fn
	pm.allocationMu.Unlock()
}

func (pm *ProxyManager) resolveSystemNameFor(ctx context.Context, name string) string {
	pm.allocationMu.Lock()
	resolver := pm.resolveSystemName
	pm.allocationMu.Unlock()
	if resolver != nil {
		return resolver(ctx, name)
	}
	if pm.queries != nil && pm.queries.Interfaces != nil {
		return pm.queries.Interfaces.ResolveSystemName(ctx, name)
	}
	return ""
}

// ProxyObservation is the read-only observed state of an NDMS Proxy interface.
type ProxyObservation struct {
	Name        string
	Exists      bool
	Description string
	State       string
	Link        string
	Up          bool
	SystemName  string
	Address     string
	ListenPort  int
}

func parseProxyPortFromRunningConfig(lines []string, ifaceName string) int {
	prefix := "interface " + ifaceName
	inIface := false
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(line, "interface ") {
			if line == prefix {
				inIface = true
				continue
			}
			if inIface {
				break
			}
		}
		if !inIface {
			continue
		}
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "!") {
			break
		}
		if strings.HasPrefix(trimmed, "proxy upstream ") {
			fields := strings.Fields(trimmed)
			if len(fields) >= 4 {
				if p, err := strconv.Atoi(fields[3]); err == nil && p > 0 {
					return p
				}
			}
		}
	}
	return 0
}

// InspectProxy inspects the live NDMS state of ProxyN.
func (pm *ProxyManager) InspectProxy(ctx context.Context, index int) (ProxyObservation, error) {
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	var listenPort int
	if pm.getProxyPort != nil {
		listenPort, _ = pm.getProxyPort(ctx, index)
	} else if pm.queries != nil && pm.queries.RunningConfig != nil {
		if lines, err := pm.queries.RunningConfig.Lines(ctx); err == nil {
			listenPort = parseProxyPortFromRunningConfig(lines, name)
		}
	}

	getIf := pm.getInterface
	if getIf == nil && pm.queries != nil && pm.queries.Interfaces != nil {
		getIf = pm.queries.Interfaces.Get
	}
	if getIf != nil {
		iface, err := getIf(ctx, name)
		if err != nil {
			return ProxyObservation{}, err
		}
		if iface == nil {
			return ProxyObservation{Name: name, Exists: false}, nil
		}
		sysName := iface.SystemName
		if sysName == "" {
			sysName = pm.resolveSystemNameFor(ctx, name)
		}
		return ProxyObservation{
			Name:        iface.ID,
			Exists:      true,
			Description: iface.Description,
			State:       iface.State,
			Link:        iface.Link,
			Up:          iface.State == "up",
			SystemName:  sysName,
			Address:     iface.Address,
			ListenPort:  listenPort,
		}, nil
	}

	getPr := pm.getProxy
	if getPr == nil && pm.queries != nil && pm.queries.Interfaces != nil {
		getPr = pm.queries.Interfaces.GetProxy
	}
	if getPr != nil {
		pInfo, err := getPr(ctx, name)
		if err != nil {
			return ProxyObservation{}, err
		}
		if pInfo == nil || !pInfo.Exists {
			return ProxyObservation{Name: name, Exists: false}, nil
		}
		sysName := pm.resolveSystemNameFor(ctx, name)
		return ProxyObservation{
			Name:        pInfo.Name,
			Exists:      true,
			Description: pInfo.Description,
			State:       pInfo.State,
			Link:        pInfo.Link,
			Up:          pInfo.Up,
			SystemName:  sysName,
			ListenPort:  listenPort,
		}, nil
	}

	return ProxyObservation{Name: name, Exists: false}, nil
}

// ListProxyObservations returns all observed NDMS Proxy interfaces.
func (pm *ProxyManager) ListProxyObservations(ctx context.Context) ([]ProxyObservation, error) {
	list := pm.listInterfaces
	if list == nil && pm.queries != nil && pm.queries.Interfaces != nil {
		list = pm.queries.Interfaces.List
	}
	if list == nil {
		return nil, nil
	}
	ifaces, err := list(ctx)
	if err != nil {
		return nil, fmt.Errorf("list proxy observations: %w", err)
	}
	var lines []string
	if pm.getProxyPort == nil && pm.queries != nil && pm.queries.RunningConfig != nil {
		lines, _ = pm.queries.RunningConfig.Lines(ctx)
	}
	var observations []ProxyObservation
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var port int
		idx, _ := strconv.Atoi(strings.TrimPrefix(iface.ID, proxyIfacePrefix))
		if pm.getProxyPort != nil {
			port, _ = pm.getProxyPort(ctx, idx)
		} else if len(lines) > 0 {
			port = parseProxyPortFromRunningConfig(lines, iface.ID)
		}
		sysName := iface.SystemName
		if sysName == "" {
			sysName = pm.resolveSystemNameFor(ctx, iface.ID)
		}
		observations = append(observations, ProxyObservation{
			Name:        iface.ID,
			Exists:      true,
			Description: iface.Description,
			State:       iface.State,
			Link:        iface.Link,
			Up:          iface.State == "up",
			SystemName:  sysName,
			Address:     iface.Address,
			ListenPort:  port,
		})
	}
	return observations, nil
}

// RemoveProxyIfOwned removes ProxyN only when its current stable description
// matches owner or one of legacyOwners. A false result is a safe ownership conflict, not an error.
func (pm *ProxyManager) RemoveProxyIfOwned(ctx context.Context, index int, owner string, legacyOwners ...string) (bool, error) {
	return pm.RemoveProxyIfOwnedBy(ctx, index, owner, legacyOwners...)
}

// RemoveProxyIfOwnedBy is the migration-aware variant used by persisted
// subscription/group records. A foreign interface is left untouched.
func (pm *ProxyManager) RemoveProxyIfOwnedBy(ctx context.Context, index int, owner string, legacyOwners ...string) (bool, error) {
	defer markProxyMgrDur(fmt.Sprintf("RemoveProxyIfOwned(%d)", index), time.Now())
	pm.allocationMu.Lock()
	defer func() {
		delete(pm.pending, index)
		pm.allocationMu.Unlock()
	}()
	description, exists, err := pm.lookupProxyLocked(ctx, index)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if !proxyOwnerMatches(description, owner, legacyOwners) {
		return false, nil
	}
	return true, pm.removeProxyLocked(ctx, index)
}

// RemoveProxy tears down ProxyN.
func (pm *ProxyManager) RemoveProxy(ctx context.Context, index int) error {
	defer markProxyMgrDur(fmt.Sprintf("RemoveProxy(%d)", index), time.Now())
	pm.allocationMu.Lock()
	defer func() {
		delete(pm.pending, index)
		pm.allocationMu.Unlock()
	}()
	return pm.removeProxyLocked(ctx, index)
}

func (pm *ProxyManager) removeProxyLocked(ctx context.Context, index int) error {
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	if pm.queries != nil && pm.queries.Interfaces != nil {
		if rec, err := pm.queries.Interfaces.Refresh(ctx, name); err == nil && rec == nil {
			return nil
		}
	}
	down := pm.downProxy
	if down == nil {
		down = pm.commands.Proxies.ProxyDown
	}
	remove := pm.deleteProxy
	if remove == nil {
		remove = pm.commands.Proxies.DeleteProxy
	}
	_ = down(ctx, name) // ignore error — may be already down
	return remove(ctx, name)
}

// AllowAdoptEmptyDescription is an ownership token accepted by EnsureProxyIfOwned
// that permits claiming an existing NDMS proxy interface whose description is empty.
const AllowAdoptEmptyDescription = "__AWGM_ADOPT_EMPTY__"

// ExtractProxyOwnerToken extracts the canonical awg-manager ownership token
// from a raw or formatted NDMS description (e.g. "🇷🇺 RUS [awg-manager:mihomo:proxy:123]").
func ExtractProxyOwnerToken(desc string) string {
	desc = strings.TrimSpace(desc)
	if start := strings.Index(desc, "[awg-manager:"); start != -1 {
		end := strings.Index(desc[start:], "]")
		if end != -1 {
			return desc[start+1 : start+end]
		}
		return desc[start+1:]
	}
	if strings.HasPrefix(desc, "awg-manager:") {
		return desc
	}
	return ""
}

func proxyOwnerMatches(description, owner string, legacyOwners []string) bool {
	if description == owner {
		return true
	}
	descToken := ExtractProxyOwnerToken(description)
	ownerToken := ExtractProxyOwnerToken(owner)
	if descToken != "" && ownerToken != "" && descToken == ownerToken {
		return true
	}
	if ownerToken != "" && (description == ownerToken || strings.Contains(description, "["+ownerToken+"]")) {
		return true
	}
	if descToken != "" && (owner == descToken || strings.Contains(owner, "["+descToken+"]")) {
		return true
	}
	for _, legacy := range legacyOwners {
		if legacy == AllowAdoptEmptyDescription && description == "" {
			return true
		}
		if legacy != "" {
			if description == legacy || (descToken != "" && descToken == legacy) {
				return true
			}
			legacyToken := ExtractProxyOwnerToken(legacy)
			if legacyToken != "" && (descToken == legacyToken || description == legacyToken) {
				return true
			}
		}
	}
	return false
}

// RemoveOrphanSingboxProxies удаляет ProxyN, ассоциированные с sing-box,
// которые остались в NDMS после перехода в режим "NDMS Proxy disabled"
// (или после кривого middle-of-MigrateOff обрыва). Безопасно сохраняет
// Proxy, созданные пользователем вручную.
//
// Критерии "ours":
//  1. iface.Description совпадает с одним из tunnelTags (наш ProxyManager
//     пишет tunnel tag в description — proxy.go:47, operator.go:1292).
//  2. iface.Description пустой И idx попадает в ourPortSlots (некоторые
//     версии прошивки могут не возвращать description в List).
//
// Прочие ProxyN остаются нетронутыми — это пользовательские интерфейсы.
// Best-effort: при ошибке удаления одного proxy переходит к следующему,
// возвращает первую ошибку.
func (pm *ProxyManager) RemoveOrphanSingboxProxies(ctx context.Context, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) error {
	return pm.RemoveOrphanSingboxProxiesOwned(ctx, tunnelTags, ourPortSlots, subProxyIdx, nil)
}

// RemoveOrphanSingboxProxiesOwned is the ownership-aware variant for managed
// composites with stable descriptions. A persisted index alone cannot grant
// deletion rights after a foreign ProxyN has taken over that slot.
func (pm *ProxyManager) RemoveOrphanSingboxProxiesOwned(ctx context.Context, tunnelTags map[string]bool, ourPortSlots, legacySubProxyIdx map[int]bool, subProxyOwners map[int]map[string]bool) error {
	ifaces, err := pm.queries.Interfaces.List(ctx)
	if err != nil {
		return fmt.Errorf("list interfaces: %w", err)
	}
	var firstErr error
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, e := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); e != nil || n != 1 {
			continue
		}
		if !proxyIsOursOwned(idx, iface.Description, tunnelTags, ourPortSlots, legacySubProxyIdx, subProxyOwners, false) {
			continue
		}
		// Stable composite ownership must be re-checked under allocationMu:
		// the interface may have changed owner after Interfaces.List.
		if accepted := subProxyOwners[idx]; accepted != nil && accepted[iface.Description] {
			if _, err := pm.RemoveProxyIfOwnedBy(ctx, idx, iface.Description); err != nil && firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := pm.RemoveProxy(ctx, idx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListNativeProxies returns kernel names (e.g. "t2s0") of NDMS Proxy
// interfaces NOT created by us — KeenOS-native SOCKS proxies the user may
// bind a router direct outbound to (#323). Mirrors RemoveOrphanSingboxProxies'
// enumeration but inverts the ownership test and resolves kernel names.
func (pm *ProxyManager) ListNativeProxies(ctx context.Context, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) ([]string, error) {
	return pm.ListNativeProxiesOwned(ctx, tunnelTags, ourPortSlots, subProxyIdx, nil)
}

// ListNativeProxiesOwned mirrors RemoveOrphanSingboxProxiesOwned while keeping
// foreign takeovers visible as native bind targets.
func (pm *ProxyManager) ListNativeProxiesOwned(ctx context.Context, tunnelTags map[string]bool, ourPortSlots, legacySubProxyIdx map[int]bool, subProxyOwners map[int]map[string]bool) ([]string, error) {
	ifaces, err := pm.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	var entries []proxyEntry
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, e := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); e != nil || n != 1 {
			continue
		}
		kernel := pm.queries.Interfaces.ResolveSystemName(ctx, iface.ID)
		if kernel == "" {
			continue
		}
		entries = append(entries, proxyEntry{idx: idx, desc: iface.Description, kernel: kernel})
	}
	return nativeProxyKernelNamesOwned(entries, tunnelTags, ourPortSlots, legacySubProxyIdx, subProxyOwners), nil
}

// SubscriptionProxy describes an NDMS ProxyN created for a subscription
// composite (urltest/selector). These live in a separate managed set from
// tunnel proxies (Tunnels()): their port and proxy index are allocated by the
// subscription system, not derived from listen_port-firstPort.
type SubscriptionProxy struct {
	Index            int
	Port             int
	Label            string
	OwnerDescription string
	// Bindable keeps a managed ProxyN visible in the native-proxy picker.
	// Mihomo bridges are valid sing-box direct-bind targets; sing-box's own
	// subscription/group composites are internal and remain hidden.
	Bindable bool
}

// SubscriptionProxySet enumerates active subscription composite proxies.
// Implemented by the wiring layer over the subscription store.
type SubscriptionProxySet interface {
	SubscriptionProxies() []SubscriptionProxy
}

// proxyIsOurs reports whether ProxyN (index idx, interface description desc) was
// created by awg-manager for sing-box. Tunnel proxies are matched by their tag
// description or port slot; subscription composites carry the user label as
// description (not a tunnel tag), so they are recognised by their explicitly
// tracked proxy index instead.
func proxyIsOurs(idx int, desc string, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) bool {
	return proxyIsOursOwned(idx, desc, tunnelTags, ourPortSlots, subProxyIdx, nil, false)
}

func proxyIsOursOwned(idx int, desc string, tunnelTags map[string]bool, ourPortSlots, legacySubProxyIdx map[int]bool, subProxyOwners map[int]map[string]bool, hideOwnerIndexWithoutDescription bool) bool {
	if accepted := subProxyOwners[idx]; accepted != nil {
		if accepted[desc] {
			return true
		}
		// Some KeenOS versions omit descriptions from interface listings. It
		// is unsafe to delete on index alone, but the native picker must hide a
		// known managed index to prevent selecting our own composite in a loop.
		if desc == "" && hideOwnerIndexWithoutDescription {
			return true
		}
	}
	if legacySubProxyIdx[idx] {
		return true
	}
	if desc != "" {
		return tunnelTags[desc]
	}
	return ourPortSlots[idx]
}

// proxyEntry is an NDMS Proxy interface candidate: NDMS slot index, NDMS
// description, and resolved kernel name (e.g. "t2s0").
type proxyEntry struct {
	idx    int
	desc   string
	kernel string
}

// nativeProxyKernelNames returns kernel names of Proxy interfaces NOT created
// by us — KeenOS-native SOCKS proxies the user may bind a router direct
// outbound to (#323). Pure filter over proxyIsOurs; I/O lives in the caller.
func nativeProxyKernelNames(proxies []proxyEntry, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) []string {
	return nativeProxyKernelNamesOwned(proxies, tunnelTags, ourPortSlots, subProxyIdx, nil)
}

func nativeProxyKernelNamesOwned(proxies []proxyEntry, tunnelTags map[string]bool, ourPortSlots, legacySubProxyIdx map[int]bool, subProxyOwners map[int]map[string]bool) []string {
	var out []string
	for _, p := range proxies {
		if proxyIsOursOwned(p.idx, p.desc, tunnelTags, ourPortSlots, legacySubProxyIdx, subProxyOwners, true) {
			continue
		}
		out = append(out, p.kernel)
	}
	return out
}

// SyncProxies reconciles NDMS Proxy interfaces with current config.json tunnels.
// Creates missing Proxy for each tunnel and brings existing Proxy up if Down.
// Removal of proxies for absent tunnels is the Operator's responsibility.
func (pm *ProxyManager) SyncProxies(ctx context.Context, tunnels []TunnelInfo) error {
	for _, t := range tunnels {
		var idx int
		if _, err := fmt.Sscanf(t.ProxyInterface, proxyIfacePrefix+"%d", &idx); err != nil {
			return fmt.Errorf("bad proxy iface name %q: %w", t.ProxyInterface, err)
		}
		info, err := pm.queries.Interfaces.GetProxy(ctx, t.ProxyInterface)
		if err != nil || !info.Exists {
			if err := pm.EnsureProxy(ctx, idx, t.ListenPort, t.Tag); err != nil {
				return err
			}
			continue
		}
		if !info.Up {
			if err := pm.commands.Proxies.ProxyUp(ctx, t.ProxyInterface); err != nil {
				return err
			}
		}
	}
	return nil
}

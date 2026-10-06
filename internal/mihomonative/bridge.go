package mihomonative

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

const (
	bridgePortBase = 12000
	bridgePortMax  = 12099
)

type BridgeProxyRegistrar interface {
	NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error)
	ReleaseProxyIndex(idx int)
	LookupProxy(ctx context.Context, idx int) (description string, exists bool, err error)
	EnsureProxy(ctx context.Context, idx, port int, description string) error
	EnsureProxyIfOwned(ctx context.Context, idx, port int, owner string, legacyOwners ...string) (owned bool, err error)
	RemoveProxyIfOwned(ctx context.Context, idx int, owner string, legacyOwners ...string) (removed bool, err error)
}

// BridgeManager reconciles the persisted native resources with real KeenOS
// ProxyN interfaces. It owns only allocations recorded in the native store.
type BridgeManager struct {
	store     *Store
	registrar BridgeProxyRegistrar
	enabled   func() bool
	reserved  func() map[int]bool
	ports     func() map[int]bool
	running   func() bool
	mu        sync.Mutex
}

func NewBridgeManager(store *Store, registrar BridgeProxyRegistrar, enabled func() bool) *BridgeManager {
	return &BridgeManager{store: store, registrar: registrar, enabled: enabled}
}

// SetReservedIndices supplies persisted indices owned by other AWG Manager
// subsystems (currently sing-box subscription composites). They must be
// excluded even when their ProxyN is temporarily absent from NDMS.
func (m *BridgeManager) SetReservedIndices(fn func() map[int]bool) {
	m.mu.Lock()
	m.reserved = fn
	m.mu.Unlock()
}

// SetReservedPorts supplies persisted listen ports owned by other subsystems.
// Unlike a live bind probe, this also protects configured-but-currently-down
// device-proxy/custom listeners from being claimed by a Mihomo bridge.
func (m *BridgeManager) SetReservedPorts(fn func() map[int]bool) {
	m.mu.Lock()
	m.ports = fn
	m.mu.Unlock()
}

// SetRuntimeActive lets persisted-port reconciliation distinguish Mihomo's
// own bound listeners from foreign TCP/UDP occupants.
func (m *BridgeManager) SetRuntimeActive(fn func() bool) {
	m.mu.Lock()
	m.running = fn
	m.mu.Unlock()
}

// BridgeOwnershipDescription is the stable NDMS ownership token for one
// native resource. Labels are intentionally excluded: they are editable and
// therefore cannot prove that a persisted ProxyN still belongs to us.
func BridgeOwnershipDescription(kind, id string) string {
	return "awg-manager:mihomo:" + kind + ":" + id
}

// FormatBridgeDescription returns the NDMS interface description. When a label
// is present, it prepends the human label so Keenetic Web UI displays friendly names:
// e.g. "🇷🇺 RUS (Россия) [awg-manager:mihomo:proxy:123]".
func FormatBridgeDescription(label, kind, id string) string {
	canonical := BridgeOwnershipDescription(kind, id)
	label = strings.TrimSpace(label)
	if label == "" {
		return canonical
	}
	return fmt.Sprintf("%s [%s]", label, canonical)
}

func (m *BridgeManager) Enabled() bool {
	return m != nil && m.store != nil && m.registrar != nil && (m.enabled == nil || m.enabled())
}

// Reconcile allocates missing bridges, removes allocations that existed in
// previous but no longer belong to a resource, and idempotently ensures every
// desired ProxyN. Passing the pre-mutation list makes delete/rollback exact.
func (m *BridgeManager) Reconcile(ctx context.Context, previous []BridgeRef) error {
	if !m.Enabled() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	usedPorts := make(map[int]bool)
	configuredPorts := make(map[int]bool)
	if m.ports != nil {
		for port := range m.ports() {
			configuredPorts[port] = true
			usedPorts[port] = true
		}
	}
	reservedIndices := make(map[int]bool)
	pendingIndices := make(map[int]bool)
	defer func() {
		for index := range pendingIndices {
			m.registrar.ReleaseProxyIndex(index)
		}
	}()
	if m.reserved != nil {
		for index := range m.reserved() {
			reservedIndices[index] = true
		}
	}
	for _, ref := range append(append([]BridgeRef(nil), previous...), m.store.ListBridges()...) {
		if ref.Bridge.ListenPort >= bridgePortBase && ref.Bridge.ListenPort <= bridgePortMax {
			usedPorts[ref.Bridge.ListenPort] = true
		}
		if ref.Bridge.ProxyIndex >= 0 {
			reservedIndices[ref.Bridge.ProxyIndex] = true
		}
	}

	proxies := m.store.ListProxies()
	for _, proxy := range proxies {
		if proxy.SourceID != "" || proxy.Bridge != nil || !proxy.Enabled || proxy.SelectedEngine != EngineMihomo {
			continue
		}
		index, err := m.allocate(ctx, "proxy", proxy.ID, usedPorts, reservedIndices)
		if err != nil {
			return err
		}
		pendingIndices[index] = true
	}
	for _, sub := range m.store.ListSubscriptions() {
		if sub.Bridge != nil || !m.store.IsSubscriptionExportable(sub.ID) {
			continue
		}
		index, err := m.allocate(ctx, "subscription", sub.ID, usedPorts, reservedIndices)
		if err != nil {
			return err
		}
		pendingIndices[index] = true
	}

	desired := m.store.ListBridges()
	desiredIndices := make(map[int]bool, len(desired))
	for _, ref := range desired {
		if !ref.Enabled {
			continue
		}
		desiredIndices[ref.Bridge.ProxyIndex] = true
	}
	for _, ref := range previous {
		// An already disabled resource has no live ProxyN owned by this
		// reconciliation pass. A transition enabled -> disabled is still
		// removed because the previous snapshot carries Enabled=true.
		if !ref.Enabled {
			continue
		}
		if desiredIndices[ref.Bridge.ProxyIndex] {
			continue
		}
		canonicalOwner := BridgeOwnershipDescription(ref.Kind, ref.ID)
		legacyOwners := []string{}
		if ref.LegacyOwner != "" && ref.LegacyOwner != canonicalOwner {
			legacyOwners = append(legacyOwners, ref.LegacyOwner)
		}
		removed, err := m.registrar.RemoveProxyIfOwned(
			ctx, ref.Bridge.ProxyIndex, canonicalOwner, legacyOwners...,
		)
		if err != nil {
			return fmt.Errorf("remove Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, err)
		}
		// A mismatched description means the saved index was taken over by a
		// user/another subsystem. Never delete that interface.
		_ = removed
	}
	sort.Slice(desired, func(i, j int) bool { return desired[i].Bridge.ProxyIndex < desired[j].Bridge.ProxyIndex })
	for i := range desired {
		ref := &desired[i]
		if !ref.Enabled {
			continue
		}
		canonicalOwner := BridgeOwnershipDescription(ref.Kind, ref.ID)
		legacyOwners := []string{canonicalOwner}
		if ref.LegacyOwner != "" && ref.LegacyOwner != canonicalOwner {
			legacyOwners = append(legacyOwners, ref.LegacyOwner)
		}
		desc := FormatBridgeDescription(ref.Label, ref.Kind, ref.ID)
		bridge := ref.Bridge
		changed := false
		runtimeOwnsPorts := m.running != nil && m.running()
		if bridge.ListenPort < bridgePortBase || bridge.ListenPort > bridgePortMax || configuredPorts[bridge.ListenPort] || (!runtimeOwnsPorts && !bridgePortAvailable(bridge.ListenPort)) {
			port, portErr := nextBridgePort(usedPorts)
			if portErr != nil {
				return fmt.Errorf("reallocate conflicting Mihomo port %s/%s: %w", ref.Kind, ref.ID, portErr)
			}
			bridge.ListenPort = port
			usedPorts[port] = true
			changed = true
		}
		if changed {
			if setErr := m.store.SetBridge(ref.Kind, ref.ID, bridge); setErr != nil {
				return fmt.Errorf("persist reallocated Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, setErr)
			}
			ref.Bridge = bridge
		}
		if err := validateBridge(ref.Bridge); err != nil {
			return fmt.Errorf("invalid Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, err)
		}
		isGated := false
		if g, ok := m.registrar.(interface{ IsGated() bool }); ok {
			isGated = g.IsGated()
		}
		owned, err := m.registrar.EnsureProxyIfOwned(ctx, ref.Bridge.ProxyIndex, ref.Bridge.ListenPort, desc, legacyOwners...)
		if err != nil {
			return fmt.Errorf("ensure Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, err)
		}
		if owned {
			delete(pendingIndices, ref.Bridge.ProxyIndex)
			if !isGated && ref.Bridge.LegacyOwner != "" {
				bridge.LegacyOwner = ""
				if setErr := m.store.SetBridge(ref.Kind, ref.ID, bridge); setErr != nil {
					return fmt.Errorf("clear migrated Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, setErr)
				}
			}
			continue
		}

		// The persisted index now belongs to somebody else. Allocate a new
		// slot, persist it, and leave the conflicting interface untouched.
		index, allocErr := m.registrar.NextFreeIndex(ctx, reservedIndices)
		if allocErr != nil {
			return fmt.Errorf("reallocate conflicting Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, allocErr)
		}
		pendingIndices[index] = true
		bridge = ref.Bridge
		bridge.ProxyIndex = index
		bridge.ProxyInterface = fmt.Sprintf("Proxy%d", index)
		bridge.KernelInterface = fmt.Sprintf("t2s%d", index)
		reservedIndices[index] = true
		if setErr := m.store.SetBridge(ref.Kind, ref.ID, bridge); setErr != nil {
			return fmt.Errorf("persist reallocated Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, setErr)
		}
		owned, err = m.registrar.EnsureProxyIfOwned(ctx, index, bridge.ListenPort, desc, legacyOwners...)
		if err != nil {
			return fmt.Errorf("ensure reallocated Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, err)
		}
		if !owned {
			return fmt.Errorf("ensure reallocated Mihomo bridge %s/%s: Proxy%d ownership changed during allocation", ref.Kind, ref.ID, index)
		}
		if !isGated && ref.Bridge.LegacyOwner != "" {
			bridge.LegacyOwner = ""
			if setErr := m.store.SetBridge(ref.Kind, ref.ID, bridge); setErr != nil {
				return fmt.Errorf("clear migrated reallocated Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, setErr)
			}
		}
		delete(pendingIndices, index)
	}
	return nil
}

func (m *BridgeManager) allocate(ctx context.Context, kind, id string, usedPorts, reservedIndices map[int]bool) (int, error) {
	port, err := nextBridgePort(usedPorts)
	if err != nil {
		return -1, err
	}
	index, err := m.registrar.NextFreeIndex(ctx, reservedIndices)
	if err != nil {
		return -1, fmt.Errorf("allocate Mihomo ProxyN: %w", err)
	}
	bridge := ProxyBridge{
		ListenPort: port, ProxyIndex: index,
		ProxyInterface:  fmt.Sprintf("Proxy%d", index),
		KernelInterface: fmt.Sprintf("t2s%d", index),
	}
	if err := m.store.SetBridge(kind, id, bridge); err != nil {
		m.registrar.ReleaseProxyIndex(index)
		return -1, err
	}
	usedPorts[port], reservedIndices[index] = true, true
	return index, nil
}

func nextBridgePort(used map[int]bool) (int, error) {
	for port := bridgePortBase; port <= bridgePortMax; port++ {
		if used[port] {
			continue
		}
		if bridgePortAvailable(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("mihomo native: no free bridge port in %d-%d", bridgePortBase, bridgePortMax)
}

func bridgePortAvailable(port int) bool {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	tcpListener, err := net.Listen("tcp", address)
	if err != nil {
		return false
	}
	defer tcpListener.Close()
	udpListener, err := net.ListenPacket("udp", address)
	if err != nil {
		return false
	}
	_ = udpListener.Close()
	return true
}

func validateBridge(bridge ProxyBridge) error {
	if bridge.ListenPort < bridgePortBase || bridge.ListenPort > bridgePortMax {
		return fmt.Errorf("listen port %d is outside %d-%d", bridge.ListenPort, bridgePortBase, bridgePortMax)
	}
	if bridge.ProxyIndex < 0 || bridge.ProxyInterface != fmt.Sprintf("Proxy%d", bridge.ProxyIndex) || bridge.KernelInterface != fmt.Sprintf("t2s%d", bridge.ProxyIndex) {
		return fmt.Errorf("inconsistent ProxyN/t2sN identity")
	}
	return nil
}

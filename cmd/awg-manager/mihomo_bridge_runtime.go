package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/singbox"
)

// gatedBridgeRegistrar makes BridgeManager's allocation/reconciliation pass
// two-phase without coupling the native store to process management. While the
// gate is closed, BridgeManager may persist ports and ProxyN indices, but its
// final Ensure call is intentionally suppressed. The gate is opened only after
// Mihomo has loaded that exact listener configuration and passed readiness.
type bridgeProxyRegistrar interface {
	NextFreeIndex(context.Context, map[int]bool) (int, error)
	ReleaseProxyIndex(int)
	LookupProxy(context.Context, int) (string, bool, error)
	EnsureProxy(context.Context, int, int, string) error
	EnsureProxyIfOwned(context.Context, int, int, string, ...string) (bool, error)
	RemoveProxyIfOwned(context.Context, int, string, ...string) (bool, error)
	InspectProxy(context.Context, int) (singbox.ProxyObservation, error)
	ListProxyObservations(context.Context) ([]singbox.ProxyObservation, error)
}

type gatedBridgeRegistrar struct {
	base bridgeProxyRegistrar

	mu    sync.RWMutex
	ready bool
}

func (g *gatedBridgeRegistrar) setReady(ready bool) {
	g.mu.Lock()
	g.ready = ready
	g.mu.Unlock()
}

func (g *gatedBridgeRegistrar) isReady() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.ready
}

func (g *gatedBridgeRegistrar) NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error) {
	return g.base.NextFreeIndex(ctx, reserved)
}

func (g *gatedBridgeRegistrar) ReleaseProxyIndex(index int) {
	g.base.ReleaseProxyIndex(index)
}

func (g *gatedBridgeRegistrar) LookupProxy(ctx context.Context, index int) (string, bool, error) {
	return g.base.LookupProxy(ctx, index)
}

func (g *gatedBridgeRegistrar) EnsureProxy(ctx context.Context, index, port int, description string) error {
	if !g.isReady() {
		// NextFreeIndex owns an in-process reservation until EnsureProxy. A
		// prepared bridge is already persisted and therefore reserved by the
		// next scan, so release the transient reservation while advertisement
		// is deliberately deferred.
		g.base.ReleaseProxyIndex(index)
		return nil
	}
	return g.base.EnsureProxy(ctx, index, port, description)
}

func (g *gatedBridgeRegistrar) IsGated() bool {
	return !g.isReady()
}

func (g *gatedBridgeRegistrar) EnsureProxyIfOwned(
	ctx context.Context,
	index, port int,
	owner string,
	legacyOwners ...string,
) (bool, error) {
	if !g.isReady() {
		g.base.ReleaseProxyIndex(index)
		desc, exists, err := g.base.LookupProxy(ctx, index)
		if err != nil {
			return false, err
		}
		if !exists {
			return true, nil
		}
		if desc == owner {
			return true, nil
		}
		for _, leg := range legacyOwners {
			if leg != "" && desc == leg {
				return true, nil
			}
		}
		return false, nil
	}
	return g.base.EnsureProxyIfOwned(ctx, index, port, owner, legacyOwners...)
}

func (g *gatedBridgeRegistrar) RemoveProxyIfOwned(ctx context.Context, index int, owner string, legacyOwners ...string) (bool, error) {
	return g.base.RemoveProxyIfOwned(ctx, index, owner, legacyOwners...)
}

func (g *gatedBridgeRegistrar) InspectProxy(ctx context.Context, index int) (singbox.ProxyObservation, error) {
	if g.base == nil {
		return singbox.ProxyObservation{}, fmt.Errorf("base bridge registrar is nil")
	}
	return g.base.InspectProxy(ctx, index)
}

func (g *gatedBridgeRegistrar) ListProxyObservations(ctx context.Context) ([]singbox.ProxyObservation, error) {
	if g.base == nil {
		return nil, nil
	}
	return g.base.ListProxyObservations(ctx)
}

// mihomoBridgeRuntime serializes the config/runtime/NDMS publication edge.
// BridgeManager remains responsible for allocation and ownership; this layer
// only controls when those allocations become externally routable.
type mihomoBridgeRuntime struct {
	mu               sync.Mutex
	store            *mihomonative.Store
	manager          *mihomonative.BridgeManager
	gate             *gatedBridgeRegistrar
	durableMu        sync.RWMutex
	durableBridges   []mihomo.BridgeRef
	activeRoles      *mihomo.ActiveTransactionRoles
	manifestFile     string
	compensatingRefs map[string]struct{}
}

func newMihomoBridgeRuntime(
	store *mihomonative.Store,
	manager *mihomonative.BridgeManager,
	gate *gatedBridgeRegistrar,
) *mihomoBridgeRuntime {
	return &mihomoBridgeRuntime{store: store, manager: manager, gate: gate}
}

func (r *mihomoBridgeRuntime) prepare(ctx context.Context, previous []mihomonative.BridgeRef) error {
	if r == nil || r.manager == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate.setReady(false)
	return r.manager.Reconcile(ctx, previous)
}

func (r *mihomoBridgeRuntime) activate(ctx context.Context) error {
	if r == nil || r.manager == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.manager.Enabled() {
		return r.deactivateLocked(ctx)
	}
	if err := waitForMihomoBridgeListeners(ctx, r.store.ConfigBridgeListeners()); err != nil {
		r.gate.setReady(false)
		if cleanupErr := r.deactivateLocked(ctx); cleanupErr != nil {
			return fmt.Errorf("Mihomo bridge readiness: %v; withdraw exports: %w", err, cleanupErr)
		}
		return err
	}
	r.gate.setReady(true)
	// A second idempotent reconciliation is the publication commit. Passing
	// the current snapshot prevents it from treating any allocation as stale.
	if err := r.manager.Reconcile(ctx, r.store.ListBridges()); err != nil {
		r.gate.setReady(false)
		if cleanupErr := r.deactivateLocked(ctx); cleanupErr != nil {
			return fmt.Errorf("activate Mihomo bridges: %v; withdraw partial exports: %w", err, cleanupErr)
		}
		return fmt.Errorf("activate Mihomo bridges: %w", err)
	}
	return nil
}

func waitForMihomoBridgeListeners(ctx context.Context, listeners []mihomonative.BridgeListener) error {
	if len(listeners) == 0 {
		return nil
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pending := make(map[int]bool, len(listeners))
	for _, listener := range listeners {
		pending[listener.Port] = true
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	for len(pending) > 0 {
		for port := range pending {
			conn, err := dialer.DialContext(readyCtx, "tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err == nil {
				_ = conn.Close()
				delete(pending, port)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("Mihomo bridge listeners not ready on ports %v: %w", sortedBridgePorts(pending), readyCtx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func sortedBridgePorts(pending map[int]bool) []int {
	ports := make([]int, 0, len(pending))
	for port := range pending {
		ports = append(ports, port)
	}
	for i := 1; i < len(ports); i++ {
		for j := i; j > 0 && ports[j] < ports[j-1]; j-- {
			ports[j], ports[j-1] = ports[j-1], ports[j]
		}
	}
	return ports
}

func (r *mihomoBridgeRuntime) deactivate(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deactivateLocked(ctx)
}

// deactivateIf serializes the stale-exit guard with activation. If a newer
// Mihomo generation already activated, the old callback observes current=false
// while holding the same runtime mutex and cannot withdraw the new exports.
// If the old callback wins the lock, the later activation is necessarily the
// final operation and republishes them.
func (r *mihomoBridgeRuntime) deactivateIf(ctx context.Context, current func() bool) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current != nil && !current() {
		return nil
	}
	return r.deactivateLocked(ctx)
}

// deactivateAfterUnexpectedExit closes the publication gate immediately and
// retries best-effort withdrawal for a bounded period. NDMS can transiently
// reject one interface mutation while still accepting the others; keeping the
// gate closed and retrying avoids leaving a dead ProxyN indefinitely.
func (r *mihomoBridgeRuntime) deactivateAfterUnexpectedExit(ctx context.Context, current func() bool) error {
	if r == nil {
		return nil
	}
	delays := [...]time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond}
	var joined error
	for attempt, delay := range delays {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return errors.Join(joined, ctx.Err())
			case <-time.After(delay):
			}
		}
		err := r.deactivateIf(ctx, current)
		if err == nil {
			return nil
		}
		joined = errors.Join(joined, err)
	}
	return joined
}

func (r *mihomoBridgeRuntime) deactivateLocked(ctx context.Context) error {
	if r.gate != nil {
		r.gate.setReady(false)
	}
	if r.store == nil || r.gate == nil || r.gate.base == nil {
		return nil
	}
	var joined error
	for _, ref := range r.store.ListBridges() {
		if _, err := r.gate.base.RemoveProxyIfOwned(
			ctx,
			ref.Bridge.ProxyIndex,
			mihomonative.BridgeOwnershipDescription(ref.Kind, ref.ID),
		); err != nil {
			joined = errors.Join(joined, fmt.Errorf("withdraw Mihomo bridge %s/%s: %w", ref.Kind, ref.ID, err))
		}
	}
	return joined
}

// Ensure *mihomoBridgeRuntime satisfies mihomo.BridgeRuntime, mihomo.ExactBridgeRuntime, and mihomo.DurableBridgeRegistry.
var (
	_ mihomo.BridgeRuntime         = (*mihomoBridgeRuntime)(nil)
	_ mihomo.ExactBridgeRuntime    = (*mihomoBridgeRuntime)(nil)
	_ mihomo.DurableBridgeRegistry = (*mihomoBridgeRuntime)(nil)
)

var globalBridgeLegacyOwners = []string{"awg-manager", "awgm"}

func isAllowedLegacyOwner(desc string, legacyOwners []string) bool {
	for _, leg := range legacyOwners {
		if leg != "" && desc == leg {
			return true
		}
	}
	return false
}

func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// ReplaceDurableBridges atomically replaces the runtime's memory cache of durable bridge references.
// It validates that all refs are complete and unique. This cache is never authoritative for mutations.
func (r *mihomoBridgeRuntime) ReplaceDurableBridges(bridges []mihomo.BridgeRef) error {
	if r == nil {
		return nil
	}
	validBridges := make([]mihomo.BridgeRef, 0, len(bridges))
	seenSlots := make(map[string]struct{}, len(bridges))
	for _, b := range bridges {
		if err := b.ValidateComplete(); err != nil {
			return fmt.Errorf("invalid durable bridge %s: %w", b.SlotKey(), err)
		}
		slot := b.SlotKey()
		if _, exists := seenSlots[slot]; exists {
			return fmt.Errorf("duplicate slot key %q in durable bridges", slot)
		}
		seenSlots[slot] = struct{}{}
		validBridges = append(validBridges, b)
	}
	r.durableMu.Lock()
	r.durableBridges = validBridges
	r.durableMu.Unlock()
	return nil
}

// SetDurableManifestFile sets the absolute path to the active transaction manifest file on disk
// so that active transaction roles can be reconstructed after an in-memory cache loss.
func (r *mihomoBridgeRuntime) SetDurableManifestFile(path string) {
	if r == nil {
		return
	}
	r.durableMu.Lock()
	r.manifestFile = path
	r.durableMu.Unlock()
}

// SetActiveTransactionRoles updates the active transaction roles in the bridge registry.
func (r *mihomoBridgeRuntime) SetActiveTransactionRoles(roles *mihomo.ActiveTransactionRoles) error {
	if r == nil {
		return nil
	}
	r.durableMu.Lock()
	defer r.durableMu.Unlock()
	if roles == nil {
		r.activeRoles = nil
		return nil
	}
	cpy := *roles
	if roles.PreviousBridges != nil {
		cpy.PreviousBridges = make([]mihomo.BridgeRef, len(roles.PreviousBridges))
		copy(cpy.PreviousBridges, roles.PreviousBridges)
	}
	if roles.TargetBridges != nil {
		cpy.TargetBridges = make([]mihomo.BridgeRef, len(roles.TargetBridges))
		copy(cpy.TargetBridges, roles.TargetBridges)
	}
	r.activeRoles = &cpy
	return nil
}

var authorizedPortReplacementStates = map[mihomo.ManifestState]bool{
	mihomo.StateBridgesReconciling:      true,
	mihomo.StateSwapApplied:             true,
	mihomo.StateSwapVerified:            true,
	mihomo.StateRuntimeIntent:           true,
	mihomo.StateRuntimeApplied:          true,
	mihomo.StateRollbackBridgesVerified: true,
}

// isAuthorizedPortReplacementLocked checks if a same-owner port mismatch is proven by an active transaction.
func (r *mihomoBridgeRuntime) isAuthorizedPortReplacementLocked(ref, storeRef mihomo.BridgeRef) bool {
	// Must be same owner, same proxy index, same proxy interface, same kernel interface, differing only in port
	if ref.OwnerUUID != storeRef.OwnerUUID ||
		ref.ProxyIndex != storeRef.ProxyIndex ||
		ref.ProxyInterface != storeRef.ProxyInterface ||
		ref.KernelInterface != storeRef.KernelInterface ||
		ref.ListenPort == storeRef.ListenPort {
		return false
	}
	// The durable manifest is authoritative. The in-memory cache can be stale
	// after the transaction advances to a terminal or disallowed phase.
	var roles *mihomo.ActiveTransactionRoles
	if r.manifestFile != "" {
		cleanPath := filepath.Clean(r.manifestFile)
		if filepath.Base(cleanPath) == "config.yaml.txn.json" {
			if data, err := os.ReadFile(cleanPath); err == nil {
				var m mihomo.TransactionManifest
				if mihomo.DecodeJSONStrict(data, &m) == nil &&
					m.ValidateSchema() == nil &&
					m.ValidateSchemaForPhase() == nil &&
					m.OperationKind != mihomo.OperationMigration &&
					authorizedPortReplacementStates[m.State] {
					prevDigest := mihomo.BridgesDigest(m.PreviousBridges)
					targetDigest := mihomo.BridgesDigest(m.TargetBridges)
					if m.PreviousBridgesDigest == prevDigest && m.TargetBridgesDigest == targetDigest {
						roles = &mihomo.ActiveTransactionRoles{
							TxID:                 m.TxID,
							PreviousGenerationID: m.PreviousGenerationID,
							TargetGenerationID:   m.CandidateGenerationID,
							PreviousBridges:      m.PreviousBridges,
							TargetBridges:        m.TargetBridges,
						}
					}
				}
			}
		}
	}
	if roles == nil || roles.TxID == "" {
		return false
	}

	matchPair := func(aList, bList []mihomo.BridgeRef) bool {
		foundA := false
		for _, b := range aList {
			if b.Digest() == ref.Digest() {
				foundA = true
				break
			}
		}
		foundB := false
		for _, b := range bList {
			if b.Digest() == storeRef.Digest() {
				foundB = true
				break
			}
		}
		return foundA && foundB
	}

	return matchPair(roles.PreviousBridges, roles.TargetBridges) || matchPair(roles.TargetBridges, roles.PreviousBridges)
}

func (r *mihomoBridgeRuntime) isAuthorizedCompensationRestoreLocked(ref mihomo.BridgeRef) bool {
	if r == nil || len(r.compensatingRefs) == 0 {
		return false
	}
	_, ok := r.compensatingRefs[ref.Digest()]
	return ok
}

// resolvePublishIdentity enforces strict store-backed identity for publishing a target bridge.
// Requirements:
// 1. Complete durable BridgeRef.
// 2. Exactly one current-store resource matching the slot.
// 3. Exact equality of owner, interfaces, and listen port (or authorized replacement/compensation).
func (r *mihomoBridgeRuntime) resolvePublishIdentity(ref mihomo.BridgeRef) (canonicalOwner string, legacyOwners []string, listenPort int, err error) {
	if err := ref.ValidateCompleteForPublish(); err != nil {
		return "", nil, 0, fmt.Errorf("%w: invalid complete bridge ref for publish: %v", mihomo.ErrForeignBridgeOwnership, err)
	}
	if r == nil || r.store == nil {
		return "", nil, 0, fmt.Errorf("%w: runtime or native store is nil", mihomo.ErrForeignBridgeOwnership)
	}

	nativeBridges := r.store.ListBridges()
	var matched []mihomonative.BridgeRef
	for _, nb := range nativeBridges {
		if nb.Bridge.ProxyIndex == ref.ProxyIndex &&
			nb.Bridge.ProxyInterface == ref.ProxyInterface &&
			nb.Bridge.KernelInterface == ref.KernelInterface {
			matched = append(matched, nb)
		}
	}
	if len(matched) == 0 {
		return "", nil, 0, fmt.Errorf("%w: bridge %s not found in native store for publish", mihomo.ErrForeignBridgeOwnership, ref.SlotKey())
	}
	if len(matched) > 1 {
		return "", nil, 0, fmt.Errorf("%w: ambiguous native store bridges for %s (%d matches)", mihomo.ErrForeignBridgeOwnership, ref.SlotKey(), len(matched))
	}

	nb := matched[0]
	storeOwner := mihomonative.BridgeOwnershipDescription(nb.Kind, nb.ID)
	if ref.OwnerUUID != storeOwner {
		return "", nil, 0, fmt.Errorf("%w: store owner mismatch for %s: ref=%q store=%q", mihomo.ErrForeignBridgeOwnership, ref.SlotKey(), ref.OwnerUUID, storeOwner)
	}
	if ref.ListenPort != nb.Bridge.ListenPort {
		storeRef := mihomo.BridgeRef{
			ProxyIndex:      nb.Bridge.ProxyIndex,
			ProxyInterface:  nb.Bridge.ProxyInterface,
			KernelInterface: nb.Bridge.KernelInterface,
			ListenPort:      nb.Bridge.ListenPort,
			OwnerUUID:       storeOwner,
			LegacyOwner:     nb.LegacyOwner,
		}
		r.durableMu.RLock()
		authorized := r.isAuthorizedPortReplacementLocked(ref, storeRef) || r.isAuthorizedCompensationRestoreLocked(ref)
		r.durableMu.RUnlock()
		if !authorized {
			return "", nil, 0, fmt.Errorf("%w: store listen port mismatch for %s: ref=%d store=%d", mihomo.ErrForeignBridgeOwnership, ref.SlotKey(), ref.ListenPort, nb.Bridge.ListenPort)
		}
	}

	canonicalOwner = storeOwner
	listenPort = ref.ListenPort

	legacyOwners = append([]string{}, globalBridgeLegacyOwners...)
	if nb.LegacyOwner != "" && !containsString(legacyOwners, nb.LegacyOwner) {
		legacyOwners = append(legacyOwners, nb.LegacyOwner)
	}
	if ref.LegacyOwner != "" && !containsString(legacyOwners, ref.LegacyOwner) {
		legacyOwners = append(legacyOwners, ref.LegacyOwner)
	}
	return canonicalOwner, legacyOwners, listenPort, nil
}

// resolveInspectIdentity resolves identity for inspecting live NDMS state.
// Complete durable ref is required. Canonical owner comes from ref.OwnerUUID, never an unrelated occupant.
// If current store contains the same canonical owner, require exactly one record and 5-field equality.
// Absence from store is allowed (e.g. deleted or rollback).
// If store has a different owner in the same slot, inspection of the durable ref is still allowed.
func (r *mihomoBridgeRuntime) resolveInspectIdentity(ref mihomo.BridgeRef) (canonicalOwner string, legacyOwners []string, err error) {
	if err := ref.ValidateComplete(); err != nil {
		return "", nil, fmt.Errorf("%w: invalid complete bridge ref for inspect: %v", mihomo.ErrForeignBridgeOwnership, err)
	}
	canonicalOwner = ref.OwnerUUID
	legacyOwners = append([]string{}, globalBridgeLegacyOwners...)

	if r != nil && r.store != nil {
		var sameOwnerRecords []mihomonative.BridgeRef
		for _, nb := range r.store.ListBridges() {
			storeOwner := mihomonative.BridgeOwnershipDescription(nb.Kind, nb.ID)
			if storeOwner == canonicalOwner {
				sameOwnerRecords = append(sameOwnerRecords, nb)
			}
		}

		if len(sameOwnerRecords) > 1 {
			return "", nil, fmt.Errorf("%w: ambiguous store records for owner %q (%d matches)", mihomo.ErrForeignBridgeOwnership, canonicalOwner, len(sameOwnerRecords))
		}
		if len(sameOwnerRecords) == 1 {
			nb := sameOwnerRecords[0]
			storeRef := mihomo.BridgeRef{
				ProxyIndex:      nb.Bridge.ProxyIndex,
				ProxyInterface:  nb.Bridge.ProxyInterface,
				KernelInterface: nb.Bridge.KernelInterface,
				ListenPort:      nb.Bridge.ListenPort,
				OwnerUUID:       canonicalOwner,
				LegacyOwner:     nb.LegacyOwner,
			}
			if err := storeRef.ValidateComplete(); err != nil {
				return "", nil, fmt.Errorf("%w: store record for owner %q has incomplete bridge ref: %v", mihomo.ErrForeignBridgeOwnership, canonicalOwner, err)
			}
			mismatch := ref.ProxyIndex != storeRef.ProxyIndex ||
				ref.ProxyInterface != storeRef.ProxyInterface ||
				ref.KernelInterface != storeRef.KernelInterface ||
				ref.ListenPort != storeRef.ListenPort ||
				ref.OwnerUUID != storeRef.OwnerUUID

			if mismatch {
				r.durableMu.RLock()
				authorized := r.isAuthorizedPortReplacementLocked(ref, storeRef)
				r.durableMu.RUnlock()
				if !authorized {
					return "", nil, fmt.Errorf("%w: store record for owner %q does not match durable ref and is not authorized by active transaction: store=(%d, %s, %s, %d) ref=(%d, %s, %s, %d)",
						mihomo.ErrForeignBridgeOwnership, canonicalOwner,
						storeRef.ProxyIndex, storeRef.ProxyInterface, storeRef.KernelInterface, storeRef.ListenPort,
						ref.ProxyIndex, ref.ProxyInterface, ref.KernelInterface, ref.ListenPort)
				}
			}
			if nb.LegacyOwner != "" && !containsString(legacyOwners, nb.LegacyOwner) {
				legacyOwners = append(legacyOwners, nb.LegacyOwner)
			}
		}
	}

	if ref.LegacyOwner != "" && !containsString(legacyOwners, ref.LegacyOwner) {
		legacyOwners = append(legacyOwners, ref.LegacyOwner)
	}
	return canonicalOwner, legacyOwners, nil
}

// resolveWithdrawIdentity resolves identity for withdrawing a bridge from NDMS.
// Complete durable ref is required. Absence from store is allowed (e.g. resource was deleted
// or store reverted during rollback). Deletion is protected by RemoveProxyIfOwned.
// If current store contains the same canonical owner, require exactly one record and 5-field equality,
// allowing opposite-generation durable refs during replacement and rollback.
func (r *mihomoBridgeRuntime) resolveWithdrawIdentity(ref mihomo.BridgeRef) (canonicalOwner string, legacyOwners []string, err error) {
	if err := ref.ValidateComplete(); err != nil {
		return "", nil, fmt.Errorf("%w: invalid complete bridge ref for withdraw: %v", mihomo.ErrForeignBridgeOwnership, err)
	}
	canonicalOwner = ref.OwnerUUID
	legacyOwners = append([]string{}, globalBridgeLegacyOwners...)

	if r != nil && r.store != nil {
		var sameOwnerRecords []mihomonative.BridgeRef
		for _, nb := range r.store.ListBridges() {
			storeOwner := mihomonative.BridgeOwnershipDescription(nb.Kind, nb.ID)
			if storeOwner == canonicalOwner {
				sameOwnerRecords = append(sameOwnerRecords, nb)
			}
		}

		if len(sameOwnerRecords) > 1 {
			return "", nil, fmt.Errorf("%w: ambiguous store records for owner %q (%d matches)", mihomo.ErrForeignBridgeOwnership, canonicalOwner, len(sameOwnerRecords))
		}
		if len(sameOwnerRecords) == 1 {
			nb := sameOwnerRecords[0]
			storeRef := mihomo.BridgeRef{
				ProxyIndex:      nb.Bridge.ProxyIndex,
				ProxyInterface:  nb.Bridge.ProxyInterface,
				KernelInterface: nb.Bridge.KernelInterface,
				ListenPort:      nb.Bridge.ListenPort,
				OwnerUUID:       canonicalOwner,
				LegacyOwner:     nb.LegacyOwner,
			}
			if err := storeRef.ValidateComplete(); err != nil {
				return "", nil, fmt.Errorf("%w: store record for owner %q has incomplete bridge ref: %v", mihomo.ErrForeignBridgeOwnership, canonicalOwner, err)
			}
			mismatch := ref.ProxyIndex != storeRef.ProxyIndex ||
				ref.ProxyInterface != storeRef.ProxyInterface ||
				ref.KernelInterface != storeRef.KernelInterface ||
				ref.ListenPort != storeRef.ListenPort ||
				ref.OwnerUUID != storeRef.OwnerUUID

			if mismatch {
				r.durableMu.RLock()
				authorized := r.isAuthorizedPortReplacementLocked(ref, storeRef)
				r.durableMu.RUnlock()
				if !authorized {
					return "", nil, fmt.Errorf("%w: store record for owner %q does not match durable ref and is not authorized by active transaction: store=(%d, %s, %s, %d) ref=(%d, %s, %s, %d)",
						mihomo.ErrForeignBridgeOwnership, canonicalOwner,
						storeRef.ProxyIndex, storeRef.ProxyInterface, storeRef.KernelInterface, storeRef.ListenPort,
						ref.ProxyIndex, ref.ProxyInterface, ref.KernelInterface, ref.ListenPort)
				}
			}
			if nb.LegacyOwner != "" && !containsString(legacyOwners, nb.LegacyOwner) {
				legacyOwners = append(legacyOwners, nb.LegacyOwner)
			}
		}
	}

	if ref.LegacyOwner != "" && !containsString(legacyOwners, ref.LegacyOwner) {
		legacyOwners = append(legacyOwners, ref.LegacyOwner)
	}
	if !containsString(legacyOwners, singbox.AllowAdoptEmptyDescription) {
		legacyOwners = append(legacyOwners, singbox.AllowAdoptEmptyDescription)
	}
	return canonicalOwner, legacyOwners, nil
}

func (r *mihomoBridgeRuntime) inspectBridgeLocked(
	ctx context.Context,
	ref mihomo.BridgeRef,
	canonicalOwner string,
	legacyOwners []string,
) (mihomo.ObservedBridge, error) {
	if r.gate == nil || r.gate.base == nil {
		return mihomo.ObservedBridge{}, fmt.Errorf("%w: bridge registrar is nil", mihomo.ErrForeignBridgeOwnership)
	}
	obsProxy, err := r.gate.InspectProxy(ctx, ref.ProxyIndex)
	if err != nil {
		return mihomo.ObservedBridge{}, fmt.Errorf("inspect proxy %d: %w", ref.ProxyIndex, err)
	}
	if !obsProxy.Exists {
		absentRef := mihomo.BridgeRef{
			ProxyIndex:      ref.ProxyIndex,
			ProxyInterface:  obsProxy.Name,
			KernelInterface: "",
			ListenPort:      0,
			OwnerUUID:       "",
			LegacyOwner:     "",
		}
		return mihomo.ObservedBridge{
			BridgeRef:  absentRef,
			Exists:     false,
			Up:         false,
			AssignedIP: obsProxy.Address,
		}, nil
	}

	obsRef := mihomo.BridgeRef{
		ProxyIndex:      ref.ProxyIndex,
		ProxyInterface:  obsProxy.Name,
		KernelInterface: obsProxy.SystemName, // strictly live observation; NEVER fall back to ref.KernelInterface
		ListenPort:      obsProxy.ListenPort,
		OwnerUUID:       "", // populated from live description below
		LegacyOwner:     "",
	}

	obs := mihomo.ObservedBridge{
		BridgeRef:  obsRef,
		Exists:     true,
		Up:         obsProxy.Up,
		AssignedIP: obsProxy.Address,
	}

	desc := obsProxy.Description
	token := singbox.ExtractProxyOwnerToken(desc)
	if desc == "" {
		// Empty description in NDMS means unmanaged interface
		obs.OwnerUUID = ""
		obs.LegacyOwner = ""
	} else if desc == canonicalOwner || (token != "" && token == canonicalOwner) {
		// Proven canonical owner
		obs.OwnerUUID = canonicalOwner
		obs.LegacyOwner = ""
		obs.BridgeRef.OwnerUUID = canonicalOwner
	} else if isAllowedLegacyOwner(desc, legacyOwners) || (token != "" && isAllowedLegacyOwner(token, legacyOwners)) {
		// Proven legacy owner
		obs.OwnerUUID = ""
		obs.LegacyOwner = desc
		obs.BridgeRef.LegacyOwner = desc
	} else {
		// Any other non-empty description is foreign
		obs.OwnerUUID = desc
		obs.LegacyOwner = ""
		obs.BridgeRef.OwnerUUID = desc
	}

	return obs, nil
}

// InspectBridge inspects the live NDMS state of a bridge interface, classifying its ownership
// mutually exclusively against the durable ref identity.
func (r *mihomoBridgeRuntime) InspectBridge(ctx context.Context, ref mihomo.BridgeRef) (mihomo.ObservedBridge, error) {
	canonicalOwner, legacyOwners, err := r.resolveInspectIdentity(ref)
	if err != nil {
		return mihomo.ObservedBridge{}, err
	}
	return r.inspectBridgeLocked(ctx, ref, canonicalOwner, legacyOwners)
}

// PublishBridge creates/updates a bridge interface in NDMS ensuring exact ownership by AWGM.
func (r *mihomoBridgeRuntime) PublishBridge(ctx context.Context, ref mihomo.BridgeRef) error {
	canonicalOwner, legacyOwners, listenPort, err := r.resolvePublishIdentity(ref)
	if err != nil {
		return err
	}
	if r.gate == nil || r.gate.base == nil {
		return fmt.Errorf("%w: bridge registrar is nil", mihomo.ErrForeignBridgeOwnership)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.gate.setReady(true)
	publishDesc := canonicalOwner
	if r.store != nil {
		for _, nb := range r.store.ListBridges() {
			if nb.Bridge.ProxyIndex == ref.ProxyIndex {
				publishDesc = mihomonative.FormatBridgeDescription(nb.Label, nb.Kind, nb.ID)
				break
			}
		}
	}
	allLegacy := append([]string{canonicalOwner}, legacyOwners...)
	owned, err := r.gate.EnsureProxyIfOwned(ctx, ref.ProxyIndex, listenPort, publishDesc, allLegacy...)
	if err != nil {
		return fmt.Errorf("publish bridge Proxy%d: %w", ref.ProxyIndex, err)
	}
	if !owned {
		return fmt.Errorf("%w: Proxy%d is owned by another entity", mihomo.ErrForeignBridgeOwnership, ref.ProxyIndex)
	}

	var obs mihomo.ObservedBridge
	deadline := time.Now().Add(10 * time.Second)
	for {
		obs, err = r.inspectBridgeLocked(ctx, ref, canonicalOwner, legacyOwners)
		if err == nil && obs.Exists && obs.OwnerUUID == canonicalOwner && obs.Up && (ref.KernelInterface == "" || obs.KernelInterface == ref.KernelInterface) {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break
		case <-time.After(200 * time.Millisecond):
		}
	}

	if err != nil {
		return fmt.Errorf("verify publish bridge Proxy%d: %w", ref.ProxyIndex, err)
	}
	if !obs.Exists || obs.OwnerUUID != canonicalOwner {
		return fmt.Errorf("%w: Proxy%d publication verification failed (exists=%v, live_owner=%q, want=%q)",
			mihomo.ErrForeignBridgeOwnership, ref.ProxyIndex, obs.Exists, obs.OwnerUUID, canonicalOwner)
	}
	if !obs.Up {
		return fmt.Errorf("verify publish bridge Proxy%d: interface is down", ref.ProxyIndex)
	}
	if ref.KernelInterface != "" && obs.KernelInterface != ref.KernelInterface {
		return fmt.Errorf("verify publish bridge Proxy%d: kernel interface mismatch: live=%q, want=%q",
			ref.ProxyIndex, obs.KernelInterface, ref.KernelInterface)
	}
	return nil
}

// WithdrawBridge tears down a bridge interface in NDMS only if proven owned by AWGM.
func (r *mihomoBridgeRuntime) WithdrawBridge(ctx context.Context, ref mihomo.BridgeRef) error {
	canonicalOwner, legacyOwners, err := r.resolveWithdrawIdentity(ref)
	if err != nil {
		return err
	}
	if r.gate == nil || r.gate.base == nil {
		return fmt.Errorf("%w: bridge registrar is nil", mihomo.ErrForeignBridgeOwnership)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	removed, err := r.gate.RemoveProxyIfOwned(ctx, ref.ProxyIndex, canonicalOwner, legacyOwners...)
	if err != nil {
		return fmt.Errorf("withdraw bridge Proxy%d: %w", ref.ProxyIndex, err)
	}
	if !removed {
		return fmt.Errorf("%w: refusing to withdraw unowned bridge Proxy%d (live owner is not %q)", mihomo.ErrForeignBridgeOwnership, ref.ProxyIndex, canonicalOwner)
	}

	obs, err := r.inspectBridgeLocked(ctx, ref, canonicalOwner, legacyOwners)
	if err != nil {
		return fmt.Errorf("verify withdraw bridge Proxy%d: %w", ref.ProxyIndex, err)
	}
	if obs.Exists && (obs.OwnerUUID == canonicalOwner || isAllowedLegacyOwner(obs.LegacyOwner, legacyOwners)) {
		return fmt.Errorf("%w: Proxy%d still exists with owned description after withdraw", mihomo.ErrForeignBridgeOwnership, ref.ProxyIndex)
	}
	return nil
}

// ListObservedBridges inspects only a validated union of durable and current-store bridges.
// It keys by full identity (BridgeRef.Digest()) to ensure both durable A and store B
// sharing the same slot can be inspected during replacement/recovery.
// It never performs arbitrary numeric slot scans.
func (r *mihomoBridgeRuntime) ListObservedBridges(ctx context.Context) ([]mihomo.ObservedBridge, error) {
	if r == nil {
		return nil, nil
	}

	refsByIdentity := make(map[string]mihomo.BridgeRef)

	// 1. Durable bridges from registry cache
	r.durableMu.RLock()
	for _, b := range r.durableBridges {
		refsByIdentity[b.Digest()] = b
	}
	r.durableMu.RUnlock()

	// 2. Current native store bridges
	if r.store != nil {
		for _, nb := range r.store.ListBridges() {
			ref := mihomo.BridgeRef{
				ProxyIndex:      nb.Bridge.ProxyIndex,
				ProxyInterface:  nb.Bridge.ProxyInterface,
				KernelInterface: nb.Bridge.KernelInterface,
				ListenPort:      nb.Bridge.ListenPort,
				LegacyOwner:     nb.LegacyOwner,
				OwnerUUID:       mihomonative.BridgeOwnershipDescription(nb.Kind, nb.ID),
			}
			refsByIdentity[ref.Digest()] = ref
		}
	}

	keys := make([]string, 0, len(refsByIdentity))
	for k := range refsByIdentity {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ri := refsByIdentity[keys[i]]
		rj := refsByIdentity[keys[j]]
		if ri.ProxyIndex != rj.ProxyIndex {
			return ri.ProxyIndex < rj.ProxyIndex
		}
		if ri.OwnerUUID != rj.OwnerUUID {
			return ri.OwnerUUID < rj.OwnerUUID
		}
		return ri.ListenPort < rj.ListenPort
	})

	var out []mihomo.ObservedBridge
	for _, k := range keys {
		ref := refsByIdentity[k]
		obs, err := r.InspectBridge(ctx, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, nil
}

type bridgeCompensationAction struct {
	action string // "withdraw" or "restore"
	ref    mihomo.BridgeRef
}

func (r *mihomoBridgeRuntime) runCompensations(ctx context.Context, compensations []bridgeCompensationAction) error {
	r.durableMu.Lock()
	if r.compensatingRefs == nil {
		r.compensatingRefs = make(map[string]struct{})
	}
	for _, act := range compensations {
		r.compensatingRefs[act.ref.Digest()] = struct{}{}
	}
	r.durableMu.Unlock()

	defer func() {
		r.durableMu.Lock()
		r.compensatingRefs = make(map[string]struct{})
		r.durableMu.Unlock()
	}()

	var compErr error
	for i := len(compensations) - 1; i >= 0; i-- {
		item := compensations[i]
		switch item.action {
		case "withdraw":
			if err := r.WithdrawBridge(ctx, item.ref); err != nil {
				compErr = errors.Join(compErr, fmt.Errorf("compensation withdraw %s: %w", item.ref.SlotKey(), err))
			}
		case "restore":
			if err := r.PublishBridge(ctx, item.ref); err != nil {
				compErr = errors.Join(compErr, fmt.Errorf("compensation restore %s: %w", item.ref.SlotKey(), err))
			}
		}
	}
	return compErr
}

// ApplyBridges ensures all requested bridges are active in the kernel/NDMS.
// It inspects each bridge before publication, classifies it (absent, exact-owned, or foreign),
// and compensates ONLY mutations introduced by the current batch. Pre-existing bridges remain untouched.
func (r *mihomoBridgeRuntime) ApplyBridges(ctx context.Context, bridges []mihomo.BridgeRef) error {
	if r == nil {
		return nil
	}
	var compensations []bridgeCompensationAction
	for _, ref := range bridges {
		obs, err := r.InspectBridge(ctx, ref)
		if err != nil {
			compErr := r.runCompensations(ctx, compensations)
			return errors.Join(fmt.Errorf("inspect bridge %s before apply: %w", ref.SlotKey(), err), compErr)
		}

		if !obs.Exists {
			// Absent: publish and add to compensation set as withdraw
			if err := r.PublishBridge(ctx, ref); err != nil {
				compErr := r.runCompensations(ctx, compensations)
				return errors.Join(fmt.Errorf("apply bridge %s: %w", ref.SlotKey(), err), compErr)
			}
			compensations = append(compensations, bridgeCompensationAction{action: "withdraw", ref: ref})
			continue
		}

		// Bridge exists. Verify ownership
		if obs.OwnerUUID != ref.OwnerUUID {
			// Foreign or unmanaged: fail closed without mutation
			origErr := fmt.Errorf("%w: cannot apply bridge %s: slot %d occupied by %q",
				mihomo.ErrForeignBridgeOwnership, ref.SlotKey(), ref.ProxyIndex, obs.OwnerUUID)
			compErr := r.runCompensations(ctx, compensations)
			return errors.Join(origErr, compErr)
		}

		// Exact-owned by AWGM. Check observable identity
		isIdentical := obs.BridgeRef.ProxyIndex == ref.ProxyIndex &&
			obs.BridgeRef.ProxyInterface == ref.ProxyInterface &&
			obs.BridgeRef.KernelInterface == ref.KernelInterface &&
			obs.BridgeRef.ListenPort == ref.ListenPort &&
			obs.Up

		if isIdentical {
			// Already present and exact-owned with correct observable identity:
			// Refresh/publish idempotently, but DO NOT add to compensation
			if err := r.PublishBridge(ctx, ref); err != nil {
				compErr := r.runCompensations(ctx, compensations)
				return errors.Join(fmt.Errorf("apply bridge %s: %w", ref.SlotKey(), err), compErr)
			}
		} else {
			// Modified: live target port must be genuinely observable before attempting mutation
			if obs.BridgeRef.ListenPort <= 0 {
				origErr := fmt.Errorf("%w: cannot safely modify bridge %s: live listen port cannot be observed for before-image compensation",
					mihomo.ErrVerificationFailed, ref.SlotKey())
				compErr := r.runCompensations(ctx, compensations)
				return errors.Join(origErr, compErr)
			}
			beforeRef := obs.BridgeRef
			if err := r.PublishBridge(ctx, ref); err != nil {
				compErr := r.runCompensations(ctx, compensations)
				return errors.Join(fmt.Errorf("apply bridge %s: %w", ref.SlotKey(), err), compErr)
			}
			compensations = append(compensations, bridgeCompensationAction{action: "restore", ref: beforeRef})
		}
	}
	return nil
}

// WithdrawBridges withdraws the specified bridge interfaces from the kernel/NDMS using exact per-bridge operations.
func (r *mihomoBridgeRuntime) WithdrawBridges(ctx context.Context, bridges []mihomo.BridgeRef) error {
	if r == nil {
		return nil
	}
	var joined error
	for _, ref := range bridges {
		if err := r.WithdrawBridge(ctx, ref); err != nil {
			joined = errors.Join(joined, fmt.Errorf("withdraw bridge %s: %w", ref.SlotKey(), err))
		}
	}
	return joined
}

// VerifyBridges verifies that Mihomo is listening on the required bridge ports.
func (r *mihomoBridgeRuntime) VerifyBridges(ctx context.Context, bridges []mihomo.BridgeRef) error {
	if r == nil || r.store == nil {
		return nil
	}
	listeners := r.store.ConfigBridgeListeners()
	if len(listeners) == 0 {
		return nil
	}
	targetPorts := make(map[int]bool)
	for _, b := range bridges {
		for _, nb := range r.store.ListBridges() {
			if nb.Bridge.ProxyIndex == b.ProxyIndex {
				targetPorts[nb.Bridge.ListenPort] = true
			}
		}
	}
	var requiredListeners []mihomonative.BridgeListener
	for _, l := range listeners {
		if targetPorts[l.Port] {
			requiredListeners = append(requiredListeners, l)
		}
	}
	if len(requiredListeners) > 0 {
		return waitForMihomoBridgeListeners(ctx, requiredListeners)
	}
	return nil
}

// ListActiveBridges returns all currently registered bridge interfaces with canonical OwnerUUID.
func (r *mihomoBridgeRuntime) ListActiveBridges(ctx context.Context) ([]mihomo.BridgeRef, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	nativeBridges := r.store.ListBridges()
	out := make([]mihomo.BridgeRef, len(nativeBridges))
	for i, nb := range nativeBridges {
		out[i] = mihomo.BridgeRef{
			ProxyIndex:      nb.Bridge.ProxyIndex,
			ProxyInterface:  nb.Bridge.ProxyInterface,
			KernelInterface: nb.Bridge.KernelInterface,
			ListenPort:      nb.Bridge.ListenPort,
			LegacyOwner:     nb.LegacyOwner,
			OwnerUUID:       mihomonative.BridgeOwnershipDescription(nb.Kind, nb.ID),
		}
	}
	return out, nil
}

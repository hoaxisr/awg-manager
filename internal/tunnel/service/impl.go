package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/cache"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/traffic"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
	"github.com/hoaxisr/awg-manager/internal/tunnel/ops"
	"github.com/hoaxisr/awg-manager/internal/tunnel/state"
	"github.com/hoaxisr/awg-manager/internal/tunnel/wan"
)

// ServiceImpl is the concrete implementation of Service.
type ServiceImpl struct {
	store          *storage.AWGTunnelStore
	state          state.Manager         // state detection for kernel tunnels only
	nwgOperator    *nwg.OperatorNativeWG // NativeWG backend (nil if unavailable)
	legacyOperator ops.Operator          // Kernel backend (OS5/OS4)
	appLog         *logging.ScopedLogger // UI-visible logging

	// opkgOccupancy — занятость номеров OpkgTun (живые интерфейсы плюс пины
	// чужих подсистем). Нужна только kernel-ветке выдачи идентификатора:
	// номер kernel-туннеля одновременно является номером интерфейса.
	opkgOccupancy storage.OpkgTunPins

	// tunnelMu provides per-tunnel mutexes for lifecycle operations.
	// Key: tunnelID (string), Value: *sync.Mutex
	tunnelMu sync.Map

	// wan is the unified WAN state model (up/down tracking).
	wan *wan.Model

	// orch is the orchestrator for lifecycle operations (Start/Stop/Restart/Delete).
	orch *orchestrator.Orchestrator

	// bus is the event bus for SSE publishing.
	bus *events.Bus

	// stateCache dedups raw state reads across read paths (TTL 2s +
	// singleflight). nil in bare test constructions.
	stateCache      *cache.KeyedStore[string, tunnel.StateInfo]
	invalidatorOnce sync.Once

	// selfCreateGate (optional) suppresses the hook-driven snapshot refresh
	// during awg-manager-initiated NDMS interface creations. Without it,
	// the ifcreated hook fires (and rebroadcasts system tunnels) before
	// our own store.Save completes — producing a transient ghost entry in
	// the system tunnels list.
	selfCreateGate tunnel.SelfCreateGater

	awgSyncer AWGSyncer

	deviceProxyRefs DeviceProxyRefChecker
	routerRefs      RouterRefChecker
}

type AWGSyncer interface {
	SyncAWGOutbounds(ctx context.Context) error
}

func (s *ServiceImpl) SetAWGSyncer(sync AWGSyncer) { s.awgSyncer = sync }

// SetOpkgTunOccupancy задаёт источник занятости номеров OpkgTun.
func (s *ServiceImpl) SetOpkgTunOccupancy(occ storage.OpkgTunPins) { s.opkgOccupancy = occ }

func (s *ServiceImpl) SetDeviceProxyRefChecker(c DeviceProxyRefChecker) { s.deviceProxyRefs = c }
func (s *ServiceImpl) SetRouterRefChecker(c RouterRefChecker)           { s.routerRefs = c }

func (s *ServiceImpl) notifyAWGSyncer(ctx context.Context) {
	if s.awgSyncer == nil {
		return
	}
	if err := s.awgSyncer.SyncAWGOutbounds(ctx); err != nil {
		s.appLog.Warn("awg-sync", "", err.Error())
	}
}

// New creates a new TunnelService.
func New(
	store *storage.AWGTunnelStore,
	nwgOp *nwg.OperatorNativeWG,
	legacyOp ops.Operator,
	stateMgr state.Manager,
	wanModel *wan.Model,
	appLogger logging.AppLogger,
) *ServiceImpl {
	svc := &ServiceImpl{
		store:          store,
		state:          stateMgr,
		nwgOperator:    nwgOp,
		legacyOperator: legacyOp,
		appLog:         logging.NewScopedLogger(appLogger, logging.GroupTunnel, logging.SubLifecycle),
		wan:            wanModel,
	}
	svc.stateCache = cache.NewKeyedStore[string, tunnel.StateInfo](
		stateCacheTTL, nil, "tunnel state", svc.fetchRawStateByID)
	return svc
}

// WANModel returns the WAN state model for direct access by API handlers.
func (s *ServiceImpl) WANModel() *wan.Model { return s.wan }

// SetSelfCreateGate wires the self-create gate used to suppress hook-driven
// snapshot refreshes during Create/Import. Optional; nil is safe (code paths
// degrade to the old behavior).
func (s *ServiceImpl) SetSelfCreateGate(g tunnel.SelfCreateGater) { s.selfCreateGate = g }

// GetResolvedISP returns the resolved ISP interface name for a running tunnel.
func (s *ServiceImpl) GetResolvedISP(tunnelID string) string {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return ""
	}
	return stored.ActiveWAN
}

// SetOrchestrator sets the orchestrator for lifecycle delegation.
func (s *ServiceImpl) SetOrchestrator(orch *orchestrator.Orchestrator) {
	s.orch = orch
}

// SetEventBus sets the event bus for SSE publishing.
func (s *ServiceImpl) SetEventBus(bus *events.Bus) {
	s.bus = bus
	s.startStateInvalidator(bus)
}

// RunningTunnels returns the list of currently running tunnels for the traffic collector.
//
// Per-tunnel GetState вызовы идут в горутинах: each ждёт NDMS RCI ~150ms,
// последовательно = O(N×150ms). Параллельные сабмиты прилетают в
// transport batcher одновременно (за <1ms) и объединяются в один HTTP
// POST с массивом запросов → O(150ms) total независимо от N.
func (s *ServiceImpl) RunningTunnels(ctx context.Context) []traffic.RunningTunnel {
	stored, err := s.store.List()
	if err != nil {
		return nil
	}
	type slot struct {
		idx int
		rt  traffic.RunningTunnel
		ok  bool
	}
	slots := make([]slot, len(stored))
	var wg sync.WaitGroup
	for i := range stored {
		t := stored[i]
		if t.Backend != "wdtt-raw" && !t.Enabled {
			continue
		}
		wg.Add(1)
		go func(i int, t storage.AWGTunnel) {
			defer wg.Done()
			si := s.rawState(ctx, &t)
			if si.State != tunnel.StateRunning {
				return
			}
			var ifaceName, ndmsName string
			switch t.Backend {
			case "nativewg":
				names := nwg.NewNWGNames(t.NWGIndex)
				ifaceName = names.IfaceName
				ndmsName = names.NDMSName
			case "wdtt-raw":
				ifaceName = strings.TrimSpace(t.RawKernelIface)
				ndmsName = strings.TrimSpace(t.RawNdmsIface)
				if ifaceName == "" {
					return
				}
			default:
				names := tunnel.NewNames(t.ID)
				ifaceName = names.IfaceName
				ndmsName = names.NDMSName
			}
			slots[i] = slot{
				idx: i,
				ok:  true,
				rt: traffic.RunningTunnel{
					ID:            t.ID,
					BackendType:   s.backendLabel(&t),
					IfaceName:     ifaceName,
					NDMSName:      ndmsName,
					RxBytes:       si.RxBytes,
					TxBytes:       si.TxBytes,
					LastHandshake: si.LastHandshake,
					ConnectedAt:   si.ConnectedAt,
				},
			}
		}(i, t)
	}
	wg.Wait()
	result := make([]traffic.RunningTunnel, 0, len(stored))
	for _, s := range slots {
		if s.ok {
			result = append(result, s.rt)
		}
	}
	return result
}

// lockTunnel acquires the per-tunnel mutex.
func (s *ServiceImpl) lockTunnel(tunnelID string) {
	mu, _ := s.tunnelMu.LoadOrStore(tunnelID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
}

// unlockTunnel releases the per-tunnel mutex.
func (s *ServiceImpl) unlockTunnel(tunnelID string) {
	if mu, ok := s.tunnelMu.Load(tunnelID); ok {
		mu.(*sync.Mutex).Unlock()
	}
}

// === CRUD Operations ===

// Create создаёт туннель целиком: ресурс в NDMS, запись в хранилище и .conf —
// одной операцией с откатом. Конфиг для оператора собирается здесь же из
// записи каноническим StoredToConfig: вызывающий передаёт только запись, и
// расходиться этим двум источникам больше негде.
func (s *ServiceImpl) Create(ctx context.Context, stored *storage.AWGTunnel) error {
	if stored == nil {
		return fmt.Errorf("nil tunnel record")
	}
	tunnelID := stored.ID
	cfg := orchestrator.StoredToConfig(stored)
	// StoredToConfig это поле не переносит — те, кому оно нужно, дописывают
	// его сами (так делает и оркестратор перед запуском). Без него отметка
	// «маршрут по умолчанию» не действовала до первого включения туннеля.
	cfg.DefaultRoute = stored.DefaultRoute

	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	// Check if tunnel already exists in storage
	if s.store.Exists(tunnelID) {
		return tunnel.ErrAlreadyExists
	}

	// NativeWG path
	if s.isNativeWG(stored) {
		if s.nwgOperator == nil {
			return fmt.Errorf("NativeWG backend not available")
		}
		index, err := s.nwgOperator.Create(ctx, stored)
		if err != nil {
			return err
		}
		stored.NWGIndex = index
		// Симметрично kernel-ветке: запись сохраняем здесь, иначе созданный
		// в NDMS интерфейс осиротеет. Конфиг для nativewg не пишется — его
		// никто не читает.
		if err := s.store.Create(stored); err != nil {
			if derr := s.nwgOperator.Delete(ctx, stored); derr != nil {
				s.logWarn("create", tunnelID, "откат не удался, интерфейс остался в NDMS: "+derr.Error())
			}
			return fmt.Errorf("save tunnel: %w", err)
		}
		s.logInfo("create", tunnelID, "NativeWG tunnel created")
		// Legacy tunnel:created publish removed (Task 14 sweep); handler
		// layer calls publishTunnelList → resource:invalidated after all
		// mutations, so no subscriber missed an update.
		s.notifyAWGSyncer(ctx)
		return nil
	}

	// Kernel path: create in NDMS (for OS5, no-op for OS4)
	if err := s.legacyOperator.Create(ctx, cfg); err != nil {
		return err
	}

	// Запись и конфиг — здесь же, а не у вызывающего: ресурс в NDMS уже
	// создан, и если сохранить его не удастся, он останется жить без записи.
	// Никто уже не будет знать, что он наш, и никто его не уберёт: стартовый
	// подметатель ходит только по записям, а полная уборка бывает лишь при
	// удалении пакета.
	if err := s.store.Create(stored); err != nil {
		if derr := s.legacyOperator.Delete(ctx, stored); derr != nil {
			s.logWarn("create", tunnelID, "откат не удался, интерфейс остался в NDMS: "+derr.Error())
		}
		return fmt.Errorf("save tunnel: %w", err)
	}
	if err := config.WriteFile(stored); err != nil {
		if derr := s.store.Delete(tunnelID); derr != nil {
			s.logWarn("create", tunnelID, "откат не удался, запись осталась: "+derr.Error())
		}
		if derr := s.legacyOperator.Delete(ctx, stored); derr != nil {
			s.logWarn("create", tunnelID, "откат не удался, интерфейс остался в NDMS: "+derr.Error())
		}
		return fmt.Errorf("write config: %w", err)
	}

	s.logInfo("create", tunnelID, "Tunnel created")
	// Legacy tunnel:created publish removed (Task 14 sweep); handler
	// layer emits resource:invalidated via publishTunnelList.
	s.notifyAWGSyncer(ctx)
	return nil
}

// storedIfaceNames resolves kernel and NDMS interface names for a stored
// tunnel. Kernel tunnels do have an NDMS name (awgN -> OpkgTunN); only OS4
// awgmN and raw clients without a live iface legitimately have none.
func storedIfaceNames(t *storage.AWGTunnel) (ifaceName, ndmsName string) {
	switch t.Backend {
	case "nativewg":
		names := nwg.NewNWGNames(t.NWGIndex)
		return names.IfaceName, names.NDMSName
	case "wdtt-raw":
		return strings.TrimSpace(t.RawKernelIface), strings.TrimSpace(t.RawNdmsIface)
	default:
		names := tunnel.NewNames(t.ID)
		return names.IfaceName, names.NDMSName
	}
}

// Get returns a tunnel with its current state.
func (s *ServiceImpl) Get(ctx context.Context, tunnelID string) (*TunnelWithStatus, error) {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return nil, tunnel.ErrNotFound
	}

	stateInfo := s.stateForStored(ctx, stored)

	ifaceName, ndmsName := storedIfaceNames(stored)

	return &TunnelWithStatus{
		ID:            stored.ID,
		Name:          stored.Name,
		Config:        orchestrator.StoredToConfig(stored),
		State:         stateInfo.State,
		StateInfo:     stateInfo,
		Enabled:       stored.Enabled,
		AutoStart:     stored.Enabled, // AutoStart == Enabled in current design
		PingCheckOn:   stored.PingCheck != nil && stored.PingCheck.Enabled,
		DefaultRoute:  stored.DefaultRoute,
		ISPInterface:  stored.ISPInterface,
		InterfaceName: ifaceName,
		NDMSName:      ndmsName,
		ConfigPreview: config.Generate(stored),
		Backend:       s.backendLabel(stored),
	}, nil
}

// List returns all tunnels with their current states.
//
// Per-tunnel GetState вызовы идут параллельно: каждый блокируется на
// NDMS RCI ~150ms, последовательно = O(N×150ms). Параллельные сабмиты
// объединяются transport batcher'ом в один HTTP POST → O(150ms) total
// независимо от N туннелей.
func (s *ServiceImpl) List(ctx context.Context) ([]TunnelWithStatus, error) {
	stored, err := s.store.List()
	if err != nil {
		return nil, fmt.Errorf("list tunnels: %w", err)
	}

	result := make([]TunnelWithStatus, len(stored))
	var wg sync.WaitGroup
	for i := range stored {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			t := stored[i]
			stateInfo := s.stateForStored(ctx, &t)

			ifaceName, ndmsName := storedIfaceNames(&t)
			result[i] = TunnelWithStatus{
				ID:            t.ID,
				Name:          t.Name,
				Config:        orchestrator.StoredToConfig(&t),
				State:         stateInfo.State,
				StateInfo:     stateInfo,
				Enabled:       t.Enabled,
				AutoStart:     t.Enabled,
				PingCheckOn:   t.PingCheck != nil && t.PingCheck.Enabled,
				DefaultRoute:  t.DefaultRoute,
				ISPInterface:  t.ISPInterface,
				InterfaceName: ifaceName,
				NDMSName:      ndmsName,
				Backend:       s.backendLabel(&t),
			}
		}(i)
	}
	wg.Wait()
	return result, nil
}

// Update applies the difference between oldStored and newStored to the
// running tunnel via RCI commands. Storage save is the handler's
// responsibility — this method does NOT persist anything.
//
// Per-field Sync* operations are dispatched only for fields that actually
// changed, minimising RCI traffic. Pre-condition validation rejects empty
// Address or non-positive MTU.
func (s *ServiceImpl) Update(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error {
	if oldStored == nil || newStored == nil {
		return fmt.Errorf("oldStored and newStored must not be nil")
	}
	tunnelID := newStored.ID
	if tunnelID == "" || tunnelID != oldStored.ID {
		return fmt.Errorf("tunnel id mismatch")
	}

	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	if newStored.Interface.Address == "" {
		return fmt.Errorf("address must not be empty")
	}
	if newStored.Interface.MTU <= 0 {
		return fmt.Errorf("MTU must be > 0")
	}

	// Block address change in kernel mode once OpkgTun or the backend process
	// exists — NDMS/kernel cannot rename the live interface. Before first
	// start (not_created / no iface) only the .conf is updated.
	if !s.isNativeWG(newStored) && newStored.Interface.Address != oldStored.Interface.Address {
		stateInfo := s.state.GetState(ctx, tunnelID)
		if stateInfo.BackendType == "kernel" && (stateInfo.OpkgTunExists || stateInfo.ProcessRunning) {
			return fmt.Errorf("address change is not supported in kernel mode")
		}
	}

	// Regenerate kernel .conf if any conf-affecting field changed.
	confChanged := !awgInterfaceEqual(oldStored.Interface, newStored.Interface) ||
		!awgPeerEqual(oldStored.Peer, newStored.Peer)
	if confChanged && !s.isNativeWG(newStored) {
		if err := config.WriteFile(newStored); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
	}

	// Description rename — cheap, dispatch on change.
	if oldStored.Name != newStored.Name {
		if s.isNativeWG(newStored) && s.nwgOperator != nil {
			if err := s.nwgOperator.UpdateDescription(ctx, newStored, newStored.Name); err != nil {
				s.logWarn("update", tunnelID, "Failed to update description: "+err.Error())
			}
		} else {
			if err := s.legacyOperator.UpdateDescription(ctx, tunnelID, newStored.Name); err != nil {
				s.logWarn("update", tunnelID, "Failed to update description: "+err.Error())
			}
		}
	}

	// Below this point we only act on the running interface. Skip if not.
	var stateInfo tunnel.StateInfo
	if s.isNativeWG(newStored) && s.nwgOperator != nil {
		stateInfo = s.nwgOperator.GetState(ctx, newStored)
	} else {
		stateInfo = s.state.GetState(ctx, tunnelID)
	}
	if stateInfo.State != tunnel.StateRunning {
		s.logInfo("update", tunnelID, "Tunnel updated (not running, runtime sync skipped)")
		return nil
	}

	if s.isNativeWG(newStored) && s.nwgOperator != nil {
		if err := s.applyDiffNWG(ctx, oldStored, newStored); err != nil {
			return err
		}
	} else {
		if err := s.applyDiffKernel(ctx, oldStored, newStored); err != nil {
			return err
		}
	}

	s.logInfo("update", tunnelID, "Tunnel updated")
	s.notifyAWGSyncer(ctx)
	s.invalidateState(newStored.ID)
	return nil
}

// applyDiffKernel applies field-level diffs to a running kernel-backend
// tunnel via the legacy operator.
//
// Each Sync* failure is logged AND collected into the returned error so
// the handler can fail-closed (reject the storage save). All Sync*
// dispatches still run regardless — one field's failure does not block
// reconciliation of the others. Returns nil only if every dispatch
// succeeded.
func (s *ServiceImpl) applyDiffKernel(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error {
	tunnelID := newStored.ID
	confPath := tunnel.NewNames(tunnelID).ConfPath
	var errs []error

	if !awgInterfaceEqual(oldStored.Interface, newStored.Interface) ||
		!awgPeerEqual(oldStored.Peer, newStored.Peer) {
		if err := s.legacyOperator.ApplyConfig(ctx, tunnelID, confPath); err != nil {
			s.logWarn("update", tunnelID, "Failed to apply config: "+err.Error())
			errs = append(errs, fmt.Errorf("apply config: %w", err))
		}
	}

	if oldStored.Interface.MTU != newStored.Interface.MTU {
		if err := s.legacyOperator.SetMTU(ctx, tunnelID, newStored.Interface.MTU); err != nil {
			s.logWarn("update", tunnelID, "Failed to apply MTU: "+err.Error())
			errs = append(errs, fmt.Errorf("set MTU: %w", err))
		}
	}

	if oldStored.Interface.DNS != newStored.Interface.DNS {
		if err := s.legacyOperator.SyncDNS(ctx, tunnelID, tunnel.ParseDNSList(newStored.Interface.DNS)); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync DNS: "+err.Error())
			errs = append(errs, fmt.Errorf("sync DNS: %w", err))
		}
	}

	if oldStored.Interface.Address != newStored.Interface.Address {
		ipv4, ipv6 := orchestrator.SplitAddresses(newStored.Interface.Address)
		prefix := orchestrator.AddressPrefixOf(newStored.Interface.Address)
		if err := s.legacyOperator.SyncAddress(ctx, tunnelID, ipv4, prefix, ipv6); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync address: "+err.Error())
			errs = append(errs, fmt.Errorf("sync address: %w", err))
		}
	}

	if oldStored.Peer.Endpoint != newStored.Peer.Endpoint || oldStored.ISPInterface != newStored.ISPInterface {
		_ = s.legacyOperator.CleanupEndpointRoute(ctx, tunnelID)
		resolvedWAN, resolveErr := s.resolveWAN(ctx, newStored.ISPInterface)
		if resolveErr != nil {
			s.logWarn("update", tunnelID, "Failed to resolve WAN: "+resolveErr.Error())
			errs = append(errs, fmt.Errorf("resolve WAN: %w", resolveErr))
		} else if ip, err := s.legacyOperator.SetupEndpointRoute(ctx, tunnelID, newStored.Peer.Endpoint, s.resolveKernelDevice(resolvedWAN), resolvedWAN); err != nil {
			s.logWarn("update", tunnelID, "Failed to setup endpoint route: "+err.Error())
			errs = append(errs, fmt.Errorf("setup endpoint route: %w", err))
		} else {
			newStored.ResolvedEndpointIP = ip
			newStored.ActiveWAN = resolvedWAN
		}
	}

	if oldStored.DefaultRoute != newStored.DefaultRoute {
		s.logInfo("update", tunnelID, fmt.Sprintf("DefaultRoute changed to %v (apply via /api/control/toggle-default-route)", newStored.DefaultRoute))
	}

	return errors.Join(errs...)
}

// applyDiffNWG applies field-level diffs to a running NativeWG tunnel.
// See applyDiffKernel for the error-collection contract.
func (s *ServiceImpl) applyDiffNWG(ctx context.Context, oldStored, newStored *storage.AWGTunnel) error {
	tunnelID := newStored.ID
	var errs []error

	// Правка через границу 2.0↔3.x меняет сам путь туннеля: ASC прошивки или
	// awg_proxy. Посинхронно этот переход не применяется — половина параметров
	// осталась бы у прошивки, половина у kmod, обе обфускации легли бы друг на
	// друга, и туннель выглядел бы живым, не пропуская ни пакета. Только
	// полный перезапуск: Stop снимает слот и параметры прежнего пути, Start
	// поднимает по новому.
	if nwg.UsesProxyPath(&oldStored.Interface) != nwg.UsesProxyPath(&newStored.Interface) {
		s.logInfo("update", tunnelID, "путь туннеля меняется (ASC ↔ awg_proxy) — перезапуск")
		if err := s.nwgOperator.Stop(ctx, oldStored); err != nil {
			s.logWarn("update", tunnelID, "Failed to stop on path switch: "+err.Error())
			return fmt.Errorf("stop on path switch: %w", err)
		}
		if err := s.nwgOperator.Start(ctx, newStored); err != nil {
			s.logWarn("update", tunnelID, "Failed to start on path switch: "+err.Error())
			return fmt.Errorf("start on path switch: %w", err)
		}
		return nil
	}

	if oldStored.Interface.PrivateKey != newStored.Interface.PrivateKey {
		if err := s.nwgOperator.SyncPrivateKey(ctx, newStored); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync NWG private-key: "+err.Error())
			errs = append(errs, fmt.Errorf("sync private-key: %w", err))
		}
	}

	if oldStored.Interface.Address != newStored.Interface.Address ||
		oldStored.Interface.MTU != newStored.Interface.MTU {
		if err := s.nwgOperator.SyncAddressMTU(ctx, newStored); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync NWG address/MTU: "+err.Error())
			errs = append(errs, fmt.Errorf("sync address/MTU: %w", err))
		}
	}

	if oldStored.Interface.DNS != newStored.Interface.DNS {
		oldList := tunnel.ParseDNSList(oldStored.Interface.DNS)
		newList := tunnel.ParseDNSList(newStored.Interface.DNS)
		if err := s.nwgOperator.SyncDNS(ctx, newStored, oldList, newList); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync NWG DNS: "+err.Error())
			errs = append(errs, fmt.Errorf("sync DNS: %w", err))
		}
	}

	if !awgPeerEqual(oldStored.Peer, newStored.Peer) {
		if err := s.nwgOperator.SyncPeer(ctx, newStored, oldStored.Peer.PublicKey); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync NWG peer: "+err.Error())
			errs = append(errs, fmt.Errorf("sync peer: %w", err))
		}
	}

	if !awgParamsEqual(oldStored.Interface, newStored.Interface) {
		if err := s.nwgOperator.SyncAWGParams(ctx, newStored); err != nil {
			// AWG params may need restart on some firmware — log Warn but
			// don't fail the entire Update; user gets the rest of the diff
			// applied and a restart hint.
			s.logWarn("update", tunnelID, "Failed to sync NWG AWG params (restart may be required): "+err.Error())
		}
	}

	// Rebuild the kmod proxy slot when fields that shape it change. Without
	// this, the slot keeps pre-Update keys/obfuscation silently, and the
	// next daemon-restart's RestoreTunnel adopts the stale slot — handshake
	// fails forever with no log line beyond "adopt-tunnel". SyncKmodSlot
	// is a no-op on ASC-native firmware (no kmod slot exists).
	if kmodShapingChanged(oldStored, newStored) {
		if err := s.nwgOperator.SyncKmodSlot(ctx, newStored); err != nil {
			s.logWarn("update", tunnelID, "Failed to sync kmod slot: "+err.Error())
			errs = append(errs, fmt.Errorf("sync kmod slot: %w", err))
		}
	}

	if oldStored.ISPInterface != newStored.ISPInterface {
		s.logInfo("update", tunnelID, "ISPInterface changed; restart tunnel to apply route changes")
	}

	if oldStored.DefaultRoute != newStored.DefaultRoute {
		s.logInfo("update", tunnelID, fmt.Sprintf("DefaultRoute changed to %v (apply via NDMS toggle)", newStored.DefaultRoute))
	}

	return errors.Join(errs...)
}

// awgInterfaceEqual returns true when two AWGInterface structs are
// identical. Used to skip redundant config regeneration.
func awgInterfaceEqual(a, b storage.AWGInterface) bool {
	return a == b
}

// awgPeerEqual returns true when two AWGPeer structs hold the same data.
// AllowedIPs is treated as a set (order-independent) — WireGuard itself
// has no semantic ordering for allowed-ips, so [0.0.0.0/0, ::/0] and
// [::/0, 0.0.0.0/0] are the same peer config.
func awgPeerEqual(a, b storage.AWGPeer) bool {
	if a.PublicKey != b.PublicKey ||
		a.PresharedKey != b.PresharedKey ||
		a.Endpoint != b.Endpoint ||
		a.PersistentKeepalive != b.PersistentKeepalive {
		return false
	}
	if len(a.AllowedIPs) != len(b.AllowedIPs) {
		return false
	}
	if len(a.AllowedIPs) == 0 {
		return true
	}
	aSorted := append([]string(nil), a.AllowedIPs...)
	bSorted := append([]string(nil), b.AllowedIPs...)
	sort.Strings(aSorted)
	sort.Strings(bSorted)
	for i := range aSorted {
		if aSorted[i] != bSorted[i] {
			return false
		}
	}
	return true
}

// awgParamsEqual reports whether AmneziaWG obfuscation parameters are
// identical between two interfaces. Comparison is delegated to the
// embedded AWGObfuscation value type — `==` automatically picks up
// new obfuscation fields without manual enumeration.
func awgParamsEqual(a, b storage.AWGInterface) bool {
	return a.AWGObfuscation == b.AWGObfuscation
}

// kmodShapingChanged reports whether any field that shapes the awg_proxy.ko
// slot differs between the old and new stored configs: PrivateKey,
// Peer.PublicKey, Peer.Endpoint, and obfuscation parameters. When true,
// applyDiffNWG must rebuild the slot — otherwise it keeps pre-Update
// values silently and the next daemon-restart's RestoreTunnel adopts the
// stale slot. PresharedKey is NOT included: it's WG-side only, the kmod
// proxy does not see it.
func kmodShapingChanged(old, neu *storage.AWGTunnel) bool {
	return old.Interface.PrivateKey != neu.Interface.PrivateKey ||
		old.Peer.PublicKey != neu.Peer.PublicKey ||
		old.Peer.Endpoint != neu.Peer.Endpoint ||
		old.Interface.AWGObfuscation != neu.Interface.AWGObfuscation
}

// SetEnabled changes the enabled/autostart state of a tunnel.
func (s *ServiceImpl) SetEnabled(ctx context.Context, tunnelID string, enabled bool) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	if err := s.store.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.Enabled = enabled
		return nil
	}); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return tunnel.ErrNotFound
		}
		return fmt.Errorf("save tunnel: %w", err)
	}

	s.logInfo("set-enabled", tunnelID, fmt.Sprintf("Enabled set to %v", enabled))
	s.invalidateState(tunnelID)
	return nil
}

// SetDefaultRoute changes the default route setting.
// If tunnel is running, immediately applies route changes.
func (s *ServiceImpl) SetDefaultRoute(ctx context.Context, tunnelID string, enabled bool) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}
	// Зеркальная запись raw-выхода: её маршрутами распоряжается прокси-рантайм.
	// Отказ здесь — не косметика: NewNames считает NDMS-имя из идентификатора,
	// а у "wdttraw-*" цифр нет, и фолбэк даёт OpkgTun0 — ЧУЖОЙ интерфейс.
	// Дальше по коду это ушло бы в legacyOperator.SetDefaultRoute и увело
	// маршрут по умолчанию на посторонний объект роутера.
	if stored.Backend == "wdtt-raw" {
		return fmt.Errorf("маршрутом raw-выхода распоряжается инстанс WDTT — меняйте в его настройках")
	}

	oldValue := stored.DefaultRoute

	if err := s.store.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.DefaultRoute = enabled
		t.DefaultRouteSet = true
		return nil
	}); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return tunnel.ErrNotFound
		}
		return fmt.Errorf("save tunnel: %w", err)
	}

	// If tunnel is running and value changed, apply default route changes.
	// NativeWG: NDMS manages routes natively, no action needed here.
	// Kernel: endpoint route is always present (set up in Start), only default route toggles.
	if !s.isNativeWG(stored) {
		stateInfo := s.state.GetState(ctx, tunnelID)
		if stateInfo.State == tunnel.StateRunning && oldValue != enabled {
			if enabled {
				if err := s.legacyOperator.SetDefaultRoute(ctx, tunnelID); err != nil {
					s.logWarn("set-default-route", tunnelID, "Failed to set default route: "+err.Error())
				}
			} else {
				if err := s.legacyOperator.RemoveDefaultRoute(ctx, tunnelID); err != nil {
					s.logWarn("set-default-route", tunnelID, "Failed to remove default route: "+err.Error())
				}
			}
		}
	}

	s.logInfo("set-default-route", tunnelID, fmt.Sprintf("DefaultRoute set to %v", enabled))
	return nil
}

// Import parses a WireGuard .conf file and creates a tunnel.
func (s *ServiceImpl) Import(ctx context.Context, confContent, name, backend string) (*TunnelWithStatus, error) {
	// Parse config
	parsed, err := config.Parse(confContent)
	if err != nil {
		return nil, fmt.Errorf("parse conf: %w", err)
	}

	// Тот же гейт, что и на create/update: чужой .conf с битым
	// HeaderProtectionKey или коротким S1-S4 иначе доедет до ядра и туннель
	// встанет с выключенной header protection — молча.
	if err := config.ValidateAWG3(&parsed.Interface.AWGObfuscation); err != nil {
		return nil, err
	}

	// Set name
	if name != "" {
		parsed.Name = name
	}
	if parsed.Name == "" {
		parsed.Name = "Imported Tunnel"
	}

	// Determine backend
	if backend == "" {
		backend = "kernel" // default for backwards compat
	}
	parsed.Backend = backend

	if backend == "nativewg" {
		return s.importNativeWG(ctx, parsed)
	}

	// Kernel path (existing logic)
	tunnelID, err := s.store.NextAvailableID(ctx, backend, s.opkgOccupancy)
	if err != nil {
		return nil, fmt.Errorf("generate ID: %w", err)
	}
	parsed.ID = tunnelID
	parsed.Type = "awg"
	parsed.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	parsed.Enabled = false

	if err := s.store.Create(parsed); err != nil {
		return nil, fmt.Errorf("save tunnel: %w", err)
	}
	if err := config.WriteFile(parsed); err != nil {
		_ = s.store.Delete(tunnelID)
		return nil, fmt.Errorf("write config: %w", err)
	}

	s.logInfo("import", tunnelID, "Tunnel imported: "+parsed.Name)
	// Legacy tunnel:created publish removed (Task 14 sweep); import
	// handler emits resource:invalidated via publishTunnelList.
	return s.Get(ctx, tunnelID)
}

// importNativeWG creates a tunnel using the NativeWG backend.
func (s *ServiceImpl) importNativeWG(ctx context.Context, parsed *storage.AWGTunnel) (*TunnelWithStatus, error) {
	if s.nwgOperator == nil {
		return nil, fmt.Errorf("NativeWG backend not available")
	}

	// Generate tunnel ID — NativeWG-диапазон (awg20+), не делит
	// kernel-лимит OpkgTun10..16.
	tunnelID, err := s.store.NextAvailableID(ctx, "nativewg", nil)
	if err != nil {
		return nil, fmt.Errorf("generate ID: %w", err)
	}
	parsed.ID = tunnelID
	parsed.Type = "awg"
	parsed.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	parsed.Enabled = false
	parsed.Backend = "nativewg"

	// Guard: the ifcreated hook fires from NDMS AS SOON AS the interface
	// is created. Without the gate, the hook handler rebroadcasts a
	// snapshot that sees the new NDMS interface but does NOT see this
	// tunnel in our managed store yet (Save hasn't run), so the interface
	// is misclassified as a "system tunnel" — a ghost duplicate vanishing
	// only on next refresh. Gate spans both Create and Save; the caller
	// (import handler) publishes the final snapshot after us.
	if s.selfCreateGate != nil {
		s.selfCreateGate.EnterSelfCreate()
		defer s.selfCreateGate.ExitSelfCreate()
	}

	// Create NDMS WireGuard interface via NativeWG operator
	index, err := s.nwgOperator.Create(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("create NativeWG interface: %w", err)
	}
	parsed.NWGIndex = index

	// Save to storage
	if err := s.store.Create(parsed); err != nil {
		_ = s.nwgOperator.Delete(ctx, parsed)
		return nil, fmt.Errorf("save tunnel: %w", err)
	}

	s.logInfo("import", tunnelID, "NativeWG tunnel imported: "+parsed.Name)
	// Legacy tunnel:created publish removed (Task 14 sweep); import
	// handler emits resource:invalidated via publishTunnelList.
	return s.Get(ctx, tunnelID)
}

// ReplaceConfig replaces a tunnel's Interface and Peer from a parsed .conf,
// preserving identity, routing, monitoring, and all other metadata.
func (s *ServiceImpl) ReplaceConfig(ctx context.Context, tunnelID, confContent, newName string) error {
	s.lockTunnel(tunnelID)
	defer s.unlockTunnel(tunnelID)

	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.ErrNotFound
	}

	parsed, err := config.Parse(confContent)
	if err != nil {
		return fmt.Errorf("parse conf: %w", err)
	}

	wasNativeRunning := false
	wasKernelRunning := false
	switch {
	case s.nwgOperator != nil && s.isNativeWG(stored):
		stateInfo := s.nwgOperator.GetState(ctx, stored)
		wasNativeRunning = stateInfo.State == tunnel.StateRunning ||
			stateInfo.State == tunnel.StateStarting ||
			// A stalled ASC tunnel is Broken now (#702), and replacing the
			// .conf is exactly how it gets fixed — restart it too.
			stateInfo.State == tunnel.StateBroken
	case s.legacyOperator != nil:
		stateInfo := s.state.GetState(ctx, tunnelID)
		wasKernelRunning = stateInfo.State == tunnel.StateRunning || stateInfo.State == tunnel.StateStarting
	}

	// Capture the old peer's public key BEFORE overwriting Interface/Peer.
	// SyncPeer needs it to remove the orphan peer entry from NDMS when the
	// new conf carries a different PublicKey — without this the interface
	// ends up with both old and new peers (NDMS indexes by key).
	oldPublicKey := stored.Peer.PublicKey

	// Capture old DNS for the non-running sync branch below — handler skips
	// Stop+Start when the tunnel isn't running, leaving NDMS DNS entries
	// orphaned (pointing to the previous conf's servers).
	oldDNS := stored.Interface.DNS

	// Replace Interface + Peer entirely
	stored.Interface = parsed.Interface
	stored.Peer = parsed.Peer

	// Optionally update name
	if newName != "" {
		stored.Name = newName
	}

	// Clear runtime state (will be re-populated on next start)
	stored.ResolvedEndpointIP = ""
	stored.ActiveWAN = ""
	stored.StartedAt = ""

	// Save to storage. Мутатор присваивает уже вычисленные выше поля свежей
	// записи под локом — сброс runtime-полей здесь осознанная часть замены
	// конфига, а не затирание чужой параллельной правки.
	if err := s.store.Update(tunnelID, func(t *storage.AWGTunnel) error {
		t.Interface = stored.Interface
		t.Peer = stored.Peer
		// Имя — только если его действительно меняли: иначе сюда уехало бы
		// имя из снимка, снятого до GetState (для nativewg это RCI-обмен), и
		// параллельное переименование волной wdttlink молча откатилось бы.
		if newName != "" {
			t.Name = newName
		}
		t.ResolvedEndpointIP = stored.ResolvedEndpointIP
		t.ActiveWAN = stored.ActiveWAN
		t.StartedAt = stored.StartedAt
		return nil
	}); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return tunnel.ErrNotFound
		}
		return fmt.Errorf("save tunnel: %w", err)
	}

	// Перезаписываем .conf только для kernel-пути: его читает `awg setconf`.
	// У NativeWG конфигурация уезжает в NDMS байтами через RCI, а экспорт
	// пользователю регенерируется из записи — файл на диске не читает никто.
	if !s.isNativeWG(stored) {
		if err := config.WriteFile(stored); err != nil {
			s.logWarn("replace-config", tunnelID, "Failed to write config file: "+err.Error())
		}
	}

	// NativeWG: sync peer + address/MTU to NDMS.
	// If the tunnel was running, perform a soft restart so runtime/kmod state
	// is rebuilt from the new peer config.
	if s.nwgOperator != nil && s.isNativeWG(stored) {
		if wasNativeRunning {
			if err := s.nwgOperator.Stop(ctx, stored); err != nil {
				s.logWarn("replace-config", tunnelID, "Stop before peer sync failed: "+err.Error())
			}
		} else if oldDNS != stored.Interface.DNS {
			// Tunnel was not running — handler skipped Stop (which would
			// clear OLD DNS) and will skip Start (which would set NEW DNS).
			// Sync DNS here so NDMS doesn't keep orphan entries from the
			// previous conf.
			oldList := tunnel.ParseDNSList(oldDNS)
			newList := tunnel.ParseDNSList(stored.Interface.DNS)
			if err := s.nwgOperator.SyncDNS(ctx, stored, oldList, newList); err != nil {
				s.logWarn("replace-config", tunnelID, "SyncDNS failed: "+err.Error())
			}
		}
		if err := s.nwgOperator.SyncPrivateKey(ctx, stored); err != nil {
			s.logWarn("replace-config", tunnelID, "SyncPrivateKey failed: "+err.Error())
		}
		if err := s.nwgOperator.SyncPeer(ctx, stored, oldPublicKey); err != nil {
			s.logWarn("replace-config", tunnelID, "SyncPeer failed: "+err.Error())
		}
		if err := s.nwgOperator.SyncAddressMTU(ctx, stored); err != nil {
			s.logWarn("replace-config", tunnelID, "SyncAddressMTU failed: "+err.Error())
		}
		// Update description if name changed
		if newName != "" {
			if err := s.nwgOperator.UpdateDescription(ctx, stored, newName); err != nil {
				s.logWarn("replace-config", tunnelID, "UpdateDescription failed: "+err.Error())
			}
		}
		if wasNativeRunning {
			if err := s.nwgOperator.Start(ctx, stored); err != nil {
				s.logWarn("replace-config", tunnelID, "Start after peer sync failed: "+err.Error())
			}
		}
	}

	// Kernel-backend tunnels: hot-apply the new conf to a running interface
	// via `awg setconf`. setconf carries WGDEVICE_REPLACE_PEERS, so the
	// kernel atomically swaps the entire peer set — no orphan-peer cleanup
	// needed (unlike NDMS). When the tunnel is stopped, skip — the new
	// conf will be applied on next Start.
	if !s.isNativeWG(stored) && s.legacyOperator != nil && wasKernelRunning {
		confPath := tunnel.NewNames(tunnelID).ConfPath
		if err := s.legacyOperator.ApplyConfig(ctx, tunnelID, confPath); err != nil {
			s.logWarn("replace-config", tunnelID, "ApplyConfig failed: "+err.Error())
		}
	}

	s.logInfo("replace-config", tunnelID, "Configuration replaced: "+stored.Name)
	// Legacy tunnel:updated publish removed (Task 14 sweep); the
	// ReplaceConfig handler emits resource:invalidated via publishTunnelList.

	s.invalidateState(tunnelID)
	return nil
}

// === Validation ===

// CheckAddressConflicts returns warnings if the tunnel's address
// conflicts with any other stored tunnel.
func (s *ServiceImpl) CheckAddressConflicts(_ context.Context, tunnelID string) []string {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return nil
	}
	return checkStoredAddressConflicts(s.store, stored.Interface.Address, tunnelID)
}

// GetState returns the current state of a tunnel.
func (s *ServiceImpl) GetState(ctx context.Context, tunnelID string) tunnel.StateInfo {
	stored, err := s.store.Get(tunnelID)
	if err != nil {
		return tunnel.StateInfo{State: tunnel.StateUnknown}
	}
	return s.stateForStored(ctx, stored)
}

// stateForStored is the single source of truth for a tunnel's canonical state.
// It reads the raw state (nativewg RCI or kernel matrix), then applies the
// Enabled-correction: a tunnel we disabled (Enabled=false) reads as Disabled
// regardless of backend or a lingering interface — this also normalizes the
// nativewg path (classifyNWGState reports Stopped for conf=disabled) to the
// same "disabled" the kernel matrix reports. An out-of-band bring-up
// (Intent=UP while Enabled=false) still surfaces its real state, so the user
// sees the divergence. Get, List and GetState all route through here.
func (s *ServiceImpl) stateForStored(ctx context.Context, stored *storage.AWGTunnel) tunnel.StateInfo {
	info := s.rawState(ctx, stored)

	if !stored.Enabled {
		switch info.State {
		case tunnel.StateNeedsStop, tunnel.StateStopped, tunnel.StateDisabled:
			info.State = tunnel.StateDisabled
		}
	}

	return info
}

// === Helper Methods ===

// resolveWAN resolves the tunnel's ISPInterface to a kernel interface name.
// Auto mode (empty): uses WAN model priority or NDMS default gateway.
// Tunnel chaining (tunnel:xxx): resolves to parent tunnel's WAN.
// Explicit: returns as-is (after migration, stores kernel name).
func (s *ServiceImpl) resolveWAN(ctx context.Context, ispInterface string) (string, error) {
	if ispInterface == "" {
		// Auto mode: prefer WAN model (priority-based, returns kernel name)
		if iface, ok := s.wan.PreferredUp(); ok {
			return iface, nil
		}
		// Fallback: wan.Model not yet populated (early boot)
		// GetDefaultGatewayInterface returns NDMS ID → translate to kernel name
		ndmsID, err := s.legacyOperator.GetDefaultGatewayInterface(ctx)
		if err != nil {
			return "", fmt.Errorf("no default gateway available: %w", err)
		}
		// Try model reverse lookup first
		if kernelName := s.wan.NameForID(ndmsID); kernelName != "" {
			return kernelName, nil
		}
		// Model not populated — direct NDMS lookup
		return s.legacyOperator.GetSystemName(ctx, ndmsID), nil
	}

	if tunnel.IsTunnelRoute(ispInterface) {
		// Tunnel chaining: resolve to parent's persisted WAN
		parentID := tunnel.TunnelRouteID(ispInterface)
		parentStored, err := s.store.Get(parentID)
		if err != nil {
			return "", fmt.Errorf("parent tunnel %s not found", parentID)
		}
		if parentStored.ActiveWAN != "" {
			return parentStored.ActiveWAN, nil
		}
		// Fallback: ActiveWAN empty (first start or upgrade from old version)
		parentState := s.state.GetState(ctx, parentID)
		if parentState.State != tunnel.StateRunning {
			return "", fmt.Errorf("parent tunnel %s not running (state: %s)", parentID, parentState.State)
		}
		if tunnel.IsTunnelRoute(parentStored.ISPInterface) {
			return "", fmt.Errorf("parent tunnel %s: nested chain, ActiveWAN not tracked", parentID)
		}
		s.logInfo("resolve-wan", parentID, "ActiveWAN empty, resolving from stored config")
		return s.resolveWAN(ctx, parentStored.ISPInterface)
	}

	// Explicit WAN — after migration this is already a kernel name
	return ispInterface, nil
}

// resolveKernelDevice extracts the kernel device name from a resolved WAN.
// resolveWAN already returns kernel names, so this just handles tunnel chaining.
func (s *ServiceImpl) resolveKernelDevice(resolvedWAN string) string {
	if resolvedWAN == "" {
		return ""
	}
	if tunnel.IsTunnelRoute(resolvedWAN) {
		return tunnel.NewNames(tunnel.TunnelRouteID(resolvedWAN)).IfaceName
	}
	return resolvedWAN // already a kernel name
}

// logInfo logs an info message via the UI-visible scoped logger.
func (s *ServiceImpl) logInfo(action, target, message string) {
	s.appLog.Info(action, target, message)
}

// logWarn logs a warning message via the UI-visible scoped logger.
func (s *ServiceImpl) logWarn(action, target, message string) {
	s.appLog.Warn(action, target, message)
}

// MigrateISPInterfaceNone converts legacy "none" ISPInterface values to "" (auto).
func (s *ServiceImpl) MigrateISPInterfaceNone() {
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if t.ISPInterface != "none" {
			continue
		}
		// Снимок List выбирает кандидатов; решение о записи мутатор
		// принимает заново по свежей записи под локом.
		migrated := false
		err := s.store.Update(t.ID, func(fresh *storage.AWGTunnel) error {
			if fresh.ISPInterface != "none" {
				return storage.ErrNoChange
			}
			fresh.ISPInterface = ""
			migrated = true
			return nil
		})
		// Update отдаёт nil и на ErrNoChange, поэтому «мигрировали» решает
		// флаг из мутатора, а не отсутствие ошибки: иначе строка печаталась бы
		// и тогда, когда свежая запись кандидата не подтвердила.
		if err == nil && migrated {
			s.logInfo("migrate", t.ID, "Migrated ISPInterface from 'none' to auto")
		}
	}
}

// MigrateEmptyBackend sets Backend="kernel" on all tunnels with empty Backend field.
// Legacy tunnels (created before per-tunnel backend) are kernel-mode by definition.
func (s *ServiceImpl) MigrateEmptyBackend() {
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if t.Backend != "" {
			continue
		}
		_ = s.store.Update(t.ID, func(fresh *storage.AWGTunnel) error {
			if fresh.Backend != "" {
				return storage.ErrNoChange
			}
			fresh.Backend = "kernel"
			return nil
		})
	}
}

// MigrateISPInterfaceToKernel converts legacy NDMS ID values (e.g., "PPPoE0", "ISP")
// in ISPInterface and ActiveWAN to kernel names (e.g., "ppp0", "eth3").
// Called once at startup after WAN model is populated.
func (s *ServiceImpl) MigrateISPInterfaceToKernel() {
	if !s.wan.IsPopulated() {
		return
	}
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		// NativeWG tunnels store NDMS names — skip kernel migration
		if t.Backend == "nativewg" {
			continue
		}
		var ispFrom, ispTo, wanFrom, wanTo string
		err := s.store.Update(t.ID, func(fresh *storage.AWGTunnel) error {
			changed := false
			// Migrate ISPInterface — условие и значение пересчитаны по
			// свежей записи, а не по снимку List.
			if fresh.ISPInterface != "" && !tunnel.IsTunnelRoute(fresh.ISPInterface) {
				if kernelName := s.wan.NameForID(fresh.ISPInterface); kernelName != "" {
					ispFrom, ispTo = fresh.ISPInterface, kernelName
					fresh.ISPInterface = kernelName
					changed = true
				}
			}
			// Migrate ActiveWAN
			if fresh.ActiveWAN != "" && !tunnel.IsTunnelRoute(fresh.ActiveWAN) {
				if kernelName := s.wan.NameForID(fresh.ActiveWAN); kernelName != "" {
					wanFrom, wanTo = fresh.ActiveWAN, kernelName
					fresh.ActiveWAN = kernelName
					changed = true
				}
			}
			if !changed {
				return storage.ErrNoChange
			}
			return nil
		})
		if err != nil {
			continue
		}
		if ispTo != "" {
			s.logInfo("migrate", t.ID, fmt.Sprintf("ISPInterface: %s → %s", ispFrom, ispTo))
		}
		if wanTo != "" {
			s.logInfo("migrate", t.ID, fmt.Sprintf("ActiveWAN: %s → %s", wanFrom, wanTo))
		}
	}
}

// HealStaleActiveWAN clears stored.ActiveWAN entries that are not real
// kernel interface names. NativeWG tunnels persist ResolveActiveWAN's
// return value into storage; on certain Keenetic firmwares the resolver
// used to short-circuit on a cached `interface-name` field that held a
// logical NDMS label (e.g. "ISP") instead of the kernel device. The
// resolver itself is now hardened, but historical garbage stays in
// storage until next successful resolve — which never happens for
// disabled tunnels. UI labels and tunnel chaining (resolves via
// parent.ActiveWAN) keep seeing the stale value.
//
// Called once at startup. The next ResolveActiveWAN call after Heal
// will populate the empty field with the correct kernel name.
func (s *ServiceImpl) HealStaleActiveWAN() {
	tunnels, err := s.store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		if t.ActiveWAN == "" || tunnel.IsTunnelRoute(t.ActiveWAN) {
			continue
		}
		if kernelIfaceExists(t.ActiveWAN) {
			continue
		}
		var staleWAN string
		err := s.store.Update(t.ID, func(fresh *storage.AWGTunnel) error {
			if fresh.ActiveWAN == "" || tunnel.IsTunnelRoute(fresh.ActiveWAN) || kernelIfaceExists(fresh.ActiveWAN) {
				return storage.ErrNoChange
			}
			staleWAN = fresh.ActiveWAN
			fresh.ActiveWAN = ""
			return nil
		})
		// staleWAN непуст только когда мутатор реально чистил поле: на
		// ErrNoChange Update тоже возвращает nil, и строка врала бы пустым %q.
		if err == nil && staleWAN != "" {
			s.logInfo("migrate", t.ID, fmt.Sprintf("Clearing stale ActiveWAN=%q (not a kernel interface)", staleWAN))
		}
	}
}

// kernelIfaceExists reports whether a Linux network interface with the
// given name is present in the running kernel. Kept inline (rather than
// shared with internal/ndms/query) because the dependency is one syscall;
// a separate helper package would be premature. Stored as a package-level
// variable so tests can override it without touching /sys/class/net.
var kernelIfaceExists = func(name string) bool {
	if name == "" {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name)
	return err == nil
}

// isNativeWG returns true if the tunnel uses the NativeWG backend.
func (s *ServiceImpl) isNativeWG(stored *storage.AWGTunnel) bool {
	return stored.Backend == "nativewg"
}

// backendLabel returns the backend label for a stored tunnel.
func (s *ServiceImpl) backendLabel(stored *storage.AWGTunnel) string {
	if s.isNativeWG(stored) {
		return "nativewg"
	}
	if stored.Backend != "" {
		return stored.Backend
	}
	return "kernel"
}

// Ensure ServiceImpl implements Service interface.
var _ Service = (*ServiceImpl)(nil)

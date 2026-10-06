package adaptiverouting

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type RouterSlotController interface {
	ParkRoutingSlot(ctx context.Context) error
	RestoreRoutingSlot(ctx context.Context) error
}

type Service struct {
	mu         sync.Mutex
	store      *Store
	catalog    *Catalog
	refChecker *ReferenceChecker
	dataDir    string

	datapath    *DatapathController
	procMgr     *ProcessManager
	installer   *SusaninInstaller
	systemExec  *SystemExecutor
	mihomoExec  *MihomoExecutor
	singboxExec *SingboxExecutor

	slotController  RouterSlotController
	lanInterfaces   []string
	routerIPs       []string
	customRouterIPs bool
}

func detectRouterIPs(lanInterfaces []string) []string {
	var ips []string
	seen := make(map[string]bool)
	if len(lanInterfaces) == 0 {
		lanInterfaces = []string{"br0"}
	}
	for _, ifaceName := range lanInterfaces {
		if iface, err := net.InterfaceByName(ifaceName); err == nil {
			if addrs, err := iface.Addrs(); err == nil {
				for _, addr := range addrs {
					if ipNet, ok := addr.(*net.IPNet); ok {
						if ip4 := ipNet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
							s := ip4.String()
							if !seen[s] {
								seen[s] = true
								ips = append(ips, s)
							}
						}
					}
				}
			}
		}
	}
	// If no IPs were found on the specified LAN interfaces, search all active interfaces
	if len(ips) == 0 {
		if ifaces, err := net.Interfaces(); err == nil {
			for _, iface := range ifaces {
				if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
					continue
				}
				if addrs, err := iface.Addrs(); err == nil {
					for _, addr := range addrs {
						if ipNet, ok := addr.(*net.IPNet); ok {
							if ip4 := ipNet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
								s := ip4.String()
								if !seen[s] {
									seen[s] = true
									ips = append(ips, s)
								}
							}
						}
					}
				}
			}
		}
	}
	return ips
}

func NewService(
	dataDir string,
	catalog *Catalog,
	refChecker *ReferenceChecker,
	store *Store,
	archOpt ...string,
) *Service {
	arch := "aarch64"
	if len(archOpt) > 0 && strings.TrimSpace(archOpt[0]) != "" {
		arch = strings.TrimSpace(archOpt[0])
	}

	lanIfaces := []string{"br0"}
	s := &Service{
		dataDir:       dataDir,
		catalog:       catalog,
		refChecker:    refChecker,
		store:         store,
		lanInterfaces: lanIfaces,
		routerIPs:     detectRouterIPs(lanIfaces),
	}

	s.datapath = NewDatapathController(nil)
	// Susanin is installed into the managed path by SusaninInstaller.  Keep the
	// path in ProcessManager from construction time so boot-time Apply can find
	// and stop orphaned agents before Start is called.  Passing an empty path
	// made Stop unable to scan /proc, allowing an old daemon to survive package
	// upgrades next to the newly started process.
	s.procMgr = NewProcessManager(filepath.Join(dataDir, "susanin"), ManagedSusaninBinaryPath)
	s.installer = NewInstaller(arch)
	s.systemExec = NewSystemExecutor()

	return s
}

func (s *Service) SetExecutors(
	system *SystemExecutor,
	mihomo *MihomoExecutor,
	singbox *SingboxExecutor,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if system != nil {
		s.systemExec = system
	}
	if mihomo != nil {
		s.mihomoExec = mihomo
	}
	if singbox != nil {
		s.singboxExec = singbox
	}
}

func (s *Service) SetDatapathController(dp *DatapathController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.datapath = dp
}

func (s *Service) SetProcessManager(pm *ProcessManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.procMgr = pm
}

func (s *Service) SetRouterSlotController(sc RouterSlotController) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slotController = sc
}

func (s *Service) SetLanInterfaces(ifaces []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lanInterfaces = ifaces
	if !s.customRouterIPs {
		s.routerIPs = detectRouterIPs(ifaces)
	}
}

func (s *Service) SetRouterIPs(ips []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routerIPs = ips
	s.customRouterIPs = true
}

func (s *Service) currentRouterIPsLocked() []string {
	if s.customRouterIPs && len(s.routerIPs) > 0 {
		return s.routerIPs
	}
	ips := detectRouterIPs(s.lanInterfaces)
	if len(ips) > 0 {
		s.routerIPs = ips
	}
	return s.routerIPs
}

func (s *Service) SetPolicyMarkResolver(r PolicyMarkResolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.datapath != nil {
		s.datapath.SetPolicyMarkResolver(r)
	}
}

func (s *Service) GetStore() *Store {
	return s.store
}

func (s *Service) GetCatalog() *Catalog {
	return s.catalog
}

func (s *Service) GetReferenceChecker() *ReferenceChecker {
	return s.refChecker
}

func (s *Service) GetStatus(ctx context.Context) (OperationalState, Settings, error) {
	settings := s.store.GetSettings()
	state := s.store.GetState()
	applied := s.store.GetApplied()

	if applied != nil && applied.Settings.Enabled && s.procMgr != nil {
		if running, _ := s.procMgr.IsRunning(); running {
			if state.Status != "running" {
				state.Status = "running"
				state.LastError = ""
			}
		} else {
			if state.Status == "running" {
				state.Status = "stopped"
				state.LastError = "susanin-agent process terminated"
			}
		}
	}

	// Update stats from datapath if active
	if s.datapath != nil && applied != nil && applied.Settings.Enabled {
		if stats, err := s.datapath.GetStats(ctx); err == nil {
			state.LearnedTCPCount = stats.OkTcpCount
			state.LearnedUDPCount = stats.OkUdpCount
			state.TestingTCPCount = stats.TestTcpCount
			state.TestingUDPCount = stats.TestUdpCount
			state.AlwaysCount = stats.OkNetCount
			state.NeverCount = stats.NeverCount
			state.FallbackActive = stats.FailOpen
		}
	}
	if s.installer != nil {
		bin := s.installer.ResolveBinary()
		state.Binary = bin
		state.Installed = bin != ""
		state.Version = SusaninVersion
	}

	return state, settings, nil
}

func (s *Service) Restart(ctx context.Context) (OperationalState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.procMgr != nil {
		if err := s.procMgr.Stop(ctx); err != nil {
			return s.store.GetState(), fmt.Errorf("stop susanin: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		applied := s.store.GetApplied()
		if applied != nil && applied.Settings.Enabled {
			if err := s.procMgr.Start(ctx, ""); err != nil {
				return s.store.GetState(), fmt.Errorf("start susanin: %w", err)
			}
		}
	}
	return s.store.GetState(), nil
}

func (s *Service) Install(ctx context.Context) error {
	if s.installer == nil {
		return errors.New("susanin installer not configured")
	}
	_, err := s.installer.EnsureInstalled(ctx)
	return err
}

func (s *Service) Uninstall(ctx context.Context) error {
	_, _ = s.Stop(ctx)
	if s.installer == nil {
		return errors.New("susanin installer not configured")
	}
	return s.installer.Uninstall()
}

// Reconcile verifies the committed Susanin runtime and applies the configured
// failure policy. Draft settings are intentionally ignored: only the last
// fully committed AppliedConfig is allowed to affect live routing.
func (s *Service) Reconcile(ctx context.Context) (OperationalState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	applied := s.store.GetApplied()
	if applied == nil || !applied.Settings.Enabled {
		return s.store.GetState(), nil
	}

	now := time.Now().UTC()
	var healthErr error
	resolved, err := s.catalog.Resolve(ctx, applied.Egress.Ref)
	if err != nil {
		healthErr = fmt.Errorf("resolve applied egress: %w", err)
	} else if !resolved.Available {
		reason := resolved.UnavailableReason
		if reason == "" {
			reason = "egress is unavailable"
		}
		healthErr = errors.New(reason)
	}

	if healthErr == nil && s.procMgr != nil {
		if running, _ := s.procMgr.IsRunning(); !running {
			if err := s.procMgr.Start(ctx, ""); err != nil {
				healthErr = fmt.Errorf("restart susanin-agent: %w", err)
			} else if running, _ := s.procMgr.IsRunning(); !running {
				healthErr = errors.New("susanin-agent stopped after restart")
			}
		}
	}

	if healthErr != nil {
		if s.datapath != nil {
			if applied.Settings.FailurePolicy == "block" {
				err = s.datapath.SetFailClosed(ctx, true, applied.Settings)
			} else {
				err = s.datapath.SetFailOpen(ctx, true, applied.Settings)
			}
			if err != nil {
				healthErr = errors.Join(healthErr, fmt.Errorf("apply failure policy: %w", err))
			}
		}
		state, saveErr := s.store.SaveState(func(st *OperationalState) error {
			st.Status = "degraded"
			st.FallbackActive = applied.Settings.FailurePolicy != "block"
			st.LastError = healthErr.Error()
			st.LastReconcile = now
			return nil
		})
		if saveErr != nil {
			return state, errors.Join(healthErr, fmt.Errorf("persist degraded state: %w", saveErr))
		}
		return state, healthErr
	}

	currentState := s.store.GetState()
	if currentState.Status != "running" || currentState.FallbackActive {
		exec := s.resolveExecutorLocked(applied.Egress.Ref.Engine)
		if exec == nil {
			return currentState, fmt.Errorf("executor for applied engine %s is not configured", applied.Egress.Ref.Engine)
		}
		egressDev := exec.InterfaceName()
		if egressDev == "" {
			egressDev = applied.Egress.Interface
		}
		if egressDev == "" {
			egressDev = TunInterfaceName
		}
		if s.datapath != nil {
			// EnsureRules atomically replaces a temporary blackhole route and also
			// restores rules removed by fail-open mode.
			if err := s.datapath.EnsureRules(ctx, applied.Settings, egressDev); err != nil {
				return currentState, fmt.Errorf("restore healthy datapath: %w", err)
			}
			_ = s.datapath.SetFailClosed(ctx, false, applied.Settings)
		}
	}

	state, err := s.store.SaveState(func(st *OperationalState) error {
		st.Status = "running"
		st.RoutingOwner = RoutingOwnerSusanin
		st.ActiveEgress = &resolved
		st.FallbackActive = false
		st.LastError = ""
		st.LastReconcile = now
		return nil
	})
	if err != nil {
		return state, fmt.Errorf("persist reconciled state: %w", err)
	}
	return state, nil
}

func (s *Service) Preview(ctx context.Context, candidate Settings) (*ResolvedEgress, error) {
	if candidate.PrimaryEgress.ResourceID == "" {
		return nil, fmt.Errorf("основной выход (primaryEgress) не выбран")
	}

	resolved, err := s.catalog.Resolve(ctx, candidate.PrimaryEgress)
	if err != nil {
		return nil, fmt.Errorf("не удалось разрешить выбранный выход: %w", err)
	}

	return &resolved, nil
}

func (s *Service) Apply(ctx context.Context, desired Settings) (OperationalState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. Validation phase
	if _, err := ValidateRoutingTableID(desired.RoutingTableID); err != nil {
		return s.store.GetState(), fmt.Errorf("валидация таблицы маршрутизации не удалась: %w", err)
	}
	resolved, err := s.catalog.Resolve(ctx, desired.PrimaryEgress)
	if err != nil {
		return s.store.GetState(), fmt.Errorf("валидация выхода не удалась: %w", err)
	}

	// 2. Persist the user draft. Boot restoration uses GetApplied, not this draft.
	savedSettings, err := s.store.UpdateSettings(func(st *Settings) error {
		*st = desired
		return nil
	})
	if err != nil {
		return s.store.GetState(), fmt.Errorf("сохранение настроек не удалось: %w", err)
	}

	if !savedSettings.Enabled {
		return s.stopLocked(ctx, savedSettings)
	}

	// 3. Executor selection & preparation
	exec := s.resolveExecutorLocked(resolved.Ref.Engine)
	if exec == nil {
		return s.store.GetState(), fmt.Errorf("исполнитель для движка %s не настроен", resolved.Ref.Engine)
	}

	if err := exec.Prepare(ctx, resolved); err != nil {
		return s.store.GetState(), fmt.Errorf("подготовка исполнителя: %w", err)
	}
	if err := exec.Commit(ctx); err != nil {
		_ = exec.Rollback(ctx)
		return s.store.GetState(), fmt.Errorf("применение исполнителя: %w", err)
	}
	executorCommitted := true

	egressDev := exec.InterfaceName()
	if egressDev == "" {
		egressDev = TunInterfaceName
	}

	// 4. Park old owner routing slot
	if s.slotController != nil {
		if err := s.slotController.ParkRoutingSlot(ctx); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, false,
				fmt.Errorf("parking current routing slot: %w", err))
		}
	}
	slotParked := s.slotController != nil

	// Generate config and resolve the binary before touching the datapath.
	var binPath string
	if s.procMgr != nil {
		var policyMark string
		if savedSettings.Source.Type == "policy" && s.datapath != nil && s.datapath.policyResolver != nil {
			if mark, markErr := s.datapath.policyResolver.Get(ctx, savedSettings.Source.PolicyID); markErr == nil {
				policyMark = mark
			}
		}
		if err := s.procMgr.WriteConfigFiles(savedSettings, egressDev, s.lanInterfaces, nil, policyMark); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("write susanin-agent config: %w", err))
		}
		if s.installer == nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				errors.New("susanin-agent installer is not configured"))
		}
		binPath, err = s.installer.EnsureInstalled(ctx)
		if err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("install susanin-agent: %w", err))
		}
		if binPath == "" {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				errors.New("susanin-agent installer returned an empty binary path"))
		}
	}

	// 5. Restart the upstream agent so it consumes the freshly written config.
	// It installs its own generic br0 hook, so our source-scoped datapath must be
	// applied afterwards and remain the final owner of PREROUTING.
	if s.procMgr != nil {
		if err := s.procMgr.Stop(ctx); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("stop previous susanin-agent: %w", err))
		}
		if err := s.procMgr.Start(ctx, binPath); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("start susanin-agent: %w", err))
		}
		if running, _ := s.procMgr.IsRunning(); !running {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				errors.New("susanin-agent did not become ready"))
		}
	}

	// 6. Apply the AWG Manager source scope after the agent has initialized.
	if s.datapath != nil {
		if err := s.datapath.EnsureSets(ctx); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("prepare ipset: %w", err))
		}
		if err := s.datapath.EnsureChain(ctx, savedSettings, s.lanInterfaces, s.currentRouterIPsLocked()); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("prepare iptables: %w", err))
		}
		if err := s.datapath.EnsureRules(ctx, savedSettings, egressDev); err != nil {
			return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
				fmt.Errorf("install ip rule / route: %w", err))
		}
	}

	// 7. Single commit boundary after all runtime stages succeeded.
	now := time.Now().UTC()
	generation := fmt.Sprintf("gen-%d", now.UnixNano())
	newState := s.store.GetState()
	newState.AppliedGeneration = generation
	newState.ActiveEgress = &resolved
	newState.LastReconcile = now
	newState.RoutingOwner = RoutingOwnerSusanin
	newState.Status = "running"
	newState.LastError = ""
	newState.RecoveryMarker = ""
	if err := s.store.CommitApplied(AppliedConfig{
		Generation: generation,
		Settings:   savedSettings,
		Egress:     resolved,
		Committed:  now,
	}, newState); err != nil {
		return s.failApplyLocked(ctx, savedSettings, exec, executorCommitted, slotParked,
			fmt.Errorf("commit applied Susanin state: %w", err))
	}
	return newState, nil
}

func (s *Service) failApplyLocked(ctx context.Context, settings Settings, exec Executor, executorCommitted, slotParked bool, cause error) (OperationalState, error) {
	previous := s.store.GetApplied()
	rollbackErrs := []error{cause}
	if s.procMgr != nil {
		if err := s.procMgr.Stop(ctx); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("stop process: %w", err))
		}
	}
	if s.datapath != nil {
		if err := s.datapath.Teardown(ctx, settings, s.lanInterfaces); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("teardown datapath: %w", err))
		}
	}
	if executorCommitted && exec != nil {
		if err := exec.Rollback(ctx); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("rollback executor: %w", err))
		}
	}
	if slotParked && s.slotController != nil {
		if err := s.slotController.RestoreRoutingSlot(ctx); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore routing slot: %w", err))
		}
	}
	if previous != nil && previous.Settings.Enabled {
		if restored, err := s.restoreAppliedRuntimeLocked(ctx, *previous); err == nil {
			return restored, errors.Join(cause, errors.New("previous Susanin configuration was restored"))
		} else {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore previous applied configuration: %w", err))
		}
	}
	fullErr := errors.Join(rollbackErrs...)
	state, stateErr := s.store.SaveState(func(st *OperationalState) error {
		st.Status = "recovery_required"
		st.RoutingOwner = RoutingOwnerNone
		st.ActiveEgress = nil
		st.LastReconcile = time.Now().UTC()
		st.LastError = fullErr.Error()
		st.RecoveryMarker = "apply_failed"
		return nil
	})
	if stateErr != nil {
		fullErr = errors.Join(fullErr, fmt.Errorf("persist recovery state: %w", stateErr))
	}
	return state, fullErr
}

// restoreAppliedRuntimeLocked re-activates the last committed runtime after a
// candidate apply crossed the runtime mutation boundary and then failed. It
// deliberately does not rewrite desired settings: the failed draft remains
// visible to the user, while the applied snapshot remains the source of truth
// for the running datapath and for boot recovery.
func (s *Service) restoreAppliedRuntimeLocked(ctx context.Context, applied AppliedConfig) (OperationalState, error) {
	exec := s.resolveExecutorLocked(applied.Egress.Ref.Engine)
	if exec == nil {
		return s.store.GetState(), fmt.Errorf("executor for previous engine %s is not configured", applied.Egress.Ref.Engine)
	}
	if err := exec.Prepare(ctx, applied.Egress); err != nil {
		return s.store.GetState(), fmt.Errorf("prepare previous executor: %w", err)
	}
	if err := exec.Commit(ctx); err != nil {
		_ = exec.Rollback(ctx)
		return s.store.GetState(), fmt.Errorf("commit previous executor: %w", err)
	}

	egressDev := exec.InterfaceName()
	if egressDev == "" {
		egressDev = TunInterfaceName
	}
	if s.slotController != nil {
		if err := s.slotController.ParkRoutingSlot(ctx); err != nil {
			_ = exec.Rollback(ctx)
			return s.store.GetState(), fmt.Errorf("park routing slot for previous configuration: %w", err)
		}
	}

	cleanup := func() {
		if s.procMgr != nil {
			_ = s.procMgr.Stop(ctx)
		}
		if s.datapath != nil {
			_ = s.datapath.Teardown(ctx, applied.Settings, s.lanInterfaces)
		}
		_ = exec.Rollback(ctx)
		if s.slotController != nil {
			_ = s.slotController.RestoreRoutingSlot(ctx)
		}
	}

	var binPath string
	var err error
	if s.procMgr != nil {
		var policyMark string
		if applied.Settings.Source.Type == "policy" && s.datapath != nil && s.datapath.policyResolver != nil {
			if mark, markErr := s.datapath.policyResolver.Get(ctx, applied.Settings.Source.PolicyID); markErr == nil {
				policyMark = mark
			}
		}
		if err = s.procMgr.WriteConfigFiles(applied.Settings, egressDev, s.lanInterfaces, nil, policyMark); err != nil {
			cleanup()
			return s.store.GetState(), fmt.Errorf("write previous susanin-agent config: %w", err)
		}
		if s.installer == nil {
			cleanup()
			return s.store.GetState(), errors.New("susanin-agent installer is not configured")
		}
		if binPath, err = s.installer.EnsureInstalled(ctx); err != nil || binPath == "" {
			cleanup()
			if err == nil {
				err = errors.New("empty binary path")
			}
			return s.store.GetState(), fmt.Errorf("restore susanin-agent binary: %w", err)
		}
	}
	if s.procMgr != nil {
		if err = s.procMgr.Stop(ctx); err != nil {
			cleanup()
			return s.store.GetState(), fmt.Errorf("stop current susanin-agent before restore: %w", err)
		}
		if err = s.procMgr.Start(ctx, binPath); err != nil {
			cleanup()
			return s.store.GetState(), fmt.Errorf("start previous susanin-agent: %w", err)
		}
		if running, _ := s.procMgr.IsRunning(); !running {
			cleanup()
			return s.store.GetState(), errors.New("previous susanin-agent did not become ready")
		}
	}
	if s.datapath != nil {
		if err = s.datapath.EnsureSets(ctx); err == nil {
			err = s.datapath.EnsureChain(ctx, applied.Settings, s.lanInterfaces, s.currentRouterIPsLocked())
		}
		if err == nil {
			err = s.datapath.EnsureRules(ctx, applied.Settings, egressDev)
		}
		if err != nil {
			cleanup()
			return s.store.GetState(), fmt.Errorf("restore previous datapath: %w", err)
		}
	}

	state := s.store.GetState()
	state.AppliedGeneration = applied.Generation
	state.ActiveEgress = &applied.Egress
	state.LastReconcile = time.Now().UTC()
	state.RoutingOwner = RoutingOwnerSusanin
	state.Status = "running"
	state.LastError = ""
	state.RecoveryMarker = ""
	if err := s.store.CommitApplied(applied, state); err != nil {
		cleanup()
		return s.store.GetState(), fmt.Errorf("persist restored applied configuration: %w", err)
	}
	return state, nil
}

func (s *Service) Start(ctx context.Context) (OperationalState, error) {
	settings := s.store.GetSettings()
	settings.Enabled = true
	return s.Apply(ctx, settings)
}

func (s *Service) Stop(ctx context.Context) (OperationalState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	settings := s.store.GetSettings()
	settings.Enabled = false
	_, err := s.store.UpdateSettings(func(st *Settings) error {
		st.Enabled = false
		return nil
	})
	if err != nil {
		return s.store.GetState(), err
	}

	return s.stopLocked(ctx, settings)
}

func (s *Service) stopLocked(ctx context.Context, settings Settings) (OperationalState, error) {
	var stopErrs []error
	// 1. Stop process
	if s.procMgr != nil {
		if err := s.procMgr.Stop(ctx); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("stop process: %w", err))
		}
	}

	// 2. Tear down datapath
	if s.datapath != nil {
		if err := s.datapath.Teardown(ctx, settings, s.lanInterfaces); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("teardown datapath: %w", err))
		}
	}

	// 3. Tear down executors
	if s.mihomoExec != nil {
		if err := s.mihomoExec.TearDown(ctx); err != nil {
			stopErrs = append(stopErrs, err)
		}
	}
	if s.singboxExec != nil {
		if err := s.singboxExec.TearDown(ctx); err != nil {
			stopErrs = append(stopErrs, err)
		}
	}
	if s.systemExec != nil {
		if err := s.systemExec.TearDown(ctx); err != nil {
			stopErrs = append(stopErrs, err)
		}
	}

	// 4. Restore router slot
	if s.slotController != nil {
		if err := s.slotController.RestoreRoutingSlot(ctx); err != nil {
			stopErrs = append(stopErrs, fmt.Errorf("restore routing slot: %w", err))
		}
	}

	newState := s.store.GetState()
	newState.LastReconcile = time.Now().UTC()
	if len(stopErrs) > 0 {
		stopErr := errors.Join(stopErrs...)
		newState.Status = "recovery_required"
		newState.LastError = stopErr.Error()
		newState.RecoveryMarker = "stop_failed"
		persisted, persistErr := s.store.SaveState(func(st *OperationalState) error { *st = newState; return nil })
		return persisted, errors.Join(stopErr, persistErr)
	}
	newState.AppliedGeneration = ""
	newState.RoutingOwner = RoutingOwnerNone
	newState.ActiveEgress = nil
	newState.Status = "stopped"
	newState.LastError = ""
	newState.RecoveryMarker = ""
	newState.FallbackActive = false
	if err := s.store.ClearApplied(newState); err != nil {
		return s.store.GetState(), fmt.Errorf("persist stopped Susanin state: %w", err)
	}
	return newState, nil
}

func (s *Service) ClearCache(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.datapath != nil {
		return s.datapath.FlushSets(ctx)
	}
	return nil
}

func (s *Service) Forget(ctx context.Context, ip string, proto string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.datapath != nil {
		return s.datapath.RemoveLearned(ctx, ip, proto)
	}
	return nil
}

func (s *Service) GetLearnedEntries(ctx context.Context) (map[string][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.datapath != nil {
		return s.datapath.GetLearnedEntries(ctx)
	}
	return make(map[string][]string), nil
}

func (s *Service) GetRecentLogs(ctx context.Context, limit int) ([]LogEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	logPath := filepath.Join(ManagedSusaninVarDir, "agent.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []LogEvent{}, nil
		}
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	var nonEmpty []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			nonEmpty = append(nonEmpty, l)
		}
	}

	start := 0
	if len(nonEmpty) > limit {
		start = len(nonEmpty) - limit
	}
	recent := nonEmpty[start:]

	events := make([]LogEvent, 0, len(recent))
	for i := len(recent) - 1; i >= 0; i-- {
		events = append(events, parseSusaninLogLine(recent[i]))
	}

	return events, nil
}

func parseSusaninLogLine(line string) LogEvent {
	parts := strings.SplitN(line, " ", 3)
	ts := ""
	lvl := "INFO"
	rest := line
	if len(parts) >= 2 {
		ts = parts[0]
		lvl = strings.TrimSuffix(parts[1], ":")
		if len(parts) >= 3 {
			rest = parts[2]
		}
	}

	action := lvl
	target := ""
	msg := rest

	if strings.Contains(rest, "AUTO-SUSANIN:") {
		subParts := strings.SplitN(rest, "AUTO-SUSANIN:", 2)
		sub := strings.TrimSpace(subParts[1])
		tokens := strings.Fields(sub)
		if len(tokens) > 0 {
			target = tokens[len(tokens)-1]
		}
		switch {
		case strings.Contains(sub, "CONFIRMED"):
			action = "CONFIRMED"
			msg = "Маршрут переведён в VPN (подтверждено)"
		case strings.Contains(sub, "TCP-STALL"):
			action = "STALL"
			msg = "Зависание прямого TCP потока → тест туннеля"
		case strings.Contains(sub, "LATE-STALL"):
			action = "LATE-STALL"
			msg = "Обрыв потока во время передачи → тест туннеля"
		case strings.Contains(sub, "TCP-CLOSE"):
			action = "RESET"
			msg = "Сброс сессии (TCP RST от цензора) → переключение"
		case strings.Contains(sub, "TCP-SYN"):
			action = "SYN-TIMEOUT"
			msg = "Таймаут подключения (блокировка SYN) → тест туннеля"
		case strings.Contains(sub, "QUIC"):
			action = "QUIC"
			msg = "Детекция сброса UDP/QUIC (HTTP/3) → тест туннеля"
		case strings.Contains(sub, "COOLDOWN"):
			action = "COOLDOWN"
			msg = "Охлаждение ресурса после серии проверок"
		default:
			action = "DISCOVER"
			msg = sub
		}
	} else if strings.Contains(rest, "re-provisioning") {
		action = "DATAPATH"
		msg = "Обновление правил маршрутизации ядра"
	} else if strings.Contains(rest, "vpn_always") {
		action = "PINNED"
		msg = "Синхронизация фиксированных подсетей"
	}

	ev := LogEvent{
		Timestamp: ts,
		Level:     lvl,
		Action:    action,
		Target:    target,
		Message:   msg,
		Raw:       line,
	}

	if target != "" {
		host := target
		if h, _, err := net.SplitHostPort(target); err == nil {
			host = h
		}
		if info := FindDomainKnowledge("", host); info != nil {
			ev.ResourceTitle = info.Title
			ev.ResourceOrg = info.Org
			ev.ResourceCountry = info.Country
			ev.ResourceCC = info.CountryCode
			ev.ResourceIcon = info.Icon
		}
	}

	return ev
}


func (s *Service) resolveExecutorLocked(engine EgressEngine) Executor {
	switch engine {
	case EngineMihomo:
		if s.mihomoExec != nil {
			return s.mihomoExec
		}
	case EngineSingbox:
		if s.singboxExec != nil {
			return s.singboxExec
		}
	case EngineSystem:
		if s.systemExec != nil {
			return s.systemExec
		}
	}
	return nil
}

func (s *Service) ReconcileDatapath(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	applied := s.store.GetApplied()
	if applied == nil || !applied.Settings.Enabled || s.datapath == nil {
		return nil
	}

	state := s.store.GetState()
	if state.Status == "recovery_required" || !state.Installed {
		return nil
	}

	exec := s.resolveExecutorLocked(applied.Egress.Ref.Engine)
	egressDev := ""
	if exec != nil {
		egressDev = exec.InterfaceName()
	}
	if egressDev == "" {
		egressDev = applied.Egress.Interface
	}
	if egressDev == "" {
		egressDev = TunInterfaceName
	}

	return s.datapath.ReconcileDatapath(ctx, applied.Settings, s.lanInterfaces, s.currentRouterIPsLocked(), egressDev)
}

func (s *Service) CleanupStaleOrphans(ctx context.Context) error {
	if s.procMgr == nil {
		return nil
	}
	return s.procMgr.CleanupOrphans(ctx)
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomo/installer"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/proxyengine"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func (a *app) setupMihomo() {
	binaryPath := installer.DefaultBinaryPath
	configDir := filepath.Join(a.dataDir, "mihomo")

	rulesDir := filepath.Join(configDir, "rules")
	_ = os.MkdirAll(rulesDir, 0755)
	susaninPath := filepath.Join(rulesDir, "susanin.yaml")
	if _, err := os.Stat(susaninPath); os.IsNotExist(err) {
		_ = os.WriteFile(susaninPath, []byte("payload: []\n"), 0644)
	}

	arch := detectArch()
	spec := installer.EmbeddedBinaries[arch]
	a.mihomoInstaller = installer.New(binaryPath, arch, spec, a.loggingService)

	a.mihomoOp = mihomo.NewOperator(binaryPath, configDir)
	a.mihomoOp.SetLogger(func(level, action, message string) {
		if a.loggingService != nil {
			a.loggingService.AppLog(logging.Level(level), logging.GroupMihomo, logging.SubSBProcess, action, "mihomo", message)
		}
	})
	a.mihomoHandler = api.NewMihomoHandler(a.mihomoOp)
	a.mihomoHandler.SetInstaller(a.mihomoInstaller)
	if a.eventBus != nil {
		a.mihomoHandler.SetEventBus(a.eventBus)
	}
	a.mihomoHandler.SetSettingsStore(a.settingsStore)
	nativePath := filepath.Join(configDir, "native.json")
	nativeStore, err := mihomonative.NewStore(nativePath)
	if err != nil {
		corruptPath := fmt.Sprintf("%s.corrupt.%d", nativePath, time.Now().Unix())
		if a.bootLog != nil {
			a.bootLog.Warn("startup", "mihomo-native", fmt.Sprintf("failed to load %s (%v); quarantining to %s and initializing clean store", nativePath, err, filepath.Base(corruptPath)))
		}
		if renameErr := os.Rename(nativePath, corruptPath); renameErr != nil {
			_ = os.Remove(nativePath)
		}
		nativeStore, err = mihomonative.NewStore(nativePath)
		if err != nil {
			if a.bootLog != nil {
				a.bootLog.Error("startup", "mihomo-native", fmt.Sprintf("fallback clean store failed: %v", err))
			}
			fallbackPath := filepath.Join(os.TempDir(), fmt.Sprintf("native-ephemeral-%d.json", time.Now().UnixNano()))
			nativeStore, _ = mihomonative.NewStore(fallbackPath)
		}
	}
	a.mihomoNativeStore = nativeStore
	a.mihomoHandler.SetNativeStore(nativeStore)

	// Native Mihomo resources are exported through the same KeenOS ProxyN
	// namespace as sing-box subscriptions. Keep the allocations in the common
	// managed set so toggle migration and orphan cleanup recognise their
	// ownership, and give mutation handlers the transactional bridge manager.
	bridgeGate := &gatedBridgeRegistrar{base: a.ndmsProxyMgr}
	exportsEnabled := func() bool {
		if a.settingsStore == nil {
			return false
		}
		return a.settingsStore.IsSingboxNDMSProxyEnabled() || (nativeStore != nil && len(nativeStore.ConfigBridgeListeners()) > 0)
	}
	a.mihomoBridge = mihomonative.NewBridgeManager(
		nativeStore,
		bridgeGate,
		exportsEnabled,
	)
	a.mihomoBridgeRuntime = newMihomoBridgeRuntime(nativeStore, a.mihomoBridge, bridgeGate)
	if a.singboxHandler != nil && a.ndmsQueries != nil && a.ndmsQueries.Interfaces != nil {
		a.singboxHandler.SetMihomoDiagnosticsResolver(&mihomoDiagnosticResolver{
			store:          nativeStore,
			proxies:        a.ndmsProxyMgr,
			kernel:         a.ndmsQueries.Interfaces,
			exportsEnabled: exportsEnabled,
			runtimeReady: func() bool {
				running, _ := a.mihomoOp.IsRunning()
				return running && bridgeGate.isReady()
			},
		})
	}
	managedProxySet := subProxySet{
		store: a.subStore, groups: a.subGroupStore, mihomo: nativeStore,
	}
	if a.ndmsProxyMgr != nil {
		a.ndmsProxyMgr.SetReservedIndices(managedProxySet.ManagedProxyIndices)
	}
	a.mihomoBridge.SetReservedIndices(managedProxySet.SingboxProxyIndices)
	a.mihomoBridge.SetRuntimeActive(func() bool {
		running, _ := a.mihomoOp.IsRunning()
		return running
	})
	a.mihomoHandler.SetNativeBridgeManager(a.mihomoBridge)
	a.mihomoHandler.SetNativeBridgeLifecycle(
		a.mihomoBridgeRuntime.prepare,
		a.mihomoBridgeRuntime.activate,
		a.mihomoBridgeRuntime.deactivate,
	)
	if a.singboxOp != nil {
		a.singboxOp.SetSubscriptionProxySet(managedProxySet)
		a.singboxOp.SetSubscriptionProxySync(a.syncNDMSProxyExports)
	}
	a.mihomoOp.SetOnUnexpectedExit(func(generation uint64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := a.mihomoBridgeRuntime.deactivateAfterUnexpectedExit(ctx, func() bool {
			return a.mihomoOp.CurrentGeneration() == generation
		}); err != nil {
			a.bootLog.Warn("mihomo-native-bridges", "unexpected-exit", err.Error())
		}
	})

	// Forward Mihomo runtime logs from external-controller (/logs) into its own
	// engine bucket. Keeping this separate from sing-box is essential: sing-box
	// can still run as a compatibility proxy component while Mihomo routes.
	logFwdCtx, logFwdCancel := context.WithCancel(context.Background())
	a.deferOnExit(logFwdCancel)
	go singbox.NewEngineLogForwarder(
		func() string { return "127.0.0.1:9090" },
		a.loggingService,
		logging.GroupMihomo,
		"mihomo",
	).Run(logFwdCtx)
}

// syncNDMSProxyExports reconciles the two composite-proxy families in an order
// that protects retained indices after a reboot. Existing Mihomo indices are
// registered first, then subscription.SyncProxies can safely scan/allocate,
// and only then are new Mihomo bridge indices allocated.
func (a *app) syncNDMSProxyExports(ctx context.Context) error {
	enabled := a.settingsStore.IsSingboxNDMSProxyEnabled()
	if enabled && a.ndmsProxyMgr == nil {
		return failMihomoProxySync(
			fmt.Errorf("NDMS proxy manager is not initialized"),
			a.mihomoBridgeDeactivate,
		)
	}
	// Capture current bridge state before mutation.
	var previous []mihomonative.BridgeRef
	if a.mihomoNativeStore != nil {
		previous = a.mihomoNativeStore.ListBridges()
	}
	if a.dynamicEngine != nil {
		if err := a.dynamicEngine.nativeMutationTransition(func() error {
			// Bridge preparation.
			if enabled && a.mihomoBridgeRuntime != nil {
				if err := a.mihomoBridgeRuntime.prepare(ctx, previous); err != nil {
					return err
				}
			}
			// Sync subscription proxies.
			if enabled && a.subSvc != nil {
				if err := a.subSvc.SyncProxies(ctx); err != nil {
					return err
				}
			}
			// Finally sync Mihomo runtime.
			if err := a.dynamicEngine.syncMihomoRuntimeWithinTransition(); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return failMihomoProxySync(err, a.mihomoBridgeDeactivate)
		}
	}
	return nil
}

func (a *app) mihomoBridgeDeactivate(ctx context.Context) error {
	if a == nil || a.mihomoBridgeRuntime == nil {
		return nil
	}
	return a.mihomoBridgeRuntime.deactivate(ctx)
}

func failMihomoProxySync(original error, deactivate func(context.Context) error) error {
	if deactivate == nil {
		return original
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := deactivate(ctx); err != nil {
		return errors.Join(original, fmt.Errorf("withdraw Mihomo exports after sync failure: %w", err))
	}
	return original
}

func (a *app) mihomoSidecarNeeded() bool {
	if a.settingsStore == nil || a.mihomoNativeStore == nil {
		return false
	}
	hasNativeResources := len(a.mihomoNativeStore.ConfigBridgeListeners()) > 0 ||
		len(a.mihomoNativeStore.ListGroups()) > 0 ||
		len(a.mihomoNativeStore.ListProxies()) > 0
	if !hasNativeResources {
		return false
	}
	// When sing-box is the primary engine, always run Mihomo as sidecar whenever
	// there are native Mihomo resources (proxies/subscriptions/groups), so they remain
	// alive, auto-switch via url-test, and are accessible.
	return true
}

// syncMihomoAfterSingboxReload mirrors changes from shared sing-box slots
// (subscriptions, device proxy and router fragments) into Mihomo. It is only
// used by the sing-box Process.OnReload bridge; DynamicEngine already prepares
// Mihomo itself before direct Mihomo Start/Reload calls.
func syncMihomoAfterSingboxReload(
	settingsStore *storage.SettingsStore,
	generate func() error,
	mihomoEngine proxyengine.Engine,
) error {
	settings, err := settingsStore.Load()
	if err != nil {
		return fmt.Errorf("load settings for Mihomo sync: %w", err)
	}
	if settings.SingboxRouter.RoutingEngine != "mihomo" {
		if running, _ := mihomoEngine.IsRunning(); running {
			if err := mihomoEngine.Stop(); err != nil {
				return fmt.Errorf("stop Mihomo when sing-box is primary: %w", err)
			}
		}
		return nil
	}
	if !settings.SingboxRouter.Enabled {
		if err := mihomoEngine.Stop(); err != nil {
			return fmt.Errorf("stop disabled Mihomo after sing-box reload: %w", err)
		}
		return nil
	}
	if err := generate(); err != nil {
		return fmt.Errorf("generate Mihomo config after sing-box reload: %w", err)
	}
	if settings.SingboxRouter.RoutingMode == "policy-tun" || settings.SingboxRouter.RoutingMode == "fakeip-tun" {
		if running, _ := mihomoEngine.IsRunning(); running {
			_ = mihomoEngine.Stop()
		}
		if err := mihomoEngine.Start(); err != nil {
			return fmt.Errorf("start Mihomo after sing-box reload: %w", err)
		}
		return nil
	}
	if err := mihomoEngine.Reload(); err != nil {
		return fmt.Errorf("reload Mihomo after sing-box reload: %w", err)
	}
	return nil
}

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
	"github.com/hoaxisr/awg-manager/internal/uplink"
	"github.com/hoaxisr/awg-manager/internal/uplink/wireguard"
)

func (a *app) routerLifecycle() routerExternalEngineLifecycle {
	if a.routerCoord != nil {
		return a.routerCoord
	}
	if a.routerSvc != nil {
		return a.routerSvc
	}
	return nil
}

// isFircEngineActive reports whether firc is configured as the active routing engine.
func (a *app) isFircEngineActive() bool {
	if a.settingsStore != nil {
		if s, err := a.settingsStore.Get(); err == nil && s != nil {
			if s.SingboxRouter.RoutingEngine == "firc" {
				return true
			}
		}
	}
	return false
}

// suppressAndStopSingbox serializes engine suppression with any in-flight
// orchestrator Reload() cold-start/restart, then ensures sing-box is stopped.
func (a *app) suppressAndStopSingbox(verifyTimeout time.Duration) error {
	stopFn := func() error {
		if a.singboxOp == nil {
			return nil
		}
		if running, _ := a.singboxOp.IsRunning(); running {
			if err := a.singboxOp.Stop(); err != nil {
				return fmt.Errorf("stop sing-box: %w", err)
			}
		}
		if verifyTimeout > 0 {
			deadline := time.Now().Add(verifyTimeout)
			for {
				running, _ := a.singboxOp.IsRunning()
				if !running {
					break
				}
				if time.Now().After(deadline) {
					return errors.New("sing-box is still running after Stop")
				}
				time.Sleep(25 * time.Millisecond)
			}
		}
		return nil
	}
	if a.sbOrch != nil {
		return a.sbOrch.SuppressAndStop(stopFn)
	}
	return stopFn()
}

// buildProductionEngineSwitchHooks constructs the unified production EngineSwitchHooks
// connecting firc and sing-box lifecycles with readiness verification and rollback.
func (a *app) buildProductionEngineSwitchHooks() firc.EngineSwitchHooks {
	getCurrent := func(_ context.Context) (string, error) {
		if a.settingsStore == nil {
			return "", errors.New("settings store is not initialized")
		}
		s, err := a.settingsStore.Get()
		if err != nil || s == nil {
			return "", fmt.Errorf("read current settings: %w", err)
		}
		eng := strings.TrimSpace(s.SingboxRouter.RoutingEngine)
		if eng == "" {
			eng = "sing-box"
		}
		return eng, nil
	}

	validateMutation := func(_ context.Context, _, _ string) error {
		return nil
	}

	captureState := func(ctx context.Context) (func(ctx context.Context) error, error) {
		prevStoppedByEngine := false
		prevManuallyStopped := false
		prevProxiesParked := false
		prevRunning := false
		if a.singboxOp != nil {
			prevStoppedByEngine = a.singboxOp.IsStoppedByEngine()
			prevManuallyStopped = a.singboxOp.IsManuallyStopped()
			prevProxiesParked = a.singboxOp.AreProxiesParked()
			prevRunning, _ = a.singboxOp.IsRunning()
		}
		prevEngine, _ := getCurrent(ctx)
		if overridePrev, ok := firc.SwitchPreviousEngineFromContext(ctx); ok {
			prevEngine = overridePrev
		}
		return func(rctx context.Context) error {
			var errs []error
			if a.singboxOp != nil {
				if err := a.singboxOp.SetStoppedByEngine(prevStoppedByEngine); err != nil {
					errs = append(errs, err)
				}
			}
			if prevEngine == "firc" {
				if a.singboxOp != nil {
					if err := a.singboxOp.ParkOwnedNDMSProxies(rctx); err != nil {
						errs = append(errs, err)
					}
				}
				if rl := a.routerLifecycle(); rl != nil {
					if err := rl.ParkForExternalEngine(rctx); err != nil {
						errs = append(errs, err)
					}
				}
				if a.singboxOp != nil {
					running, _ := a.singboxOp.IsRunning()
					wantRunning := prevRunning && !prevStoppedByEngine && !prevManuallyStopped
					if !wantRunning && running {
						if err := a.singboxOp.Stop(); err != nil {
							errs = append(errs, err)
						}
					} else if wantRunning && !running && (a.sbOrch == nil || a.sbOrch.HasActiveWork()) {
						if err := a.singboxOp.Start(); err != nil {
							errs = append(errs, err)
						}
					}
				}
				return errors.Join(errs...)
			}
			if prevStoppedByEngine || prevManuallyStopped {
				if a.singboxOp != nil {
					if running, _ := a.singboxOp.IsRunning(); running {
						if err := a.singboxOp.Stop(); err != nil {
							errs = append(errs, err)
						}
					}
					a.singboxOp.SetProxiesParked(prevProxiesParked)
					if !prevProxiesParked {
						if err := a.singboxOp.ResumeOwnedNDMSProxies(rctx); err != nil {
							errs = append(errs, err)
						}
					}
				}
				if rl := a.routerLifecycle(); rl != nil {
					if err := rl.ResumeFromExternalEngine(rctx); err != nil {
						errs = append(errs, err)
					}
				}
				return errors.Join(errs...)
			}
			if rl := a.routerLifecycle(); rl != nil {
				if err := rl.ResumeFromExternalEngine(rctx); err != nil {
					errs = append(errs, err)
				}
			}
			if a.singboxOp != nil {
				a.singboxOp.SetProxiesParked(prevProxiesParked)
				if !prevProxiesParked {
					if err := a.singboxOp.ResumeOwnedNDMSProxies(rctx); err != nil {
						errs = append(errs, err)
					}
				}
			}
			if a.singboxOp != nil && prevRunning {
				running, _ := a.singboxOp.IsRunning()
				if !running && (a.sbOrch == nil || a.sbOrch.HasActiveWork()) {
					if err := a.singboxOp.Start(); err != nil {
						errs = append(errs, err)
					}
				}
			}
			return errors.Join(errs...)
		}, nil
	}

	startTarget := func(ctx context.Context, targetEngine string) error {
		switch targetEngine {
		case "sing-box":
			if a.singboxOp == nil {
				return errors.New("sing-box operator is not initialized")
			}
			if !a.singboxOp.IsPresent() {
				return errors.New("sing-box binary is not installed; install sing-box first")
			}
			a.singboxOp.EnsureConfigDirInitialized()
			_ = a.syncSingboxAuxiliarySlots(ctx)
			if err := a.singboxOp.SetStoppedByEngine(false); err != nil {
				return fmt.Errorf("clear singboxStoppedByEngine: %w", err)
			}
			// Never erase an explicit user manual stop during engine switch.
			if a.singboxOp.IsManuallyStopped() {
				return nil
			}
			// Do not start an idle sing-box with no non-router work yet; ActivateTarget
			// calls ResumeFromExternalEngine after firc stops to promote router slots.
			if a.sbOrch == nil || a.sbOrch.HasActiveWork() {
				if err := a.singboxOp.StartAndWait(ctx); err != nil {
					return fmt.Errorf("start sing-box: %w", err)
				}
				if running, _ := a.singboxOp.IsRunning(); !running {
					return errors.New("sing-box did not reach running state after start")
				}
			}
			return nil

		default:
			return fmt.Errorf("unsupported non-firc target engine %q", targetEngine)
		}
	}

	parkSingboxSlotsFallback := func() {
		if a.sbOrch != nil {
			_ = a.sbOrch.SetEnabledSilent(router.RouterSlotForMode("tproxy"), false)
			_ = a.sbOrch.SetEnabledSilent(router.OtherRouterSlot("tproxy"), false)
			_ = a.sbOrch.SetEnabledSilent(orchestrator.SlotQoSRoutes, false)
		}
	}

	stopEngine := func(ctx context.Context, engine string) error {
		switch engine {
		case "sing-box":
			if a.sbOrch != nil {
				releaseHold := a.sbOrch.HoldReloads()
				defer func() {
					a.sbOrch.CancelPendingReload()
					releaseHold()
				}()
			}
			prevByEngine := false
			if a.singboxOp != nil {
				prevByEngine = a.singboxOp.IsStoppedByEngine()
				if err := a.singboxOp.SetStoppedByEngine(true); err != nil {
					return fmt.Errorf("persist singboxStoppedByEngine: %w", err)
				}
			}
			if rl := a.routerLifecycle(); rl != nil {
				if err := rl.ParkForExternalEngine(ctx); err != nil {
					if a.singboxOp != nil {
						_ = a.singboxOp.SetStoppedByEngine(prevByEngine)
					}
					return fmt.Errorf("park sing-box router: %w", err)
				}
			} else {
				parkSingboxSlotsFallback()
			}
			if a.singboxOp != nil {
				if err := a.singboxOp.ParkOwnedNDMSProxies(ctx); err != nil {
					if err2 := a.singboxOp.SetStoppedByEngine(prevByEngine); err2 != nil {
						return fmt.Errorf("park sing-box NDMS proxies (%w) + restore flag (%v)", err, err2)
					}
					return fmt.Errorf("park sing-box NDMS proxies: %w", err)
				}
			}
			if a.sbOrch != nil {
				a.sbOrch.CancelPendingReload()
			}
			if err := a.suppressAndStopSingbox(2 * time.Second); err != nil {
				return err
			}
			return nil

		default:
			return fmt.Errorf("unsupported non-firc engine %q for StopEngine", engine)
		}
	}

	parkStandby := func(ctx context.Context, engine string) error {
		switch engine {
		case "sing-box":
			if rl := a.routerLifecycle(); rl != nil {
				if err := rl.ParkForExternalEngine(ctx); err != nil {
					return fmt.Errorf("park sing-box router: %w", err)
				}
			} else {
				parkSingboxSlotsFallback()
			}
			if a.singboxOp != nil {
				if err := a.singboxOp.ParkOwnedNDMSProxies(ctx); err != nil {
					return fmt.Errorf("park sing-box NDMS proxies: %w", err)
				}
				if err := a.singboxOp.SetStoppedByEngine(false); err != nil {
					return fmt.Errorf("clear singboxStoppedByEngine: %w", err)
				}
				running, _ := a.singboxOp.IsRunning()
				hasNonRouterWork := a.sbOrch == nil || a.sbOrch.HasActiveWork()
				if a.singboxOp.IsManuallyStopped() || !hasNonRouterWork {
					if err := a.suppressAndStopSingbox(0); err != nil {
						return fmt.Errorf("stop idle sing-box after standby park: %w", err)
					}
				} else if a.sbOrch != nil {
					if err := a.sbOrch.ReloadNow(); err != nil {
						return fmt.Errorf("reload standby sing-box: %w", err)
					}
				} else if !running {
					if err := a.singboxOp.Start(); err != nil {
						return fmt.Errorf("start standby sing-box: %w", err)
					}
				} else {
					_ = a.singboxOp.Reload()
				}
			}
			return nil

		default:
			return fmt.Errorf("unsupported non-firc engine %q for ParkEngineForStandby", engine)
		}
	}

	activateTarget := func(ctx context.Context, targetEngine string) error {
		switch targetEngine {
		case "sing-box":
			if a.singboxOp != nil {
				a.singboxOp.EnsureConfigDirInitialized()
				if err := a.singboxOp.ReconcileRestoredProxies(ctx); err != nil {
					return fmt.Errorf("reconcile sing-box NDMS proxies: %w", err)
				}
			}
			if rl := a.routerLifecycle(); rl != nil {
				if err := rl.ResumeFromExternalEngine(ctx); err != nil {
					return fmt.Errorf("resume sing-box router: %w", err)
				}
			}
			if a.singboxOp != nil && a.sbOrch != nil && !a.singboxOp.IsManuallyStopped() {
				if running, _ := a.singboxOp.IsRunning(); running {
					if err := a.sbOrch.ReloadNow(); err != nil {
						return fmt.Errorf("reload sing-box after activation: %w", err)
					}
				}
			}
			return nil
		default:
			return nil
		}
	}

	commitSettings := func(_ context.Context, targetEngine string) error {
		if a.settingsStore == nil {
			return errors.New("settings store is not initialized")
		}
		return a.settingsStore.Update(func(s *storage.Settings) error {
			s.SingboxRouter.RoutingEngine = targetEngine
			return nil
		})
	}

	onSwitched := func(_ bool) {}

	return firc.EngineSwitchHooks{
		AcquireTransition: func(ctx context.Context) (firc.EngineTransitionGuard, error) {
			controller := a.uplinkController.Load()
			if controller == nil {
				// Before setupUplink, or after a store initialization error, read
				// durable policy rather than silently discarding its protection.
				store, err := uplink.OpenStore(filepath.Join(a.dataDir, "uplink-policies.json"))
				if err != nil {
					return nil, fmt.Errorf("read uplink policies before engine switch: %w", err)
				}
				// Only capabilities are used before initialization. Native
				// WireGuard is independent of the proxy routing engine.
				controller = uplink.New(store, nil, map[string]uplink.Adapter{"wireguard": &wireguard.Managed{}, "system-wireguard": &wireguard.Managed{}, "kernel-amneziawg": &wireguard.Kernel{}})
			}
			return controller.AcquireEngineTransition(ctx)
		},
		GetCurrentEngine:     getCurrent,
		ValidateMutation:     validateMutation,
		CaptureState:         captureState,
		StartTarget:          startTarget,
		StopEngine:           stopEngine,
		ParkEngineForStandby: parkStandby,
		ActivateTarget:       activateTarget,
		CommitSettings:       commitSettings,
		OnSwitched:           onSwitched,
	}
}

type fircDownloaderAdapter struct {
	svc *downloader.Service
}

func (f fircDownloaderAdapter) DownloadFile(ctx context.Context, req firc.DownloadFileRequest) (firc.DownloadFileResult, error) {
	if f.svc == nil {
		return firc.DownloadFileResult{}, errors.New("downloader service is not initialized")
	}
	res, err := f.svc.DownloadFile(ctx, downloader.FileRequest{
		Request: downloader.Request{
			Purpose:   "firc-binary",
			UserAgent: appver.UA(),
			URL:       req.URL,
			Timeout:   req.Timeout,
		},
		DestPath:     req.DestPath,
		TempPath:     req.DestPath + ".part",
		MaxFileBytes: req.MaxFileBytes,
		Mode:         req.Mode,
		Progress:     req.Progress,
		Atomic:       true,
	})
	if err != nil {
		return firc.DownloadFileResult{}, err
	}
	return firc.DownloadFileResult{
		Path: res.Path,
		Size: res.Size,
	}, nil
}

func (a *app) syncSingboxAuxiliarySlots(ctx context.Context) error {
	if a.singboxOp == nil {
		return nil
	}
	a.singboxOp.EnsureConfigDirInitialized()
	if a.awg3Store != nil && a.awg3Svc != nil && a.awg3Store.Len() > 0 {
		_ = a.awg3Svc.Sync()
	}
	if a.awgoutboundsSvc != nil {
		_ = a.awgoutboundsSvc.Reconcile(ctx)
	}
	if a.dnsRewriteSvc != nil {
		_ = a.dnsRewriteSvc.Resync()
	}
	if a.routerSvc != nil {
		a.routerSvc.SyncKeenDNSPreset(ctx)
	}
	if a.sbOrch != nil {
		return a.sbOrch.Bootstrap()
	}
	return nil
}

func (a *app) purgeSingboxEngineAndTunnels(ctx context.Context) (tunnelsRemoved, subsRemoved int, err error) {
	if a.singboxOp == nil {
		return 0, 0, errors.New("sing-box operator is not initialized")
	}
	if tuns, lerr := a.singboxOp.ListTunnels(ctx); lerr == nil {
		tunnelsRemoved = len(tuns)
	}
	if a.subSvc != nil {
		subsRemoved = len(a.subSvc.List())
	}

	if rl := a.routerLifecycle(); rl != nil {
		if perr := rl.ParkForExternalEngine(ctx); perr != nil {
			return 0, 0, fmt.Errorf("park sing-box router before uninstall: %w", perr)
		}
	}
	router.ReleaseNetfilterForRemoval(ctx, router.Deps{AppLog: a.loggingService})

	if a.sbOrch != nil {
		a.sbOrch.CancelPendingReload()
	}
	if serr := a.suppressAndStopSingbox(2 * time.Second); serr != nil {
		return 0, 0, fmt.Errorf("stop sing-box before uninstall: %w", serr)
	}
	if uerr := a.singboxOp.Uninstall(ctx); uerr != nil {
		return 0, 0, uerr
	}

	var clearErrs []error
	if a.subSvc != nil {
		if cerr := a.subSvc.ResetMemoryAndClearFiles(); cerr != nil {
			clearErrs = append(clearErrs, cerr)
		}
	} else if a.subStore != nil {
		if cerr := a.subStore.Clear(); cerr != nil {
			clearErrs = append(clearErrs, cerr)
		}
	}
	if a.subGroupStore != nil && a.subSvc == nil {
		if cerr := a.subGroupStore.Clear(); cerr != nil {
			clearErrs = append(clearErrs, cerr)
		}
	}
	if a.awg3Store != nil {
		if cerr := a.awg3Store.Clear(); cerr != nil {
			clearErrs = append(clearErrs, cerr)
		}
	}
	if err := a.singboxOp.SetStoppedByEngine(true); err != nil {
		clearErrs = append(clearErrs, err)
	}
	a.singboxOp.SetProxiesParked(true)
	return tunnelsRemoved, subsRemoved, errors.Join(clearErrs...)
}

func (a *app) syncStoresAfterRestore(ctx context.Context, restoredSingbox, restoredSettings bool) error {
	var errs []error
	if restoredSettings && a.settingsStore != nil {
		if _, err := a.settingsStore.Load(); err != nil {
			errs = append(errs, err)
		}
	}
	if restoredSingbox {
		if a.subSvc != nil && a.singboxOp != nil {
			if err := a.subSvc.ReloadFromDisk(a.singboxOp.ConfigDir()); err != nil {
				errs = append(errs, err)
			}
		} else {
			if a.subStore != nil {
				if err := a.subStore.Reload(); err != nil {
					errs = append(errs, err)
				}
			}
			if a.subGroupStore != nil {
				if err := a.subGroupStore.Reload(); err != nil {
					errs = append(errs, err)
				}
			}
		}
		if a.awg3Store != nil {
			if err := a.awg3Store.Reload(); err != nil {
				errs = append(errs, err)
			}
		}
		if a.clientRouteStore != nil {
			_ = a.clientRouteStore.Reload()
		}
		if a.singboxOp != nil {
			_, dirErr := os.Stat(a.singboxOp.ConfigDir())
			if a.singboxOp.IsPresent() || dirErr == nil {
				if err := a.syncSingboxAuxiliarySlots(ctx); err != nil {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

// wireFircEngineCoordinator registers the authoritative EngineSwitchHooks on both
// fircOp and fircHandler so all UI and API transitions share the same coordinator.
func (a *app) wireFircEngineCoordinator() {
	if a.fircOp == nil {
		return
	}
	hooks := a.buildProductionEngineSwitchHooks()
	a.fircOp.SetDefaultEngineHooks(hooks)

	if a.singboxOp != nil {
		a.singboxOp.SetPostInstallSync(func(ctx context.Context) error {
			return a.syncSingboxAuxiliarySlots(ctx)
		})
	}

	arch := detectArch()
	spec := firc.EmbeddedFircBinaries[arch]
	installer := firc.NewOnDemandInstaller(arch, spec)
	if !spec.IsAvailable() {
		if fallback, ok := firc.FallbackFircPackages[arch]; ok && fallback.IsAvailable() {
			installer.SetFallbackPackageSpec(fallback)
		}
	}
	installer.SetRuntimeLocker(a.fircOp.LockRuntimeMutation)
	if a.downloadSvc != nil {
		installer.SetDownloader(fircDownloaderAdapter{svc: a.downloadSvc})
	}
	installer.SetEngineActiveChecker(a.isFircEngineActive)
	installer.SetFircRunningChecker(func(ctx context.Context) bool {
		return a.fircOp != nil && a.fircOp.IsRunning(ctx)
	})
	installer.SetTunvlessRunningChecker(func(ctx context.Context) bool {
		out, err := exec.CommandContext(ctx, "pidof", "tunvless").Output()
		if err == nil {
			return len(strings.TrimSpace(string(out))) > 0
		}
		var exitErr *exec.ExitError
		// pidof exit 1 means no process; any other failure is unknown, not safe.
		return !errors.As(err, &exitErr) || exitErr.ExitCode() != 1
	})
	installer.SetDependencyChecker(func(ctx context.Context) ([]string, error) {
		if a.settingsStore == nil {
			return nil, fmt.Errorf("settings store unavailable")
		}
		if _, err := a.settingsStore.Get(); err != nil {
			return nil, err
		}
		return a.fircOp.RuntimeDependents()
	})
	a.fircInstaller = installer

	if a.fircHandler != nil {
		a.fircHandler.SetOtherEngineLifecycle(hooks.StartTarget, hooks.StopEngine)
		a.fircHandler.SetOtherEngineRoutingHooks(hooks.CaptureState, hooks.ParkEngineForStandby, hooks.ActivateTarget)
		a.fircHandler.SetOnSelectEngine(hooks.OnSwitched)
		a.fircHandler.SetInstaller(installer)
		a.fircHandler.SetOtherEngineIntegrationHooks(api.OtherEngineIntegrationHooks{
			IsSingboxRunning: func() bool {
				if a.singboxOp == nil {
					return false
				}
				running, _ := a.singboxOp.IsRunning()
				return running
			},
			IsSingboxInstalled: func() bool {
				return a.singboxOp != nil && a.singboxOp.IsPresent()
			},
			GetSingboxCounts: func() (int, int) {
				var tunnelsCount, subsCount int
				if a.singboxOp != nil {
					if list, err := a.singboxOp.ListTunnels(context.Background()); err == nil {
						tunnelsCount = len(list)
					}
				}
				if a.subSvc != nil {
					subsCount = len(a.subSvc.List())
				} else if a.subStore != nil {
					subsCount = len(a.subStore.List())
				}
				return tunnelsCount, subsCount
			},
			PurgeSingbox: a.purgeSingboxEngineAndTunnels,
			PostRestoreSync: func(ctx context.Context, restoredSingbox, restoredSettings bool) error {
				return a.syncStoresAfterRestore(ctx, restoredSingbox, restoredSettings)
			},
			OnBeforePurgeFirc: func(_ context.Context) {
				if a.vlessServerService != nil {
					a.vlessServerService.SetFircRoutingOverride(false)
				}
			},
		})
	}
	if a.updaterService != nil {
		a.updaterService.SetPreUpgradeHook(func(ctx context.Context) error {
			legacyOwned, err := installer.HasLegacyBundledOwnership()
			if err != nil || !legacyOwned {
				return err
			}
			if spec.IsAvailable() {
				return installer.MigrateLegacyBundledRuntime(ctx, a.fircOp)
			}
			if a.isFircEngineActive() || (a.fircOp != nil && a.fircOp.IsRunning(ctx)) {
				return errors.New("cannot upgrade awg-manager while legacy bundled FIRC is active and standalone awg-firc-runtime release is unavailable; switch routing engine first")
			}
			return nil
		})
	}
}

func resolveFircConfigDir(dataDir string) string {
	if dataDir != "" && dataDir != defaultDataDir {
		return filepath.Join(dataDir, "firc")
	}
	return firc.DefaultConfigDir
}

// setupFirc initializes the firc Operator, performs one-shot automatic migration
// from sing-box config.d if firc configs are not yet populated, and starts fircd
// when RoutingEngine == "firc".
func (a *app) setupFirc() {
	configDir := resolveFircConfigDir(a.dataDir)

	a.fircOp = firc.NewOperator(firc.Options{
		Logger:      slog.Default().With("component", "firc"),
		ConfigDir:   configDir,
		InboundPort: firc.DefaultMixedInboundPort,
	})
	a.fircHandler = api.NewFircHandler(a.fircOp)
	a.fircHandler.SetSettingsStore(a.settingsStore)
	if a.singboxOp != nil {
		a.fircHandler.SetSingboxConfigDir(a.singboxOp.ConfigDir())
		a.fircOp.SetSingboxBinary(a.singboxOp.Binary())
	}
	a.fircHandler.SetOnExtChanged(func(ext firc.ExtConfig) {
		a.syncRouterSettingsFromFircExt(ext)
	})
	a.fircOp.SetServerStatusCallbacks(
		func() bool {
			if a.vlessServerService == nil {
				return false
			}
			return a.vlessServerService.GetStatus().Running
		},
		func() (bool, string) {
			return false, ""
		},
	)
	if a.vlessServerService != nil {
		a.vlessServerService.SetFircRoutingOverride(a.fircOp.LoadExtConfig().ServerRouting.IsServerEnabled("vless_xhttp_server"))
	}
	a.wireFircKeenDNSCloudProvider()
	a.wireFircEngineCoordinator()
	a.completeFircStartup()
}

// wireFircKeenDNSCloudProvider registers a narrow RCI relay provider backed by NDMS /show/ndns
// queries so FIRC cloud routing stays decoupled from legacy router packages.
func (a *app) wireFircKeenDNSCloudProvider() {
	if a.fircOp == nil || a.ndmsQueries == nil || a.ndmsQueries.KeenDNS == nil {
		return
	}
	kd := a.ndmsQueries.KeenDNS
	a.fircOp.SetKeenCloudProvider(func(ctx context.Context) (firc.KeenCloudRelaySnapshot, error) {
		info, err := kd.Get(ctx)
		if err != nil {
			return firc.KeenCloudRelaySnapshot{}, err
		}
		if info == nil {
			return firc.KeenCloudRelaySnapshot{}, nil
		}
		return firc.KeenCloudRelaySnapshot{
			Enabled:      info.Enabled,
			RelayIPs:     append([]string(nil), info.RelayIPs...),
			RelayDomains: append([]string(nil), info.RelayDomains...),
		}, nil
	})
}

// runFircStartupRecoveryGate enforces both the Operator's recovery marker and the
// OnDemandInstaller's package/migration operation journal before allowing automatic
// sing-box config migration or FIRC daemon startup.
func (a *app) runFircStartupRecoveryGate() bool {
	if a.fircOp != nil && a.fircOp.IsRecoveryRequired() {
		reason := a.fircOp.RecoveryReason()
		if a.bootLog != nil {
			a.bootLog.Warn("firc-recovery", "startup", "firc recovery_required persisted across restart; automatic migration/start blocked until reconciled: "+reason)
		}
		return false
	}
	if a.fircInstaller != nil {
		recCtx, recCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		recErr := a.fircInstaller.RecoverPendingOperationOnStartup(recCtx, a.fircOp)
		recCancel()
		if recErr != nil {
			if a.bootLog != nil {
				a.bootLog.Warn("firc-runtime-recovery", "startup", "firc package operation recovery required; automatic migration/start blocked: "+recErr.Error())
			}
			return false
		}
	}
	return true
}

func (a *app) completeFircStartup() {
	if !a.runFircStartupRecoveryGate() {
		return
	}

	// One-shot migration from sing-box config.d if FIRC runtime is installed and tunnels.yaml & groups.yaml do not exist yet
	if a.singboxOp != nil && a.fircOp.ValidateBinaries() == nil {
		_, tunErr := os.Stat(a.fircOp.TunnelsPath())
		_, grpErr := os.Stat(a.fircOp.GroupsPath())
		if os.IsNotExist(tunErr) && os.IsNotExist(grpErr) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			res, err := a.fircOp.MigrateFromSingbox(ctx, a.singboxOp.ConfigDir())
			cancel()
			if err != nil {
				if a.bootLog != nil {
					a.bootLog.Warn("firc-migration", "", err.Error())
				}
			} else if res != nil && (len(res.Tunnels) > 0 || len(res.Groups) > 0) {
				if a.bootLog != nil {
					a.bootLog.Info("firc-migration", "", "migrated sing-box tunnels and routing groups to firc")
				}
			}
		}
	}

	if a.isFircEngineActive() {
		stopByEngine := a.singboxOp != nil && a.singboxOp.IsStoppedByEngine()
		if a.singboxOp != nil {
			a.singboxOp.SetProxiesParked(true)
			parkCtx, parkCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := a.singboxOp.ParkOwnedNDMSProxies(parkCtx); err != nil && a.bootLog != nil {
				a.bootLog.Warn("firc-startup", "park-proxies", err.Error())
			}
			parkCancel()
		}
		var releaseHold func()
		if stopByEngine && a.sbOrch != nil {
			releaseHold = a.sbOrch.HoldReloads()
		}
		if rl := a.routerLifecycle(); rl != nil {
			parkCtx, parkCancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := rl.ParkForExternalEngine(parkCtx); err != nil && a.bootLog != nil {
				a.bootLog.Warn("firc-startup", "park-router", err.Error())
			}
			parkCancel()
		}
		if stopByEngine {
			if a.sbOrch != nil {
				a.sbOrch.CancelPendingReload()
				releaseHold()
			}
			_ = a.suppressAndStopSingbox(0)
		}
		if !a.fircOp.ManuallyDisabled() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.fircOp.Start(ctx); err != nil {
				if a.bootLog != nil {
					a.bootLog.Warn("firc", "start", err.Error())
				}
			}
			a.deferOnExit(func() {
				a.fircOp.StopDirectChild()
			})
		}
	}
}

func isAwgServerIngressRef(ref string) bool {
	return strings.HasPrefix(ref, "managed:") || ref == "iface:nwg2"
}

func isKeeneticWgIngressRef(ref string) bool {
	return ref == "iface:Wireguard0" || ref == "iface:nwg0"
}

func isWdttServerIngressRef(ref string) bool {
	switch ref {
	case "iface:wdtt0", "iface:wdttraw0", "iface:opkgtun17", "iface:opkgtun18", "iface:opkgtun19":
		return true
	default:
		return false
	}
}

func hasAnyRef(refs []string, match func(string) bool) bool {
	for _, r := range refs {
		if match(r) {
			return true
		}
	}
	return false
}

// syncRouterSettingsFromFircExt propagates changes made in FIRC -> Settings
// ("Маршрутизация входных VPN-серверов через FIRC") back to VLESS server config
// and SingboxRouter.IngressInterfaces / SSTPIngress so all server cards stay in sync.
func (a *app) syncRouterSettingsFromFircExt(ext firc.ExtConfig) {
	if a.vlessServerService != nil {
		a.vlessServerService.SyncFircServerRouting(ext.ServerRouting.IsServerEnabled("vless_xhttp_server"))
	}
	if a.settingsStore == nil {
		return
	}
	_ = a.settingsStore.Update(func(s *storage.Settings) error {
		refs := append([]string(nil), s.SingboxRouter.IngressInterfaces...)
		removeMatching := func(match func(string) bool) {
			kept := make([]string, 0, len(refs))
			for _, r := range refs {
				if !match(r) {
					kept = append(kept, r)
				}
			}
			refs = kept
		}
		addUnique := func(newRefs ...string) {
			for _, nr := range newRefs {
				exists := false
				for _, r := range refs {
					if r == nr {
						exists = true
						break
					}
				}
				if !exists {
					refs = append(refs, nr)
				}
			}
		}

		// 1. AWG Server (Wireguard2)
		if ext.ServerRouting.IsServerEnabled("awg_server") {
			if !hasAnyRef(refs, isAwgServerIngressRef) {
				target := "managed:Wireguard2"
				if len(s.ManagedServers) > 0 && strings.TrimSpace(s.ManagedServers[0].InterfaceName) != "" {
					target = "managed:" + strings.TrimSpace(s.ManagedServers[0].InterfaceName)
				}
				addUnique(target)
			}
		} else {
			removeMatching(isAwgServerIngressRef)
		}

		// 2. Keenetic WireGuard (Wireguard0)
		if ext.ServerRouting.IsServerEnabled("keenetic_wg") {
			if !hasAnyRef(refs, isKeeneticWgIngressRef) {
				addUnique("iface:Wireguard0")
			}
		} else {
			removeMatching(isKeeneticWgIngressRef)
		}

		// 3. WDTT Server
		if ext.ServerRouting.IsServerEnabled("wdtt_server") {
			if !hasAnyRef(refs, isWdttServerIngressRef) {
				addUnique("iface:wdtt0", "iface:wdttraw0", "iface:opkgtun17", "iface:opkgtun19")
			}
		} else {
			removeMatching(isWdttServerIngressRef)
		}

		s.SingboxRouter.IngressInterfaces = refs
		s.SingboxRouter.SSTPIngress = ext.ServerRouting.IsServerEnabled("keenetic_sstp")
		return nil
	})
}

// syncFircFromRouterSettings propagates ingress toggle changes made on individual
// server cards (AWG, Wireguard0, WDTT, SSTP) into FIRC's awgm-ext.json.
func (a *app) syncFircFromRouterSettings(ctx context.Context, prev, next storage.SingboxRouterSettings) {
	if a.fircOp == nil {
		return
	}

	prevAwg := hasAnyRef(prev.IngressInterfaces, isAwgServerIngressRef)
	nextAwg := hasAnyRef(next.IngressInterfaces, isAwgServerIngressRef)

	prevWg0 := hasAnyRef(prev.IngressInterfaces, isKeeneticWgIngressRef)
	nextWg0 := hasAnyRef(next.IngressInterfaces, isKeeneticWgIngressRef)

	prevWdtt := hasAnyRef(prev.IngressInterfaces, isWdttServerIngressRef)
	nextWdtt := hasAnyRef(next.IngressInterfaces, isWdttServerIngressRef)

	ext := a.fircOp.LoadExtConfig()
	changed := false
	setIfDiff := func(id string, want bool) {
		if ext.ServerRouting.IsServerEnabled(id) != want {
			ext.ServerRouting.SetServerEnabled(id, want)
			changed = true
		}
	}

	if prevAwg != nextAwg {
		setIfDiff("awg_server", nextAwg)
	}
	if prevWg0 != nextWg0 {
		setIfDiff("keenetic_wg", nextWg0)
	}
	if prevWdtt != nextWdtt {
		setIfDiff("wdtt_server", nextWdtt)
	}
	if prev.SSTPIngress != next.SSTPIngress {
		setIfDiff("keenetic_sstp", next.SSTPIngress)
	}

	if changed && (a.isFircEngineActive() || a.fircOp.ValidateBinaries() == nil) {
		if a.isFircEngineActive() {
			_, _ = a.fircOp.ApplyExtConfig(ctx, ext)
		} else {
			_, _ = a.fircOp.StageExtConfigOffline(ctx, ext)
		}
	}
}

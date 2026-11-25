package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/api"
	"github.com/hoaxisr/awg-manager/internal/downloader"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/firc"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/appver"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
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
				if errors.Is(a.fircOp.CheckNotForeign(), firc.ErrForeignFirc) {
					// Сторонний firc: с движка FIRC уходят и без sing-box —
					// маршрутизация панели просто выключается.
					return a.singboxOp.SetStoppedByEngine(false)
				}
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
			if a.singboxOp != nil && !a.singboxOp.IsPresent() && errors.Is(a.fircOp.CheckNotForeign(), firc.ErrForeignFirc) {
				return nil // ушли с FIRC без sing-box (см. startTarget): поднимать нечего
			}
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
	installer.SetRuntimeLocker(a.fircOp.LockRuntimeMutation)
	if a.downloadSvc != nil {
		installer.SetDownloader(fircDownloaderAdapter{svc: a.downloadSvc})
	}
	installer.SetEngineActiveChecker(a.isFircEngineActive)
	// Ping не отвечает и у зависшего fircd, а prerm рантайма проверяет pidof:
	// по одному ping обновление сочло бы процесс остановленным и упало в opkg.
	installer.SetFircRunningChecker(func(ctx context.Context) bool {
		return (a.fircOp != nil && a.fircOp.IsRunning(ctx)) || pidofRunning(ctx, "fircd")
	})
	installer.SetTunvlessRunningChecker(func(ctx context.Context) bool {
		return pidofRunning(ctx, "tunvless")
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
	// Конец асинхронной операции рантайма (успех или ошибка) — без этого
	// вкладка, не державшая опрос хода, так и показывала бы «устанавливается».
	installer.SetOnFinished(func() {
		a.eventBus.PublishInvalidated(events.ResourceFirc, "runtime-operation-finished")
	})

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
		AppLog:      a.loggingService,
		ConfigDir:   configDir,
		InboundPort: firc.DefaultMixedInboundPort,
		Foreign:     firc.NewForeignDetector(""),
	})
	a.fircHandler = api.NewFircHandler(a.fircOp)
	a.fircHandler.SetSettingsStore(a.settingsStore)
	// Шину хендлеру раньше не передавали вовсе — его подсказки
	// (publishEngineInvalidations после purge/restore) уходили в nil.
	a.fircHandler.SetEventBus(a.eventBus)
	a.fircOp.SetOnRuntimeChange(func() {
		a.eventBus.PublishInvalidated(events.ResourceFirc, "runtime-changed")
	})
	if a.singboxOp != nil {
		a.fircHandler.SetSingboxConfigDir(a.singboxOp.ConfigDir())
		a.fircOp.SetSingboxBinary(a.singboxOp.Binary())
	}
	a.fircHandler.SetOnExtChanged(func(ext firc.ExtConfig) {
		a.syncRouterSettingsFromFircExt(ext)
	})
	a.fircOp.SetWGServersProvider(a.listDynamicFircWGServers)
	a.fircOp.SetRouterPoliciesProvider(a.fircRouterPolicies)
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
	a.migrateLegacyFircServerRouting(context.Background())
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
		info, ndnsErr := kd.Get(ctx)
		cloudIPs, _ := kd.GetCloudTargets(ctx)
		if ndnsErr != nil && len(cloudIPs) == 0 {
			return firc.KeenCloudRelaySnapshot{}, ndnsErr
		}
		seen := make(map[string]struct{})
		var mergedIPs []string
		addIP := func(ip string) {
			if ip == "" {
				return
			}
			if _, ok := seen[ip]; !ok {
				seen[ip] = struct{}{}
				mergedIPs = append(mergedIPs, ip)
			}
		}
		snap := firc.KeenCloudRelaySnapshot{}
		if info != nil {
			snap.Enabled = info.Enabled
			snap.RelayDomains = append([]string(nil), info.RelayDomains...)
			for _, ip := range info.RelayIPs {
				addIP(ip)
			}
		}
		for _, ip := range cloudIPs {
			addIP(ip)
		}
		snap.RelayIPs = mergedIPs
		return snap, nil
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
		abandoned := a.fircInstaller.PendingOperationAction() == firc.AbandonedExternalMigration
		recCtx, recCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		recErr := a.fircInstaller.RecoverPendingOperationOnStartup(recCtx, a.fircOp)
		recCancel()
		if recErr != nil {
			if a.bootLog != nil {
				a.bootLog.Warn("firc-runtime-recovery", "startup", "firc package operation recovery required; automatic migration/start blocked: "+recErr.Error())
			}
			return false
		}
		if abandoned && a.bootLog != nil {
			a.bootLog.Warn("firc-runtime-recovery", "migrate_external",
				"прерванный перевод пакета firc на рантайм AWGM отменён; копия настроек, если она была сделана, осталась в "+a.fircInstaller.ExternalMigrationBackupDir())
		}
	}
	return true
}

func (a *app) completeFircStartup() {
	if err := a.fircOp.CheckNotForeign(); err != nil {
		a.completeForeignFircStartup(err)
		a.recoverOrphanServerRoutingTx() // под чужим каталогом только отмечает отложенное
		return
	}
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
		a.parkSingboxForFirc()
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
	a.recoverOrphanServerRoutingTx()
}

// parkSingboxForFirc паркует sing-box на старте, когда выбран движок FIRC.
func (a *app) parkSingboxForFirc() {
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
}

// completeForeignFircStartup — старт при стороннем firc: ни восстановления
// операций рантайма, ни миграции, ни запуска демона. Если движок FIRC уже был
// выбран, sing-box остаётся припаркован, а свои цепочки FEXT_* и хук снимаем —
// иначе они действовали бы на туннели стороннего firc.
func (a *app) completeForeignFircStartup(reason error) {
	if !a.isFircEngineActive() {
		return
	}
	if a.bootLog != nil {
		a.bootLog.Warn("firc", "start", reason.Error())
	}
	a.parkSingboxForFirc()
	if !errors.Is(reason, firc.ErrForeignFirc) {
		return // чей firc — неизвестно: работающий может быть нашим, не трогаем
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.fircOp.Stop(ctx); err != nil && a.bootLog != nil {
		a.bootLog.Warn("firc", "release-rules", err.Error())
	}
}

// recoverOrphanServerRoutingTx подбирает транзакции sr-tx, которые журнал
// ingress не успел записать (сбой между PrepareServerRouting и записью
// журнала). Зовётся после запуска движка: откат транзакции, начатой при живом
// fircd, синхронизирует его группы и на остановленном упал бы в
// recovery_required. Журнал ingress к этому моменту закрыт его StartupRecovery.
func (a *app) recoverOrphanServerRoutingTx() {
	if a.fircOp == nil || a.ingressCoordinator == nil {
		return
	}
	if req, _ := a.ingressCoordinator.IsRecoveryRequired(); req {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.fircOp.RecoverIncompleteServerRoutingTx(ctx); err != nil && a.bootLog != nil {
		a.bootLog.Warn("firc", "server_routing_recovery", err.Error())
	}
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

func isKernelNetIfaceUp(name string) bool {
	if name == "" {
		return false
	}
	nif, err := net.InterfaceByName(name)
	return err == nil && nif != nil && (nif.Flags&net.FlagUp) != 0
}

func (a *app) listDynamicFircWGServers(ctx context.Context) []firc.DynamicWGServer {
	list, _ := a.listDynamicFircWGServersWithStatus(ctx)
	return list
}

func (a *app) listDynamicFircWGServersWithStatus(ctx context.Context) ([]firc.DynamicWGServer, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	qCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		qCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}

	var ndmsWG []ndms.WireguardServer
	ndmsByID := make(map[string]ndms.WireguardServer)
	ndmsListOK := false
	if a.ndmsQueries != nil && a.ndmsQueries.WGServers != nil {
		if listed, err := a.ndmsQueries.WGServers.List(qCtx); err == nil {
			ndmsWG = listed
			ndmsListOK = true
			for _, s := range listed {
				ndmsByID[s.ID] = s
			}
		}
	}

	outboundNWGSet := make(map[string]bool)
	if a.awgStore != nil {
		if tunnels, err := a.awgStore.List(); err == nil {
			for _, t := range tunnels {
				if t.Backend == "nativewg" {
					outboundNWGSet[nwg.NewNWGNames(t.NWGIndex).NDMSName] = true
				}
			}
		}
	}

	resolvePolicy := func(ndmsName string) string {
		if a.ndmsQueries == nil || a.ndmsQueries.RunningConfig == nil {
			return ""
		}
		p, err := a.ndmsQueries.RunningConfig.GetInterfaceHotspotPolicy(qCtx, ndmsName)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(p)
	}

	var out []firc.DynamicWGServer
	managedSet := make(map[string]bool)
	var markedIDs []string
	if a.settingsStore != nil {
		markedIDs = a.settingsStore.GetServerInterfaces()
		for _, ms := range a.settingsStore.GetManagedServers() {
			ndmsName := strings.TrimSpace(ms.InterfaceName)
			if ndmsName == "" {
				continue
			}
			managedSet[ndmsName] = true
			live, hasLive := ndmsByID[ndmsName]
			kernelIface := ""
			if hasLive && strings.TrimSpace(live.InterfaceName) != "" {
				kernelIface = strings.TrimSpace(live.InterfaceName)
			} else if k, ok := ndms.KernelName(ndmsName); ok {
				kernelIface = k
			}
			running := false
			if hasLive {
				running = live.Status == "up"
			} else {
				running = isKernelNetIfaceUp(kernelIface)
			}
			subnet := firc.FormatIPv4SubnetCIDR(ms.Address, ms.Mask)
			if subnet == "" && hasLive {
				subnet = firc.FormatIPv4SubnetCIDR(live.Address, live.Mask)
			}
			name := strings.TrimSpace(ms.Description)
			if name == "" && hasLive {
				name = strings.TrimSpace(live.Description)
			}
			if name == "" {
				name = "Сервер AmneziaWG"
			}
			out = append(out, firc.DynamicWGServer{
				ID:          "managed:" + ndmsName,
				NDMSName:    ndmsName,
				KernelIface: kernelIface,
				Name:        name,
				Category:    "awgm",
				Subnet:      subnet,
				Running:     running,
				Policy:      resolvePolicy(ndmsName),
			})
		}
	}

	markedSet := make(map[string]bool, len(markedIDs))
	for _, id := range markedIDs {
		markedSet[id] = true
	}
	for _, s := range ndmsWG {
		if managedSet[s.ID] || outboundNWGSet[s.ID] {
			continue
		}
		isBuiltIn := s.Description == ndms.BuiltInVPNServerDescription
		isMarked := markedSet[s.ID]
		if !isBuiltIn && !isMarked {
			continue
		}
		kernelIface := strings.TrimSpace(s.InterfaceName)
		if kernelIface == "" {
			if k, ok := ndms.KernelName(s.ID); ok {
				kernelIface = k
			}
		}
		name := strings.TrimSpace(s.Description)
		if name == "" {
			name = s.ID
		}
		out = append(out, firc.DynamicWGServer{
			ID:          "wg:" + s.ID,
			NDMSName:    s.ID,
			KernelIface: kernelIface,
			Name:        name,
			Category:    "system",
			Subnet:      firc.FormatIPv4SubnetCIDR(s.Address, s.Mask),
			Running:     s.Status == "up",
			BuiltIn:     isBuiltIn,
			Policy:      resolvePolicy(s.ID),
		})
	}
	return out, ndmsListOK
}

// fircRouterPolicies собирает политики роутера для разбора исключений групп FIRC.
// Исключение снимается безвозвратно, поэтому чтение мимо кэша (Fetch), а любой
// отказ — «данных нет».
func (a *app) fircRouterPolicies(ctx context.Context) (firc.RouterPolicies, bool) {
	if a.ndmsQueries == nil || a.ndmsQueries.Policies == nil || a.ndmsQueries.RunningConfig == nil || a.proxyStore == nil {
		return firc.RouterPolicies{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	policies, err := a.ndmsQueries.Policies.Fetch(ctx)
	if err != nil {
		return firc.RouterPolicies{}, false
	}
	lines, err := a.ndmsQueries.RunningConfig.Fetch(ctx)
	if err != nil {
		return firc.RouterPolicies{}, false
	}
	st, err := a.proxyStore.Load()
	if err != nil {
		return firc.RouterPolicies{}, false
	}

	var rp firc.RouterPolicies
	rp.Interfaces, rp.Hosts = ndmsquery.HotspotPolicies(lines)
	policies = slices.Clone(policies) // срез общий с кэшем
	slices.SortFunc(policies, func(x, y ndms.Policy) int { return x.RCOrder - y.RCOrder })
	for _, p := range policies {
		rp.Policies = append(rp.Policies, firc.RouterPolicy{Name: p.Name, Description: p.Description})
	}
	for _, rec := range st.Records {
		if rec.WdttServer == nil {
			continue
		}
		rp.WDTT = append(rp.WDTT, firc.WDTTServerPolicy{
			Policy:     rec.WdttServer.Policy,
			Interfaces: []string{rec.WdttServer.NdmsIface, rec.WdttServer.RawNdmsIface},
		})
	}
	return rp, true
}

func (a *app) migrateLegacyFircServerRouting(ctx context.Context) {
	if a.fircOp == nil {
		return
	}
	ext := a.fircOp.LoadExtConfig()
	hasAwgLegacy := ext.ServerRouting.IsServerEnabled("awg_server")
	hasWgLegacy := ext.ServerRouting.IsServerEnabled("keenetic_wg")
	if !hasAwgLegacy && !hasWgLegacy {
		return
	}
	wgServers, ndmsListOK := a.listDynamicFircWGServersWithStatus(ctx)
	changed := false
	if hasAwgLegacy {
		var managedTargets []string
		for _, srv := range wgServers {
			if srv.Category == "awgm" {
				if strings.EqualFold(srv.NDMSName, "Wireguard2") {
					managedTargets = []string{srv.ID}
					break
				}
				managedTargets = append(managedTargets, srv.ID)
			}
		}
		if len(managedTargets) > 0 || a.settingsStore != nil {
			ext.ServerRouting.SetServerEnabled("awg_server", false)
			for _, id := range managedTargets {
				ext.ServerRouting.SetServerEnabled(id, true)
			}
			changed = true
		}
	}
	if hasWgLegacy {
		builtInID := ""
		for _, srv := range wgServers {
			if srv.Category == "system" && srv.BuiltIn {
				builtInID = srv.ID
				break
			}
		}
		if builtInID != "" {
			ext.ServerRouting.SetServerEnabled("keenetic_wg", false)
			ext.ServerRouting.SetServerEnabled(builtInID, true)
			changed = true
		} else if ndmsListOK {
			ext.ServerRouting.SetServerEnabled("keenetic_wg", false)
			changed = true
		}
	}
	if changed {
		_ = a.fircOp.SaveExtConfig(ext)
	}
}

func legacyAwgServerMatches(wgServers []firc.DynamicWGServer, target firc.DynamicWGServer) bool {
	if target.Category != "awgm" {
		return false
	}
	for _, srv := range wgServers {
		if srv.Category == "awgm" && strings.EqualFold(srv.NDMSName, "Wireguard2") {
			return strings.EqualFold(target.NDMSName, "Wireguard2")
		}
	}
	return true
}

func systemServerIngressMatcher(srv firc.DynamicWGServer) (targetRef string, matchFn func(string) bool) {
	if srv.KernelIface != "" {
		targetRef = "iface:" + srv.KernelIface
	} else if srv.NDMSName != "" {
		targetRef = "iface:" + srv.NDMSName
	}
	matchFn = func(ref string) bool {
		return (srv.KernelIface != "" && ref == "iface:"+srv.KernelIface) ||
			(srv.NDMSName != "" && ref == "iface:"+srv.NDMSName)
	}
	return targetRef, matchFn
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
	wgServers := a.listDynamicFircWGServers(context.Background())
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
				if nr == "" {
					continue
				}
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

		// 1. Dynamic AWG & Keenetic WireGuard servers
		seenManagedRefs := make(map[string]bool)
		for _, srv := range wgServers {
			switch srv.Category {
			case "awgm":
				ref := "managed:" + srv.NDMSName
				seenManagedRefs[ref] = true
				enabled := ext.ServerRouting.IsServerEnabled(srv.ID) ||
					(ext.ServerRouting.IsServerEnabled("awg_server") && legacyAwgServerMatches(wgServers, srv))
				if enabled {
					addUnique(ref)
				} else {
					removeMatching(func(r string) bool { return r == ref })
				}
			case "system":
				targetRef, matchFn := systemServerIngressMatcher(srv)
				enabled := ext.ServerRouting.IsServerEnabled(srv.ID) ||
					(srv.BuiltIn && ext.ServerRouting.IsServerEnabled("keenetic_wg"))
				if enabled {
					if !hasAnyRef(refs, matchFn) {
						addUnique(targetRef)
					}
				} else {
					removeMatching(matchFn)
				}
			}
		}
		for _, id := range ext.ServerRouting.EnabledServers {
			if strings.HasPrefix(id, "managed:") && !seenManagedRefs[id] {
				addUnique(id)
			}
		}

		// 2. WDTT Server
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
// server cards (managed AWG, built-in/marked WG, WDTT, SSTP) into FIRC's awgm-ext.json.
func (a *app) syncFircFromRouterSettings(ctx context.Context, prev, next storage.SingboxRouterSettings) {
	if a.fircOp == nil {
		return
	}

	wgServers := a.listDynamicFircWGServers(ctx)
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

	handledRefs := make(map[string]bool)
	for _, srv := range wgServers {
		switch srv.Category {
		case "awgm":
			ref := "managed:" + srv.NDMSName
			handledRefs[ref] = true
			prevOn := slices.Contains(prev.IngressInterfaces, ref)
			nextOn := slices.Contains(next.IngressInterfaces, ref)
			if prevOn != nextOn {
				setIfDiff(srv.ID, nextOn)
				if !nextOn && legacyAwgServerMatches(wgServers, srv) {
					setIfDiff("awg_server", false)
				}
			}
		case "system":
			_, matchFn := systemServerIngressMatcher(srv)
			if srv.KernelIface != "" {
				handledRefs["iface:"+srv.KernelIface] = true
			}
			if srv.NDMSName != "" {
				handledRefs["iface:"+srv.NDMSName] = true
			}
			prevOn := hasAnyRef(prev.IngressInterfaces, matchFn)
			nextOn := hasAnyRef(next.IngressInterfaces, matchFn)
			if prevOn != nextOn {
				setIfDiff(srv.ID, nextOn)
				if !nextOn && srv.BuiltIn {
					setIfDiff("keenetic_wg", false)
				}
			}
		}
	}

	// Fallback if a managed:<name> ref changed when SettingsStore/NDMS was absent in unit tests.
	for _, ref := range prev.IngressInterfaces {
		if strings.HasPrefix(ref, "managed:") && !handledRefs[ref] && !slices.Contains(next.IngressInterfaces, ref) {
			setIfDiff(ref, false)
		}
	}
	for _, ref := range next.IngressInterfaces {
		if strings.HasPrefix(ref, "managed:") && !handledRefs[ref] && !slices.Contains(prev.IngressInterfaces, ref) {
			setIfDiff(ref, true)
		}
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

// pidofRunning: pidof с кодом 1 — процесса нет; любой другой отказ — неизвестно,
// поэтому «запущен» (безопасная сторона для остановки перед opkg).
func pidofRunning(ctx context.Context, name string) bool {
	out, err := exec.CommandContext(ctx, "pidof", name).Output()
	if err == nil {
		return len(strings.TrimSpace(string(out))) > 0
	}
	var exitErr *exec.ExitError
	return !errors.As(err, &exitErr) || exitErr.ExitCode() != 1
}

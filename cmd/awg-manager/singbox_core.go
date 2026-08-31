package main

import (
	"log/slog"
	"path/filepath"
	"runtime"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// singboxCoreDeps — всё, что нужно ядру sing-box-рантайма и что обязан
// предоставить вызывающий (демон или cleanup).
type singboxCoreDeps struct {
	queries  *ndmsquery.Queries
	commands *ndmscommand.Commands
	settings *storage.SettingsStore // источник замыканий OperatorDeps
	appLog   *logging.Service       // AppLogger + логгер оркестратора
	bus      *events.Bus
	bootLog  *logging.ScopedLogger
	dataDir  string // awg3.json
	// dir — каталог управляемого sing-box; пусто = дефолт оператора
	// (каталог рядом с бинарём).
	dir string
	// initialManuallyStopped — снимок Settings.SingboxManuallyStopped,
	// прочитанный вызывающим (демон держит его в a.settings).
	initialManuallyStopped bool
}

type singboxCore struct {
	op        *singbox.Operator
	orch      *singboxorch.Orchestrator
	awg3Store *awg3endpoint.Store
	migrated  bool // ruleset-URL/address-or переписали файлы — runtime решает про reload
}

// buildSingboxCore собирает общую для демона и cleanup часть sing-box-рантайма:
// оператор, оркестратор config.d со всеми слотами и store импортированных
// AWG3-endpoint'ов.
func buildSingboxCore(d singboxCoreDeps) singboxCore {
	// Sing-box integration
	op := singbox.NewOperator(singbox.OperatorDeps{
		Log:             slog.Default().With("component", "singbox"),
		Dir:             d.dir,
		Queries:         d.queries,
		Commands:        d.commands,
		AppLogger:       d.appLog,
		SingboxLogLevel: d.settings.GetSingboxLogLevel,
		BootstrapDNS:    d.settings.GetSingboxBootstrapDNS,
		ClashPort:       d.settings.GetSingboxClashPort,
		// Seed the sticky-stop flag from disk so the watchdog respects
		// a user-pressed Stop across awgm restarts. SetManuallyStopped
		// writes the new intent back through a single-field updater so
		// concurrent writers on other Settings fields (e.g. router
		// service toggling SingboxRouter) cannot silently overwrite it.
		InitialManuallyStopped: d.initialManuallyStopped,
		SetManuallyStopped:     d.settings.SetSingboxManuallyStopped,
		IsNDMSProxyEnabled:     d.settings.IsSingboxNDMSProxyEnabled,
		Bus:                    d.bus,
	})
	// Если на старте флаг disabled — orphan-cleanup (после возможного
	// обрыва прошлой MigrateOff в любой момент). Reconcile подберёт
	// сигнал на первом тике watchdog'а.
	if !d.settings.IsSingboxNDMSProxyEnabled() {
		op.MarkNeedsOrphanCleanup()
	}

	// config.d orchestrator — the single writer of slot files (00-base /
	// 10-tunnels / 15-awg / 20-router / 30-deviceproxy). Producers route
	// their writes through Save / SetEnabled so a "disabled" domain
	// actually moves the file out of sing-box's view (config.d/disabled/)
	// instead of leaving stale content behind.
	singboxConfigDir := op.ConfigDir()
	deviceProxyMigrated, err := singbox.MigrateDeviceProxyOutOfTunnels(singboxConfigDir)
	if err != nil {
		d.bootLog.Warn("deviceproxy-migration", "", err.Error())
	}
	ruleSetURLsMigrated, err := singbox.MigrateRuleSetURLsToFork(singboxConfigDir)
	if err != nil {
		d.bootLog.Warn("ruleset-fork-migration", "", err.Error())
	}
	addressOrMigrated, err := router.MigrateAddressOrRules(singboxConfigDir)
	if err != nil {
		d.bootLog.Warn("address-or-migration", "", err.Error())
	}
	orch := singboxorch.New(singboxConfigDir, op.Process())
	orch.SetLogger(func(level, msg string) {
		switch level {
		case "warn":
			d.appLog.AppLog(logging.LevelWarn, logging.GroupSingbox, logging.SubSBProcess, "orchestrator", "", msg)
		case "error":
			d.appLog.AppLog(logging.LevelError, logging.GroupSingbox, logging.SubSBProcess, "orchestrator", "", msg)
		default:
			d.appLog.AppLog(logging.LevelInfo, logging.GroupSingbox, logging.SubSBProcess, "orchestrator", "", msg)
		}
	})
	orch.SetValidator(&orchValidatorAdapter{v: singbox.NewValidator(installer.DefaultBinaryPath)})
	// Propagate the sticky-stop intent so reload-triggered cold-starts
	// (slot-file writes from router/deviceproxy/subscriptions) respect a
	// user-pressed Stop in the same way the watchdog does.
	orch.SetShouldRun(func() bool { return !op.IsManuallyStopped() })
	// AWG3 imported endpoints — store owns awg3.json, service projects it into
	// the 16-awg3.json slot. Constructed here so the SlotAwg3 HasContent closure
	// (below) can read the store, mirroring SlotTunnels' HasUserTunnels gate.
	awg3Store := awg3endpoint.NewStore(filepath.Join(d.dataDir, "awg3.json"))
	for _, meta := range singboxorch.KnownSlots() {
		// SlotTunnels is AlwaysOn but only counts as "active work" when
		// the user has defined sing-box tunnels — wire HasContent so
		// the daemon stops running for an empty 10-tunnels.json.
		if meta.Slot == singboxorch.SlotTunnels {
			meta.HasContent = func() bool {
				return op.HasUserTunnels()
			}
		}
		// SlotAwg3 is AlwaysOn too — it only justifies keeping the daemon
		// running once the user has imported at least one AWG3 endpoint.
		if meta.Slot == singboxorch.SlotAwg3 {
			meta.HasContent = func() bool {
				return awg3Store.Len() > 0
			}
		}
		if err := orch.Register(meta); err != nil {
			d.bootLog.Error("singbox-orchestrator", string(meta.Slot), "register failed: "+err.Error())
		}
	}
	if err := orch.Bootstrap(); err != nil {
		d.bootLog.Error("singbox-orchestrator", "bootstrap", err.Error())
	}

	// Wire orchestrator into Operator so ApplyConfig writes 10-tunnels.json
	// through SlotTunnels rather than an in-place write that bypasses
	// the orchestrator's validate / debounced reload.
	op.SetOrch(orch)
	// Предикат перезапуска для watchdog/Reconcile (#456): упавший sing-box
	// поднимается и когда легаси-туннелей нет, но активны orchestrator-слоты
	// (router / deviceproxy / subscriptions / пользовательские туннели).
	op.SetActiveWorkFn(orch.HasActiveWork)

	return singboxCore{
		op:        op,
		orch:      orch,
		awg3Store: awg3Store,
		migrated:  ruleSetURLsMigrated || addressOrMigrated || deviceProxyMigrated,
	}
}

// setupSingboxRuntime собирает демонский sing-box-рантайм: ядро
// (buildSingboxCore), AWG3-сервис, подписочный слой и managed-binary
// installer. Присваивает поля *app; периферия (handlers / фоновые
// воркеры / updater) остаётся в setupSingbox.
func (a *app) setupSingboxRuntime() {
	var err error
	core := buildSingboxCore(singboxCoreDeps{
		queries:                a.ndmsQueries,
		commands:               a.ndmsCommands,
		settings:               a.settingsStore,
		appLog:                 a.loggingService,
		bus:                    a.eventBus,
		bootLog:                a.bootLog,
		dataDir:                a.dataDir,
		dir:                    a.singboxDir,
		initialManuallyStopped: a.settings.SingboxManuallyStopped,
	})
	a.singboxOp = core.op
	a.sbOrch = core.orch
	a.awg3Store = core.awg3Store
	singboxConfigDir := a.singboxOp.ConfigDir()
	a.awg3Svc = awg3endpoint.NewService(a.awg3Store, a.sbOrch, a.loggingService)
	// Project any imported AWG3 endpoints into 16-awg3.json on boot so a
	// restart re-materializes the slot from awg3.json (the source of truth).
	// Skip when there is neither a store record nor a 16-awg3.json file:
	// Sync would only spawn a `sing-box check` subprocess (seconds on MIPS,
	// warns without the binary) to produce an empty slot that is already empty.
	// If the slot file exists while the store is empty, still Sync to clear it.
	slotExists := func() bool {
		_, _, ok := a.sbOrch.EffectiveStat(singboxorch.SlotAwg3)
		return ok
	}
	// || is lazy: EffectiveStat (a stat syscall) runs only when the store is empty.
	if a.awg3Store.Len() > 0 || slotExists() {
		if err := a.awg3Svc.Sync(); err != nil {
			a.bootLog.Warn("awg3-sync", "boot", err.Error())
		}
	}
	// Миграция URL rule-set'ов переписала файлы мимо оркестратора: переживший
	// рестарт awgm sing-box иначе держит старые (заблокированные) URL в памяти
	// до случайного reload по другому поводу. Холодный старт (процесс не
	// запущен) прочитает новые файлы сам — reload не нужен.
	// То же и для нормализации правил «пресет ИЛИ свои адреса» (#699).
	if core.migrated {
		if running, _ := a.singboxOp.IsRunning(); running {
			a.bootLog.Info("ruleset-fork-migration", "", "правила/URL мигрированы — перечитываем конфиг живого sing-box")
			if err := a.sbOrch.ReloadNow(); err != nil {
				a.bootLog.Warn("ruleset-fork-migration", "reload", err.Error())
			}
		}
	}
	// Legacy download-proxy slot (35-download-proxy.json) is no longer used
	// by the downloader, but disable it on boot in case a previous awgm
	// process crashed with the slot still enabled.
	if err := a.sbOrch.SetEnabledSilent(singboxorch.SlotDownloadProxy, false); err != nil {
		a.bootLog.Warn("singbox-orchestrator", "downloadproxy-disable", err.Error())
	}
	// Reflect Settings into orchestrator slot enabled-state. router /
	// deviceproxy / subscriptions are content-driven; tunnels / awg
	// are AlwaysOn (registered as such above) and cannot be toggled
	// here — Register already marked them enabled. deviceproxy is
	// reflected after deviceProxySvc is constructed below.
	if curSettings, err := a.settingsStore.Load(); err == nil && curSettings != nil {
		mode := curSettings.SingboxRouter.RoutingMode
		_ = a.sbOrch.SetEnabled(router.RouterSlotForMode(mode), curSettings.SingboxRouter.Enabled)
		_ = a.sbOrch.SetEnabled(router.OtherRouterSlot(mode), false)
		// Повторное примирение base здесь больше не нужно: разметка слотов
		// меняет владельца dns.strategy, но дефолт лежит в 99-defaults.json —
		// в проигрывающей позиции merge, — и перекрывается активным слотом
		// сам. Раньше рассинхрон base пережил бы бут и не лечился периодическим
		// Reconcile (его drift-heal берётся только при запаркованном слоте, а
		// он здесь уже распаркован).
	}

	// Subscription service — owns 40-subscriptions.json in config.d.
	// Слот регистрирует цикл KnownSlots внутри buildSingboxCore, до
	// Bootstrap; Register внутри NewOperatorAdapter — no-op дубль с той же
	// meta (ErrSlotAlreadyRegistered там глотается, см. его докстринг).
	// А вот LoadFromDisk обязан идти ПОСЛЕ Bootstrap: он приводит память
	// адаптера в соответствие с тем, что оказалось на диске.
	subStorePath := filepath.Join(a.dataDir, "subscriptions.json")
	a.subStore, err = subscription.NewStore(subStorePath)
	if err != nil {
		a.bootLog.Error("subscription-store", "", err.Error())
	}
	subProxyMgr := singbox.NewProxyManager(a.ndmsQueries, a.ndmsCommands)
	a.subAdapter = subscription.NewOperatorAdapter(a.sbOrch, subProxyMgr, a.singboxOp.Clash())
	// Wire the Operator's cached sing-box build-tag probe into the
	// subscription adapter so flush() Pass 1 can cheaply pre-filter
	// outbounds whose type requires a missing optional build tag
	// (naive). The probe is cached by binary
	// mtime+size in Operator.detectVersionAndFeaturesCached — common
	// path is ~10µs per call (stat-only check, no subprocess).
	a.subAdapter.SetSingboxFeaturesFn(a.singboxOp.SingboxFeatures)
	if err := a.subAdapter.LoadFromDisk(singboxConfigDir); err != nil {
		a.bootLog.Warn("subscription-adapter", "load-from-disk", err.Error())
	}
	a.subSvc = subscription.NewService(a.subStore, a.subAdapter)
	a.subSvc.SetAppLogger(a.loggingService)
	// Gate subscription ProxyN creation on the global toggle (same flag the
	// Operator uses for tunnels) so disabling it stops subscriptions from
	// creating NDMS Proxy interfaces too.
	a.subSvc.SetNDMSProxyEnabled(a.settingsStore.IsSingboxNDMSProxyEnabled)
	if err := a.subSvc.LoadHappKeys(); err != nil {
		a.bootLog.Warn("subscription-happ-keys", "load-from-disk", err.Error())
	}

	// Сводные группы (#372) — отдельный JSON-файл рядом с subscriptions.json.
	subGroupStorePath := filepath.Join(a.dataDir, "subscription-groups.json")
	a.subGroupStore, err = subscription.NewGroupStore(subGroupStorePath)
	if err != nil {
		// Битый файл групп: карантиним (<path>.corrupt — данные сохраняются
		// для ручного восстановления, как у других store) и пересоздаём
		// пустой store. Иначе функциональность групп молча выключалась бы,
		// а их ProxyN оставались бы без владельца до конца аптайма.
		a.bootLog.Error("subscription-group-store", "", err.Error()+" — quarantining and recreating empty store")
		storage.QuarantineCorrupt(subGroupStorePath, err)
		a.subGroupStore, err = subscription.NewGroupStore(subGroupStorePath)
		if err != nil {
			a.bootLog.Error("subscription-group-store", "", "recreate after quarantine failed: "+err.Error())
		}
	}
	if a.subGroupStore != nil {
		a.subSvc.SetGroupStore(a.subGroupStore)
	}

	// Let NDMS-proxy enable/disable + orphan cleanup manage subscription
	// composite proxies (a set separate from Tunnels()).
	a.singboxOp.SetSubscriptionProxySet(subProxySet{store: a.subStore, groups: a.subGroupStore})
	// On MigrateOn, reconcile subscription proxies through the service so
	// subscriptions created while the toggle was off get a freshly allocated
	// ProxyN (not just the already-indexed ones).
	a.singboxOp.SetSubscriptionProxySync(a.subSvc.SyncProxies)

	// Wire managed-binary installer into Operator. The installer is keyed
	// by the build-time arch string (e.g. "mipsel-3.4") so it can resolve
	// the correct download URL and SHA256 from EmbeddedBinaries.
	arch := detectArch()
	if arch == "" {
		a.bootLog.Warn("managed-singbox", runtime.GOARCH, "could not derive arch — install/update disabled")
	} else {
		spec, ok := installer.EmbeddedBinaries[arch]
		if !ok {
			a.bootLog.Warn("managed-singbox", arch, "no embedded BinarySpec — install/update disabled")
		} else {
			a.singboxInstaller = installer.New(installer.DefaultBinaryPath, arch, spec, a.loggingService)
			a.singboxOp.SetInstaller(a.singboxInstaller)

			// Stream sing-box install/update lifecycle over SSE so the UI
			// can render a live progress bar instead of a blocking spinner.
			a.singboxOp.SetInstallProgressReporter(func(op, phase string, downloaded, total int64, errMsg string) {
				a.eventBus.Publish("singbox:install-progress", events.SingboxInstallProgressEvent{
					Op:         op,
					Phase:      phase,
					Downloaded: downloaded,
					Total:      total,
					Error:      errMsg,
				})
			})

		}
	}
}

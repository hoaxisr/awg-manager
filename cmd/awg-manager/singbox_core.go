package main

import (
	"log/slog"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/awg3endpoint"
	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	ndmscommand "github.com/hoaxisr/awg-manager/internal/ndms/command"
	ndmsquery "github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	singboxorch "github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
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
	if err := singbox.MigrateDeviceProxyOutOfTunnels(singboxConfigDir); err != nil {
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
		migrated:  ruleSetURLsMigrated || addressOrMigrated,
	}
}

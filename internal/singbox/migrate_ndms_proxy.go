package singbox

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"

	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// SettingsToggler — минимальный subset SettingsStore, нужный мигратору.
// Декаплинг изолирует юнит-тесты мигратора от полного SettingsStore.
type SettingsToggler interface {
	SetSingboxCreateNDMSProxy(v bool) error
	IsSingboxNDMSProxyEnabled() bool
}

// Migrator переводит sing-box между режимами NDMS Proxy on/off.
// Не делает dependency-check: реальные NDMS-policies живут в прошивке
// роутера (не в awg-manager storages), пользователю показывается
// предупреждение в UI перед выключением.
type Migrator struct {
	op       *Operator
	settings SettingsToggler
	log      *slog.Logger
	// appLog — журнал приложения (system/ndms): ошибки создания/удаления
	// ProxyN при переключении режима видны пользователю, а не только в
	// stderr (nil-safe).
	appLog *logging.ScopedLogger
}

func NewMigrator(op *Operator, settings SettingsToggler, appLogger logging.AppLogger) *Migrator {
	log := op.log
	if log == nil {
		log = slog.Default()
	}
	return &Migrator{
		op:       op,
		settings: settings,
		log:      log,
		appLog:   logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubNDMS),
	}
}

// MigrateOff выключает создание ProxyN. Sequence:
//  1. Settings.flag := false (single-writer pattern). Делаем первым,
//     чтобы при обрыве в шаге 2 next-start подобрал orphan-cleanup.
//  2. Для каждого туннеля с ненулевым ProxyInterface — RemoveProxy(idx).
//     Best-effort, ошибки только в лог.
//  3. MarkNeedsOrphanCleanup — Reconcile дочистит остатки на следующем тике.
//  4. SSE invalidate.
//
// config.json не правится: ProxyInterface/KernelInterface — derived в
// Tunnels() парсере из listen_port, не stored. ListTunnels (T9) очищает
// их в выдаче на верх в disabled-режиме.
func (m *Migrator) MigrateOff(ctx context.Context) error {
	m.op.migrationMu.Lock()
	defer m.op.migrationMu.Unlock()

	if err := m.settings.SetSingboxCreateNDMSProxy(false); err != nil {
		return fmt.Errorf("flip setting: %w", err)
	}

	cfg, err := m.op.loadConfig()
	if err == nil && cfg != nil {
		for _, t := range cfg.Tunnels() {
			if t.ProxyInterface == "" {
				continue
			}
			idx, perr := parseProxyIdx(t.ProxyInterface)
			if perr != nil || idx < 0 {
				continue
			}
			if rerr := m.op.proxyMgr.RemoveProxy(ctx, idx); rerr != nil {
				m.log.Warn("MigrateOff: RemoveProxy failed",
					"tag", t.Tag, "iface", t.ProxyInterface, "err", rerr)
				m.appLog.Warn("ndms-proxy-migrate", t.Tag, fmt.Sprintf("remove %s failed: %v", t.ProxyInterface, rerr))
			}
		}
	}

	// Subscription composite proxies are a separate managed set, invisible to
	// Tunnels() — remove them explicitly so disabling NDMS Proxy also tears
	// down the ProxyN behind selector/urltest subscriptions.
	for _, sp := range m.op.subscriptionProxies() {
		if rerr := m.op.proxyMgr.RemoveProxy(ctx, sp.Index); rerr != nil {
			m.log.Warn("MigrateOff: RemoveProxy (subscription) failed",
				"label", sp.Label, "idx", sp.Index, "err", rerr)
			m.appLog.Warn("ndms-proxy-migrate", sp.Label, fmt.Sprintf("remove Proxy%d failed: %v", sp.Index, rerr))
		}
	}

	m.op.MarkNeedsOrphanCleanup()
	if m.op.bus != nil {
		m.op.bus.PublishInvalidated(events.ResourceSingboxStatus, "ndms-proxy-migration")
		m.op.bus.PublishInvalidated(events.ResourceSingboxTunnels, "ndms-proxy-migration")
	}
	return nil
}

// MigrateOn включает создание ProxyN. Precondition (NDMS-компонент
// 'proxy' установлен) проверяется ДО изменения settings, чтобы не
// оставить флаг включённым без рабочей инфраструктуры.
func (m *Migrator) MigrateOn(ctx context.Context) error {
	m.op.migrationMu.Lock()
	defer m.op.migrationMu.Unlock()

	if !ndmsinfo.HasProxyComponent() {
		return ErrProxyComponentMissing
	}

	if err := m.settings.SetSingboxCreateNDMSProxy(true); err != nil {
		return fmt.Errorf("flip setting: %w", err)
	}

	cfg, err := m.op.loadConfig()
	if err == nil && cfg != nil {
		// SyncProxies идемпотентен — создаст недостающие ProxyN.
		// Tunnels() заполнит ProxyInterface "Proxy<slot>" из listen_port.
		if serr := m.op.proxyMgr.SyncProxies(ctx, cfg.Tunnels()); serr != nil {
			m.log.Warn("MigrateOn: SyncProxies failed", "err", serr)
			m.appLog.Warn("ndms-proxy-migrate", "", "proxy sync failed: "+serr.Error())
		}
	}

	// Recreate subscription composite proxies symmetrically (SyncProxies only
	// covers Tunnels()). When the subscription reconciler is wired it also
	// allocates fresh ProxyN for subscriptions created while the toggle was
	// off (ProxyIndex=-1) — which subscriptionProxies() (filtered to >=0)
	// cannot see. Fall back to the enumerator-only path when not wired.
	if m.op.subProxySync != nil {
		if serr := m.op.subProxySync(ctx); serr != nil {
			m.log.Warn("MigrateOn: subscription proxy sync failed", "err", serr)
			m.appLog.Warn("ndms-proxy-migrate", "", "subscription proxy sync failed: "+serr.Error())
		}
	} else {
		for _, sp := range m.op.subscriptionProxies() {
			if eerr := m.op.proxyMgr.EnsureProxy(ctx, sp.Index, sp.Port, sp.Label); eerr != nil {
				m.log.Warn("MigrateOn: EnsureProxy (subscription) failed",
					"label", sp.Label, "idx", sp.Index, "err", eerr)
				m.appLog.Warn("ndms-proxy-migrate", sp.Label, fmt.Sprintf("ensure Proxy%d failed: %v", sp.Index, eerr))
			}
		}
	}

	if m.op.bus != nil {
		m.op.bus.PublishInvalidated(events.ResourceSingboxStatus, "ndms-proxy-migration")
		m.op.bus.PublishInvalidated(events.ResourceSingboxTunnels, "ndms-proxy-migration")
	}
	return nil
}

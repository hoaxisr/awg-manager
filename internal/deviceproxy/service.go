package deviceproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"
)

// Deps groups the external collaborators Service needs. Wired once at
// startup in main.go. Nil fields are tolerated — Service degrades and
// logs where applicable.
type Deps struct {
	Store                 *Store
	Singbox               SingboxOperator              // nil → treated as "no sb tunnels, no apply"
	SubscriptionOutbounds SubscriptionOutboundsCatalog // nil → no subscription selector/urltest outbounds
	NDMSQuery             NDMSInterfaceQuery           // nil → ListenInterface resolution fails explicitly
	Bus                   *events.Bus                  // nil → no event subscriptions or publishes
	AWGOutbounds          AWGOutboundsCatalog          // nil → AWG-related selector members empty
	AppLogger             logging.AppLogger            // nil → silent in UI logs
}

// AWGOutboundsCatalog is the narrow contract Service needs from the
// awgoutbounds package. Defined here (not imported) to keep the
// dependency direction clean — main.go injects the real impl.
type AWGOutboundsCatalog interface {
	ListTags(ctx context.Context) ([]AWGTagInfo, error)
}

// AWGTagInfo is deviceproxy's projection of awgoutbounds.TagInfo.
// Same shape; lives here so deviceproxy doesn't depend on the
// awgoutbounds package types.
type AWGTagInfo struct {
	Tag   string
	Label string
	Iface string
}

// SubscriptionOutboundsCatalog is the narrow contract Service needs from
// the sing-box subscription service. It exposes subscription selector/urltest
// outbounds that can be used as device-proxy targets.
type SubscriptionOutboundsCatalog interface {
	ListDeviceProxyOutbounds() []SubscriptionOutboundInfo
}

// SubscriptionOutboundInfo describes a subscription selector/urltest outbound
// that can be selected by device-proxy.
type SubscriptionOutboundInfo struct {
	Tag    string
	Label  string
	Detail string
}

// RouterOutboundsCatalog is the narrow contract Service needs from the
// sing-box router service. It exposes outbounds defined in 20-router.json
// (composite groups and user direct-binds) that can be used as device-proxy
// targets. Wired post-construction via SetRouterOutbounds (router service is
// built after deviceproxy.Service).
type RouterOutboundsCatalog interface {
	ListDeviceProxyRouterOutbounds() []RouterOutboundInfo
}

// RouterOutboundInfo describes one router-defined outbound selectable by
// device-proxy. DefaultMember/Members mirror the composite definition in
// 20-router.json (empty for direct-binds) — buildSpec uses them to pick a
// graceful fallback when the router slot is parked and the composite tag
// disappears from the merged config (issue #465).
type RouterOutboundInfo struct {
	Tag           string
	Label         string
	Detail        string
	DefaultMember string   // selector/urltest default, "" когда не задан
	Members       []string // члены композита в порядке объявления
}

// SingboxOperator is the narrow contract Service needs from
// singbox.Operator. Adapter in singbox_adapter.go binds it to the
// real Operator.
type SingboxOperator interface {
	ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error
	ApplyDeviceProxyNoReload(ctx context.Context, spec ExternalSpec) error
	ApplyDeviceProxyInstances(ctx context.Context, specs []ExternalInstanceSpec) error
	SetSelectorDefault(ctx context.Context, selectorTag, memberTag string) error
	GetSelectorActive(ctx context.Context, selectorTag string) (string, error)
	TunnelTags() []string
	IsRunning() bool
}

// availableOutboundTagsProvider is the optional extension the production
// SingboxAdapter implements on top of SingboxOperator: the set of outbound
// tags declared by ENABLED orchestrator slots (the merged-config visibility
// rule prune applies before every reload). nil result = "unknown" — the
// caller keeps legacy behaviour. Optional interface (type assertion, like
// singboxTunnelOutboundsProvider) so existing fakes stay source-compatible.
type availableOutboundTagsProvider interface {
	AvailableOutboundTags() map[string]bool
}

// NDMSInterfaceQuery resolves an NDMS interface id (e.g. "Bridge0") to
// its current primary IPv4 address.
type NDMSInterfaceQuery interface {
	GetInterfaceAddress(ctx context.Context, ndmsID string) (string, error)
}

// ExternalSpec mirrors singbox.DeviceProxySpec but lives in this
// package to keep deviceproxy independent of singbox at the type
// level. The adapter translates.
type ExternalSpec struct {
	Enabled     bool
	ListenAddr  string
	Port        int
	Auth        AuthSpec
	SelectedTag string
	AWGTags     []string
	SBTags      []string
}

// ExternalInstanceSpec mirrors singbox.DeviceProxyInstanceSpec.
type ExternalInstanceSpec struct {
	ID          string
	Enabled     bool
	ListenAddr  string
	Port        int
	Auth        AuthSpec
	SelectedTag string
	AWGTags     []string
	SBTags      []string
}

// TunnelInboundPortsFn returns the set of listen_ports currently used
// by sing-box tunnel-internal inbounds. Used by ValidateConfig to
// detect port conflicts when the user picks a port for the device proxy.
type TunnelInboundPortsFn func() []int

// Service owns the deviceproxy storage + mutation surface. All public
// methods serialise through the embedded mutex.
type Service struct {
	d      Deps
	appLog *logging.ScopedLogger

	mu          sync.Mutex
	tunnelPorts TunnelInboundPortsFn

	routerOutbounds RouterOutboundsCatalog

	// lastDegradedWarn: instanceID → пара «selected→fallback» последней
	// залогированной деградации (issue #465). buildSpec вызывается на каждую
	// регенерацию каждого инстанса (долгий «движок выключен» + churn туннелей
	// → шквал одинаковых Warn); логируем только смену состояния деградации и
	// её снятие. Защищён существующим s.mu (buildSpec всегда под ним).
	lastDegradedWarn map[string]string
}

// ErrOutboundUnavailable is returned by SelectRuntimeOutbound when the caller
// requests a tag that is not in the current list of available outbounds.
var ErrOutboundUnavailable = errors.New("outbound is not available")
var ErrInstanceNotFound = errors.New("instance not found")

// deviceProxyInstanceSelectorTag returns the selector tag for a given instance.
// The default instance uses the shared "device-proxy-selector" tag;
// named instances get their own isolated selector: "device-proxy-<id>-selector".
func deviceProxyInstanceSelectorTag(id string) string {
	if id == "" || id == "default" {
		return "device-proxy-selector"
	}
	return "device-proxy-" + id + "-selector"
}

func NewService(d Deps) *Service {
	return &Service{
		d:      d,
		appLog: logging.NewScopedLogger(d.AppLogger, logging.GroupRouting, logging.SubDeviceProxy),
	}
}

// GetConfig returns the current persisted Config. Defensive copy via Store.
func (s *Service) GetConfig() Config {
	return s.d.Store.Get()
}

// GetSnapshot returns all configured proxy instances.
func (s *Service) GetSnapshot() Snapshot {
	return s.d.Store.Snapshot()
}

// GetInstance returns one configured proxy instance.
func (s *Service) GetInstance(id string) (Instance, bool) {
	return s.d.Store.GetInstance(id)
}

// SaveInstance validates and persists one proxy instance.
// It now applies the full snapshot after saving.
func (s *Service) SaveInstance(ctx context.Context, in Instance) error {
	if in.ID == "" {
		return fmt.Errorf("instance id is empty")
	}
	if in.Name == "" {
		in.Name = in.ID
	}

	s.mu.Lock()
	portFn := s.tunnelPorts
	prev := s.d.Store.Snapshot()
	s.mu.Unlock()

	// Build list of OTHER instances (exclude the one being saved by ID).
	others := make([]Instance, 0, len(prev.Instances))
	for _, ins := range prev.Instances {
		if ins.ID != in.ID {
			others = append(others, ins)
		}
	}

	cfg := instanceToConfig(in)
	if err := validateConfigRaw(cfg, portFn, others); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	prev = s.d.Store.Snapshot()

	if err := s.d.Store.SaveInstance(in); err != nil {
		return err
	}
	if err := s.applyInstancesLocked(ctx); err != nil {
		_ = s.restoreSnapshot(ctx, prev)
		return err
	}
	return nil
}

// DeleteInstance removes one configured proxy instance.
// Storage is always updated; a sing-box apply failure is logged but does
// not roll back the deletion — the user should be able to drop an inbound
// even when the daemon is temporarily unavailable.
// The returned applied flag is false when storage was updated but sing-box
// could not be reloaded yet.
func (s *Service) DeleteInstance(ctx context.Context, id string) (applied bool, err error) {
	if id == "" {
		return false, fmt.Errorf("instance id is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.d.Store.DeleteInstance(id); err != nil {
		return false, err
	}
	// Инстанс удалён — состояние дедупликации Warn'ов деградации больше
	// не нужно (иначе запись висела бы в map навсегда).
	delete(s.lastDegradedWarn, id)
	if err := s.applyInstancesLocked(ctx); err != nil {
		s.appLog.Warn("delete-instance", id, "apply after delete failed: "+err.Error())
		if s.d.Bus != nil {
			s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyConfig, "")
			s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
		}
		return false, nil
	}
	return true, nil
}

// buildInstanceSpec builds an ExternalInstanceSpec from a stored Instance.
func (s *Service) buildInstanceSpec(ctx context.Context, in Instance) (ExternalInstanceSpec, error) {
	cfg := instanceToConfig(in)

	base, err := s.buildSpec(ctx, in.ID, cfg)
	if err != nil {
		return ExternalInstanceSpec{}, err
	}

	return ExternalInstanceSpec{
		ID:          in.ID,
		Enabled:     base.Enabled,
		ListenAddr:  base.ListenAddr,
		Port:        base.Port,
		Auth:        base.Auth,
		SelectedTag: base.SelectedTag,
		AWGTags:     append([]string(nil), base.AWGTags...),
		SBTags:      append([]string(nil), base.SBTags...),
	}, nil
}

// buildSnapshotSpecs converts a Snapshot into a slice of ExternalInstanceSpec.
func (s *Service) buildSnapshotSpecs(ctx context.Context, snap Snapshot) ([]ExternalInstanceSpec, error) {
	specs := make([]ExternalInstanceSpec, 0, len(snap.Instances))
	for _, in := range snap.Instances {
		spec, err := s.buildInstanceSpec(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("build instance %q: %w", in.ID, err)
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// ApplyInstances rebuilds and applies all configured proxy instances.
func (s *Service) ApplyInstances(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.applyInstancesLocked(ctx)
}

func (s *Service) applyInstancesLocked(ctx context.Context) error {
	snap := s.d.Store.Snapshot()

	specs, err := s.buildSnapshotSpecs(ctx, snap)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		if err := s.d.Singbox.ApplyDeviceProxyInstances(ctx, specs); err != nil {
			return fmt.Errorf("apply device proxy instances: %w", err)
		}
	}

	if s.d.Bus != nil {
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyConfig, "")
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
	}
	return nil
}

// restoreSnapshot attempts to roll the Store back to a previous snapshot
// and re-apply the restored snapshot to sing-box so the runtime matches
// storage. Best-effort: errors during rollback (storage or reapply) are
// logged via appLog but not returned — the caller already has the
// original error from the failed mutation.
func (s *Service) restoreSnapshot(ctx context.Context, snap Snapshot) error {
	current := s.d.Store.Snapshot()

	// Delete instances present in current but missing in previous.
	for _, cur := range current.Instances {
		found := false
		for _, prev := range snap.Instances {
			if prev.ID == cur.ID {
				found = true
				break
			}
		}
		if !found {
			if err := s.d.Store.DeleteInstance(cur.ID); err != nil {
				s.appLog.Warn("rollback", "delete", fmt.Sprintf("failed to delete instance %s: %v", cur.ID, err))
				return err
			}
		}
	}

	// Restore (upsert) all instances from the previous snapshot.
	for _, prev := range snap.Instances {
		if err := s.d.Store.SaveInstance(prev); err != nil {
			s.appLog.Warn("rollback", "restore", fmt.Sprintf("failed to restore instance %s: %v", prev.ID, err))
			return err
		}
	}

	// Re-apply restored snapshot to sing-box so runtime matches storage.
	// Best-effort: if this also fails, log via appLog — there's nothing
	// more we can do at this layer, the next Reconcile will retry.
	if err := s.applyInstancesLocked(ctx); err != nil {
		s.appLog.Warn("rollback", "reapply", fmt.Sprintf("post-rollback reapply failed: %v", err))
	}
	return nil
}

// SetTunnelInboundPorts wires a lookup that ValidateConfig uses to
// detect port conflicts with sing-box tunnel inbounds.
func (s *Service) SetTunnelInboundPorts(fn TunnelInboundPortsFn) {
	s.mu.Lock()
	s.tunnelPorts = fn
	s.mu.Unlock()
}

// SetRouterOutbounds wires the router-outbounds catalog after construction.
// The router service is built after deviceproxy.Service in main.go, so it
// cannot be passed via Deps — mirrors SetTunnelInboundPorts.
func (s *Service) SetRouterOutbounds(c RouterOutboundsCatalog) {
	s.mu.Lock()
	s.routerOutbounds = c
	s.mu.Unlock()
}

// withTunnelInboundPorts is a test helper that injects a fixed list.
func (s *Service) withTunnelInboundPorts(ports []int) {
	s.SetTunnelInboundPorts(func() []int { return ports })
}

// validateConfigRaw contains the stateless validation rules that both
// ValidateConfig (public, takes the mutex), validateLocked (internal,
// caller holds the mutex), and SaveInstance share. otherInstances may
// be nil for legacy single-config call sites; multi-instance callers
// pass the snapshot's other instances (excluding the one being saved)
// so port collisions across instances are caught at validation time.
func validateConfigRaw(cfg Config, tunnelPorts TunnelInboundPortsFn, otherInstances []Instance) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return fmt.Errorf("port %d is outside 1024-65535", cfg.Port)
	}
	if tunnelPorts != nil {
		for _, p := range tunnelPorts() {
			if p == cfg.Port {
				return fmt.Errorf("port %d is used by a sing-box tunnel inbound", cfg.Port)
			}
		}
	}
	for _, other := range otherInstances {
		if other.Enabled && other.Port == cfg.Port {
			return fmt.Errorf("port %d is used by another device-proxy instance %q", cfg.Port, other.ID)
		}
	}
	if cfg.Auth.Enabled {
		if cfg.Auth.Username == "" {
			return fmt.Errorf("auth enabled but username is empty")
		}
		if cfg.Auth.Password == "" {
			return fmt.Errorf("auth enabled but password is empty")
		}
	}
	if !cfg.ListenAll && cfg.ListenInterface == "" {
		return fmt.Errorf("listen set to specific interface but interface is empty")
	}
	return nil
}

// ValidateConfig checks the user-supplied Config for obvious errors
// before it is persisted. Errors wrap validation context so the API
// layer can surface them as 400 responses with meaningful messages.
func (s *Service) ValidateConfig(cfg Config) error {
	s.mu.Lock()
	portFn := s.tunnelPorts
	s.mu.Unlock()
	return validateConfigRaw(cfg, portFn, nil)
}

// SaveConfig validates, applies to sing-box, and persists cfg.
// Transactional on the pre-apply phase; post-apply errors are logged
// but do not roll back persisted storage.
//
// Reload decision: if the diff between old and new is a SelectedOutbound-
// only change (and both states are Enabled, and sing-box is running), the
// process is NOT reloaded — we surgically rewrite config.json so the new
// selector.default takes effect on next reload/restart, and the current
// live selector.now (possibly set by a hot-switch) stays untouched.
// Any other change (port, listen, auth, enabled toggle) requires a
// full reload. When sing-box is not running the full apply path is always
// taken so the cold-start safety net (startAndWait) fires normally.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateLocked(cfg); err != nil {
		return err
	}

	oldCfg := s.d.Store.Get()

	spec, err := s.buildSpec(ctx, "default", cfg)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		// No-reload path only makes sense when the daemon is actually up —
		// otherwise there's no live selector.now to preserve, AND the
		// reload path includes a cold-start safety net that the no-reload
		// path deliberately skips. Require both conditions.
		if onlySelectedOutboundChanged(oldCfg, cfg) && s.d.Singbox.IsRunning() {
			if err := s.d.Singbox.ApplyDeviceProxyNoReload(ctx, spec); err != nil {
				return fmt.Errorf("apply to singbox (no-reload): %w", err)
			}
		} else {
			if err := s.d.Singbox.ApplyDeviceProxy(ctx, spec); err != nil {
				return fmt.Errorf("apply to singbox: %w", err)
			}
		}
	}

	if err := s.d.Store.Save(cfg); err != nil {
		return fmt.Errorf("persist storage: %w", err)
	}

	switch {
	case oldCfg.Enabled && !cfg.Enabled:
		s.appLog.Info("disable", cfg.SelectedOutbound, "Device proxy disabled")
	case !oldCfg.Enabled && cfg.Enabled:
		s.appLog.Info("enable", cfg.SelectedOutbound, fmt.Sprintf("Device proxy enabled on :%d via %s", cfg.Port, cfg.SelectedOutbound))
	case onlySelectedOutboundChanged(oldCfg, cfg):
		s.appLog.Info("change-outbound", cfg.SelectedOutbound, fmt.Sprintf("Device proxy outbound switched to %s", cfg.SelectedOutbound))
	default:
		s.appLog.Info("update", cfg.SelectedOutbound, fmt.Sprintf("Device proxy config updated (port=%d outbound=%s)", cfg.Port, cfg.SelectedOutbound))
	}

	if s.d.Bus != nil {
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyConfig, "")
		// A default-only change also shifts what the runtime store would
		// derive "temporarily" against, so invalidate both.
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
	}
	return nil
}

// ForceApply re-applies the currently-persisted Config to sing-box via
// the full reload path, regardless of whether anything changed since
// last Save. Used by the "Применить сейчас" UI action when the user
// has saved a new default via the no-reload surgical path and wants
// the live selector.now to snap to that default immediately — the
// reload causes sing-box to reinit selector.now from the updated
// config.json's selector.default.
//
// Client SOCKS connections are interrupted during reload; that trade-off
// is explicit since the user clicked a reload button.
func (s *Service) ForceApply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.d.Store.Get()

	if cfg.Enabled && s.d.Singbox != nil {
		active, err := s.d.Singbox.GetSelectorActive(ctx, "device-proxy-selector")
		if err != nil {
			return fmt.Errorf("force apply read active selector: %w", err)
		}
		if active != "" && active != cfg.SelectedOutbound {
			cfg.SelectedOutbound = active
			if err := s.d.Store.Save(cfg); err != nil {
				return fmt.Errorf("force apply persist active selector: %w", err)
			}
		}
	}

	spec, err := s.buildSpec(ctx, "default", cfg)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		if err := s.d.Singbox.ApplyDeviceProxy(ctx, spec); err != nil {
			return fmt.Errorf("force apply: %w", err)
		}

		if cfg.Enabled && cfg.SelectedOutbound != "" {
			if err := s.d.Singbox.SetSelectorDefault(ctx, "device-proxy-selector", cfg.SelectedOutbound); err != nil {
				return fmt.Errorf("force apply selector: %w", err)
			}
		}
	}

	if s.d.Bus != nil {
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyConfig, "")
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
	}
	return nil
}

// onlySelectedOutboundChanged returns true when the only field that
// differs between old and new is SelectedOutbound AND both states are
// Enabled. Used by SaveConfig to decide whether to skip the sing-box
// reload (live selector.now must be preserved through the save).
func onlySelectedOutboundChanged(oldCfg, newCfg Config) bool {
	if !oldCfg.Enabled || !newCfg.Enabled {
		return false
	}
	copyNew := newCfg
	copyNew.SelectedOutbound = oldCfg.SelectedOutbound
	return copyNew == oldCfg
}

// validateLocked is the mutex-holding variant used by SaveConfig to
// avoid a nested Lock(). ValidateConfig (the public form) still works
// standalone for API-layer input checking.
func (s *Service) validateLocked(cfg Config) error {
	return validateConfigRaw(cfg, s.tunnelPorts, nil)
}

// buildSpec собирает ExternalSpec для одного инстанса. instanceID нужен
// только для дедупликации логов деградации (map на Service); legacy-пути
// одиночного конфига (SaveConfig/ForceApply) передают "default".
// Вызывается строго под s.mu.
func (s *Service) buildSpec(ctx context.Context, instanceID string, cfg Config) (ExternalSpec, error) {
	spec := ExternalSpec{
		Enabled:     cfg.Enabled,
		Port:        cfg.Port,
		Auth:        cfg.Auth,
		SelectedTag: cfg.SelectedOutbound,
	}
	if cfg.ListenAll {
		spec.ListenAddr = "0.0.0.0"
	} else {
		if s.d.NDMSQuery == nil {
			return spec, fmt.Errorf("cannot resolve listen interface: NDMS query unavailable")
		}
		addr, err := s.d.NDMSQuery.GetInterfaceAddress(ctx, cfg.ListenInterface)
		if err != nil || addr == "" {
			return spec, fmt.Errorf("resolve listen interface %q: %w", cfg.ListenInterface, err)
		}
		spec.ListenAddr = addr
	}

	sbTags, awgTags, routerInfos, avail := s.collectSelectorMembers(ctx, s.routerOutbounds, s.d.Singbox)
	spec.SBTags = sbTags
	spec.AWGTags = awgTags

	// Graceful degradation (issue #465): если выбранный выход — router-
	// композит, отсутствующий сейчас в merged-конфиге (слот 20-router
	// припаркован — движок маршрутизации выключен), подставляем в спек
	// default-член композита (намерение пользователя), а не оставляем
	// висячую ссылку, которую prune оркестратора вырезал бы молча и
	// sing-box увёл бы трафик в произвольный выживший член. Хранилище
	// (SelectedOutbound) НЕ трогаем: при включении движка регенерация
	// вернёт композит на место.
	deg := degradationFor(cfg.SelectedOutbound, spec.SBTags, spec.AWGTags, routerInfos, avail)
	switch {
	case deg != nil:
		spec.SelectedTag = deg.FallbackTag
		s.warnDegradationLocked(instanceID, deg)
	case avail != nil && !selectedTagEmittable(cfg.SelectedOutbound, spec.SBTags, spec.AWGTags):
		// Недоступный НЕ-router тег (например, subscription-selector при
		// припаркованном слоте подписок): default не эмитим — иначе
		// singbox/config.go записал бы в слот 30 висячий "default", который
		// prune вычищал бы на каждом reload, ломая инвариант «prune никогда
		// не трогает свежий слот 30». Selector падает на direct; Reconcile
		// выключит инстанс при следующем событии.
		spec.SelectedTag = ""
		// Не «снова доступен» — состояние дедупликации снимаем молча.
		delete(s.lastDegradedWarn, instanceID)
	default:
		s.clearDegradationLocked(instanceID)
	}
	return spec, nil
}

// selectedTagEmittable reports whether the selected tag will actually be
// present among the emitted selector members. "" и "direct" эмитимы всегда
// (direct — встроенный член каждого селектора).
func selectedTagEmittable(selected string, sbTags, awgTags []string) bool {
	if selected == "" || selected == "direct" {
		return true
	}
	for _, t := range sbTags {
		if t == selected {
			return true
		}
	}
	for _, t := range awgTags {
		if t == selected {
			return true
		}
	}
	return false
}

// degradedWarnSep разделяет selected и fallback в значении lastDegradedWarn.
// NUL не встречается в тегах sing-box.
const degradedWarnSep = "\x00"

// warnDegradationLocked логирует деградацию один раз на состояние: повторный
// buildSpec с той же парой selected→fallback молчит. Caller держит s.mu.
func (s *Service) warnDegradationLocked(instanceID string, deg *OutboundDegradation) {
	key := deg.SelectedTag + degradedWarnSep + deg.FallbackTag
	if s.lastDegradedWarn[instanceID] == key {
		return
	}
	if s.lastDegradedWarn == nil {
		s.lastDegradedWarn = make(map[string]string)
	}
	s.lastDegradedWarn[instanceID] = key
	s.appLog.Warn("degraded-outbound", deg.SelectedTag,
		fmt.Sprintf("Выход %q недоступен в текущем конфиге (слот-источник выключен) — прокси временно через %q", deg.SelectedTag, deg.FallbackTag))
}

// clearDegradationLocked снимает состояние деградации и логирует
// восстановление (Info) — только если деградация была. Caller держит s.mu.
func (s *Service) clearDegradationLocked(instanceID string) {
	prev, ok := s.lastDegradedWarn[instanceID]
	if !ok {
		return
	}
	delete(s.lastDegradedWarn, instanceID)
	selected, _, _ := strings.Cut(prev, degradedWarnSep)
	s.appLog.Info("degraded-outbound", selected,
		fmt.Sprintf("Выход %q снова доступен — прокси снова идёт через него", selected))
}

// collectSelectorMembers gathers the member-tag universe for device-proxy
// selectors: sing-box tunnels + subscription selectors + router-defined
// outbounds (SB side) and AWG tags, already filtered through the enabled-
// slot availability oracle when it is known (avail != nil). routerInfos is
// returned UNfiltered — the fallback resolver needs composite definitions
// even (especially) when their tags are unavailable. catalog/sb are passed
// explicitly: buildSpec calls under s.mu with the service fields, runtime
// paths snapshot them first.
func (s *Service) collectSelectorMembers(ctx context.Context, catalog RouterOutboundsCatalog, sb SingboxOperator) (sbTags, awgTags []string, routerInfos []RouterOutboundInfo, avail map[string]bool) {
	if p, ok := sb.(availableOutboundTagsProvider); ok {
		avail = p.AvailableOutboundTags()
	}

	// Sing-box tunnel tags
	if sb != nil {
		sbTags = append(sbTags, sb.TunnelTags()...)
	}

	// Sing-box subscription selector/urltest tags
	if s.d.SubscriptionOutbounds != nil {
		for _, t := range s.d.SubscriptionOutbounds.ListDeviceProxyOutbounds() {
			sbTags = append(sbTags, t.Tag)
		}
	}

	// Router-defined outbounds (20-router.json) — composite groups and
	// user direct-binds, exposed as selectable members.
	if catalog != nil {
		routerInfos = catalog.ListDeviceProxyRouterOutbounds()
		for _, ro := range routerInfos {
			sbTags = append(sbTags, ro.Tag)
		}
	}

	// AWG tags — single source of truth is the awgoutbounds package,
	// which enumerates managed + system tunnels and emits canonical
	// awg-{id} / awg-sys-{id} tags. We just collect the tags.
	if s.d.AWGOutbounds != nil {
		tags, err := s.d.AWGOutbounds.ListTags(ctx)
		if err == nil {
			for _, t := range tags {
				awgTags = append(awgTags, t.Tag)
			}
		}
	}

	// Никогда не эмитим член селектора, которого нет в merged-конфиге:
	// sing-box отвергает селектор с неизвестным членом, а prune оркестратора
	// вырезал бы его молча уже ПОСЛЕ записи слота (issue #465).
	if avail != nil {
		sbTags = keepAvailableTags(sbTags, avail)
		awgTags = keepAvailableTags(awgTags, avail)
	}
	return sbTags, awgTags, routerInfos, avail
}

// keepAvailableTags filters tags through the enabled-slot oracle.
func keepAvailableTags(tags []string, avail map[string]bool) []string {
	kept := make([]string, 0, len(tags))
	for _, t := range tags {
		if avail[t] {
			kept = append(kept, t)
		}
	}
	return kept
}

// OutboundDegradation describes a generation-time substitution: the
// user-selected outbound tag is currently absent from the merged config
// (its slot is parked), so slot 30's selector default was pointed at
// FallbackTag instead. Present only while the source slot stays disabled;
// the stored SelectedOutbound is never modified.
type OutboundDegradation struct {
	SelectedTag string // выбранный пользователем тег, недоступный сейчас
	FallbackTag string // через какой член фактически идёт трафик
}

// degradationFor decides whether the FIX for issue #465 applies: selected
// references a router composite whose tag is not among the members we can
// emit. sbTags/awgTags must already be availability-filtered. Non-router
// unknown tags are left alone — there is no definition to recover intent
// from (Reconcile disables such instances; prune protects the daemon).
func degradationFor(selected string, sbTags, awgTags []string, routerInfos []RouterOutboundInfo, avail map[string]bool) *OutboundDegradation {
	if avail == nil || selected == "" || selected == "direct" {
		return nil
	}
	members := map[string]bool{"direct": true}
	for _, t := range sbTags {
		members[t] = true
	}
	for _, t := range awgTags {
		members[t] = true
	}
	if members[selected] {
		return nil
	}
	byTag := make(map[string]RouterOutboundInfo, len(routerInfos))
	for _, ro := range routerInfos {
		byTag[ro.Tag] = ro
	}
	if _, isRouter := byTag[selected]; !isRouter {
		return nil
	}
	return &OutboundDegradation{
		SelectedTag: selected,
		FallbackTag: resolveOutboundFallback(selected, byTag, members),
	}
}

// resolveOutboundFallback walks the unavailable composite's definition to
// find the member the user's intent degrades to: the composite's default
// member first, then the remaining members in declaration order; a member
// that is itself an unavailable router composite is resolved recursively
// (visited-set guards cycles). Last resort — "direct".
func resolveOutboundFallback(selected string, byTag map[string]RouterOutboundInfo, members map[string]bool) string {
	visited := map[string]bool{selected: true}
	var walk func(tag string) (string, bool)
	walk = func(tag string) (string, bool) {
		info, ok := byTag[tag]
		if !ok {
			return "", false
		}
		candidates := make([]string, 0, len(info.Members)+1)
		if info.DefaultMember != "" {
			candidates = append(candidates, info.DefaultMember)
		}
		candidates = append(candidates, info.Members...)
		for _, c := range candidates {
			if c == "" || visited[c] {
				continue
			}
			visited[c] = true
			if members[c] {
				return c, true
			}
			if fb, ok := walk(c); ok {
				return fb, true
			}
		}
		return "", false
	}
	if fb, ok := walk(selected); ok {
		return fb
	}
	return "direct"
}

// runtimeDegradation recomputes the buildSpec substitution for display:
// which slot-30 default the generator emits right now for selected. Used
// by the runtime-state endpoints so the UI can show «выход недоступен —
// трафик через Y» without trusting the live selector.now. Takes s.mu
// briefly to snapshot collaborators; safe to call without the lock.
func (s *Service) runtimeDegradation(ctx context.Context, selected string) *OutboundDegradation {
	// Быстрый выход ДО collectSelectorMembers: для ""/direct деградация
	// невозможна (direct — встроенный член каждого селектора), а runtime-
	// endpoint'ы UI опрашивает постоянно — гонять оракул слотов и каталоги
	// (туннели/подписки/роутер/awg) на каждый poll дорого на MIPS-роутерах.
	if selected == "" || selected == "direct" {
		return nil
	}
	s.mu.Lock()
	catalog := s.routerOutbounds
	sb := s.d.Singbox
	s.mu.Unlock()

	sbTags, awgTags, routerInfos, avail := s.collectSelectorMembers(ctx, catalog, sb)
	return degradationFor(selected, sbTags, awgTags, routerInfos, avail)
}

// RuntimeState is the UI-facing snapshot of the selector's live state.
// Not persisted; returned on demand.
//
// DegradedOutbound/FallbackTag surface the issue-#465 degradation: the
// stored SelectedOutbound (== DefaultTag) is a composite whose slot is
// parked, so the generated selector actually defaults to FallbackTag.
// Both empty when there is no degradation.
type RuntimeState struct {
	Alive            bool   `json:"alive"`
	ActiveTag        string `json:"activeTag"`
	DefaultTag       string `json:"defaultTag"`
	DegradedOutbound string `json:"degradedOutbound,omitempty"`
	FallbackTag      string `json:"fallbackTag,omitempty"`
}

// InstanceIPCheckResult contains the direct WAN IP and IP observed through
// a concrete device-proxy instance.
type InstanceIPCheckResult struct {
	DirectIP  string `json:"directIp"`
	ProxyIP   string `json:"proxyIp"`
	IPChanged bool   `json:"ipChanged"`
	Service   string `json:"service"`
}

// GetRuntimeState returns the current selector.now from Clash API
// (empty if sing-box is down) plus the persisted default for
// convenient client-side diffing.
func (s *Service) GetRuntimeState(ctx context.Context) RuntimeState {
	s.mu.Lock()
	defaultTag := s.d.Store.Get().SelectedOutbound
	sb := s.d.Singbox
	s.mu.Unlock()

	state := RuntimeState{DefaultTag: defaultTag}
	if deg := s.runtimeDegradation(ctx, defaultTag); deg != nil {
		state.DegradedOutbound = deg.SelectedTag
		state.FallbackTag = deg.FallbackTag
	}
	if sb == nil || !sb.IsRunning() {
		return state
	}
	state.Alive = true
	if active, err := sb.GetSelectorActive(ctx, "device-proxy-selector"); err == nil {
		state.ActiveTag = active
	}
	return state
}

// GetInstanceRuntimeState returns the current selector.now for a specific
// instance (empty if sing-box is down) plus the persisted default for
// that instance.
func (s *Service) GetInstanceRuntimeState(ctx context.Context, id string) (RuntimeState, error) {
	if id == "" {
		return RuntimeState{}, fmt.Errorf("instance id is empty")
	}

	s.mu.Lock()
	in, ok := s.d.Store.GetInstance(id)
	sb := s.d.Singbox
	s.mu.Unlock()

	if !ok {
		return RuntimeState{}, fmt.Errorf("instance %q not found", id)
	}

	state := RuntimeState{DefaultTag: in.SelectedOutbound}
	if deg := s.runtimeDegradation(ctx, in.SelectedOutbound); deg != nil {
		state.DegradedOutbound = deg.SelectedTag
		state.FallbackTag = deg.FallbackTag
	}
	if sb == nil || !sb.IsRunning() {
		return state, nil
	}

	state.Alive = true
	if active, err := sb.GetSelectorActive(ctx, deviceProxyInstanceSelectorTag(id)); err == nil {
		state.ActiveTag = active
	}
	return state, nil
}

// Outbound describes one selectable proxy target exposed to the UI.
type Outbound struct {
	Tag    string `json:"tag"`
	Kind   string `json:"kind"` // "direct" | "singbox" | "subscription" | "awg" | "router"
	Label  string `json:"label"`
	Detail string `json:"detail"` // extra info for UI (kernel iface, protocol, etc)
}

// TunnelOutboundInfo describes one standalone sing-box tunnel outbound
// for device-proxy selector UI metadata.
type TunnelOutboundInfo struct {
	Tag      string
	Protocol string
	Server   string
	Port     int
}

type singboxTunnelOutboundsProvider interface {
	TunnelOutbounds() []TunnelOutboundInfo
}

// ListOutbounds returns all members that can be assigned as the
// selector's active outbound — direct + every sb-tunnel tag + every
// AWG tunnel's awg-<id> tag. Order is deterministic: direct first,
// then sb by name, then AWG by id.
func (s *Service) ListOutbounds(ctx context.Context) []Outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listOutboundsLocked(ctx)
}

func (s *Service) listOutboundsLocked(ctx context.Context) []Outbound {
	out := []Outbound{{Tag: "direct", Kind: "direct", Label: "Direct (WAN)", Detail: "без туннеля"}}

	if s.d.Singbox != nil {
		tags := append([]string(nil), s.d.Singbox.TunnelTags()...)
		sort.Strings(tags)

		byTag := map[string]TunnelOutboundInfo{}
		if p, ok := s.d.Singbox.(singboxTunnelOutboundsProvider); ok {
			for _, ti := range p.TunnelOutbounds() {
				byTag[ti.Tag] = ti
			}
		}

		for _, tag := range tags {
			detail := ""
			if ti, ok := byTag[tag]; ok {
				switch {
				case ti.Protocol != "" && ti.Server != "" && ti.Port > 0:
					detail = strings.ToUpper(ti.Protocol) + " · " + ti.Server + ":" + fmt.Sprintf("%d", ti.Port)
				case ti.Server != "" && ti.Port > 0:
					detail = ti.Server + ":" + fmt.Sprintf("%d", ti.Port)
				case ti.Protocol != "":
					detail = strings.ToUpper(ti.Protocol)
				}
			}
			out = append(out, Outbound{Tag: tag, Kind: "singbox", Label: tag, Detail: detail})
		}
	}

	if s.d.SubscriptionOutbounds != nil {
		subs := append([]SubscriptionOutboundInfo(nil), s.d.SubscriptionOutbounds.ListDeviceProxyOutbounds()...)
		sort.Slice(subs, func(i, j int) bool {
			return subs[i].Label < subs[j].Label
		})
		for _, sub := range subs {
			out = append(out, Outbound{
				Tag:    sub.Tag,
				Kind:   "subscription",
				Label:  sub.Label,
				Detail: sub.Detail,
			})
		}
	}

	if s.d.AWGOutbounds != nil {
		tags, err := s.d.AWGOutbounds.ListTags(ctx)
		if err == nil {
			for _, t := range tags {
				out = append(out, Outbound{
					Tag:    t.Tag,
					Kind:   "awg",
					Label:  t.Label,
					Detail: t.Iface,
				})
			}
		}
	}

	if s.routerOutbounds != nil {
		for _, ro := range s.routerOutbounds.ListDeviceProxyRouterOutbounds() {
			out = append(out, Outbound{
				Tag:    ro.Tag,
				Kind:   "router",
				Label:  ro.Label,
				Detail: ro.Detail,
			})
		}
	}
	return out
}

// SelectRuntimeOutbound switches the live selector.now via Clash API.
// No storage write. No config.json write. The choice is ephemeral —
// sing-box reload or restart reverts to the persisted default.
//
// Errors:
//   - ErrOutboundUnavailable — tag is not in the currently-available list.
//   - singbox.ErrSingboxNotRunning — bubbled up from the operator when
//     the daemon is down, so API layer can map to 409.
func (s *Service) SelectRuntimeOutbound(ctx context.Context, tag string) error {
	s.mu.Lock()
	available := s.listOutboundsLocked(ctx)
	sb := s.d.Singbox
	bus := s.d.Bus
	s.mu.Unlock()

	found := false
	for _, ob := range available {
		if ob.Tag == tag {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrOutboundUnavailable, tag)
	}

	if sb == nil {
		return fmt.Errorf("singbox operator unavailable")
	}
	if err := sb.SetSelectorDefault(ctx, "device-proxy-selector", tag); err != nil {
		s.appLog.Warn("select-runtime", tag, fmt.Sprintf("hot-switch failed: %v", err))
		return err
	}
	s.appLog.Info("select-runtime", tag, "Device proxy hot-switched outbound")

	// Hot-switch changed runtime.activeTag — give the frontend SSE
	// fast-path so the "Активный туннель" card updates sub-second,
	// without waiting for the 5s runtime polling tick.
	if bus != nil {
		bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
	}
	return nil
}

// SelectInstanceRuntimeOutbound switches the live selector.now for a
// specific instance via Clash API. No storage write. No config.json
// write. The choice is ephemeral — sing-box reload/restart reverts to
// the instance's persisted default.
//
// Errors:
//   - ErrOutboundUnavailable — tag is not in the currently-available list.
//   - singbox.ErrSingboxNotRunning — bubbled up from the operator when
//     the daemon is down.
func (s *Service) SelectInstanceRuntimeOutbound(ctx context.Context, id, tag string) error {
	if id == "" {
		return fmt.Errorf("instance id is empty")
	}

	s.mu.Lock()
	_, ok := s.d.Store.GetInstance(id)
	available := s.listOutboundsLocked(ctx)
	sb := s.d.Singbox
	bus := s.d.Bus
	s.mu.Unlock()

	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}

	found := false
	for _, ob := range available {
		if ob.Tag == tag {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrOutboundUnavailable, tag)
	}

	if sb == nil {
		return fmt.Errorf("singbox operator unavailable")
	}

	selectorTag := deviceProxyInstanceSelectorTag(id)
	if err := sb.SetSelectorDefault(ctx, selectorTag, tag); err != nil {
		s.appLog.Warn("select-runtime", tag, fmt.Sprintf("hot-switch failed for instance %s: %v", id, err))
		return err
	}
	s.appLog.Info("select-runtime", tag, fmt.Sprintf("Device proxy instance %s hot-switched outbound", id))

	if bus != nil {
		bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
	}
	return nil
}

// Reconcile is the idempotent rebuild that verifies every enabled instance's
// selected outbound is still available. Instances with missing targets are
// disabled, then the full snapshot is applied to sing-box.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := s.d.Store.Snapshot()
	available := s.listOutboundsLocked(ctx)

	availableByTag := make(map[string]struct{}, len(available))
	for _, ob := range available {
		availableByTag[ob.Tag] = struct{}{}
	}

	changed := false
	missingTags := make([]string, 0)

	for i := range snap.Instances {
		in := &snap.Instances[i]
		if !in.Enabled || in.SelectedOutbound == "" {
			continue
		}
		if _, ok := availableByTag[in.SelectedOutbound]; ok {
			continue
		}

		missingTags = append(missingTags, in.SelectedOutbound)
		in.Enabled = false
		in.SelectedOutbound = ""
		changed = true

		// Persist the changed instance immediately.
		if err := s.d.Store.SaveInstance(*in); err != nil {
			return fmt.Errorf("persist after missing target: %w", err)
		}
	}

	if changed && s.d.Bus != nil {
		for _, wasTag := range missingTags {
			s.d.Bus.Publish("deviceproxy:missing-target", map[string]string{"wasTag": wasTag})
		}
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyConfig, "")
		s.d.Bus.PublishInvalidated(events.ResourceDeviceProxyRuntime, "")
	}

	snap = s.d.Store.Snapshot()

	specs, err := s.buildSnapshotSpecs(ctx, snap)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		if err := s.d.Singbox.ApplyDeviceProxyInstances(ctx, specs); err != nil {
			return fmt.Errorf("apply specs: %w", err)
		}
	}

	return nil
}

// BridgeChoice describes a single Bridge interface for the inbound
// listen address dropdown.
type BridgeChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	IP    string `json:"ip"`
}

// ListenChoicesResult aggregates the data the UI needs to render the
// inbound settings form.
type ListenChoicesResult struct {
	LanIP          string         `json:"lanIP"`
	Bridges        []BridgeChoice `json:"bridges"`
	SingboxRunning bool           `json:"singboxRunning"`
}

// bridgeLister is the optional interface NDMSAdapter implements so that
// ListenChoices can enumerate Bridge interfaces. Guarded by a type
// assertion so the rest of NDMSInterfaceQuery is unchanged.
type bridgeLister interface {
	ListBridges(ctx context.Context) ([]BridgeChoice, error)
}

// ListenChoices returns the bridge list, LAN IP, and singbox-running
// status needed by the frontend inbound settings form.
func (s *Service) ListenChoices(ctx context.Context) (ListenChoicesResult, error) {
	res := ListenChoicesResult{Bridges: []BridgeChoice{}}
	if s.d.Singbox != nil {
		res.SingboxRunning = s.d.Singbox.IsRunning()
	}
	if lister, ok := s.d.NDMSQuery.(bridgeLister); ok {
		bridges, err := lister.ListBridges(ctx)
		if err == nil {
			res.Bridges = bridges
			for _, b := range bridges {
				if b.ID == "Bridge0" && b.IP != "" {
					res.LanIP = b.IP
					break
				}
			}
			if res.LanIP == "" {
				for _, b := range bridges {
					if b.IP != "" {
						res.LanIP = b.IP
						break
					}
				}
			}
		}
	}
	return res, nil
}

// SubscribeBus registers event handlers that trigger Reconcile. Call
// once at startup. Returns an unsubscribe function to call during
// shutdown.
func (s *Service) SubscribeBus(ctx context.Context) func() {
	if s.d.Bus == nil {
		return func() {}
	}
	_, ch, unsub := s.d.Bus.Subscribe()
	go func() {
		for ev := range ch {
			if ev.Type != events.EventResourceInvalidated &&
				ev.Type != "singbox:tunnels-changed" &&
				ev.Type != "singbox-router:outbounds" {
				continue
			}
			if ev.Type == events.EventResourceInvalidated {
				// Only react to invalidations that change our child list.
				payload, ok := ev.Data.(events.ResourceInvalidatedEvent)
				if !ok {
					continue
				}
				if payload.Resource != "tunnels" &&
					payload.Resource != "singbox.tunnels" &&
					payload.Resource != "singbox.subscriptions" {
					continue
				}
			}
			if err := s.Reconcile(ctx); err != nil {
				// Reconcile failure is non-fatal at the subscriber level;
				// the user-facing flow already has its own error path.
				// No logger is wired on Service yet (would be added in a
				// future task); silent swallow matches the project's other
				// similar subscribers.
				_ = err
			}
		}
	}()
	return unsub
}

// HasSelectorReference reports whether any persisted proxy instance references
// the given outbound tag as its user-chosen SelectedOutbound default.
// Used by tunnel/subscription delete flows to refuse deletions that would
// orphan an explicit proxy choice.
func (s *Service) HasSelectorReference(tag string) bool {
	if tag == "" {
		return false
	}

	s.mu.Lock()
	snap := s.d.Store.Snapshot()
	s.mu.Unlock()

	for _, in := range snap.Instances {
		if in.SelectedOutbound == tag {
			return true
		}
	}
	return false
}

const (
	instanceIPDirectTimeout = 10 * time.Second
	instanceIPProxyTimeout  = 20 * time.Second
	instanceIPMaxTimeSec    = 4
)

var instanceIPCheckServices = []string{
	"https://api.ipify.org",
	"https://wtfismyip.com/text",
	"https://ipinfo.io/ip",
}

func parseIPResult(stdout string) (string, bool) {
	ip := strings.TrimSpace(stdout)
	return ip, net.ParseIP(ip) != nil
}

func fetchIPViaHTTP(ctx context.Context, serviceURL string, proxyURL string) (string, string, error) {
	if serviceURL != "" {
		res, err := httpclient.DefaultClient.Do(ctx, httpclient.CallConfig{
			URL:      serviceURL,
			ProxyURL: proxyURL,
			MaxTime:  instanceIPMaxTimeSec * time.Second,
		})
		if err != nil {
			return "", "", fmt.Errorf("%s: %w", serviceURL, err)
		}
		if ip, ok := parseIPResult(res.Body); ok {
			return ip, serviceURL, nil
		}
		return "", "", fmt.Errorf("%s: invalid response %q", serviceURL, strings.TrimSpace(res.Body))
	}

	var lastErr error
	for _, svcURL := range instanceIPCheckServices {
		ip, usedService, err := fetchIPViaHTTP(ctx, svcURL, proxyURL)
		if err != nil {
			lastErr = err
			continue
		}
		return ip, usedService, nil
	}
	if lastErr != nil {
		return "", "", lastErr
	}
	return "", "", fmt.Errorf("all IP services failed")
}

// CheckInstanceExternalIP resolves external IP through the selected proxy
// instance and compares it to direct WAN IP.
func (s *Service) CheckInstanceExternalIP(ctx context.Context, id, serviceURL string) (InstanceIPCheckResult, error) {
	if id == "" {
		return InstanceIPCheckResult{}, fmt.Errorf("instance id is empty")
	}

	s.mu.Lock()
	in, ok := s.d.Store.GetInstance(id)
	s.mu.Unlock()
	if !ok {
		return InstanceIPCheckResult{}, fmt.Errorf("%w: %q", ErrInstanceNotFound, id)
	}
	if !in.Enabled {
		return InstanceIPCheckResult{}, fmt.Errorf("instance %q is disabled", id)
	}

	proxyHost := "127.0.0.1"
	if !in.ListenAll {
		if s.d.NDMSQuery == nil {
			return InstanceIPCheckResult{}, fmt.Errorf("cannot resolve listen interface: NDMS query unavailable")
		}
		addr, err := s.d.NDMSQuery.GetInterfaceAddress(ctx, in.ListenInterface)
		if err != nil || addr == "" {
			return InstanceIPCheckResult{}, fmt.Errorf("resolve listen interface %q: %w", in.ListenInterface, err)
		}
		proxyHost = addr
	}

	proxyURL := &url.URL{
		Scheme: "socks5h",
		Host:   fmt.Sprintf("%s:%d", proxyHost, in.Port),
	}
	if in.Auth.Enabled {
		proxyURL.User = url.UserPassword(in.Auth.Username, in.Auth.Password)
	}

	directCtx, directCancel := context.WithTimeout(ctx, instanceIPDirectTimeout)
	defer directCancel()
	directIP, _, err := fetchIPViaHTTP(directCtx, serviceURL, "")
	if err != nil {
		return InstanceIPCheckResult{}, fmt.Errorf("failed to get WAN IP: %w", err)
	}

	proxyCtx, proxyCancel := context.WithTimeout(ctx, instanceIPProxyTimeout)
	defer proxyCancel()
	proxyIP, usedService, err := fetchIPViaHTTP(proxyCtx, serviceURL, proxyURL.String())
	if err != nil {
		return InstanceIPCheckResult{}, fmt.Errorf("failed to get IP through proxy instance %q: %w", id, err)
	}

	return InstanceIPCheckResult{
		DirectIP:  directIP,
		ProxyIP:   proxyIP,
		IPChanged: directIP != proxyIP,
		Service:   usedService,
	}, nil
}

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/singbox/heavyop"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type Service interface {
	Reconcile(ctx context.Context) error
	// SwitchRoutingMode orchestrates a routing-mode transition (off↔tproxy↔
	// fakeip-tun) with directional fail-closed rollback and progress events.
	SwitchRoutingMode(ctx context.Context, target string) error
	GetStatus(ctx context.Context) (Status, error)
	GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error)
	UpdateSettings(ctx context.Context, s storage.SingboxRouterSettings) error

	// ListWANInterfaces returns all router WAN interfaces (no up/down
	// filtering) for the WAN-binding picker. Pairs with
	// SingboxRouterSettings.WANInterface, which stores the kernel
	// system-name from this list.
	ListWANInterfaces(ctx context.Context) ([]WANInterfaceInfo, error)

	// ListBindableInterfaces returns interfaces a user can bind a direct
	// outbound to (all interfaces minus auto-managed AWG/WG ones).
	ListBindableInterfaces(ctx context.Context) ([]WANInterfaceInfo, error)
	ListAllBindableInterfaces(ctx context.Context) ([]WANInterfaceInfo, error)

	// ListIngressEligibleInterfaces returns interfaces eligible for
	// sing-box ingress-scope (bindable minus WAN minus LAN bridges).
	ListIngressEligibleInterfaces(ctx context.Context) ([]WANInterfaceInfo, error)

	// PolicyTunNATPreview returns the router segments with their current NAT
	// mode — the editable "what will change" preview behind the policy-tun
	// source-preserve toggle.
	PolicyTunNATPreview(ctx context.Context) (NATPreview, error)

	SetRouteFinal(ctx context.Context, tag string) error

	ListRules(ctx context.Context) ([]Rule, error)
	AddRule(ctx context.Context, rule Rule) error
	UpdateRule(ctx context.Context, index int, rule Rule) error
	DeleteRule(ctx context.Context, index int) error
	// BulkSetRuleOutbound sets Outbound on every rule at the given indices in
	// a single config write. See ServiceImpl.BulkSetRuleOutbound for the
	// validation contract.
	BulkSetRuleOutbound(ctx context.Context, indices []int, outbound string) error
	MoveRule(ctx context.Context, from, to int) error

	ListRuleSets(ctx context.Context) ([]RuleSet, error)
	AddRuleSet(ctx context.Context, rs RuleSet) error
	UpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error
	// BulkSetRuleSetDetour sets DownloadDetour on every rule set with a tag in
	// the given list, in a single config write. See
	// ServiceImpl.BulkSetRuleSetDetour for the validation contract.
	BulkSetRuleSetDetour(ctx context.Context, tags []string, detour string) error
	DeleteRuleSet(ctx context.Context, tag string, force bool) error
	DatRuleSetURL(ctx context.Context, kind string, tags []string) (string, error)
	DatRuleSetFile(ctx context.Context, kind string, tags []string, token string) (string, error)

	ListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error)
	AddCompositeOutbound(ctx context.Context, o Outbound) error
	UpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error
	DeleteCompositeOutbound(ctx context.Context, tag string, force bool) error

	ApplyPreset(ctx context.Context, presetID, outboundTag string) error
	ListPresets() ([]Preset, error)

	ListPolicies(ctx context.Context) ([]PolicyInfo, error)
	CreatePolicy(ctx context.Context, description string) (PolicyInfo, error)
	ListPolicyDevices(ctx context.Context, policyName string) ([]PolicyDevice, error)
	BindDevice(ctx context.Context, mac, policyName string) error
	UnbindDevice(ctx context.Context, mac string) error

	ListDNSServers(ctx context.Context) ([]DNSServer, error)
	AddDNSServer(ctx context.Context, s DNSServer) error
	UpdateDNSServer(ctx context.Context, tag string, s DNSServer) error
	DeleteDNSServer(ctx context.Context, tag string, force bool) error
	MoveDNSServer(ctx context.Context, from, to int) error

	ListDNSRules(ctx context.Context) ([]DNSRule, error)
	AddDNSRule(ctx context.Context, r DNSRule) error
	UpdateDNSRule(ctx context.Context, index int, r DNSRule) error
	DeleteDNSRule(ctx context.Context, index int) error
	MoveDNSRule(ctx context.Context, from, to int) error

	GetDNSGlobals(ctx context.Context) (final, strategy string, err error)
	SetDNSGlobals(ctx context.Context, final, strategy string) error

	// GetDNSChainPreset / SetDNSChainPreset — DNS-пресет цепочек sing-box 1.14
	// (Mode "" = выключен). Состояние живёт в настройках, правила цепочки —
	// managed-оверлей поверх DNS-правил конфига.
	GetDNSChainPreset(ctx context.Context) (storage.DNSChainPresetState, error)
	SetDNSChainPreset(ctx context.Context, st storage.DNSChainPresetState) error

	Inspect(ctx context.Context, input InspectInput) (InspectResult, error)
	InspectStream(ctx context.Context, input InspectInput) (<-chan InspectStreamEvent, error)
	InspectDNS(ctx context.Context, input InspectDNSInput) (InspectDNSResult, error)

	StagingStatus(ctx context.Context) StagingStatus
	ApplyStaging(ctx context.Context) (orchestrator.ValidationResult, error)
	DiscardStaging(ctx context.Context) error
}

type InspectStreamEvent struct {
	Type     string           `json:"type"`
	Progress *InspectProgress `json:"progress,omitempty"`
	Result   *InspectResult   `json:"result,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type SingboxController interface {
	Reload() error
	IsRunning() (bool, int)
	Start() error
	// ClearManualStop clears the sticky master-Stop intent so the
	// orchestrator cold-start is no longer suppressed. Called by an explicit
	// router Enable (see Enable's call site); no-op when already clear.
	ClearManualStop() error
	ValidateConfigDir(ctx context.Context) error
	ConfigDir() string
	// Binary returns the absolute path (or PATH-resolvable name) of the
	// sing-box executable. Inspect shells out to it for `rule-set match`
	// evaluation. May return empty string when the binary is unknown —
	// callers must tolerate that and degrade gracefully.
	Binary() string
	// LastError returns the last captured sing-box fatal/exit reason
	// (stderr FATAL line or exit tail). Empty after a clean start.
	// Surfaced via Status.LastError so the UI explains «СБОЙ».
	LastError() string
	// CrashStats returns crash observability for Status (issue #456):
	// crashes within the recent window, the reason of the newest one,
	// and until when auto-restart is suppressed (zero = not suppressed).
	CrashStats() (recentCrashes int, lastCrashReason string, restartSuppressedUntil time.Time)
}

// GeoTagExpander is the narrow contract used by dat→SRS rule-set export.
// *hydraroute.GeoDataStore satisfies it without making router depend on the
// hydraroute package.
type GeoTagExpander interface {
	ExpandGeoTag(kind, tag string) (lines []string, filePath string, err error)
	// ExpandGeoTagTyped preserves the v2ray domain type per line
	// ("keyword:", "domain_regex:", ".", "full:") so datLinesToRuleSetRules
	// can map Plain→domain_keyword and Full→domain instead of collapsing
	// everything to domain_suffix (issue #448).
	ExpandGeoTagTyped(kind, tag string) (lines []string, filePath string, err error)
}

// PolicyDevice is one LAN device known to NDMS hotspot, annotated with
// whether it is currently bound to a specific policy.
type PolicyDevice struct {
	MAC   string `json:"mac"`
	IP    string `json:"ip"`
	Name  string `json:"name,omitempty"`
	Bound bool   `json:"bound"`
}

// WANInterfaceInfo is the public projection of one router WAN
// interface for the WAN-binding picker. Name is the kernel system-name
// (stable across NDMS re-creation) and is what gets persisted into
// SingboxRouterSettings.WANInterface and emitted into sing-box config
// route.default_interface. ID and Label are display-only.
type WANInterfaceInfo struct {
	Name     string `json:"name"`     // kernel system-name: "ppp0", "eth3"
	ID       string `json:"id"`       // NDMS interface ID: "ISP", "PPPoE0"
	Label    string `json:"label"`    // human-friendly: description or type-derived
	Up       bool   `json:"up"`       // current up/down — info-only, never gates selection
	Priority int    `json:"priority"` // NDMS priority (higher = preferred by user)
	Type     string `json:"type"`     // NDMS-тип интерфейса: "Wireguard", "Bridge", "PPP", ...
}

// WANInterfaceLister is the narrow contract the service needs from the
// NDMS interface store. *ndmsquery.InterfaceStore satisfies it. The
// router stays decoupled from concrete internal/ndms types via this
// consumer-owned interface (DIP); the adapter in cmd/awg-manager bridges the gap.
type WANInterfaceLister interface {
	ListWAN(ctx context.Context) ([]WANInterfaceInfo, error)
}

// BindableInterfaceLister enumerates router interfaces a user can bind a
// direct outbound to (all router interfaces minus our own and the
// awgoutbounds auto-managed set). Optional dep; nil = no existence check.
type BindableInterfaceLister interface {
	ListBindable(ctx context.Context) ([]WANInterfaceInfo, error)
	// ListAllBindable — то же множество, но БЕЗ вычитания интерфейсов, уже
	// занятых direct-outbound'ом: подписки и одиночные туннели делят
	// интерфейс с direct-выходом свободно (#709).
	ListAllBindable(ctx context.Context) ([]WANInterfaceInfo, error)
}

// IngressResolver резолвит ref интерфейса ("managed:Wireguard3") в
// kernel-имя ("nwg3"). Возвращает "" если не резолвится (сервер удалён /
// интерфейс ещё не поднят). Реализуется адаптером в cmd/awg-manager
// поверх InterfaceStore.ResolveSystemName (router декаплен от конкретных
// типов internal/ndms через consumer-owned контракт — DIP).
type IngressResolver interface {
	Resolve(ctx context.Context, ref string) string
}

// PolicyInfo is the public projection of one NDMS access policy that
// the router UI consumes for the policy selector.
type PolicyInfo struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Mark         string `json:"mark,omitempty"` // hex (e.g. "0xffffaaa"); may be empty if NDMS hasn't assigned yet
	DeviceCount  int    `json:"deviceCount"`
	IsOurDefault bool   `json:"isOurDefault"` // true if Description == "awgm-router"
}

// AccessPolicyProvider is the narrow contract Service needs from
// internal/accesspolicy. Adapter in cmd/awg-manager wires it.
//
// Контракт GetPolicyMark: «политика удалена / метки нет» обязана
// сигнализироваться ошибкой, для которой errors.Is(err,
// query.ErrPolicyMarkNotFound) == true — на этом sentinel'е держится
// fail-safe disable в reconcileInstalled (issue #523). Любая другая
// ошибка трактуется как транзиентная (RCI недоступен) и движок НЕ
// выключает. Реализация без sentinel'а молча отключит fail-safe.
type AccessPolicyProvider interface {
	GetPolicyMark(ctx context.Context, policyName string) (string, error)
	AssignDevice(ctx context.Context, mac, policyName string) error
	UnassignDevice(ctx context.Context, mac string) error
	ListDevicesForPolicy(ctx context.Context, policyName string) ([]PolicyDevice, error)
	ListPolicies(ctx context.Context) ([]PolicyInfo, error)
	CreatePolicy(ctx context.Context, description string) (PolicyInfo, error)
	// ListPolicyExits — политики, чей дефолт ведёт в iface (policy-tun:
	// источник connmark для перехвата DNS; политика там задаётся в NDMS,
	// а не в настройках, поэтому sr.PolicyName непригоден).
	ListPolicyExits(ctx context.Context, iface string) ([]query.PolicyDefaultExit, error)
	// PermitInterface разрешает интерфейс как выход политики. order —
	// позиция с нуля; policy-tun ставит 0, потому что туннель обязан стать
	// дефолтным выходом политики (в непустой политике order=0 вставляет в
	// начало и сдвигает прежние — это цель, а не побочный эффект).
	PermitInterface(ctx context.Context, policyName, iface string, order int) error
}

// AWGTagCatalog returns the canonical AWG-direct outbound tags owned
// by awgoutbounds (lives in 15-awg.json, not 20-router.json). Router
// consults this so computeIssues knows which tags are valid even
// though they don't appear in cfg.Outbounds.
type AWGTagCatalog interface {
	ListTags(ctx context.Context) ([]AWGTag, error)
}

// AWGTag is router's local projection of awgoutbounds.TagInfo.
type AWGTag struct {
	Tag string
}

// SingboxTunnelCatalog returns the outbound tags for sing-box tunnels
// owned by internal/singbox (lives in 10-tunnels.json). Routes can
// reference these tags as their Outbound (e.g. "veesp" for a VLESS
// outbound) — without this catalog, computeIssues would flag every
// such reference as a dangling outbound, surfacing a misleading
// "правило ссылается на несуществующий outbound" warn even though
// sing-box itself merges the tags across slots and the rule resolves
// at runtime.
type SingboxTunnelCatalog interface {
	ListTunnelTags(ctx context.Context) ([]string, error)
}

// StagingEventBus is the narrow interface the router service uses to
// publish resource:invalidated events for the staging/draft flow.
// *events.Bus satisfies it; tests pass a mockBus.
type StagingEventBus interface {
	Publish(event string, data any)
}

type Deps struct {
	AppLog   logging.AppLogger
	Settings *storage.SettingsStore
	// PresetCatalog is the unified preset catalog. Required for ListPresets and ApplyPreset.
	PresetCatalog *presets.Catalog
	Singbox       SingboxController
	Policies      AccessPolicyProvider
	Events        *events.Bus
	IPTables      *IPTables
	AWGTags       AWGTagCatalog // optional — when nil, computeIssues only sees cfg.Outbounds
	// AWGOutboundsRefresh перегенерирует 15-awg.json (awgoutbounds.Reconcile).
	// Optional. Зовётся перед валидирующим reload'ом enable fakeip-tun, чтобы
	// протухший каталог AWG-тегов не валил enable «unknown-outbound» (#567).
	AWGOutboundsRefresh func(ctx context.Context) error
	SingboxTunnels      SingboxTunnelCatalog // optional — when nil, computeIssues skips cross-slot tunnel tags
	// SubscriptionComposites lists composite outbounds owned by the
	// subscription slot (40-subscriptions.json). Optional — when nil,
	// ListCompositeOutbounds returns only this service's own composites.
	SubscriptionComposites *SubscriptionCompositesAdapter
	// Orch is the config.d orchestrator. When non-nil (production),
	// persistConfig writes 20-router.json through the slot writer and
	// Enable / Disable toggle SlotRouter so the file moves between
	// active and disabled/ — sing-box only sees the file when the
	// router is enabled. When nil (tests), persistConfig falls back
	// to the legacy in-place write at routerConfigPath().
	Orch *orchestrator.Orchestrator
	// Bus receives resource:invalidated events for the staging/draft
	// flow (SaveDraft, ApplyDraft, DiscardDraft). Optional — when nil,
	// staging event emission is silently skipped.
	Bus StagingEventBus
	// WANIPCollector returns the router's own IP addresses on
	// default-route interfaces. Used by Enable to populate WAN-IP
	// exclusions in the AWGM-TPROXY/AWGM-REDIRECT chains so LAN
	// traffic destined to the router's public WAN/tunnel IPs does
	// not loop back into sing-box. Optional — when nil, NewService
	// defaults to the production collector backed by d.Log.
	WANIPCollector WANIPCollector
	// WANInterfaces lists router WAN interfaces for the WAN-binding
	// picker. Optional — when nil, ListWANInterfaces returns an empty
	// slice (UI shows just the "auto" option). Production wiring in
	// cmd/awg-manager bridges this to ndmsQueries.Interfaces.ListWAN.
	WANInterfaces WANInterfaceLister
	// BindableInterfaces enumerates interfaces a user can bind a direct
	// outbound to. Optional — when nil, ListBindableInterfaces returns an
	// empty slice and bind_interface existence is not enforced.
	BindableInterfaces BindableInterfaceLister
	// IngressResolver резолвит managed:-ref'ы ingress-интерфейсов в
	// kernel-имена на сборке спека. Optional — nil → managed:-ref'ы
	// пропускаются (iface:-ref'ы резолвятся без него).
	IngressResolver IngressResolver
	// OnRoutingSlotsChanged, если задан, вызывается СИНХРОННО сразу после
	// того как Enable / Disable / переключение режима перепарковали слоты
	// маршрутизации (20-router.json / 21-fakeip.json), но ДО ближайшего
	// reload sing-box. Production-обвязка (main.go) дергает здесь
	// device-proxy ApplyInstances: слот 30 перегенерируется с учётом
	// новой доступности router-композитов, и коалесцированный reload
	// видит уже корректный файл — prune оркестратора не вырезает ссылки
	// на vpn/vpn2 молча (issue #465). Optional — nil пропускается.
	OnRoutingSlotsChanged func()
	// NetfilterPreflight is an optional override for the module-load /
	// target-availability check that Enable and reconcileInstalled both
	// call before every Install. When nil, prepareNetfilter runs the
	// standard fatal xt_TPROXY preflight plus best-effort preload of the
	// remaining router netfilter modules (xt_comment, xt_mark,
	// xt_connmark, xt_conntrack, xt_pkttype via
	// EnsureRouterNetfilterModules). Tests set this to avoid real syscalls.
	NetfilterPreflight func(context.Context) error

	// FirmwareRelease is an optional override for the KeeneticOS release
	// string (osdetect by default). Only tests set it — the tun-mode gate
	// needs a firmware version it can steer.
	FirmwareRelease func() string
	// XtDscpProbe is an optional override for the xt_dscp availability
	// check (kernel module + iptables `-m dscp` extension) that gates the
	// QoS-DSCP dispatch rules and the status field xtDscpAvailable. When
	// nil, the real IsXtDscpAvailable probe runs. Tests set this to avoid
	// exec'ing iptables.
	XtDscpProbe func(context.Context) bool
	GeoData     GeoTagExpander
	// GeoTagCounts даёт суммарные размеры geoip-тегов для бюджет-валидации
	// BypassGeoIPTags и список .dat для наполнения набора обхода. Optional —
	// nil в тестах; при nil бюджет не проверяется, а наполнение отдаёт ошибку.
	GeoTagCounts GeoIPTagCounter
	// OpkgTun provisions the fakeip-tun kernel interface via NDMS.
	// Optional — nil in tests; wired in cmd/awg-manager to
	// *ndmscommand.InterfaceCommands. Consumed by Slice 1D Enable.
	OpkgTun OpkgTunProvisioner
	// StaticRoutes adds/removes the NDMS auto static routes for the
	// fakeip pool + reject route. Optional — nil in tests; wired in
	// cmd/awg-manager via the route adapter. Consumed by Slice 1D Enable.
	StaticRoutes StaticRouteProvider
	// OpkgTunIndices lists occupied OpkgTun indices (kernel /sys ∪ NDMS)
	// for the fakeip index allocator. Optional — nil in tests; wired in
	// cmd/awg-manager via the union adapter. Consumed by Slice 1D Enable.
	OpkgTunIndices OpkgTunIndexLister
	// OpkgTunScan lists NDMS OpkgTun IDs carrying the given description —
	// the reap's persist-less orphan fallback (see teardownOpkgTun for why
	// such orphans are dangerous). Optional — nil skips the scan; wired in
	// cmd/awg-manager over the interface query store.
	OpkgTunScan func(ctx context.Context, description string) ([]string, error)
	// FakeIPTun holds the static fakeip-tun provisioning knobs (pool
	// ranges, tun addrs, MTU, DHCP pool). Zero-value in tests; defaults
	// wired in cmd/awg-manager. Consumed by Slice 1D Enable.
	FakeIPTun FakeIPTunParams

	// Segments отдаёт описание и адресацию сегментов для предпоказа
	// source-preserve: без них пользователь выбирает сегменты по системным
	// именам вслепую. Optional — nil оставляет системные имена.
	Segments SegmentDetails
	// DefaultRoute паркует NDMS-дефолт (v4/v6) на policy-tun интерфейс.
	// Optional — nil в тестах; wired в cmd/awg-manager на *RouteCommands.
	DefaultRoute DefaultRouteProvider
	// SegmentNAT управляет NAT сегментов (`ip nat` / `ip static`) в
	// policy-tun. Optional — nil в тестах; wired на *NATCommands.
	SegmentNAT SegmentNATProvider
	// RunningConfig читает /show/running-config для сверки дрейфа
	// policy-tun. Optional — nil в тестах; wired на *RunningConfigStore.
	RunningConfig RunningConfigReader
	// NATState — структурированное состояние NAT для той же сверки.
	// Optional — nil в тестах; wired через адаптер над NAT/StaticNAT-сторами.
	NATState NATStateReader
	// DefaultGateway отдаёт NDMS-имя WAN (интерфейс дефолтного маршрута) —
	// цель static-NAT в source-preserve. Optional — nil в тестах; wired на
	// *query.RouteStore.
	DefaultGateway DefaultGatewayResolver
	// ReconcileBaseDNSStrategy примиряет dns.strategy 00-base.json с
	// владением routing-слотов ПОСЛЕ того, как их содержимое изменилось
	// (ApplyStaging / FakeIPSetDNSGlobals): мерж скаляров в config.d —
	// first-file-wins, поэтому без примирения base затеняет выбор
	// пользователя до перезапуска демона. Best-effort: ошибка не валит
	// успешное применение. Optional — nil в тестах.
	ReconcileBaseDNSStrategy func() error
	// ReconcileBaseDomainResolver примиряет route.default_domain_resolver
	// 00-base.json с владением routing-слота: пока база несёт свой ключ,
	// резолвер роутера (fakeip-tun ставит "real") выбрасывается мержем.
	// Опционален — nil означает «шаг не подключён».
	ReconcileBaseDomainResolver func() error
}

// routerLoggerAdapter narrows *logging.ScopedLogger to the wanLogger
// interface required by NewWANIPCollector. ScopedLogger expects
// (action, target, message) — we collapse to a single message.
type routerLoggerAdapter struct {
	log *logging.ScopedLogger
}

func (a *routerLoggerAdapter) Warn(msg string) {
	if a.log == nil {
		return
	}
	a.log.Warn("wan-ip", "", msg)
}

func (a *routerLoggerAdapter) Info(msg string) {
	if a.log == nil {
		return
	}
	a.log.Info("wan-ip", "", msg)
}

type ServiceImpl struct {
	deps   Deps
	appLog *logging.ScopedLogger
	// bypassLog — журнал набора обхода (подгруппа «Набор обхода»), чтобы итоги
	// наполнения не терялись в общем потоке singbox-router.
	bypassLog *logging.ScopedLogger
	mu        sync.Mutex
	// transitionMu serializes SwitchRoutingMode calls. It is DISTINCT from mu:
	// Enable/Disable (which SwitchRoutingMode composes) take mu themselves, so
	// holding mu across the whole switch would self-deadlock.
	transitionMu sync.Mutex
	// transitionReadinessProgress emits readiness heartbeats during
	// waitForSingbox while SwitchRoutingMode is in flight (nil otherwise).
	transitionReadinessProgress func(message string)

	// ─── Применённое состояние netfilter ────────────────────────────────────
	// appliedSpec, appliedBlackhole, netfilterStateKnown и
	// currentBypassGeoIPTags описывают то, что РЕАЛЬНО установлено в netfilter.
	//
	// ИНВАРИАНТ: взаимное исключение даёт transitionMu, и только он.
	// Писатели: enableLocked, Disable, enablePolicyTun, disablePolicyTun,
	// reconcileInstalled, reconcilePolicyTunQoS. Читатели: reconcileInstalled,
	// reconcilePolicyTunQoS. Каждый достижим ровно двумя путями — Reconcile
	// (берёт transitionMu через TryLock) и SwitchRoutingMode (через Lock).
	//
	// s.mu вокруг записей НЕ является защитой этих полей: часть чтений в
	// reconcileInstalled (forceInitialSync, specChanged, bypassGeoTagsChanged)
	// идёт вне s.mu, так что опираться на него нельзя — он держится ради
	// соседних полей в тех же критических секциях.
	//
	// Пока путей два, гонки нет по построению. Третий путь (например новая
	// публичная ручка, зовущая Enable/Disable мимо transitionMu) её вернёт —
	// раньше такой путь давали ручки POST /singbox/router/{enable,disable},
	// и они удалены именно поэтому. Новый вызывающий обязан заходить через
	// Reconcile или SwitchRoutingMode.
	//
	// appliedSpec — снимок ровно того RestoreInputSpec, что уехал в
	// IPTables.Install; nil = «ничего нашего в netfilter не установлено».
	// Хранятся ВХОДЫ правил целиком, а не их разложенные копии: пока входы
	// лежали двенадцатью полями, каждая новая точка сравнения забывала часть
	// из них (так и жил дефект policy-tun, сравнивавший один вход из семи).
	// Сравнение — equalInstalledSpec, по РЕНДЕРУ спека: поле, которого нет в
	// правилах, изменить netfilter не может, поэтому «забыли поле» здесь
	// невыразимо. После записи спек не мутируется.
	// Единственный писатель непустого значения — успешный Install (nil в него
	// пишут ещё Disable, disablePolicyTun и нулевая ветка reconcilePolicyTunQoS).
	appliedSpec *RestoreInputSpec

	// appliedBlackhole — такой же снимок ВТОРОГО ресурса: fail-closed DROP,
	// который reconcileInstalled поднимает, пока sing-box мёртв, а
	// PREROUTING-джампы снесены. nil = блокировки нет. Ресурс отдельный от
	// appliedSpec во всём: свой рендер (buildBlackholeRestoreInput), свой файл
	// правил (persistBlackhole) и своя цепочка (BlackholeChain), — поэтому и
	// снимок свой (сравнение — equalBlackholeSpec). Снимком, а не булевым
	// флагом: по флагу переустановка сводилась бы к «уже стоит — не трогаем»,
	// и смена WAN-адреса роутера посреди простоя движка не доехала бы до
	// исключений блокировки — это закреплено
	// TestReconcileInstalled_BlackholeFollowsWANIPChange.
	appliedBlackhole *RestoreInputSpec

	// last-installed geoip-теги обхода; в спеке от них остаётся только
	// производный BypassGeoIPSet, то есть состав тегов из снимка
	// невосстановим. Поэтому смена состава видна ТОЛЬКО здесь — и это триггер
	// пересборки/сноса набора AWGM-BYPASS.
	currentBypassGeoIPTags []string

	// bypassPopulating — single-flight наполнения AWGM-BYPASS: пока идёт
	// пересборка, повторные триггеры (Enable, reconcile, смена .dat) только
	// взводят bypassRerunPending.
	bypassPopulating atomic.Bool
	// bypassRerunPending — триггер, пришедший во время наполнения: по его
	// завершении прогон повторяется один раз с актуальными тегами.
	bypassRerunPending atomic.Bool
	// Итог последнего наполнения bypass-набора (под mu). bypassCountOK
	// различает подтверждённый ноль и «счётчик получить не удалось» — пустой
	// набор и неизвестный размер не одно и то же.
	bypassEntryCount   int
	bypassCountOK      bool
	bypassLastPopulate time.Time
	bypassLastError    string
	bypassMissingTags  []string
	// populateBypassSet — шов наполнения для тестов (nil = bypassset.Populate).
	populateBypassSet func(ctx context.Context, tags []string) (bypassset.PopulateResult, error)
	// teardownBypassSetFn — шов сноса набора для тестов (nil = реальный снос).
	teardownBypassSetFn func(ctx context.Context)

	// Однократные зачистки наследия выпиленного селектива: файлы + managed-
	// правила на старте, ipset'ы AWGM-SELECTIVE(-STG) — после первой установки
	// правил (до неё набор ещё занят старыми правилами).
	legacySelectiveOnce    sync.Once
	legacySelectiveSetOnce sync.Once

	// netfilterStateKnown tracks whether we know for certain that the
	// installed iptables rules match the current desired state. It starts
	// false on every ServiceImpl construction (including after a daemon
	// restart / upgrade where the old iptables chains may be stale). The
	// first reconcileInstalled or Enable install forces a full re-install.
	// After Install succeeds the flag is set to true until Disable resets it.
	netfilterStateKnown bool

	// fakeipACLAsserted — one-shot флаг drift-heal'а: permit-ACL fakeip-tun
	// переассертится один раз за жизнь процесса (апгрейд поверх включённого
	// fakeip / ручное удаление списка). Доступ только из Reconcile
	// (сериализован transitionMu).
	fakeipACLAsserted bool

	// policyTunACLAsserted — тот же one-shot флаг для policy-tun (свой, чтобы
	// смена режима не съедала ассерт соседа). Доступ только из Reconcile.
	policyTunACLAsserted bool

	// *ACLv6Asserted — отдельные флаги v6-разрешения: у NDMS под IPv6 своё
	// пространство списков, и v4-ассерт его не ставит. Свой флаг на каждый, а
	// не общий с v4: иначе успех v4 гасил бы ретрай упавшего v6, и интерфейс
	// молча остался бы без v6-разрешения до перезапуска режима.
	fakeipACLv6Asserted    bool
	policyTunACLv6Asserted bool

	// qosApplyFailed remembers a failed sing-box apply of the QoS routes
	// slot so the next heal re-applies even when disk state is byte-equal.
	// См. applyQoSRoutesSlot.
	qosApplyFailed atomic.Bool

	// xtDscpState tracks the last observed xt_dscp availability for
	// transition-only logging (0 = unknown, 1 = available, 2 = unavailable):
	// the reconcile loop must not Warn every tick while the module stays
	// missing — only when availability actually flips.
	xtDscpState atomic.Int32

	// inspectCache backs the route-inspector's rule_set match path. Lazy
	// constructed on first Inspect call so dev-machine builds (no
	// sing-box binary, no /tmp writes during NewService) stay clean.
	inspectCacheOnce sync.Once
	inspectCache     *ruleSetCache
	datRuleSetMu     sync.Mutex

	// Optional keendns-preset → имена KeenDNS резолвит сам роутер, а его
	// адреса идут мимо sing-box.
	// Wired post-construction via SetKeenDNSPreset (dnsrewrite lives in
	// setupListen after the router service) — под keenDNSMu, потому что
	// startup-Reconcile читает их из своей горутины уже во время wiring.
	keenDNSMu sync.Mutex
	// keenDNSWarnState — последнее залогированное состояние пресета, чтобы
	// не писать один и тот же warn на каждом тике Reconcile.
	keenDNSWarnState string
	keenDNSInfoProv  KeenDNSInfoProvider
	keenDNSSync      KeenDNSPresetSyncer
	// Кэш данных с роутера (см. keenDNSInfoTTL) и производный от него
	// список CIDR обхода, который уезжает в RestoreInputSpec.
	keenDNSFQDN        string
	keenDNSAddrs       []string
	keenDNSInfoAt      time.Time
	keenDNSBypassCIDRs []string
}

func NewService(d Deps) *ServiceImpl {
	if d.IPTables == nil {
		d.IPTables = NewIPTables()
	}
	appLog := logging.NewScopedLogger(d.AppLog, logging.GroupRouting, logging.SubSingboxRouter)
	if d.WANIPCollector == nil {
		d.WANIPCollector = NewWANIPCollector(&routerLoggerAdapter{log: appLog})
	}
	// Idempotently refresh the netfilter hook script: if a previous
	// version is on disk (older AWGM without pidof guard), this writes
	// the current version. No-op when the file is absent — Install
	// creates it on first Enable.
	refreshNetfilterHookIfPresent()
	return &ServiceImpl{
		deps:      d,
		appLog:    appLog,
		bypassLog: logging.NewScopedLogger(d.AppLog, logging.GroupRouting, logging.SubBypassSet),
	}
}

func (s *ServiceImpl) routerConfigPath() string {
	return filepath.Join(s.deps.Singbox.ConfigDir(), "20-router.json")
}

func (s *ServiceImpl) resolveIngressInterfaces(ctx context.Context, refs []string) []string {
	out := make([]string, 0, len(refs))
	seen := map[string]bool{}
	for _, ref := range refs {
		var name string
		switch {
		case strings.HasPrefix(ref, "iface:"):
			name = strings.TrimPrefix(ref, "iface:")
		case strings.HasPrefix(ref, "managed:"):
			if s.deps.IngressResolver != nil {
				name = s.deps.IngressResolver.Resolve(ctx, ref)
			}
		}
		if name == "" {
			s.appLog.Warn("resolve-ingress", "", fmt.Sprintf("ingress ref %q не резолвится (сервер не поднят / кэш не готов), пропущен", ref))
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func (s *ServiceImpl) ruleSetMaterializer() ruleSetMaterializer {
	var configDir, binary string
	if s.deps.Orch != nil {
		configDir = s.deps.Orch.ConfigDir()
	} else if s.deps.Singbox != nil {
		configDir = s.deps.Singbox.ConfigDir()
	}
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	return ruleSetMaterializer{
		configDir: configDir,
		binary:    binary,
		log:       logging.NewScopedLogger(s.deps.AppLog, logging.GroupRouting, logging.SubSingboxRouter),
	}
}

// loadRouterConfig returns the router config the user is currently editing.
// When the orchestrator is wired, it delegates to LoadEffective which
// prefers pending/ over active/ — so UI callers (ListRules etc.) always
// see "what's being edited" rather than "what's currently live". Falls
// back to an empty config when neither file exists yet.
func (s *ServiceImpl) loadRouterConfig() (*RouterConfig, error) {
	if s.deps.Orch != nil {
		data, err := s.deps.Orch.LoadEffective(orchestrator.SlotRouter)
		if err != nil {
			return nil, fmt.Errorf("load router config: %w", err)
		}
		return parseRouterConfigBytes(data)
	}
	// Legacy fallback (no orchestrator): read from active path directly.
	activePath := s.routerConfigPath()
	if _, statErr := os.Stat(activePath); statErr == nil {
		return LoadConfig(activePath)
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	return LoadConfig(activePath) // returns NewEmptyConfig per contract
}

// loadAppliedRouterConfig returns the APPLIED router config (active/, then
// disabled/), never the pending draft. Enforcement decisions — the reconcile
// self-heal that can persistently flip settings — must judge what is actually
// running, not what the user has staged and may still discard.
func (s *ServiceImpl) loadAppliedRouterConfig() (*RouterConfig, error) {
	if s.deps.Orch != nil {
		data, err := s.deps.Orch.LoadApplied(orchestrator.SlotRouter)
		if err != nil {
			return nil, fmt.Errorf("load applied router config: %w", err)
		}
		return parseRouterConfigBytes(data)
	}
	return s.loadRouterConfig()
}

// parseRouterConfigBytes unmarshals slot bytes into a RouterConfig with all
// collection fields normalized to non-nil and DNS sanitized. nil data (slot
// never configured) yields an empty config.
func parseRouterConfigBytes(data []byte) (*RouterConfig, error) {
	if data == nil {
		return NewEmptyConfig(), nil
	}
	cfg := NewEmptyConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse router config: %w", err)
	}
	if cfg.Inbounds == nil {
		cfg.Inbounds = []Inbound{}
	}
	if cfg.Outbounds == nil {
		cfg.Outbounds = []Outbound{}
	}
	if cfg.Route.RuleSet == nil {
		cfg.Route.RuleSet = []RuleSet{}
	}
	if cfg.Route.Rules == nil {
		cfg.Route.Rules = []Rule{}
	}
	if cfg.DNS.Servers == nil {
		cfg.DNS.Servers = []DNSServer{}
	}
	if cfg.DNS.Rules == nil {
		cfg.DNS.Rules = []DNSRule{}
	}
	SanitizeDNSConfig(cfg)
	return cfg, nil
}

// loadRouterConfigForMode returns the routing config for the active mode:
// SlotFakeIP in fakeip-tun mode, SlotRouter (tproxy) otherwise. Lets
// mode-agnostic readers (GetStatus) reflect whichever slot is live.
func (s *ServiceImpl) loadRouterConfigForMode(mode string) (*RouterConfig, error) {
	if mode == "fakeip-tun" {
		return s.loadFakeIPConfig()
	}
	return s.loadRouterConfig()
}

// persistConfigDirect writes the router config straight to active/ —
// skipping the staging pipeline that persistConfig uses for user-driven
// edits. Intended for system-initiated paths (Enable, Disable cleanup,
// healTProxyInbound) where there is no user "Apply" expected and the
// pending-file → banner UX would be a phantom on every router reboot.
//
// Byte-equal short-circuit: when the marshalled bytes already match the
// active file we return without writing. This is the common boot-recovery
// case (Reconcile detects iptables gone, dispatches to Enable, Enable
// regenerates the identical config) and the no-op skips a spurious
// SIGHUP plus avoids touching mtime.
//
// Caller must have already arranged for the slot to be enabled in the
// orchestrator (so orch.Save targets the active path, not disabled/) —
// Enable does that via SetEnabled(true) earlier in the flow.
func (s *ServiceImpl) persistConfigDirect(ctx context.Context, cfg *RouterConfig) error {
	if s.deps.Orch == nil {
		// Test-only legacy fallback: reuse the in-place writer.
		return s.persistConfig(ctx, cfg)
	}
	return s.persistSlotDirect(orchestrator.SlotRouter, cfg, false)
}

// persistSlotDirect materializes cfg and, when the serialized bytes differ from
// what is already on disk, writes them to the slot's active file via the
// orchestrator (scheduling a debounced reload). The active path is resolved
// from the orchestrator so it stays in sync with the slot's registered
// filename instead of a hardcoded literal. checkCycles runs the composite-cycle
// guard before writing. Orch must be non-nil; the caller must have arranged for
// the slot to be enabled. Shared by persistConfigDirect and persistFakeIPConfig.
func (s *ServiceImpl) persistSlotDirect(slot orchestrator.Slot, cfg *RouterConfig, checkCycles bool) error {
	materialized, err := s.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		return err
	}
	if checkCycles {
		if err := validateNoCompositeCycles(materialized.Outbounds); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(materialized, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s config: %w", slot, err)
	}
	activePath, err := s.deps.Orch.ActivePath(slot)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(activePath); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	return s.deps.Orch.Save(slot, data)
}

// notifyRoutingSlotsChanged invokes the OnRoutingSlotsChanged hook (see
// Deps). Must be called AFTER the slot toggle it reports and BEFORE the
// reload that would otherwise prune the dependents' now-dangling refs.
func (s *ServiceImpl) notifyRoutingSlotsChanged() {
	if s.deps.OnRoutingSlotsChanged != nil {
		s.deps.OnRoutingSlotsChanged()
	}
}

// orchestratorApplyNow flushes any debounced reload and applies config.d to
// sing-box immediately. No-op when Orch is nil (tests).
func (s *ServiceImpl) orchestratorApplyNow() error {
	if s.deps.Orch == nil {
		return nil
	}
	return s.deps.Orch.ReloadNow()
}

func (s *ServiceImpl) persistConfig(ctx context.Context, cfg *RouterConfig) error {
	materialized, err := s.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		return err
	}
	// sing-box only reports circular outbound dependencies at "start
	// service" (not via `sing-box check`), so a cyclic config would persist
	// and FATAL-loop. Catch it here before writing, regardless of source
	// (UI, subscription refresh, import, migration).
	if err := validateNoCompositeCycles(materialized.Outbounds); err != nil {
		return err
	}
	if s.deps.Orch != nil {
		// Orchestrator path — write to pending/ (staging). The draft will
		// be applied explicitly via ApplyStaging. No SIGHUP is triggered
		// here; sing-box keeps running with the previously-applied config.
		data, err := json.MarshalIndent(materialized, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal router config: %w", err)
		}
		// Phantom-draft guard: an inline rule-set's rules live in sidecar
		// artifacts, not in 20-router.json (which carries only a
		// {type:local,format:binary,path} reference). Editing only the rules
		// of an already-applied inline rule-set therefore leaves the
		// materialized config byte-identical to active — materializeConfig
		// has already rewritten the live .srs and sing-box hot-reloads it
		// without SIGHUP. There is no real config change to stage, so a draft
		// would only raise a phantom "unsaved changes" banner. Drop any stale
		// draft and return. This guard's safety rests on the invariant that
		// any genuinely structural change (new rule-set, tag rename, route/DNS/
		// outbound edit) perturbs the materialized bytes and so is never
		// byte-equal. A missing/unreadable active file (router disabled, first
		// Enable, slot parked under disabled/) is NOT equal — fall through to
		// staging so the change is applied normally.
		activePath := filepath.Join(s.deps.Orch.ConfigDir(), "20-router.json")
		if existing, rerr := os.ReadFile(activePath); rerr == nil && bytes.Equal(existing, data) {
			if err := s.deps.Orch.DiscardDraft(orchestrator.SlotRouter); err != nil {
				return err
			}
			s.emitStagingEvent("discarded")
			return nil
		}
		if err := s.deps.Orch.SaveDraft(orchestrator.SlotRouter, data); err != nil {
			return err
		}
		s.emitStagingEvent("staged")
		return nil
	}

	// Legacy fallback (tests) — in-place write + sing-box check + reload.
	path := s.routerConfigPath()
	backupPath := path + ".bak"

	_, hadExisting := os.Stat(path)
	if hadExisting == nil {
		if err := os.Rename(path, backupPath); err != nil {
			return fmt.Errorf("backup router config: %w", err)
		}
	}

	restore := func() {
		_ = os.Remove(path)
		if hadExisting == nil {
			_ = os.Rename(backupPath, path)
		}
	}

	if err := SaveConfig(path, materialized); err != nil {
		restore()
		return err
	}
	if err := s.deps.Singbox.ValidateConfigDir(ctx); err != nil {
		restore()
		return fmt.Errorf("%s", cleanValidateError(err))
	}
	if running, _ := s.deps.Singbox.IsRunning(); running {
		heavyop.Default.Lock()
		err := s.deps.Singbox.Reload()
		heavyop.Default.Unlock()
		if err != nil {
			return err
		}
	}
	if hadExisting == nil {
		_ = os.Remove(backupPath)
	}
	return nil
}

func (s *ServiceImpl) withConfig(ctx context.Context, event string, fn func(*RouterConfig) error) error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	cfg = s.ruleSetMaterializer().restoreConfig(cfg)
	if err := fn(cfg); err != nil {
		return err
	}
	if err := s.ensureDNSChainOverlayFromState(cfg); err != nil {
		return err
	}
	if err := s.persistConfig(ctx, cfg); err != nil {
		return err
	}
	s.emitCfgEvent(event, cfg)
	return nil
}

func (s *ServiceImpl) emitCfgEvent(event string, cfg *RouterConfig) {
	if s.deps.Events == nil {
		return
	}
	switch event {
	case "all":
		s.deps.Events.Publish("singbox-router:rules", cfg.Route.Rules)
		s.deps.Events.Publish("singbox-router:rulesets", cfg.Route.RuleSet)
		s.deps.Events.Publish("singbox-router:outbounds", cfg.CompositeOutbounds())
		s.deps.Events.Publish("singbox-router:dns-servers", cfg.DNS.Servers)
		s.deps.Events.Publish("singbox-router:dns-rules", cfg.DNS.Rules)
		s.deps.Events.Publish("singbox-router:dns-globals", map[string]string{
			"final": cfg.DNS.Final, "strategy": cfg.DNS.Strategy,
		})
	case "rules":
		s.deps.Events.Publish("singbox-router:rules", cfg.Route.Rules)
	case "rulesets":
		s.deps.Events.Publish("singbox-router:rulesets", cfg.Route.RuleSet)
	case "outbounds":
		s.deps.Events.Publish("singbox-router:outbounds", cfg.CompositeOutbounds())
	case "dns-servers":
		s.deps.Events.Publish("singbox-router:dns-servers", cfg.DNS.Servers)
	case "dns-rules":
		s.deps.Events.Publish("singbox-router:dns-rules", cfg.DNS.Rules)
	case "dns-globals":
		s.deps.Events.Publish("singbox-router:dns-globals", map[string]string{
			"final": cfg.DNS.Final, "strategy": cfg.DNS.Strategy,
		})
	}
	if status, err := s.GetStatus(context.Background()); err == nil {
		s.deps.Events.Publish("singbox-router:status", status)
	}
}

// ---------------------------------------------------------------------------
// Staging API
// ---------------------------------------------------------------------------

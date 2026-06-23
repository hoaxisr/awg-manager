package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type Service interface {
	Enable(ctx context.Context) error
	Disable(ctx context.Context) error
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

	// ListIngressEligibleInterfaces returns interfaces eligible for
	// sing-box ingress-scope (bindable minus WAN minus LAN bridges).
	ListIngressEligibleInterfaces(ctx context.Context) ([]WANInterfaceInfo, error)

	SetRouteFinal(ctx context.Context, tag string) error

	ListRules(ctx context.Context) ([]Rule, error)
	AddRule(ctx context.Context, rule Rule) error
	UpdateRule(ctx context.Context, index int, rule Rule) error
	DeleteRule(ctx context.Context, index int) error
	MoveRule(ctx context.Context, from, to int) error

	ListRuleSets(ctx context.Context) ([]RuleSet, error)
	AddRuleSet(ctx context.Context, rs RuleSet) error
	UpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error
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
}

// GeoTagExpander is the narrow contract used by dat→SRS rule-set export.
// *hydraroute.GeoDataStore satisfies it without making router depend on the
// hydraroute package.
type GeoTagExpander interface {
	ExpandGeoTag(kind, tag string) (lines []string, filePath string, err error)
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
type AccessPolicyProvider interface {
	GetPolicyMark(ctx context.Context, policyName string) (string, error)
	AssignDevice(ctx context.Context, mac, policyName string) error
	UnassignDevice(ctx context.Context, mac string) error
	ListDevicesForPolicy(ctx context.Context, policyName string) ([]PolicyDevice, error)
	ListPolicies(ctx context.Context) ([]PolicyInfo, error)
	CreatePolicy(ctx context.Context, description string) (PolicyInfo, error)
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
	PresetCatalog  *presets.Catalog
	Singbox        SingboxController
	Policies       AccessPolicyProvider
	Events         *events.Bus
	IPTables       *IPTables
	AWGTags        AWGTagCatalog        // optional — when nil, computeIssues only sees cfg.Outbounds
	SingboxTunnels SingboxTunnelCatalog // optional — when nil, computeIssues skips cross-slot tunnel tags
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
	// NetfilterPreflight is an optional override for the module-load /
	// target-availability check that Enable and reconcileInstalled both
	// call before every Install. When nil, prepareNetfilter runs the
	// standard fatal xt_TPROXY preflight plus best-effort preload of the
	// remaining router netfilter modules (xt_comment, xt_mark,
	// xt_connmark, xt_conntrack, xt_pkttype via
	// EnsureRouterNetfilterModules). Tests set this to avoid real syscalls.
	NetfilterPreflight func(context.Context) error
	GeoData            GeoTagExpander
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
	// FakeIPTun holds the static fakeip-tun provisioning knobs (pool
	// ranges, tun addrs, MTU, DHCP pool). Zero-value in tests; defaults
	// wired in cmd/awg-manager. Consumed by Slice 1D Enable.
	FakeIPTun FakeIPTunParams
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
	mu     sync.Mutex
	// transitionMu serializes SwitchRoutingMode calls. It is DISTINCT from mu:
	// Enable/Disable (which SwitchRoutingMode composes) take mu themselves, so
	// holding mu across the whole switch would self-deadlock.
	transitionMu            sync.Mutex
	currentMark             string              // last-installed iptables mark; used by Reconcile to detect change
	currentWANIPs           []string            // last-collected WAN IPs; used by Reconcile to detect change
	currentLANBridges       []LANBridgeDNSRedir // last-discovered LAN-bridge (name, ndnproxy port) pairs; reconcile triggers re-install when this changes (e.g. NDMS hotspot reconfigured, bridge added/removed, port reassigned)
	currentBypassPresets    []string
	currentBypassExtraPorts   string
	currentBypassExtraSubnets string
	currentIngress            []string // last-installed резолвленные ingress kernel-имена

	// netfilterStateKnown tracks whether we know for certain that the
	// installed iptables rules match the current desired state. It starts
	// false on every ServiceImpl construction (including after a daemon
	// restart / upgrade where the old iptables chains may be stale). The
	// first reconcileInstalled or Enable install forces a full re-install.
	// After Install succeeds the flag is set to true until Disable resets it.
	netfilterStateKnown bool

	// inspectCache backs the route-inspector's rule_set match path. Lazy
	// constructed on first Inspect call so dev-machine builds (no
	// sing-box binary, no /tmp writes during NewService) stay clean.
	inspectCacheOnce sync.Once
	inspectCache     *ruleSetCache
	datRuleSetMu     sync.Mutex
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
	return &ServiceImpl{deps: d, appLog: appLog}
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
	// Legacy fallback (no orchestrator): read from active path directly.
	activePath := s.routerConfigPath()
	if _, statErr := os.Stat(activePath); statErr == nil {
		return LoadConfig(activePath)
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	return LoadConfig(activePath) // returns NewEmptyConfig per contract
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
	materialized, err := s.ruleSetMaterializer().materializeConfig(cfg)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(materialized, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal router config: %w", err)
	}
	activePath := filepath.Join(s.deps.Orch.ConfigDir(), "20-router.json")
	if existing, err := os.ReadFile(activePath); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	if err := s.deps.Orch.Save(orchestrator.SlotRouter, data); err != nil {
		return err
	}
	return nil
}

// prepareNetfilter runs the common netfilter preflight: xt_TPROXY module
// load and TPROXY target availability check. It is shared by Enable and
// reconcileInstalled so both paths run identical validation. Tests can
// override it via deps.NetfilterPreflight to avoid real syscalls.
func (s *ServiceImpl) prepareNetfilter(ctx context.Context) error {
	if s.deps.NetfilterPreflight != nil {
		return s.deps.NetfilterPreflight(ctx)
	}

	if err := EnsureTProxyModule(ctx); err != nil {
		return err
	}

	if !IsTProxyTargetAvailable(ctx) {
		return fmt.Errorf("iptables TPROXY target unavailable — kernel module loaded but iptables extension missing")
	}

	// Best-effort preload of all remaining router netfilter modules.
	// TPROXY is already handled above as fatal; the rest are soft.
	// This mirrors the full matcher/target set bisect-combo.sh warms up:
	// xt_comment, xt_mark, xt_connmark, xt_conntrack, xt_pkttype.
	if errs := EnsureRouterNetfilterModules(ctx); len(errs) > 0 {
		for _, err := range errs {
			s.appLog.Warn("ensure-netfilter", "", err.Error())
		}
	}

	return nil
}

// waitForSingbox polls until sing-box is BOTH process-alive and actually
// listening on the router inbound sockets (TCP RedirectPort + UDP
// TPROXYPort), or the deadline expires. Used by Enable after SetEnabled
// triggers the orchestrator's debounced cold-start so iptables redirects
// don't land on a TPROXY port that nothing is listening on yet.
//
// PID-alive alone is not enough (issue #354): the router config reaches
// sing-box via the orchestrator's debounced (250ms) reload, so an
// already-running process keeps serving the OLD inbound set for a moment,
// and a freshly started one binds inbounds only at the end of startup
// (after config parse + rule-set load — seconds on mipsel). Gating on the
// same socket probe GetStatus uses means the status emitted at the end of
// Enable reflects a truly active interception path instead of a transient
// «СБОЙ».
//
// Returns ctx.Err on cancellation, or a timeout error after the deadline;
// callers can treat the timeout as soft (proceed with iptables and accept
// the brief race) or hard at their discretion.
// fakeIPIfaceName builds the KERNEL interface name for a fakeip-tun OpkgTun from
// its allocated index (e.g. index 3 → "opkgtun3"). Use this ONLY where the
// kernel sees the iface: the sing-box tun inbound interface_name, the
// "ip addr flush dev <iface>" exec, /sys/class/net/<iface>/carrier, the
// /proc/net/route iface match, and the /sys index scan. For NDMS RCI calls use
// fakeIPNDMSName instead — NDMS rejects the lowercase kernel name.
func fakeIPIfaceName(index int) string {
	return tunnel.NewNames("awg" + strconv.Itoa(index)).IfaceName
}

// fakeIPNDMSName builds the NDMS RCI interface name for a fakeip-tun OpkgTun from
// its allocated index (e.g. index 3 → "OpkgTun3"). This mirrors
// tunnel.Names.NDMSName (CamelCase "OpkgTun%s"); the kernel name is its lowercase
// (strings.ToLower → fakeIPIfaceName). NDMS REQUIRES this CamelCase form for every
// RCI interface op (create/delete, address/mtu, up/down) and StaticRouteSpec
// Interface — passing the lowercase kernel name yields
// "unsupported interface type: \"opkgtun\"" (stand-verified). Use fakeIPIfaceName
// only for the kernel-facing sites (sing-box config, ip exec, /sys, /proc).
func fakeIPNDMSName(index int) string {
	return tunnel.NewNames("awg" + strconv.Itoa(index)).NDMSName
}

// ReapOrphanedFakeIPTun removes a fakeip-tun OpkgTun left provisioned by a crash
// or incomplete teardown when the router is no longer in fakeip-tun mode. It is
// STARTUP-ONLY (wired once in cmd/awg-manager) — it must NOT run on every
// Reconcile, or a live mode-switch (fakeip→tproxy) would tear the iface down
// bluntly, bypassing the safe drain in Disable(fakeip-tun). Idempotent and
// best-effort: reaps strictly by persisted state (Index). A description-based
// scan to catch OpkgTuns whose persist was lost is a deferred fallback (v1
// reaps by persist only).
//
// It ALSO sweeps a stale v4 drain reject route for the configured pool in
// non-fakeip mode — the startup safety net for a disable drain interrupted by a
// restart (the async drain goroutine does not survive one) or an async-remove
// that didn't match the route (Fix 1). That sweep is persist-independent.
//
// INVARIANT (relied on by this reap): Enable(fakeip-tun) MUST persist the index
// via SetFakeIPState BEFORE CreateOpkgTun (and roll back its own partial work on
// failure), so persisted state is a reliable superset of live ifaces — otherwise
// a crash mid-Enable leaves a persist-less orphan this reap cannot see (the
// description-scan fallback is deferred).
func (s *ServiceImpl) ReapOrphanedFakeIPTun(ctx context.Context) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, _ := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if sr.RoutingMode == "fakeip-tun" {
		return nil // active mode owns the iface; Enable/Reconcile manage it
	}

	// Startup safety net for the disable drain (Fix 1): the async drain goroutine
	// that removes the v4 reject route does NOT survive a daemon restart (no
	// persisted pending-drain). So in NON-fakeip mode (we are not currently the
	// active owner) best-effort remove a stale drain reject route for the
	// CONFIGURED pool. This covers a drain that was interrupted by a restart OR an
	// async-remove that didn't match the route. Derive net/mask exactly as
	// disableFakeIPTun does (Masked → splitCIDR). NDMS no:true on a non-existent
	// route is idempotent, so this is safe on a clean boot. Done independently of
	// the persist-based iface reap below (the reject route can outlive the FakeIP
	// persist that disableFakeIPTun clears).
	//
	// The reject route is a kill-switch FLAG on the pool→OpkgTun route (stand-
	// verified), so its NDMS form is interface-bound. We can only target it when the
	// persisted index tells us the OpkgTun NDMS name; with no persisted index the
	// route cannot be addressed here (it is tied to the OpkgTun being reaped below,
	// so DeleteOpkgTun may cascade-remove it — verify cascade semantics on stand).
	if s.deps.StaticRoutes != nil {
		if poolNet, poolMask, derr := poolV4NetMask(s.deps.FakeIPTun.Inet4Range); derr == nil {
			sweepIface := ""
			if st := settings.FakeIP; st != nil && st.Provisioned {
				sweepIface = fakeIPNDMSName(st.Index)
			}
			if sweepIface != "" {
				if err := s.deps.StaticRoutes.RemoveStaticRoute(ctx, StaticRouteSpec{
					Network: poolNet, Mask: poolMask, Interface: sweepIface, Comment: fakeIPDrainComment,
				}); err != nil {
					s.appLog.Warn("fakeip-reap", sweepIface, "sweep stale drain reject route: "+err.Error())
				}
			}
		}
	}

	st := settings.FakeIP
	if st == nil || !st.Provisioned {
		return nil // nothing persisted to reap
	}
	ndmsName := fakeIPNDMSName(st.Index)
	if s.deps.OpkgTun == nil {
		// No provisioner (degraded/test): we can't confirm the iface is gone, so
		// KEEP the persist — clearing it would convert a tracked orphan into an
		// un-reapable persist-less one. The index isn't leaked: the allocator is
		// live-sourced (reads /sys + NDMS), so a still-live iface stays occupied.
		// A future boot with a real provisioner reaps it.
		return nil
	}
	if err := s.deps.OpkgTun.DeleteOpkgTun(ctx, ndmsName); err != nil {
		// Keep the persist on failure: returning without clearing it lets the
		// next boot retry the reap rather than leaking the orphan forever.
		return fmt.Errorf("reap opkgtun %s: %w", ndmsName, err)
	}
	s.appLog.Info("fakeip-reap", ndmsName, "removed orphaned fakeip OpkgTun (mode != fakeip-tun)")
	// Clear persist ONLY after a confirmed delete success (NDMS returns nil even
	// when the iface was already gone, i.e. idempotent), so the index frees.
	return s.deps.Settings.SetFakeIPState(nil)
}

// fakeIPReadyInputs derives the inputs the fakeip-tun readiness probes need
// from loaded settings + the static FakeIPTun params: the tun iface name (from
// the allocated OpkgTun index), the tun-side DNS address (the other /30 host,
// where sing-box's DNS server listens), and the fakeip v4 pool prefix. ok is
// false when fakeip is not provisioned or any field is unparseable, so callers
// can fail-closed without a fakeip branch firing on tproxy state.
func (s *ServiceImpl) fakeIPReadyInputs() (iface, dnsAddr string, fakeipNet netip.Prefix, ok bool) {
	if s.deps.Settings == nil {
		return "", "", netip.Prefix{}, false
	}
	settings, err := s.deps.Settings.Load()
	if err != nil || settings == nil || settings.FakeIP == nil || !settings.FakeIP.Provisioned {
		return "", "", netip.Prefix{}, false
	}
	iface = fakeIPIfaceName(settings.FakeIP.Index)
	dnsAddr, err = DeriveTunDNS(s.deps.FakeIPTun.TunAddr4)
	if err != nil {
		return "", "", netip.Prefix{}, false
	}
	fakeipNet, err = netip.ParsePrefix(s.deps.FakeIPTun.Inet4Range)
	if err != nil || !fakeipNet.Addr().Is4() {
		return "", "", netip.Prefix{}, false
	}
	return iface, dnsAddr, fakeipNet, true
}

func (s *ServiceImpl) waitForSingbox(ctx context.Context, timeout time.Duration) error {
	if s.deps.Singbox == nil {
		return nil
	}

	// Mode-aware readiness: read the mode INTERNALLY (the signature has test
	// callers and must not change). fakeip-tun has no inbound sockets, so the
	// tproxy socket probe never turns true for it — gate instead on process +
	// tun carrier (carrier=1 = sing-box attached the gvisor tun stack, the
	// structural "config is live" signal). The live .2→fakeip DNS answer is NO
	// longer in this gate (it tripped on resolv.conf attempts:1, stand-verified
	// 2026-06-15) — it is now a best-effort confirm after readiness in
	// enableFakeIPTun. See singboxReady for the full rationale.
	fakeIP := false
	if s.deps.Settings != nil {
		if settings, err := s.deps.Settings.Load(); err == nil && settings != nil {
			fakeIP = settings.SingboxRouter.RoutingMode == "fakeip-tun"
		}
	}

	deadline := time.Now().Add(timeout)
	const pollInterval = 100 * time.Millisecond
	for {
		if s.singboxReady(ctx, fakeIP) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sing-box did not come up within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// singboxReady reports whether sing-box is up for the active mode. tproxy:
// process + both inbound sockets bound. fakeip-tun: process + tun carrier.
//
// For fakeip-tun, carrier=1 IS the structural readiness signal: it means
// sing-box created and attached the gvisor tun stack from the fakeip config —
// the analog of "inbound socket bound" for tproxy, and it is fast and reliable.
//
// The live .2→fakeip DNS probe was DEMOTED out of this hard gate (stand-verified
// 2026-06-15): the Go resolver (net.Resolver{PreferGo:true}) HONORS the router's
// /etc/resolv.conf `options timeout:1 attempts:1`, so it does a single ~1s-bounded
// attempt with no retry. In the first seconds after sing-box starts the fakeip
// round-trip to .2 is occasionally slower than that, so the probe returned false
// on every poll and waitForSingbox timed out at 60s — falsely failing Enable even
// though sing-box was fully up (carrier=1) and fakeip worked. The DNS check now
// runs ONCE as a best-effort, logged confirmation AFTER readiness (see
// enableFakeIPTun), never as a flaky gate. ctx is unused now that the live DNS
// probe is out of the gate; kept on the signature for the tproxy/test callers.
func (s *ServiceImpl) singboxReady(_ context.Context, fakeIP bool) bool {
	running, _ := s.deps.Singbox.IsRunning()
	if !running {
		return false
	}
	if !fakeIP {
		return singboxListeningProbe()
	}
	// Only iface is needed for the carrier gate; dnsAddr/fakeipNet (which the
	// demoted DNS probe used) are derived later in enableFakeIPTun for the
	// best-effort confirm.
	iface, _, _, ok := s.fakeIPReadyInputs()
	if !ok {
		return false
	}
	return tunReadyProbe(iface)
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
		if err := s.deps.Singbox.Reload(); err != nil {
			return err
		}
	}
	if hadExisting == nil {
		_ = os.Remove(backupPath)
	}
	return nil
}

func cleanValidateError(err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\x1b[31m", "")
	msg = strings.ReplaceAll(msg, "\x1b[0m", "")
	if idx := strings.Index(msg, "FATAL"); idx >= 0 {
		msg = msg[idx+len("FATAL"):]
	}
	msg = strings.TrimSpace(msg)
	if idx := strings.Index(msg, ": exit status"); idx > 0 {
		msg = msg[:idx]
	}
	if idx := strings.Index(msg, "decode config at "); idx >= 0 {
		tail := msg[idx+len("decode config at "):]
		if j := strings.Index(tail, ": "); j > 0 {
			tail = tail[j+2:]
		}
		msg = "конфиг недопустим: " + tail
	}
	return strings.TrimSpace(msg)
}

// Enable is the USER-INITIATED router enable (HTTP handler + SwitchRoutingMode).
// It clears the sticky master-stop intent — an explicit enable is an explicit
// intent to run sing-box, which must override a prior master-Stop — then runs
// the idempotent provisioning. Drift-heal (Reconcile / reconcileFakeIPTun) must
// NOT clear the intent (the watchdog must respect a user's manual stop and not
// resurrect the daemon on drift), so it calls enableLocked(ctx, false) instead.
func (s *ServiceImpl) Enable(ctx context.Context) error {
	return s.enableLocked(ctx, true)
}

// enableLocked provisions the router under s.mu. clearManualStop gates the
// sticky-intent clear: true for user-initiated Enable, false for drift-heal
// reuse (Reconcile / reconcileFakeIPTun) which must honour a prior master-Stop.
func (s *ServiceImpl) enableLocked(ctx context.Context, clearManualStop bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate settings first — fail fast with a meaningful error before
	// attempting any kernel / iptables operations.
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return fmt.Errorf("router settings: %w", err)
	}

	// Explicit enable = explicit intent to run sing-box. Clear any sticky
	// manual-stop so the orchestrator cold-start (triggered by SetEnabled below)
	// isn't suppressed by shouldRun()=!IsManuallyStopped — otherwise Enable waits
	// the full boot window and fails with a misleading readiness timeout
	// (stand-found 2026-06-15, applies to BOTH tproxy and fakeip-tun). Gated to
	// user-initiated Enable: a drift-heal reconcile (clearManualStop=false) must
	// NOT wipe a user's master-Stop. The nil guard keeps test wirings that omit
	// Singbox working.
	if clearManualStop && s.deps.Singbox != nil {
		if err := s.deps.Singbox.ClearManualStop(); err != nil {
			return fmt.Errorf("clear manual-stop intent: %w", err)
		}
	}

	// fakeip-tun has an entirely separate provisioning path (OpkgTun + tun +
	// fakeip DNS + pool/CIDR routes) with its own rollback. The tproxy body
	// below stays byte-for-byte unchanged for RoutingMode=="tproxy".
	if sr.RoutingMode == "fakeip-tun" {
		sr.Enabled = true
		return s.enableFakeIPTun(ctx, settings, sr)
	}

	policyMode := sr.DeviceMode == "" || sr.DeviceMode == "policy"
	mark := ""
	if policyMode {
		if sr.PolicyName == "" {
			return ErrPolicyNotConfigured
		}
		mark, err = s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName)
		if err != nil || mark == "" {
			return fmt.Errorf("policy %q: %w", sr.PolicyName, ErrPolicyMissing)
		}
	}

	if err := s.prepareNetfilter(ctx); err != nil {
		return err
	}

	sr.Enabled = true

	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, sr.UDPTimeout)
	cfg.Outbounds = stripAutoManagedDirect(cfg.Outbounds)
	cfg.EnsureSystemRules(sr.SnifferEnabled)
	// Settings was already loaded above; revalidate here in case the
	// store is corrupted or hand-edited around a schema migration. We
	// fail Enable rather than apply a half-broken config — the user
	// sees a clean error in the UI and can fix it.
	if err := ValidateSingboxRouterSettings(sr); err != nil {
		return fmt.Errorf("router settings: %w", err)
	}
	cfg.EnsureRouteWAN(sr.WANAutoDetect, sr.WANInterface)

	// Promote SlotRouter to active FIRST so persistConfigDirect's
	// orch.Save targets the active path (it keys on the slot's enabled
	// flag). SetEnabled also triggers the orchestrator's debounced cold-
	// start — sing-box will read the active config we are about to write.
	// Legacy fallback (tests) keeps the explicit Start call.
	if s.deps.Orch != nil {
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, true); err != nil {
			return fmt.Errorf("orchestrator enable router: %w", err)
		}
	} else {
		if running, _ := s.deps.Singbox.IsRunning(); !running {
			if err := s.deps.Singbox.Start(); err != nil {
				return fmt.Errorf("sing-box start: %w", err)
			}
		}
	}
	// Direct write — no staging. Byte-equal short-circuit makes boot
	// recovery (Reconcile→Enable with iptables gone but active config
	// already on disk) a no-op write, which is what kills the phantom
	// "Несохранённые правки" banner that used to follow every reboot.
	if err := s.persistConfigDirect(ctx, cfg); err != nil {
		return err
	}

	// Wait for sing-box to be listening before iptables start redirecting
	// traffic to its TPROXY/REDIRECT ports. HARD fail (issue #221): if
	// sing-box never comes up — most commonly because a slot config is
	// rejected by `sing-box check` at load time — installing the AWGM-TPROXY
	// rule still redirects DNS:53 to 127.0.0.1:<proxy_port>, where nothing
	// is listening, and the router loses DNS until the user manually stops
	// awg-manager. The earlier "brief packet-drop blip vs no routing"
	// trade-off is wrong: a failed sing-box start turns the blip into a
	// permanent outage.
	//
	// Same env-var contract as singbox.maxSingboxBootWait — clamped to a
	// 60s floor (bootWaitWithFloor). Import-cycle (integration_test in parent
	// already pulls router) blocks reusing the parent helper directly.
	bootWait := bootWaitWithFloor()
	if err := s.waitForSingbox(ctx, bootWait); err != nil {
		return fmt.Errorf("%w: waited %s (%v)", ErrSingboxNotReady, bootWait, err)
	}

	// Collect WAN IPs BEFORE Install: the router's own public-IP
	// addresses on default-route interfaces become RETURN rules in
	// AWGM-TPROXY/AWGM-REDIRECT, preventing LAN-to-router-WAN-IP
	// traffic from looping back into sing-box. A collector failure
	// is fatal — installing without the exclusions would silently
	// expose the loop edge case to users.
	wanIPs, err := s.deps.WANIPCollector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect WAN IPs: %w", err)
	}

	// Discover LAN bridges that NDMS knows how to REDIRECT DNS for
	// (i.e. has _NDM_HOTSPOT_DNSREDIR rules on). DNS-NOPOLICY rules
	// re-mark mark=0 DNS up to one of those marks so the existing
	// REDIRECT picks it up and forwards to the per-policy ndnproxy.
	// We pass our policy mark so the picker avoids it — re-marking
	// default DNS up to the sing-box mark would route it via Policy1's
	// (permit-less) table and DNS would never resolve. Empty result =
	// no qualifying bridges = skip the DNS-NOPOLICY logic entirely.
	var lanBridges []LANBridgeDNSRedir
	if policyMode {
		lanBridges, _ = DiscoverLANBridges(ctx, mark)
		if len(lanBridges) == 0 {
			s.appLog.Warn("discover-lan-bridges", "", "no NDMS hotspot LAN bridges, DNS fallback skipped")
		}
	}

	var ingress []string
	if policyMode {
		ingress = s.resolveIngressInterfaces(ctx, sr.IngressInterfaces)
	}

	bypassUDP, bypassTCP, _ := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
	bypassSubnets, _ := resolveBypassSubnets(sr.BypassExtraSubnets)
	if err := s.deps.IPTables.Install(ctx, RestoreInputSpec{
		PolicyMark:        mark,
		MatchAll:          !policyMode,
		WANIPs:            wanIPs,
		LANBridges:        lanBridges,
		BypassUDPPorts:    bypassUDP,
		BypassTCPPorts:    bypassTCP,
		BypassCIDRs:       bypassSubnets,
		IngressInterfaces: ingress,
	}); err != nil {
		// Stop sing-box from listening on the now-orphan TPROXY port,
		// but DO NOT corrupt the persisted user config. With orchestrator
		// wired we just park the slot back under disabled/ — sing-box
		// stops seeing it on next reload, the file's content (including
		// tproxy-in) is preserved verbatim. Without the orchestrator
		// (legacy fallback) the only recourse is to strip the inbound.
		if s.deps.Orch != nil {
			_ = s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false)
		} else {
			cfg.Inbounds = filterTProxyInbound(cfg.Inbounds)
			_ = s.persistConfigDirect(ctx, cfg)
		}
		return fmt.Errorf("iptables install: %w", err)
	}
	s.currentMark = mark
	s.currentWANIPs = wanIPs
	s.currentLANBridges = lanBridges
	s.currentBypassPresets = sr.BypassPresets
	s.currentBypassExtraPorts = sr.BypassExtraPorts
	s.currentBypassExtraSubnets = sr.BypassExtraSubnets
	s.currentIngress = ingress
	s.netfilterStateKnown = true

	settings.SingboxRouter = sr
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}

	s.emitStatus(ctx)
	return nil
}

func filterTProxyInbound(in []Inbound) []Inbound {
	out := make([]Inbound, 0, len(in))
	for _, i := range in {
		if i.Tag != "tproxy-in" {
			out = append(out, i)
		}
	}
	return out
}

// healTProxyInbound checks the persisted router config and re-adds the
// tproxy-in inbound if missing. Idempotent. Used by Reconcile to
// recover from a prior failed-Install rollback (which used to strip
// the inbound destructively).
func (s *ServiceImpl) healTProxyInbound(ctx context.Context, udpTimeout string) error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "tproxy-in" {
			return nil // already present, nothing to do
		}
	}
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds, udpTimeout)
	// System self-heal — direct write, no staging UI.
	return s.persistConfigDirect(ctx, cfg)
}

// ensureTProxyInbound enforces the SKeen-style split: tproxy-in
// handles UDP only, redirect-in handles TCP. TPROXY for TCP relies on
// `-m socket --transparent` to deliver established-connection packets
// to sing-box's accept()ed transparent socket, but that match
// evaluates to 0 on Keenetic 4.9-ndm-5 — established TCP packets fall
// through to the listener and get RST. NAT REDIRECT sidesteps the
// problem: conntrack records the DNAT for SYN, established packets
// are auto-translated.
//
// Both inbounds bind to 0.0.0.0 because iptables REDIRECT rewrites
// the packet destination to the *primary IP of the inbound interface*
// (e.g. 10.10.10.1 on br0), NOT to 127.0.0.1. A listener on 127.0.0.1
// would never see redirected packets — kernel emits RST. SKeen uses
// "::" for the same reason.
const inboundListen = "0.0.0.0"

// DefaultUDPTimeout is the fallback UDP session timeout for tproxy-in when
// the user has not configured a custom value. 3 minutes matches the original
// sing-box default, but may cause games and other UDP applications that go
// quiet for longer than 3 minutes to drop their session unexpectedly.
const DefaultUDPTimeout = "3m0s"

// resolveUDPTimeout returns the effective UDP timeout string: the user value
// when non-empty, otherwise DefaultUDPTimeout.
func resolveUDPTimeout(configured string) string {
	if configured != "" {
		return configured
	}
	return DefaultUDPTimeout
}

func ensureTProxyInbound(in []Inbound, udpTimeout string) []Inbound {
	effective := resolveUDPTimeout(udpTimeout)
	hasTProxy := false
	hasRedirect := false
	for i := range in {
		switch in[i].Tag {
		case "tproxy-in":
			hasTProxy = true
			// Force UDP-only on existing entry. Older configs had no
			// `network` field which means TCP+UDP — that's the broken
			// behaviour we're moving away from.
			if in[i].Network != "udp" {
				in[i].Network = "udp"
			}
			if !in[i].UDPFragment {
				in[i].UDPFragment = true
			}
			// Always apply the effective timeout — user may have changed it.
			in[i].UDPTimeout = effective
			// tcp_fast_open is meaningless on a UDP-only inbound.
			if in[i].TCPFastOpen {
				in[i].TCPFastOpen = false
			}
			// Strip RoutingMark — see history note below.
			if in[i].RoutingMark != 0 {
				in[i].RoutingMark = 0
			}
			if in[i].Listen != inboundListen {
				in[i].Listen = inboundListen
			}
		case "redirect-in":
			hasRedirect = true
			if !in[i].TCPFastOpen {
				in[i].TCPFastOpen = true
			}
			if in[i].Listen != inboundListen {
				in[i].Listen = inboundListen
			}
		}
	}
	out := in
	if !hasTProxy {
		out = append([]Inbound{{
			Type:        "tproxy",
			Tag:         "tproxy-in",
			Listen:      inboundListen,
			ListenPort:  TPROXYPort,
			Network:     "udp",
			UDPFragment: true,
			UDPTimeout:  effective,
		}}, out...)
	}
	if !hasRedirect {
		out = append([]Inbound{{
			Type:        "redirect",
			Tag:         "redirect-in",
			Listen:      inboundListen,
			ListenPort:  RedirectPort,
			TCPFastOpen: true,
		}}, out...)
	}
	return out
}

func (s *ServiceImpl) emitStatus(ctx context.Context) {
	if s.deps.Events == nil {
		return
	}
	status, _ := s.GetStatus(ctx)
	s.deps.Events.Publish("singbox-router:status", status)
}

func (s *ServiceImpl) emitStagingEvent(reason string) {
	if s.deps.Bus == nil {
		return
	}
	s.deps.Bus.Publish("resource:invalidated", map[string]any{
		"resource": "singbox.router.staging",
		"reason":   reason,
	})
}

func (s *ServiceImpl) emitRulesEvent() {
	if s.deps.Bus == nil {
		return
	}
	s.deps.Bus.Publish("resource:invalidated", map[string]any{
		"resource": "singbox.router.rules",
	})
}

func (s *ServiceImpl) GetStatus(ctx context.Context) (Status, error) {
	settings, _ := s.deps.Settings.Load()
	sr := storage.SingboxRouterSettings{}
	if settings != nil {
		sr, _ = NormalizeSingboxRouterSettings(settings.SingboxRouter)
	}
	cfg, _ := s.loadRouterConfigForMode(sr.RoutingMode)
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	awgCount := 0
	compCount := len(cfg.CompositeOutbounds())

	policyExists := false
	policyMark := ""
	deviceCount := 0
	if sr.PolicyName != "" && s.deps.Policies != nil {
		if mark, err := s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName); err == nil && mark != "" {
			policyExists = true
			policyMark = mark
		}
		if devices, err := s.deps.Policies.ListDevicesForPolicy(ctx, sr.PolicyName); err == nil {
			for _, d := range devices {
				if d.Bound {
					deviceCount++
				}
			}
		}
	}

	// One -S probe per table yields both chain existence and jump presence.
	// A probe error is treated as "unknown" — installed/jumps stay false but
	// the badge self-corrects on the next status read (no side effect here,
	// unlike the reconcile path which must not reinstall on a transient error).
	installed, jumps, _ := s.deps.IPTables.Probe(ctx)
	// Active = interception path truly live, computed per routing mode.
	var active bool
	if sr.RoutingMode == "fakeip-tun" {
		// fakeip-tun has no iptables jumps and no inbound sockets. Steady-state
		// liveness = process up + tun carrier + the fakeip pool auto-route
		// present (the honest structural check — the fakeip equivalent of
		// "TPROXY jumps present"). No live DNS query here: that would add
		// per-poll latency, and the route-presence check is enough once Enable
		// has finished wiring routes (waitForSingbox already gated on live DNS).
		running, _ := s.deps.Singbox.IsRunning()
		if iface, _, fakeipNet, ok := s.fakeIPReadyInputs(); ok {
			active = running && tunReadyProbe(iface) && fakeIPPoolRoutePresent(iface, fakeipNet)
		}
	} else {
		// tproxy: chains + PREROUTING jumps + sing-box listening on both inbound sockets.
		active = jumps && singboxListeningProbe()
	}
	// Surface the captured sing-box fatal reason only when the engine is
	// meant to be up but isn't (СБОЙ). lastError is cleared by the operator
	// on a successful (re)start, so a healthy engine reports empty.
	lastError := ""
	if sr.Enabled && !active && s.deps.Singbox != nil {
		lastError = s.deps.Singbox.LastError()
	}
	issues := s.computeIssues(cfg)
	// fakeip-tun active iface: surface the provisioned kernel iface name
	// ("opkgtun<idx>") so the UI can show it in the engine-settings panel. Only
	// when in fakeip-tun mode AND actually provisioned (persisted FakeIPState);
	// empty otherwise.
	var fakeIPIface string
	fakeIPDns := ""
	fakeIPTunAddr := ""
	if sr.RoutingMode == "fakeip-tun" && settings != nil &&
		settings.FakeIP != nil && settings.FakeIP.Provisioned {
		fakeIPIface = fakeIPIfaceName(settings.FakeIP.Index)
		if d, derr := DeriveTunDNS(s.deps.FakeIPTun.TunAddr4); derr == nil {
			fakeIPDns = d
		}
		if addr, _, aerr := splitCIDRToAddrMask(s.deps.FakeIPTun.TunAddr4); aerr == nil {
			fakeIPTunAddr = addr
		}
	}
	return Status{
		Enabled:                sr.Enabled,
		Installed:              installed,
		Active:                 active,
		NetfilterAvailable:     IsNetfilterAvailable(),
		NetfilterComponentName: "Модули ядра подсистемы сетевой фильтрации",
		TProxyTargetAvailable:  IsTProxyTargetAvailable(ctx),
		PolicyName:             sr.PolicyName,
		PolicyMark:             policyMark,
		PolicyExists:           policyExists,
		DeviceMode:             sr.DeviceMode,
		SnifferEnabled:         sr.SnifferEnabled,
		DeviceCount:            deviceCount,
		RuleCount:              len(cfg.Route.Rules),
		RuleSetCount:           len(cfg.Route.RuleSet),
		OutboundAWGCount:       awgCount,
		OutboundCompositeCount: compCount,
		Final:                  cfg.Route.Final,
		FakeIPIface:            fakeIPIface,
		FakeIPDns:              fakeIPDns,
		FakeIPTunAddr:          fakeIPTunAddr,
		Issues:                 issues,
		LastError:              lastError,
	}, nil
}

func (s *ServiceImpl) Disable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// fakeip-tun teardown is an entirely separate path (no iptables; opkgtun +
	// pool/CIDR routes + a fail-closed drain). Dispatch by mode before the
	// tproxy body so the tproxy path below stays byte-for-byte unchanged for
	// RoutingMode=="tproxy".
	dispatchSettings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	// Dispatch on the RAW persisted RoutingMode, NOT the normalized value: a
	// Normalize error (corrupt/hand-edited settings) would otherwise mis-route a
	// fakeip-tun teardown into the tproxy body, orphaning the opkgtun/routes.
	// A raw string compare keeps the fakeip branch independent of normalize.
	if dispatchSettings.SingboxRouter.RoutingMode == "fakeip-tun" {
		return s.disableFakeIPTun(ctx, dispatchSettings)
	}

	if err := s.deps.IPTables.Uninstall(ctx); err != nil {
		s.appLog.Warn("uninstall", "", err.Error())
	}
	s.currentMark = ""
	s.currentWANIPs = nil
	s.currentLANBridges = nil
	s.currentBypassPresets = nil
	s.currentBypassExtraPorts = ""
	s.currentBypassExtraSubnets = ""
	s.currentIngress = nil
	s.netfilterStateKnown = false

	if s.deps.Orch != nil {
		// Move 20-router.json under disabled/ — sing-box's non-recursive
		// -C config.d does not see it after the next reload, so the
		// tproxy inbound, route rules, DNS rules and composite outbounds
		// all disappear from the merged config in one atomic rename.
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
			s.appLog.Warn("orch-disable", "", err.Error())
		}
	} else {
		// Legacy fallback: strip the tproxy inbound in place so
		// the running sing-box stops accepting on the TPROXY port
		// after the persistConfigDirect reload.
		cfg, err := s.loadRouterConfig()
		if err == nil && cfg != nil {
			filtered := make([]Inbound, 0, len(cfg.Inbounds))
			for _, in := range cfg.Inbounds {
				if in.Tag != "tproxy-in" {
					filtered = append(filtered, in)
				}
			}
			cfg.Inbounds = filtered
			_ = s.persistConfigDirect(ctx, cfg)
		}
	}

	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	settings.SingboxRouter.Enabled = false
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}

	s.emitStatus(ctx)
	return nil
}

func (s *ServiceImpl) Reconcile(ctx context.Context) error {
	// A routing-mode switch (SwitchRoutingMode) holds transitionMu across its
	// Disable→persist→Enable sequence, during which the persisted state is
	// transiently half-flipped. Reconcile is a periodic heal — if a switch is in
	// flight, skip this tick rather than act on the in-between state and race the
	// switch's own (possibly rolling-back) Enable/Disable. TryLock: never block the
	// scheduler; just defer the heal one tick.
	if !s.transitionMu.TryLock() {
		return nil
	}
	defer s.transitionMu.Unlock()
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr, err := NormalizeSingboxRouterSettings(settings.SingboxRouter)
	if err != nil {
		return err
	}
	// fakeip-tun installs NO iptables, so the tproxy switch below (keyed on
	// IPTables.IsInstalled/HasAnyInstalled) would always read "not installed"
	// and route every tick to Enable. Dispatch by mode FIRST so the tproxy
	// switch stays byte-for-byte unchanged for RoutingMode=="tproxy".
	if sr.RoutingMode == "fakeip-tun" {
		return s.reconcileFakeIPTun(ctx, sr)
	}
	installedComplete := s.deps.IPTables.IsInstalled(ctx)
	installedAny := s.deps.IPTables.HasAnyInstalled(ctx)
	switch {
	case sr.Enabled && !installedComplete:
		// Drift-heal, NOT user-initiated: must honour a prior master-Stop, so
		// do not clear the sticky intent (clearManualStop=false).
		return s.enableLocked(ctx, false)
	case !sr.Enabled && installedAny:
		return s.Disable(ctx)
	case sr.Enabled && installedComplete:
		return s.reconcileInstalled(ctx, sr)
	}
	return nil
}

// reconcileInstalled handles the "Enabled && installed" branch:
// detect mark or WAN-IP changes and re-Install. Extracted from Reconcile
// to keep the decision tree testable without stubbing IsInstalled.
func (s *ServiceImpl) reconcileInstalled(ctx context.Context, sr storage.SingboxRouterSettings) error {
	sr, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		return err
	}
	policyMode := sr.DeviceMode == "" || sr.DeviceMode == "policy"
	mark := ""
	if policyMode {
		mark, err = s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName)
		if err != nil || mark == "" {
			// Policy gone upstream — fail-safe disable, no auto-recovery.
			return s.Disable(ctx)
		}
	}
	wanIPs, err := s.deps.WANIPCollector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect WAN IPs: %w", err)
	}

	markChanged := mark != s.currentMark
	wanIPsChanged := !slices.Equal(s.currentWANIPs, wanIPs)
	var lanBridges []LANBridgeDNSRedir
	if policyMode {
		lanBridges, _ = DiscoverLANBridges(ctx, mark)
	}
	lanBridgesChanged := !equalLANBridges(s.currentLANBridges, lanBridges)
	var ingress []string
	if policyMode {
		ingress = s.resolveIngressInterfaces(ctx, sr.IngressInterfaces)
	}
	ingressChanged := !slices.Equal(s.currentIngress, ingress)
	bypassPresetsChanged := !slices.Equal(s.currentBypassPresets, sr.BypassPresets)
	bypassExtraChanged := s.currentBypassExtraPorts != sr.BypassExtraPorts
	bypassSubnetsChanged := s.currentBypassExtraSubnets != sr.BypassExtraSubnets

	// After a daemon restart or upgrade the old awg-manager process died
	// with no chance to run Uninstall, so stale AWGM chains, ip rules
	// and ip routes may remain from the old process. netfilterStateKnown
	// starts false on every fresh ServiceImpl, so the very first
	// reconcileInstalled after startup always forces a full re-install
	// regardless of what IsInstalled reports.
	forceInitialSync := !s.netfilterStateKnown
	// Self-heal: chains can survive while PREROUTING jumps get wiped (NDMS
	// rebuilds PREROUTING on reconfig), leaving the engine "installed" but
	// intercepting nothing. The netfilter.d hook restores them immediately on
	// the NDMS reload; this is the slower secondary net. On a probe error treat
	// the state as unknown and DO NOT reinstall — a transient `-S` failure
	// during an NDMS reload must not trigger a needless rebuild.
	_, jumps, probeErr := s.deps.IPTables.Probe(ctx)
	jumpsMissing := probeErr == nil && !jumps
	needsInstall := forceInitialSync || jumpsMissing || markChanged || wanIPsChanged || lanBridgesChanged || ingressChanged || bypassPresetsChanged || bypassExtraChanged || bypassSubnetsChanged

	if needsInstall {
		if forceInitialSync {
			s.appLog.Info("reconcile", "", "first after daemon start — reinstalling netfilter rules")
		} else if jumpsMissing {
			s.appLog.Warn("reconcile", "", "PREROUTING jumps missing while chains present — reinstalling to restore interception")
		}

		if err := s.prepareNetfilter(ctx); err != nil {
			return err
		}

		bypassUDP, bypassTCP, _ := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
		bypassSubnets, _ := resolveBypassSubnets(sr.BypassExtraSubnets)
		s.mu.Lock()
		if err := s.deps.IPTables.Install(ctx, RestoreInputSpec{
			PolicyMark:        mark,
			MatchAll:          !policyMode,
			WANIPs:            wanIPs,
			LANBridges:        lanBridges,
			BypassUDPPorts:    bypassUDP,
			BypassTCPPorts:    bypassTCP,
			BypassCIDRs:       bypassSubnets,
			IngressInterfaces: ingress,
		}); err != nil {
			s.mu.Unlock()
			return err
		}
		s.currentMark = mark
		s.currentWANIPs = wanIPs
		s.currentLANBridges = lanBridges
		s.currentBypassPresets = sr.BypassPresets
		s.currentBypassExtraPorts = sr.BypassExtraPorts
		s.currentBypassExtraSubnets = sr.BypassExtraSubnets
		s.currentIngress = ingress
		s.netfilterStateKnown = true
		s.mu.Unlock()
	}

	// Self-heal: a previous Install rollback or upgrade hop may
	// have left 20-router.json without the tproxy-in inbound. Re-add
	// it idempotently so sing-box keeps listening on TPROXYPort.
	if err := s.healTProxyInbound(ctx, sr.UDPTimeout); err != nil {
		s.appLog.Warn("heal-tproxy", "", err.Error())
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

// IsOutboundTagInUse reports whether tag is already occupied by any outbound
// catalog visible to singbox-router.
func (s *ServiceImpl) IsOutboundTagInUse(ctx context.Context, tag string) bool {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		cfg = NewEmptyConfig()
	}
	return s.isKnownOutboundTag(ctx, tag, cfg)
}

// RenameExternalOutboundTag rewrites references to a base outbound owned by
// another producer (for example a single sing-box tunnel in 10-tunnels.json).
// It updates both live/disabled router config and the pending draft, if any.
func (s *ServiceImpl) RenameExternalOutboundTag(ctx context.Context, oldTag, newTag string) error {
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if oldTag == "" || newTag == "" || oldTag == newTag {
		return nil
	}
	if s.deps.Orch == nil {
		return s.withConfig(ctx, "all", func(c *RouterConfig) error {
			c.renameOutboundReferences(oldTag, newTag)
			return nil
		})
	}

	configDir := s.deps.Orch.ConfigDir()
	activePath := filepath.Join(configDir, "20-router.json")
	disabledPath := filepath.Join(configDir, "disabled", "20-router.json")
	pendingPath := filepath.Join(configDir, "pending", "20-router.json")
	changed := false

	if data, ok, err := rewriteRouterConfigOutboundRefs(activePath, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := s.deps.Orch.Save(orchestrator.SlotRouter, data); err != nil {
			return err
		}
		changed = true
	}
	if data, ok, err := rewriteRouterConfigOutboundRefs(disabledPath, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := writeRouterConfigAtomic(disabledPath, data); err != nil {
			return err
		}
		changed = true
	}
	if data, ok, err := rewriteRouterConfigOutboundRefs(pendingPath, oldTag, newTag); err != nil {
		return err
	} else if ok {
		if err := s.deps.Orch.SaveDraft(orchestrator.SlotRouter, data); err != nil {
			return err
		}
		s.emitStagingEvent("staged")
		changed = true
	}
	if changed {
		if cfg, err := s.loadRouterConfig(); err == nil {
			s.emitCfgEvent("all", s.ruleSetMaterializer().restoreConfig(cfg))
		}
	}
	return nil
}

func rewriteRouterConfigOutboundRefs(path, oldTag, newTag string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	cfg := NewEmptyConfig()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(cfg.outboundReferences(oldTag)) == 0 {
		return nil, false, nil
	}
	cfg.renameOutboundReferences(oldTag, newTag)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func writeRouterConfigAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *ServiceImpl) ListRules(ctx context.Context) ([]Rule, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return s.ruleSetMaterializer().restoreConfig(cfg).Route.Rules, nil
}

func (s *ServiceImpl) AddRule(ctx context.Context, r Rule) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.AddRule(r) })
}

func (s *ServiceImpl) UpdateRule(ctx context.Context, index int, r Rule) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.UpdateRule(index, r) })
}

func (s *ServiceImpl) DeleteRule(ctx context.Context, index int) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.DeleteRule(index) })
}

func (s *ServiceImpl) MoveRule(ctx context.Context, from, to int) error {
	return s.withConfig(ctx, "rules", func(c *RouterConfig) error { return c.MoveRule(from, to) })
}

func (s *ServiceImpl) SetRouteFinal(ctx context.Context, tag string) error {
	return s.withConfig(ctx, "route", func(c *RouterConfig) error {
		if !s.isKnownOutboundTag(ctx, tag, c) {
			return fmt.Errorf("unknown outbound tag %q for route.final", tag)
		}
		return c.SetRouteFinal(tag)
	})
}

// isKnownOutboundTag returns true if tag is a sing-box built-in or matches
// an outbound from any known catalog (router composites, subscription
// composites, AWG, sing-box tunnels).
func (s *ServiceImpl) isKnownOutboundTag(ctx context.Context, tag string, cfg *RouterConfig) bool {
	if tag == "direct" || tag == "block" || tag == "dns" {
		return true
	}
	// Router-managed composites
	for _, o := range cfg.Outbounds {
		if o.Tag == tag {
			return true
		}
	}
	// Subscription composites (40-subscriptions.json)
	if s.deps.SubscriptionComposites != nil {
		for _, o := range s.deps.SubscriptionComposites.ListSubscriptionComposites() {
			if o.Tag == tag {
				return true
			}
		}
	}
	// AWG-direct outbounds (managed + system)
	if s.deps.AWGTags != nil {
		if tags, err := s.deps.AWGTags.ListTags(ctx); err == nil {
			for _, t := range tags {
				if t.Tag == tag {
					return true
				}
			}
		}
	}
	// Sing-box tunnels (10-tunnels.json)
	if s.deps.SingboxTunnels != nil {
		if tags, err := s.deps.SingboxTunnels.ListTunnelTags(ctx); err == nil {
			for _, t := range tags {
				if t == tag {
					return true
				}
			}
		}
	}
	return false
}

func (s *ServiceImpl) ListRuleSets(ctx context.Context) ([]RuleSet, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	restored := s.ruleSetMaterializer().restoreConfig(cfg)
	return restored.Route.RuleSet, nil
}

func (s *ServiceImpl) AddRuleSet(ctx context.Context, rs RuleSet) error {
	if rs.Type == "" {
		rs.Type = "remote"
	}
	if rs.Format == "" && rs.Type != "inline" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" && rs.Type == "remote" {
		rs.UpdateInterval = "24h"
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.AddRuleSet(rs) })
}

func (s *ServiceImpl) UpdateRuleSet(ctx context.Context, tag string, rs RuleSet) error {
	if rs.Type == "" {
		rs.Type = "remote"
	}
	if rs.Format == "" && rs.Type != "inline" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" && rs.Type == "remote" {
		rs.UpdateInterval = "24h"
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.UpdateRuleSet(tag, rs) })
}

func (s *ServiceImpl) DeleteRuleSet(ctx context.Context, tag string, force bool) error {
	inlineTag := tag
	if base, ok := inlineTagFromSRSTag(tag); ok {
		inlineTag = base
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error {
		if err := c.DeleteRuleSet(inlineTag, force); err != nil {
			return err
		}
		if s.deps.Orch == nil {
			s.ruleSetMaterializer().removeInlineArtifacts(inlineTag)
		}
		return nil
	})
}

func (s *ServiceImpl) ListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	own := cfg.CompositeOutbounds()
	out := make([]CompositeOutboundView, 0, len(own))
	for _, o := range own {
		out = append(out, CompositeOutboundView{Outbound: o, Source: "router"})
	}
	if s.deps.SubscriptionComposites != nil {
		for _, o := range s.deps.SubscriptionComposites.ListSubscriptionComposites() {
			out = append(out, CompositeOutboundView{Outbound: o, Source: "subscription"})
		}
	}
	return out, nil
}

func (s *ServiceImpl) AddCompositeOutbound(ctx context.Context, o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		if err := s.validateBindInterface(ctx, o.BindInterface); err != nil {
			return err
		}
	}
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.AddCompositeOutbound(o) })
}

func (s *ServiceImpl) UpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error {
	if strings.EqualFold(o.Type, "direct") {
		if err := s.validateBindInterface(ctx, o.BindInterface); err != nil {
			return err
		}
	}
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.UpdateCompositeOutbound(tag, o) })
}

func (s *ServiceImpl) DeleteCompositeOutbound(ctx context.Context, tag string, force bool) error {
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.DeleteCompositeOutbound(tag, force) })
}

func (s *ServiceImpl) ListPresets() ([]Preset, error) {
	return listRouterPresets(s.deps.PresetCatalog)
}

func (s *ServiceImpl) ApplyPreset(ctx context.Context, presetID, outboundTag string) error {
	p, err := findRouterPreset(s.deps.PresetCatalog, presetID)
	if err != nil {
		return err
	}
	return s.withConfig(ctx, "status", func(c *RouterConfig) error {
		return ApplyPresetToConfig(c, p, outboundTag)
	})
}

func (s *ServiceImpl) GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error) {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return storage.SingboxRouterSettings{}, err
	}
	return NormalizeSingboxRouterSettings(settings.SingboxRouter)
}

func (s *ServiceImpl) UpdateSettings(ctx context.Context, sr storage.SingboxRouterSettings) error {
	normalized, err := NormalizeSingboxRouterSettings(sr)
	if err != nil {
		return err
	}
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	settings.SingboxRouter = normalized
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}
	return s.Reconcile(ctx)
}

// ValidateSingboxRouterSettings enforces the WAN-binding discriminator:
//   - WANAutoDetect=true   && WANInterface==""    → OK
//   - WANAutoDetect=false  && WANInterface!=""    → OK
//   - WANAutoDetect=true   && WANInterface!=""    → error (contradictory)
//   - WANAutoDetect=false  && WANInterface==""    → error (no target)
//
// This guards both the storage layer (UpdateSettings) and the apply
// path (Enable → EnsureRouteWAN) so an invalid state cannot reach
// sing-box config either through the API or through a hand-edited
// settings.json.
func NormalizeSingboxRouterSettings(sr storage.SingboxRouterSettings) (storage.SingboxRouterSettings, error) {
	if sr.DeviceMode == "" {
		sr.DeviceMode = "policy"
	}
	switch sr.DeviceMode {
	case "policy", "all":
	default:
		return sr, fmt.Errorf("deviceMode must be %q or %q, got %q", "policy", "all", sr.DeviceMode)
	}
	if sr.RoutingMode == "" {
		sr.RoutingMode = "tproxy"
	}
	if sr.RoutingMode != "tproxy" && sr.RoutingMode != "fakeip-tun" {
		return sr, fmt.Errorf("invalid routingMode %q (want tproxy|fakeip-tun)", sr.RoutingMode)
	}
	if sr.WANAutoDetect && sr.WANInterface != "" {
		return sr, fmt.Errorf("wanAutoDetect=true requires wanInterface to be empty (got %q)", sr.WANInterface)
	}
	if !sr.WANAutoDetect && sr.WANInterface == "" {
		return sr, fmt.Errorf("wanAutoDetect=false requires wanInterface to be set to a kernel interface name")
	}
	for _, name := range sr.BypassPresets {
		if _, ok := knownPresets[name]; !ok {
			return sr, fmt.Errorf("unknown bypass preset %q", name)
		}
	}
	if _, _, err := parseExtraPorts(sr.BypassExtraPorts); err != nil {
		return sr, fmt.Errorf("bypassExtraPorts: %w", err)
	}
	if _, err := resolveBypassSubnets(sr.BypassExtraSubnets); err != nil {
		return sr, fmt.Errorf("bypassExtraSubnets: %w", err)
	}
	if err := validateIngressRefs(sr.IngressInterfaces); err != nil {
		return sr, err
	}
	if err := normalizeFakeIPSettings(&sr); err != nil {
		return sr, err
	}
	if sr.UDPTimeout != "" {
		if _, err := time.ParseDuration(sr.UDPTimeout); err != nil {
			return sr, fmt.Errorf("udpTimeout: invalid duration %q: %w", sr.UDPTimeout, err)
		}
	}
	return sr, nil
}

// fakeIPMTUMin / fakeIPMTUMax bound the user-editable tun MTU. 576 is the IPv4
// minimum-reassembly floor; 9000 is jumbo-frame ceiling. Outside this range
// sing-tun behaves unpredictably, so reject early.
const (
	fakeIPMTUMin = 576
	fakeIPMTUMax = 9000
)

// normalizeFakeIPSettings defaults the user-editable fakeip engine fields from
// DefaultFakeIPTunParams (single source of truth) when empty/zero, then
// validates them. Per spec, an empty FakeIPPool6 is defaulted to the v6 pool
// (so a fresh install gets dual-stack); v6 is disabled at a higher layer, not
// by persisting "" here. Idempotent: re-running on a normalized struct is a
// fixed point.
func normalizeFakeIPSettings(sr *storage.SingboxRouterSettings) error {
	def := DefaultFakeIPTunParams()
	if sr.FakeIPStack == "" {
		sr.FakeIPStack = "gvisor"
	}
	if sr.FakeIPStack != "gvisor" && sr.FakeIPStack != "system" {
		return fmt.Errorf("fakeipStack must be %q or %q, got %q", "gvisor", "system", sr.FakeIPStack)
	}
	if sr.FakeIPPool4 == "" {
		sr.FakeIPPool4 = def.Inet4Range
	}
	if sr.FakeIPPool6 == "" {
		sr.FakeIPPool6 = def.Inet6Range
	}
	if sr.FakeIPMTU == 0 {
		sr.FakeIPMTU = def.MTU
	}
	// v4 pool must parse as an IPv4 prefix.
	if p, err := netip.ParsePrefix(sr.FakeIPPool4); err != nil {
		return fmt.Errorf("fakeipPool4: invalid CIDR %q: %w", sr.FakeIPPool4, err)
	} else if !p.Addr().Is4() {
		return fmt.Errorf("fakeipPool4: %q is not IPv4", sr.FakeIPPool4)
	}
	// v6 pool: empty disables v6; otherwise it must parse as an IPv6 prefix.
	if sr.FakeIPPool6 != "" {
		if p, err := netip.ParsePrefix(sr.FakeIPPool6); err != nil {
			return fmt.Errorf("fakeipPool6: invalid CIDR %q: %w", sr.FakeIPPool6, err)
		} else if p.Addr().Is4() {
			return fmt.Errorf("fakeipPool6: %q is not IPv6", sr.FakeIPPool6)
		}
	}
	if sr.FakeIPMTU < fakeIPMTUMin || sr.FakeIPMTU > fakeIPMTUMax {
		return fmt.Errorf("fakeipMtu %d out of range [%d, %d]", sr.FakeIPMTU, fakeIPMTUMin, fakeIPMTUMax)
	}
	return nil
}

func validateIngressRefs(refs []string) error {
	for _, ref := range refs {
		if !strings.HasPrefix(ref, "managed:") && !strings.HasPrefix(ref, "iface:") {
			return fmt.Errorf("ingress interface ref %q must be prefixed managed: or iface:", ref)
		}
		if strings.TrimSpace(strings.SplitN(ref, ":", 2)[1]) == "" {
			return fmt.Errorf("ingress interface ref %q has empty target", ref)
		}
	}
	return nil
}

func ValidateSingboxRouterSettings(sr storage.SingboxRouterSettings) error {
	_, err := NormalizeSingboxRouterSettings(sr)
	return err
}

func (s *ServiceImpl) ListWANInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	if s.deps.WANInterfaces == nil {
		return []WANInterfaceInfo{}, nil
	}
	return s.deps.WANInterfaces.ListWAN(ctx)
}

// ListBindableInterfaces returns interfaces a user can bind a direct
// outbound to. Empty when no lister is wired.
func (s *ServiceImpl) ListBindableInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	if s.deps.BindableInterfaces == nil {
		return []WANInterfaceInfo{}, nil
	}
	return s.deps.BindableInterfaces.ListBindable(ctx)
}

// ListIngressEligibleInterfaces возвращает интерфейсы, пригодные для
// ingress-scope: bindable минус WAN минус LAN-бриджи (по Type). Для UI
// router-страницы (мультиселект).
func (s *ServiceImpl) ListIngressEligibleInterfaces(ctx context.Context) ([]WANInterfaceInfo, error) {
	bindable, err := s.ListBindableInterfaces(ctx)
	if err != nil {
		return nil, err
	}
	wan, _ := s.ListWANInterfaces(ctx)
	wanNames := map[string]bool{}
	for _, w := range wan {
		wanNames[w.Name] = true
	}
	out := make([]WANInterfaceInfo, 0, len(bindable))
	for _, i := range bindable {
		if wanNames[i.Name] || strings.EqualFold(i.Type, "Bridge") {
			continue
		}
		out = append(out, i)
	}
	return out, nil
}

// validateBindInterface ensures name refers to a bindable interface. With
// no lister wired (tests / minimal deployments) it is permissive.
func (s *ServiceImpl) validateBindInterface(ctx context.Context, name string) error {
	if s.deps.BindableInterfaces == nil {
		return nil
	}
	ifaces, err := s.deps.BindableInterfaces.ListBindable(ctx)
	if err != nil {
		return err
	}
	for _, i := range ifaces {
		if i.Name == name {
			return nil
		}
	}
	return fmt.Errorf("bind_interface %q is not a selectable interface", name)
}

func (s *ServiceImpl) computeIssues(cfg *RouterConfig) []Issue {
	var issues []Issue
	outboundTags := make(map[string]struct{})
	for _, o := range cfg.Outbounds {
		outboundTags[o.Tag] = struct{}{}
	}
	// AWG-direct outbounds live in 15-awg.json owned by awgoutbounds —
	// add them to the validation set so rules referencing awg-{id} tags
	// don't get flagged as orphans.
	if s.deps.AWGTags != nil {
		if awgTags, err := s.deps.AWGTags.ListTags(context.Background()); err == nil {
			for _, t := range awgTags {
				outboundTags[t.Tag] = struct{}{}
			}
		}
	}
	// Sing-box tunnels live in 10-tunnels.json owned by internal/singbox.
	// Their tags (e.g. "veesp" for a VLESS outbound) are valid route
	// targets but invisible to a router-only view of cfg.Outbounds.
	if s.deps.SingboxTunnels != nil {
		if tags, err := s.deps.SingboxTunnels.ListTunnelTags(context.Background()); err == nil {
			for _, tag := range tags {
				outboundTags[tag] = struct{}{}
			}
		}
	}
	// Subscription composites live in 40-subscriptions.json owned by subscription slot.
	// Their tags are valid route targets but invisible to a router-only view of cfg.Outbounds.
	if s.deps.SubscriptionComposites != nil {
		for _, o := range s.deps.SubscriptionComposites.ListSubscriptionComposites() {
			if o.Tag != "" {
				outboundTags[o.Tag] = struct{}{}
			}
		}
	}
	for i, r := range cfg.Route.Rules {
		issues = append(issues, s.computeRuleOutboundIssues(r, i, outboundTags)...)
	}
	if cfg.Route.Final != "" && !isKnownOutboundRef(cfg.Route.Final, outboundTags) {
		issues = append(issues, Issue{
			Severity: "warning",
			Kind:     "orphan-outbound",
			Tag:      cfg.Route.Final,
			Message:  fmt.Sprintf("route.final ссылается на несуществующий outbound %q", cfg.Route.Final),
		})
	}
	for i, o := range cfg.Outbounds {
		for _, member := range o.Outbounds {
			if !isKnownOutboundRef(member, outboundTags) {
				issues = append(issues, Issue{
					Severity: "warning",
					Kind:     "orphan-outbound",
					Tag:      member,
					Message:  fmt.Sprintf("outbound %q содержит несуществующий member %q", o.Tag, member),
				})
			}
		}
		if o.Default != "" && !isKnownOutboundRef(o.Default, outboundTags) {
			issues = append(issues, Issue{
				Severity:  "warning",
				Kind:      "orphan-outbound",
				RuleIndex: i,
				Tag:       o.Default,
				Message:   fmt.Sprintf("outbound %q использует несуществующий default %q", o.Tag, o.Default),
			})
		}
	}
	for _, srv := range cfg.DNS.Servers {
		if srv.Detour != "" && !isKnownOutboundRef(srv.Detour, outboundTags) {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "orphan-outbound",
				Tag:      srv.Detour,
				Message:  fmt.Sprintf("DNS server %q использует несуществующий detour %q", srv.Tag, srv.Detour),
			})
		}
	}
	for _, rs := range cfg.Route.RuleSet {
		if rs.DownloadDetour != "" && !isKnownOutboundRef(rs.DownloadDetour, outboundTags) {
			issues = append(issues, Issue{
				Severity: "warning",
				Kind:     "orphan-outbound",
				Tag:      rs.DownloadDetour,
				Message:  fmt.Sprintf("rule_set %q использует несуществующий download_detour %q", rs.Tag, rs.DownloadDetour),
			})
		}
	}

	ruleSetTags := make(map[string]struct{}, len(cfg.Route.RuleSet))
	for _, rs := range cfg.Route.RuleSet {
		ruleSetTags[rs.Tag] = struct{}{}
	}
	for i, r := range cfg.Route.Rules {
		issues = append(issues, computeRuleSetIssuesInRouteRule(r, i, ruleSetTags)...)
	}
	for i, r := range cfg.DNS.Rules {
		for _, tag := range r.RuleSet {
			if _, ok := ruleSetTags[tag]; !ok {
				issues = append(issues, Issue{
					Severity:  "warning",
					Kind:      "orphan-rule-set",
					RuleIndex: i,
					Tag:       tag,
					Message:   fmt.Sprintf("DNS-правило ссылается на несуществующий rule_set %q", tag),
				})
			}
		}
	}
	return issues
}

func (s *ServiceImpl) computeRuleOutboundIssues(r Rule, index int, outboundTags map[string]struct{}) []Issue {
	var issues []Issue
	if r.Action == "route" && r.Outbound != "" && !isKnownOutboundRef(r.Outbound, outboundTags) {
		issues = append(issues, Issue{
			Severity:  "warning",
			Kind:      "orphan-rule",
			RuleIndex: index,
			Tag:       r.Outbound,
			Message:   fmt.Sprintf("правило ссылается на несуществующий outbound %q", r.Outbound),
		})
	}
	for _, nested := range r.Rules {
		issues = append(issues, s.computeRuleOutboundIssues(nested, index, outboundTags)...)
	}
	return issues
}

func isKnownOutboundRef(tag string, outboundTags map[string]struct{}) bool {
	if tag == "direct" || tag == "block" || tag == "dns" {
		return true
	}
	_, ok := outboundTags[tag]
	return ok
}

func computeRuleSetIssuesInRouteRule(r Rule, index int, ruleSetTags map[string]struct{}) []Issue {
	var issues []Issue
	for _, tag := range r.RuleSet {
		if _, ok := ruleSetTags[tag]; !ok {
			issues = append(issues, Issue{
				Severity:  "warning",
				Kind:      "orphan-rule-set",
				RuleIndex: index,
				Tag:       tag,
				Message:   fmt.Sprintf("правило ссылается на несуществующий rule_set %q", tag),
			})
		}
	}
	for _, nested := range r.Rules {
		issues = append(issues, computeRuleSetIssuesInRouteRule(nested, index, ruleSetTags)...)
	}
	return issues
}

// RulesReferencing returns the indices of route rules whose outbound
// equals tag. Used by tunnel.Service.Delete to refuse deletions that
// would orphan references in router rules.
func (s *ServiceImpl) RulesReferencing(tag string) []int {
	cfg, err := s.loadRouterConfig()
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.rulesReferencingOutbound(tag)
}

// OutboundReferenceLocations returns human-readable locations in the
// router config that reference tag, EXCLUDING route.rules[...] (covered
// by RulesReferencing). Used by the tunnel-delete guard to refuse
// deletion of a tunnel still referenced via composite member, route
// final, dns detour, or rule_set download_detour.
func (s *ServiceImpl) OutboundReferenceLocations(tag string) []string {
	cfg, err := s.loadRouterConfig()
	if err != nil || cfg == nil {
		return nil
	}
	return cfg.outboundReferencesExcludingRules(tag)
}

func (s *ServiceImpl) ListPolicies(ctx context.Context) ([]PolicyInfo, error) {
	if s.deps.Policies == nil {
		return nil, fmt.Errorf("access policy provider not configured")
	}
	return s.deps.Policies.ListPolicies(ctx)
}

func (s *ServiceImpl) CreatePolicy(ctx context.Context, description string) (PolicyInfo, error) {
	if s.deps.Policies == nil {
		return PolicyInfo{}, fmt.Errorf("access policy provider not configured")
	}
	if description == "" {
		description = "awgm-router"
	}
	return s.deps.Policies.CreatePolicy(ctx, description)
}

func (s *ServiceImpl) ListPolicyDevices(ctx context.Context, policyName string) ([]PolicyDevice, error) {
	if s.deps.Policies == nil {
		return nil, fmt.Errorf("access policy provider not configured")
	}
	if policyName == "" {
		return nil, fmt.Errorf("policy name required")
	}
	return s.deps.Policies.ListDevicesForPolicy(ctx, policyName)
}

func (s *ServiceImpl) BindDevice(ctx context.Context, mac, policyName string) error {
	if s.deps.Policies == nil {
		return fmt.Errorf("access policy provider not configured")
	}
	if mac == "" || policyName == "" {
		return fmt.Errorf("mac and policyName required")
	}
	return s.deps.Policies.AssignDevice(ctx, mac, policyName)
}

func (s *ServiceImpl) UnbindDevice(ctx context.Context, mac string) error {
	if s.deps.Policies == nil {
		return fmt.Errorf("access policy provider not configured")
	}
	if mac == "" {
		return fmt.Errorf("mac required")
	}
	return s.deps.Policies.UnassignDevice(ctx, mac)
}

// Inspect simulates which router rule would match the given input
// (a domain or an IP). The matcher walk is purely Go; only rule_set
// matchers shell out to `sing-box rule-set match` to consult the
// binary or downloaded JSON list. Reads the current persisted config so
// the result reflects what the user would observe at runtime.
//
// When the sing-box binary is unavailable (dev machine, fresh install
// before the user has installed the package) rule_set matchers degrade
// to no-match and a Note is appended to the result — the rest of the
// inspector still works.
func (s *ServiceImpl) Inspect(ctx context.Context, input InspectInput) (InspectResult, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return InspectResult{}, err
	}
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	final := cfg.Route.Final
	if final == "" {
		final = "direct"
	}
	binary := ""
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	s.inspectCacheOnce.Do(func() {
		s.inspectCache = newRuleSetCache("")
	})
	return Inspect(input, cfg.Route.Rules, cfg.Route.RuleSet, final, binary, s.inspectCache), nil
}

// InspectDNS simulates which DNS rule would match the given domain and how
// the resolved DNS server classifies the resolution (fakeip → tunnel /
// real → upstream / local → router). It is the DNS-resolution branch that
// precedes the route inspector: a domain that gets a fakeip is then routed
// by Inspect. The matcher walk is purely Go; only rule_set matchers shell
// out to `sing-box rule-set match`. Reads the current persisted config.
func (s *ServiceImpl) InspectDNS(ctx context.Context, input InspectDNSInput) (InspectDNSResult, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return InspectDNSResult{}, err
	}
	if cfg == nil {
		cfg = NewEmptyConfig()
	}
	dnsRules := s.ruleSetMaterializer().restoreConfig(cfg).DNS.Rules
	binary := ""
	if s.deps.Singbox != nil {
		binary = s.deps.Singbox.Binary()
	}
	s.inspectCacheOnce.Do(func() {
		s.inspectCache = newRuleSetCache("")
	})
	return InspectDNS(input, dnsRules, cfg.DNS.Servers, cfg.Route.RuleSet, cfg.DNS.Final, binary, s.inspectCache), nil
}

func (s *ServiceImpl) InspectStream(ctx context.Context, input InspectInput) (<-chan InspectStreamEvent, error) {
	ch := make(chan InspectStreamEvent, 32)
	go func() {
		defer close(ch)
		emitEvent := func(ev InspectStreamEvent) bool {
			select {
			case <-ctx.Done():
				return false
			case ch <- ev:
				return true
			}
		}
		if !emitEvent(InspectStreamEvent{Type: "progress", Progress: &InspectProgress{Phase: "start", Message: "Запускаем инспектор маршрутов…"}}) {
			return
		}
		if !emitEvent(InspectStreamEvent{Type: "progress", Progress: &InspectProgress{Phase: "load_config", Message: "Загружаем конфигурацию маршрутизации…"}}) {
			return
		}
		cfg, err := s.loadRouterConfig()
		if err != nil {
			emitEvent(InspectStreamEvent{Type: "inspect-error", Error: err.Error()})
			return
		}
		if cfg == nil {
			cfg = NewEmptyConfig()
		}
		final := cfg.Route.Final
		if final == "" {
			final = "direct"
		}
		usingDraft := false
		if s.deps.Orch != nil {
			usingDraft = s.deps.Orch.DraftInfo(orchestrator.SlotRouter).HasDraft
		}
		if !emitEvent(InspectStreamEvent{Type: "progress", Progress: &InspectProgress{
			Phase:        "config_loaded",
			Message:      fmt.Sprintf("Конфигурация загружена: %d правил, %d rule_set, final: %s", len(cfg.Route.Rules), len(cfg.Route.RuleSet), final),
			RuleTotal:    intPtr(len(cfg.Route.Rules)),
			RuleSetTotal: intPtr(len(cfg.Route.RuleSet)),
			Final:        final,
			UsingDraft:   usingDraft,
		}}) {
			return
		}
		binary := ""
		if s.deps.Singbox != nil {
			binary = s.deps.Singbox.Binary()
		}
		s.inspectCacheOnce.Do(func() {
			s.inspectCache = newRuleSetCache("")
		})
		res := InspectWithProgress(input, cfg.Route.Rules, cfg.Route.RuleSet, final, binary, s.inspectCache, func(p InspectProgress) {
			select {
			case <-ctx.Done():
				return
			case ch <- InspectStreamEvent{Type: "progress", Progress: &p}:
			}
		})
		select {
		case <-ctx.Done():
			return
		case ch <- InspectStreamEvent{Type: "result", Result: &res}:
		}
	}()
	return ch, nil
}

// ---------------------------------------------------------------------------
// Staging API
// ---------------------------------------------------------------------------

// StagingStatus is what /api/singbox/router/staging returns.
type StagingStatus struct {
	HasDraft   bool
	DraftedAt  time.Time
	Validation *orchestrator.ValidationResult
}

// StagingStatus returns metadata about the current pending draft for the
// router slot. When a draft exists, Validation is populated with the
// current cross-slot diagnostic so the UI can render a preview of "what
// Apply would say".
func (s *ServiceImpl) StagingStatus(ctx context.Context) StagingStatus {
	info := s.deps.Orch.DraftInfo(orchestrator.SlotRouter)
	st := StagingStatus{HasDraft: info.HasDraft, DraftedAt: info.DraftedAt}
	if !info.HasDraft {
		return st
	}
	bytes, err := s.deps.Orch.LoadEffective(orchestrator.SlotRouter)
	if err != nil || bytes == nil {
		return st
	}
	res := s.deps.Orch.ValidateDraft(orchestrator.SlotRouter, bytes)
	st.Validation = &res
	return st
}

// ApplyStaging is the service-level wrapper around Orch.ApplyDraft. On
// success it emits "singbox.router.staging" + "singbox.router.rules" SSE
// invalidations.
func (s *ServiceImpl) ApplyStaging(ctx context.Context) (orchestrator.ValidationResult, error) {
	res, err := s.deps.Orch.ApplyDraft(orchestrator.SlotRouter)
	if err == nil && res.Ok() {
		s.emitStagingEvent("applied")
		s.emitRulesEvent()
	}
	return res, err
}

// DiscardStaging removes the pending draft for the router slot.
func (s *ServiceImpl) DiscardStaging(ctx context.Context) error {
	if err := s.deps.Orch.DiscardDraft(orchestrator.SlotRouter); err != nil {
		return err
	}
	if err := s.restoreEffectiveRuleSetArtifacts(); err != nil {
		return err
	}
	s.emitStagingEvent("discarded")
	s.emitRulesEvent()
	return nil
}

func (s *ServiceImpl) restoreEffectiveRuleSetArtifacts() error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	m := s.ruleSetMaterializer()
	_, err = m.materializeConfig(m.restoreConfig(cfg))
	return err
}

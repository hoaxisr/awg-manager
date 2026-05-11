package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type Service interface {
	Enable(ctx context.Context) error
	Disable(ctx context.Context) error
	Reconcile(ctx context.Context) error
	GetStatus(ctx context.Context) (Status, error)
	GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error)
	UpdateSettings(ctx context.Context, s storage.SingboxRouterSettings) error

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
	RefreshRuleSet(ctx context.Context, tag string) error

	ListCompositeOutbounds(ctx context.Context) ([]CompositeOutboundView, error)
	AddCompositeOutbound(ctx context.Context, o Outbound) error
	UpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error
	DeleteCompositeOutbound(ctx context.Context, tag string, force bool) error

	ApplyPreset(ctx context.Context, presetID, outboundTag string) error

	ListPolicies(ctx context.Context) ([]PolicyInfo, error)
	CreatePolicy(ctx context.Context, description string) (PolicyInfo, error)
	ListPolicyDevices(ctx context.Context, policyName string) ([]PolicyDevice, error)
	BindDevice(ctx context.Context, mac, policyName string) error
	UnbindDevice(ctx context.Context, mac string) error

	ListDNSServers(ctx context.Context) ([]DNSServer, error)
	AddDNSServer(ctx context.Context, s DNSServer) error
	UpdateDNSServer(ctx context.Context, tag string, s DNSServer) error
	DeleteDNSServer(ctx context.Context, tag string, force bool) error

	ListDNSRules(ctx context.Context) ([]DNSRule, error)
	AddDNSRule(ctx context.Context, r DNSRule) error
	UpdateDNSRule(ctx context.Context, index int, r DNSRule) error
	DeleteDNSRule(ctx context.Context, index int) error
	MoveDNSRule(ctx context.Context, from, to int) error

	GetDNSGlobals(ctx context.Context) (final, strategy string, err error)
	SetDNSGlobals(ctx context.Context, final, strategy string) error

	Inspect(ctx context.Context, input InspectInput) (InspectResult, error)

	StagingStatus(ctx context.Context) StagingStatus
	ApplyStaging(ctx context.Context) (orchestrator.ValidationResult, error)
	DiscardStaging(ctx context.Context) error
}

type SingboxController interface {
	Reload() error
	IsRunning() (bool, int)
	Start() error
	ValidateConfigDir(ctx context.Context) error
	ConfigDir() string
	// Binary returns the absolute path (or PATH-resolvable name) of the
	// sing-box executable. Inspect shells out to it for `rule-set match`
	// evaluation. May return empty string when the binary is unknown —
	// callers must tolerate that and degrade gracefully.
	Binary() string
}

// PolicyDevice is one LAN device known to NDMS hotspot, annotated with
// whether it is currently bound to a specific policy.
type PolicyDevice struct {
	MAC   string `json:"mac"`
	IP    string `json:"ip"`
	Name  string `json:"name,omitempty"`
	Bound bool   `json:"bound"`
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
	Log          *logger.Logger
	Settings     *storage.SettingsStore
	Singbox      SingboxController
	Policies     AccessPolicyProvider
	Events       *events.Bus
	IPTables     *IPTables
	AWGTags      AWGTagCatalog        // optional — when nil, computeIssues only sees cfg.Outbounds
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
}

type ServiceImpl struct {
	deps        Deps
	mu          sync.Mutex
	currentMark string // last-installed iptables mark; used by Reconcile to detect change

	// inspectCache backs the route-inspector's rule_set match path. Lazy
	// constructed on first Inspect call so dev-machine builds (no
	// sing-box binary, no /tmp writes during NewService) stay clean.
	inspectCacheOnce sync.Once
	inspectCache     *ruleSetCache
}

func NewService(d Deps) *ServiceImpl {
	if d.IPTables == nil {
		d.IPTables = NewIPTables()
	}
	// Idempotently refresh the netfilter hook script: if a previous
	// version is on disk (older AWGM without pidof guard), this writes
	// the current version. No-op when the file is absent — Install
	// creates it on first Enable.
	refreshNetfilterHookIfPresent()
	return &ServiceImpl{deps: d}
}

func (s *ServiceImpl) routerConfigPath() string {
	return filepath.Join(s.deps.Singbox.ConfigDir(), "20-router.json")
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

func (s *ServiceImpl) persistConfig(ctx context.Context, cfg *RouterConfig) error {
	if s.deps.Orch != nil {
		// Orchestrator path — write to pending/ (staging). The draft will
		// be applied explicitly via ApplyStaging. No SIGHUP is triggered
		// here; sing-box keeps running with the previously-applied config.
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal router config: %w", err)
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

	if err := SaveConfig(path, cfg); err != nil {
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

func (s *ServiceImpl) Enable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate settings first — fail fast with a meaningful error before
	// attempting any kernel / iptables operations.
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr := settings.SingboxRouter
	if sr.PolicyName == "" {
		return ErrPolicyNotConfigured
	}
	mark, err := s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName)
	if err != nil || mark == "" {
		return ErrPolicyMissing
	}

	if err := EnsureTProxyModule(ctx); err != nil {
		return err
	}
	if !IsTProxyTargetAvailable(ctx) {
		return fmt.Errorf("iptables TPROXY target unavailable — kernel module loaded but iptables extension missing")
	}

	sr.Enabled = true

	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds)
	cfg.Outbounds = stripLegacyAWGDirect(cfg.Outbounds)
	cfg.EnsureSystemRules()

	if err := s.persistConfig(ctx, cfg); err != nil {
		return err
	}
	// Promote SlotRouter to active so the orchestrator's reload picks
	// up 20-router.json. The orchestrator handles starting sing-box if
	// it isn't already running. When orch is unwired (tests), we keep
	// the legacy explicit Start call.
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

	if err := s.deps.IPTables.Install(ctx, mark); err != nil {
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
			_ = s.persistConfig(ctx, cfg)
		}
		return fmt.Errorf("iptables install: %w", err)
	}
	s.currentMark = mark

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
func (s *ServiceImpl) healTProxyInbound(ctx context.Context) error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == "tproxy-in" {
			return nil // already present, nothing to do
		}
	}
	cfg.Inbounds = ensureTProxyInbound(cfg.Inbounds)
	return s.persistConfig(ctx, cfg)
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

func ensureTProxyInbound(in []Inbound) []Inbound {
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
			if in[i].UDPTimeout == "" {
				in[i].UDPTimeout = "3m0s"
			}
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
			UDPTimeout:  "3m0s",
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
		sr = settings.SingboxRouter
	}
	cfg, _ := s.loadRouterConfig()
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

	return Status{
		Enabled:                sr.Enabled,
		Installed:              s.deps.IPTables.IsInstalled(ctx),
		NetfilterAvailable:     IsNetfilterAvailable(),
		NetfilterComponentName: "Модули ядра подсистемы сетевой фильтрации",
		TProxyTargetAvailable:  IsTProxyTargetAvailable(ctx),
		PolicyName:             sr.PolicyName,
		PolicyMark:             policyMark,
		PolicyExists:           policyExists,
		DeviceCount:            deviceCount,
		RuleCount:              len(cfg.Route.Rules),
		RuleSetCount:           len(cfg.Route.RuleSet),
		OutboundAWGCount:       awgCount,
		OutboundCompositeCount: compCount,
		Final:                  cfg.Route.Final,
		Issues:                 s.computeIssues(cfg),
	}, nil
}

func (s *ServiceImpl) Disable(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.deps.IPTables.Uninstall(ctx); err != nil {
		s.deps.Log.Warn(fmt.Sprintf("router iptables uninstall: %v", err))
	}
	s.currentMark = ""

	if s.deps.Orch != nil {
		// Move 20-router.json under disabled/ — sing-box's non-recursive
		// -C config.d does not see it after the next reload, so the
		// tproxy inbound, route rules, DNS rules and composite outbounds
		// all disappear from the merged config in one atomic rename.
		if err := s.deps.Orch.SetEnabled(orchestrator.SlotRouter, false); err != nil {
			s.deps.Log.Warn(fmt.Sprintf("orchestrator disable router: %v", err))
		}
	} else {
		// Legacy fallback: strip the tproxy inbound in place so
		// the running sing-box stops accepting on the TPROXY port
		// after the persistConfig reload.
		cfg, err := s.loadRouterConfig()
		if err == nil && cfg != nil {
			filtered := make([]Inbound, 0, len(cfg.Inbounds))
			for _, in := range cfg.Inbounds {
				if in.Tag != "tproxy-in" {
					filtered = append(filtered, in)
				}
			}
			cfg.Inbounds = filtered
			_ = s.persistConfig(ctx, cfg)
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
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	sr := settings.SingboxRouter
	installed := s.deps.IPTables.IsInstalled(ctx)
	switch {
	case sr.Enabled && !installed:
		return s.Enable(ctx)
	case !sr.Enabled && installed:
		return s.Disable(ctx)
	case sr.Enabled && installed:
		mark, err := s.deps.Policies.GetPolicyMark(ctx, sr.PolicyName)
		if err != nil || mark == "" {
			// Policy gone upstream — fail-safe disable, no auto-recovery.
			return s.Disable(ctx)
		}
		if mark != s.currentMark {
			s.mu.Lock()
			if err := s.deps.IPTables.Install(ctx, mark); err != nil {
				s.mu.Unlock()
				return err
			}
			s.currentMark = mark
			s.mu.Unlock()
		}
		// Self-heal: a previous Install rollback or upgrade hop may
		// have left 20-router.json without the tproxy-in inbound. Re-add
		// it idempotently so sing-box keeps listening on TPROXYPort.
		if err := s.healTProxyInbound(ctx); err != nil {
			s.deps.Log.Warn(fmt.Sprintf("router: heal tproxy inbound: %v", err))
		}
	}
	return nil
}

func (s *ServiceImpl) withConfig(ctx context.Context, event string, fn func(*RouterConfig) error) error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
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

func (s *ServiceImpl) ListRules(ctx context.Context) ([]Rule, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return cfg.Route.Rules, nil
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
// an outbound from any known catalog (router composites, AWG, sing-box tunnels).
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
	return cfg.Route.RuleSet, nil
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
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.DeleteRuleSet(tag, force) })
}


func (s *ServiceImpl) RefreshRuleSet(ctx context.Context, tag string) error {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return err
	}
	found := false
	for _, rs := range cfg.Route.RuleSet {
		if rs.Tag == tag {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("rule set %q not found", tag)
	}
	return s.deps.Singbox.Reload()
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
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.AddCompositeOutbound(o) })
}

func (s *ServiceImpl) UpdateCompositeOutbound(ctx context.Context, tag string, o Outbound) error {
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.UpdateCompositeOutbound(tag, o) })
}

func (s *ServiceImpl) DeleteCompositeOutbound(ctx context.Context, tag string, force bool) error {
	return s.withConfig(ctx, "outbounds", func(c *RouterConfig) error { return c.DeleteCompositeOutbound(tag, force) })
}

func (s *ServiceImpl) ApplyPreset(ctx context.Context, presetID, outboundTag string) error {
	return s.withConfig(ctx, "status", func(c *RouterConfig) error {
		return ApplyPresetToConfig(c, presetID, outboundTag)
	})
}

func (s *ServiceImpl) GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error) {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return storage.SingboxRouterSettings{}, err
	}
	return settings.SingboxRouter, nil
}

func (s *ServiceImpl) UpdateSettings(ctx context.Context, sr storage.SingboxRouterSettings) error {
	settings, err := s.deps.Settings.Load()
	if err != nil {
		return err
	}
	settings.SingboxRouter = sr
	if err := s.deps.Settings.Save(settings); err != nil {
		return err
	}
	return s.Reconcile(ctx)
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
	for i, r := range cfg.Route.Rules {
		if r.Action == "route" && r.Outbound != "" && r.Outbound != "direct" {
			if _, ok := outboundTags[r.Outbound]; !ok {
				issues = append(issues, Issue{
					Severity:  "warning",
					Kind:      "orphan-rule",
					RuleIndex: i,
					Tag:       r.Outbound,
					Message:   fmt.Sprintf("правило ссылается на несуществующий outbound %q", r.Outbound),
				})
			}
		}
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
	s.emitStagingEvent("discarded")
	s.emitRulesEvent()
	return nil
}

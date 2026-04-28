package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/logger"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type Service interface {
	Enable(ctx context.Context) error
	Disable(ctx context.Context) error
	Reconcile(ctx context.Context) error
	GetStatus(ctx context.Context) (Status, error)
	GetSettings(ctx context.Context) (storage.SingboxRouterSettings, error)
	UpdateSettings(ctx context.Context, s storage.SingboxRouterSettings) error

	ListRules(ctx context.Context) ([]Rule, error)
	AddRule(ctx context.Context, rule Rule) error
	UpdateRule(ctx context.Context, index int, rule Rule) error
	DeleteRule(ctx context.Context, index int) error
	MoveRule(ctx context.Context, from, to int) error

	ListRuleSets(ctx context.Context) ([]RuleSet, error)
	AddRuleSet(ctx context.Context, rs RuleSet) error
	DeleteRuleSet(ctx context.Context, tag string, force bool) error
	RefreshRuleSet(ctx context.Context, tag string) error

	ListCompositeOutbounds(ctx context.Context) ([]Outbound, error)
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
}

type SingboxController interface {
	Reload() error
	IsRunning() (bool, int)
	Start() error
	ValidateConfigDir(ctx context.Context) error
	ConfigDir() string
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

type Deps struct {
	Log      *logger.Logger
	Settings *storage.SettingsStore
	Singbox  SingboxController
	Policies AccessPolicyProvider
	Events   *events.Bus
	IPTables *IPTables
	AWGTags  AWGTagCatalog // optional — when nil, computeIssues only sees cfg.Outbounds
}

type ServiceImpl struct {
	deps        Deps
	mu          sync.Mutex
	currentMark string // last-installed iptables mark; used by Reconcile to detect change
}

func NewService(d Deps) *ServiceImpl {
	if d.IPTables == nil {
		d.IPTables = NewIPTables()
	}
	return &ServiceImpl{deps: d}
}

func (s *ServiceImpl) routerConfigPath() string {
	return filepath.Join(s.deps.Singbox.ConfigDir(), "20-router.json")
}

func (s *ServiceImpl) loadRouterConfig() (*RouterConfig, error) {
	return LoadConfig(s.routerConfigPath())
}

func (s *ServiceImpl) persistConfig(ctx context.Context, cfg *RouterConfig) error {
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
	if running, _ := s.deps.Singbox.IsRunning(); !running {
		if err := s.deps.Singbox.Start(); err != nil {
			return fmt.Errorf("sing-box start: %w", err)
		}
	}

	if err := s.deps.IPTables.Install(ctx, mark); err != nil {
		rollback := filterTProxyInbound(cfg.Inbounds)
		cfg.Inbounds = rollback
		_ = s.persistConfig(ctx, cfg)
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

func ensureTProxyInbound(in []Inbound) []Inbound {
	for _, i := range in {
		if i.Tag == "tproxy-in" {
			return in
		}
	}
	return append([]Inbound{{
		Type:        "tproxy",
		Tag:         "tproxy-in",
		Listen:      "127.0.0.1",
		ListenPort:  TPROXYPort,
		RoutingMark: Fwmark,
	}}, in...)
}


func (s *ServiceImpl) emitStatus(ctx context.Context) {
	if s.deps.Events == nil {
		return
	}
	status, _ := s.GetStatus(ctx)
	s.deps.Events.Publish("singbox-router:status", status)
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
	if rs.Format == "" {
		rs.Format = "binary"
	}
	if rs.UpdateInterval == "" {
		rs.UpdateInterval = "24h"
	}
	return s.withConfig(ctx, "rulesets", func(c *RouterConfig) error { return c.AddRuleSet(rs) })
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

func (s *ServiceImpl) ListCompositeOutbounds(ctx context.Context) ([]Outbound, error) {
	cfg, err := s.loadRouterConfig()
	if err != nil {
		return nil, err
	}
	return cfg.CompositeOutbounds(), nil
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

package localdeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// routingModeFakeIP is the sing-box router's fakeip-tun routing mode
// (storage.SingboxRouterSettings.RoutingMode).
const routingModeFakeIP = "fakeip-tun"

// Web form defaults for a new group (CompositeOutboundEditModal.svelte):
// a group made through MCP starts the way one made in the form does.
const (
	groupProbeURL       = "https://www.gstatic.com/generate_204"
	groupProbeInterval  = "3m"
	groupProbeTolerance = 50
)

// requireTProxyRouter refuses a router edit in fakeip-tun mode. The router
// service edits the tproxy configuration (SlotRouter) whatever the mode;
// in fakeip-tun the engine routes by a configuration of its own
// (SlotFakeIP), so the edit would be staged, applied, and change nothing.
func (l *Local) requireTProxyRouter(ctx context.Context) error {
	settings, err := l.c.Router.GetSettings(ctx)
	if err != nil {
		return err
	}
	if settings.RoutingMode == routingModeFakeIP {
		return fmt.Errorf("the sing-box router runs in fakeip-tun mode, which routes by a configuration of its own that MCP does not edit; an edit here would change nothing. Make this change in the web interface")
	}
	return nil
}

// routerReader is the configuration the router routes by now: the tproxy
// one (SlotRouter), read through its draft, or in fakeip-tun mode the
// fakeip-tun one (SlotFakeIP), which has no draft. The router service's
// own List* methods read the tproxy configuration whatever the mode, the
// way the web interface's tproxy pages do; in fakeip-tun those pages are
// not shown and lists read there would describe nothing the engine runs.
type routerReader struct {
	fakeIP     bool
	rules      func(context.Context) ([]router.Rule, error)
	composites func(context.Context) ([]router.CompositeOutboundView, error)
	dnsServers func(context.Context) ([]router.DNSServer, error)
	dnsRules   func(context.Context) ([]router.DNSRule, error)
	dnsGlobals func(context.Context) (string, string, string, error)
	ruleSets   func(context.Context) ([]router.RuleSet, error)
}

func (l *Local) routerReads(ctx context.Context) (routerReader, error) {
	settings, err := l.c.Router.GetSettings(ctx)
	if err != nil {
		return routerReader{}, err
	}
	if settings.RoutingMode != routingModeFakeIP {
		r := l.c.Router
		return routerReader{
			rules: r.ListRules, composites: r.ListCompositeOutbounds, dnsServers: r.ListDNSServers,
			dnsRules: r.ListDNSRules, dnsGlobals: r.GetDNSGlobals, ruleSets: r.ListRuleSets,
		}, nil
	}
	if l.c.FakeIP == nil {
		return routerReader{}, errUnavailable("the fakeip-tun configuration")
	}
	f := l.c.FakeIP
	return routerReader{
		fakeIP: true,
		rules:  f.FakeIPListRules, composites: f.FakeIPListCompositeOutbounds, dnsServers: f.FakeIPListDNSServers,
		dnsRules: f.FakeIPListDNSRules, dnsGlobals: f.FakeIPGetDNSGlobals, ruleSets: f.FakeIPListRuleSets,
	}, nil
}

// hasDraft reports whether a read includes unapplied edits. The fakeip-tun
// configuration (fakeIP) is edited without a draft, so a router draft —
// which may exist from before the switch — says nothing about it.
func (l *Local) hasDraft(ctx context.Context, fakeIP bool) bool {
	return !fakeIP && l.c.Router.StagingStatus(ctx).HasDraft
}

// ListSingboxDNSServers maps the router's DNS servers. The address is the
// user's own, typed into the form; it still goes through hostShaped, as
// every address this package hands out does.
func (l *Local) ListSingboxDNSServers(ctx context.Context) ([]mcpsrv.SingboxDNSServer, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	rr, err := l.routerReads(ctx)
	if err != nil {
		return nil, false, err
	}
	servers, err := rr.dnsServers(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make([]mcpsrv.SingboxDNSServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, mcpsrv.SingboxDNSServer{
			Tag: s.Tag, Type: token(s.Type), Server: hostShaped(s.Server), ServerPort: s.ServerPort,
			Detour: s.Detour, Managed: s.Tag == router.ManagedDNSDirectTag,
		})
	}
	return out, l.hasDraft(ctx, rr.fakeIP), nil
}

// ListSingboxRuleSets maps the router's rule sets. Of the URL only the
// host is kept, for the reason hostOf gives: a self-hosted list can carry
// a token in its path. An inline set's rules are not returned either.
func (l *Local) ListSingboxRuleSets(ctx context.Context) ([]mcpsrv.SingboxRuleSet, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	rr, err := l.routerReads(ctx)
	if err != nil {
		return nil, false, err
	}
	sets, err := rr.ruleSets(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make([]mcpsrv.SingboxRuleSet, 0, len(sets))
	for _, rs := range sets {
		out = append(out, mcpsrv.SingboxRuleSet{
			Tag: rs.Tag, Type: token(rs.Type), Format: token(rs.Format), Host: hostOf(rs.URL), DownloadDetour: rs.DownloadDetour,
		})
	}
	return out, l.hasDraft(ctx, rr.fakeIP), nil
}

// CreateSingboxOutboundGroup builds the group the web form would: the
// probe settings it starts with, interrupt_exist_connections on a fallback
// group, and the first member as a selector's default. The service checks
// the members and the tag.
func (l *Local) CreateSingboxOutboundGroup(ctx context.Context, in mcpsrv.SingboxGroupInput) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if len(in.Members) == 0 {
		return fmt.Errorf("a group needs at least one member")
	}
	o := router.Outbound{Type: in.Type, Tag: in.Tag, Outbounds: in.Members}
	switch in.Type {
	case "selector":
		o.Default = in.Members[0]
	case "urltest":
		o.URL, o.Interval, o.Tolerance = groupProbeURL, groupProbeInterval, groupProbeTolerance
	case "fallback":
		o.URL, o.Interval, o.InterruptExistConnections = groupProbeURL, groupProbeInterval, true
	default:
		return fmt.Errorf("type must be selector, urltest or fallback")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.AddCompositeOutbound(ctx, o); err != nil {
		l.singboxLog.Warn("outbound-create", in.Tag, "Failed to create sing-box group (MCP): "+err.Error())
		return routerEditError(err)
	}
	l.singboxLog.Info("outbound-create", in.Tag, "Sing-box "+in.Type+" group created (MCP, staged)")
	return nil
}

// SetSingboxOutboundGroupMembers replaces a router group's members. A
// subscription's group is listed by the router but owned by the
// subscription service: the router would answer "not found", which sends
// the agent looking for a typo, so it is told here what to use instead.
func (l *Local) SetSingboxOutboundGroupMembers(ctx context.Context, tag string, members []string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	groups, err := l.c.Router.ListCompositeOutbounds(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Tag == tag && g.Source == "subscription" {
			return fmt.Errorf("%q is a subscription's group: its servers come from the subscription. Add one with add_singbox_subscription_server, or build a router group over it with create_singbox_outbound_group", tag)
		}
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.SetCompositeOutboundMembers(ctx, tag, members); err != nil {
		l.singboxLog.Warn("outbound-members", tag, "Failed to change sing-box group members (MCP): "+err.Error())
		return routerEditError(err)
	}
	l.singboxLog.Info("outbound-members", tag, fmt.Sprintf("Sing-box group members set, %d in all (MCP, staged)", len(members)))
	return nil
}

func (l *Local) SetSingboxDNSServerDetour(ctx context.Context, tag, detour string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.SetDNSServerDetour(ctx, tag, detour); err != nil {
		l.singboxLog.Warn("dns-server-detour", tag, "Failed to set sing-box DNS server detour (MCP): "+err.Error())
		return routerEditError(err)
	}
	l.singboxLog.Info("dns-server-detour", tag, "Sing-box DNS server detour set to "+detourName(detour)+" (MCP, staged)")
	return nil
}

func (l *Local) SetSingboxRuleSetDetour(ctx context.Context, tags []string, detour string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	joined := strings.Join(tags, ", ")
	if err := l.c.Router.BulkSetRuleSetDetour(ctx, tags, detour); err != nil {
		l.singboxLog.Warn("ruleset-detour", joined, "Failed to set sing-box rule set download detour (MCP): "+err.Error())
		return routerEditError(err)
	}
	l.singboxLog.Info("ruleset-detour", joined, "Sing-box rule set download detour set to "+detourName(detour)+" (MCP, staged)")
	return nil
}

// detourName renders a detour for the journal: empty is a direct dial.
func detourName(detour string) string {
	if detour == "" {
		return "direct"
	}
	return detour
}

// routerEditError adds to a refused router edit the tool that resolves it.
// The service's own words stay: they name the tag and the rule broken.
func routerEditError(err error) error {
	switch {
	case errors.Is(err, router.ErrCompositeMemberUnknown):
		return fmt.Errorf("%w (members come from list_singbox_outbounds and list_singbox_tunnels)", err)
	case errors.Is(err, router.ErrOutboundTagConflict):
		return fmt.Errorf("%w (choose another tag; list_singbox_outbounds shows those taken)", err)
	case errors.Is(err, router.ErrOutboundNotFound):
		return fmt.Errorf("%w (use list_singbox_outbounds)", err)
	case errors.Is(err, router.ErrDNSServerNotFound):
		return fmt.Errorf("%w (use list_singbox_dns_servers)", err)
	case errors.Is(err, router.ErrRuleSetNotFound):
		return fmt.Errorf("%w (use list_singbox_rulesets)", err)
	}
	return err
}

// SingboxRouterStatus joins the router's status (what is running) with its
// settings (what the user chose). LastError and LastCrashReason are left
// out: they are sing-box's own output and can quote a server or a rule;
// CrashCount says that something happened and get_logs says what.
func (l *Local) SingboxRouterStatus(ctx context.Context) (mcpsrv.SingboxRouterStatus, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxRouterStatus{}, errUnavailable("sing-box router")
	}
	settings, err := l.c.Router.GetSettings(ctx)
	if err != nil {
		return mcpsrv.SingboxRouterStatus{}, err
	}
	st, err := l.c.Router.GetStatus(ctx)
	if err != nil {
		return mcpsrv.SingboxRouterStatus{}, err
	}
	out := mcpsrv.SingboxRouterStatus{
		Enabled: settings.Enabled, Active: st.Active, RoutingMode: token(settings.RoutingMode),
		DeviceMode: token(settings.DeviceMode), PolicyName: sanitizeLabel(settings.PolicyName), PolicyExists: st.PolicyExists,
		DeviceCount: st.DeviceCount, Sniffer: settings.SnifferEnabled, Final: st.Final, UDPTimeout: token(settings.UDPTimeout),
		InterceptFamily: token(settings.InterceptFamily),
		BypassPresets:   slices.Clone(settings.BypassPresets), BypassPorts: sanitizeText(settings.BypassExtraPorts, maxSettingRunes),
		BypassSubnets: sanitizeText(settings.BypassExtraSubnets, maxSettingRunes), BypassGeoIP: slices.Clone(settings.BypassGeoIPTags),
		IngressInterfaces: slices.Clone(settings.IngressInterfaces), CrashCount: st.CrashCount,
		RestartSuppressedUntil: st.RestartSuppressedUntil,
	}
	for _, c := range settings.QoSClasses {
		out.QoSClasses = append(out.QoSClasses, mcpsrv.SingboxQoSClass{Name: sanitizeLabel(c.Name), DSCP: c.DSCP, Outbound: c.Outbound, Enabled: c.Enabled})
	}
	for _, is := range st.Issues {
		out.Issues = append(out.Issues, mcpsrv.SingboxIssue{
			Severity: token(is.Severity), Kind: token(is.Kind), RuleIndex: is.RuleIndex, Tag: is.Tag, Message: sanitizeText(is.Message, maxMessageRunes),
		})
	}
	return out, nil
}

// ListSingboxDNSRules reads the DNS rules and globals of the configuration
// the router routes by (routerReads).
func (l *Local) ListSingboxDNSRules(ctx context.Context) (mcpsrv.SingboxDNSRules, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxDNSRules{}, errUnavailable("sing-box router")
	}
	rr, err := l.routerReads(ctx)
	if err != nil {
		return mcpsrv.SingboxDNSRules{}, err
	}
	rules, err := rr.dnsRules(ctx)
	if err != nil {
		return mcpsrv.SingboxDNSRules{}, err
	}
	final, strategy, _, err := rr.dnsGlobals(ctx)
	if err != nil {
		return mcpsrv.SingboxDNSRules{}, err
	}
	out := mcpsrv.SingboxDNSRules{Rules: make([]mcpsrv.SingboxDNSRule, 0, len(rules)), Final: final, Strategy: token(strategy), HasDraft: l.hasDraft(ctx, rr.fakeIP)}
	for i, r := range rules {
		action := r.Action
		if action == "" {
			action = "route"
		}
		out.Rules = append(out.Rules, mcpsrv.SingboxDNSRule{Index: i, Match: dnsRuleMatchSummary(r), Action: token(action), Server: r.Server})
	}
	return out, nil
}

// dnsRuleMatchSummary renders a DNS rule as ruleMatchSummary renders a
// routing rule: the name fields are alternatives to one another, and every
// other field must hold too.
func dnsRuleMatchSummary(r router.DNSRule) string {
	return ruleMatchSummary(router.Rule{
		Domain: r.Domain, DomainSuffix: r.DomainSuffix, SourceIPCIDR: r.SourceIPCIDR, RuleSet: r.RuleSet,
	}, matchExtra{
		name: [][2]string{{"domain_keyword", strings.Join(r.DomainKeyword, ", ")}, {"domain_regex", strings.Join(r.DomainRegex, ", ")}},
		and:  [][2]string{{"query_type", strings.Join(r.QueryType, ", ")}, {"answer ip_cidr", strings.Join(r.IPCIDR, ", ")}},
	})
}

// ListSingboxDNSRewrites maps the DNS Rewrite entries. The pattern is the
// user's, a domain or a glob; the addresses go through hostShaped.
func (l *Local) ListSingboxDNSRewrites(_ context.Context) ([]mcpsrv.SingboxDNSRewrite, error) {
	if l.c.DNSRewrites == nil {
		return nil, errUnavailable("DNS Rewrite")
	}
	list, err := l.c.DNSRewrites.List()
	if err != nil {
		return nil, err
	}
	out := make([]mcpsrv.SingboxDNSRewrite, 0, len(list))
	for _, r := range list {
		ips := make([]string, 0, len(r.IPs))
		for _, ip := range r.IPs {
			if h := hostShaped(ip); h != "" {
				ips = append(ips, h)
			}
		}
		out = append(out, mcpsrv.SingboxDNSRewrite{Pattern: sanitizeText(r.Pattern, 253), IPs: ips, Managed: r.Managed != ""})
	}
	return out, nil
}

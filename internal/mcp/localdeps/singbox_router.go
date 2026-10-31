package localdeps

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/singbox/dnsrewrite"
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
		out.Rules = append(out.Rules, singboxDNSRule(i, r))
	}
	return out, nil
}

func singboxDNSRule(i int, r router.DNSRule) mcpsrv.SingboxDNSRule {
	action := r.Action
	if action == "" {
		action = "route"
	}
	out := mcpsrv.SingboxDNSRule{Index: i, Match: dnsRuleMatchSummary(r), Action: token(action), Server: r.Server, Tag: r.Tag}
	if r.MatchResponse.IsEnabled() {
		out.MatchResponse = r.MatchResponse.Tag
		if out.MatchResponse == "" {
			out.MatchResponse = mcpsrv.DNSMatchResponseLast
		}
	}
	return out
}

// dnsRuleMatchSummary renders a DNS rule as sing-box evaluates it
// (route/rule/rule_dns.go, rule_abstract.go): the name fields and the
// answer ip_cidr are one destination group, alternatives to one another;
// source_ip_cidr, the rule set, query_type and each response_* field must
// hold too. The answer fields are read from the response match_response
// points at (SingboxDNSRule.MatchResponse), never from the query.
func dnsRuleMatchSummary(r router.DNSRule) string {
	records := func(values []string) string {
		clean := make([]string, 0, len(values))
		for _, v := range values {
			clean = append(clean, sanitizeText(v, maxMessageRunes))
		}
		return strings.Join(clean, ", ")
	}
	return ruleMatchSummary(router.Rule{
		Domain: r.Domain, DomainSuffix: r.DomainSuffix, SourceIPCIDR: r.SourceIPCIDR, RuleSet: r.RuleSet,
	}, matchExtra{
		name: [][2]string{
			{"domain_keyword", strings.Join(r.DomainKeyword, ", ")}, {"domain_regex", strings.Join(r.DomainRegex, ", ")},
			{"answer ip_cidr", strings.Join(r.IPCIDR, ", ")},
		},
		and: [][2]string{
			{"query_type", strings.Join(r.QueryType, ", ")}, {"response_rcode", r.ResponseRcode},
			{"response_answer", records(r.ResponseAnswer)}, {"response_ns", records(r.ResponseNS)}, {"response_extra", records(r.ResponseExtra)},
		},
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
	revision := dnsrewrite.Revision(list)
	for index, r := range list {
		ips := make([]string, 0, len(r.IPs))
		for _, ip := range r.IPs {
			if h := hostShaped(ip); h != "" {
				ips = append(ips, h)
			}
		}
		out = append(out, mcpsrv.SingboxDNSRewrite{Index: index, Revision: revision, Pattern: sanitizeText(r.Pattern, 253), IPs: ips, Managed: r.Managed != ""})
	}
	return out, nil
}

// singboxRuleFrom builds the router rule an agent asked for.
func singboxRuleFrom(in mcpsrv.SingboxRuleInput) router.Rule {
	return router.Rule{
		DomainSuffix: in.DomainSuffix, Domain: in.Domain, IPCIDR: in.IPCIDR, SourceIPCIDR: in.SourceIPCIDR,
		SourceMACAddress: in.SourceMAC, Port: in.Port, RuleSet: in.RuleSet, Network: in.Network, Protocol: in.Protocol,
		Action: in.Action, Outbound: in.Outbound,
	}
}

// AddSingboxRule inserts through InsertRule, which checks the outbound and
// keeps system rules on top in the same write. -1 is the end of the list,
// which InsertRule reads inside that write.
func (l *Local) AddSingboxRule(ctx context.Context, position int, expectMatch string, in mcpsrv.SingboxRuleInput) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if position < 0 {
		position = -1
	}
	if err := l.c.Router.InsertRule(ctx, position, singboxRuleFrom(in), expectRule(position, expectMatch)); err != nil {
		l.singboxLog.Warn("rule-add", strconv.Itoa(position), "Failed to add sing-box rule (MCP): "+err.Error())
		return ruleEditError(err)
	}
	l.singboxLog.Info("rule-add", strconv.Itoa(position), "Sing-box rule added (MCP, staged)")
	return nil
}

func (l *Local) UpdateSingboxRule(ctx context.Context, index int, expectMatch string, in mcpsrv.SingboxRuleInput) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.ReplaceUserRule(ctx, index, singboxRuleFrom(in), expectRule(index, expectMatch)); err != nil {
		l.singboxLog.Warn("rule-update", strconv.Itoa(index), "Failed to replace sing-box rule (MCP): "+err.Error())
		return ruleEditError(err)
	}
	l.singboxLog.Info("rule-update", strconv.Itoa(index), "Sing-box rule replaced (MCP, staged)")
	return nil
}

func (l *Local) DeleteSingboxRule(ctx context.Context, index int, expectMatch string) (mcpsrv.SingboxRule, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxRule{}, errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return mcpsrv.SingboxRule{}, err
	}
	deleted, err := l.c.Router.DeleteUserRule(ctx, index, expectRule(index, expectMatch))
	if err != nil {
		l.singboxLog.Warn("rule-delete", strconv.Itoa(index), "Failed to delete sing-box rule (MCP): "+err.Error())
		return mcpsrv.SingboxRule{}, ruleEditError(err)
	}
	l.singboxLog.Info("rule-delete", strconv.Itoa(index), "Sing-box rule deleted (MCP, staged): "+ruleMatchSummary(deleted))
	return singboxRule(index, deleted), nil
}

func (l *Local) MoveSingboxRule(ctx context.Context, from, to int, expectMatch string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.MoveUserRule(ctx, from, to, expectRule(from, expectMatch)); err != nil {
		l.singboxLog.Warn("rule-move", strconv.Itoa(from), "Failed to move sing-box rule (MCP): "+err.Error())
		return ruleEditError(err)
	}
	l.singboxLog.Info("rule-move", strconv.Itoa(from), "Sing-box rule moved to "+strconv.Itoa(to)+" (MCP, staged)")
	return nil
}

func (l *Local) SetSingboxRouteFinal(ctx context.Context, outbound string) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	if err := l.c.Router.SetRouteFinal(ctx, outbound); err != nil {
		l.singboxLog.Warn("route-final", outbound, "Failed to set sing-box final outbound (MCP): "+err.Error())
		return routerEditError(err)
	}
	l.singboxLog.Info("route-final", outbound, "Sing-box final outbound set (MCP, staged)")
	return nil
}

// AddSingboxRuleSet builds the set as the web form does: a remote one with
// the service's defaults (binary, 24h), an inline one as a single headless
// rule, whose destination fields sing-box treats as alternatives.
func (l *Local) AddSingboxRuleSet(ctx context.Context, in mcpsrv.SingboxRuleSetInput) error {
	if l.c.Router == nil {
		return errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return err
	}
	rs := router.RuleSet{Tag: in.Tag, Type: in.Type}
	switch in.Type {
	case "remote":
		rs.URL, rs.Format, rs.UpdateInterval = in.URL, in.Format, in.UpdateInterval
	case "inline":
		rule := map[string]any{}
		for key, values := range map[string][]string{"domain_suffix": in.DomainSuffix, "domain": in.Domain, "ip_cidr": in.IPCIDR} {
			if len(values) > 0 {
				rule[key] = values
			}
		}
		rs.Rules = []map[string]any{rule}
	default:
		return fmt.Errorf("type must be remote or inline")
	}
	if err := l.c.Router.AddRuleSet(ctx, rs); err != nil {
		l.singboxLog.Warn("ruleset-add", in.Tag, "Failed to add sing-box rule set (MCP): "+err.Error())
		return ruleEditError(err)
	}
	l.singboxLog.Info("ruleset-add", in.Tag, "Sing-box "+in.Type+" rule set added (MCP, staged)")
	return nil
}

// DeleteSingboxRuleSet reads the set and the rules first, to hand back
// the set once it is gone and, with force, the routing and DNS rules that
// used it (RouterConfig.DeleteRuleSet strips both): each lost the
// condition, and one left with none was deleted with it — after apply
// nothing else records them. The reads and the deletion are separate
// calls, so a change in between is not reflected.
func (l *Local) DeleteSingboxRuleSet(ctx context.Context, tag string, force bool) (mcpsrv.SingboxRuleSetDeletion, error) {
	if l.c.Router == nil {
		return mcpsrv.SingboxRuleSetDeletion{}, errUnavailable("sing-box router")
	}
	if err := l.requireTProxyRouter(ctx); err != nil {
		return mcpsrv.SingboxRuleSetDeletion{}, err
	}
	sets, err := l.c.Router.ListRuleSets(ctx)
	if err != nil {
		return mcpsrv.SingboxRuleSetDeletion{}, err
	}
	i := slices.IndexFunc(sets, func(rs router.RuleSet) bool { return rs.Tag == tag })
	if i < 0 {
		return mcpsrv.SingboxRuleSetDeletion{}, fmt.Errorf("%w: %q (use list_singbox_rulesets)", router.ErrRuleSetNotFound, tag)
	}
	var out mcpsrv.SingboxRuleSetDeletion
	if force {
		rules, err := l.c.Router.ListRules(ctx)
		if err != nil {
			return mcpsrv.SingboxRuleSetDeletion{}, err
		}
		for j, r := range rules {
			if ruleUsesSet(r, tag) {
				out.Rules = append(out.Rules, singboxRule(j, r))
			}
		}
		dnsRules, err := l.c.Router.ListDNSRules(ctx)
		if err != nil {
			return mcpsrv.SingboxRuleSetDeletion{}, err
		}
		for j, r := range dnsRules {
			if slices.Contains(r.RuleSet, tag) {
				out.DNSRules = append(out.DNSRules, singboxDNSRule(j, r))
			}
		}
	}
	if err := l.c.Router.DeleteRuleSet(ctx, tag, force); err != nil {
		l.singboxLog.Warn("ruleset-delete", tag, "Failed to delete sing-box rule set (MCP): "+err.Error())
		return mcpsrv.SingboxRuleSetDeletion{}, ruleEditError(err)
	}
	msg := "Sing-box rule set deleted (MCP, staged)"
	for _, r := range out.Rules {
		msg += fmt.Sprintf("; rule %d used it: %s", r.Index, r.Match)
	}
	for _, r := range out.DNSRules {
		msg += fmt.Sprintf("; DNS rule %d used it: %s", r.Index, r.Match)
	}
	l.singboxLog.Info("ruleset-delete", tag, msg)
	rs := sets[i]
	out.Deleted = mcpsrv.SingboxRuleSet{Tag: rs.Tag, Type: token(rs.Type), Format: token(rs.Format), Host: hostOf(rs.URL), DownloadDetour: rs.DownloadDetour}
	return out, nil
}

// ruleUsesSet reports a rule, or one nested in a logical rule, that names
// the set.
func ruleUsesSet(r router.Rule, tag string) bool {
	if slices.Contains(r.RuleSet, tag) {
		return true
	}
	return slices.ContainsFunc(r.Rules, func(sub router.Rule) bool { return ruleUsesSet(sub, tag) })
}

// errRuleChanged is a rule edit refused because the rule at the index is
// not the one the agent read (expectMatch).
var errRuleChanged = errors.New("the rule at this position is not the one expected")

// expectRule turns expectMatch — a match from list_singbox_rules — into
// the check the router runs in the same write as the edit. The rule is
// compared by its rendered match, the one thing the agent saw of it; ""
// skips the check.
func expectRule(index int, expectMatch string) router.RuleCheck {
	expectMatch = strings.TrimSpace(expectMatch)
	if expectMatch == "" {
		return nil
	}
	return func(r router.Rule) error {
		if got := ruleMatchSummary(r); got != expectMatch {
			return fmt.Errorf("%w: rule %d now matches %q, not %q", errRuleChanged, index, got, expectMatch)
		}
		return nil
	}
}

// ruleEditError adds to a refused rule edit the step that resolves it.
func ruleEditError(err error) error {
	switch {
	case errors.Is(err, errRuleChanged):
		return fmt.Errorf("%w — the list changed since it was read (an earlier edit shifted the positions, or someone edited it in the web interface); nothing was changed: call list_singbox_rules again and use the new position", err)
	case errors.Is(err, router.ErrRuleProtected):
		return fmt.Errorf("%w (system rules and rules awg-manager manages stay as they are; list_singbox_rules marks them system or managed)", err)
	case errors.Is(err, router.ErrRuleIndexOutOfRange):
		return fmt.Errorf("%w (positions come from list_singbox_rules)", err)
	case errors.Is(err, router.ErrRuleSetReferenced):
		return fmt.Errorf("%w (edit or delete those rules first — DNS rules only in the web interface — or pass force)", err)
	case errors.Is(err, router.ErrRuleSetNotFound):
		return fmt.Errorf("%w (rule sets come from list_singbox_rulesets; add_singbox_ruleset adds one)", err)
	case errors.Is(err, router.ErrRuleSetTagConflict):
		return fmt.Errorf("%w (choose another tag; list_singbox_rulesets shows those taken)", err)
	}
	return routerEditError(err)
}

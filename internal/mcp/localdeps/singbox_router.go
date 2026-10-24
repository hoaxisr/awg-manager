package localdeps

import (
	"context"
	"errors"
	"fmt"
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

// ListSingboxDNSServers maps the router's DNS servers. The address is the
// user's own, typed into the form; it still goes through hostShaped, as
// every address this package hands out does.
func (l *Local) ListSingboxDNSServers(ctx context.Context) ([]mcpsrv.SingboxDNSServer, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	servers, err := l.c.Router.ListDNSServers(ctx)
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
	return out, l.c.Router.StagingStatus(ctx).HasDraft, nil
}

// ListSingboxRuleSets maps the router's rule sets. Of the URL only the
// host is kept, for the reason hostOf gives: a self-hosted list can carry
// a token in its path. An inline set's rules are not returned either.
func (l *Local) ListSingboxRuleSets(ctx context.Context) ([]mcpsrv.SingboxRuleSet, bool, error) {
	if l.c.Router == nil {
		return nil, false, errUnavailable("sing-box router")
	}
	sets, err := l.c.Router.ListRuleSets(ctx)
	if err != nil {
		return nil, false, err
	}
	out := make([]mcpsrv.SingboxRuleSet, 0, len(sets))
	for _, rs := range sets {
		out = append(out, mcpsrv.SingboxRuleSet{
			Tag: rs.Tag, Type: token(rs.Type), Format: token(rs.Format), Host: hostOf(rs.URL), DownloadDetour: rs.DownloadDetour,
		})
	}
	return out, l.c.Router.StagingStatus(ctx).HasDraft, nil
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

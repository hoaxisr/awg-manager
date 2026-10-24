package mcp

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type routeIDIn struct {
	RouteID string `json:"routeId" jsonschema:"route id from the corresponding list_* tool"`
}

// removedDNSOut carries the deleted list back to the caller: the deletion
// is permanent and add_dns_route cannot recreate it (it takes only
// name/domains/tunnelId, so subnets, excludes, backend, subscriptions and
// multi-target routes are lost). The record is the only thing an agent can
// show the user afterwards.
type removedDNSOut struct {
	RouteID string   `json:"routeId"`
	Removed bool     `json:"removed"`
	Route   DNSRoute `json:"route" jsonschema:"the deleted list as it was — MCP cannot restore it"`
}

// removedStaticOut is removedDNSOut for static routing lists.
type removedStaticOut struct {
	RouteID string      `json:"routeId"`
	Removed bool        `json:"removed"`
	Route   StaticRoute `json:"route" jsonschema:"the deleted list as it was — MCP cannot restore it"`
}

type dnsRoutesOut struct {
	Routes []DNSRoute `json:"routes"`
}

// updateDNSOut is the edited list plus anything the edit cost that the
// caller did not ask for.
type updateDNSOut struct {
	DNSRoute
	Warnings []string `json:"warnings,omitempty" jsonschema:"non-fatal losses the edit caused — show these to the user"`
}

// setRouteEnabledIn carries an explicit enabled flag: there is no toggle
// semantics on purpose, so an agent retrying after a timeout cannot flip
// a list back to where it started.
type setRouteEnabledIn struct {
	RouteID string `json:"routeId" jsonschema:"list id from the corresponding list_* tool"`
	Enabled bool   `json:"enabled" jsonschema:"true turns the list on, false turns it off"`
}

type setClientRouteEnabledIn struct {
	ClientIP string `json:"clientIp" jsonschema:"LAN client IPv4 from list_client_routes or list_devices"`
	Enabled  bool   `json:"enabled" jsonschema:"true routes the device through its tunnel again, false suspends the route"`
}

type dnsRouteDetailIn struct {
	RouteID       string `json:"routeId" jsonschema:"list id from list_dns_routes"`
	DomainsOffset int    `json:"domainsOffset,omitempty" jsonschema:"index of the first domain to return; default 0"`
}

// dnsRouteDetailOut is the full record with Domains replaced by one page
// of at most MaxDomainsInDetail entries. DomainCount is always the real
// size of the list, so an agent can tell a short page from a short list —
// answering "that domain is not in the list" from a truncated page is the
// failure this output is shaped to prevent.
type dnsRouteDetailOut struct {
	DNSRouteDetail
	DomainCount      int  `json:"domainCount" jsonschema:"total domains in the list, ignoring paging"`
	DomainsOffset    int  `json:"domainsOffset" jsonschema:"index of the first domain returned"`
	DomainsTruncated bool `json:"domainsTruncated" jsonschema:"true when domains beyond this page remain — call again with a larger domainsOffset before concluding a domain is absent"`
}

// pageDNSRouteDetail cuts one page out of detail.Domains. An offset past
// the end yields an empty page rather than an error: an agent walking the
// pages should be able to stop on an empty result.
func pageDNSRouteDetail(detail DNSRouteDetail, offset int) dnsRouteDetailOut {
	total := len(detail.Domains)
	page, start, truncated := pageOf(detail.Domains, offset, MaxDomainsInDetail)
	detail.Domains = page
	detail.Routes = orEmpty(detail.Routes)
	return dnsRouteDetailOut{
		DNSRouteDetail:   detail,
		DomainCount:      total,
		DomainsOffset:    start,
		DomainsTruncated: truncated,
	}
}

type staticRoutesOut struct {
	Routes []StaticRoute `json:"routes"`
}

type clientRoutesOut struct {
	Routes []ClientRoute `json:"routes"`
}

type clientRouteOut struct {
	Route   *ClientRoute `json:"route,omitempty"`
	Removed bool         `json:"removed"`
}

type policiesOut struct {
	Policies []AccessPolicy `json:"policies"`
}

type devicesOut struct {
	Devices []Device `json:"devices"`
}

// validateDomains checks only that the list is non-empty and every entry
// is non-blank. The grammar itself belongs to the dnsroute service, which
// accepts plain domains, bare labels and CIDR subnets and rejects
// geosite:/geoip: tags for NDMS lists (F700) — a second grammar here would
// drift from the service's on the next change.
func validateDomains(domains []string) error {
	if len(domains) == 0 {
		return fmt.Errorf("domains must not be empty")
	}
	for _, dom := range domains {
		if strings.TrimSpace(dom) == "" {
			return fmt.Errorf("invalid domain %q", dom)
		}
	}
	return nil
}

// routeTargetShape is the shape of a routing target id: a tunnel id, or a
// prefixed one (wan:apcli1, system:Wireguard0). Whether it exists is for
// Deps to say, against the same catalog the web interface lists.
var routeTargetShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$`)

// maxRouteTargets bounds a failover list: a handful in practice.
const maxRouteTargets = 16

func requireRouteTargets(ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("targets must name at least one target (use list_routing_targets)")
	}
	if len(ids) > maxRouteTargets {
		return nil, fmt.Errorf("targets holds more than %d entries", maxRouteTargets)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !routeTargetShape.MatchString(id) {
			return nil, fmt.Errorf("target %q is not a target id (use list_routing_targets)", id)
		}
		if slices.Contains(out, id) {
			return nil, fmt.Errorf("targets names %q twice", id)
		}
		out = append(out, id)
	}
	return out, nil
}

// subscriptionURLs checks domain list URLs before Deps: http or https with
// a host. The service fetches each one; that is where a dead link fails.
func subscriptionURLs(urls []string) ([]string, error) {
	out := make([]string, 0, len(urls))
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(raw, " \t\r\n") {
			return nil, fmt.Errorf("subscription URL must be an http or https address")
		}
		// The same URL twice would be fetched twice on every refresh.
		if !slices.Contains(out, raw) {
			out = append(out, raw)
		}
	}
	return out, nil
}

func validateCIDRs(subnets []string) error {
	if len(subnets) == 0 {
		return fmt.Errorf("subnets must not be empty")
	}
	for _, s := range subnets {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(s)); err != nil {
			return fmt.Errorf("invalid CIDR %q", s)
		}
	}
	return nil
}

type routingTargetsOut struct {
	Targets []RoutingTarget `json:"targets"`
}

func registerRoutingTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_dns_routes",
		Description: "Domain-based routing lists: which domains are sent through which tunnel.",
		Annotations: readOnly("List DNS routes"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, dnsRoutesOut, error) {
		list, err := d.ListDNSRoutes(ctx)
		list = orEmpty(list)
		return nil, dnsRoutesOut{Routes: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "get_dns_route",
		Description: "One domain routing list in full: every domain (list_dns_routes shows only the first 50), plus the excludes and subscriptions it omits entirely. " +
			"Use this to answer whether a specific domain is in a list. Domains are paged: when domainsTruncated is true, call again with domainsOffset to read on.",
		Annotations: readOnly("Get DNS route"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in dnsRouteDetailIn) (*mcp.CallToolResult, dnsRouteDetailOut, error) {
		if in.RouteID == "" {
			return nil, dnsRouteDetailOut{}, fmt.Errorf("routeId is required")
		}
		if in.DomainsOffset < 0 {
			return nil, dnsRouteDetailOut{}, fmt.Errorf("domainsOffset must not be negative")
		}
		detail, err := d.GetDNSRoute(ctx, in.RouteID)
		if err != nil {
			return nil, dnsRouteDetailOut{}, err
		}
		return nil, pageDNSRouteDetail(detail, in.DomainsOffset), nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_dns_route",
		Description: "Create a domain routing list that sends the given domains (and subdomains) through a tunnel, or through several targets with failover (targets, from list_routing_targets: tunnels, NDMS interfaces such as Proxy0, WAN). " +
			"killSwitch blocks the traffic when every target is down; excludes carves domains out; subscriptionUrls downloads domain lists into it now. The list is created enabled and takes effect immediately.",
		Annotations: safeWrite("Add DNS route", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DNSRouteInput) (*mcp.CallToolResult, DNSRoute, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, DNSRoute{}, fmt.Errorf("name is required")
		}
		switch {
		case in.TunnelID != "" && len(in.Targets) > 0:
			return nil, DNSRoute{}, fmt.Errorf("give tunnelId or targets, not both")
		case len(in.Targets) > 0:
			targets, err := requireRouteTargets(in.Targets)
			if err != nil {
				return nil, DNSRoute{}, err
			}
			in.Targets = targets
		default:
			if err := requireTunnelID(in.TunnelID); err != nil {
				return nil, DNSRoute{}, err
			}
		}
		subs, err := subscriptionURLs(in.SubscriptionURLs)
		if err != nil {
			return nil, DNSRoute{}, err
		}
		in.SubscriptionURLs = subs
		if len(in.Domains) > 0 || len(subs) == 0 {
			if err := validateDomains(in.Domains); err != nil {
				return nil, DNSRoute{}, err
			}
		}
		if len(in.Excludes) > 0 {
			if err := validateDomains(in.Excludes); err != nil {
				return nil, DNSRoute{}, fmt.Errorf("excludes: %w", err)
			}
		}
		out, err := d.AddDNSRoute(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_dns_route",
		Description: "Edit an existing domain routing list in place: rename it, replace its manual entries or excludes, add or remove domain list subscriptions, send it through a different tunnel or an ordered set of targets with failover, or switch its kill switch. " +
			"Omitted fields are left untouched and the backend always survives, which is why this is the way to change a list, " +
			"not remove_dns_route followed by add_dns_route. manualDomains and excludes each replace EVERY entry of their kind — read them from get_dns_route first. " +
			"HydraRoute lists take one target and no excludes, subscriptions or kill switch. Check the returned warnings.",
		// Not idempotent: removeSubscriptions is by position, so a retried
		// call removes the next subscription.
		Annotations: safeWrite("Update DNS route", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DNSRouteUpdate) (*mcp.CallToolResult, updateDNSOut, error) {
		if in.RouteID == "" {
			return nil, updateDNSOut{}, fmt.Errorf("routeId is required")
		}
		if strings.TrimSpace(in.Name) == "" && in.ManualDomains == nil && in.TunnelID == "" && in.Targets == nil &&
			in.KillSwitch == nil && in.Excludes == nil && len(in.AddSubscriptionURLs) == 0 && len(in.RemoveSubscriptions) == 0 {
			return nil, updateDNSOut{}, fmt.Errorf("nothing to update: pass at least one field to change")
		}
		if in.TunnelID != "" && in.Targets != nil {
			return nil, updateDNSOut{}, fmt.Errorf("give tunnelId or targets, not both")
		}
		if in.Targets != nil {
			targets, err := requireRouteTargets(in.Targets)
			if err != nil {
				return nil, updateDNSOut{}, err
			}
			in.Targets = targets
		}
		if len(in.Excludes) > 0 {
			if err := validateDomains(in.Excludes); err != nil {
				return nil, updateDNSOut{}, fmt.Errorf("excludes: %w", err)
			}
		}
		subs, err := subscriptionURLs(in.AddSubscriptionURLs)
		if err != nil {
			return nil, updateDNSOut{}, err
		}
		in.AddSubscriptionURLs = subs
		for _, i := range in.RemoveSubscriptions {
			if i < 0 {
				return nil, updateDNSOut{}, fmt.Errorf("removeSubscriptions holds positions from 0 (use get_dns_route)")
			}
		}
		if in.ManualDomains != nil {
			if err := validateDomains(in.ManualDomains); err != nil {
				return nil, updateDNSOut{}, err
			}
		}
		if in.TunnelID != "" {
			if err := requireTunnelID(in.TunnelID); err != nil {
				return nil, updateDNSOut{}, err
			}
		}
		in.Name = strings.TrimSpace(in.Name)
		updated, warnings, err := d.UpdateDNSRoute(ctx, in)
		if err != nil {
			return nil, updateDNSOut{}, err
		}
		return nil, updateDNSOut{DNSRoute: updated, Warnings: warnings}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_dns_route_enabled",
		Description: "Turn a domain routing list on or off. The list itself is kept, so this is the reversible way to stop routing a set of domains — " +
			"prefer it to remove_dns_route, which destroys the list for good. Takes effect immediately.",
		Annotations: safeWrite("Enable/disable DNS route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setRouteEnabledIn) (*mcp.CallToolResult, DNSRoute, error) {
		if in.RouteID == "" {
			return nil, DNSRoute{}, fmt.Errorf("routeId is required")
		}
		out, err := d.SetDNSRouteEnabled(ctx, in.RouteID, in.Enabled)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "remove_dns_route",
		Description: "Delete a domain routing list by id. DESTRUCTIVE: the list is deleted permanently and cannot be restored through MCP — " +
			"add_dns_route cannot bring back the backend, the icon, subscriptions whose full address you do not have, or the list's id. " +
			"Confirm with the user first; the deleted record is returned so you can show what was destroyed.",
		Annotations: destructiveWrite("Remove DNS route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in routeIDIn) (*mcp.CallToolResult, removedDNSOut, error) {
		if in.RouteID == "" {
			return nil, removedDNSOut{}, fmt.Errorf("routeId is required")
		}
		deleted, err := d.RemoveDNSRoute(ctx, in.RouteID)
		if err != nil {
			return nil, removedDNSOut{}, err
		}
		return nil, removedDNSOut{RouteID: in.RouteID, Removed: true, Route: deleted}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_static_routes",
		Description: "Subnet (CIDR) routing lists and the tunnel each one uses.",
		Annotations: readOnly("List static routes"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, staticRoutesOut, error) {
		list, err := d.ListStaticRoutes(ctx)
		list = orEmpty(list)
		return nil, staticRoutesOut{Routes: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "add_static_route",
		Description: "Create a static routing list that sends the given CIDR subnets through a target from list_routing_targets (a tunnel, an NDMS interface such as Proxy0, or WAN). " +
			"killSwitch blocks the traffic when the target is down instead of letting it out through the provider.",
		Annotations: safeWrite("Add static route", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in StaticRouteInput) (*mcp.CallToolResult, StaticRoute, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, StaticRoute{}, fmt.Errorf("name is required")
		}
		target, err := requireRouteTargets([]string{in.TunnelID})
		if err != nil {
			return nil, StaticRoute{}, fmt.Errorf("tunnelId: %w", err)
		}
		in.TunnelID = target[0]
		if err := validateCIDRs(in.Subnets); err != nil {
			return nil, StaticRoute{}, err
		}
		out, err := d.AddStaticRoute(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_static_route",
		Description: "Edit a static (CIDR) routing list in place: rename it, replace its subnets, send it through another target from list_routing_targets, or switch its kill switch. " +
			"Omitted fields are kept, as is whether the list is on. subnets replaces EVERY subnet of the list. Takes effect immediately.",
		Annotations: safeWrite("Update static route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in StaticRouteUpdate) (*mcp.CallToolResult, StaticRoute, error) {
		if in.RouteID == "" {
			return nil, StaticRoute{}, fmt.Errorf("routeId is required")
		}
		in.Name = strings.TrimSpace(in.Name)
		if in.Name == "" && in.TunnelID == "" && in.Subnets == nil && in.KillSwitch == nil {
			return nil, StaticRoute{}, fmt.Errorf("nothing to update: pass at least one field to change")
		}
		if in.TunnelID != "" {
			target, err := requireRouteTargets([]string{in.TunnelID})
			if err != nil {
				return nil, StaticRoute{}, fmt.Errorf("tunnelId: %w", err)
			}
			in.TunnelID = target[0]
		}
		if in.Subnets != nil {
			if err := validateCIDRs(in.Subnets); err != nil {
				return nil, StaticRoute{}, err
			}
		}
		out, err := d.UpdateStaticRoute(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "list_routing_targets",
		Description: "Everything a routing list (add_dns_route, add_static_route and their updates) can send traffic through: tunnels of awg-manager, NDMS interfaces such as Proxy0 or a WireGuard made in the router, and WAN connections. " +
			"available=false means it cannot carry traffic right now.",
		Annotations: readOnly("List routing targets"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, routingTargetsOut, error) {
		list, err := d.ListRoutingTargets(ctx)
		return nil, routingTargetsOut{Targets: orEmpty(list)}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_static_route_enabled",
		Description: "Turn a static (CIDR) routing list on or off, keeping the list itself. The reversible alternative to remove_static_route, " +
			"which destroys the list and every subnet in it. Takes effect immediately.",
		Annotations: safeWrite("Enable/disable static route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setRouteEnabledIn) (*mcp.CallToolResult, StaticRoute, error) {
		if in.RouteID == "" {
			return nil, StaticRoute{}, fmt.Errorf("routeId is required")
		}
		out, err := d.SetStaticRouteEnabled(ctx, in.RouteID, in.Enabled)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "remove_static_route",
		Description: "Delete a static routing list by id. DESTRUCTIVE: the list and every subnet in it are deleted permanently and cannot be restored through MCP. " +
			"Confirm with the user first; the deleted record is returned so you can show what was destroyed.",
		Annotations: destructiveWrite("Remove static route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in routeIDIn) (*mcp.CallToolResult, removedStaticOut, error) {
		if in.RouteID == "" {
			return nil, removedStaticOut{}, fmt.Errorf("routeId is required")
		}
		deleted, err := d.RemoveStaticRoute(ctx, in.RouteID)
		if err != nil {
			return nil, removedStaticOut{}, err
		}
		return nil, removedStaticOut{RouteID: in.RouteID, Removed: true, Route: deleted}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_client_routes",
		Description: "Per-device routes: LAN clients pinned to a specific tunnel.",
		Annotations: readOnly("List client routes"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, clientRoutesOut, error) {
		list, err := d.ListClientRoutes(ctx)
		list = orEmpty(list)
		return nil, clientRoutesOut{Routes: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_client_route",
		Description: "Route one LAN device (by IP from list_devices) through a tunnel, or pass an empty tunnelId to remove its route. Idempotent.",
		Annotations: safeWrite("Set client route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ClientRouteInput) (*mcp.CallToolResult, clientRouteOut, error) {
		ip := net.ParseIP(strings.TrimSpace(in.ClientIP))
		if ip == nil || ip.To4() == nil {
			return nil, clientRouteOut{}, fmt.Errorf("clientIp %q is not a valid IPv4 address", in.ClientIP)
		}
		// Deps compares this value against the stored, already-canonical IP,
		// so only the canonical spelling may cross the boundary: forwarding
		// " 192.168.1.10" or "::ffff:192.168.1.10" verbatim would miss the
		// existing route and report a delete that never happened (or fail
		// with "already has a route" instead of updating).
		in.ClientIP = ip.To4().String()
		if in.Fallback != "" && in.Fallback != "drop" && in.Fallback != "bypass" {
			return nil, clientRouteOut{}, fmt.Errorf("fallback must be drop or bypass")
		}
		// Empty means "remove the route"; anything else must be a real id.
		if in.TunnelID != "" {
			if err := requireTunnelID(in.TunnelID); err != nil {
				return nil, clientRouteOut{}, err
			}
		}
		route, err := d.SetClientRoute(ctx, in)
		if err != nil {
			return nil, clientRouteOut{}, err
		}
		return nil, clientRouteOut{Route: route, Removed: route == nil}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "set_client_route_enabled",
		Description: "Switch one device's route off or on without removing it — the device falls back to normal routing while disabled, " +
			"and the route keeps its tunnel and fallback for when it is switched back. To remove the route entirely, call set_client_route with an empty tunnelId.",
		Annotations: safeWrite("Enable/disable client route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in setClientRouteEnabledIn) (*mcp.CallToolResult, ClientRoute, error) {
		ip := net.ParseIP(strings.TrimSpace(in.ClientIP))
		if ip == nil || ip.To4() == nil {
			return nil, ClientRoute{}, fmt.Errorf("clientIp %q is not a valid IPv4 address", in.ClientIP)
		}
		// Canonical spelling only, for the same reason as set_client_route:
		// Deps matches this against the stored, already-canonical IP.
		out, err := d.SetClientRouteEnabled(ctx, ip.To4().String(), in.Enabled)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_access_policies",
		Description: "Keenetic access policies (per-device internet profiles) and the interfaces each permits.",
		Annotations: readOnly("List access policies"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, policiesOut, error) {
		list, err := d.ListAccessPolicies(ctx)
		list = orEmpty(list)
		return nil, policiesOut{Policies: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_devices",
		Description: "LAN devices known to the router: IP, MAC, hostname, active flag and assigned access policy.",
		Annotations: readOnly("List devices"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, devicesOut, error) {
		list, err := d.ListDevices(ctx)
		list = orEmpty(list)
		return nil, devicesOut{Devices: list}, err
	})
}

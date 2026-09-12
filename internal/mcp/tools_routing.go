package mcp

import (
	"context"
	"fmt"
	"net"
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
	start := min(offset, total)
	end := min(start+MaxDomainsInDetail, total)
	// Three-index slice: the page must not be able to grow into the rest
	// of the list through an append somewhere downstream.
	detail.Domains = detail.Domains[start:end:end]
	if detail.Domains == nil {
		detail.Domains = []string{}
	}
	if detail.Routes == nil {
		detail.Routes = []RouteTarget{}
	}
	return dnsRouteDetailOut{
		DNSRouteDetail:   detail,
		DomainCount:      total,
		DomainsOffset:    start,
		DomainsTruncated: end < total,
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
// accepts plain domains, bare labels, CIDR subnets and geosite:/geoip:
// tags — a second, stricter grammar here would reject entries the service
// is happy with (and would drift from it on the next change).
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

func registerRoutingTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_dns_routes",
		Description: "Domain-based routing lists: which domains are sent through which tunnel.",
		Annotations: readOnly("List DNS routes"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, dnsRoutesOut, error) {
		list, err := d.ListDNSRoutes(ctx)
		if list == nil {
			list = []DNSRoute{}
		}
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
		Name:        "add_dns_route",
		Description: "Create a domain routing list that sends the given domains (and subdomains) through a tunnel. The list is created enabled and takes effect immediately.",
		Annotations: safeWrite("Add DNS route", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DNSRouteInput) (*mcp.CallToolResult, DNSRoute, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, DNSRoute{}, fmt.Errorf("name is required")
		}
		if err := requireTunnelID(in.TunnelID); err != nil {
			return nil, DNSRoute{}, err
		}
		if err := validateDomains(in.Domains); err != nil {
			return nil, DNSRoute{}, err
		}
		out, err := d.AddDNSRoute(ctx, in)
		return nil, out, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: "update_dns_route",
		Description: "Edit an existing domain routing list in place: rename it, replace its manual entries, or send it through a different tunnel. " +
			"Omitted fields are left untouched; excludes, subscriptions and the backend always survive, which is why this is the way to change a list, " +
			"not remove_dns_route followed by add_dns_route. manualDomains replaces EVERY manual entry, CIDR subnets included — read them from get_dns_route first. Check the returned warnings.",
		Annotations: safeWrite("Update DNS route", true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in DNSRouteUpdate) (*mcp.CallToolResult, updateDNSOut, error) {
		if in.RouteID == "" {
			return nil, updateDNSOut{}, fmt.Errorf("routeId is required")
		}
		if strings.TrimSpace(in.Name) == "" && in.ManualDomains == nil && in.TunnelID == "" {
			return nil, updateDNSOut{}, fmt.Errorf("nothing to update: pass at least one of name, manualDomains or tunnelId")
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
			"add_dns_route only takes name/domains/tunnelId, so subnets, excludes, backend, subscriptions and extra route targets are lost for good. " +
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
		if list == nil {
			list = []StaticRoute{}
		}
		return nil, staticRoutesOut{Routes: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "add_static_route",
		Description: "Create a static routing list that sends the given CIDR subnets through a tunnel.",
		Annotations: safeWrite("Add static route", false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in StaticRouteInput) (*mcp.CallToolResult, StaticRoute, error) {
		if strings.TrimSpace(in.Name) == "" {
			return nil, StaticRoute{}, fmt.Errorf("name is required")
		}
		if err := requireTunnelID(in.TunnelID); err != nil {
			return nil, StaticRoute{}, err
		}
		if err := validateCIDRs(in.Subnets); err != nil {
			return nil, StaticRoute{}, err
		}
		out, err := d.AddStaticRoute(ctx, in)
		return nil, out, err
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
		if list == nil {
			list = []ClientRoute{}
		}
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
		if list == nil {
			list = []AccessPolicy{}
		}
		return nil, policiesOut{Policies: list}, err
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_devices",
		Description: "LAN devices known to the router: IP, MAC, hostname, active flag and assigned access policy.",
		Annotations: readOnly("List devices"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, devicesOut, error) {
		list, err := d.ListDevices(ctx)
		if list == nil {
			list = []Device{}
		}
		return nil, devicesOut{Devices: list}, err
	})
}

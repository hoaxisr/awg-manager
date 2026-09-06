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

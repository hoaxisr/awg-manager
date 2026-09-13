package mcp

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxTargetLen is the longest DNS name (RFC 1035), the upper bound on
// what explain_route hands to the resolver.
const maxTargetLen = 253

type explainIn struct {
	Target   string `json:"target" jsonschema:"a domain (e.g. \"www.youtube.com\") or a literal IPv4 address to explain"`
	ClientIP string `json:"clientIp,omitempty" jsonschema:"optional LAN device IPv4: include the device's own route in the answer"`
}

// explainDNSMatch is one domain list that covers the target, and the
// entry that made it match — an agent that can quote the entry can tell
// the user which line to edit.
type explainDNSMatch struct {
	RouteID      string `json:"routeId"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled" jsonschema:"a disabled list matches but routes nothing"`
	MatchedEntry string `json:"matchedEntry" jsonschema:"the list entry the target matched, exactly as stored"`
	TunnelID     string `json:"tunnelId,omitempty"`
	TunnelName   string `json:"tunnelName,omitempty"`
}

// explainStaticMatch is one subnet list covering one of the target's
// addresses.
type explainStaticMatch struct {
	RouteID      string `json:"routeId"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	MatchedEntry string `json:"matchedEntry" jsonschema:"the CIDR that contains the address"`
	MatchedIP    string `json:"matchedIp" jsonschema:"the address of the target that fell inside it"`
	TunnelID     string `json:"tunnelId,omitempty"`
	TunnelName   string `json:"tunnelName,omitempty"`
}

// explainUnevaluated names a list this tool could not decide about. It
// exists so an empty dnsMatches never silently means "no rule covers
// this": geosite:/geoip: tags are expanded on the router from data files
// MCP has no access to.
type explainUnevaluated struct {
	RouteID string `json:"routeId"`
	Name    string `json:"name"`
	Reason  string `json:"reason"`
}

// explainExcluded is a list that matched the target and then carved it
// out again through its excludes. The list does not route it.
type explainExcluded struct {
	RouteID      string `json:"routeId"`
	Name         string `json:"name"`
	MatchedEntry string `json:"matchedEntry"`
	ExcludedBy   string `json:"excludedBy" jsonschema:"the exclude entry that removes the target from this list"`
}

type explainOut struct {
	Target      string   `json:"target"`
	IsIP        bool     `json:"isIp" jsonschema:"true when the target was given as a literal address"`
	ResolvedIPs []string `json:"resolvedIps" jsonschema:"IPv4 addresses the target resolves to right now; the router may resolve it differently later"`
	// ResolveError is set when the lookup failed. Domain matching still
	// ran; only the subnet comparison is missing.
	ResolveError         string               `json:"resolveError,omitempty"`
	DNSMatches           []explainDNSMatch    `json:"dnsMatches"`
	StaticMatches        []explainStaticMatch `json:"staticMatches"`
	ClientRoute          *ClientRoute         `json:"clientRoute,omitempty" jsonschema:"the device's own route, when clientIp was given"`
	ExcludedFrom         []explainExcluded    `json:"excludedFrom,omitempty" jsonschema:"lists that would match but explicitly exclude the target — they do NOT route it"`
	UnevaluatedLists     []explainUnevaluated `json:"unevaluatedLists,omitempty" jsonschema:"lists this tool could not decide about — check them before telling the user nothing matches"`
	DefaultRouteTunnelID string               `json:"defaultRouteTunnelId,omitempty" jsonschema:"tunnel carrying the default route; traffic matching no rule goes here, or straight out the WAN when empty"`
	DefaultRouteTunnel   string               `json:"defaultRouteTunnelName,omitempty"`
	Note                 string               `json:"note" jsonschema:"how the matches above relate to each other in plain words"`
}

// normalizeDomain lowercases and strips the root dot so "WWW.Example.COM."
// and "www.example.com" compare equal.
func normalizeDomain(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// domainCovers reports whether a routing-list entry covers target. The
// daemon's lists are suffix matches — an entry routes the domain and its
// subdomains — so "youtube.com" covers "www.youtube.com" but never
// "notyoutube.com".
func domainCovers(entry, target string) bool {
	entry = normalizeDomain(entry)
	if entry == "" {
		return false
	}
	return target == entry || strings.HasSuffix(target, "."+entry)
}

// unevaluatableEntry reports whether an entry is one this tool cannot
// decide locally: geosite:/geoip: tags expand from data files on the
// router. Only those two prefixes count — the ones dnsroute itself
// recognises — because a bare IPv6 literal also contains colons.
func unevaluatableEntry(entry string) bool {
	return strings.HasPrefix(entry, "geosite:") || strings.HasPrefix(entry, "geoip:")
}

// matchDNSList decides one list against a target. It returns the matching
// entry, and separately whether the list held entries that could not be
// judged here.
func matchDNSList(entries []string, target string, ips []net.IP) (matched string, unevaluated bool) {
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if unevaluatableEntry(entry) {
			unevaluated = true
			continue
		}
		if _, subnet, err := net.ParseCIDR(entry); err == nil {
			for _, ip := range ips {
				if subnet.Contains(ip) {
					return entry, unevaluated
				}
			}
			continue
		}
		if target != "" && domainCovers(entry, target) {
			return entry, unevaluated
		}
	}
	return "", unevaluated
}

// matchSubnets returns the first CIDR in list containing one of ips, and
// separately whether the list held geoip: tags — dnsroute stores those
// under Subnets, and a list made only of them must surface as
// unevaluated rather than as a clean miss.
func matchSubnets(subnets []string, ips []net.IP) (cidr string, hit string, unevaluated bool) {
	for _, s := range subnets {
		s = strings.TrimSpace(s)
		if unevaluatableEntry(s) {
			unevaluated = true
			continue
		}
		_, subnet, err := net.ParseCIDR(s)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			if subnet.Contains(ip) {
				return s, ip.String(), unevaluated
			}
		}
	}
	return "", "", unevaluated
}

// explainNote states in words how the matches relate. It deliberately
// stops short of naming a single winner when a device route and a list
// both apply: the device rule captures everything that device sends,
// while a list applies to that destination for every device, and which
// one wins is decided by the router's rule priorities, not here.
func explainNote(out *explainOut) string {
	var parts []string
	if out.ClientRoute != nil && out.ClientRoute.Enabled {
		parts = append(parts, fmt.Sprintf("the device %s has its own route through tunnel %s, which covers everything it sends, whatever the destination", out.ClientRoute.ClientIP, out.ClientRoute.TunnelID))
	}
	switch n := len(out.DNSMatches) + len(out.StaticMatches); {
	case n == 0 && len(out.UnevaluatedLists) > 0:
		parts = append(parts, "no list matched by name or subnet, but the lists under unevaluatedLists use geosite:/geoip: tags that only the router can expand — do not conclude the target is unrouted without checking them")
	case n == 0:
		parts = append(parts, "no routing list covers this target, so it follows the default route")
	case n > 1:
		parts = append(parts, "more than one list covers this target; the router applies them in its own order, so treat the list above as candidates rather than a decision")
	}
	// The NDMS lists are only half the picture wherever sing-box does the
	// routing: its rules are evaluated separately and this tool does not
	// read them. Saying so on every answer beats confidently naming a
	// tunnel that sing-box then overrides.
	parts = append(parts, "if the sing-box router is in use, its own rules apply too and are not covered here — check list_singbox_rules")
	if len(parts) == 1 {
		return "one routing list covers this target; " + parts[0]
	}
	return strings.Join(parts, "; ")
}

func registerExplainTools(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "explain_route",
		Description: "Explain which routing rules cover a domain or IP: the domain lists that match it (checked against every domain, not the truncated list view), " +
			"the subnet lists containing its addresses, the device's own route when clientIp is given, and the default route it falls back to. " +
			"Reports the candidates and how they relate; it does not simulate the router's rule priorities, and lists using geosite:/geoip: tags are reported as unevaluated rather than assumed not to match.",
		Annotations: readOnly("Explain route"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in explainIn) (*mcp.CallToolResult, explainOut, error) {
		target := normalizeDomain(in.Target)
		if target == "" {
			return nil, explainOut{}, fmt.Errorf("target is required (a domain or an IPv4 address)")
		}
		// A DNS name is at most 253 octets; anything longer is not a name
		// the router's resolver should be asked about.
		if len(target) > maxTargetLen {
			return nil, explainOut{}, fmt.Errorf("target is longer than %d characters", maxTargetLen)
		}
		out := explainOut{Target: target, ResolvedIPs: []string{}, DNSMatches: []explainDNSMatch{}, StaticMatches: []explainStaticMatch{}}

		var ips []net.IP
		if ip := net.ParseIP(target); ip != nil && ip.To4() != nil {
			// A literal address: nothing to resolve, and no domain to
			// compare against list entries.
			out.IsIP = true
			ips = []net.IP{ip.To4()}
			out.ResolvedIPs = []string{ip.To4().String()}
			target = ""
		} else {
			addrs, err := d.ResolveDomain(ctx, target)
			if err != nil {
				// Subnet comparison is lost, domain matching is not — so
				// report the failure and carry on rather than fail the call.
				out.ResolveError = err.Error()
			}
			for _, a := range addrs {
				if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
					ips = append(ips, ip.To4())
					out.ResolvedIPs = append(out.ResolvedIPs, ip.To4().String())
				}
			}
		}

		if in.ClientIP != "" {
			ip := net.ParseIP(strings.TrimSpace(in.ClientIP))
			if ip == nil || ip.To4() == nil {
				return nil, explainOut{}, fmt.Errorf("clientIp %q is not a valid IPv4 address", in.ClientIP)
			}
			routes, err := d.ListClientRoutes(ctx)
			if err != nil {
				return nil, explainOut{}, err
			}
			want := ip.To4().String()
			for i := range routes {
				if routes[i].ClientIP == want {
					out.ClientRoute = &routes[i]
					break
				}
			}
		}

		tunnels, err := d.ListTunnels(ctx)
		if err != nil {
			return nil, explainOut{}, err
		}
		names := make(map[string]string, len(tunnels))
		for _, t := range tunnels {
			names[t.ID] = t.Name
			if t.DefaultRoute {
				out.DefaultRouteTunnelID, out.DefaultRouteTunnel = t.ID, t.Name
			}
		}

		// Full records in one call: the list view caps Domains, and matching
		// against a truncated list would answer "no rule covers this" for a
		// rule that does; per-id reads would re-read HydraRoute's files
		// once per list.
		details, err := d.ListDNSRouteDetails(ctx)
		if err != nil {
			return nil, explainOut{}, err
		}
		for _, detail := range details {
			entry, unevaluated := matchDNSList(detail.Domains, target, ips)
			if entry == "" {
				sub, _, subUnevaluated := matchSubnets(detail.Subnets, ips)
				entry = sub
				unevaluated = unevaluated || subUnevaluated
			}
			// An exclude carves the target back out of the list: the router
			// pushes excludes to NDMS as real exceptions, so a match here
			// would report a tunnel the traffic never takes. Reported
			// separately rather than dropped — "no rule" and "explicitly
			// carved out" are different answers. Checked by name AND by
			// address: a CIDR may sit among Excludes (before the service
			// splits it out) or under ExcludeSubnets, and a literal-IP
			// target has only its address to be carved out by.
			if entry != "" {
				ex, _ := matchDNSList(detail.Excludes, target, ips)
				if ex == "" {
					ex, _, _ = matchSubnets(detail.ExcludeSubnets, ips)
				}
				if ex != "" {
					out.ExcludedFrom = append(out.ExcludedFrom, explainExcluded{RouteID: detail.ID, Name: detail.Name, MatchedEntry: entry, ExcludedBy: ex})
					continue
				}
			}
			if entry != "" {
				m := explainDNSMatch{RouteID: detail.ID, Name: detail.Name, Enabled: detail.Enabled, MatchedEntry: entry}
				if len(detail.Routes) > 0 {
					m.TunnelID = detail.Routes[0].TunnelID
					m.TunnelName = names[m.TunnelID]
				}
				out.DNSMatches = append(out.DNSMatches, m)
				continue
			}
			if unevaluated {
				out.UnevaluatedLists = append(out.UnevaluatedLists, explainUnevaluated{
					RouteID: detail.ID, Name: detail.Name,
					Reason: "contains geosite:/geoip: tags, which only the router can expand",
				})
			}
		}

		statics, err := d.ListStaticRoutes(ctx)
		if err != nil {
			return nil, explainOut{}, err
		}
		for _, sr := range statics {
			cidr, hit, _ := matchSubnets(sr.Subnets, ips)
			if cidr == "" {
				continue
			}
			out.StaticMatches = append(out.StaticMatches, explainStaticMatch{
				RouteID: sr.ID, Name: sr.Name, Enabled: sr.Enabled,
				MatchedEntry: cidr, MatchedIP: hit,
				TunnelID: sr.TunnelID, TunnelName: names[sr.TunnelID],
			})
		}

		out.Note = explainNote(&out)
		return nil, out, nil
	})
}

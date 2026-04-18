package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/routing"
)

// ndmsClient is the subset of *transport.Client used by this service.
// Both endpoints we read (/show/ip/hotspot and /show/object-group/fqdn) are
// ad-hoc raw RCI reads with no benefit from typed Stores.
type ndmsClient interface {
	GetRaw(ctx context.Context, path string) ([]byte, error)
}

// compile-time check: *transport.Client must satisfy ndmsClient
var _ ndmsClient = (*transport.Client)(nil)

// Service provides connection listing with tunnel and DNS-rule resolution.
type Service struct {
	catalog routing.Catalog
	ndms    ndmsClient
	lister  DNSListLister
}

// NewService creates a new connections service.
//
// The lister is used to resolve DNS-route list IDs into display names when
// attributing connections to rules. Pass nil to disable name resolution
// (rule attribution still works, just without ListName).
func NewService(catalog routing.Catalog, ndmsClient ndmsClient, lister DNSListLister) *Service {
	return &Service{
		catalog: catalog,
		ndms:    ndmsClient,
		lister:  lister,
	}
}

// List reads conntrack, resolves tunnels and client names, filters, and paginates.
func (s *Service) List(ctx context.Context, params ListParams) (*ListResponse, error) {
	// Normalize params
	if params.Limit <= 0 {
		params.Limit = 50
	}
	if params.Limit > 500 {
		params.Limit = 500
	}
	if params.Offset < 0 {
		params.Offset = 0
	}
	if params.Tunnel == "" {
		params.Tunnel = "all"
	}
	if params.Protocol == "" {
		params.Protocol = "all"
	}

	// 1. Read conntrack
	rawConns, err := readConntrackFile()
	if err != nil {
		return nil, fmt.Errorf("read conntrack: %w", err)
	}

	// 2. Build ifindex map
	ifMap := buildIfindexMap()

	// 3. Build reverse tunnel lookup: kernel iface → (tunnelID, tunnelName)
	tunnelByIface := s.buildTunnelMap(ctx)

	// 4. Resolve client MACs
	clientNames := s.resolveClientNames(ctx)

	// 4.5. Lazy fetch DNS-route runtime cache and build IP -> rules map.
	// Best-effort: any failure leaves the map empty and connections still
	// return without rule attribution.
	ipRules := s.fetchIPRules(ctx)

	// 5. Enrich connections
	conns := make([]Connection, 0, len(rawConns))
	for _, rc := range rawConns {
		// Skip router-local traffic without outgoing interface
		if rc.ifw == 0 {
			continue
		}

		c := rc.Connection
		c.Interface = ifMap[rc.ifw]

		// Resolve tunnel
		if info, ok := tunnelByIface[c.Interface]; ok {
			c.TunnelID = info.id
			c.TunnelName = info.name
		} else if c.Interface != "" {
			c.TunnelName = fmt.Sprintf("Direct (%s)", c.Interface)
		} else {
			c.TunnelName = "Direct"
		}

		// Resolve client name
		if c.ClientMAC != "" {
			c.ClientName = clientNames[strings.ToLower(c.ClientMAC)]
		}

		// Attach DNS-route rule attribution by destination IP.
		if rules, ok := ipRules[c.Dst]; ok {
			c.Rules = rules
		}

		conns = append(conns, c)
	}

	// 6. Compute stats (over ALL connections, before filtering)
	stats, tunnelSummary := computeStats(conns)

	// 6.5. Ensure all running tunnels appear in the summary even with 0 connections.
	for iface, ti := range tunnelByIface {
		if _, exists := tunnelSummary[ti.id]; !exists {
			tunnelSummary[ti.id] = TunnelConnectionInfo{
				Name:      ti.name,
				Interface: iface,
				Count:     0,
			}
		}
	}

	// 7. Filter
	filtered := applyFilters(conns, params)

	// 7.5. Optional sort over the filtered set (before pagination so sort
	// spans the full dataset, not just the current page).
	applySort(filtered, params.SortBy, params.SortDir)

	// 8. Paginate
	total := len(filtered)
	start := params.Offset
	if start > total {
		start = total
	}
	end := start + params.Limit
	if end > total {
		end = total
	}
	page := filtered[start:end]

	// Ensure non-nil slices/maps for JSON
	if page == nil {
		page = []Connection{}
	}
	if tunnelSummary == nil {
		tunnelSummary = make(map[string]TunnelConnectionInfo)
	}

	return &ListResponse{
		Stats:       stats,
		Tunnels:     tunnelSummary,
		Connections: page,
		Pagination: PaginationInfo{
			Total:    total,
			Offset:   start,
			Limit:    params.Limit,
			Returned: len(page),
		},
		FetchedAt: time.Now().Format(time.RFC3339),
	}, nil
}

type tunnelInfo struct {
	id   string
	name string
}

// buildTunnelMap creates kernel iface name → tunnel info mapping.
func (s *Service) buildTunnelMap(ctx context.Context) map[string]tunnelInfo {
	result := make(map[string]tunnelInfo)

	entries := s.catalog.ListAll(ctx)
	for _, e := range entries {
		if e.Type == "wan" {
			continue
		}
		kernelIface, running := s.catalog.GetKernelIface(ctx, e.ID)
		if !running || kernelIface == "" {
			continue
		}
		result[kernelIface] = tunnelInfo{
			id:   e.ID,
			name: e.Name,
		}
	}

	return result
}

// resolveClientNames queries NDMS hotspot for MAC → device name mapping.
func (s *Service) resolveClientNames(ctx context.Context) map[string]string {
	result := make(map[string]string)

	raw, err := s.ndms.GetRaw(ctx, "/show/ip/hotspot")
	if err != nil {
		return result
	}

	var resp struct {
		Host []struct {
			MAC      string `json:"mac"`
			Name     string `json:"name"`
			Hostname string `json:"hostname"`
		} `json:"host"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return result
	}

	for _, h := range resp.Host {
		mac := strings.ToLower(h.MAC)
		name := h.Name
		if name == "" {
			name = h.Hostname
		}
		if name != "" {
			result[mac] = name
		}
	}

	return result
}

// fetchIPRules queries the NDMS object-group runtime cache and returns
// a map from destination IP to matched DNS-route rule hits. Best-effort:
// any error returns an empty map without surfacing the error to the caller —
// the connections list still works without rule attribution.
func (s *Service) fetchIPRules(ctx context.Context) map[string][]RuleHit {
	raw, err := s.ndms.GetRaw(ctx, "/show/object-group/fqdn")
	if err != nil {
		return nil
	}
	groups, err := parseObjectGroupRuntime(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	return buildIPRuleMap(ctx, groups, s.lister)
}

// computeStats calculates aggregate statistics and per-tunnel counts.
func computeStats(conns []Connection) (ConnectionStats, map[string]TunnelConnectionInfo) {
	var stats ConnectionStats
	tunnels := make(map[string]TunnelConnectionInfo)

	for _, c := range conns {
		stats.Total++

		switch strings.ToLower(c.Protocol) {
		case "tcp":
			stats.Protocols.TCP++
		case "udp":
			stats.Protocols.UDP++
		case "icmp", "icmpv6":
			stats.Protocols.ICMP++
		}

		if c.TunnelID != "" {
			stats.Tunneled++
		} else {
			stats.Direct++
		}

		key := c.TunnelID // "" for direct
		info := tunnels[key]
		info.Count++
		if info.Name == "" {
			info.Name = c.TunnelName
			info.Interface = c.Interface
		}
		tunnels[key] = info
	}

	return stats, tunnels
}

// applyFilters returns connections matching the given params.
func applyFilters(conns []Connection, params ListParams) []Connection {
	var result []Connection

	search := strings.ToLower(params.Search)

	for _, c := range conns {
		// Tunnel filter
		switch params.Tunnel {
		case "all":
			// pass
		case "direct":
			if c.TunnelID != "" {
				continue
			}
		default:
			if c.TunnelID != params.Tunnel {
				continue
			}
		}

		// Protocol filter
		if params.Protocol != "all" {
			if !strings.EqualFold(c.Protocol, params.Protocol) {
				// icmp filter also matches icmpv6
				if !(params.Protocol == "icmp" && strings.EqualFold(c.Protocol, "icmpv6")) {
					continue
				}
			}
		}

		// Search filter
		if search != "" {
			haystack := strings.ToLower(c.Src + " " + c.Dst + " " + c.ClientName)
			if !strings.Contains(haystack, search) {
				continue
			}
		}

		result = append(result, c)
	}

	return result
}

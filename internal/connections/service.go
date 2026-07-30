package connections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms/transport"
	"github.com/hoaxisr/awg-manager/internal/routing"
)

// ndmsClient is the subset of *transport.Client used by this service.
// Both endpoints we read (/show/ip/hotspot and /show/object-group/fqdn) are
// ad-hoc raw RCI reads decoded once — GetStream avoids buffering the full
// response body into memory before parsing.
type ndmsClient interface {
	GetStream(ctx context.Context, path string, fn func(io.Reader) error) error
}

// compile-time check: *transport.Client must satisfy ndmsClient
var _ ndmsClient = (*transport.Client)(nil)

// Service provides connection listing with tunnel and DNS-rule resolution.
type Service struct {
	catalog routing.Catalog
	ndms    ndmsClient
	lister  DNSListLister
	appLog  *logging.ScopedLogger

	// ifaceAddrs перечисляет локальные адреса kernel-интерфейса.
	// Поле, а не прямой вызов net.InterfaceByName — для юнит-тестов.
	ifaceAddrs func(name string) ([]net.Addr, error)

	// sbMarkProvider отдаёт PolicyMark sb-router-политики (hex-строка
	// вида "0xffffaaa"), когда tproxy-движок активен. nil = не подключён.
	sbMarkProvider func(ctx context.Context) (string, bool)

	// Источники имён WG-пиров (issue #639); nil = резолвим только по MAC.
	wgServers      WGServerPeersLister
	managedServers ManagedServersLister

	markMu      sync.Mutex
	markCached  uint32
	markOK      bool
	markFetched time.Time

	rulesMu      sync.Mutex
	rulesCached  map[string][]RuleHit
	rulesFetched time.Time
}

// NewService creates a new connections service.
//
// The lister is used to resolve DNS-route list IDs into display names when
// attributing connections to rules. Pass nil to disable name resolution
// (rule attribution still works, just without ListName).
func NewService(catalog routing.Catalog, ndmsClient ndmsClient, lister DNSListLister, appLogger logging.AppLogger) *Service {
	return &Service{
		catalog: catalog,
		ndms:    ndmsClient,
		lister:  lister,
		appLog:  logging.NewScopedLogger(appLogger, logging.GroupSystem, logging.SubConnections),
		ifaceAddrs: func(name string) ([]net.Addr, error) {
			ifi, err := net.InterfaceByName(name)
			if err != nil {
				return nil, err
			}
			return ifi.Addrs()
		},
	}
}

// SetSingboxMarkProvider wires the sb-router policy-mark source used to
// attribute tproxy-intercepted flows (no ifw in conntrack).
func (s *Service) SetSingboxMarkProvider(fn func(ctx context.Context) (string, bool)) {
	s.sbMarkProvider = fn
}

const markTTL = 60 * time.Second

// singboxMark returns the numeric sb-router connmark, cached for markTTL.
func (s *Service) singboxMark(ctx context.Context) (uint32, bool) {
	if s.sbMarkProvider == nil {
		return 0, false
	}
	s.markMu.Lock()
	defer s.markMu.Unlock()
	if time.Since(s.markFetched) < markTTL {
		return s.markCached, s.markOK
	}
	s.markFetched = time.Now()
	s.markCached, s.markOK = 0, false
	if raw, ok := s.sbMarkProvider(ctx); ok {
		hex := strings.TrimPrefix(strings.ToLower(raw), "0x")
		if v, err := strconv.ParseUint(hex, 16, 32); err == nil {
			s.markCached, s.markOK = uint32(v), true
		}
	}
	return s.markCached, s.markOK
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
		s.appLog.Warn("read-conntrack", "", err.Error())
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

	// 5. Enrich connections. Записи без ifw НЕ отбрасываются — классифицируются.
	localIPs := s.tunnelLocalIPs(tunnelByIface)
	wanIPs := s.wanLocalIPs(s.wanIfaces(ctx))
	sbMark, sbMarkOK := s.singboxMark(ctx)

	conns := make([]Connection, 0, len(rawConns))
	for _, rc := range rawConns {
		c := s.classify(rc, ifMap, tunnelByIface, localIPs, wanIPs, sbMark, sbMarkOK)

		if c.ClientMAC != "" {
			c.ClientName = clientNames[strings.ToLower(c.ClientMAC)]
		}
		if rules, ok := ipRules[c.Dst]; ok {
			c.Rules = rules
		}
		conns = append(conns, c)
	}

	// 5.5. Клиенты из туннелей приходят без MAC — доименовываем их по IP
	// из описаний WG-пиров, чтобы они выглядели как LAN-устройства (#639).
	applyPeerNames(conns, s.resolvePeerNames(ctx))

	// 6. Compute stats (over ALL connections, before filtering)
	stats, tunnelSummary := computeStats(conns)

	byTunnel := computeBuckets(conns, tunnelBucketKey)
	byClient := computeBuckets(conns, clientBucketKey)
	byDst := computeBuckets(conns, dstBucketKey)

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
		ByTunnel:    byTunnel,
		ByClient:    byClient,
		ByDst:       byDst,
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

// localIPHit — атрибуция локального IP к туннелю и его kernel-интерфейсу.
type localIPHit struct {
	iface string
	info  tunnelInfo
}

// tunnelLocalIPs maps each running tunnel's local interface IPs to the
// tunnel — the attribution signal for conntrack entries without ifw:
// src ∈ map — эгресс, привязанный к туннелю; reply-dst ∈ map — SNAT-выход.
func (s *Service) tunnelLocalIPs(tunnelByIface map[string]tunnelInfo) map[string]localIPHit {
	out := make(map[string]localIPHit)
	for iface, ti := range tunnelByIface {
		addrs, err := s.ifaceAddrs(iface)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP == nil {
				continue
			}
			out[ipn.IP.String()] = localIPHit{iface: iface, info: ti}
		}
	}
	return out
}

// wanIfaces returns kernel iface names of WAN catalog entries (buildTunnelMap
// их пропускает). Нужны, чтобы форвардный SNAT-трафик в WAN без ifw
// классифицировался как direct, а не «Локально».
func (s *Service) wanIfaces(ctx context.Context) []string {
	var out []string
	for _, e := range s.catalog.ListAll(ctx) {
		if e.Type != "wan" {
			continue
		}
		if iface, running := s.catalog.GetKernelIface(ctx, e.ID); running && iface != "" {
			out = append(out, iface)
		}
	}
	return out
}

// wanLocalIPs maps local IPs of WAN interfaces to the iface name.
func (s *Service) wanLocalIPs(ifaces []string) map[string]string {
	out := make(map[string]string)
	for _, iface := range ifaces {
		addrs, err := s.ifaceAddrs(iface)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP != nil {
				out[ipn.IP.String()] = iface
			}
		}
	}
	return out
}

// classify enriches one raw conntrack entry with route attribution.
// Записи без ifw НЕ отбрасываются (fix: невидимые sing-box/nwg потоки):
// они классифицируются по локальным IP туннелей/WAN и connmark sb-router.
func (s *Service) classify(rc rawConn, ifMap map[int]string,
	tunnelByIface map[string]tunnelInfo, localIPs map[string]localIPHit,
	wanIPs map[string]string, sbMark uint32, sbMarkOK bool) Connection {

	c := rc.Connection
	if rc.ifw != 0 {
		c.Interface = ifMap[rc.ifw]
		if info, ok := tunnelByIface[c.Interface]; ok {
			c.TunnelID, c.TunnelName, c.RouteClass = info.id, info.name, "tunnel"
		} else if c.Interface != "" {
			c.TunnelName = fmt.Sprintf("Direct (%s)", c.Interface)
			c.RouteClass = "direct"
		} else {
			c.TunnelName = "Direct"
			c.RouteClass = "direct"
		}
		return c
	}
	if hit, ok := localIPs[c.Src]; ok {
		c.Interface = hit.iface
		c.TunnelID, c.TunnelName, c.RouteClass = hit.info.id, hit.info.name, "tunnel"
		return c
	}
	if hit, ok := localIPs[rc.replyDst]; ok {
		c.Interface = hit.iface
		c.TunnelID, c.TunnelName, c.RouteClass = hit.info.id, hit.info.name, "tunnel"
		return c
	}
	// Форвардный SNAT в WAN без ifw — это трафик клиента напрямую, не «Локально».
	if iface, ok := wanIPs[rc.replyDst]; ok {
		c.Interface = iface
		c.TunnelName = fmt.Sprintf("Direct (%s)", iface)
		c.RouteClass = "direct"
		return c
	}
	if sbMarkOK && rc.mark == sbMark {
		c.TunnelName = "sing-box"
		c.RouteClass = "singbox"
		return c
	}
	c.TunnelName = "Локально"
	c.RouteClass = "local"
	return c
}

// resolveClientNames queries NDMS hotspot for MAC → device name mapping.
func (s *Service) resolveClientNames(ctx context.Context) map[string]string {
	result := make(map[string]string)

	var resp struct {
		Host []struct {
			MAC      string `json:"mac"`
			Name     string `json:"name"`
			Hostname string `json:"hostname"`
		} `json:"host"`
	}
	err := s.ndms.GetStream(ctx, "/show/ip/hotspot", func(r io.Reader) error {
		return json.NewDecoder(r).Decode(&resp)
	})
	if err != nil {
		s.appLog.Warn("resolve-clients", "ndms.hotspot", err.Error())
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

// ipRulesTTL bounds how often the NDMS object-group runtime is re-fetched.
// The /show/object-group/fqdn response is large (сотни KB) и стоит ndm ~1s
// на сериализацию; перечитывание на каждый List доминировало в CPU-профиле
// (стенд 2026-07-16: ~40% под опросом страницы соединений). Атрибуция
// меняется только когда DNS-правила резолвят новые IP — лёгкая несвежесть
// бейджей незаметна.
const ipRulesTTL = 30 * time.Second

// fetchIPRules queries the NDMS object-group runtime cache and returns
// a map from destination IP to matched DNS-route rule hits, served from a
// TTL cache (mirrors singboxMark: мьютекс держится на время фетча — второй
// конкурентный List ждёт и получает кэш, а не дублирует 730KB-запрос;
// ошибка negative-кэшируется на тот же TTL). Best-effort: любая ошибка
// даёт пустую карту — список соединений работает без атрибуции правил.
// Возвращаемая карта разделяется между вызовами — НЕ мутировать.
func (s *Service) fetchIPRules(ctx context.Context) map[string][]RuleHit {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	if time.Since(s.rulesFetched) < ipRulesTTL {
		return s.rulesCached
	}
	s.rulesFetched = time.Now()
	s.rulesCached = nil
	var groups []runtimeGroup
	err := s.ndms.GetStream(ctx, "/show/object-group/fqdn", func(r io.Reader) error {
		var parseErr error
		groups, parseErr = parseObjectGroupRuntime(r)
		return parseErr
	})
	if err != nil {
		s.appLog.Warn("fetch-rules", "ndms.object-group/fqdn", err.Error())
		return nil
	}
	s.rulesCached = buildIPRuleMap(ctx, groups, s.lister)
	return s.rulesCached
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

		switch c.RouteClass {
		case "tunnel":
			stats.Tunneled++
		case "singbox":
			stats.Singbox++
		case "local":
			stats.Local++
		default:
			stats.Direct++
		}

		key := c.TunnelID
		if key == "" {
			key = "@" + c.RouteClass // "@direct" / "@singbox" / "@local"
		}
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
			if c.RouteClass != "direct" {
				continue
			}
		case "singbox":
			if c.RouteClass != "singbox" {
				continue
			}
		case "local":
			if c.RouteClass != "local" {
				continue
			}
		default:
			if c.TunnelID != params.Tunnel {
				continue
			}
		}

		// State filter (точное совпадение; "all"/"" = любое).
		if params.State != "" && params.State != "all" && c.State != params.State {
			continue
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

		// Search filter — matches src/dst IPs, client name, and either port
		// (rendered as decimal so "443" or ":443" both hit).
		if search != "" {
			haystack := strings.ToLower(
				c.Src + ":" + strconv.Itoa(c.SrcPort) + " " +
					c.Dst + ":" + strconv.Itoa(c.DstPort) + " " +
					c.ClientName,
			)
			if !strings.Contains(haystack, search) {
				continue
			}
		}

		result = append(result, c)
	}

	return result
}

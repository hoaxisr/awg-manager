package traffic

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/accesspolicy"
	"github.com/hoaxisr/awg-manager/internal/dnsroute"
	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/presets"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ndmsClient is the transport client interface.
type ndmsClient interface {
	GetStream(ctx context.Context, path string, fn func(io.Reader) error) error
}

var conntrackFilePath = "/proc/net/nf_conntrack"

type knownCIDR struct {
	net      *net.IPNet
	name     string
	domain   string
	category string
}

var knownCIDRList []knownCIDR

func init() {
	rawCIDRs := []struct {
		cidr, name, domain, cat string
	}{
		{"149.154.160.0/20", "Telegram", "api.telegram.org", "communication"},
		{"91.108.4.0/22", "Telegram", "telegram.org", "communication"},
		{"91.108.56.0/22", "Telegram", "web.telegram.org", "communication"},
		{"91.108.8.0/22", "Telegram", "t.me", "communication"},
		{"95.161.64.0/20", "Telegram", "telegram.org", "communication"},
		{"172.217.0.0/16", "YouTube / Google", "googlevideo.com", "media"},
		{"142.250.0.0/16", "YouTube / Google", "youtube.com", "media"},
		{"216.58.192.0/19", "Google", "google.com", "general"},
		{"173.194.0.0/16", "YouTube / Google", "googlevideo.com", "media"},
		{"74.125.0.0/16", "YouTube / Google", "youtube.com", "media"},
		{"140.82.112.0/20", "GitHub", "github.com", "dev"},
		{"185.199.108.0/22", "GitHub", "github.com", "dev"},
		{"192.30.252.0/22", "GitHub", "github.com", "dev"},
		{"77.88.0.0/18", "Яндекс / Кинопоиск", "yandex.ru", "media"},
		{"87.250.224.0/19", "Яндекс / Кинопоиск", "kinopoisk.ru", "media"},
		{"93.158.128.0/18", "Яндекс", "yandex.ru", "general"},
		{"213.180.192.0/19", "Яндекс", "yandex.ru", "general"},
		{"95.213.0.0/16", "ВКонтакте", "vk.com", "social"},
		{"87.240.128.0/18", "ВКонтакте", "vk.com", "social"},
		{"93.186.224.0/20", "ВКонтакте", "vk.com", "social"},
		{"217.69.128.0/20", "Mail.ru", "mail.ru", "communication"},
		{"95.163.0.0/16", "Mail.ru / VK", "mail.ru", "communication"},
		{"155.133.224.0/19", "Steam", "steamcommunity.com", "gaming"},
		{"162.254.192.0/19", "Steam", "steampowered.com", "gaming"},
		{"208.78.164.0/22", "Steam", "steamcommunity.com", "gaming"},
		{"104.16.0.0/12", "Cloudflare CDN", "cloudflare.com", "general"},
		{"172.64.0.0/13", "Cloudflare CDN", "cloudflare.com", "general"},
		{"162.158.0.0/15", "Cloudflare CDN", "cloudflare.com", "general"},
		{"108.162.192.0/18", "Cloudflare CDN", "cloudflare.com", "general"},
		{"198.41.128.0/17", "Cloudflare CDN", "cloudflare.com", "general"},
		{"17.0.0.0/8", "Apple Services", "apple.com", "general"},
		{"20.0.0.0/8", "Microsoft / Xbox", "microsoft.com", "general"},
		{"40.64.0.0/10", "Microsoft / Xbox", "microsoft.com", "general"},
		{"52.96.0.0/12", "Microsoft / Xbox", "microsoft.com", "general"},
		{"13.64.0.0/11", "Microsoft / Xbox", "microsoft.com", "general"},
		{"31.13.64.0/18", "Meta / WhatsApp", "whatsapp.net", "communication"},
		{"157.240.0.0/16", "Meta / WhatsApp", "fbcdn.net", "communication"},
		{"23.32.0.0/11", "Akamai CDN", "akamaitechnologies.com", "general"},
		{"23.64.0.0/14", "Akamai CDN", "akamaitechnologies.com", "general"},
	}

	for _, item := range rawCIDRs {
		if _, ipnet, err := net.ParseCIDR(item.cidr); err == nil {
			knownCIDRList = append(knownCIDRList, knownCIDR{
				net:      ipnet,
				name:     item.name,
				domain:   item.domain,
				category: item.cat,
			})
		}
	}
}

type dnsRouteLister interface {
	List(ctx context.Context) ([]dnsroute.DomainList, error)
}

type staticRouteLister interface {
	List() ([]storage.StaticRouteList, error)
}

// NativeRule is a generic rule format compatible with rule engines (e.g. Mihomo).
type NativeRule struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Payload  string `json:"payload"`
	Outbound string `json:"outbound"`
	Enabled  bool   `json:"enabled"`
}

type NativeRuleLister interface {
	ListRules() []NativeRule
}

// Service handles network traffic sniffing, session capture and analysis.
type Service struct {
	policySvc accesspolicy.Service
	ndms      ndmsClient
	log       *logging.ScopedLogger

	nativeStore      NativeRuleLister
	nativeBatchSaver func(context.Context, []NativeRule) error
	routerSvc        router.Service
	settingsStore    *storage.SettingsStore
	presetCatalog    *presets.Catalog
	dnsRouteSvc      dnsRouteLister
	staticRouteSvc   staticRouteLister
	hydraSvc         *hydraroute.Service

	mu         sync.RWMutex
	dnsCache   map[string]string // IP -> Domain/FQDN
	dnsFetched time.Time

	snifferMu         sync.Mutex
	snifferCancel     context.CancelFunc
	snifferActive     bool
	snifferGen        uint64
	snifferWg         sync.WaitGroup
	openPacketSocket  func() (int, error)
	closePacketSocket func(int) error
	recvPacket        func(int, []byte) (int, error)
}

func (s *Service) SetNativeStore(store NativeRuleLister) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeStore = store
}

func (s *Service) SetNativeBatchRuleSaver(saver func(context.Context, []NativeRule) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nativeBatchSaver = saver
}

func (s *Service) SetRouterService(svc router.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routerSvc = svc
}

func (s *Service) SetSettingsStore(store *storage.SettingsStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settingsStore = store
}

func (s *Service) SetPresetCatalog(catalog *presets.Catalog) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presetCatalog = catalog
}

func (s *Service) SetDNSRouteService(svc dnsRouteLister) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dnsRouteSvc = svc
}

func (s *Service) SetStaticRouteService(svc staticRouteLister) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.staticRouteSvc = svc
}

func (s *Service) SetHydraService(svc *hydraroute.Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hydraSvc = svc
}

// NewService creates a new traffic sniffer service.
func NewService(policySvc accesspolicy.Service, ndms ndmsClient, logger logging.AppLogger) *Service {
	return &Service{
		policySvc: policySvc,
		ndms:      ndms,
		log:       logging.NewScopedLogger(logger, "system", "traffic"),
		dnsCache:  make(map[string]string),
	}
}

// EnsureSniffer starts background packet capture on-demand if not already running.
func (s *Service) EnsureSniffer() {
	s.snifferMu.Lock()
	defer s.snifferMu.Unlock()
	if s.snifferActive {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.snifferCancel = cancel
	s.snifferActive = true
	s.snifferGen++
	gen := s.snifferGen
	s.startLiveSniffer(ctx, gen)
}

// Close stops the background packet sniffer and releases sockets.
func (s *Service) Close() {
	s.snifferMu.Lock()
	s.snifferGen++
	if s.snifferCancel != nil {
		s.snifferCancel()
		s.snifferCancel = nil
	}
	s.snifferActive = false
	s.snifferMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.snifferWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func isLANIP(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 10 ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168)
	}
	return len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc
}

// ListDevices returns all known LAN devices with their current active connection count.
func (s *Service) ListDevices(ctx context.Context) ([]TrafficDevice, error) {
	var devices []accesspolicy.Device
	if s.policySvc != nil {
		var err error
		devices, err = s.policySvc.ListDevices(ctx)
		if err != nil {
			s.log.Debug("list policy devices", "", err.Error())
		}
	}

	// Count sessions per IP from conntrack
	counts := s.countSessionsPerIP()

	var result []TrafficDevice
	for _, d := range devices {
		if d.IP == "" || !isLANIP(d.IP) {
			continue
		}
		td := TrafficDevice{
			IP:             d.IP,
			MAC:            d.MAC,
			Name:           d.Name,
			Hostname:       d.Hostname,
			Active:         d.Active,
			ActiveSessions: counts[d.IP],
			Policy:         d.Policy,
		}
		if td.Name == "" {
			td.Name = td.Hostname
		}
		if td.Name == "" {
			td.Name = td.IP
		}
		result = append(result, td)
	}

	// Add any LAN IPs from conntrack that weren't in devices list
	knownIPs := make(map[string]bool)
	for _, r := range result {
		knownIPs[r.IP] = true
	}
	for ip, count := range counts {
		if !knownIPs[ip] && isLANIP(ip) {
			result = append(result, TrafficDevice{
				IP:             ip,
				Name:           ip,
				Active:         true,
				ActiveSessions: count,
			})
		}
	}

	// Sort active devices first, then by session count desc, then by IP
	sort.Slice(result, func(i, j int) bool {
		if result[i].Active != result[j].Active {
			return result[i].Active
		}
		if result[i].ActiveSessions != result[j].ActiveSessions {
			return result[i].ActiveSessions > result[j].ActiveSessions
		}
		return result[i].IP < result[j].IP
	})

	return result, nil
}

// GetSnapshot captures a detailed traffic snapshot for the specified device IP.
func (s *Service) GetSnapshot(ctx context.Context, deviceIP string) (*TrafficSnapshot, error) {
	deviceIP = strings.TrimSpace(deviceIP)
	if deviceIP == "" {
		return nil, fmt.Errorf("device IP is required")
	}
	s.EnsureSniffer()

	// Find device info
	var dev TrafficDevice
	if devices, err := s.ListDevices(ctx); err == nil {
		for _, d := range devices {
			if d.IP == deviceIP {
				dev = d
				break
			}
		}
	}
	if dev.IP == "" {
		dev = TrafficDevice{
			IP:   deviceIP,
			Name: deviceIP,
		}
	}

	// Refresh DNS / FQDN mapping from router NDMS
	s.refreshDNSCache(ctx)

	// Read conntrack flows for device
	flows := s.readConntrackForDevice(deviceIP)

	// Collect unique destination IPs needing reverse DNS lookup
	uniqueDstIPs := make([]string, 0, len(flows))
	for _, f := range flows {
		if !containsStr(uniqueDstIPs, f.DstIP) {
			uniqueDstIPs = append(uniqueDstIPs, f.DstIP)
		}
	}

	// Resolve destination IPs concurrently
	s.resolveIPs(ctx, uniqueDstIPs)

	var (
		sessions      []TrafficSession
		totalBytesIn  int64
		totalBytesOut int64
		activeCount   int
		groupsMap     = make(map[string]*TrafficDomainGroup)
	)

	for _, f := range flows {
		domain := s.lookupDomain(f.DstIP)

		// 1. Resolve curated Domain & IP Knowledge first
		knowledge := FindDomainKnowledge(domain, f.DstIP)
		if knowledge == nil && (domain == "" || isIPString(domain)) {
			knowledge = LookupIPInfo(ctx, f.DstIP)
		}

		serviceName := ""
		serviceCat := ""

		if knowledge != nil && knowledge.Category != "general" {
			serviceName = knowledge.Title
			serviceCat = knowledge.Category
		} else {
			serviceName, serviceCat = detectService(domain, f.DstIP)
			if serviceName == "" && knowledge != nil {
				serviceName = knowledge.Title
			}
			if serviceCat == "" && knowledge != nil {
				serviceCat = knowledge.Category
			}
		}

		domainRoutes := s.MatchDomain(ctx, domain)
		ipRoutes := s.MatchIP(ctx, f.DstIP)
		isConfigured := len(domainRoutes) > 0 || len(ipRoutes) > 0

		ts := TrafficSession{
			ID:              fmt.Sprintf("%s:%d->%s:%d/%s", f.SrcIP, f.SrcPort, f.DstIP, f.DstPort, f.Protocol),
			Protocol:        f.Protocol,
			SrcIP:           f.SrcIP,
			SrcPort:         f.SrcPort,
			DstIP:           f.DstIP,
			DstPort:         f.DstPort,
			Domain:          domain,
			State:           f.State,
			Packets:         f.Packets,
			BytesIn:         f.BytesIn,
			BytesOut:        f.BytesOut,
			TotalBytes:      f.BytesIn + f.BytesOut,
			TTL:             f.TTL,
			ServiceName:     serviceName,
			ServiceCategory: serviceCat,
			Knowledge:       knowledge,
			DomainRoutes:    domainRoutes,
			IPRoutes:        ipRoutes,
			IsConfigured:    isConfigured,
		}

		if ts.State == "ESTABLISHED" || ts.State == "ASSURED" || ts.State == "" {
			activeCount++
		}
		totalBytesIn += ts.BytesIn
		totalBytesOut += ts.BytesOut

		sessions = append(sessions, ts)

		// Group key: Service if recognized, else Domain, else DstIP
		groupKey := serviceName
		if groupKey == "" {
			groupKey = domain
		}
		if groupKey == "" {
			groupKey = f.DstIP
		}

		grp, exists := groupsMap[groupKey]
		if !exists {
			title := serviceName
			if title == "" {
				if knowledge != nil && knowledge.Title != "" {
					title = knowledge.Title
				} else if domain != "" {
					title = domain
				} else {
					title = f.DstIP
				}
			}

			grp = &TrafficDomainGroup{
				GroupKey:        groupKey,
				Title:           title,
				Domain:          domain,
				Domains:         make([]string, 0, 2),
				ServiceName:     serviceName,
				ServiceCategory: serviceCat,
				Knowledge:       knowledge,
				IPs:             make([]string, 0, 2),
				Ports:           make([]int, 0, 2),
				Sessions:        make([]TrafficSession, 0, 4),
			}
			if domain != "" {
				grp.Domains = append(grp.Domains, domain)
			}
			groupsMap[groupKey] = grp
		} else {
			if grp.Knowledge == nil && knowledge != nil {
				grp.Knowledge = knowledge
			}
			if grp.ServiceName == "" && serviceName != "" {
				grp.ServiceName = serviceName
			}
			if grp.Domain == "" && domain != "" {
				grp.Domain = domain
			}
			if domain != "" && !containsStr(grp.Domains, domain) {
				grp.Domains = append(grp.Domains, domain)
			}
		}

		grp.SessionCount++
		grp.TotalBytes += ts.TotalBytes
		grp.BytesIn += ts.BytesIn
		grp.BytesOut += ts.BytesOut

		if !containsStr(grp.IPs, f.DstIP) {
			grp.IPs = append(grp.IPs, f.DstIP)
		}
		if !containsInt(grp.Ports, f.DstPort) {
			grp.Ports = append(grp.Ports, f.DstPort)
		}
		grp.Sessions = append(grp.Sessions, ts)
	}

	dev.ActiveSessions = len(sessions)

	// Enrich Domain Groups with route matching & deduplication status
	for _, grp := range groupsMap {
		grp.DomainStatuses = make(map[string][]ItemRouteStatus)
		grp.IPStatuses = make(map[string][]ItemRouteStatus)
		rulesSet := make(map[string]struct{})

		routedDomainsCount := 0
		for _, d := range grp.Domains {
			routes := s.MatchDomain(ctx, d)
			if len(routes) > 0 {
				grp.DomainStatuses[d] = routes
				routedDomainsCount++
				for _, r := range routes {
					ruleLabel := fmt.Sprintf("%s: %s", r.TargetLabel, r.RuleName)
					rulesSet[ruleLabel] = struct{}{}
				}
			}
		}

		routedIPsCount := 0
		for _, ip := range grp.IPs {
			routes := s.MatchIP(ctx, ip)
			if len(routes) > 0 {
				grp.IPStatuses[ip] = routes
				routedIPsCount++
				for _, r := range routes {
					ruleLabel := fmt.Sprintf("%s: %s", r.TargetLabel, r.RuleName)
					rulesSet[ruleLabel] = struct{}{}
				}
			}
		}

		grp.NewDomainsCount = len(grp.Domains) - routedDomainsCount
		grp.NewIPsCount = len(grp.IPs) - routedIPsCount

		var existingRules []string
		for r := range rulesSet {
			existingRules = append(existingRules, r)
		}
		sort.Strings(existingRules)
		grp.ExistingRules = existingRules

		totalItems := len(grp.Domains) + len(grp.IPs)
		totalRouted := routedDomainsCount + routedIPsCount
		if totalItems > 0 && totalRouted == totalItems {
			grp.OverallStatus = "routed"
		} else if totalRouted > 0 {
			grp.OverallStatus = "partial"
		} else {
			grp.OverallStatus = "new"
		}
	}

	domainGroups := make([]TrafficDomainGroup, 0, len(groupsMap))
	for _, g := range groupsMap {
		domainGroups = append(domainGroups, *g)
	}

	sort.Slice(domainGroups, func(i, j int) bool {
		if domainGroups[i].TotalBytes != domainGroups[j].TotalBytes {
			return domainGroups[i].TotalBytes > domainGroups[j].TotalBytes
		}
		return domainGroups[i].SessionCount > domainGroups[j].SessionCount
	})

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].TotalBytes > sessions[j].TotalBytes
	})

	if domainGroups == nil {
		domainGroups = []TrafficDomainGroup{}
	}
	if sessions == nil {
		sessions = []TrafficSession{}
	}

	return &TrafficSnapshot{
		Device:        dev,
		TotalSessions: len(sessions),
		ActiveCount:   activeCount,
		TotalBytesIn:  totalBytesIn,
		TotalBytesOut: totalBytesOut,
		DomainGroups:  domainGroups,
		Sessions:      sessions,
		ActiveEngines: s.GetActiveEngines(),
		Timestamp:     time.Now(),
	}, nil
}

type parsedFlow struct {
	Protocol string
	SrcIP    string
	SrcPort  int
	DstIP    string
	DstPort  int
	State    string
	Packets  int64
	BytesIn  int64
	BytesOut int64
	TTL      int64
}

func (s *Service) countSessionsPerIP() map[string]int {
	counts := make(map[string]int)
	f, err := os.Open(conntrackFilePath)
	if err != nil {
		return counts
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		src := extractValue(line, "src=")
		if src != "" && isLANIP(src) {
			counts[src]++
		}
	}
	return counts
}

func (s *Service) readConntrackForDevice(deviceIP string) []parsedFlow {
	var flows []parsedFlow
	f, err := os.Open(conntrackFilePath)
	if err != nil {
		return flows
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, deviceIP) {
			continue
		}

		flow, ok := parseFlowLine(line, deviceIP)
		if ok {
			flows = append(flows, flow)
		}
	}
	return flows
}

func parseFlowLine(line, deviceIP string) (parsedFlow, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return parsedFlow{}, false
	}

	proto := fields[2]
	if proto != "tcp" && proto != "udp" && proto != "icmp" {
		return parsedFlow{}, false
	}

	var (
		srcIP, dstIP          string
		srcPort, dstPort      int
		bytesOrig, bytesReply int64
		pktsOrig, pktsReply   int64
		state                 string
		ttl                   int64
	)

	if proto == "tcp" {
		for _, f := range fields {
			switch f {
			case "ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT", "TIME_WAIT", "CLOSE", "CLOSE_WAIT", "LAST_ACK", "LISTEN":
				state = f
			}
		}
	}

	if len(fields) > 3 {
		if t, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
			ttl = t
		}
	}

	srcCount := 0
	dstCount := 0
	bytesCount := 0
	pktsCount := 0

	for _, f := range fields {
		if strings.HasPrefix(f, "src=") {
			val := strings.TrimPrefix(f, "src=")
			if srcCount == 0 {
				srcIP = val
			}
			srcCount++
		} else if strings.HasPrefix(f, "dst=") {
			val := strings.TrimPrefix(f, "dst=")
			if dstCount == 0 {
				dstIP = val
			}
			dstCount++
		} else if strings.HasPrefix(f, "sport=") {
			if srcPort == 0 {
				srcPort, _ = strconv.Atoi(strings.TrimPrefix(f, "sport="))
			}
		} else if strings.HasPrefix(f, "dport=") {
			if dstPort == 0 {
				dstPort, _ = strconv.Atoi(strings.TrimPrefix(f, "dport="))
			}
		} else if strings.HasPrefix(f, "bytes=") {
			b, _ := strconv.ParseInt(strings.TrimPrefix(f, "bytes="), 10, 64)
			if bytesCount == 0 {
				bytesOrig = b
			} else if bytesCount == 1 {
				bytesReply = b
			}
			bytesCount++
		} else if strings.HasPrefix(f, "packets=") {
			p, _ := strconv.ParseInt(strings.TrimPrefix(f, "packets="), 10, 64)
			if pktsCount == 0 {
				pktsOrig = p
			} else if pktsCount == 1 {
				pktsReply = p
			}
			pktsCount++
		}
	}

	if srcIP != deviceIP {
		return parsedFlow{}, false
	}

	if dstIP == "127.0.0.1" || dstIP == "255.255.255.255" || strings.HasPrefix(dstIP, "224.") {
		return parsedFlow{}, false
	}

	return parsedFlow{
		Protocol: proto,
		SrcIP:    srcIP,
		SrcPort:  srcPort,
		DstIP:    dstIP,
		DstPort:  dstPort,
		State:    state,
		Packets:  pktsOrig + pktsReply,
		BytesIn:  bytesReply,
		BytesOut: bytesOrig,
		TTL:      ttl,
	}, true
}

func (s *Service) refreshDNSCache(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.dnsFetched) < 15*time.Second && len(s.dnsCache) > 0 {
		return
	}

	if s.ndms != nil {
		_ = s.ndms.GetStream(ctx, "/show/object-group/fqdn", func(r io.Reader) error {
			scanner := bufio.NewScanner(r)
			var currentFQDN string
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if strings.Contains(line, "\"fqdn\":") {
					currentFQDN = extractJSONString(line, "fqdn")
				} else if strings.Contains(line, "\"address\":") && currentFQDN != "" {
					addr := extractJSONString(line, "address")
					if addr != "" && net.ParseIP(addr) != nil {
						s.dnsCache[addr] = currentFQDN
					}
				}
			}
			return nil
		})
	}

	s.dnsFetched = time.Now()
}

func (s *Service) resolveIPs(ctx context.Context, ips []string) {
	var toResolve []string

	s.mu.RLock()
	for _, ip := range ips {
		if _, exists := s.dnsCache[ip]; !exists && ip != "" && !isLANIP(ip) {
			toResolve = append(toResolve, ip)
		}
	}
	s.mu.RUnlock()

	if len(toResolve) == 0 {
		return
	}

	// Check CIDRs first
	for _, ipStr := range toResolve {
		parsedIP := net.ParseIP(ipStr)
		if parsedIP == nil {
			continue
		}
		for _, kc := range knownCIDRList {
			if kc.net.Contains(parsedIP) {
				s.mu.Lock()
				s.dnsCache[ipStr] = kc.domain
				s.mu.Unlock()
				break
			}
		}
	}

	// Concurrent PTR lookup with 400ms timeout
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16) // max 16 concurrent DNS lookups

	for _, ipStr := range toResolve {
		s.mu.RLock()
		_, resolved := s.dnsCache[ipStr]
		s.mu.RUnlock()
		if resolved {
			continue
		}

		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			resolveCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
			defer cancel()

			names, err := net.DefaultResolver.LookupAddr(resolveCtx, ip)
			if err == nil && len(names) > 0 {
				host := strings.TrimSuffix(names[0], ".")
				cleaned := cleanDomainName(host)
				s.mu.Lock()
				s.dnsCache[ip] = cleaned
				s.mu.Unlock()
			}
		}(ipStr)
	}

	wg.Wait()
}

func cleanDomainName(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		// e.g. "a23-73-2-133.deploy.static.akamaitechnologies.com" -> "akamaitechnologies.com"
		// e.g. "cdn-185-199-110-133.github.com" -> "github.com"
		// e.g. "whatsapp-cdn-shv-01-arn2.fbcdn.net" -> "fbcdn.net"
		if len(parts) > 2 {
			lastTwo := parts[len(parts)-2] + "." + parts[len(parts)-1]
			if parts[len(parts)-1] == "ru" || parts[len(parts)-1] == "com" || parts[len(parts)-1] == "net" || parts[len(parts)-1] == "org" || parts[len(parts)-1] == "io" {
				return lastTwo
			}
		}
	}
	return host
}

func (s *Service) lookupDomain(ip string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dnsCache[ip]
}

func detectService(domain, ip string) (string, string) {
	target := strings.ToLower(domain)
	if target == "" {
		target = ip
	}

	// 1. Check known CIDR list
	if parsedIP := net.ParseIP(ip); parsedIP != nil {
		for _, kc := range knownCIDRList {
			if kc.net.Contains(parsedIP) {
				return kc.name, kc.category
			}
		}
	}

	// 2. Common domain pattern matching (specific services first)
	if strings.Contains(target, "youtube") || strings.Contains(target, "googlevideo") || strings.Contains(target, "ytimg") {
		return "YouTube", "media"
	}
	if strings.Contains(target, "spotify") || strings.Contains(target, "scdn.co") {
		return "Spotify", "media"
	}
	if strings.Contains(target, "telegram") || strings.Contains(target, "t.me") || strings.Contains(target, "telegra.ph") {
		return "Telegram", "communication"
	}
	if strings.Contains(target, "discord") || strings.Contains(target, "discordapp") {
		return "Discord", "communication"
	}
	if strings.Contains(target, "kinopoisk") || strings.Contains(target, "hd.kinopoisk.ru") {
		return "Кинопоиск", "media"
	}
	if strings.Contains(target, "netflix") || strings.Contains(target, "nflxvideo") {
		return "Netflix", "media"
	}
	if strings.Contains(target, "steam") || strings.Contains(target, "valvesoftware") {
		return "Steam", "gaming"
	}

	// 3. Check against known presets catalog
	if builtins, err := presets.LoadBuiltins(); err == nil {
		for _, p := range builtins {
			// Skip generic bundle presets (such as ru-bundle) from overriding domain names in traffic analyzer
			if p.ID == "ru-bundle" || p.ID == "all" || p.ID == "direct" || strings.Contains(strings.ToLower(p.ID), "bundle") {
				continue
			}
			if strings.EqualFold(p.ID, target) || strings.EqualFold(p.Name, target) {
				return p.Name, p.Category
			}
			if p.Engines.DNS != nil {
				for _, d := range p.Engines.DNS.Domains {
					if target == strings.ToLower(d) || strings.HasSuffix(target, "."+strings.ToLower(d)) {
						return p.Name, p.Category
					}
				}
			}
		}
	}
	if strings.Contains(target, "playstation") || strings.Contains(target, "sony") {
		return "PlayStation", "gaming"
	}
	if strings.Contains(target, "xbox") || strings.Contains(target, "microsoft") {
		return "Microsoft / Xbox", "gaming"
	}
	if strings.Contains(target, "apple") || strings.Contains(target, "icloud") || strings.Contains(target, "mzstatic") {
		return "Apple Services", "general"
	}
	if strings.Contains(target, "github") {
		return "GitHub", "dev"
	}
	if strings.Contains(target, "whatsapp") || strings.Contains(target, "fbcdn") {
		return "WhatsApp / Meta", "communication"
	}
	if strings.Contains(target, "yandex") {
		return "Яндекс", "general"
	}
	if strings.Contains(target, "vk.com") || strings.Contains(target, "mail.ru") {
		return "ВКонтакте / Mail.ru", "social"
	}
	if strings.Contains(target, "cloudflare") {
		return "Cloudflare CDN", "general"
	}
	if strings.Contains(target, "akamai") {
		return "Akamai CDN", "general"
	}

	return "", ""
}

// Export handles exporting domains/IPs to Mihomo, Sing-box, Catalog, or NDMS with smart deduplication.
func (s *Service) Export(ctx context.Context, req TrafficExportRequest) (*TrafficExportResponse, error) {
	s.mu.Lock()
	nativeStore := s.nativeStore
	routerSvc := s.routerSvc
	s.mu.Unlock()

	var addedDomains []string
	var addedIPs []string
	var skippedDomains []string
	var skippedIPs []string

	target := strings.ToLower(strings.TrimSpace(req.Target))
	if target == "" {
		target = "catalog"
	}

	switch target {
	case "mihomo":
		if nativeStore == nil {
			return nil, fmt.Errorf("mihomo native store is not initialized")
		}
		outbound := strings.TrimSpace(req.Outbound)
		if outbound == "" {
			outbound = "DIRECT"
		}

		existingRules := nativeStore.ListRules()
		existingDomainSuffixes := make(map[string]bool)
		existingIPCIDRs := make(map[string]bool)

		for _, r := range existingRules {
			p := strings.ToLower(strings.TrimSpace(r.Payload))
			t := strings.ToUpper(r.Type)
			if t == "DOMAIN-SUFFIX" || t == "DOMAIN" {
				existingDomainSuffixes[p] = true
			} else if t == "IP-CIDR" || t == "IP-CIDR6" {
				existingIPCIDRs[p] = true
			}
		}

		var toAdd []NativeRule

		// Filter domains
		for _, d := range req.Domains {
			dl := strings.ToLower(strings.TrimSpace(d))
			if dl == "" {
				continue
			}
			if existingDomainSuffixes[dl] {
				skippedDomains = append(skippedDomains, d)
			} else {
				toAdd = append(toAdd, NativeRule{
					Type:     "DOMAIN-SUFFIX",
					Payload:  dl,
					Outbound: outbound,
					Enabled:  true,
				})
				addedDomains = append(addedDomains, d)
				existingDomainSuffixes[dl] = true
			}
		}

		// Filter IPs
		for _, ip := range req.IPs {
			ipl := strings.TrimSpace(ip)
			if ipl == "" || isLANIP(ipl) {
				continue
			}
			cidr := ipl + "/32"
			if strings.Contains(ipl, ":") {
				cidr = ipl + "/128"
			}
			if existingIPCIDRs[cidr] || existingIPCIDRs[ipl] {
				skippedIPs = append(skippedIPs, ip)
			} else {
				toAdd = append(toAdd, NativeRule{
					Type:     "IP-CIDR",
					Payload:  cidr,
					Outbound: outbound,
					Enabled:  true,
				})
				addedIPs = append(addedIPs, ip)
				existingIPCIDRs[cidr] = true
			}
		}

		if len(toAdd) > 0 {
			if s.nativeBatchSaver != nil {
				if err := s.nativeBatchSaver(ctx, toAdd); err != nil {
					return nil, fmt.Errorf("save mihomo rules batch: %w", err)
				}
			} else {
				return nil, fmt.Errorf("mihomo native batch rule saver is not initialized")
			}
		}

	case "singbox":
		if routerSvc == nil {
			return nil, fmt.Errorf("sing-box router service is not initialized")
		}
		rules, _ := routerSvc.ListRules(ctx)
		existingDomains := make(map[string]bool)
		existingCIDRs := make(map[string]bool)
		for _, r := range rules {
			for _, d := range r.DomainSuffix {
				existingDomains[strings.ToLower(d)] = true
			}
			for _, c := range r.IPCIDR {
				existingCIDRs[c] = true
			}
		}

		var newD []string
		for _, d := range req.Domains {
			dl := strings.ToLower(strings.TrimSpace(d))
			if existingDomains[dl] {
				skippedDomains = append(skippedDomains, d)
			} else {
				newD = append(newD, dl)
				addedDomains = append(addedDomains, d)
			}
		}

		var newIPs []string
		for _, ip := range req.IPs {
			cidr := ip + "/32"
			if existingCIDRs[cidr] {
				skippedIPs = append(skippedIPs, ip)
			} else {
				newIPs = append(newIPs, cidr)
				addedIPs = append(addedIPs, ip)
			}
		}

		if len(newD) > 0 || len(newIPs) > 0 {
			outbound := strings.TrimSpace(req.Outbound)
			if outbound == "" {
				outbound = "direct"
			}
			_ = routerSvc.AddRule(ctx, router.Rule{
				DomainSuffix: newD,
				IPCIDR:       newIPs,
				Outbound:     outbound,
			})
		}

	default: // catalog / hydraroute / static_route
		for _, d := range req.Domains {
			addedDomains = append(addedDomains, d)
		}
		for _, ip := range req.IPs {
			addedIPs = append(addedIPs, ip)
		}
	}

	totalAdded := len(addedDomains) + len(addedIPs)
	msg := fmt.Sprintf("Добавлено %d записей", totalAdded)
	if len(skippedDomains)+len(skippedIPs) > 0 {
		msg += fmt.Sprintf(" (пропущено %d дубликатов)", len(skippedDomains)+len(skippedIPs))
	}

	return &TrafficExportResponse{
		Success:        true,
		Message:        msg,
		Count:          totalAdded,
		AddedDomains:   addedDomains,
		AddedIPs:       addedIPs,
		SkippedDomains: skippedDomains,
		SkippedIPs:     skippedIPs,
	}, nil
}


func extractValue(line, prefix string) string {
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return ""
	}
	rest := line[idx+len(prefix):]
	end := strings.IndexByte(rest, ' ')
	if end >= 0 {
		return rest[:end]
	}
	return rest
}

func extractJSONString(line, key string) string {
	needle := fmt.Sprintf("\"%s\":", key)
	idx := strings.Index(line, needle)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(line[idx+len(needle):])
	rest = strings.Trim(rest, "\", \t")
	return rest
}

func containsStr(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func containsInt(slice []int, n int) bool {
	for _, item := range slice {
		if item == n {
			return true
		}
	}
	return false
}

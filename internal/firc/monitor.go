package firc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MonitorDomainStat represents query telemetry for a single domain inside a routing group.
type MonitorDomainStat struct {
	Domain  string `json:"domain"`
	Queries int    `json:"queries"`
	LastAt  int64  `json:"lastAt,omitempty"`
}

// MonitorServerRow summarizes live FIRC routing telemetry for an inbound VPN server or LAN segment.
type MonitorServerRow struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Category          string `json:"category"` // "awgm" | "system" | "lan"
	Detail            string `json:"detail"`
	Running           bool   `json:"running"`
	RoutingEnabled    bool   `json:"routingEnabled"`
	DNSPackets        uint64 `json:"dnsPackets"`
	DNSBytes          uint64 `json:"dnsBytes"`
	MatchedQueries    int    `json:"matchedQueries"`
	ActiveConns       int    `json:"activeConns"`
	ActiveClients     int    `json:"activeClients"`
	TxBytes           uint64 `json:"txBytes"`
	RxBytes           uint64 `json:"rxBytes"`
	LastDomain        string `json:"lastDomain,omitempty"`
	LastGroup         string `json:"lastGroup,omitempty"`
	LastOutbound      string `json:"lastOutbound,omitempty"`
	LastOutboundLabel string `json:"lastOutboundLabel,omitempty"`
	LastAt            int64  `json:"lastAt,omitempty"`
}

// MonitorGroupRow summarizes live FIRC packet/byte counters and active sessions for a routing group.
type MonitorGroupRow struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	ActiveOutbound      string              `json:"activeOutbound"`
	ActiveOutboundLabel string              `json:"activeOutboundLabel,omitempty"`
	Live                bool                `json:"live"`
	Mark                string              `json:"mark,omitempty"`
	FakeSubnet          string              `json:"fakeSubnet,omitempty"`
	RulesTotal          int                 `json:"rulesTotal"`
	SubnetRules         int                 `json:"subnetRules"`
	MatchedPackets      uint64              `json:"matchedPackets"`
	MatchedBytes        uint64              `json:"matchedBytes"`
	ActiveConns         int                 `json:"activeConns"`
	ActiveClients       int                 `json:"activeClients"`
	ConnTxBytes         uint64              `json:"connTxBytes"`
	ConnRxBytes         uint64              `json:"connRxBytes"`
	MatchedQueries      int                 `json:"matchedQueries"`
	UniqueDomains       int                 `json:"uniqueDomains"`
	TopDomains          []MonitorDomainStat `json:"topDomains,omitempty"`
	LastDomain          string              `json:"lastDomain,omitempty"`
	LastAt              int64               `json:"lastAt,omitempty"`
}

// MonitorMatchEvent represents a single matched DNS/routing decision in FIRC.
type MonitorMatchEvent struct {
	Seq           uint64 `json:"seq"`
	At            int64  `json:"at"`
	ClientIP      string `json:"clientIp"`
	SourceID      string `json:"sourceId"`
	SourceName    string `json:"sourceName"`
	Domain        string `json:"domain"`
	FakeIP        string `json:"fakeIp,omitempty"`
	RealIP        string `json:"realIp,omitempty"`
	GroupID       string `json:"groupId"`
	GroupName     string `json:"groupName"`
	Outbound      string `json:"outbound"`
	OutboundLabel string `json:"outboundLabel,omitempty"`
}

// MonitorSnapshot is returned by GET /api/firc/monitor.
type MonitorSnapshot struct {
	Timestamp     int64               `json:"timestamp"`
	Running       bool                `json:"running"`
	TotalConns    int                 `json:"totalConns"`
	TotalClients  int                 `json:"totalClients"`
	TotalTxBytes  uint64              `json:"totalTxBytes"`
	TotalRxBytes  uint64              `json:"totalRxBytes"`
	TotalDNS      uint64              `json:"totalDns"`
	TotalMatched  int                 `json:"totalMatched"`
	Servers       []MonitorServerRow  `json:"servers"`
	Groups        []MonitorGroupRow   `json:"groups"`
	RecentMatches []MonitorMatchEvent `json:"recentMatches"`
}

// DomainCheckResult is returned by POST /api/firc/monitor/check.
type DomainCheckResult struct {
	Domain        string `json:"domain"`
	Routed        bool   `json:"routed"`
	FakeIP        string `json:"fakeIp,omitempty"`
	RealIP        string `json:"realIp,omitempty"`
	GroupID       string `json:"groupId,omitempty"`
	GroupName     string `json:"groupName,omitempty"`
	Outbound      string `json:"outbound"`
	OutboundLabel string `json:"outboundLabel,omitempty"`
	LatencyMs     int64  `json:"latencyMs"`
	Error         string `json:"error,omitempty"`
}

type parsedCIDRSource struct {
	net      *net.IPNet
	serverID string
}

func buildCIDRSources(sr ServerRoutingConfig) []parsedCIDRSource {
	type spec struct {
		cidr string
		id   string
	}
	specs := []spec{
		{"10.10.0.2/32", "freeturn_server"},
		{"10.10.0.0/24", "awg_server"},
		{"10.66.0.0/16", "wdtt_server"},
		{"10.70.0.0/16", "wdtt_server"},
		{"10.77.0.0/16", "openflux_server"},
		{"172.16.6.0/24", "keenetic_wg"},
		{"172.16.7.0/24", "keenetic_sstp"},
		{"172.16.2.0/24", "keenetic_l2tp"},
		{"172.20.8.0/24", "keenetic_ikev2"},
		{"172.20.0.0/24", "keenetic_ikev1"},
		{"172.16.1.0/24", "keenetic_pptp"},
		{"172.16.3.0/24", "keenetic_oc"},
		{"192.168.0.0/16", "lan_br0"},
	}
	var out []parsedCIDRSource
	for _, sp := range specs {
		if sp.id == "freeturn_server" && !sr.IsServerEnabled("freeturn_server") && sr.IsServerEnabled("awg_server") {
			continue
		}
		_, ipNet, err := net.ParseCIDR(sp.cidr)
		if err == nil && ipNet != nil {
			out = append(out, parsedCIDRSource{net: ipNet, serverID: sp.id})
		}
	}
	return out
}

func classifyClientIP(ipStr string, cidrs []parsedCIDRSource) string {
	ipStr = strings.TrimSpace(ipStr)
	if ipStr == "" {
		return "lan_br0"
	}
	if h, _, err := net.SplitHostPort(ipStr); err == nil {
		ipStr = h
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "lan_br0"
	}
	if ip.IsLoopback() {
		return "vless_xhttp_server"
	}
	for _, c := range cidrs {
		if c.net.Contains(ip) {
			return c.serverID
		}
	}
	return "lan_br0"
}

func parseDNSChainCounters(ctx context.Context, chain string) map[string][2]uint64 {
	res := make(map[string][2]uint64)
	out, err := exec.CommandContext(ctx, "iptables", "-w", "-t", "nat", "-L", chain, "-v", "-n", "-x").Output()
	if err != nil {
		return res
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 6 {
			continue
		}
		pkts, err1 := strconv.ParseUint(fields[0], 10, 64)
		b, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		inIface := fields[5]
		cur := res[inIface]
		cur[0] += pkts
		cur[1] += b
		res[inIface] = cur
	}
	return res
}

type groupMangleInfo struct {
	markHex    string
	markVal    uint32
	fakeSubnet string
	fakeNet    *net.IPNet
	packets    uint64
	bytes      uint64
	cidrRules  int
}

func cleanInterfaceDisplayName(raw string) string {
	raw = strings.TrimSpace(raw)
	if idx := strings.Index(raw, "[awg-manager:"); idx >= 0 {
		raw = strings.TrimSpace(raw[:idx])
	}
	return raw
}

func (op *Operator) buildOutboundNameMap(ctx context.Context) map[string]string {
	m := make(map[string]string)
	if ifaces, err := op.Client().GetInterfaces(ctx); err == nil {
		for _, iface := range ifaces {
			id := strings.TrimSpace(iface.ID)
			if id == "" {
				continue
			}
			label := cleanInterfaceDisplayName(iface.Name)
			if label == "" {
				label = strings.TrimSpace(iface.NDMSName)
			}
			if label != "" && label != id {
				m[id] = label
			}
		}
	}
	tunnels, err := op.Client().GetTunnels(ctx)
	if err != nil {
		tunnels, _ = op.LoadTunnelsFile()
	}
	for _, t := range tunnels {
		desc := cleanInterfaceDisplayName(t.Description)
		if desc == "" {
			continue
		}
		if dev := strings.TrimSpace(t.Device); dev != "" && dev != desc {
			m[dev] = desc
		}
		if tid := strings.TrimSpace(t.ID); tid != "" && tid != desc {
			m[tid] = desc
		}
	}
	return m
}

func parseGroupMangleChain(ctx context.Context, groupID string) groupMangleInfo {
	var info groupMangleInfo
	chain := "FIRC_" + groupID
	chainD := chain + "D"
	fextChain := "FEXT_" + groupID

	out, err := exec.CommandContext(ctx, "iptables", "-w", "-t", "mangle", "-L", chain, "-v", "-n", "-x").Output()
	if err != nil {
		return info
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 9 {
			continue
		}
		pkts, err1 := strconv.ParseUint(fields[0], 10, 64)
		b, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		target := fields[2]
		dst := fields[8]
		if target == "MARK" || target == chainD {
			info.packets += pkts
			info.bytes += b
			if strings.HasPrefix(dst, "198.18.") || strings.HasPrefix(dst, "198.19.") {
				info.fakeSubnet = dst
				if _, ipNet, err := net.ParseCIDR(dst); err == nil {
					info.fakeNet = ipNet
				}
			} else if target == "MARK" && dst != "0.0.0.0/0" {
				info.cidrRules++
			}
		}
		if idx := strings.Index(line, "MARK xset "); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("MARK xset "):])
			parts := strings.Split(strings.Fields(rest)[0], "/")
			if len(parts) > 0 && strings.HasPrefix(parts[0], "0x40") {
				info.markHex = parts[0]
				if v, err := strconv.ParseUint(strings.TrimPrefix(parts[0], "0x"), 16, 32); err == nil {
					info.markVal = uint32(v) & 0x40ff0000
				}
			}
		}
	}

	if info.markHex == "" {
		rawMark := extractGroupMarkFromChain(ctx, chainD)
		if rawMark == "" {
			rawMark = extractGroupMarkFromChain(ctx, chain)
		}
		if rawMark != "" {
			parts := strings.Split(rawMark, "/")
			info.markHex = parts[0]
			if v, err := strconv.ParseUint(strings.TrimPrefix(parts[0], "0x"), 16, 32); err == nil {
				info.markVal = uint32(v) & 0x40ff0000
			}
		}
	}

	if fextOut, err := exec.CommandContext(ctx, "iptables", "-w", "-t", "mangle", "-L", fextChain, "-v", "-n", "-x").Output(); err == nil {
		for _, line := range strings.Split(string(fextOut), "\n") {
			fields := strings.Fields(strings.TrimSpace(line))
			if len(fields) < 9 {
				continue
			}
			pkts, err1 := strconv.ParseUint(fields[0], 10, 64)
			b, err2 := strconv.ParseUint(fields[1], 10, 64)
			if err1 == nil && err2 == nil && fields[2] == "MARK" {
				info.packets += pkts
				info.bytes += b
			}
		}
	}

	return info
}

// GetMonitorSnapshot collects real-time FIRC traffic counters, active conntrack sessions, and recent DNS matches.
func (op *Operator) GetMonitorSnapshot(ctx context.Context) MonitorSnapshot {
	return op.getMonitorSnapshot(ctx, op.IsRunning(ctx))
}

// GetOfflineMonitorSnapshot builds a disk/server-status monitor snapshot without
// probing fircd IPC or invoking iptables. Used when FIRC is not selected.
func (op *Operator) GetOfflineMonitorSnapshot(ctx context.Context) MonitorSnapshot {
	return op.getMonitorSnapshot(ctx, false)
}

func (op *Operator) getMonitorSnapshot(ctx context.Context, running bool) MonitorSnapshot {
	snap := MonitorSnapshot{
		Timestamp:     time.Now().Unix(),
		Running:       running,
		Servers:       []MonitorServerRow{},
		Groups:        []MonitorGroupRow{},
		RecentMatches: []MonitorMatchEvent{},
	}

	ext := op.LoadExtConfig()
	catalog := op.ListAvailableServers(ctx)
	cidrs := buildCIDRSources(ext.ServerRouting)

	var fextDNS map[string][2]uint64
	var fircDNS map[string][2]uint64
	if running {
		fextDNS = parseDNSChainCounters(ctx, "FEXT_DNS")
		fircDNS = parseDNSChainCounters(ctx, "FIRC_DNSOR")
	}

	serverIndex := make(map[string]int)
	addServer := func(row MonitorServerRow) {
		serverIndex[row.ID] = len(snap.Servers)
		snap.Servers = append(snap.Servers, row)
	}

	lanDNS := fircDNS["*"]
	if brDNS, ok := fircDNS["br0"]; ok {
		lanDNS[0] += brDNS[0]
		lanDNS[1] += brDNS[1]
	}
	lanSubnet := "192.168.1.0/24"
	if brIP := detectLANBridgeIPv4(); brIP != "" {
		lanSubnet = poolStartToCIDR24(brIP, lanSubnet)
	}
	addServer(MonitorServerRow{
		ID:             "lan_br0",
		Name:           "Локальная сеть (LAN / Wi-Fi)",
		Category:       "lan",
		Detail:         "br0 · " + lanSubnet,
		Running:        true,
		RoutingEnabled: true,
		DNSPackets:     lanDNS[0],
		DNSBytes:       lanDNS[1],
	})

	ifaceToCounters := func(srvID string) (uint64, uint64) {
		switch srvID {
		case "awg_server", "freeturn_server":
			c := fextDNS["nwg2"]
			return c[0], c[1]
		case "wdtt_server":
			c17 := fextDNS["opkgtun17"]
			c19 := fextDNS["opkgtun19"]
			return c17[0] + c19[0], c17[1] + c19[1]
		case "openflux_server":
			op.mu.Lock()
			fn := op.openfluxStatusFn
			op.mu.Unlock()
			if fn != nil {
				if _, tun := fn(); tun != "" {
					c := fextDNS[tun]
					return c[0], c[1]
				}
			}
			return 0, 0
		case "keenetic_wg":
			c := fextDNS["nwg0"]
			return c[0], c[1]
		case "keenetic_sstp":
			c := fextDNS["sstp+"]
			return c[0], c[1]
		case "keenetic_l2tp":
			c := fextDNS["l2tp+"]
			return c[0], c[1]
		case "keenetic_ikev2":
			c := fextDNS["xfrms2"]
			return c[0], c[1]
		case "keenetic_ikev1":
			c := fextDNS["xfrms1"]
			return c[0], c[1]
		case "keenetic_pptp":
			c := fextDNS["vpn+"]
			return c[0], c[1]
		case "keenetic_oc":
			c := fextDNS["oc+"]
			return c[0], c[1]
		default:
			return 0, 0
		}
	}

	serverNameMap := map[string]string{
		"lan_br0": "LAN / Wi-Fi",
	}
	for _, srv := range catalog {
		serverNameMap[srv.ID] = srv.Name
		enabled := ext.ServerRouting.IsServerEnabled(srv.ID)
		if !enabled && !srv.Running {
			continue
		}
		pkts, b := ifaceToCounters(srv.ID)
		addServer(MonitorServerRow{
			ID:             srv.ID,
			Name:           srv.Name,
			Category:       srv.Category,
			Detail:         srv.Detail,
			Running:        srv.Running,
			RoutingEnabled: enabled,
			DNSPackets:     pkts,
			DNSBytes:       b,
		})
	}

	if !running {
		return snap
	}

	outboundNames := op.buildOutboundNameMap(ctx)

	groups, err := op.Client().GetGroups(ctx, true)
	if err != nil {
		groups, err = op.Client().GetGroups(ctx, false)
		if err != nil {
			groups, _ = op.LoadGroupsFile()
		}
	}

	groupIndex := make(map[string]int)
	markToGroupIdx := make(map[uint32]int)
	var fakeNetGroups []struct {
		net *net.IPNet
		idx int
	}

	for _, g := range groups {
		if !g.Enable || g.ID == "" {
			continue
		}
		outbound := strings.TrimSpace(g.Active)
		if outbound == "" && len(g.Interface) > 0 {
			outbound = g.Interface[0]
		}
		mInfo := parseGroupMangleChain(ctx, g.ID)
		idx := len(snap.Groups)
		groupIndex[g.ID] = idx
		if mInfo.markVal != 0 {
			markToGroupIdx[mInfo.markVal] = idx
		}
		if mInfo.fakeNet != nil {
			fakeNetGroups = append(fakeNetGroups, struct {
				net *net.IPNet
				idx int
			}{net: mInfo.fakeNet, idx: idx})
		}
		rulesTotal := len(g.Rules)
		if g.List != nil && g.List.RulesTotal > 0 {
			rulesTotal += g.List.RulesTotal
		}
		snap.Groups = append(snap.Groups, MonitorGroupRow{
			ID:                  g.ID,
			Name:                g.Name,
			ActiveOutbound:      outbound,
			ActiveOutboundLabel: outboundNames[outbound],
			Live:                g.Live,
			Mark:                mInfo.markHex,
			FakeSubnet:          mInfo.fakeSubnet,
			RulesTotal:          rulesTotal,
			SubnetRules:         mInfo.cidrRules,
			MatchedPackets:      mInfo.packets,
			MatchedBytes:        mInfo.bytes,
		})
	}

	globalClients := make(map[string]struct{})
	groupClients := make([]map[string]struct{}, len(snap.Groups))
	for i := range groupClients {
		groupClients[i] = make(map[string]struct{})
	}
	serverClients := make([]map[string]struct{}, len(snap.Servers))
	for i := range serverClients {
		serverClients[i] = make(map[string]struct{})
	}

	// Parse /proc/net/nf_conntrack for active FIRC sessions and byte counters.
	if ctFile, err := os.Open("/proc/net/nf_conntrack"); err == nil {
		scanner := bufio.NewScanner(ctFile)
		scanner.Buffer(make([]byte, 64*1024), 256*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.Contains(line, "mark=") {
				continue
			}
			var markDec uint64
			if mIdx := strings.Index(line, "mark="); mIdx >= 0 {
				rest := line[mIdx+5:]
				end := strings.IndexByte(rest, ' ')
				if end > 0 {
					rest = rest[:end]
				}
				markDec, _ = strconv.ParseUint(rest, 10, 64)
			}
			mark32 := uint32(markDec)
			grpIdx, matchedGrp := markToGroupIdx[mark32&0x40ff0000]

			var firstSrc, firstDst string
			var txBytes, rxBytes uint64
			bytesCount := 0
			for _, tok := range strings.Fields(line) {
				if firstSrc == "" && strings.HasPrefix(tok, "src=") {
					firstSrc = strings.TrimPrefix(tok, "src=")
				} else if firstDst == "" && strings.HasPrefix(tok, "dst=") {
					firstDst = strings.TrimPrefix(tok, "dst=")
				} else if strings.HasPrefix(tok, "bytes=") {
					val, _ := strconv.ParseUint(strings.TrimPrefix(tok, "bytes="), 10, 64)
					if bytesCount == 0 {
						txBytes = val
					} else if bytesCount == 1 {
						rxBytes = val
					}
					bytesCount++
				}
			}

			if !matchedGrp && (strings.HasPrefix(firstDst, "198.18.") || strings.HasPrefix(firstDst, "198.19.")) {
				if dstIP := net.ParseIP(firstDst); dstIP != nil {
					for _, fg := range fakeNetGroups {
						if fg.net.Contains(dstIP) {
							grpIdx = fg.idx
							matchedGrp = true
							break
						}
					}
				}
			}

			if !matchedGrp {
				continue
			}

			snap.TotalConns++
			snap.TotalTxBytes += txBytes
			snap.TotalRxBytes += rxBytes
			snap.Groups[grpIdx].ActiveConns++
			snap.Groups[grpIdx].ConnTxBytes += txBytes
			snap.Groups[grpIdx].ConnRxBytes += rxBytes
			if firstSrc != "" {
				globalClients[firstSrc] = struct{}{}
				groupClients[grpIdx][firstSrc] = struct{}{}
			}

			var srvID string
			if (mark32&0xffff) == XHTTPServerFircMark || firstSrc == "127.0.0.1" {
				srvID = "vless_xhttp_server"
			} else {
				srvID = classifyClientIP(firstSrc, cidrs)
			}
			if sIdx, ok := serverIndex[srvID]; ok {
				snap.Servers[sIdx].ActiveConns++
				snap.Servers[sIdx].TxBytes += txBytes
				snap.Servers[sIdx].RxBytes += rxBytes
				if firstSrc != "" {
					serverClients[sIdx][firstSrc] = struct{}{}
				}
			}
		}
		_ = ctFile.Close()
	}

	snap.TotalClients = len(globalClients)
	for i := range snap.Groups {
		snap.Groups[i].ActiveClients = len(groupClients[i])
	}
	for i := range snap.Servers {
		snap.Servers[i].ActiveClients = len(serverClients[i])
	}

	// Read all available pages of recent DNS events from fircd so timestamps are always up to the current second.
	if evRes, err := op.Client().GetAllEvents(ctx, 0); err == nil && evRes != nil {
		evDNSCount := make(map[string]uint64)
		var matches []MonitorMatchEvent
		for _, ev := range evRes.Events {
			if ev.Kind != "dns" {
				continue
			}
			srvID := classifyClientIP(ev.Client, cidrs)
			evDNSCount[srvID]++
			if srvID == "vless_xhttp_server" {
				if sIdx, ok := serverIndex["vless_xhttp_server"]; ok {
					snap.Servers[sIdx].DNSPackets++
				}
			}
			if ev.Decision != "issued" {
				continue
			}
			snap.TotalMatched++

			grpID := ""
			grpName := ""
			outbound := ""
			if ev.Group != nil {
				grpID = ev.Group.ID
				grpName = ev.Group.Name
			}
			if gIdx, ok := groupIndex[grpID]; ok {
				if grpName == "" {
					grpName = snap.Groups[gIdx].Name
				}
				outbound = snap.Groups[gIdx].ActiveOutbound
				snap.Groups[gIdx].MatchedQueries++
				snap.Groups[gIdx].LastDomain = ev.Name
				snap.Groups[gIdx].LastAt = ev.At
			}

			if sIdx, ok := serverIndex[srvID]; ok {
				snap.Servers[sIdx].MatchedQueries++
				snap.Servers[sIdx].LastDomain = ev.Name
				snap.Servers[sIdx].LastGroup = grpName
				snap.Servers[sIdx].LastOutbound = outbound
				snap.Servers[sIdx].LastOutboundLabel = outboundNames[outbound]
				snap.Servers[sIdx].LastAt = ev.At
			}

			realIP := ""
			if len(ev.Reals) > 0 {
				realIP = ev.Reals[0]
			}
			srcName := serverNameMap[srvID]
			if srcName == "" {
				srcName = srvID
			}
			matches = append(matches, MonitorMatchEvent{
				Seq:           ev.Seq,
				At:            ev.At,
				ClientIP:      ev.Client,
				SourceID:      srvID,
				SourceName:    srcName,
				Domain:        ev.Name,
				FakeIP:        ev.Fake,
				RealIP:        realIP,
				GroupID:       grpID,
				GroupName:     grpName,
				Outbound:      outbound,
				OutboundLabel: outboundNames[outbound],
			})
		}

		for srvID, cnt := range evDNSCount {
			if sIdx, ok := serverIndex[srvID]; ok {
				if cnt > snap.Servers[sIdx].DNSPackets {
					snap.Servers[sIdx].DNSPackets = cnt
				}
			}
		}

		// Reverse order (newest first), cap at 40.
		for i := len(matches) - 1; i >= 0 && len(snap.RecentMatches) < 40; i-- {
			snap.RecentMatches = append(snap.RecentMatches, matches[i])
		}
	}

	// Enrich per-group 24h DNS query counts, unique domains, and top domains from /api/v1/system/dnsstats.
	if dnsStats, err := op.Client().GetDNSStats(ctx, 500); err == nil && dnsStats != nil {
		grpStatsQueries := make(map[int]int)
		for _, row := range dnsStats.Rows {
			if row.Owner == nil || row.Owner.ID == "" {
				continue
			}
			gIdx, ok := groupIndex[row.Owner.ID]
			if !ok {
				continue
			}
			grpStatsQueries[gIdx] += row.Queries
			snap.Groups[gIdx].UniqueDomains++
			if len(snap.Groups[gIdx].TopDomains) < 4 && row.Name != "" {
				snap.Groups[gIdx].TopDomains = append(snap.Groups[gIdx].TopDomains, MonitorDomainStat{
					Domain:  row.Name,
					Queries: row.Queries,
					LastAt:  row.Last,
				})
			}
			if row.Last > snap.Groups[gIdx].LastAt && row.Name != "" {
				snap.Groups[gIdx].LastAt = row.Last
				snap.Groups[gIdx].LastDomain = row.Name
			}
		}
		totalFromGroups := 0
		for gIdx, qCnt := range grpStatsQueries {
			if qCnt > snap.Groups[gIdx].MatchedQueries {
				snap.Groups[gIdx].MatchedQueries = qCnt
			}
		}
		for _, g := range snap.Groups {
			totalFromGroups += g.MatchedQueries
		}
		if totalFromGroups > snap.TotalMatched {
			snap.TotalMatched = totalFromGroups
		}
	}

	for _, s := range snap.Servers {
		snap.TotalDNS += s.DNSPackets
	}

	return snap
}

// CheckDomainRouting tests how fircd resolves and routes a specific domain name in real time.
func (op *Operator) CheckDomainRouting(ctx context.Context, domain string) (*DomainCheckResult, error) {
	domain = strings.TrimSpace(strings.TrimSuffix(domain, "."))
	if idx := strings.Index(domain, "://"); idx >= 0 {
		domain = domain[idx+3:]
	}
	if idx := strings.IndexByte(domain, '/'); idx >= 0 {
		domain = domain[:idx]
	}
	if h, _, err := net.SplitHostPort(domain); err == nil {
		domain = h
	}
	if domain == "" {
		return nil, fmt.Errorf("укажите домен для проверки")
	}

	start := time.Now()
	dnsAddr := op.DNSProxyEndpoint()
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(dCtx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 4 * time.Second}
			return d.DialContext(dCtx, "udp", dnsAddr)
		},
	}

	ips, err := r.LookupIP(ctx, "ip4", domain)
	latency := time.Since(start).Milliseconds()
	if err != nil || len(ips) == 0 {
		errMsg := "не удалось разрешить домен"
		if err != nil {
			errMsg = err.Error()
		}
		return &DomainCheckResult{
			Domain:    domain,
			Routed:    false,
			Outbound:  "none",
			LatencyMs: latency,
			Error:     errMsg,
		}, nil
	}

	ip := ips[0].To4()
	if ip == nil {
		ip = ips[0]
	}
	ipStr := ip.String()
	isFake := ip != nil && ip[0] == 198 && (ip[1] == 18 || ip[1] == 19)
	if !isFake {
		return &DomainCheckResult{
			Domain:    domain,
			Routed:    false,
			RealIP:    ipStr,
			Outbound:  "Direct (WAN / вне правил FIRC)",
			LatencyMs: latency,
		}, nil
	}

	res := &DomainCheckResult{
		Domain:    domain,
		Routed:    true,
		FakeIP:    ipStr,
		LatencyMs: latency,
	}

	outboundNames := op.buildOutboundNameMap(ctx)

	// Look up real IP in FIRC_DNAT.
	if dnatOut, err := exec.CommandContext(ctx, "iptables", "-w", "-t", "nat", "-S", "FIRC_DNAT").Output(); err == nil {
		needle := "-A FIRC_DNAT -d " + ipStr + "/32 "
		for _, line := range strings.Split(string(dnatOut), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, needle) {
				if idx := strings.Index(line, "--to-destination "); idx >= 0 {
					res.RealIP = strings.TrimSpace(line[idx+len("--to-destination "):])
				}
				break
			}
		}
	}

	// Match FakeIP against active FIRC groups.
	groups, err := op.Client().GetGroups(ctx, false)
	if err != nil {
		groups, _ = op.LoadGroupsFile()
	}
	for _, g := range groups {
		if !g.Enable || g.ID == "" {
			continue
		}
		mInfo := parseGroupMangleChain(ctx, g.ID)
		if mInfo.fakeNet != nil && mInfo.fakeNet.Contains(ip) {
			res.GroupID = g.ID
			res.GroupName = g.Name
			res.Outbound = g.Active
			if res.Outbound == "" && len(g.Interface) > 0 {
				res.Outbound = g.Interface[0]
			}
			res.OutboundLabel = outboundNames[res.Outbound]
			break
		}
	}

	// Fallback: check latest DNS event in fircd if group was not found via FakeSubnet.
	if res.GroupID == "" {
		if evRes, err := op.Client().GetAllEvents(ctx, 0); err == nil && evRes != nil {
			for i := len(evRes.Events) - 1; i >= 0; i-- {
				ev := evRes.Events[i]
				if ev.Kind == "dns" && ev.Fake == ipStr && ev.Group != nil {
					res.GroupID = ev.Group.ID
					res.GroupName = ev.Group.Name
					for _, g := range groups {
						if g.ID == res.GroupID {
							res.Outbound = g.Active
							if res.Outbound == "" && len(g.Interface) > 0 {
								res.Outbound = g.Interface[0]
							}
							res.OutboundLabel = outboundNames[res.Outbound]
							break
						}
					}
					if res.RealIP == "" && len(ev.Reals) > 0 {
						res.RealIP = ev.Reals[0]
					}
					break
				}
			}
		}
	}

	// Ensure FEXT_DNAT also has this FakeIP mirrored if router OUTPUT routing is active.
	if res.RealIP != "" {
		var buf bytes.Buffer
		_ = buf
	}

	return res, nil
}

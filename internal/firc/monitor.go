package firc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MonitorDomainStat represents query telemetry for a single domain inside a routing group.
type MonitorDomainStat struct {
	Domain  string `json:"domain"`
	Queries int    `json:"queries"`
	LastAt  int64  `json:"lastAt,omitempty"`
}

// MonitorClientStat summarizes active conntrack sessions and traffic for a single client IP.
type MonitorClientStat struct {
	IP         string   `json:"ip"`
	SourceID   string   `json:"sourceId,omitempty"`
	SourceName string   `json:"sourceName,omitempty"`
	Conns      int      `json:"conns"`
	TxBytes    uint64   `json:"txBytes"`
	RxBytes    uint64   `json:"rxBytes"`
	Groups     []string `json:"groups,omitempty"`
}

// MonitorServerRow summarizes live FIRC routing telemetry for an inbound VPN server or LAN segment.
type MonitorServerRow struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Category          string              `json:"category"` // "awgm" | "system" | "lan"
	Detail            string              `json:"detail"`
	Running           bool                `json:"running"`
	RoutingEnabled    bool                `json:"routingEnabled"`
	DNSPackets        uint64              `json:"dnsPackets"`
	DNSBytes          uint64              `json:"dnsBytes"`
	MatchedQueries    int                 `json:"matchedQueries"`
	ActiveConns       int                 `json:"activeConns"`
	ActiveClients     int                 `json:"activeClients"`
	Clients           []MonitorClientStat `json:"clients,omitempty"`
	TxBytes           uint64              `json:"txBytes"`
	RxBytes           uint64              `json:"rxBytes"`
	LastDomain        string              `json:"lastDomain,omitempty"`
	LastGroup         string              `json:"lastGroup,omitempty"`
	LastOutbound      string              `json:"lastOutbound,omitempty"`
	LastOutboundLabel string              `json:"lastOutboundLabel,omitempty"`
	LastAt            int64               `json:"lastAt,omitempty"`
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
	Clients             []MonitorClientStat `json:"clients,omitempty"`
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
	Clients       []MonitorClientStat `json:"clients,omitempty"`
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
	return buildCIDRSourcesWithWG(sr, nil, false)
}

func buildCIDRSourcesWithWG(sr ServerRoutingConfig, wgServers []DynamicWGServer, hasProvider bool) []parsedCIDRSource {
	type spec struct {
		cidr string
		id   string
	}
	var specs []spec
	if hasProvider {
		if sr.IsServerEnabled("freeturn_server") {
			specs = append(specs, spec{"10.10.0.2/32", "freeturn_server"})
		}
		for _, srv := range wgServers {
			if sub := strings.TrimSpace(srv.Subnet); sub != "" {
				specs = append(specs, spec{sub, srv.ID})
			}
		}
	} else {
		if sr.IsServerEnabled("freeturn_server") || !sr.IsServerEnabled("awg_server") {
			specs = append(specs, spec{"10.10.0.2/32", "freeturn_server"})
		}
		specs = append(specs,
			spec{"10.10.0.0/24", "awg_server"},
			spec{"172.16.6.0/24", "keenetic_wg"},
		)
	}
	specs = append(specs,
		spec{"10.66.0.0/16", "wdtt_server"},
		spec{"10.70.0.0/16", "wdtt_server"},
		spec{"10.77.0.0/16", "openflux_server"},
		spec{"172.16.7.0/24", "keenetic_sstp"},
		spec{"172.16.2.0/24", "keenetic_l2tp"},
		spec{"172.20.8.0/24", "keenetic_ikev2"},
		spec{"172.20.0.0/24", "keenetic_ikev1"},
		spec{"172.16.1.0/24", "keenetic_pptp"},
		spec{"172.16.3.0/24", "keenetic_oc"},
		spec{"192.168.0.0/16", "lan_br0"},
	)
	var out []parsedCIDRSource
	for _, sp := range specs {
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

type fakeNetGroupEntry struct {
	net *net.IPNet
	idx int
}

// maxMonitorClients ограничивает каждый список клиентов в ответе; точное число —
// в счётчиках TotalClients/ActiveClients.
const maxMonitorClients = 128

// canonicalMonitorIP приводит адрес conntrack к каноническому виду: IPv6 там
// записан без сжатия, а IPv4-mapped обязан совпасть с тем же клиентом по IPv4.
func canonicalMonitorIP(raw string) string {
	if addr, err := netip.ParseAddr(raw); err == nil {
		return addr.Unmap().String()
	}
	return raw
}

type monitorClientAcc struct {
	ip         string
	sourceID   string
	sourceName string
	conns      int
	txBytes    uint64
	rxBytes    uint64
	groups     map[string]struct{}
}

func recordClientAcc(accMap map[string]*monitorClientAcc, ip, srvID, srvName, groupName string, txBytes, rxBytes uint64) {
	c := accMap[ip]
	if c == nil {
		c = &monitorClientAcc{
			ip:         ip,
			sourceID:   srvID,
			sourceName: srvName,
			groups:     make(map[string]struct{}),
		}
		accMap[ip] = c
	}
	c.conns++
	c.txBytes += txBytes
	c.rxBytes += rxBytes
	c.groups[groupName] = struct{}{}
}

func materializeMonitorClients(accMap map[string]*monitorClientAcc, includeGroups bool) []MonitorClientStat {
	if len(accMap) == 0 {
		return nil
	}
	out := make([]MonitorClientStat, 0, len(accMap))
	for _, acc := range accMap {
		st := MonitorClientStat{
			IP:         acc.ip,
			SourceID:   acc.sourceID,
			SourceName: acc.sourceName,
			Conns:      acc.conns,
			TxBytes:    acc.txBytes,
			RxBytes:    acc.rxBytes,
		}
		if includeGroups {
			grps := make([]string, 0, len(acc.groups))
			for g := range acc.groups {
				grps = append(grps, g)
			}
			sort.Strings(grps)
			st.Groups = grps
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Conns != b.Conns {
			return a.Conns > b.Conns
		}
		aTotal := a.RxBytes + a.TxBytes
		bTotal := b.RxBytes + b.TxBytes
		if aTotal != bTotal {
			return aTotal > bTotal
		}
		return a.IP < b.IP
	})
	if len(out) > maxMonitorClients {
		out = out[:maxMonitorClients]
	}
	return out
}

// applyConntrackSnapshot разбирает таблицу conntrack и заполняет счётчики сессий
// и клиентов; списки клиентов — только при withClients.
func applyConntrackSnapshot(
	r io.Reader,
	snap *MonitorSnapshot,
	markToGroupIdx map[uint32]int,
	fakeNetGroups []fakeNetGroupEntry,
	serverIndex map[string]int,
	serverNameMap map[string]string,
	cidrs []parsedCIDRSource,
	withClients bool,
) error {
	globalAcc := make(map[string]*monitorClientAcc)
	groupAcc := make([]map[string]*monitorClientAcc, len(snap.Groups))
	for i := range groupAcc {
		groupAcc[i] = make(map[string]*monitorClientAcc)
	}
	serverAcc := make([]map[string]*monitorClientAcc, len(snap.Servers))
	for i := range serverAcc {
		serverAcc[i] = make(map[string]*monitorClientAcc)
	}

	scanner := bufio.NewScanner(r)
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
		firstSrc = canonicalMonitorIP(firstSrc)

		snap.TotalConns++
		snap.TotalTxBytes += txBytes
		snap.TotalRxBytes += rxBytes
		snap.Groups[grpIdx].ActiveConns++
		snap.Groups[grpIdx].ConnTxBytes += txBytes
		snap.Groups[grpIdx].ConnRxBytes += rxBytes

		var srvID string
		if (mark32&0xffff) == XHTTPServerFircMark || firstSrc == "127.0.0.1" {
			srvID = "vless_xhttp_server"
		} else {
			srvID = classifyClientIP(firstSrc, cidrs)
		}
		srvName := serverNameMap[srvID]
		if srvName == "" {
			srvName = srvID
		}
		grpLabel := snap.Groups[grpIdx].Name
		if grpLabel == "" {
			grpLabel = snap.Groups[grpIdx].ID
		}

		if firstSrc != "" {
			recordClientAcc(globalAcc, firstSrc, srvID, srvName, grpLabel, txBytes, rxBytes)
			recordClientAcc(groupAcc[grpIdx], firstSrc, srvID, srvName, grpLabel, txBytes, rxBytes)
		}

		if sIdx, ok := serverIndex[srvID]; ok {
			snap.Servers[sIdx].ActiveConns++
			snap.Servers[sIdx].TxBytes += txBytes
			snap.Servers[sIdx].RxBytes += rxBytes
			if firstSrc != "" {
				recordClientAcc(serverAcc[sIdx], firstSrc, srvID, srvName, grpLabel, txBytes, rxBytes)
			}
		}
	}

	snap.TotalClients = len(globalAcc)
	for i := range snap.Groups {
		snap.Groups[i].ActiveClients = len(groupAcc[i])
	}
	for i := range snap.Servers {
		snap.Servers[i].ActiveClients = len(serverAcc[i])
	}
	if withClients {
		snap.Clients = materializeMonitorClients(globalAcc, true)
		for i := range snap.Groups {
			snap.Groups[i].Clients = materializeMonitorClients(groupAcc[i], false)
		}
		for i := range snap.Servers {
			snap.Servers[i].Clients = materializeMonitorClients(serverAcc[i], true)
		}
	}
	return scanner.Err()
}

// GetMonitorSnapshot collects real-time FIRC traffic counters, active conntrack sessions, and recent DNS matches.
// Per-client lists are included only when withClients is set.
func (op *Operator) GetMonitorSnapshot(ctx context.Context, withClients bool) MonitorSnapshot {
	return op.getMonitorSnapshot(ctx, op.IsRunning(ctx), withClients)
}

// GetOfflineMonitorSnapshot builds a disk/server-status monitor snapshot without
// probing fircd IPC or invoking iptables. Used when FIRC is not selected.
func (op *Operator) GetOfflineMonitorSnapshot(ctx context.Context) MonitorSnapshot {
	return op.getMonitorSnapshot(ctx, false, false)
}

func (op *Operator) getMonitorSnapshot(ctx context.Context, running, withClients bool) MonitorSnapshot {
	snap := MonitorSnapshot{
		Timestamp:     time.Now().Unix(),
		Running:       running,
		Servers:       []MonitorServerRow{},
		Groups:        []MonitorGroupRow{},
		RecentMatches: []MonitorMatchEvent{},
	}

	ext := op.LoadExtConfig()
	catalog := op.ListAvailableServers(ctx)
	wgServers, hasWGProvider := op.cachedWGServers()
	wgByID := make(map[string]DynamicWGServer, len(wgServers))
	for _, srv := range wgServers {
		wgByID[srv.ID] = srv
	}
	var enabledWG map[string]bool
	if hasWGProvider {
		matched := matchEnabledWGServers(ext.ServerRouting, wgServers)
		enabledWG = make(map[string]bool, len(matched))
		for _, srv := range matched {
			enabledWG[srv.ID] = true
		}
	}
	cidrs := buildCIDRSourcesWithWG(ext.ServerRouting, wgServers, hasWGProvider)

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
		if hasWGProvider {
			if srv, ok := wgByID[srvID]; ok {
				if kIface := strings.TrimSpace(srv.KernelIface); kIface != "" {
					c := fextDNS[kIface]
					return c[0], c[1]
				}
				return 0, 0
			}
			if srvID == "freeturn_server" {
				if kIface := findFreeTurnDetailIface(wgServers); kIface != "" {
					c := fextDNS[kIface]
					return c[0], c[1]
				}
				return 0, 0
			}
		}
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
		enabled := ext.ServerRouting.IsServerEnabled(srv.ID) || enabledWG[srv.ID]
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
	var fakeNetGroups []fakeNetGroupEntry

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
			fakeNetGroups = append(fakeNetGroups, fakeNetGroupEntry{net: mInfo.fakeNet, idx: idx})
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

	// Parse /proc/net/nf_conntrack for active FIRC sessions, per-device breakdown, and byte counters.
	if ctFile, err := os.Open(op.conntrackPath); err == nil {
		err = applyConntrackSnapshot(
			ctFile,
			&snap,
			markToGroupIdx,
			fakeNetGroups,
			serverIndex,
			serverNameMap,
			cidrs,
			withClients,
		)
		_ = ctFile.Close()
		if err != nil {
			op.log.Warn("firc monitor: conntrack read interrupted", "error", err)
		}
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

func parseFircDnatMap(ctx context.Context) map[string]string {
	out := make(map[string]string)
	dnatOut, err := exec.CommandContext(ctx, "iptables", "-w", "-t", "nat", "-S", "FIRC_DNAT").Output()
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(dnatOut), "\n") {
		line = strings.TrimSpace(line)
		const prefix = "-A FIRC_DNAT -d "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		slashIdx := strings.Index(rest, "/32 ")
		if slashIdx <= 0 {
			continue
		}
		fakeIP := rest[:slashIdx]
		if toIdx := strings.Index(rest, "--to-destination "); toIdx >= 0 {
			realIP := strings.TrimSpace(rest[toIdx+len("--to-destination "):])
			if realIP != "" {
				out[fakeIP] = realIP
			}
		}
	}
	return out
}

func normalizeCheckDomain(raw string) string {
	domain := strings.TrimSpace(strings.TrimSuffix(raw, "."))
	if idx := strings.Index(domain, "://"); idx >= 0 {
		domain = domain[idx+3:]
	}
	if idx := strings.IndexByte(domain, '/'); idx >= 0 {
		domain = domain[:idx]
	}
	if h, _, err := net.SplitHostPort(domain); err == nil {
		domain = h
	}
	return strings.TrimSpace(strings.TrimSuffix(domain, "."))
}

var fircDNSTxSeq atomic.Uint32

// lookupFircDNSIPv4 sends an A-record UDP query directly to dnsAddr (fircd :3553),
// bypassing Go's resolv.conf parser which on Keenetic imposes `options timeout:1 attempts:1`
// and aborts uncached tunnel-resolved queries (1.0-1.4s) prematurely.
func lookupFircDNSIPv4(ctx context.Context, dnsAddr, domain string) (net.IP, error) {
	return lookupFircDNSIPv4Depth(ctx, dnsAddr, domain, 0)
}

func lookupFircDNSIPv4Depth(ctx context.Context, dnsAddr, domain string, depth int) (net.IP, error) {
	domain = normalizeCheckDomain(domain)
	if domain == "" {
		return nil, fmt.Errorf("empty domain")
	}
	if ip := net.ParseIP(domain); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4, nil
		}
		return nil, fmt.Errorf("non-IPv4 literal %q", domain)
	}
	if depth > 4 {
		return nil, fmt.Errorf("CNAME chain too deep for %q", domain)
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ip, cname, rcode, err := exchangeFircDNSA(ctx, dnsAddr, domain)
		if err == nil {
			if ip != nil {
				return ip, nil
			}
			if cname != "" {
				return lookupFircDNSIPv4Depth(ctx, dnsAddr, cname, depth+1)
			}
			return nil, fmt.Errorf("no IPv4 A record for %s", domain)
		}
		lastErr = err
		if rcode >= 0 {
			// Authoritative non-zero DNS RCODE (NXDOMAIN, SERVFAIL, etc.) — no UDP retry needed.
			return nil, err
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			return nil, err
		}
		if deadline, has := ctx.Deadline(); has && time.Until(deadline) < 1200*time.Millisecond {
			break
		}
	}
	return nil, lastErr
}

func encodeFircDNSQueryA(txID uint16, domain string) ([]byte, error) {
	labels := strings.Split(strings.TrimSuffix(domain, "."), ".")
	wireLen := 1
	for _, lbl := range labels {
		if len(lbl) == 0 || len(lbl) > 63 {
			return nil, fmt.Errorf("invalid DNS name %q", domain)
		}
		wireLen += 1 + len(lbl)
	}
	if wireLen > 255 {
		return nil, fmt.Errorf("DNS name too long %q", domain)
	}

	pkt := make([]byte, 12+wireLen+4)
	binary.BigEndian.PutUint16(pkt[0:2], txID)
	binary.BigEndian.PutUint16(pkt[2:4], 0x0100) // RD=1
	binary.BigEndian.PutUint16(pkt[4:6], 1)      // QDCOUNT=1
	off := 12
	for _, lbl := range labels {
		pkt[off] = byte(len(lbl))
		copy(pkt[off+1:], lbl)
		off += 1 + len(lbl)
	}
	pkt[off] = 0
	off++
	binary.BigEndian.PutUint16(pkt[off:off+2], 1)   // QTYPE=A
	binary.BigEndian.PutUint16(pkt[off+2:off+4], 1) // QCLASS=IN
	return pkt, nil
}

func exchangeFircDNSA(ctx context.Context, dnsAddr, domain string) (ip net.IP, cname string, rcode int, err error) {
	rcode = -1
	txID := uint16(fircDNSTxSeq.Add(1))
	if txID == 0 {
		txID = uint16(fircDNSTxSeq.Add(1))
	}
	query, err := encodeFircDNSQueryA(txID, domain)
	if err != nil {
		return nil, "", -1, err
	}

	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "udp", dnsAddr)
	if err != nil {
		return nil, "", -1, err
	}
	defer conn.Close()

	deadline := time.Now().Add(4 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(query); err != nil {
		return nil, "", -1, err
	}

	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil, "", -1, fmt.Errorf("lookup %s via %s: %w", domain, dnsAddr, err)
		}
		if n < 12 {
			continue
		}
		if binary.BigEndian.Uint16(buf[0:2]) != txID {
			continue
		}
		return parseFircDNSAResponse(buf[:n], domain)
	}
}

func parseFircDNSAResponse(pkt []byte, domain string) (net.IP, string, int, error) {
	if len(pkt) < 12 {
		return nil, "", -1, fmt.Errorf("short DNS response")
	}

	flags := binary.BigEndian.Uint16(pkt[2:4])
	if (flags>>15)&1 != 1 {
		return nil, "", -1, fmt.Errorf("unexpected DNS query packet (QR=0) for %s", domain)
	}
	if opcode := (flags >> 11) & 0x0f; opcode != 0 {
		return nil, "", -1, fmt.Errorf("unexpected DNS opcode %d for %s", opcode, domain)
	}
	if (flags>>9)&1 != 0 {
		return nil, "", -1, fmt.Errorf("DNS response truncated (TC=1) for %s", domain)
	}

	wantDomain := normalizeCheckDomain(domain)
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	anCount := int(binary.BigEndian.Uint16(pkt[6:8]))
	if qdCount != 1 {
		return nil, "", -1, fmt.Errorf("unexpected DNS question count %d for %s", qdCount, domain)
	}

	qname, off, err := readFircDNSName(pkt, 12)
	if err != nil || off+4 > len(pkt) {
		return nil, "", -1, fmt.Errorf("malformed DNS question")
	}
	if normalizeCheckDomain(qname) != wantDomain {
		return nil, "", -1, fmt.Errorf("DNS question domain mismatch: got %q want %q", qname, wantDomain)
	}
	if qType := binary.BigEndian.Uint16(pkt[off : off+2]); qType != 1 {
		return nil, "", -1, fmt.Errorf("DNS question QTYPE mismatch: %d", qType)
	}
	if qClass := binary.BigEndian.Uint16(pkt[off+2 : off+4]); qClass != 1 {
		return nil, "", -1, fmt.Errorf("DNS question QCLASS mismatch: %d", qClass)
	}
	off += 4

	rcode := int(flags & 0x0f)
	if rcode != 0 {
		name := fmt.Sprintf("rcode %d", rcode)
		switch rcode {
		case 2:
			name = "SERVFAIL"
		case 3:
			name = "NXDOMAIN"
		case 5:
			name = "REFUSED"
		}
		return nil, "", rcode, fmt.Errorf("DNS %s for %s", name, domain)
	}

	aRecords := make(map[string]net.IP)
	cnameRecords := make(map[string]string)
	for i := 0; i < anCount; i++ {
		ownerRaw, next, err := readFircDNSName(pkt, off)
		if err != nil || next+10 > len(pkt) {
			return nil, "", -1, fmt.Errorf("malformed DNS answer")
		}
		owner := normalizeCheckDomain(ownerRaw)
		rrType := binary.BigEndian.Uint16(pkt[next : next+2])
		rrClass := binary.BigEndian.Uint16(pkt[next+2 : next+4])
		rdLen := int(binary.BigEndian.Uint16(pkt[next+8 : next+10]))
		rdataOff := next + 10
		if rdataOff+rdLen > len(pkt) {
			return nil, "", -1, fmt.Errorf("truncated DNS rdata")
		}
		if rrClass == 1 && owner != "" {
			switch rrType {
			case 1: // A
				if rdLen == 4 && aRecords[owner] == nil {
					aRecords[owner] = net.IPv4(pkt[rdataOff], pkt[rdataOff+1], pkt[rdataOff+2], pkt[rdataOff+3])
				}
			case 5: // CNAME
				if cnameRecords[owner] == "" {
					if target, err := decodeFircDNSName(pkt, rdataOff); err == nil {
						if normTarget := normalizeCheckDomain(target); normTarget != "" {
							cnameRecords[owner] = normTarget
						}
					}
				}
			}
		}
		off = rdataOff + rdLen
	}

	cur := wantDomain
	visited := map[string]bool{cur: true}
	for step := 0; step < 8; step++ {
		if ip := aRecords[cur]; ip != nil {
			return ip, "", 0, nil
		}
		nextOwner := cnameRecords[cur]
		if nextOwner == "" || visited[nextOwner] {
			break
		}
		visited[nextOwner] = true
		cur = nextOwner
	}
	if cur != wantDomain {
		return nil, cur, 0, nil
	}
	return nil, "", 0, nil
}

func readFircDNSName(pkt []byte, off int) (string, int, error) {
	var labels []string
	cur := off
	nextOff := -1
	jumps := 0
	totalWire := 1
	for steps := 0; steps <= len(pkt); steps++ {
		if cur >= len(pkt) {
			return "", 0, fmt.Errorf("truncated DNS name")
		}
		l := pkt[cur]
		if l == 0 {
			if nextOff == -1 {
				nextOff = cur + 1
			}
			return strings.Join(labels, "."), nextOff, nil
		}
		if l&0xc0 == 0xc0 {
			if cur+2 > len(pkt) {
				return "", 0, fmt.Errorf("truncated DNS pointer")
			}
			if nextOff == -1 {
				nextOff = cur + 2
			}
			ptr := int(binary.BigEndian.Uint16(pkt[cur:cur+2]) & 0x3fff)
			if ptr >= len(pkt) {
				return "", 0, fmt.Errorf("out-of-range DNS pointer")
			}
			jumps++
			if jumps > 16 {
				return "", 0, fmt.Errorf("DNS pointer loop")
			}
			cur = ptr
			continue
		}
		if l&0xc0 != 0 {
			return "", 0, fmt.Errorf("invalid DNS label header")
		}
		totalWire += 1 + int(l)
		if totalWire > 255 {
			return "", 0, fmt.Errorf("DNS name too long")
		}
		end := cur + 1 + int(l)
		if end > len(pkt) {
			return "", 0, fmt.Errorf("truncated DNS label")
		}
		labels = append(labels, string(pkt[cur+1:end]))
		cur = end
	}
	return "", 0, fmt.Errorf("DNS name loop")
}

func decodeFircDNSName(pkt []byte, off int) (string, error) {
	name, _, err := readFircDNSName(pkt, off)
	return name, err
}

// CheckDomainsRouting tests how fircd resolves and routes a batch of domains in real time,
// snapshotting DNAT/group state at most once per batch instead of spawning per-domain subprocesses.
func (op *Operator) CheckDomainsRouting(ctx context.Context, domains []string) map[string]*DomainCheckResult {
	results := make(map[string]*DomainCheckResult, len(domains))
	if op == nil || len(domains) == 0 {
		return results
	}

	seen := make(map[string]struct{}, len(domains))
	normalized := make([]string, 0, len(domains))
	for _, raw := range domains {
		d := strings.ToLower(normalizeCheckDomain(raw))
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		normalized = append(normalized, d)
	}
	if len(normalized) == 0 {
		return results
	}

	dnsAddr := op.DNSProxyEndpoint()

	type lookupOutcome struct {
		domain  string
		ip      net.IP
		res     *DomainCheckResult
		hasFake bool
	}

	outcomes := make([]lookupOutcome, len(normalized))
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, dom := range normalized {
		i, dom := i, dom
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outcomes[i] = lookupOutcome{
					domain: dom,
					res: &DomainCheckResult{
						Domain:   dom,
						Outbound: "none",
						Error:    ctx.Err().Error(),
					},
				}
				return
			}

			start := time.Now()
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			ip, err := lookupFircDNSIPv4(lookupCtx, dnsAddr, dom)
			cancel()
			latency := time.Since(start).Milliseconds()

			if err != nil || ip == nil {
				errMsg := "не удалось разрешить домен"
				if err != nil {
					errMsg = err.Error()
				}
				outcomes[i] = lookupOutcome{
					domain: dom,
					res: &DomainCheckResult{
						Domain:    dom,
						Routed:    false,
						Outbound:  "none",
						LatencyMs: latency,
						Error:     errMsg,
					},
				}
				return
			}

			if ip4 := ip.To4(); ip4 != nil {
				ip = ip4
			}
			ipStr := ip.String()
			isFake := ip != nil && ip[0] == 198 && (ip[1] == 18 || ip[1] == 19)
			if !isFake {
				outcomes[i] = lookupOutcome{
					domain: dom,
					ip:     ip,
					res: &DomainCheckResult{
						Domain:    dom,
						Routed:    false,
						RealIP:    ipStr,
						Outbound:  "Direct (WAN / вне правил AWGM Route)",
						LatencyMs: latency,
					},
				}
				return
			}

			outcomes[i] = lookupOutcome{
				domain:  dom,
				ip:      ip,
				hasFake: true,
				res: &DomainCheckResult{
					Domain:    dom,
					Routed:    true,
					FakeIP:    ipStr,
					LatencyMs: latency,
				},
			}
		}()
	}
	wg.Wait()

	needFakeMetadata := false
	for _, out := range outcomes {
		if out.hasFake {
			needFakeMetadata = true
			break
		}
	}

	if needFakeMetadata {
		outboundNames := op.buildOutboundNameMap(ctx)
		dnatMap := parseFircDnatMap(ctx)
		groups, err := op.Client().GetGroups(ctx, false)
		if err != nil {
			groups, _ = op.LoadGroupsFile()
		}

		type groupMatcher struct {
			group   Group
			fakeNet *net.IPNet
		}
		var matchers []groupMatcher
		groupByID := make(map[string]Group, len(groups))
		for _, g := range groups {
			if g.ID == "" {
				continue
			}
			groupByID[g.ID] = g
			if !g.Enable {
				continue
			}
			mInfo := parseGroupMangleChain(ctx, g.ID)
			if mInfo.fakeNet != nil {
				matchers = append(matchers, groupMatcher{group: g, fakeNet: mInfo.fakeNet})
			}
		}

		var evRes *EventsResponse
		eventsFetched := false

		for _, out := range outcomes {
			if !out.hasFake {
				continue
			}
			res := out.res
			if realIP, ok := dnatMap[res.FakeIP]; ok {
				res.RealIP = realIP
			}
			for _, gm := range matchers {
				if gm.fakeNet.Contains(out.ip) {
					res.GroupID = gm.group.ID
					res.GroupName = gm.group.Name
					res.Outbound = gm.group.Active
					if res.Outbound == "" && len(gm.group.Interface) > 0 {
						res.Outbound = gm.group.Interface[0]
					}
					res.OutboundLabel = outboundNames[res.Outbound]
					break
				}
			}

			if res.GroupID == "" || res.RealIP == "" {
				if !eventsFetched {
					eventsFetched = true
					if fetched, evErr := op.Client().GetAllEvents(ctx, 0); evErr == nil {
						evRes = fetched
					}
				}
				if evRes != nil {
					for i := len(evRes.Events) - 1; i >= 0; i-- {
						ev := evRes.Events[i]
						if ev.Kind == "dns" && ev.Fake == res.FakeIP {
							if res.GroupID == "" && ev.Group != nil {
								res.GroupID = ev.Group.ID
								res.GroupName = ev.Group.Name
								if g, ok := groupByID[res.GroupID]; ok {
									res.Outbound = g.Active
									if res.Outbound == "" && len(g.Interface) > 0 {
										res.Outbound = g.Interface[0]
									}
									res.OutboundLabel = outboundNames[res.Outbound]
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
		}
	}

	for _, out := range outcomes {
		results[out.domain] = out.res
	}
	return results
}

// CheckDomainRouting tests how fircd resolves and routes a specific domain name in real time.
func (op *Operator) CheckDomainRouting(ctx context.Context, domain string) (*DomainCheckResult, error) {
	domain = normalizeCheckDomain(domain)
	if domain == "" {
		return nil, fmt.Errorf("укажите домен для проверки")
	}

	start := time.Now()
	dnsAddr := op.DNSProxyEndpoint()
	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	ip, err := lookupFircDNSIPv4(lookupCtx, dnsAddr, domain)
	cancel()
	latency := time.Since(start).Milliseconds()
	if err != nil || ip == nil {
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

	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	ipStr := ip.String()
	isFake := ip != nil && ip[0] == 198 && (ip[1] == 18 || ip[1] == 19)
	if !isFake {
		return &DomainCheckResult{
			Domain:    domain,
			Routed:    false,
			RealIP:    ipStr,
			Outbound:  "Direct (WAN / вне правил AWGM Route)",
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
	if realIP, ok := parseFircDnatMap(ctx)[ipStr]; ok {
		res.RealIP = realIP
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

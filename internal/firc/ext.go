package firc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/cloudagent"
)

const (
	// KeenDNSGroupID is the reserved 8-hex FIRC group ID managed for KeenDNS / Keenetic Cloud routing.
	KeenDNSGroupID = "c100d001"

	fircCloudAgentResetCooldown = 30 * time.Second

	// XHTTPServerFircMark is the socket mark set by xhttpserver when outbound_mode == "router"
	// so router-originated VLESS XHTTP server traffic is matched by FIRC groups in mangle OUTPUT.
	XHTTPServerFircMark = 0xf1ec

	// FIRC owns bits 0x40ff0000 (0x40000000 handled + 0x00ff0000 group/tunnel uplink mark).
	// Foreign marks (e.g. XHTTP 0xf1ec, NDMS policies) live outside 0x40ff0000 and must be preserved.
	fircNewFlowMatch      = "-m conntrack --ctstate NEW --ctdir ORIGINAL -m mark --mark 0x0/0x40ff0000"
	fircOrigUnmarkedMatch = "-m conntrack --ctdir ORIGINAL -m mark --mark 0x0/0x40ff0000"
	fircUnmarkedUDPMatch  = "-p udp " + fircOrigUnmarkedMatch
	fircRestoreMarkRule   = "-m conntrack --ctdir ORIGINAL -m mark --mark 0x0/0x40ff0000 -m connmark --mark 0x40000000/0x40000000 -j CONNMARK --restore-mark --nfmask 0x40ff0000 --ctmask 0x40ff0000"
	fircSaveMarkRule      = "-m mark --mark 0x40000000/0x40000000 -j CONNMARK --save-mark --nfmask 0x40ff0000 --ctmask 0x40ff0000"
)

var keenDNSCloudDomains = []string{
	"keenetic.com",
	"keenetic.io",
	"cloud.keenetic.net",
	"keenetic.ru",
	"keenetic.pro",
	"keenetic.link",
	"keenetic.name",
	"keenetic.cloud",
	"netcraze.io",
	"cloud.netcraze.net",
	"netcraze.pro",
	"netcraze.ru",
	"netcraze.com",
	"netcraze.cloud",
	"crazedns.ru",
	"crazedns.com",
	"crazedns.net",
	"omni.ru",
	"knt9.xyz",
}

var keenDNSCloudSubnets = []string{
	"185.162.93.0/24",
	"95.213.212.0/24",
	"95.213.181.0/24",
	"87.228.71.0/24",
	"91.92.241.0/24",
	"193.107.216.0/24",
	"178.250.154.0/24",
	"178.72.134.0/24",
	"85.198.119.0/24",
	"37.0.127.0/24",
	"5.35.2.0/24",
	"84.38.177.0/24",
	"49.12.59.0/24",
	"167.233.7.0/24",
	"82.202.218.0/24",
	"82.202.0.0/16",
	"31.135.14.0/24",
}

// allManagedServerSubnets lists all subnets automatically injected into devices.allow when
// inbound VPN server routing selections are active.
var allManagedServerSubnets = []string{
	"10.10.0.0/24",
	"10.66.0.0/16",
	"10.70.0.0/16",
	"10.77.0.0/16",
	"10.10.0.2/32",
	"127.0.0.0/8",
	"172.16.6.0/24",
	"172.16.7.0/24",
	"172.16.2.0/24",
	"172.20.8.0/24",
	"172.20.0.0/24",
	"172.16.1.0/24",
	"172.16.3.0/24",
}

// SetServerStatusCallbacks registers optional callbacks for checking in-process AWGM server states.
func (op *Operator) SetServerStatusCallbacks(xhttpFn func() bool, openfluxFn func() (bool, string)) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.xhttpStatusFn = xhttpFn
	op.openfluxStatusFn = openfluxFn
}

// SetWGServersProvider registers a callback returning dynamically discovered AWGM managed servers
// and Keenetic NDMS built-in/marked WireGuard servers.
func (op *Operator) SetWGServersProvider(fn func(ctx context.Context) []DynamicWGServer) {
	op.mu.Lock()
	defer op.mu.Unlock()
	op.wgServersProvider = fn
}

func (op *Operator) resolveWGServers(ctx context.Context) ([]DynamicWGServer, bool) {
	if op == nil {
		return nil, false
	}
	op.mu.Lock()
	fn := op.wgServersProvider
	op.mu.Unlock()
	if fn == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	list := fn(ctx)
	op.mu.Lock()
	op.lastWGServers = slices.Clone(list)
	op.mu.Unlock()
	return list, true
}

func (op *Operator) cachedWGServers() ([]DynamicWGServer, bool) {
	if op == nil {
		return nil, false
	}
	op.mu.Lock()
	hasProvider := op.wgServersProvider != nil
	cached := slices.Clone(op.lastWGServers)
	op.mu.Unlock()
	return cached, hasProvider
}

func (op *Operator) extConfigPath() string {
	return filepath.Join(op.configDir, "awgm-ext.json")
}

func normalizeServerRouting(sr ServerRoutingConfig) ServerRoutingConfig {
	seen := make(map[string]bool)
	var list []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			list = append(list, id)
		}
	}

	if sr.EnabledServers != nil {
		for _, id := range sr.EnabledServers {
			add(id)
		}
		sr.AwgServer = seen["awg_server"]
		sr.WdttServer = seen["wdtt_server"]
		sr.FreeTurnServer = seen["freeturn_server"]
		sr.VlessXhttpServer = seen["vless_xhttp_server"]
	} else {
		if sr.AwgServer {
			add("awg_server")
		}
		if sr.WdttServer {
			add("wdtt_server")
		}
		if sr.FreeTurnServer {
			add("freeturn_server")
		}
		if sr.VlessXhttpServer {
			add("vless_xhttp_server")
		}
	}
	if list == nil {
		list = []string{}
	}
	sr.EnabledServers = list
	return sr
}

func normalizeExtConfig(ext ExtConfig) ExtConfig {
	if ext.ProxyGroups == nil {
		ext.ProxyGroups = []ProxyGroup{}
	}
	for i := range ext.ProxyGroups {
		pg := &ext.ProxyGroups[i]
		pg.ID = strings.TrimSpace(pg.ID)
		pg.Name = strings.TrimSpace(pg.Name)
		seen := make(map[string]bool)
		var cleaned []string
		for _, iface := range pg.Interfaces {
			iface = strings.TrimSpace(iface)
			if iface != "" && iface != DNSOnlyInterface && !seen[iface] && len(cleaned) < 4 {
				seen[iface] = true
				cleaned = append(cleaned, iface)
			}
		}
		if cleaned == nil {
			cleaned = []string{}
		}
		pg.Interfaces = cleaned
		pg.Check = normalizeProxyGroupCheck(pg.Check, len(cleaned))
	}
	var kdIfaces []string
	seenKD := make(map[string]bool)
	for _, iface := range ext.KeenDNSCloud.Interfaces {
		iface = strings.TrimSpace(iface)
		if iface != "" && iface != DNSOnlyInterface && !seenKD[iface] && len(kdIfaces) < 4 {
			seenKD[iface] = true
			kdIfaces = append(kdIfaces, iface)
		}
	}
	if kdIfaces == nil {
		kdIfaces = []string{}
	}
	ext.KeenDNSCloud.Interfaces = kdIfaces
	ext.KeenDNSCloud.ProxyGroupID = strings.TrimSpace(ext.KeenDNSCloud.ProxyGroupID)
	ext.KeenDNSCloud.LastSyncError = ""
	ext.KeenDNSCloud.LastSyncAt = 0
	ext.ServerRouting = normalizeServerRouting(ext.ServerRouting)
	ext.AvailableServers = nil
	return ext
}

func extWantsKeenDNS(ext ExtConfig) bool {
	return ext.KeenDNSCloud.Enabled && len(resolveKeenDNSInterfaces(ext)) > 0
}

func (op *Operator) enrichExtRuntimeStatus(ctx context.Context, ext ExtConfig) ExtConfig {
	ext.AvailableServers = op.ListAvailableServers(ctx)
	ext.GroupLinks = op.LoadExtConfig().GroupLinks
	if extWantsKeenDNS(ext) {
		lastErr, lastAt := op.keenDNSCloudStatus()
		ext.KeenDNSCloud.LastSyncError = lastErr
		ext.KeenDNSCloud.LastSyncAt = lastAt
	} else {
		ext.KeenDNSCloud.LastSyncError = ""
		ext.KeenDNSCloud.LastSyncAt = 0
	}
	return ext
}

func (op *Operator) loadExtConfigUnlocked() (ExtConfig, bool) {
	if op.CheckNotForeign() != nil {
		// Настройки поверх рантайма при стороннем firc не действуют.
		return normalizeExtConfig(ExtConfig{GroupLinks: map[string]string{}}), false
	}
	data, err := os.ReadFile(op.extConfigPath())
	if err != nil {
		return normalizeExtConfig(ExtConfig{GroupLinks: map[string]string{}}), false
	}
	var ext ExtConfig
	if err := json.Unmarshal(data, &ext); err != nil {
		return normalizeExtConfig(ExtConfig{GroupLinks: map[string]string{}}), true
	}
	return normalizeExtConfig(ext), true
}

func (op *Operator) loadExtConfigWithGen() (ExtConfig, uint64) {
	op.extConfigMu.RLock()
	gen := op.extConfigGen.Load()
	ext, exists := op.loadExtConfigUnlocked()
	op.extConfigMu.RUnlock()
	if exists {
		op.extConfigSaved.Store(true)
	}
	return ext, gen
}

func (op *Operator) currentExtConfigGen() uint64 {
	return op.extConfigGen.Load()
}

func (op *Operator) hasPersistedExtConfig() bool {
	if op.extConfigSaved.Load() {
		return true
	}
	if _, err := os.Stat(op.extConfigPath()); err == nil {
		op.extConfigSaved.Store(true)
		return true
	}
	return false
}

// LoadExtConfig loads the persisted AWGM FIRC extension config from /opt/etc/firc/awgm-ext.json.
func (op *Operator) LoadExtConfig() ExtConfig {
	ext, _ := op.loadExtConfigWithGen()
	return ext
}

// LoadExtConfigWithStatus returns the persisted ExtConfig enriched with live AvailableServers and KeenDNS sync statuses.
func (op *Operator) LoadExtConfigWithStatus(ctx context.Context) ExtConfig {
	op.migrateGroupLinks()
	return op.enrichExtRuntimeStatus(ctx, op.LoadExtConfig())
}

// SaveExtConfig persists the AWGM FIRC extension config atomically.
func (op *Operator) SaveExtConfig(ext ExtConfig) error {
	_, err := op.saveExtConfigReturningPrev(ext)
	return err
}

func (op *Operator) saveExtConfigReturningPrev(ext ExtConfig) (ExtConfig, error) {
	ext = normalizeExtConfig(ext)
	op.extConfigMu.Lock()
	saved, _ := op.loadExtConfigUnlocked()
	ext.GroupLinks = saved.GroupLinks
	if err := op.writeExtConfigLocked(ext); err != nil {
		op.extConfigMu.Unlock()
		return saved, err
	}
	op.extConfigGen.Add(1)
	extNetfilterGen.Add(1)
	op.extConfigMu.Unlock()

	if !extWantsKeenDNS(ext) {
		op.abortKeenDNSCloudSync()
	}
	return saved, nil
}

func (op *Operator) restoreProxyGroups(prev []ProxyGroup) {
	op.extConfigMu.Lock()
	defer op.extConfigMu.Unlock()
	data, err := os.ReadFile(op.extConfigPath())
	if err != nil {
		return
	}
	var cur ExtConfig
	if err := json.Unmarshal(data, &cur); err != nil {
		return
	}
	cur.ProxyGroups = slices.Clone(prev)
	cur = normalizeExtConfig(cur)
	if err := op.writeExtConfigLocked(cur); err == nil {
		op.extConfigGen.Add(1)
		extNetfilterGen.Add(1)
	}
}

func (op *Operator) writeExtConfigLocked(ext ExtConfig) error {
	if err := op.CheckNotForeign(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(ext, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(op.configDir, 0755); err != nil {
		return err
	}
	if err := atomicWriteYAMLFile(op.extConfigPath(), data, 0644); err != nil {
		return err
	}
	op.extConfigSaved.Store(true)
	return nil
}

func isNetInterfaceUp(name string) bool {
	nif, err := net.InterfaceByName(name)
	if err != nil || nif == nil {
		return false
	}
	return (nif.Flags & net.FlagUp) != 0
}

func isProcessAlive(substrings ...string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) == 0 || name[0] < '0' || name[0] > '9' {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil || len(cmdline) == 0 {
			continue
		}
		cmdStr := strings.ReplaceAll(string(cmdline), "\x00", " ")
		for _, sub := range substrings {
			if sub != "" && strings.Contains(cmdStr, sub) {
				return true
			}
		}
	}
	return false
}

type rciRootSnapshot struct {
	WireguardServer struct {
		Config struct {
			Enable bool   `json:"enable"`
			Subnet string `json:"subnet"`
		} `json:"config"`
	} `json:"wireguard-server"`
	SstpServer struct {
		Config struct {
			Enable    bool   `json:"enable"`
			PoolStart string `json:"pool-start"`
		} `json:"config"`
	} `json:"sstp-server"`
	VpnServer struct {
		Enable bool `json:"enable"`
		Config struct {
			Enable    bool   `json:"enable"`
			PoolStart string `json:"pool-start"`
		} `json:"config"`
	} `json:"vpn-server"`
	OcServer struct {
		Enable bool `json:"enable"`
		Config struct {
			Enable    bool   `json:"enable"`
			PoolStart string `json:"pool-start"`
		} `json:"config"`
	} `json:"oc-server"`
	Crypto struct {
		L2tpServer struct {
			Enable    bool   `json:"enable"`
			PoolStart string `json:"pool-start"`
		} `json:"l2tp-server"`
		VirtualIPServer struct {
			Enable    bool   `json:"enable"`
			PoolStart string `json:"pool-start"`
		} `json:"virtual-ip-server"`
		VirtualIPServerIkev2 struct {
			Enable    bool   `json:"enable"`
			PoolStart string `json:"pool-start"`
		} `json:"virtual-ip-server-ikev2"`
	} `json:"crypto"`
}

func fetchKeeneticRCISnapshot(ctx context.Context) (rciRootSnapshot, bool) {
	var snap rciRootSnapshot
	reqCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://127.0.0.1:79/rci/", nil)
	if err != nil {
		return snap, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return snap, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return snap, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snap, false
	}
	return snap, true
}

func poolStartToCIDR24(ipStr, fallback string) string {
	ip := net.ParseIP(strings.TrimSpace(ipStr)).To4()
	if ip == nil {
		return fallback
	}
	return fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
}

func normalizeCIDR24(cidrStr, fallback string) string {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidrStr))
	if err != nil || ipNet == nil {
		return fallback
	}
	return ipNet.String()
}

// FormatIPv4SubnetCIDR converts an IPv4 address and mask (dotted like "255.255.255.0" or prefix like "24")
// into a normalized network CIDR string (e.g. "172.31.0.0/24").
func FormatIPv4SubnetCIDR(addr, mask string) string {
	ip := net.ParseIP(strings.TrimSpace(addr)).To4()
	if ip == nil {
		return ""
	}
	maskStr := strings.TrimPrefix(strings.TrimSpace(mask), "/")
	var ipMask net.IPMask
	if maskIP := net.ParseIP(maskStr).To4(); maskIP != nil {
		cand := net.IPv4Mask(maskIP[0], maskIP[1], maskIP[2], maskIP[3])
		if ones, bits := cand.Size(); bits == 32 && (ones > 0 || maskStr == "0.0.0.0") {
			ipMask = cand
		}
	}
	if ipMask == nil && maskStr != "" {
		if prefix, err := strconv.Atoi(maskStr); err == nil && prefix >= 0 && prefix <= 32 {
			ipMask = net.CIDRMask(prefix, 32)
		}
	}
	if ipMask == nil {
		ipMask = net.CIDRMask(24, 32)
	}
	ones, _ := ipMask.Size()
	return fmt.Sprintf("%s/%d", ip.Mask(ipMask).String(), ones)
}

func formatWGServerDisplayName(srv DynamicWGServer) string {
	name := strings.TrimSpace(srv.Name)
	if name == "" {
		switch {
		case srv.Category == "awgm":
			name = "Сервер AmneziaWG"
		case srv.BuiltIn:
			name = "Keenetic WireGuard"
		default:
			name = strings.TrimSpace(srv.NDMSName)
		}
	}
	if ndmsName := strings.TrimSpace(srv.NDMSName); ndmsName != "" && !strings.Contains(name, ndmsName) {
		return name + " (" + ndmsName + ")"
	}
	return name
}

func formatWGServerDetail(srv DynamicWGServer) string {
	var parts []string
	if k := strings.TrimSpace(srv.KernelIface); k != "" {
		parts = append(parts, k)
	}
	if s := strings.TrimSpace(srv.Subnet); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

func matchEnabledWGServers(sr ServerRoutingConfig, wgServers []DynamicWGServer) []DynamicWGServer {
	var matched []DynamicWGServer
	seen := make(map[string]bool)
	add := func(srv DynamicWGServer) {
		if srv.ID == "" || seen[srv.ID] {
			return
		}
		seen[srv.ID] = true
		matched = append(matched, srv)
	}

	for _, srv := range wgServers {
		if sr.IsServerEnabled(srv.ID) {
			add(srv)
		}
	}
	if sr.IsServerEnabled("awg_server") {
		matchedWireguard2 := false
		for _, srv := range wgServers {
			if srv.Category == "awgm" && strings.EqualFold(strings.TrimSpace(srv.NDMSName), "Wireguard2") {
				add(srv)
				matchedWireguard2 = true
			}
		}
		if !matchedWireguard2 {
			for _, srv := range wgServers {
				if srv.Category == "awgm" {
					add(srv)
				}
			}
		}
	}
	if sr.IsServerEnabled("keenetic_wg") {
		for _, srv := range wgServers {
			if srv.Category == "system" && srv.BuiltIn {
				add(srv)
			}
		}
	}
	return matched
}

func findFreeTurnDetailIface(wgServers []DynamicWGServer) string {
	for _, srv := range wgServers {
		if srv.Category == "awgm" && strings.EqualFold(strings.TrimSpace(srv.NDMSName), "Wireguard2") && strings.TrimSpace(srv.KernelIface) != "" {
			return strings.TrimSpace(srv.KernelIface)
		}
	}
	for _, srv := range wgServers {
		if srv.Category == "awgm" && strings.TrimSpace(srv.KernelIface) != "" {
			return strings.TrimSpace(srv.KernelIface)
		}
	}
	return ""
}

// ListAvailableServers probes and returns all AWGM and Keenetic system VPN servers with live status.
func (op *Operator) ListAvailableServers(ctx context.Context) []InboundServerItem {
	op.mu.Lock()
	xhttpFn := op.xhttpStatusFn
	openfluxFn := op.openfluxStatusFn
	op.mu.Unlock()

	wgServers, hasWGProvider := op.resolveWGServers(ctx)

	wdttUp := isNetInterfaceUp("opkgtun17") || isNetInterfaceUp("opkgtun19") || isProcessAlive("wdtt-server", "qwdtt-server")
	ftUp := isProcessAlive("freeturn-server", "ft-server")
	xhttpUp := false
	if xhttpFn != nil {
		xhttpUp = xhttpFn()
	} else {
		xhttpUp = isProcessAlive("awgm-xhttpd")
	}
	openfluxUp := false
	openfluxIface := "opkgtun"
	if openfluxFn != nil {
		openfluxUp, openfluxIface = openfluxFn()
		if openfluxIface == "" {
			openfluxIface = "opkgtun"
		}
	} else {
		openfluxUp = isProcessAlive("openflux")
	}

	rci, rciOK := fetchKeeneticRCISnapshot(ctx)
	wgSysUp := isNetInterfaceUp("nwg0")
	wgSysSubnet := "172.16.6.0/24"
	sstpUp := false
	sstpSubnet := "172.16.7.0/24"
	l2tpUp := false
	l2tpSubnet := "172.16.2.0/24"
	ikev2Up := false
	ikev2Subnet := "172.20.8.0/24"
	ikev1Up := false
	ikev1Subnet := "172.20.0.0/24"
	pptpUp := false
	pptpSubnet := "172.16.1.0/24"
	ocUp := false
	ocSubnet := "172.16.3.0/24"

	if rciOK {
		wgSysUp = rci.WireguardServer.Config.Enable || wgSysUp
		wgSysSubnet = normalizeCIDR24(rci.WireguardServer.Config.Subnet, wgSysSubnet)
		sstpUp = rci.SstpServer.Config.Enable
		sstpSubnet = poolStartToCIDR24(rci.SstpServer.Config.PoolStart, sstpSubnet)
		l2tpUp = rci.Crypto.L2tpServer.Enable
		l2tpSubnet = poolStartToCIDR24(rci.Crypto.L2tpServer.PoolStart, l2tpSubnet)
		ikev2Up = rci.Crypto.VirtualIPServerIkev2.Enable
		ikev2Subnet = poolStartToCIDR24(rci.Crypto.VirtualIPServerIkev2.PoolStart, ikev2Subnet)
		ikev1Up = rci.Crypto.VirtualIPServer.Enable
		ikev1Subnet = poolStartToCIDR24(rci.Crypto.VirtualIPServer.PoolStart, ikev1Subnet)
		pptpUp = rci.VpnServer.Enable || rci.VpnServer.Config.Enable
		pptpSubnet = poolStartToCIDR24(rci.VpnServer.Config.PoolStart, pptpSubnet)
		ocUp = rci.OcServer.Enable || rci.OcServer.Config.Enable
		ocSubnet = poolStartToCIDR24(rci.OcServer.Config.PoolStart, ocSubnet)
	}

	items := make([]InboundServerItem, 0, len(wgServers)+12)
	if hasWGProvider {
		for _, srv := range wgServers {
			if srv.Category == "awgm" {
				items = append(items, InboundServerItem{
					ID:       srv.ID,
					Name:     formatWGServerDisplayName(srv),
					Category: "awgm",
					Running:  srv.Running,
					Detail:   formatWGServerDetail(srv),
				})
			}
		}
		items = append(items, InboundServerItem{
			ID:       "wdtt_server",
			Name:     "Сервер WDTT",
			Category: "awgm",
			Running:  wdttUp,
			Detail:   "opkgtun17/19 · 10.66.0.0/16",
		})
		ftEnabled := op.LoadExtConfig().ServerRouting.IsServerEnabled("freeturn_server")
		if ftUp || ftEnabled {
			ftDetail := "10.10.0.2/32"
			if kIface := findFreeTurnDetailIface(wgServers); kIface != "" {
				ftDetail = kIface + " · 10.10.0.2/32"
			}
			items = append(items, InboundServerItem{
				ID:       "freeturn_server",
				Name:     "Сервер FreeTurn",
				Category: "awgm",
				Running:  ftUp,
				Detail:   ftDetail,
			})
		}
		items = append(items,
			InboundServerItem{
				ID:       "vless_xhttp_server",
				Name:     "Сервер VLESS XHTTP",
				Category: "awgm",
				Running:  xhttpUp,
				Detail:   "router · XHTTP",
			},
			InboundServerItem{
				ID:       "openflux_server",
				Name:     "Сервер OpenFlux",
				Category: "awgm",
				Running:  openfluxUp,
				Detail:   openfluxIface + " · 10.77.0.0/16",
			},
		)
		for _, srv := range wgServers {
			if srv.Category == "system" {
				items = append(items, InboundServerItem{
					ID:       srv.ID,
					Name:     formatWGServerDisplayName(srv),
					Category: "system",
					Running:  srv.Running,
					Detail:   formatWGServerDetail(srv),
				})
			}
		}
	} else {
		awgUp := isNetInterfaceUp("nwg2")
		items = append(items,
			InboundServerItem{
				ID:       "awg_server",
				Name:     "Сервер AmneziaWG (Wireguard2)",
				Category: "awgm",
				Running:  awgUp,
				Detail:   "nwg2 · 10.10.0.0/24",
			},
			InboundServerItem{
				ID:       "wdtt_server",
				Name:     "Сервер WDTT",
				Category: "awgm",
				Running:  wdttUp,
				Detail:   "opkgtun17/19 · 10.66.0.0/16",
			},
			InboundServerItem{
				ID:       "freeturn_server",
				Name:     "Сервер FreeTurn",
				Category: "awgm",
				Running:  ftUp,
				Detail:   "nwg2 · 10.10.0.2/32",
			},
			InboundServerItem{
				ID:       "vless_xhttp_server",
				Name:     "Сервер VLESS XHTTP",
				Category: "awgm",
				Running:  xhttpUp,
				Detail:   "router · XHTTP",
			},
			InboundServerItem{
				ID:       "openflux_server",
				Name:     "Сервер OpenFlux",
				Category: "awgm",
				Running:  openfluxUp,
				Detail:   openfluxIface + " · 10.77.0.0/16",
			},
			InboundServerItem{
				ID:       "keenetic_wg",
				Name:     "Keenetic WireGuard (Wireguard0)",
				Category: "system",
				Running:  wgSysUp,
				Detail:   "nwg0 · " + wgSysSubnet,
			},
		)
	}

	items = append(items,
		InboundServerItem{
			ID:       "keenetic_sstp",
			Name:     "Keenetic SSTP",
			Category: "system",
			Running:  sstpUp,
			Detail:   "sstp+ · " + sstpSubnet,
		},
		InboundServerItem{
			ID:       "keenetic_l2tp",
			Name:     "Keenetic L2TP/IPsec",
			Category: "system",
			Running:  l2tpUp,
			Detail:   "l2tp+ · " + l2tpSubnet,
		},
		InboundServerItem{
			ID:       "keenetic_ikev2",
			Name:     "Keenetic IKEv2/IPsec",
			Category: "system",
			Running:  ikev2Up,
			Detail:   "xfrms2 · " + ikev2Subnet,
		},
		InboundServerItem{
			ID:       "keenetic_ikev1",
			Name:     "Keenetic Virtual IP (IKEv1)",
			Category: "system",
			Running:  ikev1Up,
			Detail:   "xfrms1 · " + ikev1Subnet,
		},
		InboundServerItem{
			ID:       "keenetic_pptp",
			Name:     "Keenetic PPTP",
			Category: "system",
			Running:  pptpUp,
			Detail:   "vpn+ · " + pptpSubnet,
		},
		InboundServerItem{
			ID:       "keenetic_oc",
			Name:     "Keenetic OpenConnect",
			Category: "system",
			Running:  ocUp,
			Detail:   "oc+ · " + ocSubnet,
		},
	)
	return items
}

func normalizeProxyGroupCheck(c *GroupCheck, ifaceCount int) *GroupCheck {
	if c == nil {
		return nil
	}
	if ifaceCount < 2 {
		return &GroupCheck{Mode: "none"}
	}
	out := normalizeGroupCheck(c)
	switch out.Mode {
	case "url":
	case "keendns":
		out.URL = ""
		if out.TimeoutMs == 0 {
			out.TimeoutMs = 5000
		}
	default:
		out.Mode = "none"
		out.URL = ""
	}
	return &out
}

func findProxyGroupByID(ext ExtConfig, id string) (ProxyGroup, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProxyGroup{}, false
	}
	for _, pg := range ext.ProxyGroups {
		if pg.ID == id {
			return pg, true
		}
	}
	return ProxyGroup{}, false
}

// keepOmittedProxyGroupChecks возвращает прокси-группам без поля check сохранённую
// проверку: вкладка со старым фронтом его не присылает, отсутствие — «не менять».
func (op *Operator) keepOmittedProxyGroupChecks(ext ExtConfig) {
	saved := op.LoadExtConfig()
	for i := range ext.ProxyGroups {
		pg := &ext.ProxyGroups[i]
		if pg.Check != nil {
			continue
		}
		if old, ok := findProxyGroupByID(saved, pg.ID); ok {
			pg.Check = old.Check
		}
	}
}

func resolveKeenDNSCheck(ext ExtConfig, kdIfaces []string, existingCheck *GroupCheck) *GroupCheck {
	if len(kdIfaces) < 2 {
		return &GroupCheck{Mode: "none"}
	}
	if ext.KeenDNSCloud.CloudHealthCheck {
		return &GroupCheck{Mode: "keendns", TimeoutMs: 5000}
	}
	if pg, ok := findProxyGroupByID(ext, ext.KeenDNSCloud.ProxyGroupID); ok && pg.Check != nil {
		return normalizeProxyGroupCheck(pg.Check, len(kdIfaces))
	}
	// Связь — прежнюю проверку задала прокси-группа: она уходит вместе с ней.
	if ext.GroupLinks[KeenDNSGroupID] == "" && existingCheck != nil && strings.TrimSpace(existingCheck.Mode) == "url" {
		preserved := *existingCheck
		return &preserved
	}
	return &GroupCheck{Mode: "none"}
}

func resolveKeenDNSInterfaces(ext ExtConfig) []string {
	if ext.KeenDNSCloud.ProxyGroupID != "" {
		for _, pg := range ext.ProxyGroups {
			if pg.ID == ext.KeenDNSCloud.ProxyGroupID && len(pg.Interfaces) > 0 {
				return append([]string(nil), pg.Interfaces...)
			}
		}
	}
	if len(ext.KeenDNSCloud.Interfaces) > 0 {
		return append([]string(nil), ext.KeenDNSCloud.Interfaces...)
	}
	return nil
}

func buildKeenDNSGroup(ifaces []string, cloudHealthCheck bool) Group {
	rules := make([]Rule, 0, len(keenDNSCloudDomains)+len(keenDNSCloudSubnets))
	idx := 1
	for _, dom := range keenDNSCloudDomains {
		rules = append(rules, Rule{
			ID:     fmt.Sprintf("c1%06x", idx),
			Type:   "namespace",
			Rule:   dom,
			Enable: true,
		})
		idx++
	}
	for _, cidr := range keenDNSCloudSubnets {
		rules = append(rules, Rule{
			ID:     fmt.Sprintf("c1%06x", idx),
			Type:   "subnet",
			Rule:   cidr,
			Enable: true,
		})
		idx++
	}
	resolveTunnel := true
	// Always populate Check explicitly (mode "none" when disabled) so PUT /api/v1/groups
	// actively clears any previously persisted check in fircd (omitting "check" tells fircd
	// to preserve existing->check).
	check := &GroupCheck{Mode: "none"}
	if cloudHealthCheck && len(ifaces) >= 2 {
		check = &GroupCheck{Mode: "keendns", TimeoutMs: 5000}
	}
	return Group{
		ID:        KeenDNSGroupID,
		Name:      "KeenDNS / Облако Keenetic",
		Interface: GroupInterface(ifaces),
		Enable:    true,
		Color:     "#38bdf8",
		Devices: GroupDevices{
			Allow: []string{},
			Deny:  []string{},
		},
		Resolve: GroupResolve{
			Tunnel: &resolveTunnel,
		},
		Check: check,
		Rules: rules,
	}
}

func activeServerSubnets(sr ServerRoutingConfig) []string {
	return activeServerSubnetsWithWG(sr, nil, false)
}

func activeServerSubnetsWithWG(sr ServerRoutingConfig, wgServers []DynamicWGServer, hasProvider bool) []string {
	var out []string
	add := func(cidr string) {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			return
		}
		for _, existing := range out {
			if existing == cidr {
				return
			}
		}
		out = append(out, cidr)
	}
	if hasProvider {
		for _, srv := range matchEnabledWGServers(sr, wgServers) {
			add(srv.Subnet)
		}
	} else {
		if sr.IsServerEnabled("awg_server") {
			add("10.10.0.0/24")
		}
		if sr.IsServerEnabled("keenetic_wg") {
			add("172.16.6.0/24")
		}
	}
	if sr.IsServerEnabled("wdtt_server") {
		add("10.66.0.0/16")
		add("10.70.0.0/16")
	}
	if sr.IsServerEnabled("freeturn_server") {
		add("10.10.0.2/32")
	}
	if sr.IsServerEnabled("vless_xhttp_server") {
		add("127.0.0.0/8")
	}
	if sr.IsServerEnabled("openflux_server") {
		add("10.77.0.0/16")
	}
	if sr.IsServerEnabled("keenetic_sstp") {
		add("172.16.7.0/24")
	}
	if sr.IsServerEnabled("keenetic_l2tp") {
		add("172.16.2.0/24")
	}
	if sr.IsServerEnabled("keenetic_ikev2") {
		add("172.20.8.0/24")
	}
	if sr.IsServerEnabled("keenetic_ikev1") {
		add("172.20.0.0/24")
	}
	if sr.IsServerEnabled("keenetic_pptp") {
		add("172.16.1.0/24")
	}
	if sr.IsServerEnabled("keenetic_oc") {
		add("172.16.3.0/24")
	}
	return out
}

func isManagedServerSubnet(entry string) bool {
	for _, s := range allManagedServerSubnets {
		if entry == s {
			return true
		}
	}
	return false
}

func isKnownManagedServerSubnet(entry string, wgServers []DynamicWGServer) bool {
	norm := strings.TrimSpace(entry)
	if norm == "" {
		return false
	}
	if isManagedServerSubnet(norm) {
		return true
	}
	for _, srv := range wgServers {
		if sub := strings.TrimSpace(srv.Subnet); sub != "" && strings.EqualFold(norm, sub) {
			return true
		}
	}
	return false
}

// syncGroupsWithExt returns an updated slice of FIRC groups incorporating KeenDNS Cloud
// and inbound VPN server device subnets, plus a boolean indicating whether any group changed.
func syncGroupsWithExt(groups []Group, ext ExtConfig) ([]Group, bool) {
	updated, changed, _ := syncGroupsWithExtWithWG(groups, ext, nil, false, nil)
	return updated, changed
}

func (op *Operator) syncGroupsWithExt(ctx context.Context, groups []Group, ext ExtConfig) ([]Group, bool) {
	wgServers, hasProvider := op.resolveWGServers(ctx)
	rp := op.routerPoliciesFor(ctx, groups, ext.ServerRouting)
	updated, changed, denyRules := syncGroupsWithExtWithWG(groups, ext, wgServers, hasProvider, rp)
	if rp != nil {
		op.warnKeptServerDenies(updated, denyRules)
	}
	return updated, changed
}

func syncGroupsWithExtWithWG(groups []Group, ext ExtConfig, wgServers []DynamicWGServer, hasProvider bool, rp *RouterPolicies) ([]Group, bool, serverDenyRules) {
	changed := false
	wantedSubnets := activeServerSubnetsWithWG(ext.ServerRouting, wgServers, hasProvider)
	denyRules := newServerDenyRules(ext.ServerRouting, wantedSubnets, wgServers, rp)
	kdIfaces := resolveKeenDNSInterfaces(ext)
	wantKeenDNS := ext.KeenDNSCloud.Enabled && len(kdIfaces) > 0

	var filtered []Group
	global, wantGlobal := globalGroup(ext)
	foundGlobal := false
	foundKeenDNS := false
	for _, g := range groups {
		if g.ID == globalGroupID {
			foundGlobal = true
			if global.Check == nil {
				global.Check = g.Check
				if ext.GroupLinks[globalGroupID] != "" {
					global.Check = &GroupCheck{Mode: "none"}
				}
			}
			if !wantGlobal || !g.Enable || !slices.Equal(g.Interface, global.Interface) || !keenDNSRulesEqual(g.Rules, global.Rules) || !groupCheckEqual(g.Check, global.Check) {
				changed = true
			}
			continue
		}
		if g.ID == KeenDNSGroupID {
			if !wantKeenDNS {
				changed = true
				continue
			}
			foundKeenDNS = true
			kdGroup := buildKeenDNSGroup(kdIfaces, ext.KeenDNSCloud.CloudHealthCheck)
			kdGroup.Check = resolveKeenDNSCheck(ext, kdIfaces, g.Check)
			if !equalStringSlices([]string(g.Interface), kdIfaces) || !g.Enable || !keenDNSRulesEqual(g.Rules, kdGroup.Rules) || !groupCheckEqual(g.Check, kdGroup.Check) {
				changed = true
			}
			filtered = append([]Group{kdGroup}, filtered...)
			continue
		}

		// Sync server subnets in devices.allow only if the group restricts devices.allow
		// (if user devices.allow is empty, fircd already matches all devices).
		var userAllow []string
		for _, entry := range g.Devices.Allow {
			if !isKnownManagedServerSubnet(entry, wgServers) {
				userAllow = append(userAllow, entry)
			}
		}
		var newAllow []string
		if len(userAllow) > 0 {
			newAllow = append(newAllow, userAllow...)
			newAllow = append(newAllow, wantedSubnets...)
		}
		if !equalStringSlices(g.Devices.Allow, newAllow) {
			g.Devices.Allow = newAllow
			changed = true
		}

		// Strip any deny entries that conflict with enabled inbound VPN servers.
		if len(g.Devices.Deny) > 0 {
			var newDeny []string
			for _, entry := range g.Devices.Deny {
				if strip, _, _ := denyRules.verdict(entry); !strip {
					newDeny = append(newDeny, entry)
				}
			}
			if !equalStringSlices(g.Devices.Deny, newDeny) {
				if newDeny == nil {
					newDeny = []string{}
				}
				g.Devices.Deny = newDeny
				changed = true
			}
		}

		if desired := linkedProxyGroupCheck(ext, g); desired != nil && !groupCheckEqual(g.Check, desired) {
			g.Check = desired
			changed = true
		}

		filtered = append(filtered, g)
	}

	if wantKeenDNS && !foundKeenDNS {
		kdGroup := buildKeenDNSGroup(kdIfaces, ext.KeenDNSCloud.CloudHealthCheck)
		kdGroup.Check = resolveKeenDNSCheck(ext, kdIfaces, nil)
		filtered = append([]Group{kdGroup}, filtered...)
		changed = true
	}

	if wantGlobal {
		index := 0
		if len(filtered) > 0 && filtered[0].ID == KeenDNSGroupID {
			index = 1
		}
		filtered = slices.Insert(filtered, index, global)
		if !foundGlobal || index >= len(groups) || groups[index].ID != globalGroupID {
			changed = true
		}
	}
	return filtered, changed, denyRules
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keenDNSRulesEqual(a, b []Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Rule != b[i].Rule || a[i].Enable != b[i].Enable {
			return false
		}
	}
	return true
}

func normalizeGroupCheck(c *GroupCheck) GroupCheck {
	if c == nil {
		return GroupCheck{Mode: "none"}
	}
	out := *c
	out.Mode = strings.TrimSpace(out.Mode)
	if out.Mode == "" {
		out.Mode = "none"
	}
	out.URL = strings.TrimSpace(out.URL)
	return out
}

func groupCheckEqual(a, b *GroupCheck) bool {
	return normalizeGroupCheck(a) == normalizeGroupCheck(b)
}

// ApplyExtConfig saves the new ExtConfig, synchronizes FIRC groups (KeenDNS Cloud + server subnets),
// and reconciles iptables hooks for server DNS redirection and OUTPUT chain routing.
func (op *Operator) ApplyExtConfig(ctx context.Context, ext ExtConfig) (ExtConfig, error) {
	op.mutationMu.Lock()
	defer op.mutationMu.Unlock()

	if err := op.checkMutationAllowed(); err != nil {
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	op.migrateGroupLinks()
	op.keepOmittedProxyGroupChecks(ext)
	ext = normalizeExtConfig(ext)
	prevExt, err := op.saveExtConfigReturningPrev(ext)
	if err != nil {
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	ext.GroupLinks = prevExt.GroupLinks
	if op.ManuallyDisabled() {
		groups, loadErr := op.LoadGroupsFile()
		if loadErr != nil {
			if proxyGroupInterfacesChanged(prevExt.ProxyGroups, ext.ProxyGroups) {
				op.restoreProxyGroups(prevExt.ProxyGroups)
			}
			return op.enrichExtRuntimeStatus(ctx, ext), loadErr
		}
		groups, ifaceChanged := syncLinkedProxyGroupInterfaces(groups, prevExt, ext)
		updated, changed := op.syncGroupsWithExt(ctx, groups, ext)
		if ifaceChanged || changed {
			if saveErr := op.SaveGroupsFile(updated); saveErr != nil {
				if ifaceChanged {
					op.restoreProxyGroups(prevExt.ProxyGroups)
				}
				return op.enrichExtRuntimeStatus(ctx, ext), saveErr
			}
		}
		if err := op.refreshGroupLinks(ext); err != nil {
			return op.enrichExtRuntimeStatus(ctx, ext), err
		}
		return op.enrichExtRuntimeStatus(ctx, ext), nil
	}
	if err := op.reconcileExtLocked(ctx, &prevExt); err != nil {
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	return op.enrichExtRuntimeStatus(ctx, ext), nil
}

// StageExtConfigOffline validates, normalizes, and saves ExtConfig and any derived
// disk groups (groups.yaml) under mutationMu without probing fircd IPC, invoking
// iptables, or starting any runtime process. Used when FIRC is not selected.
func (op *Operator) StageExtConfigOffline(ctx context.Context, ext ExtConfig) (ExtConfig, error) {
	op.mutationMu.Lock()
	defer op.mutationMu.Unlock()

	if err := op.checkMutationAllowed(); err != nil {
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	op.migrateGroupLinks()
	op.keepOmittedProxyGroupChecks(ext)
	ext = normalizeExtConfig(ext)
	prevExt, err := op.saveExtConfigReturningPrev(ext)
	if err != nil {
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	ext.GroupLinks = prevExt.GroupLinks
	groups, err := op.LoadGroupsFile()
	if err != nil {
		if proxyGroupInterfacesChanged(prevExt.ProxyGroups, ext.ProxyGroups) {
			op.restoreProxyGroups(prevExt.ProxyGroups)
		}
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	groups, ifaceChanged := syncLinkedProxyGroupInterfaces(groups, prevExt, ext)
	updated, changed := op.syncGroupsWithExt(ctx, groups, ext)
	if ifaceChanged || changed {
		if err := op.SaveGroupsFile(updated); err != nil {
			if ifaceChanged {
				op.restoreProxyGroups(prevExt.ProxyGroups)
			}
			return op.enrichExtRuntimeStatus(ctx, ext), err
		}
	}
	if err := op.refreshGroupLinks(ext); err != nil {
		return op.enrichExtRuntimeStatus(ctx, ext), err
	}
	return op.enrichExtRuntimeStatus(ctx, ext), nil
}

// ReconcileExt synchronizes FIRC groups and netfilter chains with the current ExtConfig.
func (op *Operator) ReconcileExt(ctx context.Context) error {
	op.mutationMu.Lock()
	defer op.mutationMu.Unlock()
	return op.reconcileExtLocked(ctx, nil)
}

func (op *Operator) reconcileExtLocked(ctx context.Context, prevExt *ExtConfig) error {
	if op.ManuallyDisabled() {
		return nil
	}
	op.migrateGroupLinks()
	ext, cfgGen := op.loadExtConfigWithGen()
	running := op.IsRunning(ctx)
	var prevGroups []Group
	var err error
	if running {
		prevGroups, err = op.Client().GetGroups(ctx, true)
	} else {
		prevGroups, err = op.LoadGroupsFile()
	}
	if err != nil {
		if prevExt != nil && proxyGroupInterfacesChanged(prevExt.ProxyGroups, ext.ProxyGroups) {
			op.restoreProxyGroups(prevExt.ProxyGroups)
		}
		return err
	}
	groups := prevGroups
	ifaceChanged := false
	if prevExt != nil {
		groups, ifaceChanged = syncLinkedProxyGroupInterfaces(groups, *prevExt, ext)
	}
	updated, changed := op.syncGroupsWithExt(ctx, groups, ext)
	if ifaceChanged || changed {
		if err := op.checkMutationAllowed(); err != nil {
			if ifaceChanged && prevExt != nil {
				op.restoreProxyGroups(prevExt.ProxyGroups)
			}
			return err
		}
		var applyLinks map[string]string
		if ifaceChanged {
			applyLinks = ext.GroupLinks
		}
		res, applyErr := op.applyGroupsWithPrevLocked(ctx, updated, prevGroups, running, applyLinks)
		if applyErr != nil {
			if ifaceChanged && prevExt != nil && res == nil && !errors.Is(applyErr, ErrRecoveryRequired) {
				op.restoreProxyGroups(prevExt.ProxyGroups)
			}
			return applyErr
		}
		groups = res
	} else if err := op.refreshGroupLinks(ext); err != nil {
		return err
	}
	wantKeenDNS := extWantsKeenDNS(ext)
	if wantKeenDNS {
		if op.IsRunning(ctx) && !op.ManuallyDisabled() && isCloudIpsetRuntimeEnabled(op) {
			_ = op.syncKeenDNSCloudWithGen(ctx, true, cfgGen, true)
		}
	} else {
		op.abortKeenDNSCloudSync()
	}
	return op.reconcileExtNetfilterStrictForCfgGen(ctx, ext, groups, cfgGen)
}

func (op *Operator) reconcileExtOnStartLocked(ctx context.Context) error {
	if op.ManuallyDisabled() {
		return nil
	}
	op.migrateGroupLinks()
	ext, cfgGen := op.loadExtConfigWithGen()
	var groups []Group
	var err error
	if op.IsRunning(ctx) {
		groups, err = op.Client().GetGroups(ctx, true)
	} else {
		groups, err = op.LoadGroupsFile()
	}
	if err != nil {
		groups, _ = op.LoadGroupsFile()
	}
	updated, changed := op.syncGroupsWithExt(ctx, groups, ext)
	if changed {
		if synced, syncErr := op.SyncGroups(ctx, updated); syncErr != nil {
			return syncErr
		} else {
			groups = synced
			_ = op.SaveGroupsFile(groups)
		}
	}
	_ = op.refreshGroupLinks(ext)
	wantKeenDNS := extWantsKeenDNS(ext)
	if wantKeenDNS {
		if op.IsRunning(ctx) && !op.ManuallyDisabled() && isCloudIpsetRuntimeEnabled(op) {
			_ = op.syncKeenDNSCloudWithGen(ctx, true, cfgGen, true)
		}
	} else {
		op.abortKeenDNSCloudSync()
	}
	return op.reconcileExtNetfilterStrictForCfgGen(ctx, ext, groups, cfgGen)
}

func runIptables(ctx context.Context, args ...string) error {
	fullArgs := append([]string{"-w"}, args...)
	out, err := runExtIptablesOutput(ctx, fullArgs...)
	if err != nil {
		// Creating an already-existing chain with -N is benign.
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "-N" && strings.Contains(strings.ToLower(string(out)), "already exists") {
				return nil
			}
		}
		return fmt.Errorf("iptables %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runExtIptablesOutput(ctx context.Context, args ...string) ([]byte, error) {
	extIptablesMu.RLock()
	fn := extIptablesRunner
	extIptablesMu.RUnlock()
	return fn(ctx, args...)
}

func hasCustomExtIptablesRunner() bool {
	extIptablesMu.RLock()
	custom := extIptablesCustom
	extIptablesMu.RUnlock()
	return custom
}

func defaultExtIptablesRunner(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "iptables", args...).CombinedOutput()
}

// SetExtIptablesRunner overrides the iptables command runner used by FIRC netfilter reconciliation (for tests).
func SetExtIptablesRunner(fn func(ctx context.Context, args ...string) ([]byte, error)) {
	extIptablesMu.Lock()
	if fn == nil {
		extIptablesRunner = defaultExtIptablesRunner
		extIptablesCustom = false
	} else {
		extIptablesRunner = fn
		extIptablesCustom = true
	}
	extIptablesMu.Unlock()
}

func (op *Operator) ensureExtWatchdog() {
	op.startExtWatchdog(3 * time.Second)
}

func (op *Operator) extWatchdogContext() context.Context {
	op.mu.Lock()
	ctx := op.extLoopCtx
	op.mu.Unlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (op *Operator) cancelExtWatchdog() <-chan struct{} {
	op.mu.Lock()
	cancel := op.extLoopCancel
	done := op.extLoopDone
	op.extLoopStarted = false
	op.extLoopCtx = nil
	op.extLoopCancel = nil
	op.extLoopDone = nil
	op.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return done
}

func (op *Operator) startExtWatchdog(interval time.Duration) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	op.mu.Lock()
	if op.extLoopStarted {
		op.mu.Unlock()
		return
	}
	loopCtx, loopCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	op.extLoopStarted = true
	op.extLoopCtx = loopCtx
	op.extLoopCancel = loopCancel
	op.extLoopDone = done
	op.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				op.runExtWatchdogTick(loopCtx)
			}
		}
	}()
}

func (op *Operator) runExtWatchdogTick(loopCtx context.Context) {
	if loopCtx == nil {
		loopCtx = context.Background()
	}
	if loopCtx.Err() != nil || op.ManuallyDisabled() {
		return
	}
	ctx, cancel := context.WithTimeout(loopCtx, 10*time.Second)
	defer cancel()
	op.releaseExtForForeign(ctx)
	running := op.IsRunning(ctx)
	// Отменённый такт (остановка сторожа) — не наблюдение: «не запущен»
	// здесь значит лишь, что пинг прервали.
	if loopCtx.Err() == nil {
		kdErr, _ := op.keenDNSCloudStatus()
		op.noteRuntimeState(running, kdErr)
	}
	if !running || op.ManuallyDisabled() {
		return
	}

	nfGen := extNetfilterGen.Load()
	ext, cfgGen := op.loadExtConfigWithGen()
	wantKeenDNS := extWantsKeenDNS(ext)
	if wantKeenDNS && isCloudIpsetRuntimeEnabled(op) {
		_ = op.syncKeenDNSCloudWithGen(ctx, false, cfgGen, true)
	} else if !wantKeenDNS && op.currentExtConfigGen() == cfgGen {
		op.abortKeenDNSCloudSync()
	}
	if ctx.Err() != nil || op.ManuallyDisabled() || op.currentExtConfigGen() != cfgGen || extNetfilterGen.Load() != nfGen {
		return
	}

	groups, err := op.Client().GetGroups(ctx, false)
	groupsLive := err == nil
	if err != nil {
		groups, _ = op.LoadGroupsFile()
	}
	if ctx.Err() != nil || op.ManuallyDisabled() || op.currentExtConfigGen() != cfgGen || extNetfilterGen.Load() != nfGen {
		return
	}
	_ = op.reconcileExtNetfilterLockedWithCfgGen(ctx, ext, groups, nfGen, cfgGen, true, false)
	// Итог синхронизации KeenDNS этого такта — тоже наблюдение.
	kdErr, _ := op.keenDNSCloudStatus()
	op.noteRuntimeState(true, kdErr)
	if !wantKeenDNS || groupsLive {
		op.maybeResetCloudAgentOnKeenDNSSwitch(ctx, wantKeenDNS, groups)
	}
}

// releaseExtForForeign снимает свои цепочки FEXT_* и хук, когда под работающим
// движком появился сторонний firc: они ссылались на наш рантайм, а действуют уже
// на чужой. Один раз и с подтверждением — на время opkg upgrade рантайм
// ненадолго остаётся без списка файлов. Сторож не гасим: уйдёт сторонний —
// обычный такт вернёт правила.
func (op *Operator) releaseExtForForeign(ctx context.Context) {
	if !op.foreignPresent() {
		op.foreignTicks.Store(0)
		return
	}
	if op.foreignTicks.Add(1) != runtimeDownConfirmations {
		return
	}
	extNetfilterGen.Add(1)
	op.netfilterMu.Lock()
	defer op.netfilterMu.Unlock()
	if op.isSystemConfigDir() {
		cleanupExtNetfilter(ctx, "")
	} else if op.managesKernelNetfilter() {
		cleanupExtNetfilterRules(ctx)
	}
}

func (op *Operator) resetKeeneticCloudAgent(ctx context.Context) (int, error) {
	if op.resetCloudAgentFn != nil {
		return op.resetCloudAgentFn(ctx)
	}
	if !hasCustomExtIptablesRunner() && !hasCustomExtIpsetRunner() && runtime.GOOS == "linux" && op.isSystemConfigDir() {
		return cloudagent.Reset(ctx)
	}
	return 0, nil
}

// maybeResetCloudAgentOnKeenDNSSwitch detects when fircd switches the active
// interface for group c100d001 and resets coalagent so cloud connections drop
// stale NAT/UDP state immediately, using a deferred 30s cooldown.
func (op *Operator) maybeResetCloudAgentOnKeenDNSSwitch(ctx context.Context, wantKeenDNS bool, groups []Group) {
	if op == nil {
		return
	}
	if !wantKeenDNS {
		op.cloudStateMu.Lock()
		op.keenDNSSyncedActiveIface = ""
		op.cloudStateMu.Unlock()
		return
	}
	active := ""
	for _, g := range groups {
		if g.ID == KeenDNSGroupID {
			active = strings.TrimSpace(g.Active)
			break
		}
	}
	if active == "" {
		return
	}

	op.cloudStateMu.Lock()
	if op.keenDNSSyncedActiveIface == "" {
		op.keenDNSSyncedActiveIface = active
		op.cloudStateMu.Unlock()
		return
	}
	if op.keenDNSSyncedActiveIface == active {
		op.cloudStateMu.Unlock()
		return
	}
	now := cloudNow()
	if !op.keenDNSLastAgentResetAt.IsZero() && now.Sub(op.keenDNSLastAgentResetAt) < fircCloudAgentResetCooldown {
		op.cloudStateMu.Unlock()
		return
	}
	prev := op.keenDNSSyncedActiveIface
	op.keenDNSSyncedActiveIface = active
	op.keenDNSLastAgentResetAt = now
	op.cloudStateMu.Unlock()

	n, err := op.resetKeeneticCloudAgent(ctx)
	if err != nil {
		if op.log != nil {
			op.log.Warn("firc keendns cloud agent reset failed", "from", prev, "to", active, "error", err)
		}
		return
	}
	if op.log != nil {
		op.log.Info("firc keendns active interface switched; signaled coalagent", "from", prev, "to", active, "signaled", n)
	}
}

const (
	ndmFircExtHookPath   = "/opt/etc/ndm/netfilter.d/66-awgm-firc-ext.sh"
	cutoverGuardHookPath = "/opt/etc/ndm/netfilter.d/05-awgm-cutover-guard.sh"
	cutoverGuardChain    = "AWGM_CUTOVER_GUARD"
	cutoverGuardPreChain = "AWGM_CUTOVER_GUARD_PRE"
	cutoverGuardFwdChain = "AWGM_CUTOVER_GUARD_FWD"
)

const cutoverGuardHookScript = `#!/bin/sh
IPT=/opt/sbin/iptables
[ -x "$IPT" ] || IPT=iptables
if [ "$type" = "ip6tables" ]; then
  IPT=/opt/sbin/ip6tables
  [ -x "$IPT" ] || IPT=ip6tables
fi
command -v "$IPT" >/dev/null 2>&1 || exit 0
case "$table" in
filter)
  "$IPT" -w -t filter -N AWGM_CUTOVER_GUARD_FWD 2>/dev/null || true
  if ! "$IPT" -w -t filter -S AWGM_CUTOVER_GUARD_FWD 2>/dev/null | grep -q -- '-j DROP'; then
    "$IPT" -w -t filter -F AWGM_CUTOVER_GUARD_FWD 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -i br+ -o br+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -o tunvless+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -i tunvless+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -o opkgtun+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -i opkgtun+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -i br+ -o nwg+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -i nwg+ -o br+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -p tcp -j REJECT --reject-with tcp-reset 2>/dev/null || true
    "$IPT" -w -t filter -A AWGM_CUTOVER_GUARD_FWD -j DROP 2>/dev/null || true
  fi
  "$IPT" -w -t filter -C FORWARD -j AWGM_CUTOVER_GUARD_FWD 2>/dev/null || \
    "$IPT" -w -t filter -I FORWARD 1 -j AWGM_CUTOVER_GUARD_FWD 2>/dev/null || true
  ;;
mangle)
  "$IPT" -w -t mangle -N AWGM_CUTOVER_GUARD_PRE 2>/dev/null || true
  if ! "$IPT" -w -t mangle -S AWGM_CUTOVER_GUARD_PRE 2>/dev/null | grep -q -- '--dport 53 -j DROP'; then
    "$IPT" -w -t mangle -F AWGM_CUTOVER_GUARD_PRE 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD_PRE -i br+ -p udp -m udp --dport 53 -j DROP 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD_PRE -i br+ -p tcp -m tcp --dport 53 -j DROP 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD_PRE -i nwg+ -p udp -m udp --dport 53 -j DROP 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD_PRE -i nwg+ -p tcp -m tcp --dport 53 -j DROP 2>/dev/null || true
  fi
  "$IPT" -w -t mangle -C PREROUTING -j AWGM_CUTOVER_GUARD_PRE 2>/dev/null || \
    "$IPT" -w -t mangle -I PREROUTING 1 -j AWGM_CUTOVER_GUARD_PRE 2>/dev/null || true

  "$IPT" -w -t mangle -N AWGM_CUTOVER_GUARD 2>/dev/null || true
  if ! "$IPT" -w -t mangle -S AWGM_CUTOVER_GUARD 2>/dev/null | grep -q -- '-j DROP'; then
    "$IPT" -w -t mangle -F AWGM_CUTOVER_GUARD 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -i br+ -o br+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -o tunvless+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -i tunvless+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -o opkgtun+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -i opkgtun+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -i br+ -o nwg+ -j RETURN 2>/dev/null || true
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -i nwg+ -o br+ -j RETURN 2>/dev/null || true
    if "$IPT" -w -t filter -C FORWARD -j AWGM_CUTOVER_GUARD_FWD >/dev/null 2>&1; then
      "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -p tcp -j RETURN 2>/dev/null || true
    fi
    "$IPT" -w -t mangle -A AWGM_CUTOVER_GUARD -j DROP 2>/dev/null || true
  fi
  "$IPT" -w -t mangle -C FORWARD -j AWGM_CUTOVER_GUARD 2>/dev/null || \
    "$IPT" -w -t mangle -I FORWARD 1 -j AWGM_CUTOVER_GUARD 2>/dev/null || true
  ;;
esac
exit 0
`

const ndmFircExtHookScript = `#!/bin/sh
[ "$type" = "ip6tables" ] && exit 0
IPT=/opt/sbin/iptables
[ -x "$IPT" ] || IPT=iptables
case "$table" in
filter)
  "$IPT" -w -t filter -C FORWARD -o tunvless+ -p udp -m udp --dport 443 -j REJECT --reject-with icmp-port-unreachable 2>/dev/null || "$IPT" -w -t filter -I FORWARD 1 -o tunvless+ -p udp -m udp --dport 443 -j REJECT --reject-with icmp-port-unreachable 2>/dev/null
  "$IPT" -w -t filter -C OUTPUT -o tunvless+ -p udp -m udp --dport 443 -j REJECT --reject-with icmp-port-unreachable 2>/dev/null || "$IPT" -w -t filter -I OUTPUT 1 -o tunvless+ -p udp -m udp --dport 443 -j REJECT --reject-with icmp-port-unreachable 2>/dev/null
  ;;
mangle)
  if "$IPT" -w -t mangle -nL FEXT_MSS >/dev/null 2>&1; then
    "$IPT" -w -t mangle -C FORWARD -j FEXT_MSS 2>/dev/null || "$IPT" -w -t mangle -I FORWARD 1 -j FEXT_MSS 2>/dev/null
  fi
  if "$IPT" -w -t mangle -nL FEXT_OUT >/dev/null 2>&1; then
    "$IPT" -w -t mangle -C OUTPUT -j FEXT_OUT 2>/dev/null || "$IPT" -w -t mangle -A OUTPUT -j FEXT_OUT 2>/dev/null
  fi
  ;;
nat)
  if "$IPT" -w -t nat -nL FEXT_DNS >/dev/null 2>&1; then
    while "$IPT" -w -t nat -D PREROUTING -j FEXT_DNS 2>/dev/null; do :; done
    "$IPT" -w -t nat -I PREROUTING 1 -j FEXT_DNS 2>/dev/null
  fi
  if "$IPT" -w -t nat -nL FEXT_NOUT >/dev/null 2>&1; then
    "$IPT" -w -t nat -C OUTPUT -j FEXT_NOUT 2>/dev/null || "$IPT" -w -t nat -A OUTPUT -j FEXT_NOUT 2>/dev/null
  fi
  ;;
esac
exit 0
`

func ensureInitScriptSparesDisabled(_ string) {
	// Never rewrite package-owned /opt/etc/init.d/S99firc on disk; mutating an
	// installed runtime file invalidates byte-level artifact verification.
}

func cleanupExtNetfilter(ctx context.Context, rootDir string) {
	hookPath := ndmFircExtHookPath
	if rootDir != "" && rootDir != "/" {
		hookPath = filepath.Join(rootDir, strings.TrimPrefix(ndmFircExtHookPath, "/"))
	}
	_ = os.Remove(hookPath)

	if (rootDir != "" && rootDir != "/") || (runtime.GOOS != "linux" && !hasCustomExtIptablesRunner()) {
		return
	}
	cleanupExtNetfilterRules(ctx)
}

func cleanupExtNetfilterRules(ctx context.Context) {
	rule := []string{"-o", "tunvless+", "-p", "udp", "-m", "udp", "--dport", "443", "-j", "REJECT", "--reject-with", "icmp-port-unreachable"}
	for _, chain := range []string{"FORWARD", "OUTPUT"} {
		for range 8 {
			if err := runIptables(ctx, append([]string{"-t", "filter", "-D", chain}, rule...)...); err != nil {
				break
			}
		}
	}
	for _, h := range []struct{ table, parent, chain string }{
		{"nat", "PREROUTING", "FEXT_DNS"},
		{"nat", "OUTPUT", "FEXT_NOUT"},
		{"mangle", "OUTPUT", "FEXT_OUT"},
		{"mangle", "FORWARD", "FEXT_MSS"},
	} {
		for range 8 {
			if err := runIptables(ctx, "-t", h.table, "-D", h.parent, "-j", h.chain); err != nil {
				break
			}
		}
		_ = runIptables(ctx, "-t", h.table, "-F", h.chain)
		_ = runIptables(ctx, "-t", h.table, "-X", h.chain)
	}
	for _, h := range []struct{ table, chain string }{
		{"nat", "FEXT_REDIR"},
		{"nat", "FEXT_DNAT"},
	} {
		_ = runIptables(ctx, "-t", h.table, "-F", h.chain)
		_ = runIptables(ctx, "-t", h.table, "-X", h.chain)
	}
	_ = teardownKeenDNSCloudChainAndSet(ctx)
	for _, table := range []string{"mangle", "nat"} {
		out, err := runExtIptablesOutput(ctx, "-w", "-t", table, "-S")
		if err != nil {
			continue
		}
		fextChains := make(map[string]bool)
		var orderedChains []string
		addChain := func(name string) {
			if strings.HasPrefix(name, "FEXT_") && !fextChains[name] {
				fextChains[name] = true
				orderedChains = append(orderedChains, name)
			}
		}
		var parentJumps [][]string
		for _, rawLine := range strings.Split(string(out), "\n") {
			line := strings.TrimSpace(rawLine)
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "-N":
				addChain(fields[1])
			case "-A":
				parent := fields[1]
				addChain(parent)
				for i := 2; i < len(fields)-1; i++ {
					if (fields[i] == "-j" || fields[i] == "-g") && strings.HasPrefix(fields[i+1], "FEXT_") {
						addChain(fields[i+1])
						if !strings.HasPrefix(parent, "FEXT_") {
							delArgs := append([]string{"-t", table, "-D", parent}, fields[2:]...)
							parentJumps = append(parentJumps, delArgs)
						}
					}
				}
			}
		}
		for _, delArgs := range parentJumps {
			_ = runIptables(ctx, delArgs...)
		}
		// Flush all FEXT_* chains first so inter-FEXT references (e.g. FEXT_OUT -> FEXT_<group>)
		// are cleared before deleting chains with -X.
		for _, ch := range orderedChains {
			_ = runIptables(ctx, "-t", table, "-F", ch)
		}
		for _, ch := range orderedChains {
			_ = runIptables(ctx, "-t", table, "-X", ch)
		}
	}
	InvalidateOutputRoutingCache()
}

// cleanupOrphanFircIpsets destroys leftover FIRC_* ipsets after fircd stops
// (e.g. FIRC_<group>S4/S6 that stayed kernel-referenced during shutdown).
func cleanupOrphanFircIpsets(ctx context.Context) {
	if !isCloudIpsetDestroyEnabled() {
		return
	}
	pkgCloudIpsetMu.Lock()
	defer pkgCloudIpsetMu.Unlock()
	out, err := runExtIpset(ctx, "", "list", "-n")
	if err != nil {
		return
	}
	for _, rawLine := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(name, "FIRC_") {
			continue
		}
		_, _ = runExtIpset(ctx, "", "destroy", name)
	}
}

func cutoverGuardBypassRules() [][]string {
	return [][]string{
		{"-i", "br+", "-o", "br+", "-j", "RETURN"},
		{"-o", "tunvless+", "-j", "RETURN"},
		{"-i", "tunvless+", "-j", "RETURN"},
		{"-o", "opkgtun+", "-j", "RETURN"},
		{"-i", "opkgtun+", "-j", "RETURN"},
		{"-i", "br+", "-o", "nwg+", "-j", "RETURN"},
		{"-i", "nwg+", "-o", "br+", "-j", "RETURN"},
	}
}

func cutoverGuardRules() [][]string {
	return append(cutoverGuardBypassRules(), []string{"-j", "DROP"})
}

func cutoverGuardMangleRules(allowTCPToFilter bool) [][]string {
	rules := cutoverGuardBypassRules()
	if allowTCPToFilter {
		rules = append(rules, []string{"-p", "tcp", "-j", "RETURN"})
	}
	return append(rules, []string{"-j", "DROP"})
}

func cutoverGuardFwdRules() [][]string {
	return append(cutoverGuardBypassRules(),
		[]string{"-p", "tcp", "-j", "REJECT", "--reject-with", "tcp-reset"},
		[]string{"-j", "DROP"},
	)
}

func cutoverGuardPreRules() [][]string {
	return [][]string{
		{"-i", "br+", "-p", "udp", "-m", "udp", "--dport", "53", "-j", "DROP"},
		{"-i", "br+", "-p", "tcp", "-m", "tcp", "--dport", "53", "-j", "DROP"},
		{"-i", "nwg+", "-p", "udp", "-m", "udp", "--dport", "53", "-j", "DROP"},
		{"-i", "nwg+", "-p", "tcp", "-m", "tcp", "--dport", "53", "-j", "DROP"},
	}
}

func isTransientChainFlushError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no chain/target/match") ||
		strings.Contains(msg, "couldn't load target") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "missing after reconcile") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "doesn't exist") ||
		strings.Contains(msg, "resource temporarily unavailable")
}

func ensureCutoverChain(ctx context.Context, table, parent, chain string, specs [][]string, ipv6CheckArgs []string) error {
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		lastErr = ensureCutoverChainOnce(ctx, table, parent, chain, specs, ipv6CheckArgs)
		if lastErr == nil || !isTransientChainFlushError(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

func ensureCutoverChainOnce(ctx context.Context, table, parent, chain string, specs [][]string, ipv6CheckArgs []string) error {
	if err := runIptables(ctx, "-t", table, "-N", chain); err != nil {
		return err
	}
	expected := make([]string, len(specs))
	for i, spec := range specs {
		expected[i] = strings.Join(spec, " ")
	}
	curRules, curErr := readChainRuleList(ctx, table, chain)
	if curErr != nil || !slices.Equal(curRules, expected) {
		if len(curRules) == 0 {
			if err := runIptables(ctx, "-t", table, "-F", chain); err != nil {
				return err
			}
			for _, spec := range specs {
				if err := runIptables(ctx, append([]string{"-t", table, "-A", chain}, spec...)...); err != nil {
					return err
				}
			}
		} else {
			// Append replacement rules ending with terminal action before deleting
			// old head rules so an active cutover guard is never flushed or fail-open.
			for _, spec := range specs {
				if err := runIptables(ctx, append([]string{"-t", table, "-A", chain}, spec...)...); err != nil {
					return err
				}
			}
			for range len(curRules) {
				if err := runIptables(ctx, "-t", table, "-D", chain, "1"); err != nil {
					return err
				}
			}
		}
	}

	// Ensure a single jump sits at parent index 1 without ever removing an active jump first.
	parentRules, listErr := readChainRuleList(ctx, table, parent)
	if listErr != nil || len(parentRules) == 0 || parentRules[0] != "-j "+chain {
		if err := runIptables(ctx, "-t", table, "-I", parent, "1", "-j", chain); err != nil {
			return err
		}
		if updated, rErr := readChainRuleList(ctx, table, parent); rErr == nil {
			parentRules = updated
		}
	}
	for i := len(parentRules) - 1; i >= 1; i-- {
		if parentRules[i] == "-j "+chain {
			_ = runIptables(ctx, "-t", table, "-D", parent, strconv.Itoa(i+1))
		}
	}

	if runtime.GOOS == "linux" && !hasCustomExtIptablesRunner() && len(ipv6CheckArgs) > 0 {
		_ = runBestEffortIP6Tables(ctx, "-t", table, "-N", chain)
		if runBestEffortIP6Tables(ctx, append([]string{"-t", table, "-C", chain}, ipv6CheckArgs...)...) != nil {
			_ = runBestEffortIP6Tables(ctx, "-t", table, "-F", chain)
			for _, spec := range specs {
				_ = runBestEffortIP6Tables(ctx, append([]string{"-t", table, "-A", chain}, spec...)...)
			}
		}
		if runBestEffortIP6Tables(ctx, "-t", table, "-C", parent, "-j", chain) != nil {
			_ = runBestEffortIP6Tables(ctx, "-t", table, "-I", parent, "1", "-j", chain)
		}
	}
	return nil
}

func releaseCutoverChain(ctx context.Context, table, parent, chain string) {
	for range 16 {
		if err := runIptables(ctx, "-t", table, "-D", parent, "-j", chain); err != nil {
			break
		}
	}
	_ = runIptables(ctx, "-t", table, "-F", chain)
	_ = runIptables(ctx, "-t", table, "-X", chain)
	if runtime.GOOS == "linux" && !hasCustomExtIptablesRunner() {
		for range 16 {
			if runBestEffortIP6Tables(ctx, "-t", table, "-D", parent, "-j", chain) != nil {
				break
			}
		}
		_ = runBestEffortIP6Tables(ctx, "-t", table, "-F", chain)
		_ = runBestEffortIP6Tables(ctx, "-t", table, "-X", chain)
	}
}

func runBestEffortIP6Tables(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath("ip6tables")
	if err != nil {
		if _, sErr := os.Stat("/opt/sbin/ip6tables"); sErr == nil {
			bin = "/opt/sbin/ip6tables"
		} else {
			return err
		}
	}
	return exec.CommandContext(ctx, bin, append([]string{"-w"}, args...)...).Run()
}

func (op *Operator) releaseCutoverBarrierChainsLocked(ctx context.Context) {
	if op.isSystemConfigDir() {
		_ = os.Remove(cutoverGuardHookPath)
	}
	releaseCutoverChain(ctx, "mangle", "FORWARD", cutoverGuardChain)
	releaseCutoverChain(ctx, "filter", "FORWARD", cutoverGuardFwdChain)
	releaseCutoverChain(ctx, "mangle", "PREROUTING", cutoverGuardPreChain)
}

// EngageCutoverBarrier installs fail-closed cutover barriers:
//   - filter FORWARD (AWGM_CUTOVER_GUARD_FWD): emits immediate TCP RST for
//     unrouted TCP and drops other fallback traffic so TCP retries do not outlive cutover;
//   - mangle FORWARD (AWGM_CUTOVER_GUARD): passes TCP down to filter FORWARD when
//     AWGM_CUTOVER_GUARD_FWD is armed and drops non-TCP (or all fallback if filter failed);
//   - mangle PREROUTING (AWGM_CUTOVER_GUARD_PRE): drops LAN UDP/TCP port 53 queries so
//     clients cannot learn real WAN IPs (sing-box -> firc) or stale FakeIPs mid-cutover.
func (op *Operator) EngageCutoverBarrier(ctx context.Context) error {
	if !op.shouldManageKernelNetfilter() {
		return nil
	}
	op.netfilterMu.Lock()
	defer op.netfilterMu.Unlock()

	if op.isSystemConfigDir() {
		dir := filepath.Dir(cutoverGuardHookPath)
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			if cur, err := os.ReadFile(cutoverGuardHookPath); err != nil || string(cur) != cutoverGuardHookScript {
				_ = storage.AtomicWritePerm(cutoverGuardHookPath, []byte(cutoverGuardHookScript), 0o755)
			}
		}
	}

	filterFwdReady := true
	if err := ensureCutoverChain(ctx, "filter", "FORWARD", cutoverGuardFwdChain, cutoverGuardFwdRules(), []string{"-j", "DROP"}); err != nil {
		releaseCutoverChain(ctx, "filter", "FORWARD", cutoverGuardFwdChain)
		filterFwdReady = false
	}
	if err := ensureCutoverChain(ctx, "mangle", "FORWARD", cutoverGuardChain, cutoverGuardMangleRules(filterFwdReady), []string{"-j", "DROP"}); err != nil {
		op.releaseCutoverBarrierChainsLocked(ctx)
		return err
	}
	if err := ensureCutoverChain(ctx, "mangle", "PREROUTING", cutoverGuardPreChain, cutoverGuardPreRules(), cutoverGuardPreRules()[0]); err != nil {
		op.releaseCutoverBarrierChainsLocked(ctx)
		return err
	}
	return nil
}

// ReleaseCutoverBarrier removes the fail-closed cutover barriers and the NDMS
// netfilter hook once target or fallback routing is verified ready.
func (op *Operator) ReleaseCutoverBarrier(ctx context.Context) error {
	if op.isSystemConfigDir() {
		_ = os.Remove(cutoverGuardHookPath)
	}
	if !op.managesKernelNetfilter() {
		return nil
	}
	op.netfilterMu.Lock()
	defer op.netfilterMu.Unlock()

	op.releaseCutoverBarrierChainsLocked(ctx)
	return nil
}

func cleanupPoolRejectBarrierRules(ctx context.Context) {
	for range 16 {
		if err := runIptables(ctx, "-t", "filter", "-D", "FORWARD", "-j", "FIRC_POOLREJECT"); err != nil {
			break
		}
	}
	_ = runIptables(ctx, "-t", "filter", "-F", "FIRC_POOLREJECT")
	_ = runIptables(ctx, "-t", "filter", "-X", "FIRC_POOLREJECT")
	if runtime.GOOS == "linux" && !hasCustomExtIptablesRunner() {
		for range 16 {
			if runBestEffortIP6Tables(ctx, "-t", "filter", "-D", "FORWARD", "-j", "FIRC_POOLREJECT") != nil {
				break
			}
		}
		_ = runBestEffortIP6Tables(ctx, "-t", "filter", "-F", "FIRC_POOLREJECT")
		_ = runBestEffortIP6Tables(ctx, "-t", "filter", "-X", "FIRC_POOLREJECT")
	}
}

// CleanupPoolRejectBarrier removes any lingering FIRC_POOLREJECT filter barrier when
// ownership leaves FIRC (engine switch, rollback of candidate FIRC, unselected stop, or uninstall).
func (op *Operator) CleanupPoolRejectBarrier(ctx context.Context) {
	if !op.shouldManageKernelNetfilter() {
		return
	}
	op.netfilterMu.Lock()
	defer op.netfilterMu.Unlock()
	cleanupPoolRejectBarrierRules(ctx)
}

func cleanupPoolRejectBarrierForRoot(ctx context.Context, rootDir string) {
	if (rootDir != "" && rootDir != "/") || (runtime.GOOS != "linux" && !hasCustomExtIptablesRunner()) {
		return
	}
	cleanupPoolRejectBarrierRules(ctx)
}

func insertChainRuleRespectingCutoverGuard(ctx context.Context, table, chain, guardChain string, ruleArgs ...string) error {
	pos := "1"
	if guardChain != "" {
		if cur, err := readChainRuleList(ctx, table, chain); err == nil && len(cur) > 0 && cur[0] == "-j "+guardChain {
			pos = "2"
		}
	}
	return runIptables(ctx, append([]string{"-t", table, "-I", chain, pos}, ruleArgs...)...)
}

func reconcileTunvlessQUICReject(ctx context.Context) error {
	var errs []error
	rule := []string{"-o", "tunvless+", "-p", "udp", "-m", "udp", "--dport", "443", "-j", "REJECT", "--reject-with", "icmp-port-unreachable"}
	for _, chain := range []string{"FORWARD", "OUTPUT"} {
		exists, err := checkIptablesRuleExists(ctx, "filter", chain, rule...)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !exists {
			guard := ""
			if chain == "FORWARD" {
				guard = cutoverGuardFwdChain
			}
			if iErr := insertChainRuleRespectingCutoverGuard(ctx, "filter", chain, guard, rule...); iErr != nil {
				errs = append(errs, iErr)
			}
		}
	}
	return errors.Join(errs...)
}

func reconcileNDMNetfilterHook(needHook bool) {
	if !needHook {
		_ = os.Remove(ndmFircExtHookPath)
		return
	}
	dir := filepath.Dir(ndmFircExtHookPath)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return
	}
	if cur, err := os.ReadFile(ndmFircExtHookPath); err == nil && string(cur) == ndmFircExtHookScript {
		return
	}
	_ = storage.AtomicWritePerm(ndmFircExtHookPath, []byte(ndmFircExtHookScript), 0o755)
}

// ReconcileExtNetfilter ensures iptables hooks in nat FEXT_DNS, mangle FEXT_OUT, and nat FEXT_NOUT
// match the active ExtConfig whenever fircd is running.
func (op *Operator) ReconcileExtNetfilter(ctx context.Context, ext ExtConfig, groups []Group) {
	if op.ManuallyDisabled() {
		return
	}
	myGen := extNetfilterGen.Add(1)
	if !op.IsRunning(ctx) {
		return
	}
	_ = op.reconcileExtNetfilterLockedWithGen(ctx, ext, groups, myGen, false)
	baseCtx := op.extWatchdogContext()
	go func(expectedGen uint64, snapExt ExtConfig, snapGroups []Group) {
		timer := time.NewTimer(1200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-baseCtx.Done():
			return
		case <-timer.C:
		}
		if extNetfilterGen.Load() != expectedGen || op.ManuallyDisabled() {
			return
		}
		bgCtx, cancel := context.WithTimeout(baseCtx, 10*time.Second)
		defer cancel()
		if !op.IsRunning(bgCtx) || op.ManuallyDisabled() {
			return
		}
		_ = op.reconcileExtNetfilterLockedWithGen(bgCtx, snapExt, snapGroups, expectedGen, false)
	}(myGen, ext, groups)
}

// ReconcileExtNetfilterStrict synchronously reconciles and verifies FIRC netfilter chains and hooks,
// invalidating any pending asynchronous reconciliation pass and returning any iptables/verification error.
func (op *Operator) ReconcileExtNetfilterStrict(ctx context.Context, ext ExtConfig, groups []Group) error {
	return op.reconcileExtNetfilterStrictImpl(ctx, ext, groups, 0, false)
}

func (op *Operator) reconcileExtNetfilterStrictForCfgGen(ctx context.Context, ext ExtConfig, groups []Group, expectedCfgGen uint64) error {
	return op.reconcileExtNetfilterStrictImpl(ctx, ext, groups, expectedCfgGen, true)
}

func (op *Operator) reconcileExtNetfilterStrictImpl(ctx context.Context, ext ExtConfig, groups []Group, expectedCfgGen uint64, enforceCfgGen bool) error {
	if op.ManuallyDisabled() {
		return nil
	}
	myGen := extNetfilterGen.Add(1)
	if !op.IsRunning(ctx) {
		return nil
	}
	op.mu.Lock()
	hook := op.netfilterStrictHook
	op.mu.Unlock()
	if hook != nil {
		return hook(ctx, ext, cloneGroups(groups))
	}
	return op.reconcileExtNetfilterLockedWithCfgGen(ctx, ext, groups, myGen, expectedCfgGen, enforceCfgGen, true)
}

func (op *Operator) reconcileExtNetfilterLocked(ctx context.Context, ext ExtConfig, groups []Group) {
	_ = op.reconcileExtNetfilterLockedWithCfgGen(ctx, ext, groups, 0, 0, false, false)
}

func (op *Operator) reconcileExtNetfilterLockedWithGen(ctx context.Context, ext ExtConfig, groups []Group, expectedGen uint64, strict bool) error {
	return op.reconcileExtNetfilterLockedWithCfgGen(ctx, ext, groups, expectedGen, 0, false, strict)
}

func (op *Operator) reconcileExtNetfilterLockedWithCfgGen(ctx context.Context, ext ExtConfig, groups []Group, expectedGen, expectedCfgGen uint64, enforceCfgGen, strict bool) error {
	var lastRetErr error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			// Invalidate mirror chain signatures outside netfilterMu/mirrorNetfilterMu
			// so a retry rebuilds everything cleanly after an NDM firewall flush.
			InvalidateOutputRoutingCache()
			delay := 150 * time.Millisecond
			if hasCustomExtIptablesRunner() {
				delay = 20 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				if strict {
					return errors.Join(lastRetErr, ctx.Err())
				}
				return nil
			case <-timer.C:
			}
		}
		retErr, transientErr := op.reconcileExtNetfilterOnceLockedWithCfgGen(ctx, ext, groups, expectedGen, expectedCfgGen, enforceCfgGen, strict)
		lastRetErr = retErr
		if transientErr == nil || !isTransientChainFlushError(transientErr) {
			return retErr
		}
	}
	return lastRetErr
}

func (op *Operator) reconcileExtNetfilterOnceLockedWithCfgGen(ctx context.Context, ext ExtConfig, groups []Group, expectedGen, expectedCfgGen uint64, enforceCfgGen, strict bool) (error, error) {
	if !op.shouldManageKernelNetfilter() {
		return nil, nil
	}
	op.netfilterMu.Lock()
	defer op.netfilterMu.Unlock()
	mirrorNetfilterMu.Lock()
	defer mirrorNetfilterMu.Unlock()
	if op.ManuallyDisabled() {
		return nil, nil
	}

	if expectedGen != 0 && extNetfilterGen.Load() != expectedGen {
		return nil, nil
	}
	if enforceCfgGen && op.currentExtConfigGen() != expectedCfgGen {
		return nil, nil
	}
	var errs []error
	if err := reconcileTunvlessQUICReject(ctx); err != nil {
		errs = append(errs, err)
	}

	ifaces := op.activeServerInterfaces(ctx, ext.ServerRouting)
	if op.isSystemConfigDir() {
		reconcileNDMNetfilterHook(true)
	}

	// 1. Reconcile DNS redirection in nat FEXT_DNS and TCPMSS clamping in mangle FEXT_MSS for inbound VPN server interfaces.
	if err := reconcileServerDNSRedirect(ctx, ifaces); err != nil {
		errs = append(errs, err)
	}
	if err := reconcileServerMSSClamp(ctx, ifaces); err != nil {
		errs = append(errs, err)
	}

	// 2. Ensure interface-based matching in FIRC_<id>D for groups with restricted device allowlists.
	if err := reconcileServerDeviceChains(ctx, ifaces, groups); err != nil {
		errs = append(errs, err)
	}

	// 3. Reconcile router OUTPUT routing in mangle FEXT_OUT & nat FEXT_NOUT for KeenDNS Cloud and VLESS XHTTP server.
	if err := op.reconcileOutputRouting(ctx, ext, groups); err != nil {
		errs = append(errs, err)
	}
	allErr := errors.Join(errs...)
	if strict {
		return allErr, allErr
	}
	return nil, allErr
}

func (op *Operator) activeServerInterfaces(ctx context.Context, sr ServerRoutingConfig) []string {
	var ifaces []string
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for _, v := range ifaces {
			if v == name {
				return
			}
		}
		ifaces = append(ifaces, name)
	}
	wgServers, hasProvider := op.resolveWGServers(ctx)
	if hasProvider {
		for _, srv := range matchEnabledWGServers(sr, wgServers) {
			add(srv.KernelIface)
		}
		if sr.IsServerEnabled("freeturn_server") {
			add(findFreeTurnDetailIface(wgServers))
		}
	} else {
		if sr.IsServerEnabled("awg_server") || sr.IsServerEnabled("freeturn_server") {
			add("nwg2")
		}
		if sr.IsServerEnabled("keenetic_wg") {
			add("nwg0")
		}
	}
	if sr.IsServerEnabled("wdtt_server") {
		add("opkgtun17")
		add("opkgtun19")
	}
	if sr.IsServerEnabled("openflux_server") {
		op.mu.Lock()
		openfluxFn := op.openfluxStatusFn
		op.mu.Unlock()
		if openfluxFn != nil {
			if _, tunIface := openfluxFn(); tunIface != "" {
				add(tunIface)
			}
		}
	}
	if sr.IsServerEnabled("keenetic_sstp") {
		add("sstp+")
	}
	if sr.IsServerEnabled("keenetic_l2tp") {
		add("l2tp+")
	}
	if sr.IsServerEnabled("keenetic_ikev2") {
		add("xfrms2")
	}
	if sr.IsServerEnabled("keenetic_ikev1") {
		add("xfrms1")
	}
	if sr.IsServerEnabled("keenetic_pptp") {
		add("vpn+")
	}
	if sr.IsServerEnabled("keenetic_oc") {
		add("oc+")
	}
	return ifaces
}

type fircFakeIPMirrorState struct {
	ruleSpec   string
	realTarget string
	generation uint64
	syncedAt   time.Time
}

var (
	mirrorNetfilterMu  sync.Mutex
	extIptablesMu      sync.RWMutex
	extIptablesRunner  = defaultExtIptablesRunner
	extIptablesCustom  bool
	fircMirrorGen      atomic.Uint64
	extNetfilterGen    atomic.Uint64
	onMirrorInvalidate func()

	lastDNSRedirectSig string
	lastMSSClampSig    string
	lastFextOutSig     string
	lastFextNoutSig    string
	lastGroupChainSigs = make(map[string]string)
	lastNatChainSigs   = make(map[string]string)
	fircFakeIPMirrors  = make(map[string]fircFakeIPMirrorState)
)

func init() {
	fircMirrorGen.Store(1)
	extNetfilterGen.Store(1)
}

// SetOnMirrorInvalidate registers a callback invoked whenever FIRC invalidates its OUTPUT/NAT mirror caches.
func SetOnMirrorInvalidate(fn func()) {
	mirrorNetfilterMu.Lock()
	onMirrorInvalidate = fn
	mirrorNetfilterMu.Unlock()
}

// InvalidateOutputRoutingCache increments the FIRC NAT/OUTPUT mirror generation and clears cached signatures.
func InvalidateOutputRoutingCache() uint64 {
	mirrorNetfilterMu.Lock()
	gen := fircMirrorGen.Add(1)
	lastDNSRedirectSig = ""
	lastMSSClampSig = ""
	lastFextOutSig = ""
	lastFextNoutSig = ""
	lastGroupChainSigs = make(map[string]string)
	lastNatChainSigs = make(map[string]string)
	fircFakeIPMirrors = make(map[string]fircFakeIPMirrorState)
	cb := onMirrorInvalidate
	mirrorNetfilterMu.Unlock()
	if cb != nil {
		cb()
	}
	return gen
}

func hasChainRules(ctx context.Context, table, chain string) bool {
	out, err := runExtIptablesOutput(ctx, "-w", "-t", table, "-S", chain)
	if err != nil {
		return false
	}
	lines := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	return lines > 1
}

func isMissingIptablesRuleError(out []byte, err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(strings.TrimSpace(string(out)))
	errStr := strings.ToLower(err.Error())
	if strings.Contains(s, "lock") || strings.Contains(s, "permission") || strings.Contains(s, "denied") ||
		strings.Contains(s, "resource temporarily unavailable") || strings.Contains(errStr, "exit status 4") {
		return false
	}
	if strings.Contains(s, "bad rule") || strings.Contains(s, "no chain/target/match") ||
		strings.Contains(s, "does a matching rule exist") || strings.Contains(s, "does not exist") ||
		strings.Contains(s, "couldn't load target") || strings.Contains(s, "no such file or directory") ||
		(s == "" && strings.Contains(errStr, "exit status 1")) {
		return true
	}
	return false
}

func isStandardIptablesTarget(target string) bool {
	switch strings.ToUpper(strings.TrimSpace(target)) {
	case "RETURN", "DROP", "ACCEPT", "MARK", "CONNMARK", "REDIRECT", "DNAT", "SNAT", "MASQUERADE", "TCPMSS", "LOG", "REJECT", "TPROXY", "NOTRACK":
		return true
	default:
		return false
	}
}

func isMissingIptablesChain(ctx context.Context, table, chain string, out []byte, err error) bool {
	if isMissingIptablesRuleError(out, err) {
		return true
	}
	if err == nil || chain == "" || !strings.Contains(strings.ToLower(string(out)), "incompatible with this kernel") {
		return false
	}
	tblOut, tblErr := runExtIptablesOutput(ctx, "-w", "-t", table, "-S")
	if tblErr != nil {
		return false
	}
	wantNewExact := "-N " + chain
	wantNewPrefix := "-N " + chain + " "
	wantRulePrefix := "-A " + chain + " "
	for _, rawLine := range strings.Split(string(tblOut), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == wantNewExact || strings.HasPrefix(line, wantNewPrefix) || strings.HasPrefix(line, wantRulePrefix) {
			return false
		}
	}
	return true
}

func checkIptablesRuleExists(ctx context.Context, table, chain string, ruleArgs ...string) (bool, error) {
	checkArgs := append([]string{"-w", "-t", table, "-C", chain}, ruleArgs...)
	out, err := runExtIptablesOutput(ctx, checkArgs...)
	if err == nil {
		return true, nil
	}
	if isMissingIptablesRuleError(out, err) {
		return false, nil
	}
	if strings.Contains(strings.ToLower(string(out)), "incompatible with this kernel") {
		if chain != "" && isMissingIptablesChain(ctx, table, chain, out, err) {
			return false, nil
		}
		for i := 0; i < len(ruleArgs)-1; i++ {
			if ruleArgs[i] == "-j" || ruleArgs[i] == "-g" {
				target := ruleArgs[i+1]
				if target != "" && !isStandardIptablesTarget(target) && isMissingIptablesChain(ctx, table, target, out, err) {
					return false, nil
				}
			}
		}
	}
	return false, fmt.Errorf("iptables -t %s -C %s %s: %w (%s)", table, chain, strings.Join(ruleArgs, " "), err, strings.TrimSpace(string(out)))
}

func deleteIptablesRuleIfPresent(ctx context.Context, table, chain string, existing []string, ruleArgs ...string) error {
	ruleSpec := strings.Join(ruleArgs, " ")
	present := false
	for _, r := range existing {
		if r == ruleSpec {
			present = true
			break
		}
	}
	delArgs := append([]string{"-w", "-t", table, "-D", chain}, ruleArgs...)
	out, err := runExtIptablesOutput(ctx, delArgs...)
	if err == nil {
		return nil
	}
	if !present && isMissingIptablesRuleError(out, err) {
		return nil
	}
	if !present && strings.Contains(strings.ToLower(string(out)), "incompatible with this kernel") {
		if chain != "" && isMissingIptablesChain(ctx, table, chain, out, err) {
			return nil
		}
		for i := 0; i < len(ruleArgs)-1; i++ {
			if ruleArgs[i] == "-j" || ruleArgs[i] == "-g" {
				target := ruleArgs[i+1]
				if target != "" && !isStandardIptablesTarget(target) && isMissingIptablesChain(ctx, table, target, out, err) {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("iptables -t %s -D %s %s: %w (%s)", table, chain, ruleSpec, err, strings.TrimSpace(string(out)))
}

func ensureIptablesRule(ctx context.Context, table, chain string, ruleArgs ...string) error {
	exists, err := checkIptablesRuleExists(ctx, table, chain, ruleArgs...)
	if err != nil {
		return err
	}
	if !exists {
		addArgs := append([]string{"-t", table, "-A", chain}, ruleArgs...)
		return runIptables(ctx, addArgs...)
	}
	return nil
}

func reconcileServerDNSRedirect(ctx context.Context, ifaces []string) error {
	var recErr error
	hookExists, hookErr := checkIptablesRuleExists(ctx, "nat", "PREROUTING", "-j", "FEXT_DNS")
	recErr = errors.Join(recErr, hookErr)
	sig := strings.Join(ifaces, ",")

	if len(ifaces) == 0 {
		if hookExists {
			recErr = errors.Join(recErr, runIptables(ctx, "-t", "nat", "-D", "PREROUTING", "-j", "FEXT_DNS"))
		}
		if lastDNSRedirectSig != "" {
			recErr = errors.Join(recErr, runIptables(ctx, "-t", "nat", "-F", "FEXT_DNS"))
			if recErr == nil {
				lastDNSRedirectSig = ""
			}
		}
		return recErr
	}

	ensureHookTop := func() {
		if !hookExists {
			recErr = errors.Join(recErr, runIptables(ctx, "-t", "nat", "-I", "PREROUTING", "1", "-j", "FEXT_DNS"))
			return
		}
		if prRules, err := readChainRuleList(ctx, "nat", "PREROUTING"); err == nil && len(prRules) > 0 && prRules[0] != "-j FEXT_DNS" {
			_ = runIptables(ctx, "-t", "nat", "-D", "PREROUTING", "-j", "FEXT_DNS")
			recErr = errors.Join(recErr, runIptables(ctx, "-t", "nat", "-I", "PREROUTING", "1", "-j", "FEXT_DNS"))
		}
	}

	if sig == lastDNSRedirectSig && hasChainRules(ctx, "nat", "FEXT_DNS") {
		ensureHookTop()
		return recErr
	}

	if err := runIptables(ctx, "-t", "nat", "-N", "FEXT_DNS"); err != nil {
		return errors.Join(recErr, err)
	}
	for _, iface := range ifaces {
		for _, proto := range []string{"udp", "tcp"} {
			recErr = errors.Join(recErr, ensureIptablesRule(ctx, "nat", "FEXT_DNS",
				"-i", iface, "-p", proto, "-m", proto, "--dport", "53", "-j", "REDIRECT", "--to-ports", "3553"))
		}
	}

	ensureHookTop()
	if recErr == nil {
		lastDNSRedirectSig = sig
	}
	return recErr
}

func reconcileServerMSSClamp(ctx context.Context, ifaces []string) error {
	var recErr error
	hookExists, hookErr := checkIptablesRuleExists(ctx, "mangle", "FORWARD", "-j", "FEXT_MSS")
	recErr = errors.Join(recErr, hookErr)
	sig := strings.Join(ifaces, ",")

	if len(ifaces) == 0 {
		if hookExists {
			recErr = errors.Join(recErr, runIptables(ctx, "-t", "mangle", "-D", "FORWARD", "-j", "FEXT_MSS"))
		}
		if lastMSSClampSig != "" {
			recErr = errors.Join(recErr, runIptables(ctx, "-t", "mangle", "-F", "FEXT_MSS"))
			if recErr == nil {
				lastMSSClampSig = ""
			}
		}
		return recErr
	}

	if sig == lastMSSClampSig && hasChainRules(ctx, "mangle", "FEXT_MSS") {
		if !hookExists {
			recErr = errors.Join(recErr, insertChainRuleRespectingCutoverGuard(ctx, "mangle", "FORWARD", cutoverGuardChain, "-j", "FEXT_MSS"))
		}
		return recErr
	}

	if err := runIptables(ctx, "-t", "mangle", "-N", "FEXT_MSS"); err != nil {
		return errors.Join(recErr, err)
	}
	mssIfaces := append([]string{}, ifaces...)
	mssIfaces = append(mssIfaces, "tunvless+")
	for _, iface := range mssIfaces {
		recErr = errors.Join(recErr, ensureIptablesRule(ctx, "mangle", "FEXT_MSS",
			"-i", iface, "-p", "tcp", "-m", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"))
		recErr = errors.Join(recErr, ensureIptablesRule(ctx, "mangle", "FEXT_MSS",
			"-o", iface, "-p", "tcp", "-m", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu"))
	}

	if !hookExists {
		recErr = errors.Join(recErr, insertChainRuleRespectingCutoverGuard(ctx, "mangle", "FORWARD", cutoverGuardChain, "-j", "FEXT_MSS"))
	}
	if recErr == nil {
		lastMSSClampSig = sig
	}
	return recErr
}

func reconcileServerDeviceChains(ctx context.Context, ifaces []string, groups []Group) error {
	if len(ifaces) == 0 {
		return nil
	}
	var recErr error
	for _, g := range groups {
		if !g.Enable || g.ID == "" || g.ID == KeenDNSGroupID || (len(g.Devices.Allow) == 0 && len(g.Devices.Deny) == 0) {
			continue
		}
		chainD := "FIRC_" + g.ID + "D"
		groupMark := extractGroupMarkFromChain(ctx, chainD)
		if groupMark == "" {
			continue
		}
		for _, iface := range ifaces {
			retRule := []string{"-i", iface, "-m", "mark", "--mark", groupMark, "-j", "RETURN"}
			saveRule := []string{"-i", iface, "-m", "mark", "--mark", groupMark, "-j", "CONNMARK", "--save-mark", "--nfmask", "0x40ff0000", "--ctmask", "0x40ff0000"}
			markRule := []string{"-i", iface, "-j", "MARK", "--set-xmark", groupMark}

			if exists, err := checkIptablesRuleExists(ctx, "mangle", chainD, retRule...); err != nil {
				recErr = errors.Join(recErr, err)
			} else if !exists {
				recErr = errors.Join(recErr, runIptables(ctx, append([]string{"-t", "mangle", "-I", chainD, "1"}, retRule...)...))
			}
			if exists, err := checkIptablesRuleExists(ctx, "mangle", chainD, saveRule...); err != nil {
				recErr = errors.Join(recErr, err)
			} else if !exists {
				recErr = errors.Join(recErr, runIptables(ctx, append([]string{"-t", "mangle", "-I", chainD, "1"}, saveRule...)...))
			}
			if exists, err := checkIptablesRuleExists(ctx, "mangle", chainD, markRule...); err != nil {
				recErr = errors.Join(recErr, err)
			} else if !exists {
				recErr = errors.Join(recErr, runIptables(ctx, append([]string{"-t", "mangle", "-I", chainD, "1"}, markRule...)...))
			}
		}
	}
	return recErr
}

func extractGroupMarkFromChain(ctx context.Context, chainD string) string {
	out, err := runExtIptablesOutput(ctx, "-w", "-t", "mangle", "-S", chainD)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, "--set-xmark ")
		if idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("--set-xmark "):])
			fields := strings.Fields(rest)
			if len(fields) > 0 && strings.HasPrefix(fields[0], "0x40") {
				return fields[0]
			}
		}
	}
	return ""
}

func detectLANBridgeIPv4() string {
	iface, err := net.InterfaceByName("br0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP != nil {
			if ip4 := ipnet.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
				return ip4.String()
			}
		}
	}
	return ""
}

func reconcileFakeIPOutputRoute(ctx context.Context, needOutput bool) {
	ruleOut, err := exec.CommandContext(ctx, "ip", "-4", "rule", "show").Output()
	if err != nil {
		return
	}
	rulesStr := string(ruleOut)
	// Purge any legacy/stale iif or fwmark rules pointing unconditionally to table 120 (which
	// only handles 198.18.0.0/15 FakeIPs and would blackhole LAN/DNAT traffic above pref 100).
	for _, line := range strings.Split(rulesStr, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, "lookup 120") || strings.Contains(line, "to 198.18.0.0/15") {
			continue
		}
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx <= 0 {
			continue
		}
		pref := strings.TrimSpace(line[:colonIdx])
		if pref != "" {
			_ = exec.CommandContext(ctx, "ip", "-4", "rule", "del", "pref", pref, "lookup", "120").Run()
		}
	}
	hasFwmarkRule := strings.Contains(rulesStr, "to 198.18.0.0/15 fwmark 0xf1ec/0xffff lookup 3553")
	hasLoRule := strings.Contains(rulesStr, "to 198.18.0.0/15 iif lo lookup 3553")

	if !needOutput {
		if hasFwmarkRule {
			_ = exec.CommandContext(ctx, "ip", "-4", "rule", "del", "pref", "48", "to", "198.18.0.0/15", "fwmark", "0xf1ec/0xffff", "lookup", "3553").Run()
		}
		if hasLoRule {
			_ = exec.CommandContext(ctx, "ip", "-4", "rule", "del", "pref", "48", "iif", "lo", "to", "198.18.0.0/15", "lookup", "3553").Run()
		}
		if hasFwmarkRule || hasLoRule {
			_ = exec.CommandContext(ctx, "ip", "-4", "route", "flush", "table", "3553").Run()
		}
		return
	}

	routeOut, _ := exec.CommandContext(ctx, "ip", "-4", "route", "show", "table", "3553").Output()
	if !strings.Contains(string(routeOut), "198.18.0.0/15 dev lo") {
		if brIP := detectLANBridgeIPv4(); brIP != "" {
			if exec.CommandContext(ctx, "ip", "-4", "route", "replace", "198.18.0.0/15", "dev", "lo", "scope", "link", "src", brIP, "table", "3553").Run() != nil {
				_ = exec.CommandContext(ctx, "ip", "-4", "route", "replace", "198.18.0.0/15", "dev", "lo", "scope", "link", "table", "3553").Run()
			}
		} else {
			_ = exec.CommandContext(ctx, "ip", "-4", "route", "replace", "198.18.0.0/15", "dev", "lo", "scope", "link", "table", "3553").Run()
		}
	}
	if !hasFwmarkRule {
		_ = exec.CommandContext(ctx, "ip", "-4", "rule", "add", "pref", "48", "to", "198.18.0.0/15", "fwmark", "0xf1ec/0xffff", "lookup", "3553").Run()
	}
	if !hasLoRule {
		_ = exec.CommandContext(ctx, "ip", "-4", "rule", "add", "pref", "48", "iif", "lo", "to", "198.18.0.0/15", "lookup", "3553").Run()
	}
	// Purge any failed/incomplete ARP neighbor entries for 198.18.0.0/15 on br0.
	_ = exec.CommandContext(ctx, "ip", "-4", "neigh", "flush", "dev", "br0", "to", "198.18.0.0/15", "nud", "all").Run()
}

func readChainRuleList(ctx context.Context, table, chain string) ([]string, error) {
	out, err := runExtIptablesOutput(ctx, "-w", "-t", table, "-S", chain)
	if err != nil {
		return nil, fmt.Errorf("iptables -t %s -S %s: %w (%s)", table, chain, err, strings.TrimSpace(string(out)))
	}
	var rules []string
	prefix := "-A " + chain + " "
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			rules = append(rules, strings.TrimSpace(strings.TrimPrefix(line, prefix)))
		}
	}
	return rules, nil
}

func verifyChainExactOrder(ctx context.Context, table, chain string, desired []string) error {
	actual, err := readChainRuleList(ctx, table, chain)
	if err != nil {
		return err
	}
	if !equalStringSlices(actual, desired) {
		return fmt.Errorf("chain %s (%s) ordered rules mismatch: got %v, want %v", chain, table, actual, desired)
	}
	return nil
}

func readChainRuleSet(ctx context.Context, table, chain string) (map[string]bool, error) {
	list, err := readChainRuleList(ctx, table, chain)
	if err != nil {
		return nil, err
	}
	rules := make(map[string]bool, len(list))
	for _, r := range list {
		rules[r] = true
	}
	return rules, nil
}

func verifyChainExactRules(ctx context.Context, table, chain string, desired map[string]bool) error {
	actual, err := readChainRuleSet(ctx, table, chain)
	if err != nil {
		return err
	}
	for want := range desired {
		if !actual[want] {
			return fmt.Errorf("chain %s (%s) missing desired rule %q", chain, table, want)
		}
	}
	for got := range actual {
		if !desired[got] {
			return fmt.Errorf("chain %s (%s) still contains obsolete rule %q", chain, table, got)
		}
	}
	return nil
}

func appendFircNewFlowMatch(matchPart string) string {
	matchPart = strings.TrimSpace(matchPart)
	if matchPart == "" {
		return fircNewFlowMatch
	}
	if strings.Contains(matchPart, "-m conntrack") {
		return matchPart
	}
	return matchPart + " " + fircNewFlowMatch
}

func syncGroupMirrorChain(ctx context.Context, fextChain, srcChain, groupMark, rawRules string, extraFirstRules ...string) error {
	if err := runIptables(ctx, "-t", "mangle", "-N", fextChain); err != nil {
		return err
	}

	var syncErr error
	existingRules, listErr := readChainRuleSet(ctx, "mangle", fextChain)
	if listErr != nil {
		return listErr
	}

	desiredMap := make(map[string]bool)
	for i := len(extraFirstRules) - 1; i >= 0; i-- {
		ruleSpec := strings.TrimSpace(extraFirstRules[i])
		if ruleSpec == "" {
			continue
		}
		desiredMap[ruleSpec] = true
		if !existingRules[ruleSpec] {
			args := append([]string{"-t", "mangle", "-I", fextChain, "1"}, strings.Fields(ruleSpec)...)
			if err := runIptables(ctx, args...); err != nil {
				syncErr = errors.Join(syncErr, err)
			}
		}
	}

	prefix := "-A " + srcChain + " "
	suffixD := " -j " + srcChain + "D"
	markTarget := " -j MARK --set-xmark "
	for _, line := range strings.Split(rawRules, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		var matchPart string
		if strings.HasSuffix(rest, suffixD) {
			matchPart = strings.TrimSpace(strings.TrimSuffix(rest, suffixD))
		} else if idx := strings.Index(rest, markTarget); idx > 0 {
			cand := strings.TrimSpace(rest[:idx])
			// Exclude device-specific input interface or MAC rules (invalid in mangle OUTPUT).
			if !strings.Contains(cand, "-m mac") && !strings.Contains(cand, "-i ") {
				matchPart = cand
			}
		}
		if matchPart != "" {
			ruleSpec := appendFircNewFlowMatch(matchPart) + " -j MARK --set-xmark " + groupMark
			desiredMap[ruleSpec] = true
			if !existingRules[ruleSpec] {
				args := append([]string{"-t", "mangle", "-A", fextChain}, strings.Fields(ruleSpec)...)
				if err := runIptables(ctx, args...); err != nil {
					syncErr = errors.Join(syncErr, err)
				}
			}
		}
	}

	// Remove any stale rules only AFTER new rules are in place (zero gap).
	for oldRule := range existingRules {
		if !desiredMap[oldRule] {
			args := append([]string{"-t", "mangle", "-D", fextChain}, strings.Fields(oldRule)...)
			if err := runIptables(ctx, args...); err != nil {
				syncErr = errors.Join(syncErr, err)
			}
		}
	}

	if vErr := verifyChainExactRules(ctx, "mangle", fextChain, desiredMap); vErr != nil {
		syncErr = errors.Join(syncErr, vErr)
	}
	return syncErr
}

// syncNatMirrorChain mirrors rules from a FIRC_* chain (such as FIRC_REDIR or FIRC_DNAT)
// into a FEXT_* chain in the nat table without ever jumping to a FIRC_* chain.
// fircd's internal x_tables merger (firc_xt_merge) strips any rule in any chain whose
// target starts with "FIRC_" if fircd did not stage that rule itself (which happens on
// every new FakeIP allocation in FIRC_DNAT). Using FEXT_* mirror chains prevents fircd
// from wiping FEXT_NOUT during DNS FakeIP allocations.
func syncNatMirrorChain(ctx context.Context, fextChain, srcChain string) (bool, error) {
	srcOut, err := runExtIptablesOutput(ctx, "-w", "-t", "nat", "-S", srcChain)
	rawRules := string(srcOut)
	if err != nil {
		if !isMissingIptablesChain(ctx, "nat", srcChain, srcOut, err) {
			delete(lastNatChainSigs, fextChain)
			return false, fmt.Errorf("read source chain %s: %w", srcChain, err)
		}
		rawRules = ""
	}

	desiredMap := make(map[string]bool)
	srcPrefix := "-A " + srcChain + " "
	for _, line := range strings.Split(rawRules, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, srcPrefix) {
			continue
		}
		ruleSpec := strings.TrimSpace(strings.TrimPrefix(line, srcPrefix))
		if ruleSpec == "" || strings.Contains(ruleSpec, "-j FIRC_") {
			continue
		}
		desiredMap[ruleSpec] = true
	}

	if lastNatChainSigs[fextChain] == rawRules {
		if verifyChainExactRules(ctx, "nat", fextChain, desiredMap) == nil {
			return len(desiredMap) > 0, nil
		}
		delete(lastNatChainSigs, fextChain)
	}

	if err := runIptables(ctx, "-t", "nat", "-N", fextChain); err != nil {
		delete(lastNatChainSigs, fextChain)
		return false, err
	}

	var syncErr error
	existingRules, listErr := readChainRuleSet(ctx, "nat", fextChain)
	if listErr != nil {
		delete(lastNatChainSigs, fextChain)
		return false, listErr
	}

	for ruleSpec := range desiredMap {
		if !existingRules[ruleSpec] {
			args := append([]string{"-t", "nat", "-A", fextChain}, strings.Fields(ruleSpec)...)
			if err := runIptables(ctx, args...); err != nil {
				syncErr = errors.Join(syncErr, err)
			}
		}
	}

	for oldRule := range existingRules {
		if !desiredMap[oldRule] {
			args := append([]string{"-t", "nat", "-D", fextChain}, strings.Fields(oldRule)...)
			if err := runIptables(ctx, args...); err != nil {
				syncErr = errors.Join(syncErr, err)
			}
		}
	}

	if vErr := verifyChainExactRules(ctx, "nat", fextChain, desiredMap); vErr != nil {
		syncErr = errors.Join(syncErr, vErr)
	}
	if syncErr != nil {
		delete(lastNatChainSigs, fextChain)
		return false, syncErr
	}

	lastNatChainSigs[fextChain] = rawRules
	return len(desiredMap) > 0, nil
}

// SyncFakeIPOutputDnat synchronizes a single FakeIP DNAT rule from FIRC_DNAT into FEXT_DNAT
// under the shared mirrorNetfilterMu lock so it never races with reconcileOutputRouting.
func SyncFakeIPOutputDnat(ctx context.Context, fakeIP string, force bool) (ruleSpec, realTarget string, changed bool, gen uint64, err error) {
	fakeIP = strings.TrimSpace(fakeIP)
	if fakeIP == "" {
		return "", "", false, fircMirrorGen.Load(), errors.New("empty fakeIP")
	}

	mirrorNetfilterMu.Lock()
	defer mirrorNetfilterMu.Unlock()

	curGen := fircMirrorGen.Load()
	srcOut, readErr := runExtIptablesOutput(ctx, "-w", "-t", "nat", "-S", "FIRC_DNAT")
	if readErr != nil {
		delete(fircFakeIPMirrors, fakeIP)
		delete(lastNatChainSigs, "FEXT_DNAT")
		return "", "", false, curGen, fmt.Errorf("read FIRC_DNAT: %w", readErr)
	}

	needle := "-A FIRC_DNAT -d " + fakeIP + "/32 "
	for _, line := range strings.Split(string(srcOut), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, needle) {
			ruleSpec = strings.TrimSpace(strings.TrimPrefix(line, "-A FIRC_DNAT "))
			break
		}
	}
	if ruleSpec == "" {
		delete(fircFakeIPMirrors, fakeIP)
		return "", "", false, curGen, fmt.Errorf("no FIRC_DNAT rule for %s", fakeIP)
	}

	fields := strings.Fields(ruleSpec)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "--to-destination" || fields[i] == "--to" {
			realTarget = fields[i+1]
			break
		}
	}

	if !force {
		if st, ok := fircFakeIPMirrors[fakeIP]; ok {
			if st.generation == curGen && st.ruleSpec == ruleSpec && time.Since(st.syncedAt) < 10*time.Second {
				return ruleSpec, realTarget, false, curGen, nil
			}
		}
	}

	if err := runIptables(ctx, "-t", "nat", "-N", "FEXT_DNAT"); err != nil {
		delete(fircFakeIPMirrors, fakeIP)
		delete(lastNatChainSigs, "FEXT_DNAT")
		return ruleSpec, realTarget, false, curGen, err
	}
	existingRules, listErr := readChainRuleSet(ctx, "nat", "FEXT_DNAT")
	if listErr != nil {
		delete(fircFakeIPMirrors, fakeIP)
		delete(lastNatChainSigs, "FEXT_DNAT")
		return ruleSpec, realTarget, false, curGen, listErr
	}

	var syncErr error
	if !existingRules[ruleSpec] {
		addArgs := append([]string{"-t", "nat", "-I", "FEXT_DNAT", "1"}, fields...)
		if err := runIptables(ctx, addArgs...); err != nil {
			syncErr = errors.Join(syncErr, err)
		} else {
			changed = true
		}
	}

	dstPrefix := "-d " + fakeIP + "/32 "
	for oldSpec := range existingRules {
		if strings.HasPrefix(oldSpec, dstPrefix) && oldSpec != ruleSpec {
			delArgs := append([]string{"-t", "nat", "-D", "FEXT_DNAT"}, strings.Fields(oldSpec)...)
			if err := runIptables(ctx, delArgs...); err != nil {
				syncErr = errors.Join(syncErr, err)
			} else {
				changed = true
			}
		}
	}

	if syncErr != nil {
		delete(fircFakeIPMirrors, fakeIP)
		delete(lastNatChainSigs, "FEXT_DNAT")
		return ruleSpec, realTarget, false, curGen, syncErr
	}

	// Verify the desired rule is present and no stale rule for fakeIP remains.
	afterRules, verifyErr := readChainRuleSet(ctx, "nat", "FEXT_DNAT")
	if verifyErr != nil || !afterRules[ruleSpec] {
		delete(fircFakeIPMirrors, fakeIP)
		delete(lastNatChainSigs, "FEXT_DNAT")
		if verifyErr != nil {
			return ruleSpec, realTarget, false, curGen, verifyErr
		}
		return ruleSpec, realTarget, false, curGen, fmt.Errorf("FEXT_DNAT verification failed for %s", fakeIP)
	}
	for r := range afterRules {
		if strings.HasPrefix(r, dstPrefix) && r != ruleSpec {
			delete(fircFakeIPMirrors, fakeIP)
			delete(lastNatChainSigs, "FEXT_DNAT")
			return ruleSpec, realTarget, false, curGen, fmt.Errorf("stale FEXT_DNAT rule remained for %s: %s", fakeIP, r)
		}
	}

	if len(fircFakeIPMirrors) > 1024 {
		fircFakeIPMirrors = make(map[string]fircFakeIPMirrorState)
	}
	fircFakeIPMirrors[fakeIP] = fircFakeIPMirrorState{
		ruleSpec:   ruleSpec,
		realTarget: realTarget,
		generation: curGen,
		syncedAt:   time.Now(),
	}
	return ruleSpec, realTarget, changed, curGen, nil
}

func reconcileOutputRouting(ctx context.Context, ext ExtConfig, groups []Group) error {
	return (*Operator)(nil).reconcileOutputRouting(ctx, ext, groups)
}

func (op *Operator) reconcileOutputRouting(ctx context.Context, ext ExtConfig, groups []Group) error {
	needKeenDNS := extWantsKeenDNS(ext)
	needXHTTP := ext.ServerRouting.IsServerEnabled("vless_xhttp_server")
	needOutput := needKeenDNS || needXHTTP

	reconcileFakeIPOutputRoute(ctx, needOutput)

	mangleHookExists, mangleHookErr := checkIptablesRuleExists(ctx, "mangle", "OUTPUT", "-j", "FEXT_OUT")
	natHookExists, natHookErr := checkIptablesRuleExists(ctx, "nat", "OUTPUT", "-j", "FEXT_NOUT")

	if !needOutput {
		var disableErr error
		if mangleHookErr != nil {
			disableErr = errors.Join(disableErr, mangleHookErr)
		} else if mangleHookExists {
			disableErr = errors.Join(disableErr, runIptables(ctx, "-t", "mangle", "-D", "OUTPUT", "-j", "FEXT_OUT"))
		}
		if natHookErr != nil {
			disableErr = errors.Join(disableErr, natHookErr)
		} else if natHookExists {
			disableErr = errors.Join(disableErr, runIptables(ctx, "-t", "nat", "-D", "OUTPUT", "-j", "FEXT_NOUT"))
		}
		if lastFextOutSig != "" || lastFextNoutSig != "" || hasChainRules(ctx, "mangle", "FEXT_OUT") || hasChainRules(ctx, "nat", "FEXT_NOUT") {
			if _, err := readChainRuleList(ctx, "mangle", "FEXT_OUT"); err == nil {
				disableErr = errors.Join(disableErr, runIptables(ctx, "-t", "mangle", "-F", "FEXT_OUT"))
			}
			if _, err := readChainRuleList(ctx, "nat", "FEXT_NOUT"); err == nil {
				disableErr = errors.Join(disableErr, runIptables(ctx, "-t", "nat", "-F", "FEXT_NOUT"))
			}
			if disableErr == nil {
				lastFextOutSig = ""
				lastFextNoutSig = ""
				lastGroupChainSigs = make(map[string]string)
				lastNatChainSigs = make(map[string]string)
			}
		}
		if disableErr == nil {
			disableErr = errors.Join(disableErr, op.teardownKeenDNSCloudChainAndSet(ctx))
			if mExists, mErr := checkIptablesRuleExists(ctx, "mangle", "OUTPUT", "-j", "FEXT_OUT"); mErr != nil {
				disableErr = errors.Join(disableErr, mErr)
			} else if mExists {
				disableErr = errors.Join(disableErr, errors.New("mangle OUTPUT -j FEXT_OUT still exists after disable"))
			}
			if nExists, nErr := checkIptablesRuleExists(ctx, "nat", "OUTPUT", "-j", "FEXT_NOUT"); nErr != nil {
				disableErr = errors.Join(disableErr, nErr)
			} else if nExists {
				disableErr = errors.Join(disableErr, errors.New("nat OUTPUT -j FEXT_NOUT still exists after disable"))
			}
		}
		return disableErr
	}

	var recErr error
	var kdMirrorReady bool
	if needKeenDNS {
		srcChain := "FIRC_" + KeenDNSGroupID
		fextChain := "FEXT_" + KeenDNSGroupID
		if out, err := runExtIptablesOutput(ctx, "-w", "-t", "mangle", "-S", srcChain); err == nil {
			kdMark := extractGroupMarkFromChain(ctx, srcChain+"D")
			if kdMark == "" {
				kdMark = extractGroupMarkFromChain(ctx, srcChain)
			}
			if kdMark != "" {
				raw := string(out)
				cloudReady := cloudIpsetExists(ctx)
				var extraRules []string
				if cloudReady {
					extraRules = append(extraRules,
						fmt.Sprintf("-m set --match-set %s dst %s -j MARK --set-xmark %s", FextCloudIpsetName, fircNewFlowMatch, kdMark),
						fmt.Sprintf("-p udp -m set --match-set %s dst %s -j MARK --set-xmark %s", FextCloudIpsetName, fircOrigUnmarkedMatch, kdMark),
					)
				}
				grpSig := fmt.Sprintf("%s;ct=new-orig+udp;cloud=%v;%s", kdMark, cloudReady, raw)
				cloudRuleMissing := false
				if cloudReady && lastGroupChainSigs[KeenDNSGroupID] == grpSig {
					for _, er := range extraRules {
						if exists, cErr := checkIptablesRuleExists(ctx, "mangle", fextChain, strings.Fields(er)...); cErr != nil || !exists {
							cloudRuleMissing = true
							break
						}
					}
				}
				var mErr error
				if lastGroupChainSigs[KeenDNSGroupID] != grpSig || !hasChainRules(ctx, "mangle", fextChain) || cloudRuleMissing {
					mErr = syncGroupMirrorChain(ctx, fextChain, srcChain, kdMark, raw, extraRules...)
					if mErr == nil {
						lastGroupChainSigs[KeenDNSGroupID] = grpSig
					} else {
						delete(lastGroupChainSigs, KeenDNSGroupID)
						recErr = errors.Join(recErr, mErr)
					}
				}
				if mErr == nil || hasChainRules(ctx, "mangle", fextChain) {
					kdMirrorReady = true
				}
			}
		} else if !isMissingIptablesChain(ctx, "mangle", srcChain, out, err) {
			recErr = errors.Join(recErr, fmt.Errorf("read %s: %w", srcChain, err))
		}
	}

	type groupChainSpec struct {
		id        string
		chain     string
		groupMark string
		rawRules  string
	}
	var activeSpecs []groupChainSpec
	var outSigBuilder strings.Builder
	fmt.Fprintf(&outSigBuilder, "kd=%v;xhttp=%v;", kdMirrorReady, needXHTTP)

	if needXHTTP {
		for _, g := range groups {
			if !g.Enable || g.ID == "" || g.ID == KeenDNSGroupID || g.ID == globalGroupID {
				continue
			}
			chain := "FIRC_" + g.ID
			chainD := chain + "D"
			out, err := runExtIptablesOutput(ctx, "-w", "-t", "mangle", "-S", chain)
			if err != nil {
				if !isMissingIptablesChain(ctx, "mangle", chain, out, err) {
					recErr = errors.Join(recErr, fmt.Errorf("read %s: %w", chain, err))
				}
				continue
			}
			groupMark := extractGroupMarkFromChain(ctx, chainD)
			if groupMark == "" {
				groupMark = extractGroupMarkFromChain(ctx, chain)
			}
			if groupMark == "" {
				continue
			}
			raw := string(out)
			fmt.Fprintf(&outSigBuilder, "g=%s;m=%s;", g.ID, groupMark)
			activeSpecs = append(activeSpecs, groupChainSpec{
				id:        g.ID,
				chain:     chain,
				groupMark: groupMark,
				rawRules:  raw,
			})
		}
	}

	// Incrementally sync each FEXT_<id> mirror chain without ever flushing it.
	for _, spec := range activeSpecs {
		fextChain := "FEXT_" + spec.id
		grpSig := spec.groupMark + ";" + spec.rawRules
		if lastGroupChainSigs[spec.id] != grpSig || !hasChainRules(ctx, "mangle", fextChain) {
			if err := syncGroupMirrorChain(ctx, fextChain, spec.chain, spec.groupMark, spec.rawRules); err == nil {
				lastGroupChainSigs[spec.id] = grpSig
			} else {
				delete(lastGroupChainSigs, spec.id)
				recErr = errors.Join(recErr, err)
			}
		}
	}

	fextOutSig := outSigBuilder.String()
	unclassifiedMarkStr := fmt.Sprintf("0x%x/0x40ffffff", XHTTPServerFircMark)

	var desiredOutRules []string
	if kdMirrorReady || needXHTTP {
		// Restore FIRC connmark bits (0x40ff0000) before any NEW-only classification so
		// packet 2+ of already-proxied flows keeps routing even when XHTTP is disabled,
		// while preserving foreign mark bits and skipping tunnel uplink/bypass marks.
		desiredOutRules = append(desiredOutRules, fircRestoreMarkRule)
	}
	if kdMirrorReady {
		desiredOutRules = append(desiredOutRules,
			fircNewFlowMatch+" -j FEXT_"+KeenDNSGroupID,
			fircUnmarkedUDPMatch+" -j FEXT_"+KeenDNSGroupID,
		)
	}
	if needXHTTP {
		for _, spec := range activeSpecs {
			desiredOutRules = append(desiredOutRules,
				fmt.Sprintf("-m conntrack --ctstate NEW --ctdir ORIGINAL -m mark --mark %s -j FEXT_%s", unclassifiedMarkStr, spec.id))
		}
		if len(activeSpecs) > 0 {
			desiredOutRules = append(desiredOutRules,
				fmt.Sprintf("-d 198.18.0.0/15 -m conntrack --ctstate NEW --ctdir ORIGINAL -m mark --mark %s -j MARK --set-xmark %s", unclassifiedMarkStr, activeSpecs[0].groupMark))
		}
	}
	if len(desiredOutRules) > 0 {
		desiredOutRules = append(desiredOutRules, fircSaveMarkRule)
	}

	if err := runIptables(ctx, "-t", "mangle", "-N", "FEXT_OUT"); err != nil {
		return errors.Join(recErr, err)
	}
	existingOut, listOutErr := readChainRuleList(ctx, "mangle", "FEXT_OUT")
	if listOutErr != nil {
		return errors.Join(recErr, listOutErr)
	}
	var outErr error
	if outErr != nil || fextOutSig != lastFextOutSig || !equalStringSlices(existingOut, desiredOutRules) {
		if listOutErr == nil && (len(existingOut) > 0 || (lastFextOutSig != "" && fextOutSig != lastFextOutSig)) {
			if !equalStringSlices(existingOut, desiredOutRules) || (lastFextOutSig != "" && fextOutSig != lastFextOutSig) {
				if fErr := runIptables(ctx, "-t", "mangle", "-F", "FEXT_OUT"); fErr != nil {
					outErr = errors.Join(outErr, fErr)
				}
			}
		}
		for _, ruleSpec := range desiredOutRules {
			outErr = errors.Join(outErr, ensureIptablesRule(ctx, "mangle", "FEXT_OUT", strings.Fields(ruleSpec)...))
		}
		if vErr := verifyChainExactOrder(ctx, "mangle", "FEXT_OUT", desiredOutRules); vErr != nil {
			outErr = errors.Join(outErr, vErr)
		}
		if outErr == nil {
			lastFextOutSig = fextOutSig
		} else {
			lastFextOutSig = ""
		}
	}
	recErr = errors.Join(recErr, outErr)
	if !needKeenDNS && outErr == nil {
		recErr = errors.Join(recErr, op.teardownKeenDNSCloudChainAndSet(ctx))
	}

	hasRedir, redirErr := syncNatMirrorChain(ctx, "FEXT_REDIR", "FIRC_REDIR")
	hasDnat, dnatErr := syncNatMirrorChain(ctx, "FEXT_DNAT", "FIRC_DNAT")
	fextNoutSig := fmt.Sprintf("fext_redir=%v;fext_dnat=%v", hasRedir, hasDnat)

	var desiredNoutRules []string
	if hasRedir {
		desiredNoutRules = append(desiredNoutRules,
			"-p tcp -m mark --mark 0x40000000/0x40000000 -m addrtype ! --dst-type LOCAL -j FEXT_REDIR")
	}
	if hasDnat {
		desiredNoutRules = append(desiredNoutRules,
			"-d 198.18.0.0/15 -j FEXT_DNAT")
	}

	if err := runIptables(ctx, "-t", "nat", "-N", "FEXT_NOUT"); err != nil {
		return errors.Join(recErr, err)
	}
	existingNout, listNoutErr := readChainRuleList(ctx, "nat", "FEXT_NOUT")
	if listNoutErr != nil {
		return errors.Join(recErr, listNoutErr)
	}
	var noutErr error
	if noutErr != nil || redirErr != nil || dnatErr != nil || fextNoutSig != lastFextNoutSig || !equalStringSlices(existingNout, desiredNoutRules) {
		noutErr = errors.Join(noutErr,
			deleteIptablesRuleIfPresent(ctx, "nat", "FEXT_NOUT", existingNout,
				"-p", "tcp", "-m", "mark", "--mark", "0x40000000/0x40000000",
				"-m", "addrtype", "!", "--dst-type", "LOCAL", "-j", "FIRC_REDIR"),
			deleteIptablesRuleIfPresent(ctx, "nat", "FEXT_NOUT", existingNout,
				"-d", "198.18.0.0/15", "-j", "FIRC_DNAT"),
		)
		if afterLegacy, rErr := readChainRuleList(ctx, "nat", "FEXT_NOUT"); rErr != nil {
			noutErr = errors.Join(noutErr, rErr)
		} else {
			existingNout = afterLegacy
		}
		if len(existingNout) > 0 && (!equalStringSlices(existingNout, desiredNoutRules) || (lastFextNoutSig != "" && fextNoutSig != lastFextNoutSig)) {
			if fErr := runIptables(ctx, "-t", "nat", "-F", "FEXT_NOUT"); fErr != nil {
				noutErr = errors.Join(noutErr, fErr)
			}
		}
		for _, ruleSpec := range desiredNoutRules {
			noutErr = errors.Join(noutErr, ensureIptablesRule(ctx, "nat", "FEXT_NOUT", strings.Fields(ruleSpec)...))
		}
		if vErr := verifyChainExactOrder(ctx, "nat", "FEXT_NOUT", desiredNoutRules); vErr != nil {
			noutErr = errors.Join(noutErr, vErr)
		}
		if noutErr == nil && redirErr == nil && dnatErr == nil {
			lastFextNoutSig = fextNoutSig
		} else {
			lastFextNoutSig = ""
		}
	}
	recErr = errors.Join(recErr, redirErr, dnatErr, noutErr)

	if mangleHookErr != nil {
		lastFextOutSig = ""
		recErr = errors.Join(recErr, mangleHookErr)
	} else if !mangleHookExists {
		if err := runIptables(ctx, "-t", "mangle", "-A", "OUTPUT", "-j", "FEXT_OUT"); err != nil {
			lastFextOutSig = ""
			recErr = errors.Join(recErr, err)
		}
	}
	if natHookErr != nil {
		lastFextNoutSig = ""
		recErr = errors.Join(recErr, natHookErr)
	} else if !natHookExists {
		if err := runIptables(ctx, "-t", "nat", "-A", "OUTPUT", "-j", "FEXT_NOUT"); err != nil {
			lastFextNoutSig = ""
			recErr = errors.Join(recErr, err)
		}
	}
	if recErr == nil {
		if mExists, mErr := checkIptablesRuleExists(ctx, "mangle", "OUTPUT", "-j", "FEXT_OUT"); mErr != nil {
			recErr = errors.Join(recErr, mErr)
		} else if !mExists {
			recErr = errors.Join(recErr, errors.New("mangle OUTPUT -j FEXT_OUT missing after reconcile"))
		}
		if nExists, nErr := checkIptablesRuleExists(ctx, "nat", "OUTPUT", "-j", "FEXT_NOUT"); nErr != nil {
			recErr = errors.Join(recErr, nErr)
		} else if !nExists {
			recErr = errors.Join(recErr, errors.New("nat OUTPUT -j FEXT_NOUT missing after reconcile"))
		}
	}
	return recErr
}

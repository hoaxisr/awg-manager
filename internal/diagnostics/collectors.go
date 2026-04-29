package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/pingcheck"
	"github.com/hoaxisr/awg-manager/internal/rci"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

var (
	reProxyListen = regexp.MustCompile(`listen=127\.0\.0\.1:(\d+)`)
	reProxyRx     = regexp.MustCompile(`rx=(\d+)`)
	reProxyTx     = regexp.MustCompile(`tx=(\d+)`)
	reProxyBind   = regexp.MustCompile(`BIND=(\S+)`)
)

func (r *Runner) collectSystem(ctx context.Context) SystemInfo {
	info := SystemInfo{
		AppVersion:    r.deps.AppVersion,
		KeeneticOS:    string(osdetect.Get()),
		IsOS5:         osdetect.Is5(),
		Arch:          runtime.GOARCH,
		TotalMemoryMB: osdetect.GetTotalMemoryMB(),
	}

	if r.deps.Backend != nil {
		info.Backend = r.deps.Backend.Type().String()
	}

	// Kernel module status — delegate to Loader (single source of truth)
	if r.deps.KmodLoader != nil {
		info.KernelModule.Exists = r.deps.KmodLoader.ModuleExists()
		info.KernelModule.Loaded = r.deps.KmodLoader.IsLoaded()
	}

	// Uptime
	if result, err := exec.Run(ctx, "cat", "/proc/uptime"); err == nil {
		fields := strings.Fields(result.Stdout)
		if len(fields) > 0 {
			var secs float64
			if _, err := fmt.Sscanf(fields[0], "%f", &secs); err == nil {
				d := time.Duration(secs) * time.Second
				hours := int(d.Hours())
				days := hours / 24
				hours = hours % 24
				mins := int(d.Minutes()) % 60
				info.Uptime = fmt.Sprintf("%dd %dh %dm", days, hours, mins)
			}
		}
	}

	return info
}

func (r *Runner) collectWAN(ctx context.Context) WANInfo {
	info := WANInfo{
		Interfaces: make(map[string]WANIfaceInfo),
	}

	// WAN model from tunnel service
	model := r.deps.TunnelService.WANModel()
	uiIfaces := model.ForUI()
	for _, iface := range uiIfaces {
		info.Interfaces[iface.Name] = WANIfaceInfo{
			Up:    iface.Up,
			Label: iface.Label,
		}
	}
	info.AnyUp = model.AnyUp()

	// NDMS route table (what AWG Manager sees via RCI)
	if r.deps.NDMSQueries != nil {
		routes, err := r.deps.NDMSQueries.Routes.List(ctx)
		if err != nil {
			info.NDMSRouteTable = "error: " + err.Error()
		} else {
			info.NDMSRouteTable = formatRouteTable(routes)
		}
	}

	// Raw kernel network state
	if result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show"); err == nil {
		info.IPRouteTable = result.Stdout
	}
	if result, err := exec.Run(ctx, "/opt/sbin/ip", "addr", "show"); err == nil {
		info.IPAddr = result.Stdout
	}

	return info
}

func (r *Runner) collectTunnels(ctx context.Context) []TunnelInfo {
	tunnels, err := r.deps.TunnelService.List(ctx)
	if err != nil {
		return nil
	}

	// PingCheck runtime status — one call, build map
	var pcMap map[string]pingcheck.TunnelStatus
	if r.deps.PingCheckFacade != nil {
		pcStatuses := r.deps.PingCheckFacade.GetStatus()
		pcMap = make(map[string]pingcheck.TunnelStatus, len(pcStatuses))
		for _, s := range pcStatuses {
			pcMap[s.TunnelID] = s
		}
	}

	var infos []TunnelInfo
	for _, t := range tunnels {
		stored, _ := r.deps.TunnelStore.Get(t.ID)

		backend := "kernel"
		if stored != nil && stored.Backend == "nativewg" {
			backend = "nativewg"
		}

		// Resolve interface names by backend
		var ifaceName, ndmsName string
		if backend == "nativewg" && stored != nil {
			names := nwg.NewNWGNames(stored.NWGIndex)
			ifaceName = names.IfaceName
			ndmsName = names.NDMSName
		} else {
			names := tunnel.NewNames(t.ID)
			ifaceName = names.IfaceName
			ndmsName = names.NDMSName
		}

		status := t.State.String()

		var ispInterface string
		if stored != nil {
			ispInterface = stored.ISPInterface
		}
		resolvedISP := r.deps.TunnelService.GetResolvedISP(t.ID)

		ti := TunnelInfo{
			ID:                   t.ID,
			Name:                 t.Name,
			Status:               status,
			Enabled:              t.Enabled,
			Backend:              backend,
			InterfaceName:        ifaceName,
			ISPInterface:         ispInterface,
			ResolvedISPInterface: resolvedISP,
			DefaultRoute:         t.DefaultRoute,
		}

		// NDMS interface state (using resolved ndmsName)
		// For nativewg, this output is reused for Connection data
		var ndmsJSON string
		if r.deps.NDMSTransport != nil && ndmsName != "" {
			if raw, err := r.deps.NDMSTransport.GetRaw(ctx, "/show/interface/"+ndmsName); err == nil {
				ndmsJSON = string(raw)
				ti.Interface.NDMSState = ndmsJSON
			}
		}

		// Kernel addresses
		if result, err := exec.Run(ctx, "/opt/sbin/ip", "addr", "show", "dev", ifaceName); err == nil {
			ti.Interface.KernelAddr = extractAddr(result.Stdout, "inet ")
			ti.Interface.KernelIPv6 = extractAddr(result.Stdout, "inet6 ")
		}

		// Connection info — backend-specific
		if backend == "nativewg" {
			r.collectNativeWGConnection(&ti, ndmsJSON)
		} else {
			r.collectKernelConnection(ctx, &ti, ifaceName)
		}

		// Routes and Firewall — kernel only
		if backend != "nativewg" {
			r.collectKernelRoutesAndFirewall(ctx, &ti, ifaceName)
		}

		// Proxy info — nativewg only
		if backend == "nativewg" && stored != nil {
			ti.Proxy = r.collectProxyInfo(ctx, stored)
		}

		// Settings from storage
		if stored != nil {
			ti.Settings = buildTunnelSettings(stored)
		}

		// PingCheck runtime status
		if pcMap != nil {
			if ps, ok := pcMap[t.ID]; ok {
				ti.PingCheck = &PingCheckInfo{
					Status:          ps.Status,
					Method:          ps.Method,
					FailCount:       ps.FailCount,
					FailThreshold:   ps.FailThreshold,
					RestartCount:    ps.RestartCount,
					SuccessCount:    ps.SuccessCount,
				}
			}
		}

		// Config file (sanitized)
		if stored != nil {
			ti.ConfigFile = sanitizeConfig(stored)
		}

		infos = append(infos, ti)
	}

	return infos
}

// collectKernelConnection populates Connection from awg show.
func (r *Runner) collectKernelConnection(ctx context.Context, ti *TunnelInfo, ifaceName string) {
	result, err := exec.Run(ctx, "/opt/sbin/awg", "show", ifaceName)
	if err != nil {
		return
	}
	ti.Connection.RawOutput = result.Stdout
	ti.Connection.LatestHandshake = extractField(result.Stdout, "latest handshake:")
	ti.Connection.TransferRx = extractTransfer(result.Stdout, "received")
	ti.Connection.TransferTx = extractTransfer(result.Stdout, "sent")
}

// collectNativeWGConnection populates Connection from RCI show interface JSON.
// ndmsJSON is the already-collected JSON from the main loop (avoids duplicate RCI call).
func (r *Runner) collectNativeWGConnection(ti *TunnelInfo, ndmsJSON string) {
	ti.Connection.RawOutput = ndmsJSON

	if ndmsJSON == "" {
		return
	}

	var wg rci.WGInterface
	if err := json.Unmarshal([]byte(ndmsJSON), &wg); err != nil {
		return
	}

	if wg.WireGuard != nil && len(wg.WireGuard.Peer) > 0 {
		peer := wg.WireGuard.Peer[0]

		ts := peer.LastHandshake
		if ts > 0 && ts < rci.NeverHandshake {
			hsTime := time.Unix(ts, 0)
			ago := time.Since(hsTime).Round(time.Second)
			ti.Connection.LatestHandshake = fmt.Sprintf("%s ago", ago)
		} else {
			ti.Connection.LatestHandshake = "(none)"
		}

		if peer.RxBytes > 0 {
			ti.Connection.TransferRx = formatBytes(peer.RxBytes)
		}
		if peer.TxBytes > 0 {
			ti.Connection.TransferTx = formatBytes(peer.TxBytes)
		}
	}

	// Parse connected timestamp from raw JSON
	if wg.Connected != nil {
		var connStr string
		if err := json.Unmarshal(wg.Connected, &connStr); err == nil {
			if ts, err := strconv.ParseInt(connStr, 10, 64); err == nil && ts > 0 {
				ti.Connection.ConnectedAt = time.Unix(ts, 0).UTC().Format(time.RFC3339)
			}
		} else {
			// Try as integer
			var connInt int64
			if err := json.Unmarshal(wg.Connected, &connInt); err == nil && connInt > 0 {
				ti.Connection.ConnectedAt = time.Unix(connInt, 0).UTC().Format(time.RFC3339)
			}
		}
	}
}

// collectKernelRoutesAndFirewall populates Routes and Firewall for kernel tunnels.
func (r *Runner) collectKernelRoutesAndFirewall(ctx context.Context, ti *TunnelInfo, ifaceName string) {
	if result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show"); err == nil {
		endpointIP := extractEndpointIP(ti.Connection.RawOutput)
		ti.Routes.EndpointRoute = extractEndpointRoute(result.Stdout, endpointIP)
		ti.Routes.DefaultRoute = extractDefaultRoute(result.Stdout, ifaceName)
	}
	if result, err := exec.Run(ctx, "/opt/sbin/iptables", "-t", "mangle", "-S"); err == nil {
		ti.Firewall.IPTablesRules = filterRules(result.Stdout, ifaceName)
	}
}

// collectProxyInfo collects awg_proxy data for a nativewg tunnel.
func (r *Runner) collectProxyInfo(ctx context.Context, stored *storage.AWGTunnel) *ProxyInfo {
	pi := &ProxyInfo{}

	versionData, err := os.ReadFile("/proc/awg_proxy/version")
	if err != nil {
		pi.Loaded = false
		return pi
	}
	pi.Loaded = true
	pi.Version = strings.TrimSpace(string(versionData))

	listData, err := os.ReadFile("/proc/awg_proxy/list")
	if err != nil {
		return pi
	}

	endpointHost, endpointPort, _ := net.SplitHostPort(stored.Peer.Endpoint)
	if net.ParseIP(endpointHost) == nil {
		if resolved, err := netutil.ResolveHost(endpointHost); err == nil {
			endpointHost = resolved
		}
	}
	targetPrefix := endpointHost + ":" + endpointPort + " "

	for _, line := range strings.Split(string(listData), "\n") {
		if !strings.HasPrefix(line, targetPrefix) {
			continue
		}
		pi.RawListEntry = line
		pi.EndpointMatch = true

		if m := reProxyListen.FindStringSubmatch(line); m != nil {
			pi.ListenPort, _ = strconv.Atoi(m[1])
		}
		if m := reProxyRx.FindStringSubmatch(line); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				pi.RxBytes = formatBytes(n)
			}
		}
		if m := reProxyTx.FindStringSubmatch(line); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				pi.TxBytes = formatBytes(n)
			}
		}
		if m := reProxyBind.FindStringSubmatch(line); m != nil {
			pi.BindIface = m[1]
		}
		break
	}

	// Actual route: ip route get endpoint
	if endpointHost != "" && net.ParseIP(endpointHost) != nil {
		if result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "get", endpointHost); err == nil {
			pi.ActualRouteIface = extractRouteGetDev(result.Stdout)
			pi.ActualRouteVia = extractRouteGetVia(result.Stdout)
		}
	}

	// Compare with wanted ISP
	pi.WantedISP = stored.ISPInterface
	if pi.WantedISP == "" {
		pi.RouteMatch = true // auto mode
	} else if pi.ActualRouteIface != "" {
		resolvedISP := r.deps.TunnelService.GetResolvedISP(stored.ID)
		pi.RouteMatch = pi.ActualRouteIface == resolvedISP
	}

	return pi
}

// buildTunnelSettings creates TunnelSettings from stored tunnel data.
func buildTunnelSettings(stored *storage.AWGTunnel) TunnelSettings {
	ts := TunnelSettings{
		MTU:               stored.Interface.MTU,
		DNS:               stored.Interface.DNS,
		Qlen:              stored.Interface.Qlen,
		Jc:                stored.Interface.Jc,
		Jmin:              stored.Interface.Jmin,
		Jmax:              stored.Interface.Jmax,
		S1:                stored.Interface.S1,
		S2:                stored.Interface.S2,
		S3:                stored.Interface.S3,
		S4:                stored.Interface.S4,
		H1:                stored.Interface.H1,
		H2:                stored.Interface.H2,
		H3:                stored.Interface.H3,
		H4:                stored.Interface.H4,
		I1:                stored.Interface.I1,
		I2:                stored.Interface.I2,
		I3:                stored.Interface.I3,
		I4:                stored.Interface.I4,
		I5:                stored.Interface.I5,
		ISPInterfaceLabel: stored.ISPInterfaceLabel,
	}
	if stored.PingCheck != nil {
		ts.PingCheckConfig = &PingCheckConfig{
			Enabled:       stored.PingCheck.Enabled,
			Method:        stored.PingCheck.Method,
			Target:        stored.PingCheck.Target,
			Interval:      stored.PingCheck.Interval,
			FailThreshold: stored.PingCheck.FailThreshold,
			DeadInterval:  stored.PingCheck.DeadInterval,
		}
	}
	return ts
}

// formatBytes formats bytes into human-readable string.
func formatBytes(b int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GiB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MiB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KiB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// extractRouteGetDev extracts "dev XXX" from ip route get output.
func extractRouteGetDev(output string) string {
	parts := strings.Fields(output)
	for i, p := range parts {
		if p == "dev" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// extractRouteGetVia extracts "via XXX" from ip route get output.
func extractRouteGetVia(output string) string {
	parts := strings.Fields(output)
	for i, p := range parts {
		if p == "via" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (r *Runner) collectLogs() []logging.LogEntry {
	if r.deps.LogService == nil {
		return nil
	}
	// Get all entries (all categories, all levels)
	return r.deps.LogService.GetLogs("", "")
}

// --- Helpers ---

func extractAddr(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}
	return ""
}

func extractField(output, field string) string {
	for _, line := range strings.Split(output, "\n") {
		if idx := strings.Index(line, field); idx >= 0 {
			return strings.TrimSpace(line[idx+len(field):])
		}
	}
	return ""
}

func extractTransfer(output, direction string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "transfer:") {
			// "transfer: 1.2 GiB received, 340 MiB sent"
			parts := strings.Split(line, ",")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if strings.Contains(p, direction) {
					return strings.TrimSuffix(strings.TrimSuffix(p, " "+direction), "transfer: ")
				}
			}
		}
	}
	return ""
}

func extractEndpointIP(awgShow string) string {
	for _, line := range strings.Split(awgShow, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "endpoint:") {
			ep := strings.TrimSpace(strings.TrimPrefix(line, "endpoint:"))
			host, _, err := net.SplitHostPort(ep)
			if err == nil {
				return host
			}
		}
	}
	return ""
}

func extractEndpointRoute(routeTable, endpointIP string) string {
	if endpointIP == "" {
		return ""
	}
	for _, line := range strings.Split(routeTable, "\n") {
		line = strings.TrimSpace(line)
		// Match "IP/32 ..." (DHCP) or "IP dev ..." (PPPoE, no /32 suffix)
		if strings.HasPrefix(line, endpointIP+"/") || strings.HasPrefix(line, endpointIP+" ") {
			return line
		}
	}
	return ""
}

func extractDefaultRoute(routeTable, ifaceName string) string {
	for _, line := range strings.Split(routeTable, "\n") {
		if strings.HasPrefix(line, "default") && strings.Contains(line, ifaceName) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func filterRules(iptablesOutput, ifaceName string) []string {
	var rules []string
	for _, line := range strings.Split(iptablesOutput, "\n") {
		if strings.Contains(line, ifaceName) {
			rules = append(rules, strings.TrimSpace(line))
		}
	}
	return rules
}

// formatRouteTable formats NDMS IPv4 routes for diagnostics output.
// Matches the legacy tunnel/ndms.ClientImpl.DumpIPv4Routes format.
func formatRouteTable(routes []ndms.Route) string {
	var sb strings.Builder
	for _, r := range routes {
		gw := r.Gateway
		if gw == "" {
			gw = "*"
		}
		fmt.Fprintf(&sb, "%s via %s dev %s\n", r.Destination, gw, r.Interface)
	}
	return sb.String()
}

// sanitizeConfig returns a config summary from stored tunnel data without private keys.
func sanitizeConfig(stored *storage.AWGTunnel) string {
	var sb strings.Builder
	sb.WriteString("[Interface]\n")
	sb.WriteString(fmt.Sprintf("Address = %s\n", stored.Interface.Address))
	if stored.Interface.MTU > 0 {
		sb.WriteString(fmt.Sprintf("MTU = %d\n", stored.Interface.MTU))
	}
	sb.WriteString("PrivateKey = [REDACTED]\n")
	sb.WriteString("\n[Peer]\n")
	sb.WriteString(fmt.Sprintf("PublicKey = %s\n", stored.Peer.PublicKey))
	if stored.Peer.PresharedKey != "" {
		sb.WriteString("PresharedKey = [REDACTED]\n")
	}
	sb.WriteString(fmt.Sprintf("Endpoint = %s\n", stored.Peer.Endpoint))
	sb.WriteString(fmt.Sprintf("AllowedIPs = %s\n", strings.Join(stored.Peer.AllowedIPs, ", ")))
	if stored.Peer.PersistentKeepalive > 0 {
		sb.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", stored.Peer.PersistentKeepalive))
	}
	return sb.String()
}

// collectAWGProxyModule reads kmod awg-proxy state and greps dmesg.
// Never panics on missing /proc files (kernel-backend without proxy);
// returns Loaded=false in that case. dmesg is grepped without a time
// window because the kernel does not expose reliable timestamps in our
// environment, and the support team wants the complete error history.
func (r *Runner) collectAWGProxyModule(ctx context.Context) AWGProxyModule {
	mod := AWGProxyModule{}

	// /proc/awg_proxy/version → Loaded + Version
	if versionData, err := os.ReadFile("/proc/awg_proxy/version"); err == nil {
		mod.Loaded = true
		mod.Version = strings.TrimSpace(string(versionData))
	}

	// /proc/awg_proxy/list → RawList + EndpointCount
	if listData, err := os.ReadFile("/proc/awg_proxy/list"); err == nil {
		mod.RawList = string(listData)
		count := 0
		for _, line := range strings.Split(mod.RawList, "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
		mod.EndpointCount = count
	}

	// dmesg | grep -i awg_proxy — every matched line. No tail, no time
	// window: the kernel does not expose reliable timestamps in our
	// environment, and support wants full error history.
	if result, err := exec.Shell(ctx, "dmesg | grep -i awg_proxy"); err == nil {
		raw := strings.TrimSpace(result.Stdout)
		if raw != "" {
			for _, line := range strings.Split(raw, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					mod.DmesgLines = append(mod.DmesgLines, line)
				}
			}
		}
	}

	return mod
}

// bootHealthInput is the per-tunnel slice needed by computeBootHealth.
// Extracted from awgStore + stateMgr; isolated into its own struct so
// computeBootHealth can be tested without mocks.
type bootHealthInput struct {
	ID              string
	Name            string
	Backend         string
	Enabled         bool
	AutoStart       bool
	Status          string // "running" | "stopped" | etc.
	StoredStartedAt string // RFC3339, may be empty
}

// bootHealthGracePeriod is how many seconds after daemon start we wait
// before considering an enabled tunnel "not started".
const bootHealthGracePeriod = 120

// computeBootHealth is a pure function that computes BootHealth from inputs.
// No I/O — all required data is passed in bootHealthInput.
// This allows logic to be tested in isolation.
func computeBootHealth(inputs []bootHealthInput) BootHealth {
	now := time.Now()
	uptimeSec := int(now.Sub(processStartedAt).Seconds())

	bh := BootHealth{
		DaemonStartedAt: processStartedAt,
		DaemonUptimeSec: uptimeSec,
		GracePeriodSec:  bootHealthGracePeriod,
	}

	expectedSet := make(map[string]bootHealthInput)
	for _, in := range inputs {
		if in.Enabled && in.AutoStart {
			bh.ExpectedRunning = append(bh.ExpectedRunning, in.ID)
			expectedSet[in.ID] = in
		}
		if in.Status == "running" {
			bh.ActualRunning = append(bh.ActualRunning, in.ID)
		}
	}

	if uptimeSec < bootHealthGracePeriod {
		// grace period not yet elapsed — do not draw conclusions
		return bh
	}

	actualSet := make(map[string]bool)
	for _, id := range bh.ActualRunning {
		actualSet[id] = true
	}

	for id, in := range expectedSet {
		if actualSet[id] {
			continue
		}
		bh.NotStartedOnBoot = append(bh.NotStartedOnBoot, TunnelBootIssue{
			TunnelID:        in.ID,
			TunnelName:      in.Name,
			Backend:         in.Backend,
			Enabled:         in.Enabled,
			AutoStart:       in.AutoStart,
			StoredStartedAt: in.StoredStartedAt,
			Reason:          "never_started",
		})
	}

	return bh
}

// collectBootHealth assembles a per-tunnel snapshot and computes BootHealth.
// Uses TunnelService.List for state + TunnelStore for StartedAt.
func (r *Runner) collectBootHealth(ctx context.Context) BootHealth {
	tunnels, err := r.deps.TunnelService.List(ctx)
	if err != nil {
		// service unavailable — return minimally populated BootHealth
		return computeBootHealth(nil)
	}

	inputs := make([]bootHealthInput, 0, len(tunnels))
	for _, t := range tunnels {
		var startedAt string
		if stored, _ := r.deps.TunnelStore.Get(t.ID); stored != nil {
			startedAt = stored.StartedAt
		}
		inputs = append(inputs, bootHealthInput{
			ID:              t.ID,
			Name:            t.Name,
			Backend:         t.Backend,
			Enabled:         t.Enabled,
			AutoStart:       t.AutoStart,
			Status:          t.State.String(),
			StoredStartedAt: startedAt,
		})
	}

	return computeBootHealth(inputs)
}

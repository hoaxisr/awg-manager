package diagnostics

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
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

	// Kernel module status
	if result, err := exec.Run(ctx, "lsmod"); err == nil {
		info.KernelModule.Loaded = strings.Contains(result.Stdout, "amneziawg")
	}
	if result, err := exec.Run(ctx, "ls", "/lib/modules"); err == nil {
		info.KernelModule.Exists = strings.Contains(result.Stdout, "amneziawg")
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

	// Raw network state
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

	var infos []TunnelInfo
	for _, t := range tunnels {
		names := tunnel.NewNames(t.ID)
		status := t.State.String()

		// Get stored tunnel data for ISP interface and config details
		stored, _ := r.deps.TunnelStore.Get(t.ID)

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
			ISPInterface:         ispInterface,
			ResolvedISPInterface: resolvedISP,
			DefaultRoute:         t.DefaultRoute,
		}

		// NDMS interface state
		if r.deps.NDMSClient != nil && names.NDMSName != "" {
			if result, err := exec.Run(ctx, "ndmc", "-c", "show interface "+names.NDMSName); err == nil {
				ti.Interface.NDMSState = result.Stdout
			}
		}

		// Kernel addresses
		if result, err := exec.Run(ctx, "/opt/sbin/ip", "addr", "show", "dev", names.IfaceName); err == nil {
			ti.Interface.KernelAddr = extractAddr(result.Stdout, "inet ")
			ti.Interface.KernelIPv6 = extractAddr(result.Stdout, "inet6 ")
		}

		// awg show
		if result, err := exec.Run(ctx, "/opt/sbin/awg", "show", names.IfaceName); err == nil {
			ti.WireGuard.AWGShow = result.Stdout
			ti.WireGuard.LatestHandshake = extractField(result.Stdout, "latest handshake:")
			ti.WireGuard.TransferRx = extractTransfer(result.Stdout, "received")
			ti.WireGuard.TransferTx = extractTransfer(result.Stdout, "sent")
		}

		// Routes
		if result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show"); err == nil {
			endpointIP := extractEndpointIP(ti.WireGuard.AWGShow)
			ti.Routes.EndpointRoute = extractEndpointRoute(result.Stdout, endpointIP)
			ti.Routes.DefaultRoute = extractDefaultRoute(result.Stdout, names.IfaceName)
		}

		// Firewall
		if result, err := exec.Run(ctx, "/opt/sbin/iptables", "-t", "mangle", "-S"); err == nil {
			ti.Firewall.IPTablesRules = filterRules(result.Stdout, names.IfaceName)
		}

		// Config file (sanitized -- private key removed)
		if stored != nil {
			ti.ConfigFile = sanitizeConfig(stored)
		}

		infos = append(infos, ti)
	}

	return infos
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

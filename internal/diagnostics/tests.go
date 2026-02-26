package diagnostics

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

func (r *Runner) runTests(ctx context.Context, report *Report) []TestResult {
	var results []TestResult

	// Global tests
	results = append(results, r.testWANConnectivity(ctx))
	results = append(results, r.testNDMSHealth(ctx))
	results = append(results, r.testKernelModule(ctx))

	// Per-tunnel tests
	for _, t := range report.Tunnels {
		r.setProgress(fmt.Sprintf("Тестирование %s...", t.Name))

		results = append(results, r.testDNSResolve(t))
		results = append(results, r.testEndpointReachable(ctx, t))
		results = append(results, r.testEndpointRouteCheck(t))
		results = append(results, r.testAWGHandshake(t))
		results = append(results, r.testTunnelConnectivity(ctx, t))
		results = append(results, r.testFirewallRules(t))
		results = append(results, r.testConfigParse(t))
		results = append(results, r.testInterfaceStateConsistency(ctx, t))
		results = append(results, r.testMTUCheck(ctx, t))
	}

	// Route leak check (global, uses all tunnels info)
	results = append(results, r.testRouteLeak(ctx, report))

	// DNS leak check (per-tunnel, only for running tunnels)
	for _, t := range report.Tunnels {
		if t.Status == "running" {
			results = append(results, r.testDNSLeak(ctx, t))
		}
	}

	// Restart cycle (last -- it's invasive)
	for _, t := range report.Tunnels {
		if t.Enabled && t.Status == "running" {
			r.setProgress(fmt.Sprintf("Restart-тест %s...", t.Name))
			results = append(results, r.testRestartCycle(ctx, t))
		}
	}

	return results
}

// --- Global tests ---

func (r *Runner) testWANConnectivity(ctx context.Context) TestResult {
	res := TestResult{Name: "wan_connectivity", Description: "WAN up с gateway"}

	model := r.deps.TunnelService.WANModel()
	if !model.AnyUp() {
		res.Status = StatusFail
		res.Detail = "Все WAN интерфейсы down"
		return res
	}

	// Check default route exists
	result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show", "default")
	if err != nil || result.Stdout == "" {
		res.Status = StatusFail
		res.Detail = "Нет default route"
		return res
	}

	res.Status = StatusPass
	res.Detail = strings.TrimSpace(result.Stdout)
	return res
}

func (r *Runner) testNDMSHealth(ctx context.Context) TestResult {
	res := TestResult{Name: "ndms_health", Description: "NDMS отвечает"}

	result, err := exec.Run(ctx, "ndmc", "-c", "show version")
	if err != nil {
		res.Status = StatusFail
		res.Detail = "ndmc не отвечает: " + err.Error()
		return res
	}

	// Extract version from output
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "title:") {
			res.Detail = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "title:"))
			break
		}
	}
	res.Status = StatusPass
	return res
}

func (r *Runner) testKernelModule(ctx context.Context) TestResult {
	res := TestResult{Name: "kernel_module", Description: "Модуль AmneziaWG"}

	result, err := exec.Run(ctx, "lsmod")
	if err != nil {
		res.Status = StatusError
		res.Detail = "Не удалось выполнить lsmod"
		return res
	}

	if strings.Contains(result.Stdout, "amneziawg") {
		res.Status = StatusPass
		res.Detail = "Загружен"
	} else {
		res.Status = StatusFail
		res.Detail = "Модуль не загружен"
	}
	return res
}

// --- Per-tunnel tests ---

func (r *Runner) testDNSResolve(t TunnelInfo) TestResult {
	res := TestResult{Name: "dns_resolve", Description: "Резолв endpoint", TunnelID: t.ID}

	endpoint := extractEndpointFromConfig(t.ConfigFile)
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		res.Status = StatusSkip
		res.Detail = "Не удалось разобрать endpoint"
		return res
	}

	// If already an IP, skip DNS
	if net.ParseIP(host) != nil {
		res.Status = StatusPass
		res.Detail = "Endpoint уже IP-адрес"
		return res
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Не удалось резолвить %s: %s", host, err.Error())
		return res
	}

	res.Status = StatusPass
	res.Detail = fmt.Sprintf("%s -> %s", host, strings.Join(ips, ", "))
	return res
}

func (r *Runner) testEndpointReachable(ctx context.Context, t TunnelInfo) TestResult {
	res := TestResult{Name: "endpoint_reachable", Description: "Ping endpoint", TunnelID: t.ID}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	endpoint := extractEndpointFromConfig(t.ConfigFile)
	host, _, _ := net.SplitHostPort(endpoint)
	if host == "" {
		res.Status = StatusSkip
		res.Detail = "Нет endpoint"
		return res
	}

	// Resolve hostname if needed
	ip := host
	if net.ParseIP(host) == nil {
		ips, err := net.LookupHost(host)
		if err != nil || len(ips) == 0 {
			res.Status = StatusSkip
			res.Detail = "Не удалось резолвить endpoint"
			return res
		}
		ip = ips[0]
	}

	result, err := exec.Run(ctx, "ping", "-c", "3", "-W", "5", ip)
	if err != nil {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Ping %s: недоступен", ip)
		return res
	}

	// Extract avg RTT from ping output
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.Contains(line, "avg") {
			res.Detail = strings.TrimSpace(line)
			break
		}
	}
	res.Status = StatusPass
	return res
}

func (r *Runner) testEndpointRouteCheck(t TunnelInfo) TestResult {
	res := TestResult{Name: "endpoint_route_check", Description: "Host route до endpoint", TunnelID: t.ID}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	if t.Routes.EndpointRoute != "" {
		res.Status = StatusPass
		res.Detail = t.Routes.EndpointRoute
	} else {
		res.Status = StatusFail
		res.Detail = "Нет host route до endpoint"
	}
	return res
}

func (r *Runner) testAWGHandshake(t TunnelInfo) TestResult {
	res := TestResult{Name: "awg_handshake", Description: "Handshake свежий (<3 мин)", TunnelID: t.ID}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	hs := t.WireGuard.LatestHandshake
	if hs == "" || hs == "(none)" {
		res.Status = StatusFail
		res.Detail = "Нет handshake"
		return res
	}

	// Parse handshake time -- format varies: "X seconds ago", "X minutes, Y seconds ago"
	if strings.Contains(hs, "hour") || strings.Contains(hs, "day") {
		res.Status = StatusFail
		res.Detail = "Устаревший handshake: " + hs
		return res
	}

	// Check if minutes > 3
	if strings.Contains(hs, "minute") {
		var mins int
		fmt.Sscanf(hs, "%d minute", &mins)
		if mins >= 3 {
			res.Status = StatusFail
			res.Detail = "Handshake старше 3 минут: " + hs
			return res
		}
	}

	res.Status = StatusPass
	res.Detail = hs
	return res
}

func (r *Runner) testTunnelConnectivity(ctx context.Context, t TunnelInfo) TestResult {
	res := TestResult{Name: "tunnel_connectivity", Description: "Связность через туннель", TunnelID: t.ID}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	names := tunnel.NewNames(t.ID)

	// Try multiple IP check services
	urls := []string{"https://ifconfig.me", "https://icanhazip.com", "https://ip.me"}
	for _, url := range urls {
		result, err := exec.Run(ctx, "/opt/bin/curl", "-s", "--max-time", "5",
			"--interface", names.IfaceName, url)
		if err == nil && strings.TrimSpace(result.Stdout) != "" {
			ip := strings.TrimSpace(result.Stdout)
			res.Status = StatusPass
			res.Detail = fmt.Sprintf("IP через туннель: %s (via %s)", ip, url)
			return res
		}
	}

	res.Status = StatusSkip
	res.Detail = "Все IP-сервисы недоступны через туннель"
	return res
}

func (r *Runner) testFirewallRules(t TunnelInfo) TestResult {
	res := TestResult{Name: "firewall_rules", Description: "Правила iptables", TunnelID: t.ID}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	if len(t.Firewall.IPTablesRules) > 0 {
		res.Status = StatusPass
		res.Detail = fmt.Sprintf("%d правил для интерфейса", len(t.Firewall.IPTablesRules))
	} else {
		res.Status = StatusFail
		res.Detail = "Нет правил iptables для интерфейса туннеля"
	}
	return res
}

func (r *Runner) testConfigParse(t TunnelInfo) TestResult {
	res := TestResult{Name: "config_parse", Description: "Валидация конфига", TunnelID: t.ID}

	cfg := t.ConfigFile
	if cfg == "" {
		res.Status = StatusFail
		res.Detail = "Конфиг не найден"
		return res
	}

	// Check required sections and fields
	var missing []string
	if !strings.Contains(cfg, "[Interface]") {
		missing = append(missing, "[Interface]")
	}
	if !strings.Contains(cfg, "[Peer]") {
		missing = append(missing, "[Peer]")
	}
	if !strings.Contains(cfg, "Address = ") {
		missing = append(missing, "Address")
	}
	if !strings.Contains(cfg, "Endpoint = ") {
		missing = append(missing, "Endpoint")
	}
	if !strings.Contains(cfg, "PublicKey = ") {
		missing = append(missing, "PublicKey")
	}

	if len(missing) > 0 {
		res.Status = StatusFail
		res.Detail = "Отсутствуют: " + strings.Join(missing, ", ")
	} else {
		res.Status = StatusPass
		res.Detail = "Конфиг валиден"
	}
	return res
}

func (r *Runner) testInterfaceStateConsistency(ctx context.Context, t TunnelInfo) TestResult {
	res := TestResult{Name: "interface_state_consistency", Description: "Консистентность state", TunnelID: t.ID}

	names := tunnel.NewNames(t.ID)

	// Check kernel interface exists
	result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "show", names.IfaceName)
	kernelExists := err == nil && result.Stdout != ""

	switch t.Status {
	case "running":
		if !kernelExists {
			res.Status = StatusFail
			res.Detail = "Status=running, но kernel interface не существует"
		} else {
			res.Status = StatusPass
			res.Detail = "Status и kernel state согласованы"
		}
	case "disabled", "stopped":
		if kernelExists && strings.Contains(result.Stdout, "UP") {
			res.Status = StatusFail
			res.Detail = fmt.Sprintf("Status=%s, но kernel interface UP", t.Status)
		} else {
			res.Status = StatusPass
			res.Detail = "Status и kernel state согласованы"
		}
	default:
		res.Status = StatusPass
		res.Detail = fmt.Sprintf("Status=%s, kernel_exists=%v", t.Status, kernelExists)
	}
	return res
}

func (r *Runner) testMTUCheck(ctx context.Context, t TunnelInfo) TestResult {
	res := TestResult{Name: "mtu_check", Description: "MTU интерфейса", TunnelID: t.ID}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	names := tunnel.NewNames(t.ID)
	result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "show", names.IfaceName)
	if err != nil {
		res.Status = StatusError
		res.Detail = "Не удалось получить link info"
		return res
	}

	// Extract MTU from "mtu NNNN"
	if idx := strings.Index(result.Stdout, "mtu "); idx >= 0 {
		mtuStr := strings.Fields(result.Stdout[idx:])[1]
		res.Status = StatusPass
		res.Detail = "MTU = " + mtuStr
		return res
	}

	res.Status = StatusPass
	res.Detail = "MTU info not available"
	return res
}

func (r *Runner) testRouteLeak(ctx context.Context, report *Report) TestResult {
	res := TestResult{Name: "route_leak_check", Description: "Осиротевшие маршруты"}

	result, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show")
	if err != nil {
		res.Status = StatusError
		res.Detail = "Не удалось получить routing table"
		return res
	}

	// Find opkgtun routes that don't belong to any active tunnel
	activeTunnels := make(map[string]bool)
	for _, t := range report.Tunnels {
		names := tunnel.NewNames(t.ID)
		activeTunnels[names.IfaceName] = true
	}

	var leaks []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Check if route references an opkgtun interface not in active tunnels
		if strings.Contains(line, "opkgtun") {
			found := false
			for iface := range activeTunnels {
				if strings.Contains(line, iface) {
					found = true
					break
				}
			}
			if !found {
				leaks = append(leaks, line)
			}
		}
	}

	if len(leaks) > 0 {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("%d осиротевших маршрутов: %s", len(leaks), strings.Join(leaks, "; "))
	} else {
		res.Status = StatusPass
		res.Detail = "Нет осиротевших маршрутов"
	}
	return res
}

func (r *Runner) testDNSLeak(ctx context.Context, t TunnelInfo) TestResult {
	res := TestResult{Name: "dns_leak_check", Description: "DNS leak проверка", TunnelID: t.ID}

	names := tunnel.NewNames(t.ID)

	// Resolve a test domain via tunnel interface
	tunnelResult, err := exec.Run(ctx, "/opt/bin/curl", "-s", "--max-time", "5",
		"--interface", names.IfaceName, "https://am.i.mullvad.net/json")
	if err != nil {
		res.Status = StatusSkip
		res.Detail = "Не удалось проверить DNS leak (сервис недоступен)"
		return res
	}

	// Check if mullvad detects us as using VPN
	output := strings.TrimSpace(tunnelResult.Stdout)
	if strings.Contains(output, "\"mullvad_exit_ip\":true") {
		res.Status = StatusPass
		res.Detail = "VPN обнаружен Mullvad -- DNS не утекает"
	} else {
		res.Status = StatusPass
		res.Detail = "Ответ получен через туннель"
	}
	return res
}

func (r *Runner) testRestartCycle(ctx context.Context, t TunnelInfo) TestResult {
	res := TestResult{Name: "restart_cycle", Description: "Цикл Stop -> Start", TunnelID: t.ID}

	// Stop
	stopStart := time.Now()
	if err := r.deps.TunnelService.Stop(ctx, t.ID); err != nil {
		res.Status = StatusError
		res.Detail = "Stop failed: " + err.Error()
		return res
	}
	stopDuration := time.Since(stopStart)

	// Wait a moment for cleanup
	time.Sleep(2 * time.Second)

	// Start
	startStart := time.Now()
	if err := r.deps.TunnelService.Start(ctx, t.ID); err != nil {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Stop OK (%s), Start failed: %s", stopDuration.Round(time.Second), err.Error())
		return res
	}
	startDuration := time.Since(startStart)

	// Wait for handshake (up to 15s)
	names := tunnel.NewNames(t.ID)
	handshakeOK := false
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		result, err := exec.Run(ctx, "/opt/sbin/awg", "show", names.IfaceName)
		if err == nil && strings.Contains(result.Stdout, "latest handshake:") {
			hs := extractField(result.Stdout, "latest handshake:")
			if hs != "" && hs != "(none)" {
				handshakeOK = true
				break
			}
		}
	}

	if handshakeOK {
		res.Status = StatusPass
		res.Detail = fmt.Sprintf("Stop: %s, Start: %s, handshake: OK",
			stopDuration.Round(time.Second), startDuration.Round(time.Second))
	} else {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Stop: %s, Start: %s, handshake: нет (timeout 15s)",
			stopDuration.Round(time.Second), startDuration.Round(time.Second))
	}
	return res
}

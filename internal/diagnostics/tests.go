package diagnostics

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

func (r *Runner) runTestsWithEvents(ctx context.Context, report *Report) []TestResult {
	var results []TestResult

	run := func(tr TestResult) {
		results = append(results, tr)
		r.emitTest(tr)
	}

	r.emitPhase("global_tests", "Проверка системы...")
	run(r.testWANConnectivity(ctx))
	run(r.testNDMSHealth(ctx))
	run(r.testKernelModule(ctx, report))

	for _, t := range report.Tunnels {
		r.emitPhase("tunnel_tests", fmt.Sprintf("Тестирование %s...", t.Name))

		run(r.testDNSResolve(t))
		run(r.testEndpointReachable(ctx, t))
		run(r.testEndpointRouteCheck(t))
		run(r.testAWGHandshake(t))
		run(r.testTunnelConnectivity(ctx, t))
		run(r.testFirewallRules(t))
		run(r.testConfigParse(t))
		run(r.testInterfaceStateConsistency(ctx, t))
		run(r.testMTUCheck(ctx, t))
		run(r.testProxyHealth(t))
		run(r.testPingCheckHealth(t))
	}

	r.emitPhase("cross_tunnel_tests", "Проверка маршрутов...")
	run(r.testRouteLeak(ctx, report))

	for _, t := range report.Tunnels {
		if t.Status == "running" {
			run(r.testDNSLeak(ctx, t))
		}
	}

	includeRestart := r.opts.Mode == ModeFull || r.opts.IncludeRestart
	if includeRestart {
		for _, t := range report.Tunnels {
			if t.Enabled && t.Status == "running" {
				r.emitPhase("restart_test", fmt.Sprintf("Restart-тест %s...", t.Name))
				run(r.testRestartCycle(ctx, t))
			}
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

func (r *Runner) testKernelModule(ctx context.Context, report *Report) TestResult {
	res := TestResult{Name: "kernel_module", Description: "Модули AmneziaWG"}

	hasKernel, hasNativeWG := false, false
	for _, t := range report.Tunnels {
		if t.Backend == "nativewg" {
			hasNativeWG = true
		} else {
			hasKernel = true
		}
	}

	if !hasKernel && !hasNativeWG {
		res.Status = StatusSkip
		res.Detail = "Нет туннелей"
		return res
	}

	var details []string
	allOK := true

	if hasKernel {
		result, err := exec.Run(ctx, "lsmod")
		if err != nil {
			details = append(details, "amneziawg: ошибка lsmod")
			allOK = false
		} else if strings.Contains(result.Stdout, "amneziawg") {
			details = append(details, "amneziawg: загружен")
		} else {
			details = append(details, "amneziawg: не загружен")
			allOK = false
		}
	}

	if hasNativeWG {
		if _, err := os.Stat("/proc/awg_proxy/version"); err == nil {
			vData, _ := os.ReadFile("/proc/awg_proxy/version")
			details = append(details, "awg_proxy: загружен (v"+strings.TrimSpace(string(vData))+")")
		} else {
			details = append(details, "awg_proxy: не загружен")
			allOK = false
		}
	}

	if allOK {
		res.Status = StatusPass
	} else {
		res.Status = StatusFail
	}
	res.Detail = strings.Join(details, "; ")
	return res
}

// --- Per-tunnel tests ---

func (r *Runner) testDNSResolve(t TunnelInfo) TestResult {
	res := TestResult{Name: "dns_resolve", Description: "Резолв endpoint", TunnelID: t.ID, TunnelName: t.Name}

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
	res := TestResult{Name: "endpoint_reachable", Description: "Ping endpoint", TunnelID: t.ID, TunnelName: t.Name}

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

	result, err := exec.Run(ctx, "ping", "-c", "3", ip)
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
	res := TestResult{Name: "endpoint_route_check", Description: "Host route до endpoint", TunnelID: t.ID, TunnelName: t.Name}

	if t.Backend == "nativewg" {
		res.Status = StatusSkip
		res.Detail = "NativeWG: маршрутизация проверяется в proxy_health"
		return res
	}

	if !osdetect.Is5() {
		res.Status = StatusSkip
		res.Detail = "OS4: маршрутизация не управляется оператором"
		return res
	}

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
	res := TestResult{Name: "awg_handshake", Description: "Handshake свежий (<3 мин)", TunnelID: t.ID, TunnelName: t.Name}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	hs := t.Connection.LatestHandshake
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
	res := TestResult{Name: "tunnel_connectivity", Description: "Связность через туннель", TunnelID: t.ID, TunnelName: t.Name}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	// Try multiple IP check services
	urls := []string{"https://ifconfig.me", "https://icanhazip.com", "https://ip.me"}
	for _, url := range urls {
		result, err := exec.Run(ctx, "/opt/bin/curl", "-s", "--max-time", "5",
			"--interface", t.InterfaceName, url)
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
	res := TestResult{Name: "firewall_rules", Description: "Правила iptables", TunnelID: t.ID, TunnelName: t.Name}

	if t.Backend == "nativewg" {
		res.Status = StatusSkip
		res.Detail = "NativeWG: firewall управляется NDMS"
		return res
	}

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
	res := TestResult{Name: "config_parse", Description: "Валидация конфига", TunnelID: t.ID, TunnelName: t.Name}

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
	res := TestResult{Name: "interface_state_consistency", Description: "Консистентность state", TunnelID: t.ID, TunnelName: t.Name}

	// Check kernel interface exists
	result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "show", t.InterfaceName)
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
	res := TestResult{Name: "mtu_check", Description: "MTU интерфейса", TunnelID: t.ID, TunnelName: t.Name}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	result, err := exec.Run(ctx, "/opt/sbin/ip", "link", "show", t.InterfaceName)
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

	activeIfaces := make(map[string]bool)
	for _, t := range report.Tunnels {
		activeIfaces[t.InterfaceName] = true
	}

	var leaks []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, "opkgtun") && !strings.Contains(line, "nwg") {
			continue
		}
		found := false
		for iface := range activeIfaces {
			if strings.Contains(line, iface) {
				found = true
				break
			}
		}
		if !found {
			leaks = append(leaks, line)
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
	res := TestResult{Name: "dns_leak_check", Description: "DNS leak проверка", TunnelID: t.ID, TunnelName: t.Name}

	// Resolve a test domain via tunnel interface
	tunnelResult, err := exec.Run(ctx, "/opt/bin/curl", "-s", "--max-time", "5",
		"--interface", t.InterfaceName, "https://am.i.mullvad.net/json")
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
	res := TestResult{Name: "restart_cycle", Description: "Цикл Stop -> Start", TunnelID: t.ID, TunnelName: t.Name}

	if t.Backend == "nativewg" {
		res.Status = StatusSkip
		res.Detail = "NativeWG: lifecycle управляется NDMS"
		return res
	}

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
	handshakeOK := false
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		result, err := exec.Run(ctx, "/opt/sbin/awg", "show", t.InterfaceName)
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

func (r *Runner) testProxyHealth(t TunnelInfo) TestResult {
	res := TestResult{Name: "proxy_health", Description: "AWG Proxy статус", TunnelID: t.ID, TunnelName: t.Name}

	if t.Backend != "nativewg" {
		res.Status = StatusSkip
		res.Detail = "Kernel backend: proxy не используется"
		return res
	}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	if t.Proxy == nil {
		res.Status = StatusError
		res.Detail = "Нет данных proxy"
		return res
	}

	if !t.Proxy.Loaded {
		res.Status = StatusFail
		res.Detail = "awg_proxy.ko не загружен"
		return res
	}

	if !t.Proxy.EndpointMatch {
		res.Status = StatusFail
		res.Detail = "Endpoint туннеля не найден в /proc/awg_proxy/list"
		return res
	}

	if t.Proxy.ListenPort == 0 {
		res.Status = StatusFail
		res.Detail = "Proxy listen port = 0"
		return res
	}

	var details []string
	details = append(details, fmt.Sprintf("proxy 127.0.0.1:%d", t.Proxy.ListenPort))
	if t.Proxy.Version != "" {
		details = append(details, "v"+t.Proxy.Version)
	}

	if !t.Proxy.RouteMatch && t.Proxy.WantedISP != "" {
		details = append(details, fmt.Sprintf("WAN mismatch: actual=%s, wanted=%s", t.Proxy.ActualRouteIface, t.Proxy.WantedISP))
	}

	res.Status = StatusPass
	res.Detail = strings.Join(details, "; ")
	return res
}

func (r *Runner) testPingCheckHealth(t TunnelInfo) TestResult {
	res := TestResult{Name: "pingcheck_health", Description: "PingCheck статус", TunnelID: t.ID, TunnelName: t.Name}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	if t.PingCheck == nil || t.PingCheck.Status == "disabled" {
		res.Status = StatusSkip
		res.Detail = "PingCheck не включён"
		return res
	}

	switch t.PingCheck.Status {
	case "dead":
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Туннель мёртв по мониторингу (%s, %d/%d fails)",
			t.PingCheck.Method, t.PingCheck.FailCount, t.PingCheck.FailThreshold)
	case "alive":
		res.Status = StatusPass
		res.Detail = fmt.Sprintf("Alive (%s)", t.PingCheck.Method)
	case "paused":
		res.Status = StatusSkip
		res.Detail = "Мониторинг на паузе"
	default:
		res.Status = StatusPass
		res.Detail = "Status: " + t.PingCheck.Status
	}
	return res
}

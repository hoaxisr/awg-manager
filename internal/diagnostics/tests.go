package diagnostics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
	"github.com/hoaxisr/awg-manager/internal/tunnel/netutil"
)

type diagEgress struct {
	mode      RouteMode
	ifaceName string
	label     string
}

func (r *Runner) runTestsWithEvents(ctx context.Context, report *Report) []TestResult {
	var results []TestResult
	egress := r.resolveEgress(report)

	run := func(tr TestResult) {
		results = append(results, tr)
		r.emitTest(tr)
	}

	r.emitPhase("global_tests", "Проверка системы...")
	run(r.testWANConnectivity(ctx))
	run(r.testNDMSHealth(ctx))
	run(r.testKernelModule(ctx, report))
	run(r.testClockSkew(ctx))

	for _, t := range report.Tunnels {
		r.emitPhase("tunnel_tests", fmt.Sprintf("Тестирование %s...", t.Name))

		run(r.testDNSResolve(ctx, t, egress))
		run(r.testEndpointReachable(ctx, t, egress))
		run(r.testEndpointRouteCheck(t))
		run(r.testAWGHandshake(t))
		run(r.testTunnelConnectivity(ctx, t, egress))
		run(r.testFirewallRules(t))
		run(r.testConfigParse(t))
		run(r.testInterfaceStateConsistency(ctx, t))
		run(r.testMTUCheck(ctx, t))
		run(r.testProxyHealth(t))
		run(r.testPingCheckHealth(t))
		run(r.testRPFilter(t))
	}

	r.emitPhase("cross_tunnel_tests", "Проверка маршрутов...")
	run(r.testRouteLeak(ctx, report))

	for _, t := range report.Tunnels {
		if t.Status == "running" {
			run(r.testDNSLeak(ctx, t, egress))
		}
	}

	includeRestart := r.opts.IncludeRestart
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

	info, err := r.deps.RCI.ShowVersion(ctx)
	if err != nil {
		res.Status = StatusFail
		res.Detail = "NDMS не отвечает: " + err.Error()
		return res
	}

	res.Status = StatusPass
	res.Detail = info.Title
	return res
}

func (r *Runner) testKernelModule(ctx context.Context, report *Report) TestResult {
	res := TestResult{Name: "kernel_module", Description: "Модули AmneziaWG"}

	// On firmware with native ASC support, no kernel modules are needed —
	// NDMS handles WireGuard and obfuscation natively.
	if ndmsinfo.SupportsWireguardASC() {
		res.Status = StatusSkip
		res.Detail = "Не требуется: NDMS обрабатывает обфускацию нативно"
		return res
	}

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

// clockSkewProbeTargets is the ordered list of HTTPS endpoints we ask for a
// `Date` response header to compute time drift. Russian-friendly hosts come
// first because Cloudflare/Google are routinely blocked by RKN — falling
// back through several targets keeps the probe useful behind the firewall.
var clockSkewProbeTargets = []string{
	"https://ya.ru/",
	"https://mail.ru/",
	"https://www.microsoft.com/",
	"https://www.apple.com/",
	"https://www.cloudflare.com/",
}

// testClockSkew compares local time against an HTTPS server's Date header.
// Wireguard handshakes fail when the local clock skew exceeds ~3 minutes;
// a skew above 60s is worth flagging. Tries clockSkewProbeTargets in order
// and uses the first one that responds with a parseable Date header.
func (r *Runner) testClockSkew(ctx context.Context) TestResult {
	res := TestResult{Name: "clock_skew", Description: "Расхождение времени с эталоном"}

	client := &http.Client{Timeout: 5 * time.Second}

	var (
		serverTime time.Time
		usedTarget string
		lastErr    string
	)
	for _, target := range clockSkewProbeTargets {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		dateHeader := resp.Header.Get("Date")
		resp.Body.Close()
		if dateHeader == "" {
			lastErr = "сервер не вернул Date header"
			continue
		}
		t, err := http.ParseTime(dateHeader)
		if err != nil {
			lastErr = "не удалось распарсить Date: " + err.Error()
			continue
		}
		serverTime = t
		usedTarget = target
		break
	}

	if usedTarget == "" {
		res.Status = StatusSkip
		res.Detail = "Нет ответа ни от одного из эталонных серверов: " + lastErr
		return res
	}

	skew := time.Since(serverTime)
	if skew < 0 {
		skew = -skew
	}

	skewSec := int(skew.Seconds())
	switch {
	case skewSec > 300:
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Системное время отличается от эталона на %ds (источник: %s). Wireguard handshake может работать некорректно.", skewSec, usedTarget)
	case skewSec > 60:
		res.Status = StatusWarn
		res.Detail = fmt.Sprintf("Системное время отличается на %ds (источник: %s). Рекомендуется настроить NTP.", skewSec, usedTarget)
	default:
		res.Status = StatusPass
		res.Detail = fmt.Sprintf("Расхождение %ds (норма, источник: %s)", skewSec, usedTarget)
	}
	return res
}

// --- Per-tunnel tests ---

func (r *Runner) testDNSResolve(ctx context.Context, t TunnelInfo, egress diagEgress) TestResult {
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

	ips, err := netutil.LookupAllIPs(host)
	if err != nil {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Не удалось резолвить %s: %s", host, err.Error())
		return res
	}

	if egress.mode == RouteTunnel && egress.ifaceName != "" && len(ips) > 0 {
		routeResult, err := exec.Run(ctx, "/opt/sbin/ip", "route", "get", ips[0])
		if err != nil {
			res.Status = StatusError
			res.Detail = fmt.Sprintf("Резолв %s OK, но маршрут до %s не проверен (%s)", host, ips[0], egress.label)
			return res
		}
		routeDev := extractRouteGetDev(routeResult.Stdout)
		if routeDev != egress.ifaceName {
			res.Status = StatusFail
			res.Detail = fmt.Sprintf("Резолв %s OK, но маршрут до %s идёт через %s, ожидался %s", host, ips[0], routeDev, egress.ifaceName)
			return res
		}
	}

	res.Status = StatusPass
	res.Detail = fmt.Sprintf("%s -> %s (%s)", host, strings.Join(ips, ", "), egress.label)
	return res
}

func (r *Runner) testEndpointReachable(ctx context.Context, t TunnelInfo, egress diagEgress) TestResult {
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
		resolved, err := netutil.ResolveHost(host)
		if err != nil {
			res.Status = StatusSkip
			res.Detail = "Не удалось резолвить endpoint"
			return res
		}
		ip = resolved
	}

	args := []string{"-c", "3"}
	if egress.mode == RouteTunnel && egress.ifaceName != "" {
		args = append(args, "-I", egress.ifaceName)
	}
	args = append(args, ip)

	result, err := exec.Run(ctx, "ping", args...)
	if err != nil {
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Ping %s: недоступен (%s)", ip, egress.label)
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
	if res.Detail == "" {
		res.Detail = fmt.Sprintf("Ping %s: доступен (%s)", ip, egress.label)
	} else {
		res.Detail = fmt.Sprintf("%s [%s]", res.Detail, egress.label)
	}
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

func (r *Runner) testTunnelConnectivity(ctx context.Context, t TunnelInfo, egress diagEgress) TestResult {
	res := TestResult{Name: "tunnel_connectivity", Description: "Связность через туннель", TunnelID: t.ID, TunnelName: t.Name}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	// Try multiple IP check services
	urls := []string{"https://ifconfig.me", "https://icanhazip.com", "https://ip.me"}
	for _, url := range urls {
		args := []string{"-s", "--max-time", "5"}
		if egress.mode == RouteTunnel && egress.ifaceName != "" {
			args = append(args, "--interface", egress.ifaceName)
		}
		args = append(args, url)

		result, err := exec.Run(ctx, "/opt/bin/curl", args...)
		if err == nil && strings.TrimSpace(result.Stdout) != "" {
			ip := strings.TrimSpace(result.Stdout)
			res.Status = StatusPass
			res.Detail = fmt.Sprintf("IP: %s (via %s, %s)", ip, url, egress.label)
			return res
		}
	}

	res.Status = StatusSkip
	res.Detail = fmt.Sprintf("Все IP-сервисы недоступны (%s)", egress.label)
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

	// Collect all managed interface names (running and non-running).
	allManagedIfaces := make(map[string]bool)
	activeIfaces := make(map[string]bool)
	for _, t := range report.Tunnels {
		allManagedIfaces[t.InterfaceName] = true
		if t.Status == "running" {
			activeIfaces[t.InterfaceName] = true
		}
	}

	var leaks []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Only check routes on OUR managed interfaces, skip everything else.
		isManagedRoute := false
		for iface := range allManagedIfaces {
			if strings.Contains(line, " dev "+iface+" ") || strings.HasSuffix(line, " dev "+iface) {
				isManagedRoute = true
				// Route exists on a managed interface that is NOT running → orphaned.
				if !activeIfaces[iface] {
					leaks = append(leaks, line)
				}
				break
			}
		}
		_ = isManagedRoute // only managed routes are checked
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

func (r *Runner) testDNSLeak(ctx context.Context, t TunnelInfo, egress diagEgress) TestResult {
	res := TestResult{Name: "dns_leak_check", Description: "DNS leak проверка", TunnelID: t.ID, TunnelName: t.Name}

	if t.Settings.DNS == "" {
		res.Status = StatusSkip
		res.Detail = "DNS не настроен в конфигурации туннеля"
		return res
	}

	// Find a tunnel-internal DNS server (private/CGNAT IP).
	// Public DNS (8.8.8.8 etc.) is reachable via WAN too, so resolving
	// through it doesn't prove anything about the tunnel path.
	dnsServer := findTunnelDNS(t.Settings.DNS)
	if dnsServer == "" {
		res.Status = StatusSkip
		res.Detail = "DNS-серверы туннеля публичные — проверка неинформативна"
		return res
	}

	// The DNS server sits inside the tunnel network and is only reachable
	// through the tunnel.  Successful resolution proves no DNS leak.
	routeNote := ""
	if egress.mode == RouteTunnel && egress.ifaceName != "" {
		routeResult, err := exec.Run(ctx, "/opt/sbin/ip", "route", "get", dnsServer)
		if err != nil {
			res.Status = StatusError
			res.Detail = fmt.Sprintf("Не удалось проверить маршрут до DNS %s (%s)", dnsServer, egress.label)
			return res
		}
		routeDev := extractRouteGetDev(routeResult.Stdout)
		if routeDev != egress.ifaceName {
			routeNote = fmt.Sprintf("route_get_dev=%s, expected=%s", routeDev, egress.ifaceName)
		}
	}

	result, err := exec.Run(ctx, "nslookup", "example.com", dnsServer)
	if err != nil {
		// NativeWG/policy-routing systems may not expose private tunnel DNS
		// to router-originated nslookup even when tunnel egress works.
		// Fallback to DoH and plain HTTPS over selected egress as a practical
		// connectivity signal; otherwise report SKIP (not FAIL) to avoid
		// false negatives on policy-routing setups.
		if egress.mode == RouteTunnel && egress.ifaceName != "" {
			doh, dohErr := exec.Run(ctx, "/opt/bin/curl", "-s", "--max-time", "5",
				"--interface", egress.ifaceName,
				"https://dns.google/resolve?name=example.com&type=A")
			if dohErr == nil && strings.Contains(doh.Stdout, "\"Answer\"") {
				res.Status = StatusPass
				if routeNote != "" {
					res.Detail = fmt.Sprintf("Туннельный DNS %s недоступен из контекста роутера, но DoH через %s работает (%s)",
						dnsServer, egress.ifaceName, routeNote)
				} else {
					res.Detail = fmt.Sprintf("Туннельный DNS %s недоступен из контекста роутера, но DoH через %s работает",
						dnsServer, egress.ifaceName)
				}
				return res
			}

			httpsProbe, httpsErr := exec.Run(ctx, "/opt/bin/curl", "-s", "--max-time", "5",
				"--interface", egress.ifaceName, "https://ifconfig.me")
			if httpsErr == nil && strings.TrimSpace(httpsProbe.Stdout) != "" {
				res.Status = StatusSkip
				if routeNote != "" {
					res.Detail = fmt.Sprintf("Туннельный DNS %s недоступен из контекста роутера, но egress через %s работает (%s)",
						dnsServer, egress.ifaceName, routeNote)
				} else {
					res.Detail = fmt.Sprintf("Туннельный DNS %s недоступен из контекста роутера, но egress через %s работает",
						dnsServer, egress.ifaceName)
				}
				return res
			}
		}

		res.Status = StatusFail
		if routeNote != "" {
			res.Detail = fmt.Sprintf("Туннельный DNS %s недоступен (%s, %s)", dnsServer, egress.label, routeNote)
		} else {
			res.Detail = fmt.Sprintf("Туннельный DNS %s недоступен (%s)", dnsServer, egress.label)
		}
		return res
	}

	output := result.Stdout + result.Stderr
	if strings.Contains(output, "Address") && !strings.Contains(output, "server can't find") {
		res.Status = StatusPass
		if routeNote != "" {
			res.Detail = fmt.Sprintf("Ответ получен через туннельный DNS %s (%s, %s)", dnsServer, egress.label, routeNote)
		} else {
			res.Detail = fmt.Sprintf("Ответ получен через туннельный DNS %s (%s)", dnsServer, egress.label)
		}
	} else {
		res.Status = StatusFail
		if routeNote != "" {
			res.Detail = fmt.Sprintf("Туннельный DNS %s не резолвит (%s, %s)", dnsServer, egress.label, routeNote)
		} else {
			res.Detail = fmt.Sprintf("Туннельный DNS %s не резолвит (%s)", dnsServer, egress.label)
		}
	}
	return res
}

func (r *Runner) resolveEgress(report *Report) diagEgress {
	direct := diagEgress{
		mode:  RouteDirect,
		label: "маршрут по умолчанию",
	}

	if r.opts.RouteMode != RouteTunnel {
		return direct
	}

	targetID := strings.TrimSpace(r.opts.RouteTunnelID)
	if targetID == "" {
		return direct
	}

	for _, t := range report.Tunnels {
		if t.ID != targetID {
			continue
		}
		if t.InterfaceName == "" {
			return direct
		}
		return diagEgress{
			mode:      RouteTunnel,
			ifaceName: t.InterfaceName,
			label:     fmt.Sprintf("через туннель %s (%s)", t.Name, t.InterfaceName),
		}
	}

	return direct
}

// findTunnelDNS returns the first private/CGNAT DNS server from a
// comma-separated list, or "" if all servers are public.
func findTunnelDNS(dnsList string) string {
	for _, s := range strings.Split(dnsList, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.IsPrivate() || isCGNAT(ip) {
			return s
		}
	}
	return ""
}

// isCGNAT checks if the IP is in the 100.64.0.0/10 range (RFC 6598).
func isCGNAT(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
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

	// On firmware with native ASC support, awg_proxy.ko is not used.
	if ndmsinfo.SupportsWireguardASC() {
		res.Status = StatusSkip
		res.Detail = "Не требуется: NDMS обрабатывает обфускацию нативно"
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
	if t.Proxy.Version != "" {
		details = append(details, "v"+t.Proxy.Version)
	}
	if t.Proxy.RxBytes != "" {
		details = append(details, "rx "+t.Proxy.RxBytes)
	}
	if t.Proxy.TxBytes != "" {
		details = append(details, "tx "+t.Proxy.TxBytes)
	}
	if t.Proxy.BindIface != "" {
		details = append(details, "bind="+t.Proxy.BindIface)
	}
	details = append(details, fmt.Sprintf("listen=127.0.0.1:%d", t.Proxy.ListenPort))

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
	case "recovering":
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("Восстановление связи (%s, рестарт #%d)",
			t.PingCheck.Method, t.PingCheck.RestartCount)
	case "alive":
		res.Status = StatusPass
		res.Detail = fmt.Sprintf("Alive (%s)", t.PingCheck.Method)
	default:
		res.Status = StatusPass
		res.Detail = "Status: " + t.PingCheck.Status
	}
	return res
}

// testRPFilter checks reverse-path filter setting for the tunnel's interface.
// rp_filter=1 (strict) blocks return traffic on VPN interfaces — common cause
// of "tunnel up but no connectivity" symptoms.
func (r *Runner) testRPFilter(t TunnelInfo) TestResult {
	res := TestResult{Name: "rp_filter", Description: "Reverse path filter", TunnelID: t.ID, TunnelName: t.Name}

	if t.Status != "running" {
		res.Status = StatusSkip
		res.Detail = "Туннель не запущен"
		return res
	}

	ifname := t.InterfaceName
	if ifname == "" {
		res.Status = StatusSkip
		res.Detail = "Имя интерфейса неизвестно"
		return res
	}

	readVal := func(path string) (int, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		v, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return 0, err
		}
		return v, nil
	}

	perIface, err1 := readVal(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/rp_filter", ifname))
	allConf, err2 := readVal("/proc/sys/net/ipv4/conf/all/rp_filter")
	if err1 != nil && err2 != nil {
		res.Status = StatusError
		res.Detail = "Не удалось прочитать /proc/sys/net/ipv4/conf/.../rp_filter"
		return res
	}

	effective := perIface
	if allConf > effective {
		effective = allConf
	}

	switch effective {
	case 1:
		res.Status = StatusFail
		res.Detail = fmt.Sprintf("rp_filter=1 (strict) на %s блокирует обратный трафик через туннель. Установите 0 или 2: sysctl -w net.ipv4.conf.%s.rp_filter=2", ifname, ifname)
	case 2:
		res.Status = StatusPass
		res.Detail = "rp_filter=2 (loose) — корректно для VPN-интерфейса"
	case 0:
		res.Status = StatusPass
		res.Detail = "rp_filter=0 (off)"
	default:
		res.Status = StatusWarn
		res.Detail = fmt.Sprintf("rp_filter=%d — нестандартное значение", effective)
	}
	return res
}

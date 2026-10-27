package pingcheck

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/icmpprobe"
)

const (
	checkTimeout = 7 * time.Second
)

// checkHTTP performs HTTP 204 connectivity check through the tunnel.
func checkHTTP(ctx context.Context, ifaceName string, checkURL string, dnsServers []string) CheckResult {
	res, err := httpprobe.ByInterface(ctx, ifaceName, checkURL, dnsServers)
	if err != nil {
		return CheckResult{
			Success: false,
			Error:   fmt.Sprintf("HTTP check failed: %v", err),
		}
	}

	if httpprobe.SuccessCode(res.HTTPCode) {
		return CheckResult{
			Success: true,
			Latency: res.LatencyMs,
		}
	}

	return CheckResult{
		Success: false,
		Latency: res.LatencyMs,
		Error:   fmt.Sprintf("unexpected HTTP code: %d", res.HTTPCode),
	}
}

// checkICMP performs a native ICMP ping check through the tunnel interface.
func checkICMP(ctx context.Context, ifaceName string, target string, dnsServers []string) CheckResult {
	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	start := time.Now()
	res, err := icmpprobe.ByInterface(checkCtx, ifaceName, target, dnsServers)
	if err != nil {
		return CheckResult{
			Success: false,
			Latency: int(time.Since(start).Milliseconds()),
			Error:   fmt.Sprintf("ping failed: %v", err),
		}
	}

	return CheckResult{
		Success: true,
		Latency: res.LatencyMs,
	}
}

// performCheck executes the appropriate check method using the resolved interface name.
func performCheck(ctx context.Context, ifaceName string, method string, target string, checkURL string, dnsServers []string) CheckResult {
	switch method {
	case "icmp":
		return checkICMP(ctx, ifaceName, target, dnsServers)
	default: // "http" is default
		return checkHTTP(ctx, ifaceName, httpCheckURL(target, checkURL), dnsServers)
	}
}

// httpCheckURL — адрес HTTP-проверки: цель из настроек туннеля (хост без
// схемы → https://), пустая цель → общий ConnectivityCheckURL. Голый IP —
// тоже общий URL: поле цели задумано под ICMP, и умолчания (общие настройки:
// http + 8.8.8.8) разложили IP по записям с методом http. https://<IP> у
// большинства адресов без сертификата на IP — вечный провал и снятие туннеля
// со всех политик после обновления (ревью 27.10).
func httpCheckURL(target, fallback string) string {
	target = strings.TrimSpace(target)
	switch {
	case target == "" || net.ParseIP(target) != nil:
		return fallback
	case strings.Contains(target, "://"):
		return target
	default:
		return "https://" + target
	}
}

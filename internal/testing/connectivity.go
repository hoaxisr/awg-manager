package testing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	connectivityURL         = "http://connectivitycheck.gstatic.com/generate_204"
	connectivityTestTimeout = 7 * time.Second
)

// CheckConnectivity performs quick connectivity test through tunnel.
func (s *Service) CheckConnectivity(ctx context.Context, tunnelID string) (*ConnectivityResult, error) {
	if err := s.CheckTunnelRunning(tunnelID); err != nil {
		return &ConnectivityResult{Connected: false, Reason: ReasonTunnelNotRunning}, nil
	}

	stored := s.GetAWG(tunnelID)
	method := "http"
	if stored != nil && stored.ConnectivityCheck != nil && stored.ConnectivityCheck.Method != "" {
		method = stored.ConnectivityCheck.Method
	}

	switch method {
	case "ping":
		return s.checkPing(ctx, tunnelID, stored)
	case "handshake":
		return s.checkHandshake(tunnelID)
	case "disabled":
		return &ConnectivityResult{Connected: true, Reason: "check disabled"}, nil
	default:
		return s.checkHTTP(ctx, tunnelID)
	}
}

// checkHTTP performs connectivity check using HTTP (curl to generate_204).
func (s *Service) checkHTTP(ctx context.Context, tunnelID string) (*ConnectivityResult, error) {
	curlOpts, err := s.GetCurlOptions(tunnelID)
	if err != nil {
		return &ConnectivityResult{Connected: false, Reason: ReasonTunnelNotRunning}, nil
	}

	args := append([]string{
		"-s", "-o", "/dev/null",
		"--max-time", "5",
		"--connect-timeout", "3", // Быстрый сброс при зависании
		"-w", "%{http_code}|%{time_namelookup}|%{time_connect}|%{time_total}",
	}, curlOpts...)
	args = append(args, connectivityURL)

	testCtx, cancel := context.WithTimeout(ctx, connectivityTestTimeout)
	defer cancel()

	result, err := exec.Run(testCtx, "/opt/bin/curl", args...)
	if err != nil {
		errDetail := exec.FormatError(result, err).Error()
		return &ConnectivityResult{Connected: false, Reason: ReasonConnectionFailed + ": " + errDetail}, nil
	}

	output := strings.TrimSpace(result.Stdout)
	parts := strings.Split(output, "|")
	if len(parts) != 4 {
		return &ConnectivityResult{Connected: false, Reason: ReasonUnexpectedResponse}, nil
	}

	httpCode, _ := strconv.Atoi(parts[0])
	timeNameLookup, _ := strconv.ParseFloat(parts[1], 64)
	timeConnect, _ := strconv.ParseFloat(parts[2], 64)
	timeTotal, _ := strconv.ParseFloat(parts[3], 64)

	var latencyMs int
	// Вычисляем чистый TCP RTT (исключая задержки DNS и HTTP-ответа)
	if timeConnect > 0 && timeConnect >= timeNameLookup {
		latencyMs = int((timeConnect - timeNameLookup) * 1000)
	} else {
		// Fallback, если вдруг метрика отработала нетипично
		latencyMs = int(timeTotal * 1000)
	}
	
	// Ограничиваем минимум в 1ms, чтобы не показывать 0ms
	if httpCode == 204 && latencyMs <= 0 {
		latencyMs = 1
	}

	if httpCode == 204 {
		return &ConnectivityResult{Connected: true, Latency: &latencyMs}, nil
	}

	return &ConnectivityResult{Connected: false, Reason: ReasonUnexpectedResponse, HTTPCode: &httpCode}, nil
}

// checkPing performs connectivity check using ICMP ping through the tunnel interface.
func (s *Service) checkPing(ctx context.Context, tunnelID string, stored *storage.AWGTunnel) (*ConnectivityResult, error) {
	iface := s.resolveIfaceName(tunnelID)

	target := ""
	if stored != nil && stored.ConnectivityCheck != nil {
		target = stored.ConnectivityCheck.PingTarget
	}
	if target == "" {
		target = autoDetectGateway(stored)
	}
	if target == "" {
		return &ConnectivityResult{Connected: false, Reason: "no ping target configured"}, nil
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	result, err := exec.Run(pingCtx, "ping", "-c", "1", "-W", "3", "-I", iface, target)
	if err != nil {
		return &ConnectivityResult{Connected: false, Reason: "ping failed: " + target}, nil
	}

	latency := parsePingLatency(result.Stdout)
	return &ConnectivityResult{Connected: true, Latency: latency}, nil
}

// autoDetectGateway derives a likely gateway IP from the tunnel address (e.g. 10.0.0.2/32 → 10.0.0.1).
func autoDetectGateway(stored *storage.AWGTunnel) string {
	if stored == nil || stored.Interface.Address == "" {
		return ""
	}
	addr := stored.Interface.Address
	if idx := strings.Index(addr, "/"); idx > 0 {
		addr = addr[:idx]
	}
	if idx := strings.Index(addr, ","); idx > 0 {
		addr = strings.TrimSpace(addr[:idx])
	}
	parts := strings.Split(addr, ".")
	if len(parts) != 4 {
		return ""
	}
	parts[3] = "1"
	return strings.Join(parts, ".")
}

// parsePingLatency extracts round-trip time from ping output.
func parsePingLatency(output string) *int {
	idx := strings.Index(output, "time=")
	if idx < 0 {
		return nil
	}
	rest := output[idx+5:]
	end := strings.IndexAny(rest, " m")
	if end <= 0 {
		return nil
	}
	val, err := strconv.ParseFloat(rest[:end], 64)
	if err != nil {
		return nil
	}
	ms := int(val)
	return &ms
}

// checkHandshake checks if WireGuard has a recent handshake (< 3 minutes).
func (s *Service) checkHandshake(tunnelID string) (*ConnectivityResult, error) {
	iface := s.resolveIfaceName(tunnelID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := exec.Run(ctx, "/opt/sbin/awg", "show", iface)
	if err != nil {
		return &ConnectivityResult{Connected: false, Reason: "cannot read WG state"}, nil
	}

	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "latest handshake:") {
			continue
		}
		hs := strings.TrimSpace(strings.TrimPrefix(line, "latest handshake:"))
		if hs == "(none)" || hs == "" {
			return &ConnectivityResult{Connected: false, Reason: "no handshake"}, nil
		}
		if strings.Contains(hs, "hour") || strings.Contains(hs, "day") {
			return &ConnectivityResult{Connected: false, Reason: "handshake stale: " + hs}, nil
		}
		if strings.Contains(hs, "minute") {
			var mins int
			fmt.Sscanf(hs, "%d minute", &mins)
			if mins >= 3 {
				return &ConnectivityResult{Connected: false, Reason: "handshake stale: " + hs}, nil
			}
		}
		return &ConnectivityResult{Connected: true}, nil
	}

	return &ConnectivityResult{Connected: false, Reason: "no handshake info"}, nil
}

// CheckConnectivityByInterface performs connectivity test using a kernel interface name directly.
// Used for system tunnels where we don't have a managed tunnel ID.
func CheckConnectivityByInterface(ctx context.Context, ifaceName string) *ConnectivityResult {
	args :=[]string{
		"-s", "-o", "/dev/null",
		"--max-time", "5",
		"--connect-timeout", "3",
		"-w", "%{http_code}|%{time_namelookup}|%{time_connect}|%{time_total}",
		"--interface", ifaceName,
		connectivityURL,
	}

	testCtx, cancel := context.WithTimeout(ctx, connectivityTestTimeout)
	defer cancel()

	result, err := exec.Run(testCtx, "/opt/bin/curl", args...)
	if err != nil {
		return &ConnectivityResult{
			Connected: false,
			Reason:    ReasonConnectionFailed,
		}
	}

	output := strings.TrimSpace(result.Stdout)
	parts := strings.Split(output, "|")
	if len(parts) != 4 {
		return &ConnectivityResult{
			Connected: false,
			Reason:    ReasonUnexpectedResponse,
		}
	}

	httpCode, _ := strconv.Atoi(parts[0])
	timeNameLookup, _ := strconv.ParseFloat(parts[1], 64)
	timeConnect, _ := strconv.ParseFloat(parts[2], 64)
	timeTotal, _ := strconv.ParseFloat(parts[3], 64)

	var latencyMs int
	if timeConnect > 0 && timeConnect >= timeNameLookup {
		latencyMs = int((timeConnect - timeNameLookup) * 1000)
	} else {
		latencyMs = int(timeTotal * 1000)
	}
	
	if httpCode == 204 && latencyMs <= 0 {
		latencyMs = 1
	}

	if httpCode == 204 {
		return &ConnectivityResult{
			Connected: true,
			Latency:   &latencyMs,
		}
	}

	return &ConnectivityResult{
		Connected: false,
		Reason:    ReasonUnexpectedResponse,
		HTTPCode:  &httpCode,
	}
}

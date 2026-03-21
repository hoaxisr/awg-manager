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
		s.appLog.Debug("connectivity-check", tunnelID, "Tunnel not running")
		return &ConnectivityResult{Connected: false, Reason: ReasonTunnelNotRunning}, nil
	}

	stored := s.GetAWG(tunnelID)
	method := "http"
	if stored != nil && stored.ConnectivityCheck != nil && stored.ConnectivityCheck.Method != "" {
		method = stored.ConnectivityCheck.Method
	}

	s.appLog.Full("connectivity-check", tunnelID, fmt.Sprintf("Starting connectivity check with method: %s", method))

	switch method {
	case "ping":
		return s.checkPing(ctx, tunnelID, stored)
	case "handshake":
		return s.checkHandshake(tunnelID)
	case "disabled":
		s.appLog.Debug("connectivity-check", tunnelID, "Check disabled, returning OK")
		return &ConnectivityResult{Connected: true, Reason: "check disabled"}, nil
	default:
		return s.checkHTTP(ctx, tunnelID)
	}
}

// checkHTTP performs connectivity check using HTTP (curl to generate_204).
func (s *Service) checkHTTP(ctx context.Context, tunnelID string) (*ConnectivityResult, error) {
	curlOpts, err := s.GetCurlOptions(tunnelID)
	if err != nil {
		s.appLog.Debug("http-check", tunnelID, "Tunnel not running, cannot get curl options")
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

	s.appLog.Full("http-check", tunnelID, fmt.Sprintf("Executing HTTP check: %s", connectivityURL))

	result, err := exec.Run(testCtx, "/opt/bin/curl", args...)
	if err != nil {
		errDetail := exec.FormatError(result, err).Error()
		s.appLog.Warn("http-check", tunnelID, fmt.Sprintf("HTTP check failed: %s, stdout=%s, stderr=%s", errDetail, result.Stdout, result.Stderr))
		return &ConnectivityResult{Connected: false, Reason: ReasonConnectionFailed + ": " + errDetail}, nil
	}

	output := strings.TrimSpace(result.Stdout)
	s.appLog.Debug("http-check", tunnelID, fmt.Sprintf("Curl output: %s", output))

	parts := strings.Split(output, "|")
	if len(parts) != 4 {
		s.appLog.Warn("http-check", tunnelID, fmt.Sprintf("Unexpected curl output: %s", output))
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
		s.appLog.Info("http-check", tunnelID, fmt.Sprintf("HTTP check successful: code=204, latency=%dms", latencyMs))
		return &ConnectivityResult{Connected: true, Latency: &latencyMs}, nil
	}

	s.appLog.Warn("http-check", tunnelID, fmt.Sprintf("HTTP check returned unexpected code: %d", httpCode))
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
		s.appLog.Warn("ping-check", tunnelID, "No ping target configured for tunnel "+tunnelID)
		return &ConnectivityResult{Connected: false, Reason: "no ping target configured"}, nil
	}

	s.appLog.Full("ping-check", tunnelID, fmt.Sprintf("Starting ping check: iface=%s, target=%s", iface, target))

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Use Entware ping with explicit path and proper interface binding
	s.appLog.Debug("ping-check", tunnelID, fmt.Sprintf("Executing: /opt/bin/ping -I %s -c 1 -W 3 %s", iface, target))

	result, err := exec.Run(pingCtx, "/opt/bin/ping", "-I", iface, "-c", "1", "-W", "3", target)

	// Check exit code, not err — ping may return err with exit 0 on some systems
	if result.ExitCode != 0 {
		errDetail := exec.FormatError(result, err).Error()
		s.appLog.Warn("ping-check", tunnelID, fmt.Sprintf("Ping failed: target=%s, exit=%d, stderr=%s, stdout=%s", target, result.ExitCode, result.Stderr, result.Stdout))
		return &ConnectivityResult{Connected: false, Reason: "ping failed: " + target + " - " + errDetail}, nil
	}

	s.appLog.Debug("ping-check", tunnelID, fmt.Sprintf("Ping raw output: stdout='%s', stderr='%s'", result.Stdout, result.Stderr))

	// Try parsing latency from stdout first, then stderr (busybox may output to stderr)
	latency := parsePingLatency(result.Stdout)
	if latency == nil {
		latency = parsePingLatency(result.Stderr)
	}
	if latency == nil {
		// If no latency parsed but exit code is 0, ping succeeded — return minimal latency
		// This happens with busybox ping which may not output timing info
		s.appLog.Full("ping-check", tunnelID, fmt.Sprintf("Ping exit code 0 but no timing output - stdout='%s', stderr='%s'", result.Stdout, result.Stderr))
		s.appLog.Info("ping-check", tunnelID, fmt.Sprintf("Ping successful (no latency parsed): target=%s", target))
		return &ConnectivityResult{Connected: true, Latency: intPtr(1)}, nil
	}

	s.appLog.Info("ping-check", tunnelID, fmt.Sprintf("Ping successful: target=%s, latency=%dms", target, *latency))
	return &ConnectivityResult{Connected: true, Latency: latency}, nil
}

// intPtr returns a pointer to an int.
func intPtr(i int) *int {
	return &i
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

	s.appLog.Full("handshake-check", tunnelID, fmt.Sprintf("Checking WireGuard handshake on interface %s", iface))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := exec.Run(ctx, "/opt/sbin/awg", "show", iface)
	if err != nil {
		s.appLog.Warn("handshake-check", tunnelID, fmt.Sprintf("Cannot read WG state: %v, stdout=%s, stderr=%s", err, result.Stdout, result.Stderr))
		return &ConnectivityResult{Connected: false, Reason: "cannot read WG state"}, nil
	}

	s.appLog.Debug("handshake-check", tunnelID, fmt.Sprintf("awg show output: %s", result.Stdout))

	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "latest handshake:") {
			continue
		}
		hs := strings.TrimSpace(strings.TrimPrefix(line, "latest handshake:"))
		if hs == "(none)" || hs == "" {
			s.appLog.Warn("handshake-check", tunnelID, "No handshake found")
			return &ConnectivityResult{Connected: false, Reason: "no handshake"}, nil
		}
		if strings.Contains(hs, "hour") || strings.Contains(hs, "day") {
			s.appLog.Warn("handshake-check", tunnelID, fmt.Sprintf("Handshake stale: %s", hs))
			return &ConnectivityResult{Connected: false, Reason: "handshake stale: " + hs}, nil
		}
		if strings.Contains(hs, "minute") {
			var mins int
			fmt.Sscanf(hs, "%d minute", &mins)
			if mins >= 3 {
				s.appLog.Warn("handshake-check", tunnelID, fmt.Sprintf("Handshake stale: %s (%d min)", hs, mins))
				return &ConnectivityResult{Connected: false, Reason: "handshake stale: " + hs}, nil
			}
		}
		s.appLog.Info("handshake-check", tunnelID, fmt.Sprintf("Handshake recent: %s", hs))
		return &ConnectivityResult{Connected: true}, nil
	}

	s.appLog.Warn("handshake-check", tunnelID, "No handshake info found in awg show output")
	return &ConnectivityResult{Connected: false, Reason: "no handshake info"}, nil
}

// CheckConnectivityByInterface performs connectivity test using a kernel interface name directly.
// Used for system tunnels where we don't have a managed tunnel ID.
func CheckConnectivityByInterface(ctx context.Context, ifaceName string) *ConnectivityResult {
	args := []string{
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

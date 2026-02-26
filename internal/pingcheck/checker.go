package pingcheck

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

const (
	connectivityURL = "http://connectivitycheck.gstatic.com/generate_204"
	checkTimeout    = 7 * time.Second
	curlMaxTime     = "5"
)

// checkHTTP performs HTTP 204 connectivity check through the tunnel.
func checkHTTP(ctx context.Context, tunnelID string) CheckResult {
	iface := tunnel.NewNames(tunnelID).IfaceName

	args := []string{
		"-s", "-o", "/dev/null",
		"--max-time", curlMaxTime,
		"-w", "%{http_code}|%{time_total}",
		"--interface", iface,
		connectivityURL,
	}

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	result, err := exec.Run(checkCtx, "/opt/bin/curl", args...)
	if err != nil {
		return CheckResult{
			Success: false,
			Error:   fmt.Sprintf("curl failed: %v", exec.FormatError(result, err)),
		}
	}

	output := strings.TrimSpace(result.Stdout)
	parts := strings.Split(output, "|")
	if len(parts) != 2 {
		return CheckResult{
			Success: false,
			Error:   fmt.Sprintf("unexpected curl output: %s", output),
		}
	}

	httpCode, _ := strconv.Atoi(parts[0])
	timeTotal, _ := strconv.ParseFloat(parts[1], 64)
	latencyMs := int(timeTotal * 1000)

	if httpCode == 204 {
		return CheckResult{
			Success: true,
			Latency: latencyMs,
		}
	}

	return CheckResult{
		Success: false,
		Latency: latencyMs,
		Error:   fmt.Sprintf("unexpected HTTP code: %d", httpCode),
	}
}

// checkICMP performs ICMP ping check through the tunnel interface.
func checkICMP(ctx context.Context, tunnelID string, target string) CheckResult {
	iface := tunnel.NewNames(tunnelID).IfaceName

	// ping -I <interface> -c 1 -W 5 <target>
	args := []string{
		"-I", iface,
		"-c", "1",
		"-W", "5",
		target,
	}

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	start := time.Now()
	result, err := exec.Run(checkCtx, "/opt/bin/ping", args...)
	latencyMs := int(time.Since(start).Milliseconds())

	if err != nil {
		return CheckResult{
			Success: false,
			Latency: latencyMs,
			Error:   fmt.Sprintf("ping failed: %v", exec.FormatError(result, err)),
		}
	}

	// Parse ping output for more accurate latency
	// Example: "64 bytes from 8.8.8.8: icmp_seq=1 ttl=117 time=12.3 ms"
	if strings.Contains(result.Stdout, "time=") {
		latencyMs = parsePingLatency(result.Stdout)
	}

	// Check if ping was successful (exit code 0 means success)
	if result.ExitCode == 0 {
		return CheckResult{
			Success: true,
			Latency: latencyMs,
		}
	}

	return CheckResult{
		Success: false,
		Latency: latencyMs,
		Error:   "ping unsuccessful",
	}
}

// checkHandshake checks tunnel liveness via `awg show` latest handshake.
// Used for dead tunnels where the opkgtun interface is down.
func checkHandshake(ctx context.Context, tunnelID string, maxAge time.Duration) CheckResult {
	iface := tunnel.NewNames(tunnelID).IfaceName

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	result, err := exec.Run(checkCtx, "/opt/sbin/awg", "show", iface)
	if err != nil {
		return CheckResult{
			Success: false,
			Error:   fmt.Sprintf("awg show failed: %v", exec.FormatError(result, err)),
		}
	}

	// Parse "latest handshake: X seconds ago"
	handshakeAge, ok := parseHandshakeAge(result.Stdout)
	if !ok {
		return CheckResult{
			Success: false,
			Error:   "no handshake found or parse error",
		}
	}

	// If handshake is recent (within maxAge), tunnel is alive
	if handshakeAge <= maxAge {
		return CheckResult{
			Success: true,
			Latency: int(handshakeAge.Seconds()),
		}
	}

	return CheckResult{
		Success: false,
		Error:   fmt.Sprintf("handshake too old: %v", handshakeAge),
	}
}

// parseHandshakeAge parses "latest handshake: X seconds ago" from awg show output.
// Returns (duration, true) on success, (0, false) on parse error or not found.
func parseHandshakeAge(output string) (time.Duration, bool) {
	// Look for "latest handshake:" line
	idx := strings.Index(output, "latest handshake:")
	if idx == -1 {
		return 0, false
	}

	// Extract the line after "latest handshake:"
	start := idx + len("latest handshake:")
	end := strings.Index(output[start:], "\n")
	if end == -1 {
		end = len(output) - start
	}
	line := strings.TrimSpace(output[start : start+end])

	// Handle "(none)" case - no handshake yet
	if line == "(none)" {
		return 0, false
	}

	// Parse formats like:
	// "21 seconds ago"
	// "1 minute, 23 seconds ago"
	// "2 hours, 3 minutes, 4 seconds ago"
	var totalSeconds int
	parsed := false

	// Remove "ago" suffix
	line = strings.TrimSuffix(line, " ago")

	parts := strings.Split(line, ", ")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		fields := strings.Fields(part)
		if len(fields) < 2 {
			continue
		}

		value, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}

		unit := fields[1]
		switch {
		case strings.HasPrefix(unit, "second"):
			totalSeconds += value
			parsed = true
		case strings.HasPrefix(unit, "minute"):
			totalSeconds += value * 60
			parsed = true
		case strings.HasPrefix(unit, "hour"):
			totalSeconds += value * 3600
			parsed = true
		case strings.HasPrefix(unit, "day"):
			totalSeconds += value * 86400
			parsed = true
		}
	}

	if !parsed {
		return 0, false
	}

	return time.Duration(totalSeconds) * time.Second, true
}

// parsePingLatency extracts latency from ping output.
func parsePingLatency(output string) int {
	// Look for "time=X.X ms" or "time=X ms"
	idx := strings.Index(output, "time=")
	if idx == -1 {
		return 0
	}

	// Extract the number after "time="
	start := idx + 5
	end := start
	for end < len(output) && (output[end] == '.' || (output[end] >= '0' && output[end] <= '9')) {
		end++
	}

	if end > start {
		if val, err := strconv.ParseFloat(output[start:end], 64); err == nil {
			return int(val)
		}
	}

	return 0
}

// performCheck executes the appropriate check method for a tunnel.
func performCheck(ctx context.Context, tunnelID string, method string, target string) CheckResult {
	switch method {
	case "icmp":
		return checkICMP(ctx, tunnelID, target)
	default: // "http" is default
		return checkHTTP(ctx, tunnelID)
	}
}


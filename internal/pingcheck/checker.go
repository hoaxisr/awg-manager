package pingcheck

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	checkTimeout = 7 * time.Second
)

// checkHTTP performs HTTP 204 connectivity check through the tunnel.
func checkHTTP(ctx context.Context, ifaceName string, checkURL string) CheckResult {
	res, err := httpprobe.ByInterface(ctx, ifaceName, checkURL)
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

// checkICMP performs ICMP ping check through the tunnel interface.
func checkICMP(ctx context.Context, ifaceName string, target string) CheckResult {
	iface := ifaceName

	// Entware ping location
	const pingTimeoutSec = "5"
	args := []string{
		"-I", iface,
		"-c", "1",
		"-W", pingTimeoutSec,
		target,
	}

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	start := time.Now()
	// Use Entware ping at /opt/bin/ping
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

// performCheck executes the appropriate check method using the resolved interface name.
func performCheck(ctx context.Context, ifaceName string, method string, target string, checkURL string) CheckResult {
	switch method {
	case "icmp":
		return checkICMP(ctx, ifaceName, target)
	default: // "http" is default
		return checkHTTP(ctx, ifaceName, checkURL)
	}
}

package testing

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	connectivityURL         = "http://connectivitycheck.gstatic.com/generate_204"
	connectivityTestTimeout = 7 * time.Second
)

// CheckConnectivity performs quick connectivity test through tunnel.
func (s *Service) CheckConnectivity(ctx context.Context, tunnelID string) (*ConnectivityResult, error) {
	if err := s.CheckTunnelRunning(tunnelID); err != nil {
		return &ConnectivityResult{
			Connected: false,
			Reason:    ReasonTunnelNotRunning,
		}, nil
	}

	curlOpts, err := s.GetCurlOptions(tunnelID)
	if err != nil {
		return &ConnectivityResult{
			Connected: false,
			Reason:    ReasonTunnelNotRunning,
		}, nil
	}

	args := append([]string{
		"-s", "-o", "/dev/null",
		"--max-time", "5",
		"-w", "%{http_code}|%{time_total}",
	}, curlOpts...)
	args = append(args, connectivityURL)

	testCtx, cancel := context.WithTimeout(ctx, connectivityTestTimeout)
	defer cancel()

	result, err := exec.Run(testCtx, "/opt/bin/curl", args...)
	if err != nil {
		errDetail := exec.FormatError(result, err).Error()
		return &ConnectivityResult{
			Connected: false,
			Reason:    ReasonConnectionFailed + ": " + errDetail,
		}, nil
	}

	output := strings.TrimSpace(result.Stdout)
	parts := strings.Split(output, "|")
	if len(parts) != 2 {
		return &ConnectivityResult{
			Connected: false,
			Reason:    ReasonUnexpectedResponse,
		}, nil
	}

	httpCode, _ := strconv.Atoi(parts[0])
	timeTotal, _ := strconv.ParseFloat(parts[1], 64)
	latencyMs := int(timeTotal * 1000)

	if httpCode == 204 {
		return &ConnectivityResult{
			Connected: true,
			Latency:   &latencyMs,
		}, nil
	}

	return &ConnectivityResult{
		Connected: false,
		Reason:    ReasonUnexpectedResponse,
		HTTPCode:  &httpCode,
	}, nil
}

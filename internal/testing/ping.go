package testing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// PingByIface measures TCP connect time (in milliseconds) to `host:port` through
// the specified kernel interface. Uses curl as a cross-tool ping substitute since
// it supports binding to an interface, doesn't require root for ICMP, and works
// with HTTP endpoints.
//
// Returns (-1, err) on execution failure, (0, nil) on timeout (configurable via ctx).
func (s *Service) PingByIface(ctx context.Context, ifaceName, host string, port int) (int, error) {
	target := fmt.Sprintf("http://%s:%d/", host, port)
	args := []string{
		"--interface", ifaceName,
		"--connect-timeout", "5",
		"-m", "10",
		"-o", "/dev/null",
		"-s",
		"-w", "%{time_connect}",
		target,
	}
	result, err := sysexec.RunWithOptions(ctx, "curl", args, sysexec.Options{
		Timeout: 12 * time.Second,
	})
	if err != nil {
		// curl returns exit code 28 on connect timeout — treat as 0ms (unreachable)
		if result != nil && strings.Contains(result.Stderr, "timed out") {
			return 0, nil
		}
		return -1, fmt.Errorf("ping %s via %s: %w", host, ifaceName, err)
	}

	// curl -w "%{time_connect}" prints seconds as a float like "0.043"
	raw := strings.TrimSpace(result.Stdout)
	if raw == "" {
		return 0, nil
	}
	sec, parseErr := strconv.ParseFloat(raw, 64)
	if parseErr != nil {
		return -1, fmt.Errorf("parse ping output %q: %w", raw, parseErr)
	}
	if sec <= 0 {
		return 0, nil
	}
	ms := int(sec * 1000)
	if ms < 1 {
		ms = 1
	}
	return ms, nil
}

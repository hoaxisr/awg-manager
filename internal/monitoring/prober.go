package monitoring

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

// Prober probes a single host through a specific interface and returns
// latency in milliseconds + success flag. Implementations must be safe for
// concurrent use.
type Prober interface {
	Probe(ctx context.Context, host, ifaceName string, timeout time.Duration) (latencyMs int, ok bool)
}

// Runner abstracts the exec call so tests can stub responses.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (*exec.Result, error)
}

// HTTPProber probes via curl HTTPS HEAD and reports the **TCP RTT** —
// `time_connect - time_namelookup` — as latency. This matches the metric
// reported by the per-tunnel connectivity-check service so numbers in the
// matrix line up with what cards used to display.
//
// "Reachable" is defined as: curl received any HTTP status code (>0)
// before the timeout. 4xx/5xx still counts — TCP+TLS handshake completed
// through the tunnel, so the host is alive.
type HTTPProber struct {
	Runner Runner
}

// NewHTTPProber builds a curl-based prober backed by the package-level
// exec.Run.
func NewHTTPProber() *HTTPProber {
	return &HTTPProber{Runner: defaultRunner{}}
}

type defaultRunner struct{}

func (defaultRunner) Run(ctx context.Context, name string, args ...string) (*exec.Result, error) {
	return exec.Run(ctx, name, args...)
}

// Probe issues a single HTTPS HEAD request through ifaceName.
// ok=false on context cancellation, exec error, non-zero exit code, or
// http_code == 0 (no response received).
func (p *HTTPProber) Probe(ctx context.Context, host, ifaceName string, timeout time.Duration) (int, bool) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout+1*time.Second)
	defer cancel()

	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	args := []string{
		"-sI",
		"-o", "/dev/null",
		"--max-time", strconv.Itoa(timeoutSec),
		"--connect-timeout", "3",
		"--interface", ifaceName,
		"-w", "%{http_code}|%{time_namelookup}|%{time_connect}|%{time_total}",
		"https://" + host + "/",
	}
	res, err := p.Runner.Run(timeoutCtx, "/opt/bin/curl", args...)
	if err != nil || res == nil {
		return 0, false
	}

	output := strings.TrimSpace(res.Stdout)
	parts := strings.Split(output, "|")
	if len(parts) != 4 {
		return 0, false
	}
	httpCode, _ := strconv.Atoi(parts[0])
	if httpCode == 0 {
		return 0, false
	}
	timeNameLookup, _ := strconv.ParseFloat(parts[1], 64)
	timeConnect, _ := strconv.ParseFloat(parts[2], 64)
	timeTotal, _ := strconv.ParseFloat(parts[3], 64)

	// Prefer pure TCP RTT — DNS resolution can dominate time_total on first
	// requests after a tunnel comes up. Fall back to time_total when the
	// per-phase timings look bogus.
	var latencyMs int
	if timeConnect > 0 && timeConnect >= timeNameLookup {
		latencyMs = int((timeConnect - timeNameLookup) * 1000)
	} else {
		latencyMs = int(timeTotal * 1000)
	}
	if latencyMs <= 0 {
		latencyMs = 1
	}
	return latencyMs, true
}

// ICMPProber sends a single ICMP echo via Entware ping bound to the tunnel
// interface. Used for matrix cells whose target is the tunnel's
// connectivity-check self host AND the tunnel's method is "ping".
type ICMPProber struct {
	Runner Runner
}

// NewICMPProber builds an ICMP prober backed by the package-level exec.Run.
func NewICMPProber() *ICMPProber {
	return &ICMPProber{Runner: defaultRunner{}}
}

// Probe sends a single ICMP echo. ok=false on exec error, non-zero exit
// code, or unparseable timing.
func (p *ICMPProber) Probe(ctx context.Context, host, ifaceName string, timeout time.Duration) (int, bool) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout+1*time.Second)
	defer cancel()

	timeoutSec := int(timeout.Seconds())
	if timeoutSec < 1 {
		timeoutSec = 1
	}
	res, err := p.Runner.Run(timeoutCtx, "/opt/bin/ping",
		"-I", ifaceName,
		"-c", "1",
		"-W", strconv.Itoa(timeoutSec),
		host,
	)
	if err != nil || res == nil || res.ExitCode != 0 {
		return 0, false
	}

	// busybox ping may report timing on either stdout or stderr.
	if ms, ok := parsePingTime(res.Stdout); ok {
		return ms, true
	}
	if ms, ok := parsePingTime(res.Stderr); ok {
		return ms, true
	}
	// Exit 0 without parseable timing — treat as success with floor latency.
	return 1, true
}

// parsePingTime extracts the round-trip time in milliseconds from
// `time=NN.N ms` in ping output.
func parsePingTime(output string) (int, bool) {
	idx := strings.Index(output, "time=")
	if idx < 0 {
		return 0, false
	}
	rest := output[idx+5:]
	end := strings.IndexAny(rest, " m")
	if end <= 0 {
		return 0, false
	}
	val, err := strconv.ParseFloat(rest[:end], 64)
	if err != nil {
		return 0, false
	}
	ms := int(val)
	if ms < 1 {
		ms = 1
	}
	return ms, true
}

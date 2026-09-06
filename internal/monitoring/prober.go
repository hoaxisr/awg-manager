package monitoring

import (
	"context"
	"time"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/icmpprobe"
)

// Prober probes a single target through a specific interface and returns
// latency in milliseconds + success flag. target is a URL for HTTPProber and
// a host/IP for ICMPProber. Implementations must be safe for concurrent use.
type Prober interface {
	Probe(ctx context.Context, target, ifaceName string, timeout time.Duration) (latencyMs int, ok bool)
}

// HTTPProber is the very probe the manual «Тест» button runs
// (testing.Service.checkHTTP → httpprobe): HTTP GET of the configured
// connectivity-check URL bound to the tunnel interface, success = 2xx/3xx.
// One code path for the card indicator and the manual check means they can
// never disagree. target is the full URL, not a bare host. The default URL is
// plain http, so no TLS handshake burns softfloat-MIPS CPU per tick; an
// https URL chosen by the user costs the same here as in the manual check.
type HTTPProber struct{}

// NewHTTPProber builds the prober shared with the manual connectivity check.
func NewHTTPProber() *HTTPProber { return &HTTPProber{} }

// Probe performs one HTTP GET of target (URL) through ifaceName.
// ok=false on transport error, non-success status or timeout.
func (p *HTTPProber) Probe(ctx context.Context, target, ifaceName string, timeout time.Duration) (int, bool) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := httpprobe.ByInterface(timeoutCtx, ifaceName, target, nil)
	if err != nil || !httpprobe.SuccessCode(res.HTTPCode) {
		return 0, false
	}
	return res.LatencyMs, true
}

// ICMPProber sends a single native ICMP echo bound to the tunnel
// interface. Used for matrix cells whose target is the tunnel's
// connectivity-check self host AND the tunnel's method is "ping".
type ICMPProber struct {
	Pinger func(ctx context.Context, ifaceName, target string, dnsServers []string) (icmpprobe.Result, error)
}

// NewICMPProber builds an ICMP prober backed by the native icmpprobe.
func NewICMPProber() *ICMPProber {
	return &ICMPProber{Pinger: icmpprobe.ByInterface}
}

// Probe sends a single ICMP echo. ok=false on resolve/socket/timeout error.
func (p *ICMPProber) Probe(ctx context.Context, host, ifaceName string, timeout time.Duration) (int, bool) {
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, err := p.Pinger(timeoutCtx, ifaceName, host, nil)
	if err != nil {
		return 0, false
	}
	return res.LatencyMs, true
}

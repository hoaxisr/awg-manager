// Package probe runs the active reachability checks for autolist targets.
//
// Check classifies one observed endpoint by talking to it: a TCP target gets
// a single HTTPS GET through system curl, a UDP target gets one STUN Binding
// Request. Every verdict follows the model rule that error never proves a
// block; error marks a probe that could not judge the path (DNS, local
// network, unexpected transport behaviour).
package probe

import (
	"context"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// probeTimeout bounds every single check. Tests shorten it to keep the
// suite fast; production always sees five seconds.
var probeTimeout = 5 * time.Second

// Path selects the network path a check takes.
type Path struct {
	// Interface optionally names a kernel interface the check must use.
	// Empty means the default routing table. Interface binding is supported
	// on Linux only: curl --interface and SO_BINDTODEVICE.
	Interface string
}

// Result is the outcome of one active check.
type Result struct {
	Verdict  model.Verdict
	Bytes    int
	Duration time.Duration
	Detail   string
	// HTTPStatus is the status of the last HTTP response, 0 when none arrived.
	HTTPStatus int
	// Timings as reported by curl, 0 when unknown.
	TimeDNS     time.Duration
	TimeConnect time.Duration
	TimeTLS     time.Duration
}

// Check dispatches one observed target to its transport probe.
func Check(ctx context.Context, target model.Target, path Path) Result {
	switch {
	case target.Domain == "" && target.IP == "":
		return Result{Verdict: model.VerdictError, Detail: "target requires a domain or an observed IP"}
	case target.Network == model.NetworkUDP:
		return stunCheck(ctx, target, path)
	case target.Network == model.NetworkTCP:
		return httpsCheck(ctx, target, path)
	default:
		return Result{Verdict: model.VerdictError, Detail: "unsupported network " + string(target.Network)}
	}
}

// DirectAndWANResult pairs the direct target check with the independent WAN check.
type DirectAndWANResult struct {
	Direct Result
	WAN    Result
}

// DirectAndWAN starts the target check and the WAN check concurrently and
// returns both. The WAN check proves that a direct blocked verdict is not a
// general Internet outage.
func DirectAndWAN(ctx context.Context, target model.Target, wanURL string, path Path) DirectAndWANResult {
	var out DirectAndWANResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		out.Direct = Check(ctx, target, path)
	}()
	go func() {
		defer wg.Done()
		out.WAN = CheckWAN(ctx, wanURL, path)
	}()
	wg.Wait()
	return out
}

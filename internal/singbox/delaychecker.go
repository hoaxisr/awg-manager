package singbox

import (
	"context"
	"sync"
	"time"
)

// DelayPublisher publishes SSE events. Satisfied by *events.Bus.
type DelayPublisher interface {
	Publish(eventType string, data any)
}

// clashAPI abstracts the subset of ClashClient we use (for testability).
type clashAPI interface {
	TestDelay(name, url string, timeout time.Duration) (int, error)
}

// tunnelLister returns current tunnel tags.
type tunnelLister interface {
	ListTunnels(ctx context.Context) ([]TunnelInfo, error)
}

const (
	defaultDelayInterval = 60 * time.Second
	defaultDelayTimeout  = 5 * time.Second
	defaultDelayTestURL  = "http://www.gstatic.com/generate_204"
	eventSingboxDelay    = "singbox:delay"
)

// DelayChecker runs periodic Clash delay tests for all sing-box tunnels
// and publishes per-tunnel SSE events.
type DelayChecker struct {
	clash     clashAPI
	lister    tunnelLister
	publisher DelayPublisher
	interval  time.Duration
	timeout   time.Duration
	testURL   string

	// mu protects single-flight per tag to avoid hammering Clash when both
	// the periodic tick and an on-demand call race.
	mu       sync.Mutex
	inflight map[string]bool
}

// NewDelayChecker constructs a checker with sane defaults.
func NewDelayChecker(clash clashAPI, lister tunnelLister, pub DelayPublisher) *DelayChecker {
	return &DelayChecker{
		clash:     clash,
		lister:    lister,
		publisher: pub,
		interval:  defaultDelayInterval,
		timeout:   defaultDelayTimeout,
		testURL:   defaultDelayTestURL,
		inflight:  map[string]bool{},
	}
}

// CheckOne runs a single delay test for `tag` and publishes the result.
// Returns the delay in ms (0 on timeout). Errors from Clash are normalized
// to (0, nil) since they represent timeouts from the UI's perspective.
func (d *DelayChecker) CheckOne(ctx context.Context, tag string) (int, error) {
	d.mu.Lock()
	if d.inflight[tag] {
		d.mu.Unlock()
		return 0, nil
	}
	d.inflight[tag] = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		delete(d.inflight, tag)
		d.mu.Unlock()
	}()

	delay, err := d.clash.TestDelay(tag, d.testURL, d.timeout)
	if err != nil {
		delay = 0
	}
	if d.publisher != nil {
		d.publisher.Publish(eventSingboxDelay, map[string]any{
			"tag":       tag,
			"delay":     delay,
			"timestamp": time.Now().Unix(),
		})
	}
	return delay, nil
}

// Check runs a delay test against every known tunnel tag, concurrently.
// Non-blocking per-tag: slow tunnels do not delay others.
func (d *DelayChecker) Check(ctx context.Context) {
	tunnels, err := d.lister.ListTunnels(ctx)
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, t := range tunnels {
		tag := t.Tag
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = d.CheckOne(ctx, tag)
		}()
	}
	wg.Wait()
}

// Run blocks until ctx is cancelled, calling Check every `interval`.
// First tick runs immediately so the UI gets data before the first minute
// elapses.
func (d *DelayChecker) Run(ctx context.Context) {
	if ctx.Err() == nil {
		d.Check(ctx)
	}
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Check(ctx)
		}
	}
}

// ListTunnels delegates to the underlying lister.
func (d *DelayChecker) ListTunnels(ctx context.Context) ([]TunnelInfo, error) {
	return d.lister.ListTunnels(ctx)
}

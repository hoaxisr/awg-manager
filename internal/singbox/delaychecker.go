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
	// ListSubActiveTags returns active outbound tags of enabled
	// subscriptions. The DelayChecker treats each tag identically
	// to a tunnel tag for the purpose of periodic latency tests.
	// Empty slice is fine if no subscriptions are configured.
	ListSubActiveTags() []string
}

const (
	defaultDelayInterval = 60 * time.Second
	defaultDelayTimeout  = 5 * time.Second
	defaultDelayTestURL  = "http://www.gstatic.com/generate_204"
	defaultRetryDelay    = 150 * time.Millisecond
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

	delay := 0
	if firstDelay, firstErr := d.clash.TestDelay(tag, d.testURL, d.timeout); firstErr == nil && firstDelay > 0 {
		delay = firstDelay
	} else {
		// Anti-flap: one transient Clash timeout/spike should not immediately
		// mark the card as failed. Retry once with a short backoff.
		timer := time.NewTimer(defaultRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
			retryDelay, retryErr := d.clash.TestDelay(tag, d.testURL, d.timeout)
			if retryErr == nil && retryDelay > 0 {
				delay = retryDelay
			}
		}
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

// Check runs a delay test against every known tunnel tag and every active
// subscription outbound tag, concurrently. Non-blocking per-tag: slow
// tunnels do not delay others.
func (d *DelayChecker) Check(ctx context.Context) {
	tunnels, err := d.lister.ListTunnels(ctx)
	if err != nil {
		return
	}
	tags := make([]string, 0, len(tunnels)+8)
	seen := make(map[string]bool, len(tunnels)+8)
	for _, t := range tunnels {
		if t.Tag != "" && !seen[t.Tag] {
			seen[t.Tag] = true
			tags = append(tags, t.Tag)
		}
	}
	for _, tag := range d.lister.ListSubActiveTags() {
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	var wg sync.WaitGroup
	for _, tag := range tags {
		tag := tag
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

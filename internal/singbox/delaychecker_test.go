package singbox

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeDelayPublisher struct {
	events []delayPublishRecord
}

type delayPublishRecord struct {
	name string
	data any
}

func (f *fakeDelayPublisher) Publish(name string, data any) {
	f.events = append(f.events, delayPublishRecord{name, data})
}

type fakeClash struct {
	delays  map[string]int
	errs    map[string]error
	lastURL string
	lastTo  time.Duration
}

func (f *fakeClash) TestDelay(name, url string, timeout time.Duration) (int, error) {
	f.lastURL = url
	f.lastTo = timeout
	if err, ok := f.errs[name]; ok {
		return 0, err
	}
	return f.delays[name], nil
}

func TestDelayChecker_CheckOne_Success(t *testing.T) {
	clash := &fakeClash{delays: map[string]int{"A": 42}}
	pub := &fakeDelayPublisher{}
	d := &DelayChecker{
		clash:     clash,
		publisher: pub,
		testURL:   "https://example.com/",
		timeout:   3 * time.Second,
		inflight:  map[string]bool{},
	}
	got, err := d.CheckOne(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	if got != 42 {
		t.Errorf("delay: %d want 42", got)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events: %d", len(pub.events))
	}
	if pub.events[0].name != "singbox:delay" {
		t.Errorf("event: %s", pub.events[0].name)
	}
}

func TestDelayChecker_CheckOne_Timeout(t *testing.T) {
	clash := &fakeClash{errs: map[string]error{"A": errors.New("timeout")}}
	pub := &fakeDelayPublisher{}
	d := &DelayChecker{
		clash:     clash,
		publisher: pub,
		testURL:   "https://example.com/",
		timeout:   3 * time.Second,
		inflight:  map[string]bool{},
	}
	got, err := d.CheckOne(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Errorf("timeout delay should be 0, got %d", got)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events: %d", len(pub.events))
	}
}

type fakeDelayLister struct {
	tunnels []TunnelInfo
}

func (f *fakeDelayLister) ListTunnels(ctx context.Context) ([]TunnelInfo, error) {
	return f.tunnels, nil
}

func TestDelayChecker_Check_AllTunnels(t *testing.T) {
	clash := &fakeClash{delays: map[string]int{"A": 10, "B": 20}}
	lister := &fakeDelayLister{tunnels: []TunnelInfo{{Tag: "A"}, {Tag: "B"}}}
	pub := &fakeDelayPublisher{}
	d := &DelayChecker{
		clash: clash, lister: lister, publisher: pub,
		testURL: "u", timeout: time.Second,
		inflight: map[string]bool{},
	}
	d.Check(context.Background())
	if len(pub.events) != 2 {
		t.Errorf("events: %d want 2", len(pub.events))
	}
}

func TestDelayChecker_Run_CancelsOnCtx(t *testing.T) {
	clash := &fakeClash{}
	lister := &fakeDelayLister{}
	pub := &fakeDelayPublisher{}
	d := &DelayChecker{
		clash: clash, lister: lister, publisher: pub,
		interval: 50 * time.Millisecond,
		testURL:  "u", timeout: time.Second,
		inflight: map[string]bool{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not exit on ctx cancel")
	}
}

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
	seq     map[string][]delayReply
	calls   map[string]int
	lastURL string
	lastTo  time.Duration
}

type delayReply struct {
	delay int
	err   error
}

func (f *fakeClash) TestDelay(name, url string, timeout time.Duration) (int, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[name]++
	f.lastURL = url
	f.lastTo = timeout
	if seq, ok := f.seq[name]; ok && len(seq) > 0 {
		r := seq[0]
		f.seq[name] = seq[1:]
		return r.delay, r.err
	}
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
func (f *fakeDelayLister) ListSubActiveTags() []string { return nil }

// combinedListerStub satisfies the extended tunnelLister interface for tests.
type combinedListerStub struct {
	tunnels []TunnelInfo
	subTags []string
}

func (l *combinedListerStub) ListTunnels(ctx context.Context) ([]TunnelInfo, error) {
	return l.tunnels, nil
}
func (l *combinedListerStub) ListSubActiveTags() []string { return l.subTags }

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

func TestDelayChecker_TicksTunnelsAndSubActiveTags(t *testing.T) {
	clash := &fakeClash{delays: map[string]int{
		"awg-vpn0":         50,
		"sub-AAA-bbbbcccc": 120,
	}}
	lister := &combinedListerStub{
		tunnels: []TunnelInfo{{Tag: "awg-vpn0"}},
		subTags: []string{"sub-AAA-bbbbcccc"},
	}
	pub := &fakeDelayPublisher{}
	dc := NewDelayChecker(clash, lister, pub)
	dc.Check(context.Background())

	if len(pub.events) != 2 {
		t.Fatalf("expected 2 publish events (tunnel + sub-active), got %d", len(pub.events))
	}
	got := map[string]int{}
	for _, e := range pub.events {
		data, _ := e.data.(map[string]any)
		tag, _ := data["tag"].(string)
		delay, _ := data["delay"].(int)
		got[tag] = delay
	}
	if got["awg-vpn0"] != 50 {
		t.Errorf("awg-vpn0 delay = %d, want 50", got["awg-vpn0"])
	}
	if got["sub-AAA-bbbbcccc"] != 120 {
		t.Errorf("sub-AAA-bbbbcccc delay = %d, want 120", got["sub-AAA-bbbbcccc"])
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

func TestDelayChecker_CheckOne_RetryThenSuccess(t *testing.T) {
	clash := &fakeClash{
		seq: map[string][]delayReply{
			"A": {
				{delay: 0, err: errors.New("transient timeout")},
				{delay: 77, err: nil},
			},
		},
	}
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
	if got != 77 {
		t.Errorf("delay after retry: %d want 77", got)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events: %d", len(pub.events))
	}
	if clash.calls["A"] != 2 {
		t.Fatalf("calls: %d want 2", clash.calls["A"])
	}
}

func TestDelayChecker_CheckOne_RetryThenTimeout(t *testing.T) {
	clash := &fakeClash{
		seq: map[string][]delayReply{
			"A": {
				{delay: 0, err: errors.New("timeout")},
				{delay: 0, err: errors.New("timeout")},
			},
		},
	}
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
		t.Errorf("delay after retry timeout: %d want 0", got)
	}
	if clash.calls["A"] != 2 {
		t.Fatalf("calls: %d want 2", clash.calls["A"])
	}
	if len(pub.events) != 1 {
		t.Fatalf("events: %d", len(pub.events))
	}
}

func TestDelayChecker_CheckOne_CtxCanceledDuringBackoff(t *testing.T) {
	clash := &fakeClash{
		seq: map[string][]delayReply{
			"A": {
				{delay: 0, err: errors.New("timeout")},
			},
		},
	}
	pub := &fakeDelayPublisher{}
	d := &DelayChecker{
		clash:     clash,
		publisher: pub,
		testURL:   "https://example.com/",
		timeout:   3 * time.Second,
		inflight:  map[string]bool{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := d.CheckOne(ctx, "A")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if got != 0 {
		t.Errorf("delay on canceled context: %d want 0", got)
	}
	if len(pub.events) != 0 {
		t.Fatalf("events should not be published on canceled context, got %d", len(pub.events))
	}
}

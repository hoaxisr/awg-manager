package metrics

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type fakeRunningProvider struct {
	mu   sync.Mutex
	refs []InterfaceRef
}

func (f *fakeRunningProvider) RunningInterfaces(_ context.Context) []InterfaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]InterfaceRef, len(f.refs))
	copy(out, f.refs)
	return out
}

func (f *fakeRunningProvider) Set(refs []InterfaceRef) {
	f.mu.Lock()
	f.refs = refs
	f.mu.Unlock()
}

type fakeMetricsPublisher struct {
	mu     sync.Mutex
	events []publishedEvent
}

type publishedEvent struct {
	Type string
	Data any
}

func (p *fakeMetricsPublisher) Publish(eventType string, data any) {
	p.mu.Lock()
	p.events = append(p.events, publishedEvent{eventType, data})
	p.mu.Unlock()
}

func (p *fakeMetricsPublisher) Events() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishedEvent, len(p.events))
	copy(out, p.events)
	return out
}

type fakeSubs struct{ count int }

func (f *fakeSubs) SubscriberCount() int { return f.count }

type fakeSnapshotPub struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeSnapshotPub) PublishServerSnapshot(_ context.Context) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
}

func (f *fakeSnapshotPub) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeHistory struct {
	mu      sync.Mutex
	entries []fakeHistoryEntry
}

type fakeHistoryEntry struct {
	ID string
	Rx int64
	Tx int64
}

func (f *fakeHistory) Feed(id string, rx, tx int64) {
	f.mu.Lock()
	f.entries = append(f.entries, fakeHistoryEntry{id, rx, tx})
	f.mu.Unlock()
}

func (f *fakeHistory) Entries() []fakeHistoryEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeHistoryEntry, len(f.entries))
	copy(out, f.entries)
	return out
}

func TestMetricsPoller_PollsRunningAndPublishes(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0/wireguard/peer", `[{"public-key":"k","rxbytes":100,"txbytes":200,"last-handshake":5,"online":true,"enabled":true}]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Second)

	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0", IsServer: false}})

	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}
	history := &fakeHistory{}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.SetHistoryFeeder(history)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)

	evs := pub.Events()
	if len(evs) == 0 {
		t.Fatal("no events published")
	}
	if evs[0].Type != "tunnel:traffic" {
		t.Errorf("first event type: %s", evs[0].Type)
	}
	payload := evs[0].Data.(map[string]any)
	if payload["id"] != "Wireguard0" {
		t.Errorf("id: %v", payload["id"])
	}
	if payload["rxBytes"].(int64) != 100 {
		t.Errorf("rx: %v", payload["rxBytes"])
	}
	hs, ok := payload["lastHandshake"].(string)
	if !ok {
		t.Fatalf("lastHandshake: want string, got %T", payload["lastHandshake"])
	}
	if hs == "" {
		t.Errorf("lastHandshake: want RFC3339, got empty (secondsAgo=5 should produce timestamp)")
	}
	if _, err := time.Parse(time.RFC3339, hs); err != nil {
		t.Errorf("lastHandshake %q not RFC3339: %v", hs, err)
	}
	if _, extra := payload["lastHandshakeSecondsAgo"]; extra {
		t.Errorf("lastHandshakeSecondsAgo must not be on the wire anymore")
	}
	if _, extra := payload["peerCount"]; extra {
		t.Errorf("peerCount must not be on the wire anymore")
	}

	ents := history.Entries()
	if len(ents) == 0 {
		t.Fatal("history feed not called")
	}
	if ents[0].ID != "Wireguard0" || ents[0].Rx != 100 || ents[0].Tx != 200 {
		t.Errorf("history entry: %+v", ents[0])
	}
}

func TestMetricsPoller_EmptyPeersCooldown_SkipsSubsequentTicks(t *testing.T) {
	// Server with no peers returns an empty list. After the first
	// tick observes this, the poller must skip that interface for
	// the cooldown period so NDMS isn't hammered every 10s for
	// nothing.
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard1/wireguard/peer", `[]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Millisecond)

	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard1", IsServer: false}})

	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 10*time.Millisecond)
	p.Start()
	defer p.Stop()

	// Let at least 6 ticks fire. Without the cooldown, that's 6 RCI
	// calls. With it, only the first tick hits NDMS and every
	// following tick is skipped.
	time.Sleep(80 * time.Millisecond)

	calls := fg.Calls("/show/interface/Wireguard1/wireguard/peer")
	if calls == 0 {
		t.Fatal("expected at least one RCI call to prime the empty state")
	}
	if calls > 2 {
		t.Errorf("cooldown not honoured: want at most 2 RCI calls, got %d", calls)
	}
}

func TestMetricsPoller_SkipsWhenNoSubscribers(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0/wireguard/peer", `[]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Second)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0"}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 0}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 10*time.Millisecond)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)

	if len(pub.Events()) != 0 {
		t.Errorf("published events despite 0 subscribers: %d", len(pub.Events()))
	}
}

func TestMetricsPoller_ServerChange_InvokesSnapshotCallback(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard10/wireguard/peer", `[{"public-key":"k","rxbytes":1,"txbytes":2,"last-handshake":0,"online":true,"enabled":true}]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Second)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard10", IsServer: true}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}
	snap := &fakeSnapshotPub{}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.SetServerSnapshotPublisher(snap)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)

	if snap.Calls() == 0 {
		t.Fatal("server snapshot callback not invoked for changed server interface")
	}
	for _, ev := range pub.Events() {
		if ev.Type == "server:updated" {
			t.Errorf("MetricsPoller must not publish server:updated directly, got %+v", ev)
		}
	}
}

func TestMetricsPoller_ServerNilCallback_NoPublish(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard10/wireguard/peer", `[{"public-key":"k","rxbytes":1,"txbytes":2,"last-handshake":0,"online":true,"enabled":true}]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Second)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard10", IsServer: true}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.Start()
	defer p.Stop()

	time.Sleep(50 * time.Millisecond)
	for _, ev := range pub.Events() {
		if ev.Type == "server:updated" {
			t.Errorf("no snapshot callback wired → no server:updated should be published, got %+v", ev)
		}
	}
}

func TestMetricsPoller_DedupsUnchangedData(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0/wireguard/peer", `[{"public-key":"k","rxbytes":100,"txbytes":200,"last-handshake":5,"online":true,"enabled":true}]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Millisecond)
	run := &fakeRunningProvider{}
	run.Set([]InterfaceRef{{ID: "Wireguard0"}})
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 10*time.Millisecond)
	p.Start()
	defer p.Stop()

	time.Sleep(80 * time.Millisecond)

	evs := pub.Events()
	if len(evs) != 1 {
		t.Errorf("dedupe: want 1 event for unchanged data, got %d", len(evs))
	}
}

func TestMetricsPoller_Stop_Idempotent(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/Wireguard0/wireguard/peer", `[]`)
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Second)
	run := &fakeRunningProvider{}
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 1}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	p.Start()
	p.Stop()
	p.Stop() // must not panic
}

func TestMetricsPoller_Stop_WithoutStart(t *testing.T) {
	fg := query.NewFakeGetter()
	peers := query.NewPeerStoreWithTTL(fg, query.NopLogger(), 1*time.Second)
	run := &fakeRunningProvider{}
	pub := &fakeMetricsPublisher{}
	subs := &fakeSubs{count: 0}

	p := NewWithInterval(peers, pub, run, subs, NopLogger(), 20*time.Millisecond)
	// Stop without Start — must not block, must not panic.
	done := make(chan struct{})
	go func() { p.Stop(); close(done) }()
	select {
	case <-done:
		// Good
	case <-time.After(100 * time.Millisecond):
		t.Errorf("Stop without Start should return immediately")
	}
}

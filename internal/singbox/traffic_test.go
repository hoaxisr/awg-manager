package singbox

import (
	"testing"
)

type publishedEvent struct {
	name string
	data any
}

type fakePublisher struct {
	events []publishedEvent
}

func (f *fakePublisher) Publish(event string, data any) {
	f.events = append(f.events, publishedEvent{name: event, data: data})
}

type feedCall struct {
	id     string
	rx, tx int64
}

type fakeFeeder struct {
	calls []feedCall
}

func (f *fakeFeeder) Feed(id string, rxBytes, txBytes int64) {
	f.calls = append(f.calls, feedCall{id: id, rx: rxBytes, tx: txBytes})
}

func TestTrafficAggregator_Ingest(t *testing.T) {
	pub := &fakePublisher{}
	agg := NewTrafficAggregator(func() string { return "unused" }, pub, nil)
	msg := []byte(`{
		"downloadTotal": 900000,
		"uploadTotal": 40000,
		"connections": [
			{"chains":["Germany"],"upload":100,"download":500},
			{"chains":["Germany"],"upload":50,"download":200},
			{"chains":["Finland"],"upload":10,"download":30}
		]
	}`)
	agg.ingest(msg)
	if len(agg.tags) != 2 {
		t.Fatalf("tags: %d", len(agg.tags))
	}
	if agg.tags["Germany"].Upload != 150 || agg.tags["Germany"].Download != 700 {
		t.Errorf("Germany: %+v", agg.tags["Germany"])
	}
	if agg.tags["Finland"].Upload != 10 {
		t.Errorf("Finland: %+v", agg.tags["Finland"])
	}
	if agg.downloadTotal != 900000 || agg.uploadTotal != 40000 {
		t.Errorf("totals: down=%d up=%d, want 900000/40000", agg.downloadTotal, agg.uploadTotal)
	}
}

func TestTrafficAggregator_Publish(t *testing.T) {
	pub := &fakePublisher{}
	agg := NewTrafficAggregator(func() string { return "unused" }, pub, nil)
	agg.tags["A"] = &TrafficSnapshot{Tag: "A", Upload: 1, Download: 2}
	agg.tags["B"] = &TrafficSnapshot{Tag: "B", Upload: 3, Download: 4}
	agg.downloadTotal = 123
	agg.uploadTotal = 45
	agg.publish()
	// publish emits singbox:traffic + singbox:traffic-totals + singbox:memory.
	if len(pub.events) != 3 {
		t.Fatalf("events: %d, want 3", len(pub.events))
	}
	if pub.events[0].name != "singbox:traffic" {
		t.Fatalf("first event name: %q", pub.events[0].name)
	}
	snap, ok := pub.events[0].data.([]TrafficSnapshot)
	if !ok {
		t.Fatalf("first event data type: %T", pub.events[0].data)
	}
	if len(snap) != 2 {
		t.Errorf("snap len: %d", len(snap))
	}
	if pub.events[1].name != "singbox:traffic-totals" {
		t.Fatalf("second event name: %q", pub.events[1].name)
	}
	totals, ok := pub.events[1].data.(TrafficTotalsEvent)
	if !ok {
		t.Fatalf("totals event data type: %T", pub.events[1].data)
	}
	if totals.DownloadTotal != 123 || totals.UploadTotal != 45 {
		t.Errorf("totals event: %+v", totals)
	}
}

func TestTrafficAggregator_PublishFeedsHistory(t *testing.T) {
	pub := &fakePublisher{}
	feeder := &fakeFeeder{}
	agg := NewTrafficAggregator(func() string { return "unused" }, pub, feeder)
	agg.tags["A"] = &TrafficSnapshot{Tag: "A", Upload: 100, Download: 500}
	agg.tags["B"] = &TrafficSnapshot{Tag: "B", Upload: 7, Download: 13}
	agg.publish()

	if len(feeder.calls) != 2 {
		t.Fatalf("feeder calls: %d, want 2", len(feeder.calls))
	}
	// Map iteration is non-deterministic — index calls by id.
	got := map[string]feedCall{}
	for _, c := range feeder.calls {
		got[c.id] = c
	}
	if got["A"].rx != 500 || got["A"].tx != 100 {
		t.Errorf("A: rx=%d tx=%d, want rx=500 tx=100", got["A"].rx, got["A"].tx)
	}
	if got["B"].rx != 13 || got["B"].tx != 7 {
		t.Errorf("B: rx=%d tx=%d, want rx=13 tx=7", got["B"].rx, got["B"].tx)
	}
}

func TestTrafficAggregator_PublishWithoutFeederIsSafe(t *testing.T) {
	pub := &fakePublisher{}
	agg := NewTrafficAggregator(func() string { return "unused" }, pub, nil)
	agg.tags["A"] = &TrafficSnapshot{Tag: "A", Upload: 1, Download: 2}
	// Should not panic.
	agg.publish()
}

func TestTrafficAggregator_IngestBadJSON(t *testing.T) {
	agg := NewTrafficAggregator(func() string { return "unused" }, nil, nil)
	agg.ingest([]byte(`not json`))
	if len(agg.tags) != 0 {
		t.Errorf("bad json should not mutate state")
	}
}

func TestTrafficAggregator_IngestEmptyChains(t *testing.T) {
	agg := NewTrafficAggregator(func() string { return "unused" }, nil, nil)
	msg := []byte(`{"connections":[{"chains":[],"upload":100,"download":500}]}`)
	agg.ingest(msg)
	if len(agg.tags) != 0 {
		t.Errorf("empty chains should be skipped")
	}
}

// Subscriptions / selector groups appear as chains[0]. Crediting every hop
// (not just the innermost) means the subscription card and the terminal
// member card both surface non-zero traffic — the v2.10.0 regression where
// "Суммарный трафик" sat at 0 B on the subscriptions page.
func TestTrafficAggregator_IngestCreditsEveryChainHop(t *testing.T) {
	agg := NewTrafficAggregator(func() string { return "unused" }, nil, nil)
	msg := []byte(`{"connections":[{"chains":["Proxy0","Germany"],"upload":10,"download":20}]}`)
	agg.ingest(msg)
	if got := agg.tags["Germany"]; got == nil || got.Upload != 10 || got.Download != 20 {
		t.Errorf("Germany (terminal): want upload=10 download=20, got %+v", got)
	}
	if got := agg.tags["Proxy0"]; got == nil || got.Upload != 10 || got.Download != 20 {
		t.Errorf("Proxy0 (selector wrapper): want upload=10 download=20, got %+v", got)
	}
}

// Per-connection dedup: Clash sometimes emits transitively self-referential
// chains (e.g. when a selector resolves through itself). Each connection's
// bytes must credit each tag exactly once.
func TestTrafficAggregator_IngestDedupsWithinSingleConnection(t *testing.T) {
	agg := NewTrafficAggregator(func() string { return "unused" }, nil, nil)
	msg := []byte(`{"connections":[{"chains":["A","A","B"],"upload":7,"download":13}]}`)
	agg.ingest(msg)
	if got := agg.tags["A"]; got == nil || got.Upload != 7 || got.Download != 13 {
		t.Errorf("A (repeated in chain): want single credit upload=7 download=13, got %+v", got)
	}
	if got := agg.tags["B"]; got == nil || got.Upload != 7 || got.Download != 13 {
		t.Errorf("B: want upload=7 download=13, got %+v", got)
	}
}

func TestTrafficAggregator_PublishEmitsMemoryEvent(t *testing.T) {
	pub := &fakePublisher{}
	agg := NewTrafficAggregator(func() string { return "unused" }, pub, nil)
	agg.ingest([]byte(`{"memory":99999,"connections":[]}`))
	agg.publish()
	// publish emits singbox:traffic, singbox:traffic-totals and singbox:memory.
	if len(pub.events) != 3 {
		t.Fatalf("events: got %d, want 3", len(pub.events))
	}
	if pub.events[2].name != "singbox:memory" {
		t.Fatalf("third event name: %q", pub.events[2].name)
	}
	ev, ok := pub.events[2].data.(MemoryEvent)
	if !ok {
		t.Fatalf("third event data type: %T, want MemoryEvent", pub.events[2].data)
	}
	if ev.Memory != 99999 {
		t.Errorf("MemoryEvent.Memory: got %d, want 99999", ev.Memory)
	}
}

// Cross-connection accumulation still works after the every-hop change —
// two connections through the same chain double the totals on every tag.
func TestTrafficAggregator_IngestAccumulatesAcrossConnections(t *testing.T) {
	agg := NewTrafficAggregator(func() string { return "unused" }, nil, nil)
	msg := []byte(`{"connections":[
		{"chains":["Proxy0","Germany"],"upload":10,"download":20},
		{"chains":["Proxy0","Germany"],"upload":100,"download":200}
	]}`)
	agg.ingest(msg)
	if got := agg.tags["Proxy0"]; got == nil || got.Upload != 110 || got.Download != 220 {
		t.Errorf("Proxy0 across two conns: want upload=110 download=220, got %+v", got)
	}
	if got := agg.tags["Germany"]; got == nil || got.Upload != 110 || got.Download != 220 {
		t.Errorf("Germany across two conns: want upload=110 download=220, got %+v", got)
	}
}

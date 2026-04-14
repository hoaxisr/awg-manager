package singbox

import (
	"testing"
)

type fakePublisher struct {
	events []any
}

func (f *fakePublisher) Publish(event string, data any) {
	f.events = append(f.events, data)
}

func TestTrafficAggregator_Ingest(t *testing.T) {
	pub := &fakePublisher{}
	agg := NewTrafficAggregator("unused", pub)
	msg := []byte(`{
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
}

func TestTrafficAggregator_Publish(t *testing.T) {
	pub := &fakePublisher{}
	agg := NewTrafficAggregator("unused", pub)
	agg.tags["A"] = &TrafficSnapshot{Tag: "A", Upload: 1, Download: 2}
	agg.tags["B"] = &TrafficSnapshot{Tag: "B", Upload: 3, Download: 4}
	agg.publish()
	if len(pub.events) != 1 {
		t.Fatalf("events: %d", len(pub.events))
	}
	snap, ok := pub.events[0].([]TrafficSnapshot)
	if !ok {
		t.Fatalf("type: %T", pub.events[0])
	}
	if len(snap) != 2 {
		t.Errorf("snap len: %d", len(snap))
	}
}

func TestTrafficAggregator_IngestBadJSON(t *testing.T) {
	agg := NewTrafficAggregator("unused", nil)
	agg.ingest([]byte(`not json`))
	if len(agg.tags) != 0 {
		t.Errorf("bad json should not mutate state")
	}
}

func TestTrafficAggregator_IngestEmptyChains(t *testing.T) {
	agg := NewTrafficAggregator("unused", nil)
	msg := []byte(`{"connections":[{"chains":[],"upload":100,"download":500}]}`)
	agg.ingest(msg)
	if len(agg.tags) != 0 {
		t.Errorf("empty chains should be skipped")
	}
}

func TestTrafficAggregator_IngestPicksInnermostChain(t *testing.T) {
	agg := NewTrafficAggregator("unused", nil)
	msg := []byte(`{"connections":[{"chains":["Selector","Germany"],"upload":10,"download":20}]}`)
	agg.ingest(msg)
	if _, ok := agg.tags["Germany"]; !ok {
		t.Errorf("expected Germany (innermost) to be tracked, got %v", agg.tags)
	}
	if _, ok := agg.tags["Selector"]; ok {
		t.Errorf("should NOT track Selector (group wrapper)")
	}
}

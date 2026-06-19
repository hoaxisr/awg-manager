package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// TrafficSnapshot is per-tunnel traffic (bytes since process start).
type TrafficSnapshot struct {
	Tag      string `json:"tag"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

// TrafficPublisher is implemented by the SSE bus.
type TrafficPublisher interface {
	Publish(event string, data any)
}

// HistoryFeeder is the minimal traffic-history surface the aggregator uses
// for sparkline backfill. *traffic.History satisfies this. Optional — pass
// nil to skip the per-publish feed (used in unit tests).
type HistoryFeeder interface {
	Feed(tunnelID string, rxBytes, txBytes int64)
}

// TrafficAggregator watches the Clash /connections WebSocket and aggregates
// upload/download bytes per outbound tag, publishing periodic snapshots.
// When a HistoryFeeder is provided, each publish also feeds the rate-history
// store so /api/tunnels/traffic backfill works for singbox tags.
type TrafficAggregator struct {
	clashAddr string
	publisher TrafficPublisher
	feeder    HistoryFeeder
	interval  time.Duration

	mu     sync.Mutex
	tags   map[string]*TrafficSnapshot
	memory int64
}

func NewTrafficAggregator(clashAddr string, pub TrafficPublisher, feeder HistoryFeeder) *TrafficAggregator {
	return &TrafficAggregator{
		clashAddr: clashAddr,
		publisher: pub,
		feeder:    feeder,
		interval:  2 * time.Second,
		tags:      map[string]*TrafficSnapshot{},
	}
}

// Run blocks until ctx is canceled. Reconnects to Clash /connections on
// disconnect with a small backoff.
func (t *TrafficAggregator) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		t.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
			// reconnect
		}
	}
}

func (t *TrafficAggregator) runOnce(ctx context.Context) {
	url := fmt.Sprintf("ws://%s/connections", t.clashAddr)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20) // 1 MiB per message is generous for /connections

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	readCh := make(chan []byte, 4)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case readCh <- msg:
			default:
				// drop if consumer is behind
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			return
		case msg := <-readCh:
			t.ingest(msg)
		case <-ticker.C:
			t.publish()
		}
	}
}

// ingest updates per-tag totals from a /connections message.
// Clash /connections emits a full snapshot on each tick — so we REPLACE totals
// (not accumulate) per the Clash API semantics.
//
// Every tag in a connection's chains gets credited the connection's bytes.
// chains lists outbounds from outermost wrapper (selector / urltest group)
// to innermost terminal outbound. Crediting both ends — and any
// intermediate hop — lets the UI surface traffic both per terminal-tunnel
// (which subscription members carry as their tag) and per subscription /
// selector group (where the group's tag is chains[0]); previously
// chains[0] was discarded and subscription cards always showed 0 B.
//
// Within one connection we deduplicate by tag so a chain like
// ["A","A","B"] still credits A only once — Clash sometimes repeats tags
// when a selector points to itself transitively, and double-counting one
// connection's bytes would distort the per-tag totals.
//
// Multiple connections sharing the same tag accumulate, as before.
func (t *TrafficAggregator) ingest(msg []byte) {
	var m struct {
		Memory      int64 `json:"memory"`
		Connections []struct {
			Chains   []string `json:"chains"`
			Upload   int64    `json:"upload"`
			Download int64    `json:"download"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(msg, &m); err != nil {
		return
	}
	sums := map[string]*TrafficSnapshot{}
	seenWithinConn := make(map[string]struct{})
	for _, conn := range m.Connections {
		if len(conn.Chains) == 0 {
			continue
		}
		for k := range seenWithinConn {
			delete(seenWithinConn, k)
		}
		for _, tag := range conn.Chains {
			if tag == "" {
				continue
			}
			if _, dup := seenWithinConn[tag]; dup {
				continue
			}
			seenWithinConn[tag] = struct{}{}
			s, ok := sums[tag]
			if !ok {
				s = &TrafficSnapshot{Tag: tag}
				sums[tag] = s
			}
			s.Upload += conn.Upload
			s.Download += conn.Download
		}
	}
	t.mu.Lock()
	t.tags = sums
	t.memory = m.Memory
	t.mu.Unlock()
}

// MemoryEvent is the payload of the "singbox:memory" SSE event.
type MemoryEvent struct {
	Memory int64 `json:"memory"`
}

// publish emits the current snapshot to SSE and (optionally) feeds the
// history store. Download maps to rxBytes (received), Upload to txBytes (sent).
func (t *TrafficAggregator) publish() {
	t.mu.Lock()
	snap := make([]TrafficSnapshot, 0, len(t.tags))
	for _, s := range t.tags {
		snap = append(snap, *s)
	}
	mem := t.memory
	t.mu.Unlock()
	if t.publisher != nil {
		t.publisher.Publish("singbox:traffic", snap)
		t.publisher.Publish("singbox:memory", MemoryEvent{Memory: mem})
	}
	if t.feeder != nil {
		for _, s := range snap {
			t.feeder.Feed(s.Tag, s.Download, s.Upload)
		}
	}
}

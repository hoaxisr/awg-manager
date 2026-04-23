// Package metrics runs a single ticker that publishes per-interface
// WireGuard peer metrics over SSE. It consumes query.PeerStore for
// reads and invokes optional callbacks (server snapshot + history
// feed) when changes are detected.
package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Logger is the narrow Warnf-only surface this package uses. Equivalent
// to query.Logger so consumers that already have a query.Logger can
// pass it straight through.
type Logger interface {
	Warnf(format string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Warnf(string, ...any) {}

// NopLogger returns a Logger that drops everything. Use in tests.
func NopLogger() Logger { return nopLogger{} }

// Poller is a ticker that fetches peer metrics for non-managed system
// WG tunnels and server interfaces at a fixed cadence. It uses the
// narrow /show/interface/<name>/wireguard/peer endpoint, publishes
// tunnel:traffic (non-managed system tunnels) and triggers
// server:updated snapshots (server interfaces).
//
// Managed AWGM tunnels are NOT served from here — they are driven by
// traffic.SysfsPoller via /sys/class/net counters (wired separately
// in cmd/awg-manager/main.go).
type Poller struct {
	peers       *query.PeerStore
	publisher   Publisher
	running     RunningInterfacesProvider
	subscribers SubscriberCounter
	interval    time.Duration
	log         Logger

	mu          sync.Mutex
	prev        map[string]peerDigest
	// emptyUntil holds per-interface cooldown timestamps. Once an
	// interface is observed with zero peers, we skip polling it for
	// emptyCooldown — no point hitting NDMS every 10s for an empty
	// server (e.g. one freshly created with no clients added yet).
	// Invalidate<All>() clears this map whenever the list of peers is refreshed.
	emptyUntil  map[string]time.Time
	snapshotPub ServerSnapshotPublisher
	history     HistoryFeeder
	stopCh      chan struct{}
	doneCh      chan struct{}
	stopOnce    sync.Once
	startOnce   sync.Once
	started     atomic.Bool
}

// emptyCooldown is how long an interface observed with zero peers is
// skipped before being polled again. One minute is short enough for
// the UI to discover a newly-added peer without feeling laggy, and
// long enough to slash RCI load for servers sitting idle.
const emptyCooldown = 60 * time.Second

// InterfaceRef names one interface to poll metrics for, plus its role.
// IsServer switches the SSE event shape between tunnel:traffic (false)
// and server:updated (true).
type InterfaceRef struct {
	ID       string
	IsServer bool
}

// RunningInterfacesProvider returns the list of interfaces the
// MetricsPoller should fetch metrics for on each tick.
type RunningInterfacesProvider interface {
	RunningInterfaces(ctx context.Context) []InterfaceRef
}

// Publisher receives per-event publish calls. Implemented by *events.Bus;
// a spy in tests.
type Publisher interface {
	Publish(eventType string, data any)
}

// SubscriberCounter reports the current number of SSE subscribers.
// MetricsPoller skips work when zero.
type SubscriberCounter interface {
	SubscriberCount() int
}

// ServerSnapshotPublisher publishes a full server:updated snapshot. The
// MetricsPoller invokes this once per tick when any server's peer metrics
// changed — the receiver (api.ServersHandler) is responsible for composing
// and broadcasting the { servers, managed, managedStats, wanIP } payload.
type ServerSnapshotPublisher interface {
	PublishServerSnapshot(ctx context.Context)
}

// HistoryFeeder records per-tunnel byte counters into a rate history.
// Called per tunnel whose peer metrics changed on a tick.
type HistoryFeeder interface {
	Feed(tunnelID string, rxBytes, txBytes int64)
}

const defaultInterval = 10 * time.Second

// New wires a Poller with production defaults.
func New(peers *query.PeerStore, pub Publisher, running RunningInterfacesProvider, subs SubscriberCounter, log Logger) *Poller {
	return NewWithInterval(peers, pub, running, subs, log, defaultInterval)
}

// NewWithInterval is the test-friendly constructor that overrides the
// ticker interval.
func NewWithInterval(peers *query.PeerStore, pub Publisher, running RunningInterfacesProvider, subs SubscriberCounter, log Logger, interval time.Duration) *Poller {
	if log == nil {
		log = NopLogger()
	}
	return &Poller{
		peers:       peers,
		publisher:   pub,
		running:     running,
		subscribers: subs,
		interval:    interval,
		log:         log,
		prev:        make(map[string]peerDigest),
		emptyUntil:  make(map[string]time.Time),
		stopCh:      make(chan struct{}),
		doneCh:      make(chan struct{}),
	}
}

// SetServerSnapshotPublisher wires the callback used to publish the full
// server:updated snapshot whenever any server's peer metrics change.
// Optional — if nil, server-role interfaces are still observed (for dedupe
// bookkeeping) but no SSE event is emitted for them.
func (p *Poller) SetServerSnapshotPublisher(pub ServerSnapshotPublisher) {
	p.mu.Lock()
	p.snapshotPub = pub
	p.mu.Unlock()
}

// SetHistoryFeeder wires the per-tunnel byte-counter feed into traffic.History.
// Optional — if nil, no history is recorded.
func (p *Poller) SetHistoryFeeder(h HistoryFeeder) {
	p.mu.Lock()
	p.history = h
	p.mu.Unlock()
}

// Start launches the ticker in a goroutine. Non-blocking. Safe to call
// multiple times — subsequent calls are no-ops.
func (p *Poller) Start() {
	p.startOnce.Do(func() {
		p.started.Store(true)
		go p.run()
	})
}

// Stop halts the ticker and waits for the goroutine to exit. Safe to
// call multiple times.
func (p *Poller) Stop() {
	p.stopOnce.Do(func() {
		close(p.stopCh)
	})
	if p.started.Load() {
		<-p.doneCh
	}
}

func (p *Poller) run() {
	defer close(p.doneCh)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

func (p *Poller) tick() {
	if p.subscribers != nil && p.subscribers.SubscriberCount() == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.interval)
	defer cancel()

	refs := p.running.RunningInterfaces(ctx)
	if len(refs) == 0 {
		return
	}

	// Drop interfaces that are within their known-empty cooldown. For
	// a server with zero peers there's nothing useful to poll — the
	// narrow /wireguard/peer endpoint just 404s or returns [] and we
	// don't want to hammer NDMS every 10s for each idle server.
	now := time.Now()
	p.mu.Lock()
	pollRefs := make([]InterfaceRef, 0, len(refs))
	for _, r := range refs {
		if until, ok := p.emptyUntil[r.ID]; ok && until.After(now) {
			continue
		}
		pollRefs = append(pollRefs, r)
	}
	p.mu.Unlock()
	if len(pollRefs) == 0 {
		return
	}

	type result struct {
		ref   InterfaceRef
		peers []ndms.Peer
		err   error
	}
	results := make(chan result, len(pollRefs))
	for _, ref := range pollRefs {
		go func(r InterfaceRef) {
			peers, err := p.peers.GetPeers(ctx, r.ID)
			results <- result{ref: r, peers: peers, err: err}
		}(ref)
	}

	var collected []result
	for range pollRefs {
		r := <-results
		collected = append(collected, r)
	}

	// Partition changed items under the lock, then publish outside of it:
	// the server snapshot callback may issue its own NDMS reads, and we do
	// not want to hold the dedupe mutex during that.
	var changedTunnels []result
	serverChanged := false

	p.mu.Lock()
	for _, r := range collected {
		if r.err != nil {
			p.log.Warnf("metrics %s: %v", r.ref.ID, r.err)
			continue
		}
		// Track empty-peer cooldown. PeerStore translates NDMS 404
		// into an empty slice (no peers configured), so len(peers)==0
		// reliably means "nothing to measure here" — skip for a bit.
		if len(r.peers) == 0 {
			p.emptyUntil[r.ref.ID] = now.Add(emptyCooldown)
		} else {
			delete(p.emptyUntil, r.ref.ID)
		}
		digest := digestPeers(r.peers)
		prev, hadPrev := p.prev[r.ref.ID]
		if hadPrev && digest.equal(prev) {
			continue
		}
		p.prev[r.ref.ID] = digest
		if r.ref.IsServer {
			serverChanged = true
		} else {
			changedTunnels = append(changedTunnels, r)
		}
	}
	snapshotPub := p.snapshotPub
	history := p.history
	p.mu.Unlock()

	for _, r := range changedTunnels {
		p.publishTunnel(r.ref, r.peers, history)
	}
	if serverChanged && snapshotPub != nil {
		snapshotPub.PublishServerSnapshot(ctx)
	}
}

// publishTunnel emits the tunnel:traffic SSE event and feeds traffic.History.
// Payload matches the legacy wire contract ({id,rxBytes,txBytes,lastHandshake})
// so the frontend handler (TunnelTrafficEvent) reads the same fields as before.
// startedAt is intentionally omitted — the poller has no signal for it; the
// frontend store preserves the existing value when the field is absent.
func (p *Poller) publishTunnel(ref InterfaceRef, peers []ndms.Peer, history HistoryFeeder) {
	var rx, tx int64
	var minSecondsAgo int64 = -1
	for _, pp := range peers {
		rx += pp.RxBytes
		tx += pp.TxBytes
		if pp.LastHandshakeSecondsAgo >= 0 && (minSecondsAgo < 0 || pp.LastHandshakeSecondsAgo < minSecondsAgo) {
			minSecondsAgo = pp.LastHandshakeSecondsAgo
		}
	}
	var lastHandshake string
	if minSecondsAgo >= 0 {
		lastHandshake = time.Now().Add(-time.Duration(minSecondsAgo) * time.Second).UTC().Format(time.RFC3339)
	}
	p.publisher.Publish("tunnel:traffic", map[string]any{
		"id":            ref.ID,
		"rxBytes":       rx,
		"txBytes":       tx,
		"lastHandshake": lastHandshake,
	})
	if history != nil {
		history.Feed(ref.ID, rx, tx)
	}
}

type peerDigest struct {
	rxSum        int64
	txSum        int64
	minHandshake int64
	peerCount    int
}

func digestPeers(peers []ndms.Peer) peerDigest {
	d := peerDigest{minHandshake: -1, peerCount: len(peers)}
	for _, p := range peers {
		d.rxSum += p.RxBytes
		d.txSum += p.TxBytes
		if d.minHandshake < 0 || p.LastHandshakeSecondsAgo < d.minHandshake {
			d.minHandshake = p.LastHandshakeSecondsAgo
		}
	}
	return d
}

func (d peerDigest) equal(other peerDigest) bool {
	return d.rxSum == other.rxSum && d.txSum == other.txSum &&
		d.minHandshake == other.minHandshake && d.peerCount == other.peerCount
}

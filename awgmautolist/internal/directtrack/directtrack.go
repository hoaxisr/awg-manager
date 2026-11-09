// Package directtrack correlates packet flows with the route observed in
// Clash snapshots. Packet capture happens before sing-box makes its routing
// decision, so a missing entry means unknown, never direct.
package directtrack

import (
	"net/netip"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

const (
	// DefaultTTL keeps recently closed flows available for correlation while
	// remaining shorter than the judge's five-minute flow lifetime.
	DefaultTTL = 2 * time.Minute
	// DefaultCapacity bounds route memory independently of packet throughput.
	DefaultCapacity = 2048
	pruneInterval   = time.Second
)

// Route is the latest known sing-box decision for a five-tuple.
type Route uint8

const (
	Unknown Route = iota
	Direct
	NonDirect
)

func (r Route) String() string {
	switch r {
	case Direct:
		return "direct"
	case NonDirect:
		return "non-direct"
	default:
		return "unknown"
	}
}

type entry struct {
	direct bool
	at     time.Time
}

// Tracker is a bounded, race-safe collection of recent route observations.
// The latest observation wins: a flow known to be non-direct must not become
// eligible because an older direct observation is still in memory.
type Tracker struct {
	mu        sync.Mutex
	entries   map[packet.Flow]entry
	capacity  int
	ttl       time.Duration
	lastPrune time.Time
}

// New constructs a tracker. Non-positive capacity or TTL selects a default.
func New(capacity int, ttl time.Duration) *Tracker {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Tracker{entries: make(map[packet.Flow]entry), capacity: capacity, ttl: ttl}
}

// Observe records the current direct/non-direct decision for a complete
// five-tuple. Incomplete Clash metadata is ignored rather than guessed.
func (t *Tracker) Observe(flow packet.Flow, direct bool, now time.Time) {
	if t == nil {
		return
	}
	flow, ok := completeFlow(flow)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)
	if _, exists := t.entries[flow]; !exists && len(t.entries) >= t.capacity {
		t.evictOldest()
	}
	t.entries[flow] = entry{direct: direct, at: now}
}

// Classify returns the latest live decision. Unknown includes both flows never
// seen by Clash and flows whose last snapshot entry has expired.
func (t *Tracker) Classify(flow packet.Flow, now time.Time) Route {
	if t == nil {
		return Unknown
	}
	flow, ok := completeFlow(flow)
	if !ok {
		return Unknown
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, exists := t.entries[flow]
	if !exists {
		return Unknown
	}
	if expired(e.at, now, t.ttl) {
		delete(t.entries, flow)
		return Unknown
	}
	if e.direct {
		return Direct
	}
	return NonDirect
}

func completeFlow(flow packet.Flow) (packet.Flow, bool) {
	if !flow.Src.IsValid() || !flow.Dst.IsValid() || flow.SrcPort == 0 || flow.DstPort == 0 {
		return packet.Flow{}, false
	}
	if flow.Protocol != packet.ProtocolTCP && flow.Protocol != packet.ProtocolUDP {
		return packet.Flow{}, false
	}
	flow.Src = canonicalAddr(flow.Src)
	flow.Dst = canonicalAddr(flow.Dst)
	return flow, true
}

func canonicalAddr(addr netip.Addr) netip.Addr { return addr.Unmap() }

func expired(at, now time.Time, ttl time.Duration) bool {
	return !now.Before(at) && now.Sub(at) > ttl
}

// prune is called by both writes and reads often enough to keep idle memory
// bounded. At capacity, an expired entry is reused before the oldest live one.
func (t *Tracker) prune(now time.Time) {
	// Classify проверяет TTL выбранной записи сам, поэтому полный scan можно
	// амортизировать вместо выполнения на каждый packet result/Clash flow.
	if !t.lastPrune.IsZero() && now.Sub(t.lastPrune) < pruneInterval {
		return
	}
	t.lastPrune = now
	for flow, e := range t.entries {
		if expired(e.at, now, t.ttl) {
			delete(t.entries, flow)
		}
	}
}

func (t *Tracker) evictOldest() {
	var oldest packet.Flow
	var at time.Time
	first := true
	for flow, e := range t.entries {
		if first || e.at.Before(at) {
			oldest, at, first = flow, e.at, false
		}
	}
	if !first {
		delete(t.entries, oldest)
	}
}

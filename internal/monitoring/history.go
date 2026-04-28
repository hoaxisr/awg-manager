package monitoring

import (
	"strings"
	"sync"
	"time"
)

// Sample is a single probe result for a (target, tunnel) pair.
type Sample struct {
	TS        time.Time `json:"ts"`
	LatencyMs *int      `json:"latencyMs"` // nil when probe failed
	OK        bool      `json:"ok"`
}

// HistoryCapacity is the per-(target, tunnel) ring buffer size — 1 hour at
// the 60-second probe interval.
const HistoryCapacity = 60

// History stores per-(targetID, tunnelID) sample ring buffers.
type History struct {
	mu       sync.RWMutex
	buffers  map[string][]Sample
	capacity int
}

// NewHistory builds a History at the default capacity (60).
func NewHistory() *History {
	return &History{
		buffers:  make(map[string][]Sample),
		capacity: HistoryCapacity,
	}
}

func key(targetID, tunnelID string) string {
	return targetID + "|" + tunnelID
}

// Append adds a sample to the (target, tunnel) ring buffer; the oldest entry
// is dropped when capacity is exceeded.
func (h *History) Append(targetID, tunnelID string, s Sample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := key(targetID, tunnelID)
	buf := h.buffers[k]
	buf = append(buf, s)
	if len(buf) > h.capacity {
		buf = buf[len(buf)-h.capacity:]
	}
	h.buffers[k] = buf
}

// Get returns up to limit most-recent samples for (target, tunnel), newest
// last. limit ≤ 0 returns all retained samples.
func (h *History) Get(targetID, tunnelID string, limit int) []Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	buf := h.buffers[key(targetID, tunnelID)]
	if limit > 0 && len(buf) > limit {
		buf = buf[len(buf)-limit:]
	}
	out := make([]Sample, len(buf))
	copy(out, buf)
	return out
}

// Latest returns the most-recent sample for (target, tunnel) or nil when
// no samples are recorded.
func (h *History) Latest(targetID, tunnelID string) *Sample {
	h.mu.RLock()
	defer h.mu.RUnlock()
	buf := h.buffers[key(targetID, tunnelID)]
	if len(buf) == 0 {
		return nil
	}
	last := buf[len(buf)-1]
	return &last
}

// PruneTunnels drops history entries whose tunnelID is NOT in keepIDs.
// Called once per scheduler tick after building the new tunnel set so that
// deleted tunnels do not leak buffers.
func (h *History) PruneTunnels(keepIDs map[string]bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.buffers {
		idx := strings.IndexByte(k, '|')
		if idx < 0 {
			continue
		}
		tunnelID := k[idx+1:]
		if !keepIDs[tunnelID] {
			delete(h.buffers, k)
		}
	}
}

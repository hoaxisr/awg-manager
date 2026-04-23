// Package traffic provides in-memory traffic rate history for tunnels.
// Data is accumulated via Feed() on each poll cycle and served via Get()
// with optional downsampling for longer periods.
package traffic

import (
	"sync"
	"time"
)

// Point is a single rate measurement.
type Point struct {
	Timestamp int64   `json:"t"`
	RxRate    float64 `json:"rx"` // bytes/sec
	TxRate    float64 `json:"tx"` // bytes/sec
}

// tunnelHistory holds raw byte counters and computed rate points for one tunnel.
type tunnelHistory struct {
	lastRx   int64
	lastTx   int64
	lastTime int64 // unix seconds
	points   []Point
}

// History accumulates traffic rate history for all tunnels.
type History struct {
	mu      sync.RWMutex
	tunnels map[string]*tunnelHistory
	maxAge  time.Duration
	stopCh  chan struct{}
}

// New creates a History that retains points for 24 hours.
// Starts a background goroutine that prunes old points every 5 minutes.
func New() *History {
	h := &History{
		tunnels: make(map[string]*tunnelHistory),
		maxAge:  24 * time.Hour,
		stopCh:  make(chan struct{}),
	}
	go h.cleanupLoop()
	return h
}

// Feed records a new byte-counter snapshot for a tunnel.
// The first call per tunnel is a baseline (no point emitted).
// Counter resets (dRx < 0 or dTx < 0) are skipped.
func (h *History) Feed(tunnelID string, rxBytes, txBytes int64) {
	now := time.Now().Unix()

	h.mu.Lock()
	defer h.mu.Unlock()

	th := h.tunnels[tunnelID]
	if th == nil {
		// First call — store baseline, no rate yet.
		h.tunnels[tunnelID] = &tunnelHistory{
			lastRx:   rxBytes,
			lastTx:   txBytes,
			lastTime: now,
		}
		return
	}

	dt := now - th.lastTime
	if dt <= 0 {
		// Same second — update counters, skip point.
		th.lastRx = rxBytes
		th.lastTx = txBytes
		return
	}

	dRx := rxBytes - th.lastRx
	dTx := txBytes - th.lastTx

	// Update baseline.
	th.lastRx = rxBytes
	th.lastTx = txBytes
	th.lastTime = now

	// Counter reset — skip this point.
	if dRx < 0 || dTx < 0 {
		return
	}

	th.points = append(th.points, Point{
		Timestamp: now,
		RxRate:    float64(dRx) / float64(dt),
		TxRate:    float64(dTx) / float64(dt),
	})
}

// Get returns rate points for a tunnel within the given duration,
// downsampled to at most maxPoints using bucket averaging.
//
// Holds the read lock across the (cheap) downsample pass instead of
// copying the full window to a temporary slice first — for a 24 h × 1 Hz
// history that is a ~2 MB allocation avoided per call. The lock window
// is still microseconds; Feed (write lock, ~1/10 s) is not starved.
func (h *History) Get(tunnelID string, since time.Duration, maxPoints int) []Point {
	cutoff := time.Now().Add(-since).Unix()

	h.mu.RLock()
	defer h.mu.RUnlock()

	th := h.tunnels[tunnelID]
	if th == nil {
		return nil
	}

	// Binary search — points are time-sorted by construction.
	pts := th.points
	lo := 0
	hi := len(pts)
	for lo < hi {
		mid := (lo + hi) / 2
		if pts[mid].Timestamp < cutoff {
			lo = mid + 1
		} else {
			hi = mid
		}
	}

	window := pts[lo:]
	if len(window) == 0 {
		return nil
	}

	// No downsampling needed: copy into an independent slice so the
	// caller survives concurrent Feed / prune that mutate th.points.
	if maxPoints <= 0 || len(window) <= maxPoints {
		out := make([]Point, len(window))
		copy(out, window)
		return out
	}

	// Downsample directly into a fresh maxPoints-sized slice; no
	// intermediate copy of the full window.
	return downsample(window, maxPoints)
}

// Clear removes all history for a tunnel (e.g. on delete).
func (h *History) Clear(tunnelID string) {
	h.mu.Lock()
	delete(h.tunnels, tunnelID)
	h.mu.Unlock()
}

// Stop terminates the background cleanup goroutine.
func (h *History) Stop() {
	close(h.stopCh)
}

// cleanupLoop periodically removes points older than maxAge.
func (h *History) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			h.prune()
		case <-h.stopCh:
			return
		}
	}
}

// prune removes expired points from all tunnels.
func (h *History) prune() {
	cutoff := time.Now().Add(-h.maxAge).Unix()

	h.mu.Lock()
	defer h.mu.Unlock()

	for id, th := range h.tunnels {
		// Find first non-expired index.
		i := 0
		for i < len(th.points) && th.points[i].Timestamp < cutoff {
			i++
		}
		if i == len(th.points) {
			// All expired — remove tunnel entry entirely.
			delete(h.tunnels, id)
		} else if i > 0 {
			th.points = th.points[i:]
		}
	}
}

// downsample reduces points to maxPoints using bucket averaging.
func downsample(pts []Point, maxPoints int) []Point {
	n := len(pts)
	bucketSize := float64(n) / float64(maxPoints)
	result := make([]Point, 0, maxPoints)

	for i := 0; i < maxPoints; i++ {
		start := int(float64(i) * bucketSize)
		end := int(float64(i+1) * bucketSize)
		if end > n {
			end = n
		}
		if start >= end {
			continue
		}

		var sumRx, sumTx float64
		var sumT int64
		count := end - start
		for j := start; j < end; j++ {
			sumRx += pts[j].RxRate
			sumTx += pts[j].TxRate
			sumT += pts[j].Timestamp
		}

		result = append(result, Point{
			Timestamp: sumT / int64(count),
			RxRate:    sumRx / float64(count),
			TxRate:    sumTx / float64(count),
		})
	}

	return result
}

// Stats is a set of aggregates over a tunnel's recent rate history,
// used by the detail modal to fill KPI tiles without forcing the
// frontend to re-compute them from the raw point array.
type Stats struct {
	Points    int     `json:"points"`
	PeakRate  float64 `json:"peakRate"`  // max of (RxRate, TxRate), bytes/sec
	AvgRx     float64 `json:"avgRx"`     // bytes/sec
	AvgTx     float64 `json:"avgTx"`     // bytes/sec
	CurrentRx float64 `json:"currentRx"` // bytes/sec, last point
	CurrentTx float64 `json:"currentTx"` // bytes/sec, last point
}

// Stats returns aggregates over the points within the given window.
// An unknown tunnel or empty window returns a zero Stats.
func (h *History) Stats(tunnelID string, since time.Duration) Stats {
	cutoff := time.Now().Add(-since).Unix()

	h.mu.RLock()
	defer h.mu.RUnlock()

	th := h.tunnels[tunnelID]
	if th == nil || len(th.points) == 0 {
		return Stats{}
	}

	pts := th.points
	lo := 0
	hi := len(pts)
	for lo < hi {
		mid := (lo + hi) / 2
		if pts[mid].Timestamp < cutoff {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	window := pts[lo:]
	if len(window) == 0 {
		return Stats{}
	}

	var s Stats
	s.Points = len(window)
	var sumRx, sumTx float64
	for _, p := range window {
		sumRx += p.RxRate
		sumTx += p.TxRate
		if p.RxRate > s.PeakRate {
			s.PeakRate = p.RxRate
		}
		if p.TxRate > s.PeakRate {
			s.PeakRate = p.TxRate
		}
	}
	s.AvgRx = sumRx / float64(len(window))
	s.AvgTx = sumTx / float64(len(window))
	last := window[len(window)-1]
	s.CurrentRx = last.RxRate
	s.CurrentTx = last.TxRate
	return s
}

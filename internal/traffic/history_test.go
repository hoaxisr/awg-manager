package traffic

import (
	"testing"
	"time"
)

func TestFeedAndGet(t *testing.T) {
	h := New()
	defer h.Stop()

	// First call is baseline — no point emitted.
	h.Feed("t1", 1000, 2000)
	pts := h.Get("t1", time.Hour, 0)
	if len(pts) != 0 {
		t.Fatalf("expected 0 points after first feed, got %d", len(pts))
	}

	// Advance time by manipulating lastTime.
	h.mu.Lock()
	h.tunnels["t1"].lastTime -= 5
	h.mu.Unlock()

	h.Feed("t1", 1500, 3000)
	pts = h.Get("t1", time.Hour, 0)
	if len(pts) != 1 {
		t.Fatalf("expected 1 point, got %d", len(pts))
	}
	// 500 bytes / 5 sec = 100 bytes/sec
	if pts[0].RxRate != 100 {
		t.Errorf("expected RxRate=100, got %f", pts[0].RxRate)
	}
	// 1000 bytes / 5 sec = 200 bytes/sec
	if pts[0].TxRate != 200 {
		t.Errorf("expected TxRate=200, got %f", pts[0].TxRate)
	}
}

func TestCounterReset(t *testing.T) {
	h := New()
	defer h.Stop()

	h.Feed("t1", 1000, 2000)

	h.mu.Lock()
	h.tunnels["t1"].lastTime -= 5
	h.mu.Unlock()

	// Counter reset (rxBytes decreased) — should be skipped.
	h.Feed("t1", 500, 3000)
	pts := h.Get("t1", time.Hour, 0)
	if len(pts) != 0 {
		t.Fatalf("expected 0 points after counter reset, got %d", len(pts))
	}
}

func TestClear(t *testing.T) {
	h := New()
	defer h.Stop()

	h.Feed("t1", 1000, 2000)
	h.Clear("t1")

	pts := h.Get("t1", time.Hour, 0)
	if len(pts) != 0 {
		t.Fatalf("expected 0 points after clear, got %d", len(pts))
	}
}

func TestGetUnknownTunnel(t *testing.T) {
	h := New()
	defer h.Stop()

	pts := h.Get("nonexistent", time.Hour, 0)
	if pts != nil {
		t.Fatalf("expected nil for unknown tunnel, got %v", pts)
	}
}

func TestDownsample(t *testing.T) {
	// Create 100 points, downsample to 10.
	pts := make([]Point, 100)
	for i := range pts {
		pts[i] = Point{
			Timestamp: int64(1000 + i),
			RxRate:    float64(i),
			TxRate:    float64(i * 2),
		}
	}

	result := downsample(pts, 10)
	if len(result) != 10 {
		t.Fatalf("expected 10 points, got %d", len(result))
	}

	// First bucket: points 0-9, avg RxRate = 4.5
	if result[0].RxRate != 4.5 {
		t.Errorf("expected first bucket RxRate=4.5, got %f", result[0].RxRate)
	}
}

func TestGetSinceFilter(t *testing.T) {
	h := New()
	defer h.Stop()

	now := time.Now().Unix()

	h.mu.Lock()
	th := &tunnelHistory{
		lastRx:   5000,
		lastTx:   10000,
		lastTime: now,
	}
	// Add points at different times.
	th.points = []Point{
		{Timestamp: now - 7200, RxRate: 10, TxRate: 20}, // 2h ago
		{Timestamp: now - 3601, RxRate: 20, TxRate: 40}, // just over 1h ago
		{Timestamp: now - 1800, RxRate: 30, TxRate: 60}, // 30min ago
		{Timestamp: now - 60, RxRate: 40, TxRate: 80},   // 1min ago
	}
	h.tunnels["t1"] = th
	h.mu.Unlock()

	// Get last 1 hour — should return 2 points (30min ago + 1min ago).
	pts := h.Get("t1", time.Hour, 0)
	if len(pts) != 2 {
		t.Fatalf("expected 2 points for 1h window, got %d", len(pts))
	}

	// Get last 3 hours — should return all 4 (2h, 1h+, 30min, 1min all within 3h).
	pts = h.Get("t1", 3*time.Hour, 0)
	if len(pts) != 4 {
		t.Fatalf("expected 4 points for 3h window, got %d", len(pts))
	}
}

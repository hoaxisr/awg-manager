package accesspolicy

import (
	"sync"
	"time"
)

type dataCache struct {
	mu  sync.RWMutex
	ttl time.Duration

	hotspot   []hotspotHost
	hotspotAt time.Time

	rcLines   []string
	rcLinesAt time.Time
}

func newDataCache(ttl time.Duration) *dataCache {
	return &dataCache{ttl: ttl}
}

func (c *dataCache) GetHotspot() ([]hotspotHost, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.hotspot == nil || time.Since(c.hotspotAt) > c.ttl {
		return nil, false
	}
	cp := make([]hotspotHost, len(c.hotspot))
	copy(cp, c.hotspot)
	return cp, true
}

func (c *dataCache) SetHotspot(hosts []hotspotHost) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hotspot = hosts
	c.hotspotAt = time.Now()
}

func (c *dataCache) GetRCLines() ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.rcLines == nil || time.Since(c.rcLinesAt) > c.ttl {
		return nil, false
	}
	return c.rcLines, true
}

// PeekRCLines returns the last-known running-config lines regardless of TTL.
// Used as a stale-ok fallback when a fresh NDMS fetch fails — we'd rather
// return slightly out-of-date data than empty the UI.
func (c *dataCache) PeekRCLines() ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.rcLines == nil {
		return nil, false
	}
	return c.rcLines, true
}

func (c *dataCache) SetRCLines(lines []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rcLines = lines
	c.rcLinesAt = time.Now()
}

func (c *dataCache) InvalidateHotspot() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hotspot = nil
}

func (c *dataCache) InvalidateRC() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rcLines = nil
}

func (c *dataCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hotspot = nil
	c.rcLines = nil
}

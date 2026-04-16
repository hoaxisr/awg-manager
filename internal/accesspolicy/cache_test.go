package accesspolicy

import (
	"testing"
	"time"
)

func TestHotspotCacheMiss(t *testing.T) {
	c := newDataCache(30 * time.Second)
	hosts, ok := c.GetHotspot()
	if ok || hosts != nil {
		t.Fatal("expected cache miss")
	}
}

func TestHotspotCacheHit(t *testing.T) {
	c := newDataCache(30 * time.Second)
	data := []hotspotHost{{MAC: "AA:BB:CC:DD:EE:FF", IP: "192.168.1.1"}}
	c.SetHotspot(data)
	hosts, ok := c.GetHotspot()
	if !ok || len(hosts) != 1 {
		t.Fatal("expected cache hit with 1 host")
	}
	if hosts[0].MAC != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("expected MAC AA:BB:CC:DD:EE:FF, got %s", hosts[0].MAC)
	}
}

func TestHotspotCacheReturnsCopy(t *testing.T) {
	c := newDataCache(30 * time.Second)
	c.SetHotspot([]hotspotHost{{MAC: "AA:BB:CC:DD:EE:FF"}})
	hosts1, _ := c.GetHotspot()
	hosts2, _ := c.GetHotspot()
	hosts1[0].MAC = "CHANGED"
	if hosts2[0].MAC == "CHANGED" {
		t.Fatal("cache returned reference, not copy")
	}
}

func TestPeekRCLines_StaleOkAfterExpiry(t *testing.T) {
	c := newDataCache(1 * time.Millisecond)
	c.SetRCLines([]string{"ip policy HydraRoute", "    permit global PPPoE0", "!"})

	time.Sleep(5 * time.Millisecond)

	if _, ok := c.GetRCLines(); ok {
		t.Fatal("GetRCLines must reject expired entries")
	}
	stale, ok := c.PeekRCLines()
	if !ok || len(stale) != 3 {
		t.Fatalf("PeekRCLines must return stale data regardless of TTL, got %v %v", stale, ok)
	}
}

func TestPeekRCLines_EmptyWhenNeverSet(t *testing.T) {
	c := newDataCache(30 * time.Second)
	if _, ok := c.PeekRCLines(); ok {
		t.Fatal("PeekRCLines must return false when cache was never set")
	}
}

func TestPeekRCLines_InvalidateClears(t *testing.T) {
	c := newDataCache(30 * time.Second)
	c.SetRCLines([]string{"x"})
	c.InvalidateRC()
	if _, ok := c.PeekRCLines(); ok {
		t.Fatal("PeekRCLines must return false after InvalidateRC")
	}
}

func TestHotspotCacheExpiry(t *testing.T) {
	c := newDataCache(1 * time.Millisecond)
	c.SetHotspot([]hotspotHost{{MAC: "AA:BB:CC:DD:EE:FF"}})
	time.Sleep(5 * time.Millisecond)
	_, ok := c.GetHotspot()
	if ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestRCLinesCacheMiss(t *testing.T) {
	c := newDataCache(30 * time.Second)
	_, ok := c.GetRCLines()
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestRCLinesCacheHit(t *testing.T) {
	c := newDataCache(30 * time.Second)
	lines := []string{"ip policy Policy0", "  description Work"}
	c.SetRCLines(lines)
	got, ok := c.GetRCLines()
	if !ok || len(got) != 2 {
		t.Fatal("expected cache hit with 2 lines")
	}
}

func TestInvalidateHotspot(t *testing.T) {
	c := newDataCache(30 * time.Second)
	c.SetHotspot([]hotspotHost{{MAC: "AA:BB:CC:DD:EE:FF"}})
	c.SetRCLines([]string{"line1"})
	c.InvalidateHotspot()
	_, ok1 := c.GetHotspot()
	_, ok2 := c.GetRCLines()
	if ok1 {
		t.Fatal("hotspot should be invalidated")
	}
	if !ok2 {
		t.Fatal("rcLines should still be cached")
	}
}

func TestInvalidateAll(t *testing.T) {
	c := newDataCache(30 * time.Second)
	c.SetHotspot([]hotspotHost{{MAC: "AA:BB:CC:DD:EE:FF"}})
	c.SetRCLines([]string{"line1"})
	c.InvalidateAll()
	_, ok1 := c.GetHotspot()
	_, ok2 := c.GetRCLines()
	if ok1 || ok2 {
		t.Fatal("all entries should be invalidated")
	}
}

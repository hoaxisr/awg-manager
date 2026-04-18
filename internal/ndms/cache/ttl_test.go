package cache

import (
	"testing"
	"time"
)

func TestTTL_SetGetFresh(t *testing.T) {
	c := NewTTL[string, int](50 * time.Millisecond)
	c.Set("k", 42)
	v, ok := c.Get("k")
	if !ok || v != 42 {
		t.Fatalf("Get: want (42, true), got (%d, %v)", v, ok)
	}
}

func TestTTL_MissOnAbsent(t *testing.T) {
	c := NewTTL[string, int](time.Second)
	_, ok := c.Get("missing")
	if ok {
		t.Fatalf("Get on absent key: want ok=false, got true")
	}
}

func TestTTL_MissAfterExpiry(t *testing.T) {
	c := NewTTL[string, int](20 * time.Millisecond)
	c.Set("k", 1)
	time.Sleep(30 * time.Millisecond)
	if _, ok := c.Get("k"); ok {
		t.Fatalf("Get after TTL: want ok=false, got true")
	}
}

func TestTTL_PeekReturnsStale(t *testing.T) {
	c := NewTTL[string, int](20 * time.Millisecond)
	c.Set("k", 77)
	time.Sleep(30 * time.Millisecond)
	v, ok := c.Peek("k")
	if !ok || v != 77 {
		t.Fatalf("Peek stale: want (77, true), got (%d, %v)", v, ok)
	}
}

func TestTTL_PeekMissOnAbsent(t *testing.T) {
	c := NewTTL[string, int](time.Second)
	if _, ok := c.Peek("nope"); ok {
		t.Fatalf("Peek absent: want ok=false, got true")
	}
}

func TestTTL_Invalidate(t *testing.T) {
	c := NewTTL[string, int](time.Second)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Invalidate("a")

	if _, ok := c.Get("a"); ok {
		t.Errorf("Get after Invalidate(a): want miss, got hit")
	}
	if _, ok := c.Peek("a"); ok {
		t.Errorf("Peek after Invalidate(a): want miss (entry erased), got hit")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("Get(b) untouched: want (2, true), got (%d, %v)", v, ok)
	}
}

func TestTTL_InvalidateAll(t *testing.T) {
	c := NewTTL[string, int](time.Second)
	c.Set("a", 1)
	c.Set("b", 2)
	c.InvalidateAll()
	if c.Len() != 0 {
		t.Errorf("Len after InvalidateAll: want 0, got %d", c.Len())
	}
}

func TestTTL_Len(t *testing.T) {
	c := NewTTL[string, int](time.Second)
	if c.Len() != 0 {
		t.Errorf("empty: want Len=0, got %d", c.Len())
	}
	c.Set("a", 1)
	c.Set("b", 2)
	if c.Len() != 2 {
		t.Errorf("2 entries: want Len=2, got %d", c.Len())
	}
}

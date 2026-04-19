package logbuf

import (
	"testing"
	"time"
)

type entry struct {
	ts  time.Time
	tag string
}

func newBuf(maxEntries int, maxAge time.Duration) *Buffer[entry] {
	return New(Options[entry]{
		MaxAge:          maxAge,
		MaxEntries:      maxEntries,
		CleanupInterval: time.Hour, // never fires in tests
		TimestampOf:     func(e entry) time.Time { return e.ts },
		SetTimestamp:    func(e *entry, t time.Time) { e.ts = t },
	})
}

func TestBuffer_AddStampsZeroTimestamp(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()

	b.Add(entry{tag: "a"})
	got := b.GetAll()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ts.IsZero() {
		t.Errorf("zero timestamp was not stamped on Add")
	}
}

func TestBuffer_AddPreservesNonZeroTimestamp(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()

	want := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	b.Add(entry{ts: want, tag: "a"})
	got := b.GetAll()
	if !got[0].ts.Equal(want) {
		t.Errorf("timestamp = %v, want %v", got[0].ts, want)
	}
}

func TestBuffer_GetAllNewestFirst(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()

	base := time.Now()
	b.Add(entry{ts: base, tag: "a"})
	b.Add(entry{ts: base.Add(time.Second), tag: "b"})
	b.Add(entry{ts: base.Add(2 * time.Second), tag: "c"})

	got := b.GetAll()
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].tag != "c" || got[1].tag != "b" || got[2].tag != "a" {
		t.Errorf("order = %v %v %v, want c b a", got[0].tag, got[1].tag, got[2].tag)
	}
}

func TestBuffer_MaxEntriesDropsOldest(t *testing.T) {
	b := newBuf(3, time.Hour)
	defer b.Stop()

	base := time.Now()
	b.Add(entry{ts: base, tag: "a"})
	b.Add(entry{ts: base.Add(time.Second), tag: "b"})
	b.Add(entry{ts: base.Add(2 * time.Second), tag: "c"})
	b.Add(entry{ts: base.Add(3 * time.Second), tag: "d"})

	got := b.GetAll()
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	// newest first: d, c, b; 'a' evicted
	if got[2].tag != "b" {
		t.Errorf("oldest-retained = %q, want b (a should evict)", got[2].tag)
	}
}

func TestBuffer_CleanupDropsOldEntries(t *testing.T) {
	b := newBuf(10, 500*time.Millisecond)
	defer b.Stop()

	now := time.Now()
	b.Add(entry{ts: now.Add(-1 * time.Hour), tag: "old"})
	b.Add(entry{ts: now, tag: "new"})

	b.cleanup()

	got := b.GetAll()
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (old entry should be dropped)", len(got))
	}
	if got[0].tag != "new" {
		t.Errorf("survivor = %q, want new", got[0].tag)
	}
}

func TestBuffer_Filter(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()

	base := time.Now()
	b.Add(entry{ts: base, tag: "a"})
	b.Add(entry{ts: base.Add(time.Second), tag: "b"})
	b.Add(entry{ts: base.Add(2 * time.Second), tag: "a"})

	got := b.Filter(func(e entry) bool { return e.tag == "a" })
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	// newest first
	if !got[0].ts.After(got[1].ts) {
		t.Errorf("not newest-first: %v vs %v", got[0].ts, got[1].ts)
	}
}

func TestBuffer_FilterPage(t *testing.T) {
	b := newBuf(100, time.Hour)
	defer b.Stop()

	base := time.Now()
	for i := 0; i < 10; i++ {
		b.Add(entry{ts: base.Add(time.Duration(i) * time.Second), tag: "x"})
	}
	// page size 3, offset 4 — want entries 5, 4, 3 counting newest-first
	page, total := b.FilterPage(func(e entry) bool { return e.tag == "x" }, 3, 4)
	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if len(page) != 3 {
		t.Errorf("page len = %d, want 3", len(page))
	}
}

func TestBuffer_FilterPageOutOfRange(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()

	b.Add(entry{ts: time.Now(), tag: "a"})

	page, total := b.FilterPage(func(e entry) bool { return true }, 10, 50)
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if len(page) != 0 {
		t.Errorf("out-of-range offset should yield empty page, got %d", len(page))
	}
}

func TestBuffer_Clear(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()

	b.Add(entry{ts: time.Now(), tag: "a"})
	b.Add(entry{ts: time.Now(), tag: "b"})
	b.Clear()
	if n := b.Len(); n != 0 {
		t.Errorf("len after Clear = %d", n)
	}
}

func TestBuffer_SetMaxAge(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()
	b.SetMaxAge(5)
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.maxAge != 5*time.Hour {
		t.Errorf("maxAge = %v, want 5h", b.maxAge)
	}
}

func TestBuffer_SetMaxAgeNonPositiveDefaults(t *testing.T) {
	b := newBuf(10, time.Hour)
	defer b.Stop()
	b.SetMaxAge(-1)
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.maxAge != 2*time.Hour {
		t.Errorf("negative → default, got %v, want 2h", b.maxAge)
	}
}

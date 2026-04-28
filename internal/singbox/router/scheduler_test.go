package router

import (
	"testing"
	"time"
)

func TestShouldRefreshInterval(t *testing.T) {
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

	if !shouldRefreshInterval(now, now.Add(-25*time.Hour), 24) {
		t.Error("25h since last > 24h interval should refresh")
	}
	if shouldRefreshInterval(now, now.Add(-1*time.Hour), 24) {
		t.Error("1h since last < 24h interval should NOT refresh")
	}
	if !shouldRefreshInterval(now, time.Time{}, 24) {
		t.Error("never refreshed should refresh")
	}
	if shouldRefreshInterval(now, time.Time{}, 0) {
		t.Error("interval 0 should never refresh")
	}
}

func TestShouldRefreshDaily(t *testing.T) {
	now := time.Date(2026, 4, 19, 3, 0, 10, 0, time.UTC)
	if !shouldRefreshDaily(now, time.Time{}, "03:00") {
		t.Error("3:00:10 within window of 3:00 with no prior refresh should fire")
	}
	last := time.Date(2026, 4, 19, 3, 0, 5, 0, time.UTC)
	if shouldRefreshDaily(now, last, "03:00") {
		t.Error("already refreshed today at 3:00:05 should not fire at 3:00:10")
	}
	now2 := time.Date(2026, 4, 19, 4, 0, 0, 0, time.UTC)
	if shouldRefreshDaily(now2, time.Time{}, "03:00") {
		t.Error("4:00 past window should not fire")
	}
	if shouldRefreshDaily(now, time.Time{}, "") {
		t.Error("empty target time should not fire")
	}
}

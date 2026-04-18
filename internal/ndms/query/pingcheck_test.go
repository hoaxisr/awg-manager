package query

import (
	"context"
	"errors"
	"testing"
	"time"
)

const pingCheckPath = "/show/ping-check/"

const samplePingCheckJSON = `{
	"pingcheck": [
		{
			"profile": "p1",
			"host": ["8.8.8.8"],
			"mode": "ip",
			"update-interval": 60,
			"max-fails": 3,
			"min-success": 1,
			"timeout": 1,
			"port": 0,
			"interface": {
				"Wireguard0": {"successcount": 123, "failcount": 4, "status": "alive"}
			}
		}
	]
}`

func TestPingCheckProfileStore_List_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(pingCheckPath, samplePingCheckJSON)
	s := NewPingCheckProfileStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].Profile != "p1" {
		t.Fatalf("result: %#v", got)
	}
	if got[0].UpdateInterval != 60 || got[0].MaxFails != 3 {
		t.Errorf("fields: %#v", got[0])
	}
	_, _ = s.List(context.Background())
	if fg.Calls(pingCheckPath) != 1 {
		t.Errorf("calls: %d", fg.Calls(pingCheckPath))
	}
}

func TestPingCheckStatusStore_List_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(pingCheckPath, samplePingCheckJSON)
	s := NewPingCheckStatusStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len: want 1, got %d", len(got))
	}
	if got[0].Profile != "p1" || got[0].Interface != "Wireguard0" {
		t.Errorf("entry: %#v", got[0])
	}
	if got[0].SuccessCount != 123 || got[0].FailCount != 4 || got[0].Status != "alive" {
		t.Errorf("counters: %#v", got[0])
	}
}

func TestPingCheckProfileStore_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(pingCheckPath, samplePingCheckJSON)
	s := NewPingCheckProfileStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)
	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(pingCheckPath, errors.New("boom"))
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len: %d", len(got))
	}
}

func TestPingCheckStatusStore_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(pingCheckPath, samplePingCheckJSON)
	s := NewPingCheckStatusStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)
	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(pingCheckPath, errors.New("boom"))
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("stale status len: want 1, got %d", len(got))
	}
}

func TestPingCheckProfileStore_InvalidateAll_DoesNotAffectStatus(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(pingCheckPath, samplePingCheckJSON)
	profile := NewPingCheckProfileStore(fg, NopLogger())
	status := NewPingCheckStatusStore(fg, NopLogger())

	_, _ = profile.List(context.Background())
	_, _ = status.List(context.Background())

	profile.InvalidateAll()
	_, _ = profile.List(context.Background())
	_, _ = status.List(context.Background())

	if got := fg.Calls(pingCheckPath); got != 3 {
		t.Errorf("total calls: want 3 (2 profile + 1 status), got %d", got)
	}
}

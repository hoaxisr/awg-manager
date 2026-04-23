package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

const hotspotPath = "/show/ip/hotspot"

const sampleHotspotJSON = `{
	"host": [
		{"ip": "192.168.1.10", "mac": "aa:bb:cc:dd:ee:ff", "name": "laptop", "hostname": "laptop.local", "active": true, "link": "up", "policy": "Policy0"},
		{"ip": "192.168.1.11", "mac": "aa:bb:cc:dd:ee:ff", "name": "laptop", "hostname": "laptop.local", "active": false, "link": "down"},
		{"ip": "192.168.1.12", "mac": "11:22:33:44:55:66", "name": "phone", "active": "yes"}
	]
}`

func TestHotspotStore_List_ParsesAndDedupsByMAC(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(hotspotPath, sampleHotspotJSON)
	s := NewHotspotStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: want 2 (deduped by MAC), got %d", len(got))
	}
	byMAC := map[string]ndms.Device{}
	for _, d := range got {
		byMAC[d.MAC] = d
	}
	lap := byMAC["aa:bb:cc:dd:ee:ff"]
	if !lap.Active || lap.IP != "192.168.1.10" {
		t.Errorf("laptop: want active with IP .10, got %#v", lap)
	}
	phone := byMAC["11:22:33:44:55:66"]
	if !phone.Active {
		t.Errorf("phone active: want true (from string 'yes'), got false")
	}
}

func TestHotspotStore_List_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(hotspotPath, sampleHotspotJSON)
	s := NewHotspotStoreWithTTL(fg, NopLogger(), 20*time.Millisecond)

	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(hotspotPath, errors.New("boom"))

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("stale len: want 2, got %d", len(got))
	}
}

func TestHotspotStore_List_SkipsEntriesWithEmptyMAC(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(hotspotPath, `{
		"host": [
			{"ip": "192.168.1.10", "mac": "aa:bb:cc:dd:ee:ff", "active": true},
			{"ip": "192.168.1.20", "mac": "", "active": true},
			{"ip": "192.168.1.21", "mac": "", "active": false}
		]
	}`)
	s := NewHotspotStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("entries after skipping empty MACs: got %#v", got)
	}
}

func TestHotspotStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(hotspotPath, sampleHotspotJSON)
	s := NewHotspotStore(fg, NopLogger())
	_, _ = s.List(context.Background())
	s.InvalidateAll()
	_, _ = s.List(context.Background())
	if got := fg.Calls(hotspotPath); got != 2 {
		t.Errorf("calls: want 2, got %d", got)
	}
}

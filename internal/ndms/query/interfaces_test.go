package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

const ifaceListPath = "/show/interface/"

// sample /show/interface/ response with two interfaces.
const sampleIfaceList = `{
	"Wireguard0": {
		"id": "Wireguard0",
		"interface-name": "nwg0",
		"type": "Wireguard",
		"description": "my tunnel",
		"state": "up",
		"link": "up",
		"connected": "yes",
		"security-level": "public",
		"address": "10.0.0.2",
		"mask": "255.255.255.255",
		"summary": {"layer": {"ipv4": "running"}}
	},
	"Bridge0": {
		"id": "Bridge0",
		"interface-name": "br0",
		"type": "Bridge",
		"state": "up",
		"link": "up",
		"security-level": "private"
	}
}`

func TestInterfaceStore_GetAll_FetchesAndParses(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)

	s := NewInterfaceStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetAll len: want 2, got %d", len(got))
	}
	if fg.Calls(ifaceListPath) != 1 {
		t.Errorf("Getter calls: want 1, got %d", fg.Calls(ifaceListPath))
	}

	names := make(map[string]bool)
	for _, iface := range got {
		names[iface.ID] = true
	}
	if !names["Wireguard0"] || !names["Bridge0"] {
		t.Errorf("missing interfaces: got %v", names)
	}
}

func TestInterfaceStore_GetAll_CacheHitSkipsFetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	if _, err := s.List(context.Background()); err != nil {
		t.Fatalf("first GetAll: %v", err)
	}
	if _, err := s.List(context.Background()); err != nil {
		t.Fatalf("second GetAll: %v", err)
	}
	if got := fg.Calls(ifaceListPath); got != 1 {
		t.Errorf("Getter calls: want 1 (cache hit), got %d", got)
	}
}

func TestInterfaceStore_GetAll_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStoreWithTTL(fg, NopLogger(), 20*time.Millisecond, 20*time.Millisecond)

	first, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("first GetAll: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	fg.SetError(ifaceListPath, errors.New("ndms timeout"))

	second, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok GetAll: want no error, got %v", err)
	}
	if len(second) != len(first) {
		t.Errorf("stale-ok result: want same length as first, got %d vs %d", len(second), len(first))
	}
}

func TestInterfaceStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.List(context.Background())
	s.InvalidateAll()
	_, _ = s.List(context.Background())

	if got := fg.Calls(ifaceListPath); got != 2 {
		t.Errorf("Getter calls: want 2, got %d", got)
	}
}

func TestInterfaceStore_Get_SingleFetchesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{
		"id": "Wireguard0",
		"interface-name": "nwg0",
		"type": "Wireguard",
		"state": "up",
		"link": "up"
	}`))
	s := NewInterfaceStore(fg, NopLogger())

	got, err := s.Get(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil || got.ID != "Wireguard0" || got.SystemName != "nwg0" {
		t.Fatalf("Get result: unexpected %#v", got)
	}

	_, _ = s.Get(context.Background(), "Wireguard0")
	if got := fg.Calls("/show/interface/Wireguard0"); got != 1 {
		t.Errorf("Getter calls: want 1 (cache hit), got %d", got)
	}
}

func TestInterfaceStore_Invalidate_OnlyAffectsName(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{"id":"Wireguard0"}`))
	fg.SetRaw("/show/interface/Wireguard1", []byte(`{"id":"Wireguard1"}`))
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.Get(context.Background(), "Wireguard0")
	_, _ = s.Get(context.Background(), "Wireguard1")

	s.Invalidate("Wireguard0")
	_, _ = s.Get(context.Background(), "Wireguard0")
	_, _ = s.Get(context.Background(), "Wireguard1")

	if got := fg.Calls("/show/interface/Wireguard0"); got != 2 {
		t.Errorf("Wireguard0 calls: want 2 (refetch after Invalidate), got %d", got)
	}
	if got := fg.Calls("/show/interface/Wireguard1"); got != 1 {
		t.Errorf("Wireguard1 calls: want 1 (untouched), got %d", got)
	}
}

func TestInterfaceStore_Get_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","state":"up"}`))
	s := NewInterfaceStoreWithTTL(fg, NopLogger(), 20*time.Millisecond, 20*time.Millisecond)

	first, err := s.Get(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("prime: %v", err)
	}

	time.Sleep(30 * time.Millisecond)
	fg.SetError("/show/interface/Wireguard0", errors.New("ndms flake"))

	stale, err := s.Get(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("stale-ok: want no error, got %v", err)
	}
	if stale == nil || stale.ID != first.ID {
		t.Errorf("stale value: want %v, got %v", first, stale)
	}
}

func TestInterfaceStore_GetProxy_PresentAndAbsent(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Proxy0", []byte(`{
		"id": "Proxy0",
		"type": "Proxy",
		"description": "sing-box outbound",
		"state": "up",
		"link": "up"
	}`))
	fg.SetRaw("/show/interface/Proxy99", []byte(""))
	s := NewInterfaceStore(fg, NopLogger())

	p, err := s.GetProxy(context.Background(), "Proxy0")
	if err != nil {
		t.Fatalf("GetProxy(Proxy0): %v", err)
	}
	if p == nil || !p.Exists || !p.Up || p.Type != "Proxy" {
		t.Errorf("Proxy0: unexpected %#v", p)
	}

	absent, err := s.GetProxy(context.Background(), "Proxy99")
	if err != nil {
		t.Fatalf("GetProxy(Proxy99): %v", err)
	}
	if absent == nil || absent.Exists {
		t.Errorf("Proxy99: want Exists=false, got %#v", absent)
	}
	if absent.Name != "Proxy99" {
		t.Errorf("Proxy99: want Name=Proxy99, got %q", absent.Name)
	}
}

func TestInterfaceStore_GetDetails_Parses(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{
		"state":"up","link":"up","connected":"yes","uptime":3600,
		"summary":{"layer":{"conf":"running"}}
	}`))
	s := NewInterfaceStore(fg, NopLogger())

	d, err := s.GetDetails(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if d.State != "up" || d.Link != "up" || !d.Connected || d.Uptime != 3600 {
		t.Errorf("parse: %#v", d)
	}
	if d.Intent() != ndms.IntentUp {
		t.Errorf("intent: want Up, got %v", d.Intent())
	}
	if !d.LinkUp() {
		t.Errorf("linkUp: want true")
	}
}

func TestInterfaceStore_GetDetails_Absent(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Nope", []byte(""))
	s := NewInterfaceStore(fg, NopLogger())

	d, err := s.GetDetails(context.Background(), "Nope")
	if err != nil || d != nil {
		t.Errorf("absent: want (nil, nil), got (%#v, %v)", d, err)
	}
}

func TestInterfaceStore_GetDetails_Disabled(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/Wireguard1", []byte(`{
		"state":"down","link":"down","connected":"no","uptime":0,
		"summary":{"layer":{"conf":"disabled"}}
	}`))
	s := NewInterfaceStore(fg, NopLogger())

	d, _ := s.GetDetails(context.Background(), "Wireguard1")
	if d.Intent() != ndms.IntentDown {
		t.Errorf("intent: want Down, got %v", d.Intent())
	}
	if d.LinkUp() {
		t.Errorf("linkUp: want false")
	}
}

func TestInterfaceStore_ResolveSystemName(t *testing.T) {
	fg := newFakeGetter()
	// NDMS returns a bare JSON string.
	fg.SetRaw("/show/interface/system-name?name=Wireguard0", []byte(`"nwg0"`))
	s := NewInterfaceStore(fg, NopLogger())

	got := s.ResolveSystemName(context.Background(), "Wireguard0")
	if got != "nwg0" {
		t.Errorf("resolve: want nwg0, got %q", got)
	}
}

func TestInterfaceStore_ResolveSystemName_Empty(t *testing.T) {
	fg := newFakeGetter()
	s := NewInterfaceStore(fg, NopLogger())

	if got := s.ResolveSystemName(context.Background(), ""); got != "" {
		t.Errorf("empty input: want empty, got %q", got)
	}
}

func TestInterfaceStore_HasIPv6Global_True(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/PPPoE0", []byte(`{
		"ipv6": {"addresses": [{"address":"fe80::1","global":false},{"address":"2a00::1","global":true}]}
	}`))
	s := NewInterfaceStore(fg, NopLogger())
	if !s.HasIPv6Global(context.Background(), "PPPoE0") {
		t.Errorf("want true for interface with global IPv6 address")
	}
}

func TestInterfaceStore_HasIPv6Global_False(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/PPPoE0", []byte(`{
		"ipv6": {"addresses": [{"address":"fe80::1","global":false}]}
	}`))
	s := NewInterfaceStore(fg, NopLogger())
	if s.HasIPv6Global(context.Background(), "PPPoE0") {
		t.Errorf("want false when no global IPv6 address present")
	}
}

func TestInterfaceStore_HasIPv6Global_Empty(t *testing.T) {
	fg := newFakeGetter()
	fg.SetRaw("/show/interface/PPPoE0", []byte(``))
	s := NewInterfaceStore(fg, NopLogger())
	if s.HasIPv6Global(context.Background(), "PPPoE0") {
		t.Errorf("empty body: want false")
	}
}

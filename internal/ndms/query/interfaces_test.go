package query

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
)

const ifaceListPath = "/show/interface/"

// sample /show/interface/ response with two interfaces — one running
// Wireguard with full set of fields and uptime, plus a Bridge.
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
		"uptime": 3600,
		"summary": {"layer": {"ipv4": "running", "conf": "running"}}
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

// === Bootstrap + List ===

func TestInterfaceStore_Bootstrap_PopulatesFromList(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List len: want 2, got %d", len(got))
	}
	if fg.Calls(ifaceListPath) != 1 {
		t.Errorf("bootstrap calls: want 1, got %d", fg.Calls(ifaceListPath))
	}

	// Subsequent reads must NOT re-bootstrap — the map is the cache.
	_, _ = s.List(context.Background())
	_, _ = s.Get(context.Background(), "Wireguard0")
	if got := fg.Calls(ifaceListPath); got != 1 {
		t.Errorf("after reads: want still 1 (cached), got %d", got)
	}
}

func TestInterfaceStore_Bootstrap_Concurrent_OneFetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Get(context.Background(), "Wireguard0")
		}()
	}
	wg.Wait()

	if got := fg.Calls(ifaceListPath); got != 1 {
		t.Errorf("16 concurrent boots: want 1 fetch, got %d", got)
	}
}

// === Get ===

func TestInterfaceStore_Get_Present(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	got, err := s.Get(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatalf("Get: want non-nil")
	}
	if got.ID != "Wireguard0" || got.SystemName != "nwg0" {
		t.Errorf("Get fields: %#v", got)
	}
}

// Critical regression test for the original bug: Get for an absent name
// must NOT issue a HTTP probe to /show/interface/<name>. The list cache
// is authoritative.
func TestInterfaceStore_Get_Absent_NoHTTP(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	got, err := s.Get(context.Background(), "OpkgTun10")
	if err != nil {
		t.Fatalf("Get absent: want no error, got %v", err)
	}
	if got != nil {
		t.Fatalf("Get absent: want nil interface, got %#v", got)
	}
	if got := fg.Calls("/show/interface/OpkgTun10"); got != 0 {
		t.Errorf("Get absent: must not probe single-name endpoint, got %d HTTP calls", got)
	}
}

func TestInterfaceStore_Get_ReturnsCopy(t *testing.T) {
	// Mutating the returned *Interface must not affect the cached entry.
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	first, _ := s.Get(context.Background(), "Wireguard0")
	first.Description = "MUTATED"

	second, _ := s.Get(context.Background(), "Wireguard0")
	if second.Description != "my tunnel" {
		t.Errorf("Get must return a copy, but cached entry was mutated: %q", second.Description)
	}
}

// === GetProxy ===

func TestInterfaceStore_GetProxy_PresentAndAbsent(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"Proxy0": {"id":"Proxy0","type":"Proxy","description":"sing-box outbound","state":"up","link":"up"}
	}`)
	s := NewInterfaceStore(fg, NopLogger())

	p, err := s.GetProxy(context.Background(), "Proxy0")
	if err != nil {
		t.Fatalf("GetProxy(Proxy0): %v", err)
	}
	if p == nil || !p.Exists || !p.Up || p.Type != "Proxy" {
		t.Errorf("Proxy0: %#v", p)
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
	if got := fg.Calls("/show/interface/Proxy99"); got != 0 {
		t.Errorf("GetProxy absent must not HTTP probe, got %d calls", got)
	}
}

// === GetDetails ===

func TestInterfaceStore_GetDetails_FromMap(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	d, err := s.GetDetails(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if d == nil {
		t.Fatalf("GetDetails: want non-nil")
	}
	if d.State != "up" || d.Link != "up" || !d.Connected {
		t.Errorf("GetDetails fields: %#v", d)
	}
	if d.ConfLayer != "running" {
		t.Errorf("ConfLayer: want running, got %q", d.ConfLayer)
	}
	if d.Intent() != ndms.IntentUp {
		t.Errorf("Intent: want Up, got %v", d.Intent())
	}
	if !d.LinkUp() {
		t.Errorf("LinkUp: want true")
	}
}

func TestInterfaceStore_GetDetails_Absent_NoHTTP(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	d, err := s.GetDetails(context.Background(), "OpkgTun10")
	if err != nil || d != nil {
		t.Errorf("absent: want (nil, nil), got (%#v, %v)", d, err)
	}
	if got := fg.Calls("/show/interface/OpkgTun10"); got != 0 {
		t.Errorf("GetDetails absent must not probe, got %d HTTP calls", got)
	}
}

func TestInterfaceStore_GetDetails_Uptime_FromStartedAt(t *testing.T) {
	// Bootstrap: Uptime=3600 → startedAt computed as now - 1h.
	// GetDetails returns d.Uptime ≈ 3600 (within rounding).
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	d, err := s.GetDetails(context.Background(), "Wireguard0")
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if d.Uptime < 3598 || d.Uptime > 3602 {
		t.Errorf("Uptime: want ~3600 (computed from startedAt), got %d", d.Uptime)
	}
}

// === ResolveSystemName ===

func TestInterfaceStore_ResolveSystemName_FromMap(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	got := s.ResolveSystemName(context.Background(), "Wireguard0")
	if got != "nwg0" {
		t.Errorf("ResolveSystemName: want nwg0, got %q", got)
	}
	// system-name endpoint must not be hit — mapping is in the list response.
	if got := fg.Calls("/show/interface/system-name?name=Wireguard0"); got != 0 {
		t.Errorf("system-name endpoint must not be probed, got %d calls", got)
	}
}

func TestInterfaceStore_ResolveSystemName_EmptyInputAndUnknownAbsent(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	if got := s.ResolveSystemName(context.Background(), ""); got != "" {
		t.Errorf("empty input: want empty, got %q", got)
	}
	// Absent name AND no fallback resolver fixture: returns "".
	if got := s.ResolveSystemName(context.Background(), "OpkgTun10"); got != "" {
		t.Errorf("absent without resolver fixture: want empty, got %q", got)
	}
}

// Critical regression test: NDMS /show/interface/ list response does
// NOT always populate `interface-name` (notably for Wireguard system
// tunnels — confirmed on Keenetic OS 5.x). When the cached SystemName
// is empty, ResolveSystemName must fall back to the dedicated
// /show/interface/system-name resolver endpoint and return the kernel
// name. Otherwise system-tunnel monitoring picks the NDMS id
// (e.g. "Wireguard0") as the kernel interface name, and `curl
// --interface Wireguard0` fails with "no such device".
func TestInterfaceStore_ResolveSystemName_FallbackOnEmptyCachedName(t *testing.T) {
	fg := newFakeGetter()
	// Bootstrap snapshot WITHOUT interface-name field (mirrors what
	// NDMS actually returns for system Wireguard tunnels in list view).
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","type":"Wireguard","state":"up"}
	}`)
	// Fallback resolver returns the kernel name.
	fg.SetRaw("/show/interface/system-name?name=Wireguard0", []byte(`"nwg0"`))

	s := NewInterfaceStore(fg, NopLogger())
	got := s.ResolveSystemName(context.Background(), "Wireguard0")
	if got != "nwg0" {
		t.Errorf("fallback resolver: want nwg0, got %q", got)
	}
}

// Fallback result must be memoised on the cached entry — second call
// reads from the map without another HTTP probe.
func TestInterfaceStore_ResolveSystemName_FallbackMemoised(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","type":"Wireguard","state":"up"}
	}`)
	fg.SetRaw("/show/interface/system-name?name=Wireguard0", []byte(`"nwg0"`))
	s := NewInterfaceStore(fg, NopLogger())

	_ = s.ResolveSystemName(context.Background(), "Wireguard0")
	_ = s.ResolveSystemName(context.Background(), "Wireguard0")
	_ = s.ResolveSystemName(context.Background(), "Wireguard0")

	if got := fg.Calls("/show/interface/system-name?name=Wireguard0"); got != 1 {
		t.Errorf("fallback resolver must be probed once and memoised, got %d calls", got)
	}
}

// Critical regression test for the production scenario: NDMS
// /show/interface/ list response populates `interface-name` with the
// NDMS id verbatim instead of the kernel name (verified on Keenetic
// OS 5.x: `interface-name: "Wireguard0"` for Wireguard0 even though
// the kernel device is `nwg0`). Bootstrap caches that garbage value;
// ResolveSystemName must detect it and fall back to the dedicated
// resolver. Without this, `curl --interface Wireguard0` runs against
// a non-existent kernel device and system-tunnel monitoring reports
// connection_failed for a perfectly working tunnel.
func TestInterfaceStore_ResolveSystemName_FallbackWhenSystemNameEqualsID(t *testing.T) {
	fg := newFakeGetter()
	// Bootstrap WITH garbage interface-name (NDMS id echoed back).
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","interface-name":"Wireguard0","type":"Wireguard","state":"up","link":"up"}
	}`)
	// Resolver returns the actual kernel name.
	fg.SetRaw("/show/interface/system-name?name=Wireguard0", []byte(`"nwg0"`))

	s := NewInterfaceStore(fg, NopLogger())
	got := s.ResolveSystemName(context.Background(), "Wireguard0")
	if got != "nwg0" {
		t.Errorf("garbage SystemName==ID: want fallback to nwg0, got %q", got)
	}

	// Subsequent calls memoised — only one resolver probe.
	_ = s.ResolveSystemName(context.Background(), "Wireguard0")
	_ = s.ResolveSystemName(context.Background(), "Wireguard0")
	if calls := fg.Calls("/show/interface/system-name?name=Wireguard0"); calls != 1 {
		t.Errorf("resolver must be probed once and memoised, got %d calls", calls)
	}
}

// Resolver returns object form ({"result":"nwg0"}) on some firmware.
func TestInterfaceStore_ResolveSystemName_FallbackObjectShape(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","type":"Wireguard","state":"up"}
	}`)
	fg.SetRaw("/show/interface/system-name?name=Wireguard0", []byte(`{"result":"nwg0"}`))
	s := NewInterfaceStore(fg, NopLogger())

	if got := s.ResolveSystemName(context.Background(), "Wireguard0"); got != "nwg0" {
		t.Errorf("object-form resolver: want nwg0, got %q", got)
	}
}

// === HasIPv6Global ===

func TestInterfaceStore_HasIPv6Global_TrueForPresent(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, `{
		"PPPoE0": {"id":"PPPoE0","type":"PPPoE","state":"up"}
	}`)
	fg.SetRaw("/show/interface/PPPoE0", []byte(`{
		"ipv6": {"addresses": [{"address":"2a00::1","global":true}]}
	}`))
	s := NewInterfaceStore(fg, NopLogger())

	if !s.HasIPv6Global(context.Background(), "PPPoE0") {
		t.Errorf("want true")
	}
}

func TestInterfaceStore_HasIPv6Global_AbsentNoHTTP(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	if s.HasIPv6Global(context.Background(), "OpkgTun10") {
		t.Errorf("absent: want false")
	}
	if got := fg.Calls("/show/interface/OpkgTun10"); got != 0 {
		t.Errorf("absent must not probe, got %d HTTP calls", got)
	}
}

// === Invalidate / InvalidateAll (proactive refresh) ===

func TestInterfaceStore_InvalidateAll_RebuildsMap(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.List(context.Background())

	// Replace fixture to simulate router-side change.
	fg.SetJSON(ifaceListPath, `{
		"Wireguard0": {"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","description":"renamed"}
	}`)

	s.InvalidateAll()

	got, _ := s.Get(context.Background(), "Wireguard0")
	if got == nil || got.Description != "renamed" {
		t.Errorf("InvalidateAll: want refreshed map, got %#v", got)
	}
	if got, _ := s.Get(context.Background(), "Bridge0"); got != nil {
		t.Errorf("InvalidateAll: stale Bridge0 must be removed, got %#v", got)
	}
	if calls := fg.Calls(ifaceListPath); calls != 2 {
		t.Errorf("list calls: want 2 (boot + InvalidateAll), got %d", calls)
	}
}

func TestInterfaceStore_Invalidate_RefreshesSingleEntry(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	fg.SetRaw("/show/interface/Wireguard0", []byte(`{
		"id":"Wireguard0","interface-name":"nwg0","type":"Wireguard","description":"updated"
	}`))
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.Get(context.Background(), "Wireguard0")
	s.Invalidate("Wireguard0")

	got, _ := s.Get(context.Background(), "Wireguard0")
	if got == nil || got.Description != "updated" {
		t.Errorf("Invalidate(name): want refreshed, got %#v", got)
	}
	if calls := fg.Calls("/show/interface/Wireguard0"); calls != 1 {
		t.Errorf("single-item calls: want 1 (the Invalidate refresh), got %d", calls)
	}
	if calls := fg.Calls(ifaceListPath); calls != 1 {
		t.Errorf("list calls: want 1 (boot only), got %d", calls)
	}
}

func TestInterfaceStore_Invalidate_RemovesFromMapOnEmptyResponse(t *testing.T) {
	// Edge case: a different actor deleted the interface concurrently
	// with our Invalidate refresh. NDMS responds with empty body → we
	// drop the entry from the map.
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	fg.SetRaw("/show/interface/Wireguard0", []byte(""))
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.Get(context.Background(), "Wireguard0")
	s.Invalidate("Wireguard0")

	if got, _ := s.Get(context.Background(), "Wireguard0"); got != nil {
		t.Errorf("Invalidate after empty body: want removed, got %#v", got)
	}
}

func TestInterfaceStore_Invalidate_OnHTTPErrorLeavesMapUntouched(t *testing.T) {
	// HTTP error during refresh should not corrupt the map. The next
	// hook event or future Invalidate will reconcile.
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	fg.SetError("/show/interface/Wireguard0", errors.New("ndms flake"))
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.Get(context.Background(), "Wireguard0")
	s.Invalidate("Wireguard0")

	got, _ := s.Get(context.Background(), "Wireguard0")
	if got == nil || got.Description != "my tunnel" {
		t.Errorf("HTTP-error Invalidate must leave map: got %#v", got)
	}
}

// === Hook-side write API: OnCreated / OnDestroyed / OnLayerChanged / OnIPChanged ===

func TestInterfaceStore_OnCreated_FetchesOnce(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	fg.SetRaw("/show/interface/Wireguard5", []byte(`{
		"id":"Wireguard5","interface-name":"nwg5","type":"Wireguard","state":"up","link":"up"
	}`))
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.List(context.Background())
	s.OnCreated(context.Background(), "Wireguard5")

	got, _ := s.Get(context.Background(), "Wireguard5")
	if got == nil {
		t.Fatalf("OnCreated: must insert into map")
	}
	if got.SystemName != "nwg5" {
		t.Errorf("OnCreated: want systemName nwg5, got %q", got.SystemName)
	}
	// Only ONE HTTP per OnCreated, exactly to the new id.
	if calls := fg.Calls("/show/interface/Wireguard5"); calls != 1 {
		t.Errorf("OnCreated calls: want 1, got %d", calls)
	}
	// Must NOT probe unrelated names.
	if calls := fg.Calls("/show/interface/Wireguard0"); calls != 0 {
		t.Errorf("OnCreated must not probe other interfaces, got %d calls to Wireguard0", calls)
	}
}

func TestInterfaceStore_OnCreated_OnFetchFailure_InsertsStub(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	fg.SetError("/show/interface/Wireguard5", errors.New("ndms timeout"))
	s := NewInterfaceStore(fg, NopLogger())

	s.OnCreated(context.Background(), "Wireguard5")
	got, _ := s.Get(context.Background(), "Wireguard5")
	if got == nil || got.ID != "Wireguard5" {
		t.Errorf("OnCreated stub: want minimal entry with ID, got %#v", got)
	}
}

func TestInterfaceStore_OnDestroyed_NoHTTP(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.List(context.Background())
	bootCalls := fg.Calls(ifaceListPath)

	s.OnDestroyed("Wireguard0")

	if got, _ := s.Get(context.Background(), "Wireguard0"); got != nil {
		t.Errorf("OnDestroyed: want removed, got %#v", got)
	}
	// No HTTP — pure delete.
	if calls := fg.Calls(ifaceListPath); calls != bootCalls {
		t.Errorf("OnDestroyed must not HTTP, list calls before=%d after=%d", bootCalls, calls)
	}
	if calls := fg.Calls("/show/interface/Wireguard0"); calls != 0 {
		t.Errorf("OnDestroyed must not probe, got %d calls", calls)
	}
}

func TestInterfaceStore_OnLayerChanged_PatchesInPlace(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())

	_, _ = s.List(context.Background())
	bootCalls := fg.Calls(ifaceListPath)

	// conf layer state passes through (NDMS state machine and our
	// ConfLayer field share semantics: "running" / "disabled" / "pending").
	s.OnLayerChanged("Wireguard0", "conf", "disabled")
	d, _ := s.GetDetails(context.Background(), "Wireguard0")
	if d == nil || d.ConfLayer != "disabled" {
		t.Errorf("conf disabled: want ConfLayer=disabled, got %#v", d)
	}

	// No HTTP for in-place patches.
	if calls := fg.Calls(ifaceListPath); calls != bootCalls {
		t.Errorf("OnLayerChanged must not HTTP, list calls before=%d after=%d", bootCalls, calls)
	}
}

// Critical regression test for the link-layer mapping bug:
// /show/interface/{name} JSON returns Link="up" / "down". NDMS hooks
// for the link layer send level=running / pending / disabled. The
// store must MAP between these — otherwise details.LinkUp() (which
// checks Link=="up") returns false even when the link is up, blocking
// the state matrix from resolving the tunnel as Running.
func TestInterfaceStore_OnLayerChanged_LinkLayerMappedToUpDown(t *testing.T) {
	fg := newFakeGetter()
	// Bootstrap with link="up" (kernel-style). After hook events the
	// field must end up as "up" or "down", never "running"/"pending".
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())
	_, _ = s.List(context.Background())

	// Initially "up" from bootstrap.
	d, _ := s.GetDetails(context.Background(), "Wireguard0")
	if !d.LinkUp() {
		t.Fatalf("after bootstrap with link=up: want LinkUp()=true, got Link=%q", d.Link)
	}

	// link=pending → mapped to Link="down" → LinkUp()=false
	s.OnLayerChanged("Wireguard0", "link", "pending")
	d, _ = s.GetDetails(context.Background(), "Wireguard0")
	if d.LinkUp() {
		t.Errorf("after link=pending: want LinkUp()=false, got Link=%q (must NOT be raw 'pending')", d.Link)
	}
	if d.Link != "down" {
		t.Errorf("after link=pending: want Link='down', got %q", d.Link)
	}

	// link=running → mapped to Link="up" → LinkUp()=true
	s.OnLayerChanged("Wireguard0", "link", "running")
	d, _ = s.GetDetails(context.Background(), "Wireguard0")
	if !d.LinkUp() {
		t.Errorf("after link=running: want LinkUp()=true, got Link=%q", d.Link)
	}
	if d.Link != "up" {
		t.Errorf("after link=running: want Link='up', got %q", d.Link)
	}

	// link=disabled → mapped to Link="down"
	s.OnLayerChanged("Wireguard0", "link", "disabled")
	d, _ = s.GetDetails(context.Background(), "Wireguard0")
	if d.LinkUp() {
		t.Errorf("after link=disabled: want LinkUp()=false, got Link=%q", d.Link)
	}
}

func TestInterfaceStore_OnLayerChanged_CtrlSetsStateAndUptime(t *testing.T) {
	fg := newFakeGetter()
	// Bootstrap snapshot: interface present but state=down (just created,
	// not running yet).
	fg.SetJSON(ifaceListPath, `{
		"OpkgTun10": {"id":"OpkgTun10","interface-name":"opkgtun10","type":"OpkgTun","state":"down","summary":{"layer":{"conf":"disabled"}}}
	}`)
	s := NewInterfaceStore(fg, NopLogger())
	_, _ = s.List(context.Background())

	// ctrl=running → State="up" + uptime clock starts.
	s.OnLayerChanged("OpkgTun10", "ctrl", "running")
	time.Sleep(10 * time.Millisecond)
	d, _ := s.GetDetails(context.Background(), "OpkgTun10")
	if d == nil || d.Uptime < 0 || d.Uptime > 1 {
		t.Errorf("after ctrl=running: want small Uptime, got %#v", d)
	}
	got, _ := s.Get(context.Background(), "OpkgTun10")
	if got == nil || got.State != "up" {
		t.Errorf("after ctrl=running: want State='up', got %#v", got)
	}

	// ctrl=disabled → State="down" + uptime=0.
	s.OnLayerChanged("OpkgTun10", "ctrl", "disabled")
	d, _ = s.GetDetails(context.Background(), "OpkgTun10")
	if d == nil || d.Uptime != 0 {
		t.Errorf("after ctrl=disabled: want Uptime=0, got %#v", d)
	}
	got, _ = s.Get(context.Background(), "OpkgTun10")
	if got == nil || got.State != "down" {
		t.Errorf("after ctrl=disabled: want State='down', got %#v", got)
	}
}

func TestInterfaceStore_OnLayerChanged_UnknownInterfaceIgnored(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())
	_, _ = s.List(context.Background())

	// Event for an interface we never saw — must not panic, must not
	// HTTP-probe to find it.
	s.OnLayerChanged("Phantom", "conf", "running")
	if got, _ := s.Get(context.Background(), "Phantom"); got != nil {
		t.Errorf("Phantom: want nil (event ignored), got %#v", got)
	}
	if calls := fg.Calls("/show/interface/Phantom"); calls != 0 {
		t.Errorf("must not probe unknown interface, got %d calls", calls)
	}
}

func TestInterfaceStore_OnIPChanged_PatchesAddressOnly(t *testing.T) {
	// OnIPChanged must NOT touch State or Connected — those are owned
	// by the ctrl layer. The hook payload's `up`/`connected` flags are
	// not always populated by the NDMS event-script forwarder, and a
	// spurious "down"/"no" overwrite of an actually-running interface
	// blocks the state matrix.
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())
	_, _ = s.List(context.Background())
	bootCalls := fg.Calls(ifaceListPath)

	// Bootstrap baseline.
	pre, _ := s.Get(context.Background(), "Wireguard0")
	preState := pre.State
	preConnected := pre.Connected

	// Hook with up=false, connected=false (default zero values when
	// the forwarder doesn't fill them) MUST NOT corrupt State/Connected.
	s.OnIPChanged("Wireguard0", "192.168.5.5", false, false)
	got, _ := s.Get(context.Background(), "Wireguard0")
	if got == nil {
		t.Fatalf("expected Wireguard0 still present")
	}
	if got.Address != "192.168.5.5" {
		t.Errorf("Address: want 192.168.5.5, got %q", got.Address)
	}
	if got.State != preState {
		t.Errorf("State must be preserved: was %q, became %q", preState, got.State)
	}
	if got.Connected != preConnected {
		t.Errorf("Connected must be preserved: was %q, became %q", preConnected, got.Connected)
	}

	// No HTTP.
	if calls := fg.Calls(ifaceListPath); calls != bootCalls {
		t.Errorf("OnIPChanged must not HTTP, list calls before=%d after=%d", bootCalls, calls)
	}
}

// === Concurrency ===

func TestInterfaceStore_Concurrent_ReadWrite(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(ifaceListPath, sampleIfaceList)
	s := NewInterfaceStore(fg, NopLogger())
	_, _ = s.List(context.Background())

	// Many goroutines reading and patching simultaneously. Run under
	// `go test -race` to catch any unprotected map access.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = s.Get(context.Background(), "Wireguard0")
					_, _ = s.GetDetails(context.Background(), "Wireguard0")
				}
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					s.OnLayerChanged("Wireguard0", "link", "up")
					s.OnIPChanged("Wireguard0", "10.0.0.2", true, true)
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

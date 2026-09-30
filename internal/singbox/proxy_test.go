package singbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

func TestProxyManagerNextFreeIndexReservesAcrossConcurrentCallers(t *testing.T) {
	pm := &ProxyManager{
		pending: make(map[int]time.Time),
		listInterfaces: func(context.Context) ([]ndms.Interface, error) {
			return nil, nil
		},
	}
	const callers = 16
	results := make(chan int, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			index, err := pm.NextFreeIndex(context.Background(), nil)
			if err != nil {
				errs <- err
				return
			}
			results <- index
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := make(map[int]bool)
	for index := range results {
		if seen[index] {
			t.Fatalf("duplicate pending Proxy%d reservation", index)
		}
		seen[index] = true
	}
	if len(seen) != callers {
		t.Fatalf("reserved %d unique indices, want %d", len(seen), callers)
	}
	pm.ReleaseProxyIndex(0)
	index, err := pm.NextFreeIndex(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if index != 0 {
		t.Fatalf("released reservation yielded Proxy%d, want Proxy0", index)
	}
}

func TestProxyManagerNextFreeIndexSkipsPersistedReservations(t *testing.T) {
	pm := &ProxyManager{
		pending: make(map[int]time.Time),
		listInterfaces: func(context.Context) ([]ndms.Interface, error) {
			return nil, nil
		},
	}
	pm.SetReservedIndices(func() map[int]bool {
		return map[int]bool{0: true, 2: true}
	})
	index, err := pm.NextFreeIndex(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("NextFreeIndex = %d, want 1 (Proxy0 and Proxy2 persisted)", index)
	}
}

func TestProxyManagerOwnedMutationPreservesForeignProxy(t *testing.T) {
	description := "user-created"
	createCalls := 0
	deleteCalls := 0
	pm := &ProxyManager{
		pending:      make(map[int]time.Time),
		hasComponent: func() bool { return true },
		getProxy: func(_ context.Context, _ string) (*ndms.ProxyInfo, error) {
			return &ndms.ProxyInfo{Exists: true, Description: description}, nil
		},
		createProxy: func(_ context.Context, _, nextDescription, _ string, _ int, _ bool) error {
			createCalls++
			description = nextDescription
			return nil
		},
		downProxy: func(context.Context, string) error { return nil },
		deleteProxy: func(context.Context, string) error {
			deleteCalls++
			return nil
		},
	}

	owner := "awg-manager:singbox:subscription:sub-id"
	owned, err := pm.EnsureProxyIfOwned(context.Background(), 4, 11004, owner, "old label")
	if err != nil {
		t.Fatal(err)
	}
	if owned || createCalls != 0 || description != "user-created" {
		t.Fatalf("foreign ensure mutated Proxy4: owned=%v createCalls=%d description=%q", owned, createCalls, description)
	}
	removed, err := pm.RemoveProxyIfOwnedBy(context.Background(), 4, owner, "old label")
	if err != nil {
		t.Fatal(err)
	}
	if removed || deleteCalls != 0 {
		t.Fatalf("foreign remove mutated Proxy4: removed=%v deleteCalls=%d", removed, deleteCalls)
	}

	// A legacy label is accepted once and rewritten to the stable owner token.
	description = "old label"
	owned, err = pm.EnsureProxyIfOwned(context.Background(), 4, 11004, owner, "old label")
	if err != nil {
		t.Fatal(err)
	}
	if !owned || createCalls != 1 || description != owner {
		t.Fatalf("legacy migration: owned=%v createCalls=%d description=%q", owned, createCalls, description)
	}
	removed, err = pm.RemoveProxyIfOwnedBy(context.Background(), 4, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !removed || deleteCalls != 1 {
		t.Fatalf("owned remove: removed=%v deleteCalls=%d", removed, deleteCalls)
	}
}

// proxyIsOurs decides whether an NDMS ProxyN belongs to awg-manager's sing-box
// management, so disable/orphan-cleanup removes it. Subscription composites are
// the regression case: their proxy carries the subscription *label* as the
// interface description (not a tunnel tag), so the tag/slot heuristics miss it —
// they must be recognised via their explicitly-tracked proxy index.
func TestProxyIsOurs(t *testing.T) {
	tunnelTags := map[string]bool{"vless-1": true}
	ourPortSlots := map[int]bool{3: true}
	subProxyIdx := map[int]bool{7: true}

	cases := []struct {
		name string
		idx  int
		desc string
		want bool
	}{
		{"tunnel matched by description tag", 0, "vless-1", true},
		{"tunnel matched by port slot (empty desc)", 3, "", true},
		{"subscription composite (label description)", 7, "Моя подписка", true},
		{"foreign proxy with description", 9, "some-other-app", false},
		{"foreign proxy empty desc unknown slot", 5, "", false},
	}
	for _, c := range cases {
		got := proxyIsOurs(c.idx, c.desc, tunnelTags, ourPortSlots, subProxyIdx)
		if got != c.want {
			t.Errorf("%s: proxyIsOurs(%d, %q) = %v, want %v", c.name, c.idx, c.desc, got, c.want)
		}
	}
}

// nativeProxyKernelNames must return kernel names of ONLY the proxies that are
// not ours — the KeenOS-native SOCKS proxies a user can bind a router outbound
// to (#323). Ours (by tunnel tag or by port slot) are excluded.
func TestNativeProxyKernelNames(t *testing.T) {
	proxies := []proxyEntry{
		{idx: 0, desc: "My-Socks5", kernel: "t2s0"},      // native — keep
		{idx: 1, desc: "vless-1", kernel: "t2s1"},        // ours by tunnel tag — drop
		{idx: 2, desc: "", kernel: "t2s2"},               // ours by port slot — drop
		{idx: 3, desc: "another-native", kernel: "t2s3"}, // native — keep
	}
	got := nativeProxyKernelNames(proxies,
		map[string]bool{"vless-1": true}, // tunnelTags
		map[int]bool{2: true},            // ourPortSlots
		map[int]bool{},                   // subProxyIdx
	)
	if len(got) != 2 {
		t.Fatalf("want 2 native, got %d: %v", len(got), got)
	}
	want := map[string]bool{"t2s0": true, "t2s3": true}
	for _, k := range got {
		if !want[k] {
			t.Errorf("unexpected native proxy %q in %v", k, got)
		}
	}
}

func TestProxyManagerInspectProxy_ResolvesEmptySystemName(t *testing.T) {
	pm := &ProxyManager{
		pending: make(map[int]time.Time),
		getInterface: func(_ context.Context, name string) (*ndms.Interface, error) {
			if name == "Proxy1" {
				return &ndms.Interface{
					ID:          "Proxy1",
					State:       "up",
					Description: "test-proxy",
					SystemName:  "", // empty in bulk/show query on Keenetic OS
				}, nil
			}
			return nil, nil
		},
	}
	pm.SetSystemNameResolver(func(_ context.Context, name string) string {
		if name == "Proxy1" {
			return "t2s1"
		}
		return ""
	})

	obs, err := pm.InspectProxy(context.Background(), 1)
	if err != nil {
		t.Fatalf("InspectProxy failed: %v", err)
	}
	if !obs.Exists {
		t.Fatalf("expected Exists=true")
	}
	if obs.SystemName != "t2s1" {
		t.Fatalf("expected SystemName='t2s1', got %q", obs.SystemName)
	}
}

func TestProxyManagerInspectProxy_PreservesExistingSystemName(t *testing.T) {
	pm := &ProxyManager{
		pending: make(map[int]time.Time),
		getInterface: func(_ context.Context, name string) (*ndms.Interface, error) {
			return &ndms.Interface{
				ID:         "Proxy2",
				State:      "up",
				SystemName: "t2s2",
			}, nil
		},
	}
	pm.SetSystemNameResolver(func(_ context.Context, name string) string {
		t.Fatal("resolver should not be called when SystemName is already populated")
		return "unexpected"
	})

	obs, err := pm.InspectProxy(context.Background(), 2)
	if err != nil {
		t.Fatalf("InspectProxy failed: %v", err)
	}
	if obs.SystemName != "t2s2" {
		t.Fatalf("expected SystemName='t2s2', got %q", obs.SystemName)
	}
}

func TestProxyManagerInspectProxy_GetProxyFallbackResolvesSystemName(t *testing.T) {
	pm := &ProxyManager{
		pending: make(map[int]time.Time),
		getProxy: func(_ context.Context, name string) (*ndms.ProxyInfo, error) {
			return &ndms.ProxyInfo{
				Name:   "Proxy3",
				Exists: true,
				Up:     true,
			}, nil
		},
	}
	pm.SetSystemNameResolver(func(_ context.Context, name string) string {
		if name == "Proxy3" {
			return "t2s3"
		}
		return ""
	})

	obs, err := pm.InspectProxy(context.Background(), 3)
	if err != nil {
		t.Fatalf("InspectProxy failed: %v", err)
	}
	if obs.SystemName != "t2s3" {
		t.Fatalf("expected SystemName='t2s3', got %q", obs.SystemName)
	}
}

func TestProxyManagerListProxyObservations_ResolvesEmptySystemName(t *testing.T) {
	pm := &ProxyManager{
		pending: make(map[int]time.Time),
		listInterfaces: func(context.Context) ([]ndms.Interface, error) {
			return []ndms.Interface{
				{ID: "Proxy1", State: "up", SystemName: ""},
				{ID: "Proxy2", State: "up", SystemName: "t2s2"},
				{ID: "Wireguard0", State: "up", SystemName: "nwg0"},
			}, nil
		},
	}
	pm.SetSystemNameResolver(func(_ context.Context, name string) string {
		if name == "Proxy1" {
			return "t2s1"
		}
		return ""
	})

	obs, err := pm.ListProxyObservations(context.Background())
	if err != nil {
		t.Fatalf("ListProxyObservations failed: %v", err)
	}
	if len(obs) != 2 {
		t.Fatalf("expected 2 proxy observations, got %d", len(obs))
	}
	if obs[0].Name != "Proxy1" || obs[0].SystemName != "t2s1" {
		t.Errorf("Proxy1 mismatch: name=%q sys=%q, want sys='t2s1'", obs[0].Name, obs[0].SystemName)
	}
	if obs[1].Name != "Proxy2" || obs[1].SystemName != "t2s2" {
		t.Errorf("Proxy2 mismatch: name=%q sys=%q, want sys='t2s2'", obs[1].Name, obs[1].SystemName)
	}
}

// F546: снятие отсутствующего ProxyN не шлёт в NDMS ничего — `interface
// ProxyN down` по отсутствующему имени создаёт запись (стенд 5.01.C.6), а
// точечный show interface пишет E «unable to find» в журнал NDMS. commands
// nil: любая команда уронила бы тест.
func TestProxyManager_RemoveProxy_AbsentSendsNothing(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{"Proxy0":{"id":"Proxy0","type":"Proxy"}}`)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	pm := NewProxyManager(q, nil)

	if err := pm.RemoveProxy(context.Background(), 5); err != nil {
		t.Fatalf("RemoveProxy(5): %v", err)
	}
	if n := fg.PostInterfaceCalls("Proxy5"); n != 0 {
		t.Fatalf("show interface Proxy5 ушёл %d раз", n)
	}
}

// NextFreeIndex обязан считать занятыми ЧУЖИЕ ProxyN (пользовательский Proxy0 —
// не наш) и переданные reserved; иначе перезапись пользовательского прокси.
func TestProxyManager_NextFreeIndex_SkipsForeignProxyAndReserved(t *testing.T) {
	fg := query.NewFakeGetter()
	fg.SetJSON("/show/interface/", `{
		"Proxy0":{"id":"Proxy0","type":"Proxy","description":"user's own"},
		"Proxy1":{"id":"Proxy1","type":"Proxy","description":"awgm"},
		"Bridge0":{"id":"Bridge0","type":"Bridge"},
		"ProxyX":{"id":"ProxyX","type":"Proxy"}
	}`)
	q := query.NewQueries(query.Deps{Getter: fg, Logger: query.NopLogger()})
	pm := NewProxyManager(q, nil)

	idx, err := pm.NextFreeIndex(context.Background(), map[int]bool{2: true})
	if err != nil {
		t.Fatal(err)
	}
	if idx != 3 {
		t.Fatalf("NextFreeIndex = %d, want 3 (0,1 заняты NDMS, 2 — reserved)", idx)
	}
}

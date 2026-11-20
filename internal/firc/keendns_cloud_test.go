package firc

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func hasIpsetMember(st *fakeIpsetState, ip string) bool {
	if norm, ok := ValidateCloudIPv4Entry(ip); ok {
		ip = norm
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	m := st.sets[FextCloudIpsetName]
	return m != nil && m[ip]
}

func snapshotIpsetMembers(st *fakeIpsetState) map[string]bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]bool)
	for k, v := range st.sets[FextCloudIpsetName] {
		out[k] = v
	}
	return out
}

func hasIpsetSet(st *fakeIpsetState, name string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.sets[name]
	return ok
}

func resetKeenCloudTestHooks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		setExtCloudNow(nil)
		SetExtDNSLookupIPv4(nil)
		SetExtIpsetRunner(nil)
		SetExtIptablesRunner(nil)
	})
}

func TestKeenDNSCloud_NegativeBackoff_NormalCadence_And_NormalTTLExpiry(t *testing.T) {
	resetKeenCloudTestHooks(t)

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	InvalidateOutputRoutingCache()

	var clockMu sync.Mutex
	curNow := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	getNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	}
	advance := func(d time.Duration) time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		curNow = curNow.Add(d)
		return curNow
	}
	setExtCloudNow(getNow)

	b0 := keenDNSCloudBaseDomains[0]
	var dnsMu sync.Mutex
	b0Empty := false
	counts := make(map[string]int)
	resetCounts := func() {
		dnsMu.Lock()
		counts = make(map[string]int)
		dnsMu.Unlock()
	}
	countFor := func(host string) int {
		dnsMu.Lock()
		defer dnsMu.Unlock()
		return counts[host]
	}
	totalLookups := func() int {
		dnsMu.Lock()
		defer dnsMu.Unlock()
		tot := 0
		for _, n := range counts {
			tot += n
		}
		return tot
	}

	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		dnsMu.Lock()
		counts[host]++
		emptyB0 := b0Empty
		dnsMu.Unlock()

		switch host {
		case "relay1.keenetic.io":
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		case b0:
			if emptyB0 {
				return nil, nil // NODATA negative answer
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
		default:
			return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
		}
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	var groupsMu sync.Mutex
	var groups []Group
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()
	t.Cleanup(func() {
		if ch := op.cancelExtWatchdog(); ch != nil {
			<-ch
		}
	})
	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"nwg0"}},
	}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	op.SetKeenCloudProvider(func(context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{
			Enabled:      true,
			RelayIPs:     []string{"192.0.2.10"},
			RelayDomains: []string{"relay1.keenetic.io"},
		}, nil
	})

	ctx := context.Background()
	t0 := getNow()
	if err := op.SyncKeenDNSCloud(ctx, true); err != nil {
		t.Fatalf("initial SyncKeenDNSCloud: %v", err)
	}
	if lastErr, _ := op.keenDNSCloudStatus(); lastErr != "" {
		t.Fatalf("expected empty lastErr after initial sync, got %q", lastErr)
	}
	for _, wantIP := range []string{"192.0.2.10/32", "203.0.113.10/32", "203.0.113.20/32"} {
		if !hasIpsetMember(ipsetSt, wantIP) {
			t.Fatalf("expected %s in FEXT_CLOUD after initial sync (members=%v)", wantIP, snapshotIpsetMembers(ipsetSt))
		}
	}

	// Normal 60s cadence must hold when only optional base domains gave NXDOMAIN (no 15s retry spam).
	resetCounts()
	advance(15 * time.Second)
	if err := op.SyncKeenDNSCloud(ctx, false); err != nil {
		t.Fatalf("SyncKeenDNSCloud at +15s: %v", err)
	}
	if got := totalLookups(); got != 0 {
		t.Fatalf("expected 0 lookups at +15s (normal 60s cadence), got %d", got)
	}

	// Advance past 60s cadence (still < 2m negative backoff for NXDOMAIN domains) with b0 turning NODATA.
	dnsMu.Lock()
	b0Empty = true
	dnsMu.Unlock()
	resetCounts()
	advance(46 * time.Second) // t0 + 61s
	if err := op.SyncKeenDNSCloud(ctx, false); err != nil {
		t.Fatalf("SyncKeenDNSCloud at +61s: %v", err)
	}
	if countFor("relay1.keenetic.io") != 1 || countFor(b0) != 1 {
		t.Fatalf("expected relay1=1 and b0=1 at +61s, got relay1=%d b0=%d", countFor("relay1.keenetic.io"), countFor(b0))
	}
	for _, other := range keenDNSCloudBaseDomains[1:] {
		if countFor(other) != 0 {
			t.Fatalf("expected optional domain %s skipped under 2m backoff at +61s, got %d lookups", other, countFor(other))
		}
	}
	if lastErr, _ := op.keenDNSCloudStatus(); lastErr != "" {
		t.Fatalf("expected empty lastErr despite optional NODATA, got %q", lastErr)
	}

	// Verify exponential backoff on b0: 2m -> 5m -> 15m -> 30m.
	// Step 0 armed at t1 with 2m delay (until t1+2m).
	resetCounts()
	advance(1 * time.Minute)
	_ = op.SyncKeenDNSCloud(ctx, true)
	if countFor(b0) != 0 {
		t.Fatalf("expected b0 skipped before 2m backoff elapsed")
	}
	resetCounts()
	advance(1*time.Minute + time.Second) // t1 + 2m1s
	_ = op.SyncKeenDNSCloud(ctx, true)
	if countFor(b0) != 1 {
		t.Fatalf("expected b0 queried once after 2m backoff, got %d", countFor(b0))
	}

	// Step 1 armed at t2 with 5m delay.
	resetCounts()
	advance(3 * time.Minute)
	_ = op.SyncKeenDNSCloud(ctx, true)
	if countFor(b0) != 0 {
		t.Fatalf("expected b0 skipped before 5m backoff elapsed")
	}
	resetCounts()
	advance(2*time.Minute + time.Second) // t2 + 5m1s
	_ = op.SyncKeenDNSCloud(ctx, true)
	if countFor(b0) != 1 {
		t.Fatalf("expected b0 queried once after 5m backoff, got %d", countFor(b0))
	}

	// Step 2 armed at t3 with 15m delay.
	resetCounts()
	advance(10 * time.Minute)
	_ = op.SyncKeenDNSCloud(ctx, true)
	if countFor(b0) != 0 {
		t.Fatalf("expected b0 skipped before 15m backoff elapsed")
	}
	resetCounts()
	t4 := advance(5*time.Minute + time.Second) // t3 + 15m1s (elapsed from t0 is ~23m4s < 30m)
	_ = op.SyncKeenDNSCloud(ctx, true)
	if countFor(b0) != 1 {
		t.Fatalf("expected b0 queried once after 15m backoff, got %d", countFor(b0))
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expected 203.0.113.20/32 retained within normal 30m TTL")
	}

	// Verify Step 3 armed at t4 with 30m delay does not freeze normal 30m cache TTL expiry for base:b0.
	// Jump to t0 + 31m (< t4+30m, so b0 stays backed off, yet > 30m since b0's last positive update at t0).
	deltaTo31m := t0.Add(31 * time.Minute).Sub(t4)
	advance(deltaTo31m)
	resetCounts()
	if err := op.SyncKeenDNSCloud(ctx, true); err != nil {
		t.Fatalf("SyncKeenDNSCloud at t0+31m: %v", err)
	}
	if countFor(b0) != 0 {
		t.Fatalf("expected b0 still skipped under 30m backoff at t0+31m, got %d", countFor(b0))
	}
	if hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expected stale optional IP 203.0.113.20/32 evicted after 30m normal TTL")
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.10/32") || !hasIpsetMember(ipsetSt, "192.0.2.10/32") {
		t.Fatalf("expected healthy relay and RCI IPs preserved after b0 expiry")
	}
	_ = keenCloudNegBackoffSteps
}

func TestKeenDNSCloud_BoundedLKG_PerSourceIsolation_And_2hEviction(t *testing.T) {
	resetKeenCloudTestHooks(t)

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	InvalidateOutputRoutingCache()

	var clockMu sync.Mutex
	t0 := time.Date(2026, 11, 14, 14, 0, 0, 0, time.UTC)
	curNow := t0
	getNow := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	}
	setNow := func(ts time.Time) {
		clockMu.Lock()
		curNow = ts
		clockMu.Unlock()
	}
	setExtCloudNow(getNow)

	b0 := keenDNSCloudBaseDomains[0]
	var snapMu sync.Mutex
	rciSnapshot := KeenCloudRelaySnapshot{
		Enabled:      true,
		RelayIPs:     []string{"192.0.2.10"},
		RelayDomains: []string{"relay1.keenetic.io", "relay-retired.keenetic.io"},
	}
	relay1TransientFail := false

	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		snapMu.Lock()
		failRelay1 := relay1TransientFail
		snapMu.Unlock()

		switch host {
		case "relay1.keenetic.io":
			if failRelay1 {
				return nil, &net.DNSError{IsTemporary: true, Name: host, Err: "i/o timeout"}
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		case "relay-retired.keenetic.io":
			return []netip.Addr{netip.MustParseAddr("203.0.113.30")}, nil
		case b0:
			return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
		default:
			return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
		}
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	var groupsMu sync.Mutex
	var groups []Group
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()
	t.Cleanup(func() {
		if ch := op.cancelExtWatchdog(); ch != nil {
			<-ch
		}
	})
	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"nwg0"}},
	}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	op.SetKeenCloudProvider(func(context.Context) (KeenCloudRelaySnapshot, error) {
		snapMu.Lock()
		defer snapMu.Unlock()
		return KeenCloudRelaySnapshot{
			Enabled:      rciSnapshot.Enabled,
			RelayIPs:     append([]string(nil), rciSnapshot.RelayIPs...),
			RelayDomains: append([]string(nil), rciSnapshot.RelayDomains...),
		}, nil
	})

	ctx := context.Background()
	if err := op.SyncKeenDNSCloud(ctx, true); err != nil {
		t.Fatalf("initial SyncKeenDNSCloud: %v", err)
	}
	for _, ip := range []string{"192.0.2.10/32", "203.0.113.10/32", "203.0.113.20/32", "203.0.113.30/32"} {
		if !hasIpsetMember(ipsetSt, ip) {
			t.Fatalf("expected %s in FEXT_CLOUD initially (members=%v)", ip, snapshotIpsetMembers(ipsetSt))
		}
	}

	// At t0+10m: RCI cleanly replaces RelayIPs & drops relay-retired, while relay1 fails transiently.
	setNow(t0.Add(10 * time.Minute))
	snapMu.Lock()
	rciSnapshot = KeenCloudRelaySnapshot{
		Enabled:      true,
		RelayIPs:     []string{"192.0.2.11"},
		RelayDomains: []string{"relay1.keenetic.io"},
	}
	relay1TransientFail = true
	snapMu.Unlock()

	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected partial transient error from failing relay1 lookup")
	}
	// During overlap < 30m: new RCI IP 192.0.2.11 is present while retired 192.0.2.10 and relay-retired remain within normal 30m grace TTL.
	if !hasIpsetMember(ipsetSt, "192.0.2.11/32") || !hasIpsetMember(ipsetSt, "192.0.2.10/32") {
		t.Fatalf("expected both new 192.0.2.11 and grace 192.0.2.10 in FEXT_CLOUD at +10m (members=%v)", snapshotIpsetMembers(ipsetSt))
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.10/32") || !hasIpsetMember(ipsetSt, "203.0.113.30/32") {
		t.Fatalf("expected relay1 and relay-retired retained at +10m")
	}

	// At t0+31m: (> 30m normal TTL, < 2h LKG cap):
	// Healthy RCI retired IP (192.0.2.10) & retired source relay-retired (203.0.113.30) MUST expire
	// (per-source isolation: relay1 failure must NOT freeze healthy sources!), while failing relay1 retains bounded LKG < 2h.
	setNow(t0.Add(31 * time.Minute))
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected partial error while relay1 still fails")
	}
	if hasIpsetMember(ipsetSt, "192.0.2.10/32") {
		t.Fatalf("expected retired RCI IP 192.0.2.10/32 evicted past 30m TTL despite relay1 transient failure")
	}
	if hasIpsetMember(ipsetSt, "203.0.113.30/32") {
		t.Fatalf("expected retired source 203.0.113.30/32 evicted past 30m TTL despite relay1 transient failure")
	}
	if !hasIpsetMember(ipsetSt, "192.0.2.11/32") || !hasIpsetMember(ipsetSt, "203.0.113.10/32") {
		t.Fatalf("expected active RCI IP 192.0.2.11/32 and failing relay1 LKG 203.0.113.10/32 retained before 2h cap")
	}

	// At t0+2h1m: (> keenCloudMaxLKGTTL): failing relay1 LKG MUST evict while healthy sources remain.
	setNow(t0.Add(keenCloudMaxLKGTTL + time.Minute))
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected partial error while relay1 fails at +2h1m")
	}
	if hasIpsetMember(ipsetSt, "203.0.113.10/32") {
		t.Fatalf("expected transiently failing relay1 LKG evicted past 2h cap")
	}
	if !hasIpsetMember(ipsetSt, "192.0.2.11/32") || !hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expected healthy RCI & base IPs preserved past 2h")
	}
	ipsetSt.mu.Lock()
	emptySwap := ipsetSt.sawEmptySwap
	ipsetSt.mu.Unlock()
	if emptySwap {
		t.Fatalf("unexpected empty ipset swap during bounded LKG lifecycle")
	}
}

func TestKeenDNSCloud_ConcurrentDisableDuringDNS_AbortsCommit(t *testing.T) {
	resetKeenCloudTestHooks(t)

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	InvalidateOutputRoutingCache()

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	var groupsMu sync.Mutex
	var groups []Group
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()

	op.SetKeenCloudProvider(func(context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{
			Enabled:      true,
			RelayIPs:     []string{"192.0.2.10"},
			RelayDomains: []string{"relay1.keenetic.io"},
		}, nil
	})
	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "relay1.keenetic.io" {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		}
		return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
	})

	ctx := context.Background()
	if _, err := op.ApplyExtConfig(ctx, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"nwg0"}},
	}); err != nil {
		t.Fatalf("enable ApplyExtConfig: %v", err)
	}
	if !hasIpsetSet(ipsetSt, FextCloudIpsetName) {
		t.Fatalf("expected FEXT_CLOUD set after initial enable")
	}

	dnsEntered := make(chan struct{})
	releaseDNS := make(chan struct{})
	var enterOnce sync.Once
	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "relay1.keenetic.io" {
			enterOnce.Do(func() { close(dnsEntered) })
			<-releaseDNS
			return []netip.Addr{netip.MustParseAddr("203.0.113.99")}, nil
		}
		return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
	})

	syncDone := make(chan error, 1)
	go func() {
		syncDone <- op.SyncKeenDNSCloud(context.Background(), true)
	}()

	select {
	case <-dnsEntered:
	case <-time.After(5 * time.Second):
		close(releaseDNS)
		t.Fatal("timed out waiting for in-flight DNS lookup")
	}

	if _, err := op.ApplyExtConfig(ctx, ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: false},
	}); err != nil {
		close(releaseDNS)
		t.Fatalf("disable ApplyExtConfig: %v", err)
	}

	close(releaseDNS)
	select {
	case <-syncDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SyncKeenDNSCloud completion after disable")
	}

	if hasIpsetSet(ipsetSt, FextCloudIpsetName) {
		t.Fatalf("in-flight DNS commit recreated %s after KeenDNS disable", FextCloudIpsetName)
	}
	op.cloudStateMu.RLock()
	ready := op.cloudSetReady
	op.cloudStateMu.RUnlock()
	if ready {
		t.Fatalf("expected cloudSetReady=false after concurrent disable")
	}
}

func TestKeenDNSCloudSync_TotalDNSOutageExpiresEachSourceIndependently(t *testing.T) {
	resetKeenCloudTestHooks(t)

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	InvalidateOutputRoutingCache()

	var clockMu sync.Mutex
	t0 := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	curNow := t0
	setExtCloudNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	})
	setNow := func(ts time.Time) {
		clockMu.Lock()
		curNow = ts
		clockMu.Unlock()
	}

	bA := keenDNSCloudBaseDomains[0]
	bB := keenDNSCloudBaseDomains[1]

	var dnsMu sync.Mutex
	failA := false
	failAll := false
	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		dnsMu.Lock()
		curFailA := failA
		curFailAll := failAll
		dnsMu.Unlock()

		if curFailAll {
			return nil, &net.DNSError{IsTemporary: true, Name: host, Err: "i/o timeout"}
		}
		switch host {
		case bA:
			if curFailA {
				return nil, &net.DNSError{IsTemporary: true, Name: host, Err: "i/o timeout"}
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		case bB:
			return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
		default:
			return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
		}
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	var groupsMu sync.Mutex
	var groups []Group
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()
	t.Cleanup(func() {
		if ch := op.cancelExtWatchdog(); ch != nil {
			<-ch
		}
	})
	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"nwg0"}},
	}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	op.SetKeenCloudProvider(func(context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{Enabled: false}, nil
	})

	ctx := context.Background()
	if err := op.SyncKeenDNSCloud(ctx, true); err != nil {
		t.Fatalf("t0 sync: %v", err)
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.10/32") || !hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expected both source IPs in FEXT_CLOUD at t0 (members=%v)", snapshotIpsetMembers(ipsetSt))
	}

	// t0+1h: A fails transiently, B refreshes to t0+3h.
	setNow(t0.Add(1 * time.Hour))
	dnsMu.Lock()
	failA = true
	dnsMu.Unlock()
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected partial error when A fails at t0+1h")
	}

	// t0+2h1m: total DNS outage -> A (>2h since t0) must be evicted while B (LKG until t0+3h) stays.
	setNow(t0.Add(keenCloudMaxLKGTTL + time.Minute))
	dnsMu.Lock()
	failAll = true
	dnsMu.Unlock()
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected error on total DNS outage at t0+2h1m")
	}
	if hasIpsetMember(ipsetSt, "203.0.113.10/32") {
		t.Fatalf("expired source A (203.0.113.10/32) remained in FEXT_CLOUD during total DNS outage")
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("fresh source B (203.0.113.20/32) was dropped before its 2h LKG deadline")
	}
	if !hasIpsetMember(ipsetSt, keenDNSCloudSubnets[0]) {
		t.Fatalf("static subnet %s missing after total outage", keenDNSCloudSubnets[0])
	}

	// t0+3h2m: total DNS outage continues -> B (>2h since t0+1h) must also expire, leaving static CIDRs only.
	setNow(t0.Add(1*time.Hour + keenCloudMaxLKGTTL + 2*time.Minute))
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected error on total DNS outage at t0+3h2m")
	}
	if hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expired source B (203.0.113.20/32) remained past its own 2h LKG deadline")
	}
	if !hasIpsetMember(ipsetSt, keenDNSCloudSubnets[0]) {
		t.Fatalf("static subnet %s missing after all dynamic LKG expired", keenDNSCloudSubnets[0])
	}
}

func TestKeenDNSCloudSync_RCIOutageExpiresEachSourceIndependently(t *testing.T) {
	resetKeenCloudTestHooks(t)

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	InvalidateOutputRoutingCache()

	var clockMu sync.Mutex
	t0 := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	curNow := t0
	setExtCloudNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	})
	setNow := func(ts time.Time) {
		clockMu.Lock()
		curNow = ts
		clockMu.Unlock()
	}

	b0 := keenDNSCloudBaseDomains[0]
	var stateMu sync.Mutex
	failRelay1 := false
	var rciErr error

	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		stateMu.Lock()
		fRelay1 := failRelay1
		stateMu.Unlock()

		switch host {
		case "relay1.keenetic.io":
			if fRelay1 {
				return nil, &net.DNSError{IsTemporary: true, Name: host, Err: "i/o timeout"}
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.31")}, nil
		case b0:
			return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
		default:
			return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
		}
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	var groupsMu sync.Mutex
	var groups []Group
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()
	t.Cleanup(func() {
		if ch := op.cancelExtWatchdog(); ch != nil {
			<-ch
		}
	})
	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"nwg0"}},
	}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	op.SetKeenCloudProvider(func(context.Context) (KeenCloudRelaySnapshot, error) {
		stateMu.Lock()
		err := rciErr
		stateMu.Unlock()
		if err != nil {
			return KeenCloudRelaySnapshot{}, err
		}
		return KeenCloudRelaySnapshot{
			Enabled:      true,
			RelayIPs:     []string{"192.0.2.10"},
			RelayDomains: []string{"relay1.keenetic.io"},
		}, nil
	})

	ctx := context.Background()
	if err := op.SyncKeenDNSCloud(ctx, true); err != nil {
		t.Fatalf("t0 sync: %v", err)
	}

	// t0+1h: RCI ("rci" = 192.0.2.10) & base b0 refresh to t0+3h, while relay1 fails transiently (LKG stays t0+2h).
	setNow(t0.Add(1 * time.Hour))
	stateMu.Lock()
	failRelay1 = true
	stateMu.Unlock()
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected partial error when relay1 fails at t0+1h")
	}

	// t0+2h1m: RCI provider now fails with 503 too. Even though "rci" still has valid LKG (t0+3h),
	// relay1 (LKG expired at t0+2h) MUST be evicted while "rci" and healthy base b0 stay.
	setNow(t0.Add(keenCloudMaxLKGTTL + time.Minute))
	stateMu.Lock()
	rciErr = errors.New("rci 503 unavailable")
	stateMu.Unlock()
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected error during RCI outage at t0+2h1m")
	}
	if hasIpsetMember(ipsetSt, "203.0.113.31/32") {
		t.Fatalf("expired relay1 LKG (203.0.113.31/32) remained during RCI outage")
	}
	if !hasIpsetMember(ipsetSt, "192.0.2.10/32") || !hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expected valid rci (192.0.2.10/32) and b0 (203.0.113.20/32) preserved at t0+2h1m")
	}

	// t0+3h2m: RCI still fails -> "rci" (>2h since t0+1h) must also evict while base b0 continues refreshing!
	setNow(t0.Add(1*time.Hour + keenCloudMaxLKGTTL + 2*time.Minute))
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected error during RCI outage at t0+3h2m")
	}
	if hasIpsetMember(ipsetSt, "192.0.2.10/32") {
		t.Fatalf("expired rci LKG (192.0.2.10/32) remained past 2h cap")
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("healthy base b0 (203.0.113.20/32) dropped during RCI outage")
	}
}

func TestKeenDNSCloudSync_NegativeDNSExpiresNormalTTLAlongsideTransientOutage(t *testing.T) {
	resetKeenCloudTestHooks(t)

	iptSt := newFakeIptablesState()
	ipsetSt := newFakeIpsetState(iptSt)
	SetExtIptablesRunner(iptSt.runner)
	SetExtIpsetRunner(ipsetSt.runner)
	InvalidateOutputRoutingCache()

	var clockMu sync.Mutex
	t0 := time.Date(2026, 11, 14, 17, 0, 0, 0, time.UTC)
	curNow := t0
	setExtCloudNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	})
	setNow := func(ts time.Time) {
		clockMu.Lock()
		curNow = ts
		clockMu.Unlock()
	}

	b0 := keenDNSCloudBaseDomains[0]
	b1 := keenDNSCloudBaseDomains[1]

	var dnsMu sync.Mutex
	outage := false
	SetExtDNSLookupIPv4(func(_ context.Context, host string) ([]netip.Addr, error) {
		dnsMu.Lock()
		inOutage := outage
		dnsMu.Unlock()

		switch host {
		case b0:
			if inOutage {
				return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.20")}, nil
		case b1:
			if inOutage {
				return nil, &net.DNSError{IsTemporary: true, Name: host, Err: "i/o timeout"}
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.21")}, nil
		default:
			if inOutage {
				return nil, &net.DNSError{IsTemporary: true, Name: host, Err: "i/o timeout"}
			}
			return nil, &net.DNSError{IsNotFound: true, Name: host, Err: "no such host"}
		}
	})

	op := NewOperator(Options{ConfigDir: t.TempDir(), InboundPort: 1099})
	var groupsMu sync.Mutex
	var groups []Group
	srv := startMockRunningFircServer(t, op, &groupsMu, &groups, iptSt)
	defer srv.Close()
	t.Cleanup(func() {
		if ch := op.cancelExtWatchdog(); ch != nil {
			<-ch
		}
	})
	if err := op.SaveExtConfig(ExtConfig{
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"nwg0"}},
	}); err != nil {
		t.Fatalf("SaveExtConfig: %v", err)
	}
	op.SetKeenCloudProvider(func(context.Context) (KeenCloudRelaySnapshot, error) {
		return KeenCloudRelaySnapshot{Enabled: false}, nil
	})

	ctx := context.Background()
	if err := op.SyncKeenDNSCloud(ctx, true); err != nil {
		t.Fatalf("t0 sync: %v", err)
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.20/32") || !hasIpsetMember(ipsetSt, "203.0.113.21/32") {
		t.Fatalf("expected both b0 and b1 IPs initially")
	}

	// At t0+31m (> 30m normal TTL, < 2h LKG): b0 gets NXDOMAIN while b1 + other domains get transient timeout.
	// b0 must expire on normal 30m TTL, while b1 stays under bounded 2h LKG.
	setNow(t0.Add(keenCloudStaleTTL + time.Minute))
	dnsMu.Lock()
	outage = true
	dnsMu.Unlock()
	if err := op.SyncKeenDNSCloud(ctx, true); err == nil {
		t.Fatalf("expected partial error due to transient DNS timeouts on b1/others")
	}
	if hasIpsetMember(ipsetSt, "203.0.113.20/32") {
		t.Fatalf("expected NXDOMAIN domain b0 (203.0.113.20/32) expired on normal 30m TTL despite concurrent transient timeouts")
	}
	if !hasIpsetMember(ipsetSt, "203.0.113.21/32") {
		t.Fatalf("expected transient-failing domain b1 (203.0.113.21/32) retained within 2h LKG")
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/mihomonative"
	"github.com/hoaxisr/awg-manager/internal/singbox"
	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

type flakyBridgeProxyRegistrar struct {
	failOnce map[int]bool
	active   map[int]bool
	attempts map[int]int
	removed  []int
}

func (f *flakyBridgeProxyRegistrar) NextFreeIndex(context.Context, map[int]bool) (int, error) {
	return 0, nil
}
func (f *flakyBridgeProxyRegistrar) ReleaseProxyIndex(int) {}
func (f *flakyBridgeProxyRegistrar) LookupProxy(context.Context, int) (string, bool, error) {
	return "", false, nil
}
func (f *flakyBridgeProxyRegistrar) EnsureProxy(context.Context, int, int, string) error {
	return nil
}
func (f *flakyBridgeProxyRegistrar) EnsureProxyIfOwned(context.Context, int, int, string, ...string) (bool, error) {
	return true, nil
}
func (f *flakyBridgeProxyRegistrar) RemoveProxyIfOwned(_ context.Context, index int, _ string, _ ...string) (bool, error) {
	f.attempts[index]++
	if f.failOnce[index] {
		delete(f.failOnce, index)
		return false, errors.New("transient NDMS remove failure")
	}
	if !f.active[index] {
		return true, nil
	}
	delete(f.active, index)
	f.removed = append(f.removed, index)
	return true, nil
}
func (f *flakyBridgeProxyRegistrar) InspectProxy(_ context.Context, index int) (singbox.ProxyObservation, error) {
	if f.active != nil && f.active[index] {
		return singbox.ProxyObservation{
			Name:   fmt.Sprintf("Proxy%d", index),
			Exists: true,
			Up:     true,
		}, nil
	}
	return singbox.ProxyObservation{
		Name:   fmt.Sprintf("Proxy%d", index),
		Exists: false,
	}, nil
}
func (f *flakyBridgeProxyRegistrar) ListProxyObservations(_ context.Context) ([]singbox.ProxyObservation, error) {
	return nil, nil
}

func TestWaitForMihomoBridgeListenersRequiresBoundListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := waitForMihomoBridgeListeners(context.Background(), []mihomonative.BridgeListener{{Port: port}}); err != nil {
		t.Fatalf("ready listener rejected: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForMihomoBridgeListeners(cancelled, []mihomonative.BridgeListener{{Port: port + 1}}); err == nil {
		t.Fatal("unbound listener unexpectedly passed readiness")
	}
}

func TestMihomoBridgeRuntimeActivateNDMSOffDoesNotWaitOrPublish(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#Native", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12099, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	}); err != nil {
		t.Fatal(err)
	}
	gate := &gatedBridgeRegistrar{}
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return false })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	started := time.Now()
	if err := runtime.activate(context.Background()); err != nil {
		t.Fatalf("activate() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("NDMS-off activation waited for absent listener: %s", elapsed)
	}
	if gate.isReady() {
		t.Fatal("NDMS-off activation opened advertisement gate")
	}
}

func TestMihomoBridgeRuntimeStaleExitDoesNotCloseNewGenerationGate(t *testing.T) {
	gate := &gatedBridgeRegistrar{}
	gate.setReady(true)
	runtime := &mihomoBridgeRuntime{gate: gate}

	if err := runtime.deactivateIf(context.Background(), func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if !gate.isReady() {
		t.Fatal("stale exit withdrew exports from a newer generation")
	}
	if err := runtime.deactivateIf(context.Background(), func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if gate.isReady() {
		t.Fatal("current generation exit did not close advertisement gate")
	}
}

func TestMihomoBridgeRuntimeUnexpectedExitWithdrawsAllAndRetriesFailures(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	firstURI := "vless://11111111-1111-1111-1111-111111111111@one.example:443#One"
	secondURI := "vless://22222222-2222-2222-2222-222222222222@two.example:443#Two"
	first, err := store.CreateProxy(firstURI, mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateProxy(secondURI, mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", first[0].ID, mihomonative.ProxyBridge{ListenPort: 12003, ProxyIndex: 3, ProxyInterface: "Proxy3", KernelInterface: "t2s3"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", second[0].ID, mihomonative.ProxyBridge{ListenPort: 12004, ProxyIndex: 4, ProxyInterface: "Proxy4", KernelInterface: "t2s4"}); err != nil {
		t.Fatal(err)
	}
	// Fail-safe withdrawal must also clean a persisted disabled bridge: its
	// earlier disable transaction may have failed after flipping the store.
	if _, err := store.UpdateProxy(second[0].ID, mihomonative.UpdateProxyInput{
		URI: secondURI, EnginePreference: mihomonative.EngineMihomo,
		RoutingEngine: mihomonative.EngineMihomo, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	base := &flakyBridgeProxyRegistrar{
		failOnce: map[int]bool{3: true},
		active:   map[int]bool{3: true, 4: true},
		attempts: make(map[int]int),
	}
	gate := &gatedBridgeRegistrar{base: base}
	gate.setReady(true)
	runtime := &mihomoBridgeRuntime{store: store, gate: gate}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := runtime.deactivateAfterUnexpectedExit(ctx, func() bool { return true }); err != nil {
		t.Fatalf("deactivateAfterUnexpectedExit() error = %v", err)
	}
	if gate.isReady() {
		t.Fatal("unexpected exit left publication gate open")
	}
	if len(base.active) != 0 {
		t.Fatalf("active ProxyN after retry = %v", base.active)
	}
	if base.attempts[3] < 2 {
		t.Fatalf("failed first Proxy3 was not retried: attempts=%v", base.attempts)
	}
	if len(base.removed) != 2 || base.removed[0] != 4 || base.removed[1] != 3 {
		t.Fatalf("successful removal order = %v, want [4 3] (second removed before first retry)", base.removed)
	}
}

type fakeNDMSRegistrarWithOwnership struct {
	descriptions       map[int]string
	ports              map[int]int
	occupied           map[int]bool
	ensured            map[int]string
	overrideUp         map[int]bool
	overrideSystemName map[int]string
	publishCalls       int
	withdrawCalls      int
	events             []string
	failPublishIndex   int
	failWithdrawIndex  int
	onBeforeWithdraw   func(index int)
	onBeforePublish    func(index int)
	mu                 sync.Mutex
}

func newFakeNDMSRegistrarWithOwnership() *fakeNDMSRegistrarWithOwnership {
	return &fakeNDMSRegistrarWithOwnership{
		descriptions:       make(map[int]string),
		ports:              make(map[int]int),
		occupied:           make(map[int]bool),
		ensured:            make(map[int]string),
		overrideUp:         make(map[int]bool),
		overrideSystemName: make(map[int]string),
	}
}

func (f *fakeNDMSRegistrarWithOwnership) NextFreeIndex(context.Context, map[int]bool) (int, error) {
	return 99, nil
}
func (f *fakeNDMSRegistrarWithOwnership) ReleaseProxyIndex(int) {}
func (f *fakeNDMSRegistrarWithOwnership) LookupProxy(_ context.Context, index int) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.occupied[index] {
		return "", false, nil
	}
	return f.descriptions[index], true, nil
}

func (f *fakeNDMSRegistrarWithOwnership) InspectProxy(_ context.Context, index int) (singbox.ProxyObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.occupied[index] {
		return singbox.ProxyObservation{
			Name:   fmt.Sprintf("Proxy%d", index),
			Exists: false,
		}, nil
	}
	up := true
	if f.overrideUp != nil {
		if val, ok := f.overrideUp[index]; ok {
			up = val
		}
	}
	state := "up"
	link := "up"
	if !up {
		state = "down"
		link = "down"
	}
	sysName := fmt.Sprintf("t2s%d", index)
	if f.overrideSystemName != nil {
		if val, ok := f.overrideSystemName[index]; ok {
			sysName = val
		}
	}
	port := 0
	if f.ports != nil {
		port = f.ports[index]
	}
	return singbox.ProxyObservation{
		Name:        fmt.Sprintf("Proxy%d", index),
		Exists:      true,
		Description: f.descriptions[index],
		State:       state,
		Link:        link,
		Up:          up,
		SystemName:  sysName,
		ListenPort:  port,
	}, nil
}

func (f *fakeNDMSRegistrarWithOwnership) ListProxyObservations(_ context.Context) ([]singbox.ProxyObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []singbox.ProxyObservation
	for idx, occ := range f.occupied {
		if !occ {
			continue
		}
		port := 0
		if f.ports != nil {
			port = f.ports[idx]
		}
		out = append(out, singbox.ProxyObservation{
			Name:        fmt.Sprintf("Proxy%d", idx),
			Exists:      true,
			Description: f.descriptions[idx],
			State:       "up",
			Link:        "up",
			Up:          true,
			SystemName:  fmt.Sprintf("t2s%d", idx),
			ListenPort:  port,
		})
	}
	return out, nil
}
func (f *fakeNDMSRegistrarWithOwnership) ensureProxyLocked(_ context.Context, index, port int, desc string) error {
	f.publishCalls++
	f.occupied[index] = true
	f.descriptions[index] = desc
	if f.ports != nil {
		f.ports[index] = port
	}
	if f.ensured != nil {
		f.ensured[index] = desc
	}
	return nil
}
func (f *fakeNDMSRegistrarWithOwnership) EnsureProxy(ctx context.Context, index, port int, desc string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensureProxyLocked(ctx, index, port, desc)
}
func (f *fakeNDMSRegistrarWithOwnership) EnsureProxyIfOwned(ctx context.Context, index, port int, owner string, legacy ...string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPublishIndex > 0 && f.failPublishIndex == index {
		if f.onBeforePublish != nil {
			f.onBeforePublish(index)
		}
		return false, errors.New("simulated NDMS publish failure")
	}
	if f.onBeforePublish != nil {
		f.onBeforePublish(index)
	}
	if f.occupied[index] {
		cur := f.descriptions[index]
		if !fakeOwnerMatches(cur, owner, legacy) {
			return false, nil
		}
	}
	f.events = append(f.events, fmt.Sprintf("publish:%d:%s:%d", index, owner, port))
	return true, f.ensureProxyLocked(ctx, index, port, owner)
}
func (f *fakeNDMSRegistrarWithOwnership) RemoveProxyIfOwned(_ context.Context, index int, owner string, legacy ...string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.withdrawCalls++
	if f.onBeforeWithdraw != nil {
		f.onBeforeWithdraw(index)
	}
	if f.failWithdrawIndex > 0 && f.failWithdrawIndex == index {
		return false, errors.New("simulated NDMS withdraw failure")
	}
	if !f.occupied[index] {
		return true, nil
	}
	cur := f.descriptions[index]
	if !fakeOwnerMatches(cur, owner, legacy) {
		return false, nil
	}
	f.events = append(f.events, fmt.Sprintf("withdraw:%d:%s", index, owner))
	delete(f.occupied, index)
	delete(f.descriptions, index)
	if f.ports != nil {
		delete(f.ports, index)
	}
	return true, nil
}

func fakeOwnerMatches(description, owner string, legacyOwners []string) bool {
	if description == owner {
		return true
	}
	descToken := singbox.ExtractProxyOwnerToken(description)
	ownerToken := singbox.ExtractProxyOwnerToken(owner)
	if descToken != "" && ownerToken != "" && descToken == ownerToken {
		return true
	}
	if ownerToken != "" && (description == ownerToken || strings.Contains(description, "["+ownerToken+"]")) {
		return true
	}
	if descToken != "" && (owner == descToken || strings.Contains(owner, "["+descToken+"]")) {
		return true
	}
	for _, legacy := range legacyOwners {
		if legacy != "" {
			if description == legacy || (descToken != "" && descToken == legacy) {
				return true
			}
			legacyToken := singbox.ExtractProxyOwnerToken(legacy)
			if legacyToken != "" && (descToken == legacyToken || description == legacyToken) {
				return true
			}
		}
	}
	return false
}

func TestMihomoBridgeRuntime_PrepareRetainsLegacyOwnerUntilActivate(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#LegacyNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		LegacyOwner: "LegacyNode",
	}); err != nil {
		t.Fatal(err)
	}

	base := &fakeNDMSRegistrarWithOwnership{
		descriptions: map[int]string{7: "LegacyNode"},
		occupied:     map[int]bool{7: true},
		ensured:      make(map[int]string),
	}
	gate := &gatedBridgeRegistrar{base: base}
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	// Step 1: Prepare (gate is closed)
	if err := runtime.prepare(context.Background(), nil); err != nil {
		t.Fatalf("prepare() error = %v", err)
	}

	// Verify that during prepare, LegacyOwner was NOT cleared and NDMS description was NOT overwritten yet
	pAfterPrepare, err := store.GetProxy(nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if pAfterPrepare.Bridge.LegacyOwner != "LegacyNode" {
		t.Fatalf("prepare prematurely cleared LegacyOwner: %+v", pAfterPrepare.Bridge)
	}
	if base.descriptions[7] != "LegacyNode" {
		t.Fatalf("prepare prematurely updated NDMS description: %q", base.descriptions[7])
	}

	// Step 2: Open listener so readiness check passes
	listener, err := net.Listen("tcp", "127.0.0.1:12007")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	// Step 3: Readiness / Activate (gate opens, publication occurs)
	if err := runtime.activate(context.Background()); err != nil {
		t.Fatalf("activate() error = %v", err)
	}

	// Verify that after activate, NDMS description is updated to canonical token AND LegacyOwner is cleared
	canonicalOwner := mihomonative.FormatBridgeDescription(nodes[0].Name, "proxy", nodes[0].ID)
	if base.descriptions[7] != canonicalOwner {
		t.Fatalf("activate did not write canonical owner: %q, want %q", base.descriptions[7], canonicalOwner)
	}

	pAfterActivate, err := store.GetProxy(nodes[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if pAfterActivate.Bridge.LegacyOwner != "" {
		t.Fatalf("activate did not clear LegacyOwner: %+v", pAfterActivate.Bridge)
	}
	if pAfterActivate.Bridge.ProxyIndex != 7 {
		t.Fatalf("proxy index changed unnecessarily: got %d, want 7", pAfterActivate.Bridge.ProxyIndex)
	}
}

func TestMihomoBridgeRuntime_ResolveOwnedBridge(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#ResolveNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		LegacyOwner: "ResolveNode",
	}); err != nil {
		t.Fatal(err)
	}

	base := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: base}
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	t.Run("single_match_resolves_canonical_and_legacy", func(t *testing.T) {
		expectedCanon := mihomonative.BridgeOwnershipDescription("proxy", nodes[0].ID)
		ref := mihomo.BridgeRef{
			ProxyIndex:      7,
			ProxyInterface:  "Proxy7",
			KernelInterface: "t2s7",
			ListenPort:      12007,
			OwnerUUID:       expectedCanon,
		}
		canon, legacy, port, err := runtime.resolvePublishIdentity(ref)
		if err != nil {
			t.Fatalf("expected successful resolve, got %v", err)
		}
		if canon != expectedCanon {
			t.Fatalf("canonical owner = %q, want %q", canon, expectedCanon)
		}
		if port != 12007 {
			t.Fatalf("port = %d, want 12007", port)
		}
		hasAWGM := false
		hasNode := false
		for _, l := range legacy {
			if l == "awg-manager" {
				hasAWGM = true
			}
			if l == "ResolveNode" {
				hasNode = true
			}
		}
		if !hasAWGM || !hasNode {
			t.Fatalf("expected legacy owners to contain awg-manager and ResolveNode, got: %v", legacy)
		}

		// Also verify inspect and withdraw identity resolution
		inspectCanon, inspectLegacy, err := runtime.resolveInspectIdentity(ref)
		if err != nil {
			t.Fatalf("resolveInspectIdentity failed: %v", err)
		}
		if inspectCanon != expectedCanon {
			t.Fatalf("inspect canonical owner = %q, want %q", inspectCanon, expectedCanon)
		}
		if len(inspectLegacy) == 0 {
			t.Fatalf("expected non-empty legacy owners for inspect")
		}

		withdrawCanon, withdrawLegacy, err := runtime.resolveWithdrawIdentity(ref)
		if err != nil {
			t.Fatalf("resolveWithdrawIdentity failed: %v", err)
		}
		if withdrawCanon != expectedCanon {
			t.Fatalf("withdraw canonical owner = %q, want %q", withdrawCanon, expectedCanon)
		}
		if len(withdrawLegacy) == 0 {
			t.Fatalf("expected non-empty legacy owners for withdraw")
		}
	})

	t.Run("unmapped_bridge_rejected", func(t *testing.T) {
		ref := mihomo.BridgeRef{
			ProxyIndex:      99,
			ProxyInterface:  "Proxy99",
			KernelInterface: "t2s99",
			ListenPort:      12099,
			OwnerUUID:       "some-other-owner",
		}
		_, _, _, err := runtime.resolvePublishIdentity(ref)
		if err == nil || !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership for unmapped bridge, got %v", err)
		}
	})
}

func TestMihomoBridgeRuntime_InspectBridgeClassificationMatrix(t *testing.T) {
	store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#InspectNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		LegacyOwner: "InspectNode",
	}); err != nil {
		t.Fatal(err)
	}

	canonicalOwner := mihomonative.BridgeOwnershipDescription("proxy", nodes[0].ID)
	ref := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       canonicalOwner,
	}

	cases := []struct {
		name       string
		occupied   bool
		desc       string
		wantExists bool
		wantOwner  string
		wantLegacy string
	}{
		{
			name:       "canonical_owner",
			occupied:   true,
			desc:       canonicalOwner,
			wantExists: true,
			wantOwner:  canonicalOwner,
			wantLegacy: "",
		},
		{
			name:       "recognized_global_legacy_owner",
			occupied:   true,
			desc:       "awg-manager",
			wantExists: true,
			wantOwner:  "",
			wantLegacy: "awg-manager",
		},
		{
			name:       "recognized_custom_legacy_owner",
			occupied:   true,
			desc:       "InspectNode",
			wantExists: true,
			wantOwner:  "",
			wantLegacy: "InspectNode",
		},
		{
			name:       "foreign_owner",
			occupied:   true,
			desc:       "alien-entity-token",
			wantExists: true,
			wantOwner:  "alien-entity-token",
			wantLegacy: "",
		},
		{
			name:       "unmanaged_bridge",
			occupied:   true,
			desc:       "",
			wantExists: true,
			wantOwner:  "",
			wantLegacy: "",
		},
		{
			name:       "absent_bridge",
			occupied:   false,
			desc:       "",
			wantExists: false,
			wantOwner:  "",
			wantLegacy: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newFakeNDMSRegistrarWithOwnership()
			if tc.occupied {
				base.occupied[7] = true
				base.descriptions[7] = tc.desc
			}
			gate := &gatedBridgeRegistrar{base: base}
			manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
			runtime := newMihomoBridgeRuntime(store, manager, gate)

			obs, err := runtime.InspectBridge(context.Background(), ref)
			if err != nil {
				t.Fatalf("InspectBridge failed: %v", err)
			}
			if obs.Exists != tc.wantExists {
				t.Fatalf("Exists = %v, want %v", obs.Exists, tc.wantExists)
			}
			if obs.OwnerUUID != tc.wantOwner {
				t.Fatalf("OwnerUUID = %q, want %q", obs.OwnerUUID, tc.wantOwner)
			}
			if obs.LegacyOwner != tc.wantLegacy {
				t.Fatalf("LegacyOwner = %q, want %q", obs.LegacyOwner, tc.wantLegacy)
			}
		})
	}
}

func TestMihomoBridgeRuntime_CoordinatorLifecycleSubtests(t *testing.T) {
	newFixture := func(t *testing.T, legacyDesc string) (*mihomoBridgeRuntime, *fakeNDMSRegistrarWithOwnership, mihomo.BridgeRef, string) {
		store, err := mihomonative.NewStore(filepath.Join(t.TempDir(), "native.json"))
		if err != nil {
			t.Fatal(err)
		}
		nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#LifecycleNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
			LegacyOwner: legacyDesc,
		}); err != nil {
			t.Fatal(err)
		}

		base := newFakeNDMSRegistrarWithOwnership()
		gate := &gatedBridgeRegistrar{base: base}
		manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
		runtime := newMihomoBridgeRuntime(store, manager, gate)

		canon := mihomonative.FormatBridgeDescription(nodes[0].Name, "proxy", nodes[0].ID)
		ref := mihomo.BridgeRef{
			ProxyIndex:      7,
			ProxyInterface:  "Proxy7",
			KernelInterface: "t2s7",
			ListenPort:      12007,
			OwnerUUID:       mihomonative.BridgeOwnershipDescription("proxy", nodes[0].ID),
		}
		return runtime, base, ref, canon
	}

	t.Run("publish_own_and_idempotent_repeat", func(t *testing.T) {
		runtime, base, ref, canon := newFixture(t, "")
		ctx := context.Background()

		if err := runtime.PublishBridge(ctx, ref); err != nil {
			t.Fatalf("first PublishBridge failed: %v", err)
		}
		if !base.occupied[7] || base.descriptions[7] != canon {
			t.Fatalf("NDMS state invalid after publish: occupied=%v desc=%q", base.occupied[7], base.descriptions[7])
		}

		callsBefore := base.publishCalls
		if err := runtime.PublishBridge(ctx, ref); err != nil {
			t.Fatalf("second PublishBridge failed: %v", err)
		}
		if base.descriptions[7] != canon {
			t.Fatalf("NDMS description changed after idempotent publish: %q", base.descriptions[7])
		}
		if base.publishCalls <= callsBefore {
			t.Fatal("publish was not called")
		}
	})

	t.Run("publish_foreign_rejected", func(t *testing.T) {
		runtime, base, ref, _ := newFixture(t, "")
		ctx := context.Background()

		base.occupied[7] = true
		base.descriptions[7] = "foreign-tenant-xyz"

		err := runtime.PublishBridge(ctx, ref)
		if err == nil || !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership on foreign publish, got: %v", err)
		}
		if base.descriptions[7] != "foreign-tenant-xyz" {
			t.Fatalf("foreign description must not be mutated, got: %q", base.descriptions[7])
		}
	})

	t.Run("publish_unmanaged_rejected", func(t *testing.T) {
		runtime, base, ref, _ := newFixture(t, "")
		ctx := context.Background()

		base.occupied[7] = true
		base.descriptions[7] = ""

		err := runtime.PublishBridge(ctx, ref)
		if err == nil || !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership on unmanaged publish, got: %v", err)
		}
	})

	t.Run("withdraw_own", func(t *testing.T) {
		runtime, base, ref, canon := newFixture(t, "")
		ctx := context.Background()

		base.occupied[7] = true
		base.descriptions[7] = canon

		if err := runtime.WithdrawBridge(ctx, ref); err != nil {
			t.Fatalf("WithdrawBridge failed: %v", err)
		}
		if base.occupied[7] {
			t.Fatal("bridge must be withdrawn from NDMS")
		}

		if err := runtime.WithdrawBridge(ctx, ref); err != nil {
			t.Fatalf("idempotent repeat WithdrawBridge failed: %v", err)
		}
	})

	t.Run("withdraw_foreign_rejected", func(t *testing.T) {
		runtime, base, ref, _ := newFixture(t, "")
		ctx := context.Background()

		base.occupied[7] = true
		base.descriptions[7] = "alien-owner"

		err := runtime.WithdrawBridge(ctx, ref)
		if err == nil || !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
			t.Fatalf("expected ErrForeignBridgeOwnership on foreign withdraw, got: %v", err)
		}
		if !base.occupied[7] || base.descriptions[7] != "alien-owner" {
			t.Fatal("foreign bridge must NOT be deleted from NDMS")
		}
	})

	t.Run("legacy_owner_migration", func(t *testing.T) {
		runtime, base, ref, canon := newFixture(t, "LegacyNode")
		ctx := context.Background()

		base.occupied[7] = true
		base.descriptions[7] = "LegacyNode"

		if err := runtime.PublishBridge(ctx, ref); err != nil {
			t.Fatalf("PublishBridge for legacy owner failed: %v", err)
		}
		if base.descriptions[7] != canon {
			t.Fatalf("legacy owner was not migrated to canonical: got %q, want %q", base.descriptions[7], canon)
		}
	})
}

type fakeCoordinatorOperator struct {
	running bool
	pid     int
}

func (f *fakeCoordinatorOperator) Start() error {
	f.running = true
	f.pid = 1001
	return nil
}

func (f *fakeCoordinatorOperator) Stop() error {
	f.running = false
	f.pid = 0
	return nil
}

func (f *fakeCoordinatorOperator) StopAndWait(context.Context) error {
	f.running = false
	f.pid = 0
	return nil
}

func (f *fakeCoordinatorOperator) IsRunning() (bool, int) {
	return f.running, f.pid
}

type fakeCoordinatorValidator struct{}

func (f *fakeCoordinatorValidator) ValidateConfigFile(context.Context, string) error {
	return nil
}

type gateBTestCluster struct {
	t         *testing.T
	dir       string
	store     *mihomonative.Store
	ndms      *fakeNDMSRegistrarWithOwnership
	runtime   *mihomoBridgeRuntime
	coord     *mihomo.ApplyCoordinator
	compileFn func(context.Context) (*mihomo.CompileResult, error)
}

func newGateBTestCluster(t *testing.T) *gateBTestCluster {
	dir := t.TempDir()
	store, err := mihomonative.NewStore(filepath.Join(dir, "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)
	runtime.SetDurableManifestFile(filepath.Join(dir, "config.yaml.txn.json"))
	operator := &fakeCoordinatorOperator{running: true, pid: 1001}
	validator := &fakeCoordinatorValidator{}

	compileFn := func(ctx context.Context) (*mihomo.CompileResult, error) {
		tb := store.TxAdapter().ListBridges()
		cfg := []byte("mixed-port: 1099\nmode: rule\n")
		return &mihomo.CompileResult{
			ConfigYAML:        cfg,
			ConfigDigest:      strictfs.ComputeBytesDigest(cfg),
			InputDigest:       "test-input",
			Mode:              mihomo.RuntimeEnforced,
			RequiredListeners: []mihomo.ListenerSpec{{Network: "tcp", Port: 1099, Purpose: "mixed-port"}},
			TargetBridges:     tb,
		}, nil
	}

	coord := mihomo.NewApplyCoordinator(mihomo.CoordinatorConfig{
		ConfigDir:     dir,
		Operator:      operator,
		Validator:     validator,
		BridgeRuntime: runtime,
		StoreTx:       store.TxAdapter(),
		Verifier:      &mihomo.NoopProcessVerifier{},
		Compiler:      compileFn,
	})

	return &gateBTestCluster{
		t:         t,
		dir:       dir,
		store:     store,
		ndms:      ndms,
		runtime:   runtime,
		coord:     coord,
		compileFn: compileFn,
	}
}

func TestGateB_Rev8_EndToEnd_NormalApplyAndCommit(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#NormalNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	err = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err != nil {
		t.Fatal(err)
	}

	canonOwner := mihomonative.FormatBridgeDescription(nodes[0].Name, "proxy", nodes[0].ID)

	err = c.coord.MutateAndApply(ctx, nil, c.compileFn)
	if err != nil {
		t.Fatalf("MutateAndApply failed: %v", err)
	}

	if !c.ndms.occupied[7] {
		t.Fatal("NDMS Proxy7 must be occupied")
	}
	if c.ndms.descriptions[7] != canonOwner {
		t.Fatalf("NDMS Proxy7 description = %q, want %q", c.ndms.descriptions[7], canonOwner)
	}
	if c.ndms.ports[7] != 12007 {
		t.Fatalf("NDMS Proxy7 port = %d, want 12007", c.ndms.ports[7])
	}

	obs, err := c.runtime.ListObservedBridges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].ProxyIndex != 7 || !obs[0].Exists {
		t.Fatalf("unexpected observed bridges: %+v", obs)
	}
}

func TestGateB_Rev8_EndToEnd_ResourceDeletion(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#DeleteNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatalf("initial apply failed: %v", err)
	}

	if !c.ndms.occupied[7] {
		t.Fatal("Proxy7 must be occupied before delete")
	}

	mutateFn := func() error {
		return c.store.DeleteProxy(nodes[0].ID)
	}

	err = c.coord.MutateAndApply(ctx, mutateFn, c.compileFn)
	if err != nil {
		t.Fatalf("MutateAndApply for deletion failed: %v", err)
	}

	if c.ndms.occupied[7] {
		t.Fatal("NDMS Proxy7 must be withdrawn after resource deletion")
	}

	if c.coord.State() != mihomo.StateIdle {
		t.Fatalf("coordinator state = %s, want StateIdle", c.coord.State())
	}
}

func TestGateB_Rev8_EndToEnd_SameSlotReplacement_WithdrawPrecedesPublish(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodesA, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#NodeA", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodesA[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatalf("initial apply A failed: %v", err)
	}

	canonA := mihomonative.BridgeOwnershipDescription("proxy", nodesA[0].ID)

	c.ndms.mu.Lock()
	c.ndms.events = nil
	c.ndms.mu.Unlock()

	var canonB string
	mutateFn := func() error {
		if err := c.store.DeleteProxy(nodesA[0].ID); err != nil {
			return err
		}
		nodesB, err := c.store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@native.example:443?type=xhttp#NodeB", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			return err
		}
		canonB = mihomonative.FormatBridgeDescription(nodesB[0].Name, "proxy", nodesB[0].ID)
		return c.store.SetBridge("proxy", nodesB[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12008, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		})
	}

	if err := c.coord.MutateAndApply(ctx, mutateFn, c.compileFn); err != nil {
		t.Fatalf("replacement MutateAndApply failed: %v", err)
	}

	c.ndms.mu.Lock()
	events := append([]string{}, c.ndms.events...)
	c.ndms.mu.Unlock()

	withdrawIdx := -1
	publishIdx := -1
	expectedWithdraw := fmt.Sprintf("withdraw:7:%s", canonA)
	expectedPublish := fmt.Sprintf("publish:7:%s:12008", canonB)

	for i, ev := range events {
		if ev == expectedWithdraw {
			withdrawIdx = i
		}
		if ev == expectedPublish {
			publishIdx = i
		}
	}

	if withdrawIdx == -1 {
		t.Fatalf("withdraw event %q not found in %v", expectedWithdraw, events)
	}
	if publishIdx == -1 {
		t.Fatalf("publish event %q not found in %v", expectedPublish, events)
	}
	if withdrawIdx >= publishIdx {
		t.Fatalf("withdraw-A (idx %d) must precede publish-B (idx %d) in events: %v", withdrawIdx, publishIdx, events)
	}

	if !c.ndms.occupied[7] || c.ndms.descriptions[7] != canonB || c.ndms.ports[7] != 12008 {
		t.Fatalf("NDMS state invalid: occupied=%v desc=%q port=%d", c.ndms.occupied[7], c.ndms.descriptions[7], c.ndms.ports[7])
	}
}

func TestGateB_Rev8_EndToEnd_PortChangeControlledReplacement(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#PortChangeNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatal(err)
	}

	canonOwner := mihomonative.BridgeOwnershipDescription("proxy", nodes[0].ID)

	c.ndms.mu.Lock()
	c.ndms.events = nil
	c.ndms.mu.Unlock()

	mutateFn := func() error {
		return c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12009, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		})
	}

	if err := c.coord.MutateAndApply(ctx, mutateFn, c.compileFn); err != nil {
		t.Fatalf("port change MutateAndApply failed: %v", err)
	}

	c.ndms.mu.Lock()
	events := append([]string{}, c.ndms.events...)
	c.ndms.mu.Unlock()

	expectedWithdraw := fmt.Sprintf("withdraw:7:%s", canonOwner)
	expectedPublish := fmt.Sprintf("publish:7:%s:12009", mihomonative.FormatBridgeDescription(nodes[0].Name, "proxy", nodes[0].ID))

	withdrawIdx := -1
	publishIdx := -1
	for i, ev := range events {
		if ev == expectedWithdraw {
			withdrawIdx = i
		}
		if ev == expectedPublish {
			publishIdx = i
		}
	}

	if withdrawIdx == -1 || publishIdx == -1 || withdrawIdx >= publishIdx {
		t.Fatalf("controlled replacement event order incorrect: withdrawIdx=%d publishIdx=%d events=%v", withdrawIdx, publishIdx, events)
	}
	if c.ndms.ports[7] != 12009 {
		t.Fatalf("port = %d, want 12009", c.ndms.ports[7])
	}
}

func TestGateB_Rev8_EndToEnd_FailPublication_RollbackRestoresPrevious(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodesA, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#RollbackA", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodesA[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatal(err)
	}

	canonA := mihomonative.FormatBridgeDescription(nodesA[0].Name, "proxy", nodesA[0].ID)

	c.ndms.failPublishIndex = 7

	mutateFn := func() error {
		_ = c.store.DeleteProxy(nodesA[0].ID)
		nodesB, err := c.store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@native.example:443?type=xhttp#RollbackB", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			return err
		}
		return c.store.SetBridge("proxy", nodesB[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12008, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		})
	}

	c.ndms.onBeforePublish = func(index int) {
		if index == 7 {
			c.ndms.failPublishIndex = 0
		}
	}

	err = c.coord.MutateAndApply(ctx, mutateFn, c.compileFn)
	if err == nil {
		t.Fatal("expected MutateAndApply to fail when publishing B fails")
	}

	if !c.ndms.occupied[7] {
		t.Fatal("Proxy 7 must be restored to occupied by rollback")
	}
	if c.ndms.descriptions[7] != canonA {
		t.Fatalf("Proxy 7 description = %q, want %q", c.ndms.descriptions[7], canonA)
	}
	if c.ndms.ports[7] != 12007 {
		t.Fatalf("Proxy 7 port = %d, want 12007", c.ndms.ports[7])
	}

	pA, err := c.store.GetProxy(nodesA[0].ID)
	if err != nil || pA.ID == "" {
		t.Fatalf("store was not restored to proxy A: %v", err)
	}
}

func TestGateB_Rev8_EndToEnd_ForeignTakeoverProtection(t *testing.T) {
	t.Run("foreign_takeover_before_withdrawal_not_deleted", func(t *testing.T) {
		c := newGateBTestCluster(t)
		ctx := context.Background()

		nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#HijackA", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		})
		if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
			t.Fatal(err)
		}

		c.ndms.onBeforeWithdraw = func(idx int) {
			if idx == 7 {
				c.ndms.descriptions[7] = "alien-corporate-vpn"
			}
		}

		mutateFn := func() error {
			return c.store.DeleteProxy(nodes[0].ID)
		}

		err = c.coord.MutateAndApply(ctx, mutateFn, c.compileFn)
		if err == nil {
			t.Fatal("expected failure on foreign takeover")
		}

		if !c.ndms.occupied[7] || c.ndms.descriptions[7] != "alien-corporate-vpn" {
			t.Fatalf("foreign bridge was tampered with! occupied=%v desc=%q", c.ndms.occupied[7], c.ndms.descriptions[7])
		}
	})

	t.Run("foreign_takeover_before_publish_not_overwritten", func(t *testing.T) {
		c := newGateBTestCluster(t)
		ctx := context.Background()

		c.ndms.occupied[7] = true
		c.ndms.descriptions[7] = "alien-corporate-vpn"

		nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#HijackB", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		})

		err = c.coord.MutateAndApply(ctx, nil, c.compileFn)
		if err == nil {
			t.Fatal("expected failure when publishing to foreign slot")
		}

		if !c.ndms.occupied[7] || c.ndms.descriptions[7] != "alien-corporate-vpn" {
			t.Fatalf("foreign bridge was overwritten! occupied=%v desc=%q", c.ndms.occupied[7], c.ndms.descriptions[7])
		}
	})
}

func TestGateB_Rev8_EndToEnd_UnmanagedLiveSlotUntouched(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	c.ndms.occupied[3] = true
	c.ndms.descriptions[3] = ""

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#UnmanagedTest", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})

	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	if !c.ndms.occupied[3] || c.ndms.descriptions[3] != "" {
		t.Fatalf("unmanaged slot was modified: occupied=%v desc=%q", c.ndms.occupied[3], c.ndms.descriptions[3])
	}
}

func TestGateB_Rev8_EndToEnd_CoexistingSingBoxAndUserSlotsUntouched(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	c.ndms.occupied[1] = true
	c.ndms.descriptions[1] = "singbox-direct"
	c.ndms.ports[1] = 1081

	c.ndms.occupied[2] = true
	c.ndms.descriptions[2] = "user-wireguard-peer"
	c.ndms.ports[2] = 51820

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#CoexistNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatal(err)
	}

	mutateFn := func() error { return c.store.DeleteProxy(nodes[0].ID) }
	if err := c.coord.MutateAndApply(ctx, mutateFn, c.compileFn); err != nil {
		t.Fatal(err)
	}

	if !c.ndms.occupied[1] || c.ndms.descriptions[1] != "singbox-direct" || c.ndms.ports[1] != 1081 {
		t.Fatal("sing-box Proxy1 slot was corrupted!")
	}
	if !c.ndms.occupied[2] || c.ndms.descriptions[2] != "user-wireguard-peer" || c.ndms.ports[2] != 51820 {
		t.Fatal("user WireGuard Proxy2 slot was corrupted!")
	}
}

func TestGateB_Rev8_EndToEnd_CurrentStoreB_DoesNotPreventDurableA_Withdrawal(t *testing.T) {
	dir := t.TempDir()
	store, err := mihomonative.NewStore(filepath.Join(dir, "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	ndms := newFakeNDMSRegistrarWithOwnership()
	gate := &gatedBridgeRegistrar{base: ndms}
	manager := mihomonative.NewBridgeManager(store, gate, func() bool { return true })
	runtime := newMihomoBridgeRuntime(store, manager, gate)

	ctx := context.Background()

	nodesB, _ := store.CreateProxy("vless://22222222-2222-2222-2222-222222222222@native.example:443?type=xhttp#NodeB", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	_ = store.SetBridge("proxy", nodesB[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12008, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})

	canonA := "proxy:old-node-a-uuid"
	durableRefA := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       canonA,
	}

	ndms.occupied[7] = true
	ndms.descriptions[7] = canonA
	ndms.ports[7] = 12007

	if err := runtime.WithdrawBridge(ctx, durableRefA); err != nil {
		t.Fatalf("WithdrawBridge(durableRefA) failed even though live owner matches: %v", err)
	}

	if ndms.occupied[7] {
		t.Fatal("Proxy 7 must be withdrawn from NDMS")
	}
}

func TestGateB_Rev8_EndToEnd_EmptyRuntimeCacheAfterRestart(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#CacheNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatal(err)
	}

	gate2 := &gatedBridgeRegistrar{base: c.ndms}
	manager2 := mihomonative.NewBridgeManager(c.store, gate2, func() bool { return true })
	runtime2 := newMihomoBridgeRuntime(c.store, manager2, gate2)

	coord2 := mihomo.NewApplyCoordinator(mihomo.CoordinatorConfig{
		ConfigDir:     c.dir,
		Operator:      &fakeCoordinatorOperator{running: true, pid: 1001},
		Validator:     &fakeCoordinatorValidator{},
		BridgeRuntime: runtime2,
		StoreTx:       c.store.TxAdapter(),
		Verifier:      &mihomo.NoopProcessVerifier{},
		Compiler:      c.compileFn,
	})

	if err := coord2.RecoverOnStartup(ctx); err != nil {
		t.Fatalf("RecoverOnStartup failed: %v", err)
	}

	obs, err := runtime2.ListObservedBridges(ctx)
	if err != nil {
		t.Fatalf("ListObservedBridges failed: %v", err)
	}
	if len(obs) != 1 || obs[0].ProxyIndex != 7 || !obs[0].Exists {
		t.Fatalf("unexpected observed bridges from recovered runtime: %+v", obs)
	}
}

func TestGateB_Rev8_EndToEnd_PersistenceOrder_CheckpointsOnDisk(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#OrderNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
		ListenPort: 12007, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	})
	if err := c.coord.MutateAndApply(ctx, nil, c.compileFn); err != nil {
		t.Fatal(err)
	}

	previousBridgesVerified := false
	mutateFn := func() error {
		manifestPath := filepath.Join(c.dir, "config.yaml.txn.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("manifest must be on disk before mutateFn: %v", err)
		}
		var m mihomo.TransactionManifest
		if err := mihomo.DecodeJSONStrict(data, &m); err != nil {
			t.Fatalf("decode manifest failed: %v", err)
		}
		if len(m.PreviousBridges) != 1 || m.PreviousBridges[0].ProxyIndex != 7 {
			t.Fatalf("PreviousBridges not on disk before mutateFn: %+v", m.PreviousBridges)
		}
		previousBridgesVerified = true

		return c.store.SetBridge("proxy", nodes[0].ID, mihomonative.ProxyBridge{
			ListenPort: 12008, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		})
	}

	targetBridgesVerified := false
	c.coord.SetHooks(mihomo.ApplyCoordinatorHooks{
		CrashAtHook: func(point string) {
			if point == "after_target_bridges_persist" {
				manifestPath := filepath.Join(c.dir, "config.yaml.txn.json")
				data, err := os.ReadFile(manifestPath)
				if err == nil {
					var m mihomo.TransactionManifest
					if err := mihomo.DecodeJSONStrict(data, &m); err == nil {
						if len(m.TargetBridges) == 1 && m.TargetBridgesDigest != "" {
							targetBridgesVerified = true
						}
					}
				}
			}
		},
	})

	if err := c.coord.MutateAndApply(ctx, mutateFn, c.compileFn); err != nil {
		t.Fatal(err)
	}

	if !previousBridgesVerified {
		t.Fatal("PreviousBridges was not verified on disk before mutateFn")
	}
	if !targetBridgesVerified {
		t.Fatal("TargetBridges and TargetBridgesDigest were not verified on disk after target persist")
	}
}

func TestGateB_Rev8_EndToEnd_CorruptBridgeJournalDigestFailsClosed(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	ref := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       "gate1-owner",
	}

	m := mihomo.TransactionManifest{
		Version:             1,
		TxID:                "20260922120000",
		State:               mihomo.StateBridgesReconciling,
		TargetBridges:       []mihomo.BridgeRef{ref},
		TargetBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{ref}),
		BridgeOperations: []mihomo.BridgeOperation{
			{
				OperationID:  fmt.Sprintf("20260922120000-create-%s-%s", ref.SlotKey(), ref.Digest()),
				Action:       "create",
				TargetDigest: "0000000000000000000000000000000000000000000000000000000000000000",
				BridgeRef:    ref,
				State:        mihomo.BridgeOpIntent,
			},
		},
	}

	err := c.coord.SyncBridgesForTest(ctx, &m, nil, []mihomo.BridgeRef{ref})
	if err == nil {
		t.Fatal("expected syncBridges to fail on corrupt digest journal")
	}
	if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrForeignBridgeOwnership on corrupt journal, got: %v", err)
	}
	if c.coord.State() != mihomo.StateRecoveryRequired {
		t.Fatalf("state = %s, want StateRecoveryRequired", c.coord.State())
	}
}

// ---------------------------------------------------------------------------
// Gate B Rev 10: Role-Aware Runtime Tests (P1-2)
// ---------------------------------------------------------------------------

// 1. Prove arbitrary cached stale same-owner ref cannot bypass mismatch.
func TestGateB_Rev10_ArbitraryCachedStaleRefCannotBypassMismatch(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@example.com:443#TestNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxyID := nodes[0].ID
	ownerUUID := mihomonative.BridgeOwnershipDescription("proxy", proxyID)
	bridge := mihomonative.ProxyBridge{
		ListenPort:      12007,
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
	}
	if err := c.store.SetBridge("proxy", proxyID, bridge); err != nil {
		t.Fatal(err)
	}

	durableRef := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       ownerUUID,
	}
	if err := c.runtime.ReplaceDurableBridges([]mihomo.BridgeRef{durableRef}); err != nil {
		t.Fatal(err)
	}

	// Arbitrary stale same-owner ref with mismatched port 9999
	staleRef := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      9999,
		OwnerUUID:       ownerUUID,
	}

	// Ensure no active transaction roles are set
	_ = c.runtime.SetActiveTransactionRoles(nil)

	// Neither Inspect nor Withdraw may bypass the mismatch without active transaction proof!
	_, err = c.runtime.InspectBridge(ctx, staleRef)
	if err == nil {
		t.Fatal("expected InspectBridge to fail on stale ref port mismatch, but got nil")
	}
	if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
	}

	err = c.runtime.WithdrawBridge(ctx, staleRef)
	if err == nil {
		t.Fatal("expected WithdrawBridge to fail on stale ref port mismatch, but got nil")
	}
	if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
	}
}

// 2. Prove valid persisted port-only A->B replacement works.
func TestGateB_Rev10_ValidPersistedPortReplacementWorks(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	// Store has new bridge B (port 12008)
	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@example.com:443#TestNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxyID := nodes[0].ID
	ownerUUID := mihomonative.BridgeOwnershipDescription("proxy", proxyID)
	bridgeB := mihomonative.ProxyBridge{
		ListenPort:      12008,
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
	}
	if err := c.store.SetBridge("proxy", proxyID, bridgeB); err != nil {
		t.Fatal(err)
	}

	refA := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       ownerUUID,
	}
	refB := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12008,
		OwnerUUID:       ownerUUID,
	}

	// A durable in-flight manifest proves the A -> B replacement. The in-memory
	// cache alone is intentionally insufficient because it can become stale.
	manifestPath := filepath.Join(c.dir, "config.yaml.txn.json")
	c.runtime.SetDurableManifestFile(manifestPath)
	m := mihomo.TransactionManifest{
		Version:               1,
		TxID:                  "20260922120000-valid-replace",
		State:                 mihomo.StateSwapApplied,
		Sequence:              2,
		PreviousBridges:       []mihomo.BridgeRef{refA},
		PreviousBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{refA}),
		TargetBridges:         []mihomo.BridgeRef{refB},
		TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
		DesiredMode:           mihomo.RuntimeEnforced,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}
	mBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := strictfs.StrictWriteAtomic(manifestPath, mBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// Populate the cache too, proving that the decision remains tied to disk.
	roles := &mihomo.ActiveTransactionRoles{
		TxID:            "20260922120000-valid-replace",
		PreviousBridges: []mihomo.BridgeRef{refA},
		TargetBridges:   []mihomo.BridgeRef{refB},
	}
	if err := c.runtime.SetActiveTransactionRoles(roles); err != nil {
		t.Fatal(err)
	}

	// Now inspecting/withdrawing old refA during the transaction is authorized!
	c.ndms.occupied[7] = true
	c.ndms.ports[7] = 12007
	c.ndms.descriptions[7] = ownerUUID

	obs, err := c.runtime.InspectBridge(ctx, refA)
	if err != nil {
		t.Fatalf("expected InspectBridge to succeed for authorized port replacement, got: %v", err)
	}
	if !obs.Exists || obs.ListenPort != 12007 {
		t.Fatalf("expected observed bridge on port 12007, got %+v", obs)
	}

	// A stale in-memory role cache must not authorize after durable commit.
	m.State = mihomo.StateCommitted
	m.Sequence++
	m.UpdatedAt = time.Now()
	mBytes, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := strictfs.StrictWriteAtomic(manifestPath, mBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.runtime.InspectBridge(ctx, refA); !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("stale in-memory roles bypassed committed durable manifest: %v", err)
	}

	// Restore the valid in-flight phase for the withdraw assertion below.
	m.State = mihomo.StateSwapApplied
	m.Sequence++
	m.UpdatedAt = time.Now()
	mBytes, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := strictfs.StrictWriteAtomic(manifestPath, mBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// Withdrawing refA is also authorized
	if err := c.runtime.WithdrawBridge(ctx, refA); err != nil {
		t.Fatalf("expected WithdrawBridge to succeed for authorized port replacement, got: %v", err)
	}
}

// 3. Prove after transaction completion old A identity is no longer authorized.
func TestGateB_Rev10_CompletedTransactionRevokesOldIdentityAuthorization(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@example.com:443#TestNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxyID := nodes[0].ID
	ownerUUID := mihomonative.BridgeOwnershipDescription("proxy", proxyID)
	bridgeB := mihomonative.ProxyBridge{
		ListenPort:      12008,
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
	}
	if err := c.store.SetBridge("proxy", proxyID, bridgeB); err != nil {
		t.Fatal(err)
	}

	refA := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       ownerUUID,
	}
	refB := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12008,
		OwnerUUID:       ownerUUID,
	}

	roles := &mihomo.ActiveTransactionRoles{
		TxID:            "20260922120000-revocation",
		PreviousBridges: []mihomo.BridgeRef{refA},
		TargetBridges:   []mihomo.BridgeRef{refB},
	}
	_ = c.runtime.SetActiveTransactionRoles(roles)

	// Transaction finishes -> active roles cleared
	_ = c.runtime.SetActiveTransactionRoles(nil)

	// Now old refA must be rejected with ErrForeignBridgeOwnership
	_, err = c.runtime.InspectBridge(ctx, refA)
	if err == nil {
		t.Fatal("expected InspectBridge to fail after transaction completion, but got nil")
	}
	if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
	}

	err = c.runtime.WithdrawBridge(ctx, refA)
	if err == nil {
		t.Fatal("expected WithdrawBridge to fail after transaction completion, but got nil")
	}
	if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
	}
}

// 4. Prove registry cache loss/rebuild preserves the same decision from disk manifest.
func TestGateB_Rev10_RegistryCacheLossPreservesDecisionFromDiskManifest(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	manifestPath := filepath.Join(c.dir, "config.yaml.txn.json")
	c.runtime.SetDurableManifestFile(manifestPath)

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@example.com:443#TestNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxyID := nodes[0].ID
	ownerUUID := mihomonative.BridgeOwnershipDescription("proxy", proxyID)
	bridgeB := mihomonative.ProxyBridge{
		ListenPort:      12008,
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
	}
	if err := c.store.SetBridge("proxy", proxyID, bridgeB); err != nil {
		t.Fatal(err)
	}

	refA := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       ownerUUID,
	}
	refB := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12008,
		OwnerUUID:       ownerUUID,
	}

	// Write active transaction manifest to disk in StateSwapApplied
	m := mihomo.TransactionManifest{
		Version:               1,
		TxID:                  "20260922120000-disk-rebuild",
		State:                 mihomo.StateSwapApplied,
		Sequence:              2,
		PreviousBridges:       []mihomo.BridgeRef{refA},
		PreviousBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{refA}),
		TargetBridges:         []mihomo.BridgeRef{refB},
		TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
		DesiredMode:           mihomo.RuntimeEnforced,
		CreatedAt:             time.Now(),
		UpdatedAt:             time.Now(),
	}
	mBytes, _ := json.MarshalIndent(m, "", "  ")
	if err := strictfs.StrictWriteAtomic(manifestPath, mBytes, 0600); err != nil {
		t.Fatal(err)
	}

	// Wipe memory cache: activeRoles = nil
	_ = c.runtime.SetActiveTransactionRoles(nil)

	// Despite empty memory cache, disk manifest proves the A->B replacement!
	c.ndms.occupied[7] = true
	c.ndms.ports[7] = 12007
	c.ndms.descriptions[7] = ownerUUID

	obs, err := c.runtime.InspectBridge(ctx, refA)
	if err != nil {
		t.Fatalf("expected InspectBridge to succeed from disk manifest proof, got: %v", err)
	}
	if !obs.Exists || obs.ListenPort != 12007 {
		t.Fatalf("expected observed bridge on port 12007, got %+v", obs)
	}

	// Now simulate transaction completion on disk (StateCommitted)
	m.State = mihomo.StateCommitted
	mBytes, _ = json.MarshalIndent(m, "", "  ")
	_ = strictfs.StrictWriteAtomic(manifestPath, mBytes, 0600)

	// Decision changes: old identity A is no longer authorized
	_, err = c.runtime.InspectBridge(ctx, refA)
	if err == nil {
		t.Fatal("expected InspectBridge to fail after disk manifest committed, but got nil")
	}
	if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
		t.Fatalf("expected ErrForeignBridgeOwnership, got: %v", err)
	}
}

func TestAuthorizedPortReplacement_P1_3_NegativeCases(t *testing.T) {
	c := newGateBTestCluster(t)
	ctx := context.Background()

	manifestPath := filepath.Join(c.dir, "config.yaml.txn.json")
	c.runtime.SetDurableManifestFile(manifestPath)

	nodes, err := c.store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@example.com:443#TestNode", mihomonative.EngineMihomo, mihomonative.EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxyID := nodes[0].ID
	ownerUUID := mihomonative.BridgeOwnershipDescription("proxy", proxyID)
	bridgeB := mihomonative.ProxyBridge{
		ListenPort:      12008,
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
	}
	if err := c.store.SetBridge("proxy", proxyID, bridgeB); err != nil {
		t.Fatal(err)
	}

	refA := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12007,
		OwnerUUID:       ownerUUID,
	}
	refB := mihomo.BridgeRef{
		ProxyIndex:      7,
		ProxyInterface:  "Proxy7",
		KernelInterface: "t2s7",
		ListenPort:      12008,
		OwnerUUID:       ownerUUID,
	}

	// Ensure NDMS has the old bridge on port 12007
	c.ndms.occupied[7] = true
	c.ndms.ports[7] = 12007
	c.ndms.descriptions[7] = ownerUUID

	tests := []struct {
		name     string
		manifest func() mihomo.TransactionManifest
	}{
		{
			name: "malformed_previous_digest",
			manifest: func() mihomo.TransactionManifest {
				return mihomo.TransactionManifest{
					Version:               1,
					TxID:                  "20260922120000000001",
					State:                 mihomo.StateSwapApplied,
					Sequence:              2,
					PreviousBridges:       []mihomo.BridgeRef{refA},
					PreviousBridgesDigest: "corrupted-digest",
					TargetBridges:         []mihomo.BridgeRef{refB},
					TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
					DesiredMode:           mihomo.RuntimeEnforced,
					CreatedAt:             time.Now(),
					UpdatedAt:             time.Now(),
				}
			},
		},
		{
			name: "incomplete_previous_refs",
			manifest: func() mihomo.TransactionManifest {
				incompleteRef := mihomo.BridgeRef{
					ProxyIndex:      7,
					KernelInterface: "t2s7",
					// Missing ProxyInterface, ListenPort, OwnerUUID
				}
				return mihomo.TransactionManifest{
					Version:               1,
					TxID:                  "20260922120000000002",
					State:                 mihomo.StateSwapApplied,
					Sequence:              2,
					PreviousBridges:       []mihomo.BridgeRef{incompleteRef},
					PreviousBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{incompleteRef}),
					TargetBridges:         []mihomo.BridgeRef{refB},
					TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
					DesiredMode:           mihomo.RuntimeEnforced,
					CreatedAt:             time.Now(),
					UpdatedAt:             time.Now(),
				}
			},
		},
		{
			name: "migration_operation_kind_rejected",
			manifest: func() mihomo.TransactionManifest {
				return mihomo.TransactionManifest{
					Version:               1,
					TxID:                  "20260922120000000003",
					OperationKind:         mihomo.OperationMigration,
					State:                 mihomo.StateMigrationActiveWritten,
					Sequence:              2,
					CandidateGenerationID: "gen-000002-test",
					PreviousRecordDigest:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					CandidateRecordDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					PreviousBridges:       []mihomo.BridgeRef{refA},
					PreviousBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{refA}),
					TargetBridges:         []mihomo.BridgeRef{refB},
					TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
					DesiredMode:           mihomo.RuntimeEnforced,
					CreatedAt:             time.Now(),
					UpdatedAt:             time.Now(),
				}
			},
		},
		{
			name: "disallowed_manifest_state",
			manifest: func() mihomo.TransactionManifest {
				return mihomo.TransactionManifest{
					Version:               1,
					TxID:                  "20260922120000000004",
					State:                 mihomo.StateCandidatePublished, // not in authorizedPortReplacementStates
					Sequence:              1,
					CandidateGenerationID: "gen-000002-test",
					CandidateConfigDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
					PreviousBridges:       []mihomo.BridgeRef{refA},
					PreviousBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{refA}),
					TargetBridges:         []mihomo.BridgeRef{refB},
					TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
					DesiredMode:           mihomo.RuntimeEnforced,
					CreatedAt:             time.Now(),
					UpdatedAt:             time.Now(),
				}
			},
		},
		{
			name: "stale_completed_manifest_state",
			manifest: func() mihomo.TransactionManifest {
				return mihomo.TransactionManifest{
					Version:               1,
					TxID:                  "20260922120000000005",
					State:                 mihomo.StateCommitted,
					Sequence:              10,
					PreviousBridges:       []mihomo.BridgeRef{refA},
					PreviousBridgesDigest: mihomo.BridgesDigest([]mihomo.BridgeRef{refA}),
					TargetBridges:         []mihomo.BridgeRef{refB},
					TargetBridgesDigest:   mihomo.BridgesDigest([]mihomo.BridgeRef{refB}),
					DesiredMode:           mihomo.RuntimeEnforced,
					CreatedAt:             time.Now(),
					UpdatedAt:             time.Now(),
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.manifest()
			mBytes, _ := json.MarshalIndent(m, "", "  ")
			if err := strictfs.StrictWriteAtomic(manifestPath, mBytes, 0600); err != nil {
				t.Fatal(err)
			}
			_ = c.runtime.SetActiveTransactionRoles(nil)

			_, err := c.runtime.InspectBridge(ctx, refA)
			if err == nil {
				t.Fatalf("expected InspectBridge to reject unauthorized replacement in %s, got nil", tc.name)
			}
			if !errors.Is(err, mihomo.ErrForeignBridgeOwnership) {
				t.Fatalf("expected ErrForeignBridgeOwnership in %s, got: %v", tc.name, err)
			}
		})
	}
}

package mihomonative

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

type recordedBridge struct {
	index       int
	port        int
	description string
}

type fakeBridgeRegistrar struct {
	occupied     map[int]bool
	descriptions map[int]string
	ensured      []recordedBridge
	removed      []int
	released     []int
	nextCalls    int
}

func (f *fakeBridgeRegistrar) ReleaseProxyIndex(index int) {
	f.released = append(f.released, index)
}

func (f *fakeBridgeRegistrar) LookupProxy(_ context.Context, index int) (string, bool, error) {
	if !f.occupied[index] {
		return "", false, nil
	}
	if description, ok := f.descriptions[index]; ok {
		return description, true, nil
	}
	return "foreign", true, nil
}

func (f *fakeBridgeRegistrar) NextFreeIndex(_ context.Context, reserved map[int]bool) (int, error) {
	f.nextCalls++
	for index := 0; index < 128; index++ {
		if !reserved[index] && !f.occupied[index] {
			return index, nil
		}
	}
	return 0, fmt.Errorf("no free proxy index")
}

func (f *fakeBridgeRegistrar) EnsureProxy(_ context.Context, index, port int, description string) error {
	f.ensured = append(f.ensured, recordedBridge{index: index, port: port, description: description})
	f.occupied[index] = true
	if f.descriptions == nil {
		f.descriptions = make(map[int]string)
	}
	f.descriptions[index] = description
	return nil
}

func (f *fakeBridgeRegistrar) EnsureProxyIfOwned(ctx context.Context, index, port int, owner string, legacyOwners ...string) (bool, error) {
	if f.occupied[index] {
		description := f.descriptions[index]
		if !fakeOwnerMatches(description, owner, legacyOwners) {
			return false, nil
		}
	}
	return true, f.EnsureProxy(ctx, index, port, owner)
}

func (f *fakeBridgeRegistrar) RemoveProxyIfOwned(_ context.Context, index int, owner string, legacyOwners ...string) (bool, error) {
	if f.occupied[index] {
		description := f.descriptions[index]
		if !fakeOwnerMatches(description, owner, legacyOwners) {
			return false, nil
		}
	}
	f.removed = append(f.removed, index)
	delete(f.occupied, index)
	delete(f.descriptions, index)
	return true, nil
}

func fakeOwnerMatches(description, owner string, legacyOwners []string) bool {
	if description == owner {
		return true
	}
	descToken := extractMockToken(description)
	ownerToken := extractMockToken(owner)
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
			legacyToken := extractMockToken(legacy)
			if legacyToken != "" && (descToken == legacyToken || description == legacyToken) {
				return true
			}
		}
	}
	return false
}

func extractMockToken(desc string) string {
	desc = strings.TrimSpace(desc)
	if start := strings.Index(desc, "[awg-manager:"); start != -1 {
		end := strings.Index(desc[start:], "]")
		if end != -1 {
			return desc[start+1 : start+end]
		}
		return desc[start+1:]
	}
	if strings.HasPrefix(desc, "awg-manager:") {
		return desc
	}
	return ""
}

func TestStoreBridgeAllocationsAreUniqueAndListenersTargetResources(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateManual(ManualProxyInput{
		Name: "First", Protocol: "anytls", Server: "first.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateManual(ManualProxyInput{
		Name: "Second", Protocol: "anytls", Server: "second.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Provider", URL: "https://example.test/provider.yaml", Format: FormatMihomoProvider,
		EnginePreference: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstBridge := ProxyBridge{ListenPort: 12020, ProxyIndex: 20, ProxyInterface: "Proxy20", KernelInterface: "t2s20"}
	if err := store.SetBridge("proxy", first.ID, firstBridge); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBridge("proxy", second.ID, ProxyBridge{ListenPort: firstBridge.ListenPort, ProxyIndex: 21, ProxyInterface: "Proxy21", KernelInterface: "t2s21"}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate-port err=%v", err)
	}
	if err := store.SetBridge("subscription", sub.ID, ProxyBridge{ListenPort: 12021, ProxyIndex: firstBridge.ProxyIndex, ProxyInterface: "Proxy20", KernelInterface: "t2s20"}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate-index err=%v", err)
	}
	if err := store.SetBridge("proxy", second.ID, ProxyBridge{ListenPort: 12022, ProxyIndex: 22, ProxyInterface: "Proxy999", KernelInterface: "t2s22"}); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("invalid-identity err=%v", err)
	}
	subBridge := ProxyBridge{ListenPort: 12021, ProxyIndex: 21, ProxyInterface: "Proxy21", KernelInterface: "t2s21"}
	if err := store.SetBridge("subscription", sub.ID, subBridge); err != nil {
		t.Fatal(err)
	}

	refs := store.ListBridges()
	if len(refs) != 2 || !refs[0].Enabled || !refs[1].Enabled {
		t.Fatalf("bridges=%#v", refs)
	}
	listeners := store.ConfigBridgeListeners()
	if len(listeners) != 2 {
		t.Fatalf("listeners=%#v", listeners)
	}
	byProxy := make(map[string]BridgeListener, len(listeners))
	for _, listener := range listeners {
		byProxy[listener.Proxy] = listener
	}
	if got := byProxy[first.Name]; got.Port != firstBridge.ListenPort || got.Name != "mihomo-native-p-"+first.ID {
		t.Fatalf("proxy listener=%#v", got)
	}
	if got := byProxy[sub.GroupName]; got.Port != subBridge.ListenPort || got.Name != "mihomo-native-s-"+sub.ID {
		t.Fatalf("subscription listener=%#v", got)
	}
}

func TestBridgeManagerAllocatesActiveResourcesAndRemovesDisabledBridge(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateManual(ManualProxyInput{
		Name: "Standalone", Protocol: "anytls", Server: "standalone.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateManual(ManualProxyInput{
		Name: "Disabled", Protocol: "anytls", Server: "disabled.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	disabled := store.ListProxies()[0]
	if disabled.Name != "Disabled" {
		t.Fatalf("sorted first proxy=%q, want Disabled", disabled.Name)
	}
	if _, err := store.UpdateProxy(disabled.ID, UpdateProxyInput{
		Manual:           &ManualProxyInput{Name: disabled.Name, Protocol: "anytls", Server: "disabled.example", Port: 443, Config: map[string]interface{}{"password": "secret"}},
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Inline", Inline: "vless://id@member.example:443?type=xhttp#Member",
		Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	registrar := &fakeBridgeRegistrar{occupied: map[int]bool{0: true}}
	manager := NewBridgeManager(store, registrar, func() bool { return true })
	if err := manager.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if registrar.nextCalls != 2 {
		t.Fatalf("NextFreeIndex calls=%d, want standalone proxy and subscription only", registrar.nextCalls)
	}
	refs := store.ListBridges()
	if len(refs) != 2 {
		t.Fatalf("bridges=%#v", refs)
	}
	if len(registrar.ensured) != 2 {
		t.Fatalf("ensured=%#v", registrar.ensured)
	}
	seenPorts := make(map[int]bool)
	seenIndices := make(map[int]bool)
	var proxyBridge ProxyBridge
	for _, ref := range refs {
		if ref.Bridge.ListenPort < bridgePortBase || ref.Bridge.ListenPort > bridgePortMax || seenPorts[ref.Bridge.ListenPort] {
			t.Fatalf("invalid or duplicate bridge port in %#v", refs)
		}
		if seenIndices[ref.Bridge.ProxyIndex] || ref.Bridge.ProxyIndex == 0 {
			t.Fatalf("invalid or duplicate proxy index in %#v", refs)
		}
		seenPorts[ref.Bridge.ListenPort] = true
		seenIndices[ref.Bridge.ProxyIndex] = true
		if ref.Kind == "proxy" && ref.ID == proxy.ID {
			proxyBridge = ref.Bridge
		}
	}
	if proxyBridge.ProxyInterface == "" {
		t.Fatalf("standalone proxy bridge not found in %#v", refs)
	}
	for _, ensured := range registrar.ensured {
		if !strings.Contains(ensured.description, "awg-manager:mihomo:") {
			t.Fatalf("description=%q", ensured.description)
		}
	}

	before := store.ListBridges()
	if err := manager.Reconcile(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	if registrar.nextCalls != 2 {
		t.Fatalf("idempotent reconcile allocated again: calls=%d", registrar.nextCalls)
	}
	if len(registrar.removed) != 0 {
		t.Fatalf("idempotent reconcile removed=%#v", registrar.removed)
	}

	current, err := store.GetProxy(proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProxy(proxy.ID, UpdateProxyInput{
		Manual:           &ManualProxyInput{Name: current.Name, Protocol: "anytls", Server: "standalone.example", Port: 443, Config: map[string]interface{}{"password": "secret"}},
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	if len(registrar.removed) != 1 || registrar.removed[0] != proxyBridge.ProxyIndex {
		t.Fatalf("removed=%#v, want proxy index %d", registrar.removed, proxyBridge.ProxyIndex)
	}
	if listeners := store.ConfigBridgeListeners(); len(listeners) != 1 || listeners[0].Proxy != sub.GroupName {
		t.Fatalf("active listeners=%#v", listeners)
	}
}

func TestBridgeManagerDisabledDoesNotAllocate(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManual(ManualProxyInput{
		Name: "Proxy", Protocol: "anytls", Server: "proxy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo); err != nil {
		t.Fatal(err)
	}
	registrar := &fakeBridgeRegistrar{occupied: make(map[int]bool)}
	manager := NewBridgeManager(store, registrar, func() bool { return false })
	if err := manager.Reconcile(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if registrar.nextCalls != 0 || len(store.ListBridges()) != 0 {
		t.Fatalf("disabled bridge manager mutated state: calls=%d bridges=%#v", registrar.nextCalls, store.ListBridges())
	}
}

func TestBridgeManagerReallocatesConfiguredReservedPort(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateManual(ManualProxyInput{
		Name: "Proxy", Protocol: "anytls", Server: "proxy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{
		ListenPort: bridgePortBase, ProxyIndex: 7,
		ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	}
	if err := store.SetBridge("proxy", proxy.ID, bridge); err != nil {
		t.Fatal(err)
	}
	owner := BridgeOwnershipDescription("proxy", proxy.ID)
	registrar := &fakeBridgeRegistrar{
		occupied: map[int]bool{7: true}, descriptions: map[int]string{7: owner},
	}
	manager := NewBridgeManager(store, registrar, func() bool { return true })
	manager.SetRuntimeActive(func() bool { return true })
	manager.SetReservedPorts(func() map[int]bool { return map[int]bool{bridgePortBase: true} })
	if err := manager.Reconcile(context.Background(), store.ListBridges()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProxy(proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bridge == nil || got.Bridge.ListenPort == bridgePortBase {
		t.Fatalf("configured device-proxy port %d was retained: %+v", bridgePortBase, got.Bridge)
	}
	if len(registrar.ensured) != 1 || registrar.ensured[0].port != got.Bridge.ListenPort {
		t.Fatalf("ensured=%+v bridge=%+v", registrar.ensured, got.Bridge)
	}
}

func TestBridgeManagerReallocatesForeignProxyWithoutOverwritingIt(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateManual(ManualProxyInput{
		Name: "Proxy", Protocol: "anytls", Server: "proxy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{
		ListenPort: bridgePortBase, ProxyIndex: 7,
		ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	}
	if err := store.SetBridge("proxy", proxy.ID, bridge); err != nil {
		t.Fatal(err)
	}
	registrar := &fakeBridgeRegistrar{
		occupied: map[int]bool{7: true}, descriptions: map[int]string{7: "user-created"},
	}
	manager := NewBridgeManager(store, registrar, func() bool { return true })
	manager.SetRuntimeActive(func() bool { return true })
	if err := manager.Reconcile(context.Background(), store.ListBridges()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProxy(proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bridge == nil || got.Bridge.ProxyIndex == 7 {
		t.Fatalf("foreign Proxy7 was retained: %+v", got.Bridge)
	}
	if registrar.descriptions[7] != "user-created" {
		t.Fatalf("foreign Proxy7 was overwritten: %q", registrar.descriptions[7])
	}
	if want := FormatBridgeDescription(proxy.Name, "proxy", proxy.ID); registrar.descriptions[got.Bridge.ProxyIndex] != want {
		t.Fatalf("new bridge owner=%q, want %q", registrar.descriptions[got.Bridge.ProxyIndex], want)
	}
}

func TestNextBridgePortSkipsUDPOnlyListener(t *testing.T) {
	used := make(map[int]bool)
	var packet net.PacketConn
	occupiedPort := 0
	for port := bridgePortBase; port < bridgePortMax; port++ {
		candidate, err := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			used[port] = true
			continue
		}
		tcp, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			_ = candidate.Close()
			used[port] = true
			continue
		}
		_ = tcp.Close()
		packet = candidate
		occupiedPort = port
		break
	}
	if packet == nil {
		t.Skip("no free bridge-range port available for UDP probe")
	}
	defer packet.Close()
	for port := bridgePortBase; port < occupiedPort; port++ {
		used[port] = true
	}
	got, err := nextBridgePort(used)
	if err != nil {
		t.Fatal(err)
	}
	if got == occupiedPort {
		t.Fatalf("UDP-only occupied port %d was selected", occupiedPort)
	}
}

func TestBridgeManagerForeignProxyWithSameLabelIsNotAdopted(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateManual(ManualProxyInput{
		Name: "Matching Label", Protocol: "anytls", Server: "proxy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{
		ListenPort: bridgePortBase, ProxyIndex: 7,
		ProxyInterface: "Proxy7", KernelInterface: "t2s7",
	}
	if err := store.SetBridge("proxy", proxy.ID, bridge); err != nil {
		t.Fatal(err)
	}
	// Foreign Proxy7 has description exactly matching the proxy's label "Matching Label"
	registrar := &fakeBridgeRegistrar{
		occupied: map[int]bool{7: true}, descriptions: map[int]string{7: "Matching Label"},
	}
	manager := NewBridgeManager(store, registrar, func() bool { return true })
	manager.SetRuntimeActive(func() bool { return true })
	if err := manager.Reconcile(context.Background(), store.ListBridges()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProxy(proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Proxy7 must NOT be adopted or overwritten because display label is no longer a legacy owner
	if got.Bridge == nil || got.Bridge.ProxyIndex == 7 {
		t.Fatalf("foreign Proxy7 was adopted based on display label: %+v", got.Bridge)
	}
	if registrar.descriptions[7] != "Matching Label" {
		t.Fatalf("foreign Proxy7 was overwritten: %q", registrar.descriptions[7])
	}
}

func TestBridgeManagerLegacyOwnerMigrationClearsLegacyOwner(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateManual(ManualProxyInput{
		Name: "Old Card", Protocol: "anytls", Server: "proxy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{
		ListenPort: bridgePortBase, ProxyIndex: 7,
		ProxyInterface: "Proxy7", KernelInterface: "t2s7",
		LegacyOwner: "old-legacy-token",
	}
	if err := store.SetBridge("proxy", proxy.ID, bridge); err != nil {
		t.Fatal(err)
	}
	registrar := &fakeBridgeRegistrar{
		occupied: map[int]bool{7: true}, descriptions: map[int]string{7: "old-legacy-token"},
	}
	manager := NewBridgeManager(store, registrar, func() bool { return true })
	manager.SetRuntimeActive(func() bool { return true })
	if err := manager.Reconcile(context.Background(), store.ListBridges()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetProxy(proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectedOwner := FormatBridgeDescription(proxy.Name, "proxy", proxy.ID)
	if registrar.descriptions[7] != expectedOwner {
		t.Fatalf("Proxy7 owner = %q, want %q", registrar.descriptions[7], expectedOwner)
	}
	if got.Bridge.LegacyOwner != "" {
		t.Fatalf("LegacyOwner was not cleared: %q", got.Bridge.LegacyOwner)
	}

	// Rename after migration: updates display label while preserving ownership token
	if _, err := store.UpdateProxy(proxy.ID, UpdateProxyInput{
		Manual: &ManualProxyInput{
			Name: "Renamed Card", Protocol: "anytls", Server: "proxy.example", Port: 443,
			EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
		},
		EnginePreference: EngineMihomo,
		Enabled:          true,
		RoutingEngine:    EngineMihomo,
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(context.Background(), store.ListBridges()); err != nil {
		t.Fatal(err)
	}
	expectedAfterRename := FormatBridgeDescription("Renamed Card", "proxy", proxy.ID)
	if registrar.descriptions[7] != expectedAfterRename {
		t.Fatalf("Proxy7 owner after rename = %q, want %q", registrar.descriptions[7], expectedAfterRename)
	}
}

func TestBridgeManager_Reconcile_ReallocatesOutOfRangePorts(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "native.json")
	store, err := NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := store.CreateProxy("vless://11111111-1111-1111-1111-111111111111@native.example:443?type=xhttp#LegacyCard", EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxy := nodes[0]
	// Set initial bridge with valid port
	initialBridge := ProxyBridge{
		ListenPort: 12050, ProxyIndex: 5,
		ProxyInterface: "Proxy5", KernelInterface: "t2s5",
	}
	if err := store.SetBridge("proxy", proxy.ID, initialBridge); err != nil {
		t.Fatal(err)
	}

	// Directly modify store data to simulate an upgraded pre-existing native.json with port 12500
	store.mu.Lock()
	store.data.Proxies[0].Bridge.ListenPort = 12500
	store.mu.Unlock()

	canonicalOwner := BridgeOwnershipDescription("proxy", proxy.ID)
	registrar := &fakeBridgeRegistrar{
		occupied:     map[int]bool{5: true},
		descriptions: map[int]string{5: canonicalOwner},
	}
	manager := NewBridgeManager(store, registrar, func() bool { return true })
	manager.SetRuntimeActive(func() bool { return true })

	if err := manager.Reconcile(context.Background(), store.ListBridges()); err != nil {
		t.Fatalf("reconcile failed to reallocate out-of-range port: %v", err)
	}
	got, err := store.GetProxy(proxy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Bridge.ListenPort < bridgePortBase || got.Bridge.ListenPort > bridgePortMax {
		t.Fatalf("reallocated port %d outside %d-%d", got.Bridge.ListenPort, bridgePortBase, bridgePortMax)
	}
	if len(registrar.ensured) == 0 {
		t.Fatal("NDMS proxy was not ensured")
	}
	if registrar.ensured[len(registrar.ensured)-1].port != got.Bridge.ListenPort {
		t.Fatalf("NDMS ensured port %d, want %d", registrar.ensured[len(registrar.ensured)-1].port, got.Bridge.ListenPort)
	}
}


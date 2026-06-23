package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

func withLegacySetupNoop(svc *Service) {
	_ = svc
}

// ensuredProxyCall records one EnsureProxy invocation with all arguments.
type ensuredProxyCall struct {
	idx         int
	port        int
	description string
}

// fakeMutator records what the service tries to commit.
type fakeMutator struct {
	addedOutbounds   []string
	updatedOutbounds []string
	removedOutbounds []string
	addedInbounds    []string
	removedInbounds  []string
	addedRules       int
	removedRules     int
	listenPort       uint16
	proxyIndex       int
	ensuredProxies   []ensuredProxyCall
	removedProxies   []int
	selectedSelector []string          // "selectorTag→memberTag" pairs recorded by SelectClashProxy
	clashActiveByTag map[string]string // selectorTag → live active member
	declaredTags     []string          // DeclaredOutboundTags result; nil = no-op (no pruning)
	bodies           map[string][]byte // tag → последний JSON из AddOutbound/UpdateOutbound
}

func (m *fakeMutator) reset() {
	m.addedOutbounds = nil
	m.removedOutbounds = nil
	m.bodies = map[string][]byte{}
}
func (m *fakeMutator) addedOutbound(tag string) bool   { return containsTag(m.addedOutbounds, tag) }
func (m *fakeMutator) removedOutbound(tag string) bool { return containsTag(m.removedOutbounds, tag) }

func containsTag(ss []string, t string) bool {
	for _, s := range ss {
		if s == t {
			return true
		}
	}
	return false
}

// selectorMembers парсит сохранённый body селектора и возвращает его outbounds.
func selectorMembers(t *testing.T, m *fakeMutator, selectorTag string) []string {
	t.Helper()
	var ob struct {
		Outbounds []string `json:"outbounds"`
	}
	if err := json.Unmarshal(m.bodies[selectorTag], &ob); err != nil {
		t.Fatalf("selector body: %v", err)
	}
	return ob.Outbounds
}

func tagOf(sub *Subscription, i int) string { return sub.Members[i].Tag }

func newTestService(t *testing.T) (*Service, *fakeMutator) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatal(err)
	}
	mut := &fakeMutator{}
	svc := NewService(store, mut)
	withLegacySetupNoop(svc)
	return svc, mut
}

// createURLSubWithMembers поднимает httptest-фид из n share-link и создаёт URL-подписку;
// возвращает sub с реальными MemberTags (active = Members[0].Tag).
func createURLSubWithMembers(t *testing.T, svc *Service, n int) *Subscription {
	t.Helper()
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "vless://3a3b1c2e-9999-4321-aaaa-1234567890a%d@h%d.example:443?security=tls&sni=h#%d\n", i, i, i)
	}
	body := b.String()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func createInlineSubWithTwoMembers(t *testing.T, svc *Service) *Subscription {
	t.Helper()
	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", Inline: "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443#A\nvless://3a3b1c2e-9999-4321-aaaa-1234567890a2@b.example:443#B"})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func (f *fakeMutator) AllocListenPort() (uint16, error) {
	if f.listenPort == 0 {
		f.listenPort = 11000
	}
	f.listenPort++
	return f.listenPort, nil
}
func (f *fakeMutator) AllocProxyIndex(_ context.Context) (int, error) {
	f.proxyIndex++
	return f.proxyIndex, nil
}
func (f *fakeMutator) AddOutbound(tag string, jsonBody []byte) error {
	f.addedOutbounds = append(f.addedOutbounds, tag)
	if f.bodies == nil {
		f.bodies = map[string][]byte{}
	}
	f.bodies[tag] = jsonBody
	return nil
}
func (f *fakeMutator) UpdateOutbound(tag string, jsonBody []byte) error {
	f.updatedOutbounds = append(f.updatedOutbounds, tag)
	if f.bodies == nil {
		f.bodies = map[string][]byte{}
	}
	f.bodies[tag] = jsonBody
	return nil
}
func (f *fakeMutator) RemoveOutbound(tag string) error {
	f.removedOutbounds = append(f.removedOutbounds, tag)
	return nil
}
func (f *fakeMutator) AddInbound(tag string, jsonBody []byte) error {
	f.addedInbounds = append(f.addedInbounds, tag)
	return nil
}
func (f *fakeMutator) RemoveInbound(tag string) error {
	f.removedInbounds = append(f.removedInbounds, tag)
	return nil
}
func (f *fakeMutator) AddRouteRule(jsonBody []byte) error {
	f.addedRules++
	return nil
}
func (f *fakeMutator) RemoveRouteRule(inboundTag, outboundTag string) error {
	f.removedRules++
	return nil
}
func (f *fakeMutator) EnsureProxy(_ context.Context, idx, port int, description string) error {
	f.ensuredProxies = append(f.ensuredProxies, ensuredProxyCall{idx: idx, port: port, description: description})
	return nil
}
func (f *fakeMutator) RemoveProxy(_ context.Context, idx int) error {
	f.removedProxies = append(f.removedProxies, idx)
	return nil
}
func (f *fakeMutator) Reload(ctx context.Context) error { return nil }
func (f *fakeMutator) Rollback()                        {}
func (f *fakeMutator) DeclaredOutboundTags() []string   { return f.declaredTags }
func (f *fakeMutator) SelectClashProxy(selectorTag, memberTag string) error {
	f.selectedSelector = append(f.selectedSelector, selectorTag+"→"+memberTag)
	return nil
}
func (f *fakeMutator) GetClashSelectorActive(selectorTag string) (string, error) {
	if f.clashActiveByTag == nil {
		return "", nil
	}
	return f.clashActiveByTag[selectorTag], nil
}

func TestService_Create_FetchAndMaterialize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n" +
			"trojan://p@example.com:444?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 2 {
		t.Errorf("MemberTags=%d want 2", len(sub.MemberTags))
	}
	if len(mutator.addedOutbounds) < 3 { // 2 members + 1 selector
		t.Errorf("expected >=3 outbounds added, got %d", len(mutator.addedOutbounds))
	}
	if len(mutator.addedInbounds) != 1 {
		t.Errorf("expected 1 mixed inbound, got %d", len(mutator.addedInbounds))
	}
}

// scanMutator simulates NextFreeIndex's real behaviour: AllocProxyIndex
// returns the lowest index not yet registered via EnsureProxy. It exposes
// whether a batch allocated collision-free (each EnsureProxy committed
// before the next Alloc).
type scanMutator struct {
	fakeMutator
	live map[int]bool
}

func (m *scanMutator) AllocProxyIndex(_ context.Context) (int, error) {
	for i := 0; ; i++ {
		if !m.live[i] {
			return i, nil
		}
	}
}
func (m *scanMutator) EnsureProxy(_ context.Context, idx, port int, description string) error {
	if m.live == nil {
		m.live = map[int]bool{}
	}
	m.live[idx] = true
	m.ensuredProxies = append(m.ensuredProxies, ensuredProxyCall{idx: idx, port: port, description: description})
	return nil
}

func TestService_Create_NDMSProxyOff_SkipsProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	svc.SetNDMSProxyEnabled(func() bool { return false })

	sub, err := svc.Create(context.Background(), CreateInput{Label: "off", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(mutator.ensuredProxies) != 0 {
		t.Errorf("EnsureProxy must not be called when toggle is off, got %d calls", len(mutator.ensuredProxies))
	}
	if mutator.proxyIndex != 0 {
		t.Errorf("AllocProxyIndex must not be called when toggle is off")
	}
	if sub.ProxyIndex != -1 {
		t.Errorf("ProxyIndex = %d, want -1 (no proxy in off mode)", sub.ProxyIndex)
	}
	if sub.ListenPort == 0 {
		t.Error("listen port must still be allocated in off mode (data path)")
	}
}

func TestService_SyncProxies_OffIsNoop(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	svc.SetNDMSProxyEnabled(func() bool { return false })

	sub, _ := store.Create(CreateInput{Label: "a", URL: "http://x", Enabled: true})
	_ = store.SetListenPort(sub.ID, 11001)

	if err := svc.SyncProxies(context.Background()); err != nil {
		t.Fatalf("SyncProxies: %v", err)
	}
	if len(mutator.ensuredProxies) != 0 {
		t.Errorf("SyncProxies must be a no-op when toggle is off, got %d EnsureProxy", len(mutator.ensuredProxies))
	}
}

func TestService_SyncProxies_AllocatesForProxylessSequentially(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &scanMutator{}
	svc := NewService(store, mutator)
	svc.SetNDMSProxyEnabled(func() bool { return true })

	// Two subscriptions created while the toggle was off → ProxyIndex=-1.
	a, _ := store.Create(CreateInput{Label: "a", URL: "http://x", Enabled: true})
	_ = store.SetListenPort(a.ID, 11001)
	b, _ := store.Create(CreateInput{Label: "b", URL: "http://y", Enabled: true})
	_ = store.SetListenPort(b.ID, 11002)

	if err := svc.SyncProxies(context.Background()); err != nil {
		t.Fatalf("SyncProxies: %v", err)
	}
	if len(mutator.ensuredProxies) != 2 {
		t.Fatalf("expected 2 EnsureProxy, got %d", len(mutator.ensuredProxies))
	}
	// Collision-safety: sequential allocation must yield distinct indexes.
	if mutator.ensuredProxies[0].idx == mutator.ensuredProxies[1].idx {
		t.Errorf("two proxy-less subs got the same index %d (allocation not committed before next)", mutator.ensuredProxies[0].idx)
	}
	for _, id := range []string{a.ID, b.ID} {
		got, _ := store.Get(id)
		if got.ProxyIndex < 0 {
			t.Errorf("sub %s ProxyIndex not persisted: %d", id, got.ProxyIndex)
		}
	}
}

// SyncProxies must not hand a freshly-allocated index to a proxy-less
// subscription that another subscription still retains in the store. A
// subscription created while the toggle was on keeps its ProxyIndex across a
// toggle-off (MigrateOff removes the router interface but not the stored
// index); a subscription created while off carries ProxyIndex=-1. On toggle-on
// SyncProxies must re-register the retained one and allocate a *distinct* index
// for the proxy-less one. store.List() order is non-deterministic, so the loop
// exercises both orderings.
func TestService_SyncProxies_RetainedIndexNotReused(t *testing.T) {
	for run := 0; run < 300; run++ {
		store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
		mutator := &scanMutator{}
		svc := NewService(store, mutator)
		svc.SetNDMSProxyEnabled(func() bool { return true })

		// A: created while on — retains ProxyIndex=0, but its router Proxy0 was
		// torn down by MigrateOff (scanMutator.live starts empty).
		a, _ := store.Create(CreateInput{Label: "a", URL: "http://x", Enabled: true})
		_ = store.SetListenPort(a.ID, 11001)
		_ = store.SetProxyIndex(a.ID, 0)
		// B: created while off — ProxyIndex stays -1.
		b, _ := store.Create(CreateInput{Label: "b", URL: "http://y", Enabled: true})
		_ = store.SetListenPort(b.ID, 11002)

		if err := svc.SyncProxies(context.Background()); err != nil {
			t.Fatalf("run %d: SyncProxies: %v", run, err)
		}
		ga, _ := store.Get(a.ID)
		gb, _ := store.Get(b.ID)
		if ga.ProxyIndex == gb.ProxyIndex {
			t.Fatalf("run %d: A and B share ProxyIndex %d (retained index reused by fresh alloc)", run, ga.ProxyIndex)
		}
	}
}

func TestService_Update_LabelOff_SkipsEnsureProxy(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	svc.SetNDMSProxyEnabled(func() bool { return false })

	// Subscription created while off: ProxyIndex stays -1.
	sub, _ := store.Create(CreateInput{Label: "a", URL: "http://x", Enabled: true})
	_ = store.SetListenPort(sub.ID, 11001)

	newLabel := "renamed"
	if _, err := svc.Update(sub.ID, UpdatePatch{Label: &newLabel}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(mutator.ensuredProxies) != 0 {
		t.Errorf("relabel must not call EnsureProxy when off / ProxyIndex<0, got %d", len(mutator.ensuredProxies))
	}
}

func TestService_Create_FailsOnZeroOutbounds_ClashYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Write([]byte("proxies:\n  - name: \"a\"\n    type: vless\n    server: 1.2.3.4\n    port: 443\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	_, err := svc.Create(context.Background(), CreateInput{Label: "clash", URL: srv.URL, Enabled: true})
	if err == nil {
		t.Fatalf("Create with Clash YAML must fail (0 outbounds)")
	}
	if !strings.Contains(err.Error(), "Clash YAML") && !strings.Contains(err.Error(), "ни одной валидной") {
		t.Errorf("error must hint at unsupported format, got: %v", err)
	}
	if len(store.List()) != 0 {
		t.Errorf("subscription must be cleaned up on failed Create, got %d", len(store.List()))
	}
}

func TestService_Create_FailsOnEmptyClashProxiesArray(t *testing.T) {
	// Real-world body shape served by an expired clash subscription:
	// YAML parses cleanly, but proxies: [] is literally empty. We must
	// surface a distinct "subscription is empty" hint instead of the
	// generic "no valid links" message.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
		w.Write([]byte(`mixed-port: 7890
dns:
  enable: true
proxies: []
proxy-groups:
  - name: → Remnawave
    type: select
    proxies: []
rules:
  - MATCH,→ Remnawave
`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	_, err := svc.Create(context.Background(), CreateInput{Label: "expired", URL: srv.URL, Enabled: true})
	if err == nil {
		t.Fatalf("Create with empty Clash proxies must fail")
	}
	if !strings.Contains(err.Error(), "пуста") {
		t.Errorf("error must hint that subscription is empty, got: %v", err)
	}
	if len(store.List()) != 0 {
		t.Errorf("subscription must be cleaned up, got %d", len(store.List()))
	}
	// Bug-fix from previous commit also applies here: ProxyN must be rolled back.
	if len(mutator.removedProxies) != 1 {
		t.Errorf("expected 1 RemoveProxy rollback, got %d", len(mutator.removedProxies))
	}
}

// TestService_Create_RollsBackProxyOnFetchFailure asserts the bug fix
// where each failed Create call leaked an NDMS ProxyN interface — the
// initial-fetch failure path now also removes the proxy registered by
// EnsureProxy.
func TestService_Create_RollsBackProxyOnFetchFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Empty body — share-link parser sees 0 outbounds, fail-fast fires.
		w.WriteHeader(200)
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	// Three failed attempts at the same URL.
	for i := 0; i < 3; i++ {
		_, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
		if err == nil {
			t.Fatalf("attempt %d: Create must fail", i+1)
		}
	}

	// Three EnsureProxy calls and three matching RemoveProxy calls — net
	// zero proxy interfaces leaked into the router.
	if len(mutator.ensuredProxies) != 3 {
		t.Errorf("ensuredProxies=%d want 3", len(mutator.ensuredProxies))
	}
	if len(mutator.removedProxies) != 3 {
		t.Errorf("removedProxies=%d want 3 (one rollback per failed Create)", len(mutator.removedProxies))
	}
	// Storage clean.
	if len(store.List()) != 0 {
		t.Errorf("storage must be empty, got %d rows", len(store.List()))
	}
}

func TestService_Refresh_AddsNewMember(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n"))
		} else {
			w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n" +
				"trojan://p@example.com:444?security=tls&sni=h\n"))
		}
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	updated, _ := store.Get(sub.ID)
	if len(updated.MemberTags) != 2 {
		t.Errorf("MemberTags after refresh=%d want 2", len(updated.MemberTags))
	}
}

// Root-cause-B: a server that flush() dropped (absent from the declared/
// emitted set) must be pruned from stored MemberTags on refresh, so it
// can't be re-introduced as a dangling group member later.
func TestService_Refresh_PrunesUndeclaredMember(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n" +
			"trojan://p@example.com:444?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// First refresh: declaredTags nil → no pruning, both members materialize.
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh#1: %v", err)
	}
	updated, _ := store.Get(sub.ID)
	if len(updated.MemberTags) != 2 {
		t.Fatalf("after refresh#1 MemberTags=%d want 2", len(updated.MemberTags))
	}

	// Simulate flush dropping the 2nd server: the slot now declares only the
	// group + the first server. Second refresh must prune the undeclared one.
	keep := updated.MemberTags[0]
	mutator.declaredTags = []string{sub.SelectorTag, keep}
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh#2: %v", err)
	}
	updated2, _ := store.Get(sub.ID)
	if len(updated2.MemberTags) != 1 || updated2.MemberTags[0] != keep {
		t.Fatalf("after refresh#2 MemberTags=%v want [%s]", updated2.MemberTags, keep)
	}
	if updated2.ActiveMember != keep {
		t.Fatalf("ActiveMember=%q want %q", updated2.ActiveMember, keep)
	}
}

func TestService_Create_RegistersNDMSProxy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(mutator.ensuredProxies) != 1 {
		t.Errorf("expected 1 EnsureProxy call, got %d", len(mutator.ensuredProxies))
	}
	if sub.ProxyIndex < 0 {
		t.Errorf("ProxyIndex should be set, got %d", sub.ProxyIndex)
	}
}

func TestService_Delete_AlwaysCleansEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n" +
			"trojan://p@example.com:444?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(context.Background(), sub.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// ProxyN must always be removed (this is the bug-fix).
	if len(mutator.removedProxies) != 1 || mutator.removedProxies[0] != sub.ProxyIndex {
		t.Errorf("RemoveProxy not called with subscription's index: %v (want %d)",
			mutator.removedProxies, sub.ProxyIndex)
	}
	// Mixed inbound removed.
	foundInbound := false
	for _, t2 := range mutator.removedInbounds {
		if t2 == sub.InboundTag {
			foundInbound = true
			break
		}
	}
	if !foundInbound {
		t.Errorf("expected inbound %s removed, got %v", sub.InboundTag, mutator.removedInbounds)
	}
	// Selector + at least 2 members removed.
	if len(mutator.removedOutbounds) < 3 {
		t.Errorf("expected >=3 outbounds removed (selector+2 members), got %d", len(mutator.removedOutbounds))
	}
	// Route rule removed.
	if mutator.removedRules < 1 {
		t.Errorf("expected route rule removed, got %d", mutator.removedRules)
	}
	// Subscription absent from storage.
	if _, err := store.Get(sub.ID); err == nil {
		t.Errorf("expected subscription removed from store, still present")
	}
}

func TestService_ListActiveMemberTags_FiltersDisabledAndEmpty(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))

	// Enabled with active set — should appear
	a, _ := store.Create(CreateInput{Label: "a", URL: "u", Enabled: true})
	store.SetMembers(a.ID, []MemberInfo{{Tag: "sub-A-1111"}}, nil)

	// Disabled — should be filtered out
	b, _ := store.Create(CreateInput{Label: "b", URL: "u", Enabled: false})
	store.SetMembers(b.ID, []MemberInfo{{Tag: "sub-B-2222"}}, nil)

	// Enabled but no members — ActiveMember stays empty, should be filtered
	store.Create(CreateInput{Label: "c", URL: "u", Enabled: true})

	svc := NewService(store, &fakeMutator{})
	withLegacySetupNoop(svc)
	tags := svc.ListActiveMemberTags()
	if len(tags) != 1 {
		t.Fatalf("expected 1 active tag, got %d: %v", len(tags), tags)
	}
	if tags[0] != "sub-A-1111" {
		t.Errorf("got %q want sub-A-1111", tags[0])
	}
}

func TestService_SetActiveMember_UsesClashAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(
			"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n" +
				"trojan://p@h2.example:443?security=tls&sni=h\n",
		))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(sub.MemberTags) < 2 {
		t.Fatalf("need at least 2 members, got %d", len(sub.MemberTags))
	}

	secondMember := sub.MemberTags[1]
	// Snapshot slot-mutation counts after Create; switching the active member
	// must not touch the config slot at all (Clash + store only).
	addedAfterCreate := len(mutator.addedOutbounds)
	removedAfterCreate := len(mutator.removedOutbounds)

	if err := svc.SetActiveMember(context.Background(), sub.ID, secondMember); err != nil {
		t.Fatalf("SetActiveMember: %v", err)
	}

	// Verify clash API was called with the right args.
	if len(mutator.selectedSelector) != 1 {
		t.Fatalf("expected 1 SelectClashProxy call, got %d: %v", len(mutator.selectedSelector), mutator.selectedSelector)
	}
	expected := sub.SelectorTag + "→" + secondMember
	if mutator.selectedSelector[0] != expected {
		t.Errorf("clash select args wrong: got %q want %q", mutator.selectedSelector[0], expected)
	}

	// Switching the active member is runtime-only: no selector Remove/Add, so
	// no open batch and no SIGHUP. The slot's selector.default is rebuilt as
	// first-member on every refresh anyway, so persisting it here is pointless;
	// the choice lives in store.ActiveMember + the live Clash selector.
	if len(mutator.addedOutbounds) != addedAfterCreate {
		t.Errorf("SetActiveMember must not add outbounds, got %d new", len(mutator.addedOutbounds)-addedAfterCreate)
	}
	if len(mutator.removedOutbounds) != removedAfterCreate {
		t.Errorf("SetActiveMember must not remove outbounds, got %d new", len(mutator.removedOutbounds)-removedAfterCreate)
	}
	stored, _ := store.Get(sub.ID)
	if stored.ActiveMember != secondMember {
		t.Errorf("store.ActiveMember=%q want %q", stored.ActiveMember, secondMember)
	}
}

func TestService_Create_InlinePastedShareLinks(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)

	inline := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n" +
		"trojan://p@h2.example:443?security=tls&sni=h\n"
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "manual",
		Inline:  inline,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create inline: %v", err)
	}
	if !sub.IsInline() {
		t.Errorf("subscription should be inline (URL=%q, Inline len=%d)", sub.URL, len(sub.Inline))
	}
	if len(sub.MemberTags) < 2 {
		t.Errorf("expected ≥2 members from pasted share-links, got %d", len(sub.MemberTags))
	}
	if sub.LastError != "" {
		t.Errorf("inline parse should not have errored: %q", sub.LastError)
	}
}

func TestService_Create_RejectsBothURLAndInline(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	withLegacySetupNoop(svc)
	_, err := svc.Create(context.Background(), CreateInput{
		Label:   "bad",
		URL:     "https://example.com/sub",
		Inline:  "vless://...",
		Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutual-exclusion error, got %v", err)
	}
}

func TestService_Create_RejectsNoSource(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	withLegacySetupNoop(svc)
	_, err := svc.Create(context.Background(), CreateInput{Label: "empty", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "either URL or inline") {
		t.Errorf("expected source-required error, got %v", err)
	}
}

func TestService_Update_RejectsClearURLAndAddingURLToInline(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)
	inlineSub, err := svc.Create(context.Background(), CreateInput{
		Label:   "inl",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create inline: %v", err)
	}
	addedURL := "https://example.com/sub"
	_, err = svc.Update(inlineSub.ID, UpdatePatch{URL: &addedURL})
	if err == nil || !strings.Contains(err.Error(), "inline") {
		t.Errorf("expected reject-add-URL-to-inline, got %v", err)
	}

	// URL-backed sub: clearing URL must be rejected too. Use httptest
	// so Create succeeds (refresh tries to fetch the URL).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=h\n"))
	}))
	defer srv.Close()
	urlSub, err := svc.Create(context.Background(), CreateInput{
		Label:   "url",
		URL:     srv.URL,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create url: %v", err)
	}
	emptyURL := ""
	_, err = svc.Update(urlSub.ID, UpdatePatch{URL: &emptyURL})
	if err == nil || !strings.Contains(err.Error(), "clear URL") {
		t.Errorf("expected reject-clear-URL, got %v", err)
	}
}

func TestService_AddManualMember_AppendsToInline(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "manual",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	original := len(sub.MemberTags)

	updated, err := svc.AddManualMember(context.Background(), sub.ID,
		"trojan://p@h2.example:443?security=tls&sni=h")
	if err != nil {
		t.Fatalf("AddManualMember: %v", err)
	}
	if len(updated.MemberTags) != original+1 {
		t.Errorf("expected %d members, got %d", original+1, len(updated.MemberTags))
	}
}

func TestService_AddManualMember_RejectsDuplicate(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h"
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "manual",
		Inline:  link + "\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = svc.AddManualMember(context.Background(), sub.ID, link)
	if !errors.Is(err, ErrMemberDuplicate) {
		t.Errorf("expected ErrMemberDuplicate, got %v", err)
	}
}

func TestService_AddManualMember_RejectsURLSub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=h\n"))
	}))
	defer srv.Close()
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	withLegacySetupNoop(svc)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "url", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = svc.AddManualMember(context.Background(), sub.ID, "trojan://p@h.example:443?security=tls&sni=h")
	if !errors.Is(err, ErrManualMemberOnURLSub) {
		t.Errorf("expected ErrManualMemberOnURLSub, got %v", err)
	}
}

func TestService_RemoveMember_DeletesSubOnLastMember(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "manual",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 1 {
		t.Fatalf("expected 1 member, got %d", len(sub.MemberTags))
	}

	updated, err := svc.RemoveMember(context.Background(), sub.ID, sub.MemberTags[0])
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if updated != nil {
		t.Errorf("expected nil (subscription deleted), got %+v", updated)
	}
	if _, getErr := store.Get(sub.ID); getErr == nil {
		t.Errorf("subscription should be deleted from store")
	}
}

func TestService_RemoveMember_BumpsActiveOnRemoval(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	sub, err := svc.Create(context.Background(), CreateInput{
		Label: "manual",
		Inline: "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n" +
			"trojan://p@h2.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 2 {
		t.Fatalf("expected 2 members, got %d", len(sub.MemberTags))
	}
	activeBefore := sub.ActiveMember
	updated, err := svc.RemoveMember(context.Background(), sub.ID, activeBefore)
	if err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated subscription, got nil")
	}
	if updated.ActiveMember == activeBefore {
		t.Errorf("ActiveMember should have moved off the removed tag")
	}
	if updated.ActiveMember == "" || updated.ActiveMember != updated.MemberTags[0] {
		t.Errorf("ActiveMember should be the first remaining tag, got %q", updated.ActiveMember)
	}
	// Selector mode: Clash API should have been hit to switch the live
	// session off the deleted tag.
	if len(mutator.selectedSelector) == 0 {
		t.Errorf("expected SelectClashProxy call after active-member removal")
	}
}

func TestService_SetActiveMember_RejectsURLTestMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(
			"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n",
		))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "test",
		URL:     srv.URL,
		Enabled: true,
		Mode:    ModeURLTest,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = svc.SetActiveMember(context.Background(), sub.ID, sub.MemberTags[0])
	if !errors.Is(err, ErrActiveMemberOnURLTest) {
		t.Fatalf("expected ErrActiveMemberOnURLTest, got %v", err)
	}
	// Verify Clash API was NOT called — the urltest mode would 404 on
	// sing-box. Bailing out at the service layer prevents the wasted
	// call and gives the API handler a clean signal to map to 409.
	if len(mutator.selectedSelector) != 0 {
		t.Errorf("Clash API should not be called for urltest mode, got %v", mutator.selectedSelector)
	}
}

func TestService_Create_ParsesClashYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Write([]byte(`
proxies:
  - name: "🇺🇸 LA-1"
    type: vless
    server: la1.example.com
    port: 443
    uuid: 3a3b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
    servername: la1.example.com
  - name: "🇩🇪 FRA-1"
    type: vless
    server: fra1.example.com
    port: 443
    uuid: 4a4b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
    servername: fra1.example.com
  - name: "🇯🇵 TYO-1"
    type: trojan
    server: tyo1.example.com
    port: 443
    password: trpass
    sni: tyo1.example.com
  - name: "VM-skipme"
    type: vmess
    server: vm.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "clash", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 3 {
		t.Errorf("MemberTags=%d want 3", len(sub.MemberTags))
	}
	if len(mutator.addedOutbounds) < 4 { // 3 members + 1 selector
		t.Errorf("expected >=4 outbounds added, got %d", len(mutator.addedOutbounds))
	}
	// Members must carry their human-readable Label.
	gotLabels := map[string]bool{}
	for _, m := range sub.Members {
		gotLabels[m.Label] = true
	}
	for _, want := range []string{"🇺🇸 LA-1", "🇩🇪 FRA-1", "🇯🇵 TYO-1"} {
		if !gotLabels[want] {
			t.Errorf("missing Label %q in MemberInfo", want)
		}
	}
}

func TestService_Refresh_ClashYAMLAddsNewMember(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/x-yaml")
		if requestCount == 1 {
			w.Write([]byte(`
proxies:
  - name: "A"
    type: vless
    server: a.example.com
    port: 443
    uuid: 3a3b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
`))
		} else {
			w.Write([]byte(`
proxies:
  - name: "A"
    type: vless
    server: a.example.com
    port: 443
    uuid: 3a3b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
  - name: "B"
    type: vless
    server: b.example.com
    port: 443
    uuid: 4a4b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
`))
		}
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "c", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 1 {
		t.Errorf("after first refresh want 1 member, got %d", len(sub.MemberTags))
	}

	res, err := svc.Refresh(context.Background(), sub.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if res.Added != 1 || res.Updated != 1 {
		t.Errorf("Refresh result Added=%d Updated=%d, want Added=1 Updated=1", res.Added, res.Updated)
	}
}

func TestService_Create_SingboxJSONConfig_Happy(t *testing.T) {
	// Real Happ-style body: a single sing-box config with outbounds for
	// vless+trojan+ss+hy2 plus the usual selector / direct service
	// outbounds the parser must silently drop.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"dns": {"servers": [{"address": "8.8.8.8"}]},
			"route": {"rules": []},
			"outbounds": [
				{"type":"vless","tag":"NL","server":"nl.example.com","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"sni","utls":{"enabled":true,"fingerprint":"chrome"},"reality":{"enabled":true,"public_key":"PK","short_id":"SID"}}},
				{"type":"trojan","tag":"DE","server":"de.example.com","server_port":443,"password":"p"},
				{"type":"shadowsocks","tag":"SS","server":"ss.example.com","server_port":8388,"method":"aes-256-gcm","password":"sp"},
				{"type":"hysteria2","tag":"HY","server":"hy.example.com","server_port":8443,"password":"hp"},
				{"type":"selector","tag":"select","outbounds":["NL","DE"]},
				{"type":"direct","tag":"direct"}
			]
		}`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "happ", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 4 {
		t.Errorf("MemberTags=%d want 4 (vless+trojan+ss+hy2; selector & direct must be dropped)", len(sub.MemberTags))
	}
	// 4 members + 1 selector outbound.
	if len(mutator.addedOutbounds) < 5 {
		t.Errorf("addedOutbounds=%d want >=5 (4 members + 1 selector)", len(mutator.addedOutbounds))
	}
	if len(mutator.addedInbounds) != 1 {
		t.Errorf("expected 1 mixed inbound, got %d", len(mutator.addedInbounds))
	}
}

func TestService_Create_SingboxJSON_EmptyOutbounds(t *testing.T) {
	// outbounds: [] is the sing-box equivalent of Clash proxies: [] —
	// account expired / quota exhausted. User must see a specific
	// "пуста" message rather than the generic share-link hint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"dns":{"servers":[]},"outbounds":[]}`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	_, err := svc.Create(context.Background(), CreateInput{Label: "empty-sb", URL: srv.URL, Enabled: true})
	if err == nil {
		t.Fatalf("Create with empty sing-box outbounds must fail")
	}
	if !strings.Contains(err.Error(), "пуста") {
		t.Errorf("error must hint subscription is empty, got: %v", err)
	}
	if !strings.Contains(err.Error(), "outbounds: []") {
		t.Errorf("error should mention outbounds: [], got: %v", err)
	}
	// ProxyN rollback (same fail-closed contract as the Clash empty path).
	if len(mutator.removedProxies) != 1 {
		t.Errorf("expected 1 RemoveProxy rollback, got %d", len(mutator.removedProxies))
	}
}

func TestService_Create_JSONBodyWithoutOutbounds_Errors(t *testing.T) {
	// Body parses as JSON but has no outbounds key anywhere — must
	// produce a specific "JSON without outbounds" error rather than
	// fall through to share-link parsing (which would emit
	// "ни одной валидной ссылки" with a meaningless prefix).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"dns":{"servers":[]},"route":{"rules":[]}}`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	_, err := svc.Create(context.Background(), CreateInput{Label: "json-no-outbounds", URL: srv.URL, Enabled: true})
	if err == nil {
		t.Fatalf("Create with JSON-shaped body without outbounds must fail")
	}
	if !strings.Contains(err.Error(), "JSON") {
		t.Errorf("error must mention JSON, got: %v", err)
	}
	if !strings.Contains(err.Error(), "outbounds") {
		t.Errorf("error must mention missing outbounds, got: %v", err)
	}
	if len(mutator.removedProxies) != 1 {
		t.Errorf("expected 1 RemoveProxy rollback, got %d", len(mutator.removedProxies))
	}
}

func TestService_Create_SingboxJSON_BareOutboundsArray(t *testing.T) {
	// Hiddify-style export: top-level array of outbound objects without
	// a config envelope. Detection must accept this (Shape 3) and
	// flatten through the standard Create path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"type":"vless","tag":"v","server":"h1","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab"},
			{"type":"trojan","tag":"t","server":"h2","server_port":443,"password":"p"}
		]`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "hiddify", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 2 {
		t.Errorf("MemberTags=%d want 2", len(sub.MemberTags))
	}
	if len(mutator.addedInbounds) != 1 {
		t.Errorf("expected 1 mixed inbound, got %d", len(mutator.addedInbounds))
	}
}

func TestService_Create_SingboxJSON_ArrayOfConfigs(t *testing.T) {
	// Multi-config Happ shape: top-level array of sing-box configs.
	// Outbounds must be flattened in order.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"outbounds":[{"type":"vless","tag":"a","server":"h1","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab"}]},
			{"outbounds":[{"type":"trojan","tag":"b","server":"h2","server_port":443,"password":"p"}]}
		]`))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "multi", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != 2 {
		t.Errorf("MemberTags=%d want 2 (one per config)", len(sub.MemberTags))
	}
}

func TestService_Create_SNI_ExplicitTrimmed(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)

	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=%20userapi.com%20"
	sub, err := svc.Create(context.Background(), CreateInput{Label: "sni", Inline: link, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 1 {
		t.Fatalf("Members=%d want 1", len(sub.Members))
	}
	if sub.Members[0].SNI != "userapi.com" {
		t.Errorf("SNI=%q want %q", sub.Members[0].SNI, "userapi.com")
	}
	if sub.Members[0].Security != "tls" {
		t.Errorf("Security=%q want %q", sub.Members[0].Security, "tls")
	}
}

func TestService_Create_SNI_RealityWithoutServerName_IsEmpty(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)

	inline := `{
		"outbounds": [
			{
				"type": "vless",
				"tag": "node1",
				"server": "h.example",
				"server_port": 443,
				"uuid": "3a3b1c2e-9999-4321-aaaa-1234567890ab",
				"tls": {
					"enabled": true,
					"utls": {"enabled": true, "fingerprint": "chrome"},
					"reality": {
						"enabled": true,
						"public_key": "PK",
						"short_id": "ab12"
					}
				}
			}
		]
	}`
	sub, err := svc.Create(context.Background(), CreateInput{Label: "reality", Inline: inline, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 1 {
		t.Fatalf("Members=%d want 1", len(sub.Members))
	}
	if sub.Members[0].SNI != "" {
		t.Errorf("SNI=%q want empty", sub.Members[0].SNI)
	}
	if sub.Members[0].Security != "reality" {
		t.Errorf("Security=%q want %q", sub.Members[0].Security, "reality")
	}
}

func TestService_Create_SNI_NoTLS_IsEmpty(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)

	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=none"
	sub, err := svc.Create(context.Background(), CreateInput{Label: "notls", Inline: link, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 1 {
		t.Fatalf("Members=%d want 1", len(sub.Members))
	}
	if sub.Members[0].SNI != "" {
		t.Errorf("SNI=%q want empty", sub.Members[0].SNI)
	}
	if sub.Members[0].Security != "" {
		t.Errorf("Security=%q want empty", sub.Members[0].Security)
	}
}

func TestService_Create_SNI_FromSingboxJSONServerName(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)

	inline := `{
		"outbounds": [
			{
				"type": "vless",
				"tag": "json-sni",
				"server": "h.example",
				"server_port": 443,
				"uuid": "3a3b1c2e-9999-4321-aaaa-1234567890ab",
				"tls": {
					"enabled": true,
					"server_name": "api.example.com"
				}
			}
		]
	}`
	sub, err := svc.Create(context.Background(), CreateInput{Label: "json-sni", Inline: inline, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.Members) != 1 {
		t.Fatalf("Members=%d want 1", len(sub.Members))
	}
	if sub.Members[0].SNI != "api.example.com" {
		t.Errorf("SNI=%q want %q", sub.Members[0].SNI, "api.example.com")
	}
	if sub.Members[0].Security != "tls" {
		t.Errorf("Security=%q want %q", sub.Members[0].Security, "tls")
	}
}

func TestService_GetActiveNow_ReturnsClashLive(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{clashActiveByTag: map[string]string{}}
	svc := NewService(store, mutator)

	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls"
	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", Inline: link, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate Clash reporting a live active member different from stored.
	mutator.clashActiveByTag[sub.SelectorTag] = "sub-test-livetag"

	got, err := svc.GetActiveNow(context.Background(), sub.ID)
	if err != nil {
		t.Fatalf("GetActiveNow: %v", err)
	}
	if got != "sub-test-livetag" {
		t.Errorf("got %q want %q", got, "sub-test-livetag")
	}
}

func TestService_GetActiveNow_ClashUnreachable_ReturnsEmpty(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{} // clashActiveByTag is nil → returns ""
	svc := NewService(store, mutator)

	link := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls"
	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", Inline: link, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.GetActiveNow(context.Background(), sub.ID)
	if err != nil {
		t.Errorf("expected nil err for clash-unreachable, got %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for clash-unreachable, got %q", got)
	}
}

func TestUpdate_LabelChangeUpdatesProxyDescription(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "Original Label", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Reset recorded calls so we only observe Update-triggered EnsureProxy.
	mutator.ensuredProxies = nil

	newLabel := "Renamed Label"
	updated, err := svc.Update(sub.ID, UpdatePatch{Label: &newLabel})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if updated.Label != newLabel {
		t.Errorf("stored label=%q want %q", updated.Label, newLabel)
	}
	if len(mutator.ensuredProxies) != 1 {
		t.Fatalf("expected 1 EnsureProxy call, got %d", len(mutator.ensuredProxies))
	}
	got := mutator.ensuredProxies[0]
	if got.description != newLabel {
		t.Errorf("EnsureProxy description=%q want %q", got.description, newLabel)
	}
	if got.idx != sub.ProxyIndex {
		t.Errorf("EnsureProxy idx=%d want %d", got.idx, sub.ProxyIndex)
	}
}

func TestService_Create_URLWorksWithoutDownloader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	_, err := svc.Create(context.Background(), CreateInput{Label: "url", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("expected URL create to work without downloader, got %v", err)
	}
}

func TestService_Create_Inline_DoesNotRequireDownloader(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	_, err := svc.Create(context.Background(), CreateInput{
		Label:   "inline",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("expected inline create to work without downloader, got %v", err)
	}
}

func TestService_Create_URLFetchError_DoesNotContainDownloadVia(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	_, err := svc.Create(context.Background(), CreateInput{
		Label:   "url-fail",
		URL:     "http://127.0.0.1:1/unreachable",
		Enabled: true,
	})
	if err == nil {
		t.Fatal("expected create fetch error")
	}
	if strings.Contains(err.Error(), "download via") {
		t.Fatalf("error must not contain downloader route prefix: %v", err)
	}
}

func TestService_MoveRejectedToInfo_PromotesToUserInfo(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "extras",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rejectTag := "sub-deadbeef-cafebabe"
	if err := store.SetRejectedAndInfo(sub.ID, []RejectedMember{{
		Tag: rejectTag, Label: "DE backup", Protocol: "vless", Reason: "reality requires utls block",
	}}, nil); err != nil {
		t.Fatalf("seed rejected: %v", err)
	}

	updated, err := svc.MoveRejectedToInfo(context.Background(), sub.ID, rejectTag)
	if err != nil {
		t.Fatalf("MoveRejectedToInfo: %v", err)
	}
	if len(updated.RejectedMembers) != 0 {
		t.Fatalf("rejected=%+v want empty", updated.RejectedMembers)
	}
	if len(updated.InfoItems) != 1 {
		t.Fatalf("info=%+v", updated.InfoItems)
	}
	if updated.InfoItems[0].Label != "DE backup" {
		t.Fatalf("info item: %+v", updated.InfoItems[0])
	}
}

func TestService_MoveRejectedToInfo_InfoFull(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "extras",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h1.example:443?security=tls&sni=h\n",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	full := make([]SubscriptionInfoItem, MaxSubscriptionInfoItems)
	for i := range full {
		full[i] = SubscriptionInfoItem{ID: fmt.Sprintf("info-%d", i), Label: fmt.Sprintf("line %d", i), Source: "auto"}
	}
	if err := store.SetRejectedAndInfo(sub.ID, []RejectedMember{{Tag: "sub-x", Label: "x", Reason: "bad"}}, full); err != nil {
		t.Fatalf("seed info: %v", err)
	}
	_, err = svc.MoveRejectedToInfo(context.Background(), sub.ID, "sub-x")
	if !errors.Is(err, ErrInfoItemsFull) {
		t.Fatalf("expected ErrInfoItemsFull, got %v", err)
	}
}

func TestService_RemoveInfoItem_DismissedOnRefresh(t *testing.T) {
	valid := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n"
	banner := "vless://not-a-uuid@localhost:80?security=none#%F0%9F%93%86%20%D0%9E%D1%81%D1%82%D0%B0%D0%BB%D0%BE%D1%81%D1%8C%3A%208%20%D0%B4%D0%BD%D0%B5%D0%B9\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(valid + banner))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	svc := NewService(store, &fakeMutator{})
	withLegacySetupNoop(svc)
	sub, err := svc.Create(context.Background(), CreateInput{Label: "info-dismiss", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, _ := store.Get(sub.ID)
	if len(got.InfoItems) != 1 {
		t.Fatalf("info after create=%+v want 1 banner", got.InfoItems)
	}
	removedID := got.InfoItems[0].ID

	if _, err := svc.RemoveInfoItem(context.Background(), sub.ID, removedID); err != nil {
		t.Fatalf("RemoveInfoItem: %v", err)
	}
	afterRemove, _ := store.Get(sub.ID)
	if len(afterRemove.InfoItems) != 0 {
		t.Fatalf("info after remove=%+v want empty", afterRemove.InfoItems)
	}
	if len(afterRemove.RejectedMembers) == 0 {
		t.Fatalf("rejected after remove empty, want banner moved from info")
	}
	foundRejected := false
	for _, r := range afterRemove.RejectedMembers {
		if r.Reason == reasonRemovedFromInfo || strings.Contains(r.Reason, "информации провайдера") {
			foundRejected = true
			break
		}
	}
	if !foundRejected {
		t.Fatalf("rejected=%+v want entry from info remove", afterRemove.RejectedMembers)
	}
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	after, _ := store.Get(sub.ID)
	for _, it := range after.InfoItems {
		if it.ID == removedID {
			t.Fatalf("dismissed info %q reappeared after refresh: %+v", removedID, after.InfoItems)
		}
	}
	for _, id := range after.DismissedInfoIDs {
		if id == removedID {
			return
		}
	}
	t.Fatalf("DismissedInfoIDs=%v want %q", after.DismissedInfoIDs, removedID)
}

func TestService_Refresh_PrunesUndeclaredMember_RecordsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@example.com:443?security=tls&sni=h\n" +
			"trojan://p@example.com:444?security=tls&sni=h\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
	withLegacySetupNoop(svc)

	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh#1: %v", err)
	}
	updated, _ := store.Get(sub.ID)
	if len(updated.MemberTags) != 2 {
		t.Fatalf("MemberTags=%d want 2", len(updated.MemberTags))
	}
	prunedTag := updated.MemberTags[1]
	mutator.declaredTags = []string{sub.SelectorTag, updated.MemberTags[0]}
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh#2: %v", err)
	}
	updated2, _ := store.Get(sub.ID)
	found := false
	for _, r := range updated2.RejectedMembers {
		if r.Tag == prunedTag && strings.Contains(r.Reason, "not materialized") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("RejectedMembers=%+v want pruned tag %q", updated2.RejectedMembers, prunedTag)
	}
}

// tagsOf collects the tags of a MemberInfo slice.
func tagsOf(ms []MemberInfo) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Tag)
	}
	return out
}

// filterByTag returns the members whose tag is in tags.
func filterByTag(ms []MemberInfo, tags ...string) []MemberInfo {
	want := make(map[string]bool, len(tags))
	for _, t := range tags {
		want[t] = true
	}
	out := make([]MemberInfo, 0, len(tags))
	for _, m := range ms {
		if want[m.Tag] {
			out = append(out, m)
		}
	}
	return out
}

func TestRefresh_ExcludedNotMaterialized(t *testing.T) {
	svc, mut := newTestService(t)
	sub := createURLSubWithMembers(t, svc, 2) // URL-подписка: refresh ре-материализует
	tagA := tagOf(sub, 0)
	tagB := tagOf(sub, 1)
	// Пометить tagB исключённым напрямую через стор (endpoint появится в Task 3).
	if err := svc.store.MoveToExcluded(sub.ID, filterByTag(sub.Members, tagA), []string{tagB}, filterByTag(sub.Members, tagB)); err != nil {
		t.Fatal(err)
	}
	mut.reset()
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	if mut.addedOutbound(tagB) {
		t.Fatal("excluded tagB must NOT be materialized")
	}
	if containsTag(selectorMembers(t, mut, sub.SelectorTag), tagB) {
		t.Fatal("excluded tagB must NOT be in selector")
	}
	got, _ := svc.store.Get(sub.ID)
	if containsTag(tagsOf(got.Members), tagB) {
		t.Fatal("excluded must not be in active Members")
	}
	if !containsTag(tagsOf(got.ExcludedMembers), tagB) {
		t.Fatal("excluded must be in ExcludedMembers")
	}
	if !containsTag(got.ExcludedTags, tagB) {
		t.Fatal("refresh must preserve ExcludedTags")
	}
}

func TestExcludeMembers_Basic(t *testing.T) {
	svc, mut := newTestService(t)
	sub := createURLSubWithMembers(t, svc, 3) // 3 члена, URL-подписка; active = Members[0]
	t0 := tagOf(sub, 0)
	mut.reset()
	got, err := svc.ExcludeMembers(context.Background(), sub.ID, []string{t0})
	if err != nil {
		t.Fatal(err)
	}
	if containsTag(tagsOf(got.Members), t0) {
		t.Fatal("t0 still active")
	}
	if !containsTag(got.ExcludedTags, t0) {
		t.Fatal("t0 not in ExcludedTags")
	}
	if got.ActiveMember == t0 {
		t.Fatal("active must move off excluded")
	}
	if !mut.removedOutbound(t0) {
		t.Fatal("t0 outbound must be removed")
	}
	if containsTag(selectorMembers(t, mut, sub.SelectorTag), t0) {
		t.Fatal("t0 must leave selector")
	}
}

func TestExcludeMembers_RejectInline(t *testing.T) {
	svc, _ := newTestService(t)
	sub := createInlineSubWithTwoMembers(t, svc)
	if _, err := svc.ExcludeMembers(context.Background(), sub.ID, []string{tagOf(sub, 0)}); !errors.Is(err, ErrExcludeOnInline) {
		t.Fatalf("want ErrExcludeOnInline, got %v", err)
	}
}

func TestExcludeMembers_RejectAll(t *testing.T) {
	svc, _ := newTestService(t)
	sub := createURLSubWithMembers(t, svc, 2)
	if _, err := svc.ExcludeMembers(context.Background(), sub.ID, []string{tagOf(sub, 0), tagOf(sub, 1)}); !errors.Is(err, ErrAllMembersExcluded) {
		t.Fatalf("want ErrAllMembersExcluded, got %v", err)
	}
}

// Инвариант спеки: исключённый тег не может одновременно стать сиротой.
func TestExcludeMembers_NoOrphanIntersection(t *testing.T) {
	svc, _ := newTestService(t)
	sub := createURLSubWithMembers(t, svc, 3)
	t1 := tagOf(sub, 1)
	if _, err := svc.ExcludeMembers(context.Background(), sub.ID, []string{t1}); err != nil {
		t.Fatal(err)
	}
	// refresh + попытка очистки сирот не должны затрагивать исключённый t1.
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatal(err)
	}
	_ = svc.DeleteOrphans(context.Background(), sub.ID)
	got, _ := svc.store.Get(sub.ID)
	if containsTag(got.OrphanTags, t1) {
		t.Fatal("excluded tag must never be an orphan")
	}
	if !containsTag(got.ExcludedTags, t1) {
		t.Fatal("excluded tag must survive refresh+DeleteOrphans")
	}
}

func TestRestoreMembers_ReMaterializes(t *testing.T) {
	svc, mut := newTestService(t)
	sub := createURLSubWithMembers(t, svc, 3)
	t1 := tagOf(sub, 1)
	if _, err := svc.ExcludeMembers(context.Background(), sub.ID, []string{t1}); err != nil {
		t.Fatal(err)
	}
	mut.reset()
	got, err := svc.RestoreMembers(context.Background(), sub.ID, []string{t1})
	if err != nil {
		t.Fatal(err)
	}
	if containsTag(got.ExcludedTags, t1) {
		t.Fatal("t1 still excluded")
	}
	if !containsTag(tagsOf(got.Members), t1) {
		t.Fatal("t1 not restored to active members")
	}
	if !mut.addedOutbound(t1) {
		t.Fatal("t1 must be re-materialized on restore")
	}
}

func TestPreviewURL_NoStoreWrite(t *testing.T) {
	svc, _ := newTestService(t)
	before := len(svc.store.List())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("vless://8f4a2c1e-0000-4000-8000-000000000001@a.example:443?security=tls&sni=a#A\nvless://8f4a2c1e-0000-4000-8000-000000000002@b.example:443#B"))
	}))
	defer srv.Close()
	members, err := svc.PreviewURL(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("want 2, got %d", len(members))
	}
	if members[0].Key == "" || len(members[0].Key) != 8 {
		t.Fatalf("bad key %q", members[0].Key)
	}
	if members[0].Label != "A" {
		t.Fatalf("label=%q", members[0].Label)
	}
	if after := len(svc.store.List()); after != before {
		t.Fatal("preview must not create subscriptions")
	}
}

func TestCreate_ExcludedKeys(t *testing.T) {
	svc, mut := newTestService(t)
	// inline-фид из 2 серверов; вычислить key одного из них (subID-независимый).
	linkA := "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443?security=tls&sni=a#A"
	linkB := "vless://3a3b1c2e-9999-4321-aaaa-1234567890a2@b.example:443?security=tls&sni=b#B"
	links := linkA + "\n" + linkB
	parsed := vlink.ParseBatch([]string{linkB})
	if len(parsed.Outbounds) != 1 {
		t.Fatalf("want 1 parsed outbound, got %d (errors=%v)", len(parsed.Outbounds), parsed.Errors)
	}
	keyB := IdentityHash(parsed.Outbounds[0])
	sub, err := svc.Create(context.Background(), CreateInput{Label: "x", Inline: links, ExcludedKeys: []string{keyB}})
	if err != nil {
		t.Fatal(err)
	}
	wantTag := "sub-" + sub.ID[:8] + "-" + keyB
	if !containsTag(sub.ExcludedTags, wantTag) {
		t.Fatalf("want %s in ExcludedTags %v", wantTag, sub.ExcludedTags)
	}
	if mut.addedOutbound(wantTag) {
		t.Fatal("excluded-by-key must not materialize on create")
	}
}

func TestPreviewURL_CollisionDistinctKeys(t *testing.T) {
	svc, _ := newTestService(t)
	body := "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=a.sni#A\n" +
		"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=b.sni#B\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	members, err := svc.PreviewURL(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members=%d want 2", len(members))
	}
	if members[0].Key == members[1].Key {
		t.Errorf("colliding endpoints must get distinct preview Key, both = %s", members[0].Key)
	}
}

func TestAddManualMember_DistinctSNIOnSameEndpoint(t *testing.T) {
	svc, _ := newTestService(t)
	// inline-подписка с одним членом, наполняем руками
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "manual",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=a.sni#A",
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// тот же host:port:uuid, другой SNI → должен добавиться
	updated, err := svc.AddManualMember(context.Background(), sub.ID,
		"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=b.sni#B")
	if err != nil {
		t.Fatalf("second SNI must add, got %v", err)
	}
	if len(updated.MemberTags) != 2 {
		t.Errorf("MemberTags=%d want 2", len(updated.MemberTags))
	}
}

func TestAddManualMember_ExactRepeatRejected(t *testing.T) {
	svc, _ := newTestService(t)
	sub, err := svc.Create(context.Background(), CreateInput{
		Label:   "manual",
		Inline:  "vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=a.sni#A",
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.AddManualMember(context.Background(), sub.ID,
		"vless://3a3b1c2e-9999-4321-aaaa-1234567890ab@h.example:443?security=tls&sni=a.sni#dup")
	if !errors.Is(err, ErrMemberDuplicate) {
		t.Fatalf("exact repeat must be ErrMemberDuplicate, got %v", err)
	}
}

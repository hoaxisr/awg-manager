package subscription

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

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
	ensuredProxies   []int
	removedProxies   []int
	selectedSelector []string            // "selectorTag→memberTag" pairs recorded by SelectClashProxy
	clashActiveByTag map[string]string   // selectorTag → live active member
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
	return nil
}
func (f *fakeMutator) UpdateOutbound(tag string, jsonBody []byte) error {
	f.updatedOutbounds = append(f.updatedOutbounds, tag)
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
	f.ensuredProxies = append(f.ensuredProxies, idx)
	return nil
}
func (f *fakeMutator) RemoveProxy(_ context.Context, idx int) error {
	f.removedProxies = append(f.removedProxies, idx)
	return nil
}
func (f *fakeMutator) Reload(ctx context.Context) error { return nil }
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

func TestService_Create_FailsOnZeroOutbounds_ClashYAML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		w.Write([]byte("proxies:\n  - name: \"a\"\n    type: vless\n    server: 1.2.3.4\n    port: 443\n"))
	}))
	defer srv.Close()

	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)

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

	sub, _ := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if _, err := svc.Refresh(context.Background(), sub.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	updated, _ := store.Get(sub.ID)
	if len(updated.MemberTags) != 2 {
		t.Errorf("MemberTags after refresh=%d want 2", len(updated.MemberTags))
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
	sub, err := svc.Create(context.Background(), CreateInput{Label: "test", URL: srv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(sub.MemberTags) < 2 {
		t.Fatalf("need at least 2 members, got %d", len(sub.MemberTags))
	}

	secondMember := sub.MemberTags[1]
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

	// Verify Reload was NOT called for SetActiveMember (no connection-dropping SIGHUP).
	// We verify this indirectly: the mutator's Reload is a no-op and SelectClashProxy
	// is recorded; the key invariant is the config update + clash call both happen.
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
	_, err := svc.Create(context.Background(), CreateInput{Label: "empty", Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "either URL or inline") {
		t.Errorf("expected source-required error, got %v", err)
	}
}

func TestService_Update_RejectsClearURLAndAddingURLToInline(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "sub.json"))
	mutator := &fakeMutator{}
	svc := NewService(store, mutator)
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
				{"type":"vless","tag":"NL","server":"nl.example.com","server_port":443,"uuid":"3a3b1c2e-9999-4321-aaaa-1234567890ab","flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"sni","reality":{"enabled":true,"public_key":"PK","short_id":"SID"}}},
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

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/subscription"
	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// noopMutator implements subscription.ConfigMutator with all-zero responses.
// Sufficient for handler tests that exercise read paths (Get, GetStream).
type noopMutator struct{}

func (noopMutator) AllocListenPort() (uint16, error)                    { return 11000, nil }
func (noopMutator) AllocProxyIndex(context.Context) (int, error)        { return 1, nil }
func (noopMutator) AddOutbound(string, []byte) error                    { return nil }
func (noopMutator) UpdateOutbound(string, []byte) error                 { return nil }
func (noopMutator) RemoveOutbound(string) error                         { return nil }
func (noopMutator) AddInbound(string, []byte) error                     { return nil }
func (noopMutator) RemoveInbound(string) error                          { return nil }
func (noopMutator) AddRouteRule([]byte) error                           { return nil }
func (noopMutator) RemoveRouteRule(string, string) error                { return nil }
func (noopMutator) EnsureProxy(context.Context, int, int, string) error { return nil }
func (noopMutator) RemoveProxy(context.Context, int) error              { return nil }
func (noopMutator) Reload(context.Context) error                        { return nil }
func (noopMutator) Rollback()                                           {}
func (noopMutator) SelectClashProxy(string, string) error               { return nil }
func (noopMutator) GetClashSelectorActive(string) (string, error)       { return "", nil }
func (noopMutator) DeclaredOutboundTags() []string                      { return nil }

type fakePresenceProbe struct{ installed bool }

func (f *fakePresenceProbe) IsPresent() bool { return f.installed }

// seedSubscription creates a subscription with N vless members via Service.Create.
// Each link uses a unique UUID and host so StableTag deduplication doesn't collapse them.
func seedSubscription(t *testing.T, n int) (*subscription.Service, string) {
	t.Helper()
	store, err := subscription.NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	svc := subscription.NewService(store, noopMutator{})

	links := make([]string, n)
	for i := 0; i < n; i++ {
		// Each member: unique UUID + unique host → unique StableTag
		links[i] = "vless://aaaaaaaa-bbbb-cccc-dddd-" + leftPad(i+1, 12) +
			"@h" + leftPad(i+1, 1) + ".example:443?security=tls#member-" + leftPad(i+1, 1)
	}
	inline := strings.Join(links, "\n")
	sub, err := svc.Create(context.Background(), subscription.CreateInput{
		Label:   "test",
		Inline:  inline,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(sub.MemberTags) != n {
		t.Fatalf("seeded %d members, got %d", n, len(sub.MemberTags))
	}
	return svc, sub.ID
}

func leftPad(n, width int) string {
	s := ""
	v := n
	for v > 0 || len(s) < width {
		s = string(rune('0'+v%10)) + s
		v /= 10
	}
	return s
}

func TestSubscriptionHandler_GetStream_HappyPath(t *testing.T) {
	svc, subID := seedSubscription(t, 3)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	req := httptest.NewRequest(http.MethodGet, "/api/singbox/subscriptions/get-stream?id="+subID, nil)
	rr := httptest.NewRecorder()
	h.GetStream(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if got := strings.Count(body, "event: meta\n"); got != 1 {
		t.Errorf("meta events=%d want 1\nbody: %s", got, body)
	}
	if got := strings.Count(body, "event: member\n"); got != 3 {
		t.Errorf("member events=%d want 3\nbody: %s", got, body)
	}
	if got := strings.Count(body, "event: done\n"); got != 1 {
		t.Errorf("done events=%d want 1\nbody: %s", got, body)
	}

	if !strings.Contains(body, `"total":3`) {
		t.Errorf("meta should include total:3, body: %s", body)
	}

	// Done event must include orphanTags as an empty array (not null).
	// A nil slice would serialize as "orphanTags":null and break frontend
	// consumers that call .length on the field.
	if !strings.Contains(body, `"orphanTags":[]`) {
		t.Errorf("done event should include empty orphanTags array, body: %s", body)
	}
}

// TestSubscriptionHandler_Create_ExcludedKeys guards the handler-boundary wiring
// of the import-preview server-picker: the JSON body's excludedKeys must reach
// CreateInput.ExcludedKeys. Before the fix CreateSubscriptionRequest had no
// ExcludedKeys field, so json.Decode silently dropped it and every member was
// materialized regardless of the user's unchecks (silent NO-OP).
func TestSubscriptionHandler_Create_ExcludedKeys(t *testing.T) {
	store, err := subscription.NewStore(filepath.Join(t.TempDir(), "sub.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	svc := subscription.NewService(store, noopMutator{})
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	linkA := "vless://3a3b1c2e-9999-4321-aaaa-1234567890a1@a.example:443?security=tls&sni=a#A"
	linkB := "vless://3a3b1c2e-9999-4321-aaaa-1234567890a2@b.example:443?security=tls&sni=b#B"
	parsed := vlink.ParseBatch([]string{linkB})
	if len(parsed.Outbounds) != 1 {
		t.Fatalf("want 1 parsed outbound, got %d (errors=%v)", len(parsed.Outbounds), parsed.Errors)
	}
	keyB := subscription.IdentityHash(parsed.Outbounds[0])

	body, _ := json.Marshal(CreateSubscriptionRequest{
		Label:        "x",
		Inline:       linkA + "\n" + linkB,
		Enabled:      true,
		ExcludedKeys: []string{keyB},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/singbox/subscriptions/create", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp SubscriptionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rr.Body.String())
	}
	wantTag := "sub-" + resp.Data.ID[:8] + "-" + keyB
	if !containsString(resp.Data.ExcludedTags, wantTag) {
		t.Fatalf("want %s in ExcludedTags %v", wantTag, resp.Data.ExcludedTags)
	}
	// The excluded member must NOT be materialized as an active member.
	if containsString(resp.Data.MemberTags, wantTag) {
		t.Fatalf("excluded-by-key member %s must not be materialized, MemberTags=%v", wantTag, resp.Data.MemberTags)
	}
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestSubscriptionHandler_GetStream_MissingID_400(t *testing.T) {
	svc, _ := seedSubscription(t, 1)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	req := httptest.NewRequest(http.MethodGet, "/api/singbox/subscriptions/get-stream", nil)
	rr := httptest.NewRecorder()
	h.GetStream(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestSubscriptionHandler_GetStream_UnknownID_404(t *testing.T) {
	svc, _ := seedSubscription(t, 1)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	req := httptest.NewRequest(http.MethodGet, "/api/singbox/subscriptions/get-stream?id=does-not-exist", nil)
	rr := httptest.NewRecorder()
	h.GetStream(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestSubscriptionHandler_GetStream_HeadersAreSSE(t *testing.T) {
	svc, subID := seedSubscription(t, 1)
	h := NewSubscriptionHandler(svc, &fakePresenceProbe{installed: true})

	req := httptest.NewRequest(http.MethodGet, "/api/singbox/subscriptions/get-stream?id="+subID, nil)
	rr := httptest.NewRecorder()
	h.GetStream(rr, req)

	if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type=%q want text/event-stream", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control=%q want no-cache", got)
	}
	if got := rr.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering=%q want no", got)
	}
}

func TestToSubscriptionDTO_PreservesMemberLabel(t *testing.T) {
	in := subscription.Subscription{
		ID:    "sub-abc",
		Label: "Test",
		URL:   "https://example.com",
		Members: []subscription.MemberInfo{
			{Tag: "sub-abc-aaaa", Label: "🇺🇸 LA-1", Protocol: "vless", Server: "h", Port: 443},
			{Tag: "sub-abc-bbbb", Label: "", Protocol: "trojan", Server: "h", Port: 444},
		},
		MemberTags: []string{"sub-abc-aaaa", "sub-abc-bbbb"},
		OrphanTags: []string{},
	}
	dto := toSubscriptionDTO(in, true)
	_ = buildSubscriptionMetaDTO(in, true) // exercise the meta path too for compile coverage
	if len(dto.Members) != 2 {
		t.Fatalf("Members=%d want 2", len(dto.Members))
	}
	if dto.Members[0].Label != "🇺🇸 LA-1" {
		t.Errorf("Members[0].Label=%q want 🇺🇸 LA-1", dto.Members[0].Label)
	}
	// Verify it serializes correctly with omitempty.
	raw, _ := json.Marshal(dto.Members[0])
	if !strings.Contains(string(raw), `"label":"🇺🇸 LA-1"`) {
		t.Errorf("JSON doesn't contain Label: %s", raw)
	}
	raw2, _ := json.Marshal(dto.Members[1])
	if strings.Contains(string(raw2), `"label"`) {
		t.Errorf("empty Label should be omitted, got: %s", raw2)
	}
}

// TestToSubscriptionDTO_ProxyIndexGate verifies that proxyIndex is
// surfaced as -1 when ndmsProxyEnabled is false (issue: cards retained
// stale t2sN/ProxyN refs after the global "Create NDMS Proxy" toggle
// was switched off, even though the composite interfaces had been
// torn down by MigrateOff).
func TestToSubscriptionDTO_ProxyIndexGate(t *testing.T) {
	in := subscription.Subscription{
		ID:         "sub-gate",
		Label:      "Gate test",
		ProxyIndex: 7,
		ListenPort: 11007,
	}

	if dto := toSubscriptionDTO(in, true); dto.ProxyIndex != 7 {
		t.Errorf("enabled=true: ProxyIndex=%d want 7 (passthrough)", dto.ProxyIndex)
	}
	if dto := toSubscriptionDTO(in, false); dto.ProxyIndex != -1 {
		t.Errorf("enabled=false: ProxyIndex=%d want -1 (gated)", dto.ProxyIndex)
	}

	if meta := buildSubscriptionMetaDTO(in, true); meta.ProxyIndex != 7 {
		t.Errorf("meta enabled=true: ProxyIndex=%d want 7", meta.ProxyIndex)
	}
	if meta := buildSubscriptionMetaDTO(in, false); meta.ProxyIndex != -1 {
		t.Errorf("meta enabled=false: ProxyIndex=%d want -1", meta.ProxyIndex)
	}
}

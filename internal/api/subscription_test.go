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
func (noopMutator) SelectClashProxy(string, string) error               { return nil }
func (noopMutator) GetClashSelectorActive(string) (string, error)       { return "", nil }

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
	dto := toSubscriptionDTO(in)
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
